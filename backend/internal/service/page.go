package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
)

const (
	maxPrefetch   = 10
	abstractChars = 1500
	restartMsg    = "서버 재시작으로 중단됨"
)

type PageDeps struct {
	Papers   PaperRepo
	Pages    PageRepo
	Results  ResultRepo
	Glossary GlossaryRepo
	LLM      LLM
	Queue    JobQueue
	Hub      EventHub
	DataDir  string
}

type PageService struct{ d PageDeps }

func NewPageService(d PageDeps) *PageService { return &PageService{d: d} }

type PageView struct {
	Page      domain.Page
	PageCount int
	Result    *domain.PageResult
}

type Stream struct {
	Replay []jobs.Event
	Events <-chan jobs.Event
	Cancel func()
	Final  *jobs.Event
}

func msgEvent(name, msg string) *jobs.Event {
	return &jobs.Event{Name: name, Data: map[string]string{"message": msg}}
}

func (s *PageService) GetPage(ctx context.Context, paperID string, pageNo int) (PageView, error) {
	paper, err := s.d.Papers.GetPaper(ctx, paperID)
	if err != nil {
		return PageView{}, err
	}
	page, err := s.d.Pages.GetPage(ctx, paperID, pageNo)
	if err != nil {
		return PageView{}, err
	}
	v := PageView{Page: page, PageCount: paper.PageCount}
	res, err := s.d.Results.GetResult(ctx, paperID, pageNo)
	switch {
	case err == nil:
		v.Result = &res
	case !errors.Is(err, domain.ErrNotFound):
		return PageView{}, err
	}
	return v, nil
}

func (s *PageService) ImageFile(ctx context.Context, paperID string, pageNo int) (string, error) {
	page, err := s.d.Pages.GetPage(ctx, paperID, pageNo)
	if err != nil {
		return "", err
	}
	return s.imagePath(page), nil
}

func (s *PageService) imagePath(p domain.Page) string {
	return filepath.Join(s.d.DataDir, filepath.FromSlash(p.ImagePath))
}

// enqueue opens the event stream and queues the page job.
func (s *PageService) enqueue(k jobs.Key, p jobs.Priority) {
	s.d.Hub.Open(k)
	s.d.Queue.Enqueue(k, p)
}

// markAndEnqueue marks the page pending and, if that changed anything,
// queues it. It reports whether the page was newly queued.
func (s *PageService) markAndEnqueue(ctx context.Context, k jobs.Key, force bool, p jobs.Priority) (bool, error) {
	changed, err := s.d.Results.MarkPending(ctx, k.PaperID, k.PageNo, force)
	if err != nil || !changed {
		return false, err
	}
	s.enqueue(k, p)
	return true, nil
}

// Generate queues a page at high priority (or promotes it if prefetch queued it).
func (s *PageService) Generate(ctx context.Context, paperID string, pageNo int, force bool) (domain.ResultStatus, error) {
	if _, err := s.d.Pages.GetPage(ctx, paperID, pageNo); err != nil {
		return "", err
	}
	k := jobs.Key{PaperID: paperID, PageNo: pageNo}
	queued, err := s.markAndEnqueue(ctx, k, force, jobs.High)
	if err != nil {
		return "", err
	}
	if queued {
		return domain.ResultPending, nil
	}
	s.d.Queue.Promote(k)
	res, err := s.d.Results.GetResult(ctx, paperID, pageNo)
	if err != nil {
		return "", err
	}
	return res.Status, nil
}

func (s *PageService) Prefetch(ctx context.Context, paperID string, from, count int) (int, error) {
	if from < 1 {
		return 0, fmt.Errorf("%w: 시작 페이지는 1 이상이어야 합니다", domain.ErrInvalid)
	}
	paper, err := s.d.Papers.GetPaper(ctx, paperID)
	if err != nil {
		return 0, err
	}
	count = min(max(count, 1), maxPrefetch)
	n := 0
	for p := from; p < from+count && p <= paper.PageCount; p++ {
		queued, err := s.markAndEnqueue(ctx, jobs.Key{PaperID: paperID, PageNo: p}, false, jobs.Low)
		if err != nil {
			return n, err
		}
		if queued {
			n++
		}
	}
	return n, nil
}

func (s *PageService) Stream(ctx context.Context, paperID string, pageNo int) (Stream, error) {
	k := jobs.Key{PaperID: paperID, PageNo: pageNo}
	if replay, ch, cancel, ok := s.d.Hub.Subscribe(k); ok {
		return Stream{Replay: replay, Events: ch, Cancel: cancel}, nil
	}
	none := func() {}
	res, err := s.d.Results.GetResult(ctx, paperID, pageNo)
	if errors.Is(err, domain.ErrNotFound) {
		return Stream{Cancel: none, Final: msgEvent("error", "생성 요청이 없습니다")}, nil
	}
	if err != nil {
		return Stream{}, err
	}
	switch res.Status {
	case domain.ResultDone:
		return Stream{Cancel: none, Final: &jobs.Event{Name: "done", Data: struct{}{}}}, nil
	case domain.ResultFailed:
		return Stream{Cancel: none, Final: msgEvent("error", res.Error)}, nil
	default:
		return Stream{Cancel: none, Final: msgEvent("error", "진행 중인 작업을 찾을 수 없습니다. 다시 시도하세요")}, nil
	}
}

func (s *PageService) Recover(ctx context.Context) error {
	n, err := s.d.Results.FailUnfinished(ctx, restartMsg)
	if n > 0 {
		log.Printf("marked %d unfinished page jobs as failed", n)
	}
	return err
}

// RunJob is the jobs.Handler for page generation. On every exit path,
// including a panic, the page row ends done or failed and the stream is closed.
func (s *PageService) RunJob(ctx context.Context, k jobs.Key) (err error) {
	defer s.d.Hub.Close(k)
	defer func() {
		if r := recover(); r != nil {
			s.fail(k, "내부 오류")
			panic(r)
		}
	}()
	if err = s.runJob(ctx, k); err == nil {
		return nil
	}
	var msg string
	var shown *shownError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		msg = "시간 초과"
	case errors.Is(err, context.Canceled):
		msg = restartMsg // server is shutting down
	case errors.As(err, &shown):
		msg = err.Error()
	default:
		log.Printf("page %s/%d: internal error: %v", k.PaperID, k.PageNo, err)
		msg = "내부 오류가 발생했습니다"
	}
	s.fail(k, msg)
	return err
}

// shownError marks errors whose text may be shown to the user as-is
// (LLM CLI failures and output-format errors); all others are internal.
type shownError struct{ err error }

func (e *shownError) Error() string { return e.err.Error() }
func (e *shownError) Unwrap() error { return e.err }

// fail records the failure and tells subscribers. The caller closes the stream.
func (s *PageService) fail(k jobs.Key, msg string) {
	if ferr := s.d.Results.MarkFailed(context.Background(), k.PaperID, k.PageNo, msg); ferr != nil {
		log.Printf("page %s/%d: mark failed: %v", k.PaperID, k.PageNo, ferr)
	}
	s.d.Hub.Publish(k, *msgEvent("error", msg))
}

func (s *PageService) runJob(ctx context.Context, k jobs.Key) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.d.Results.MarkRunning(ctx, k.PaperID, k.PageNo); err != nil {
		return err
	}
	s.d.Hub.Publish(k, jobs.Event{Name: "status", Data: map[string]string{"status": "running"}})

	req, page, err := s.buildPageRequest(ctx, k)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(page.Blocks))
	for _, b := range page.Blocks {
		known[b.ID] = true
	}

	var out llm.PageOutput
	for attempt := 1; ; attempt++ {
		parser := llm.NewMarkerParser(
			func(bt domain.BlockTranslation) {
				if known[bt.BlockID] {
					s.d.Hub.Publish(k, jobs.Event{Name: "block", Data: bt})
				}
			},
			func(delta string) {
				s.d.Hub.Publish(k, jobs.Event{Name: "explanation", Data: map[string]string{"delta": delta}})
			},
		)
		if _, err := s.d.LLM.Run(ctx, req, parser.Write); err != nil {
			return &shownError{err}
		}
		var perr error
		out, perr = parser.Finish()
		if perr == nil {
			break
		}
		if attempt >= 2 {
			return &shownError{fmt.Errorf("출력 형식 오류: %w", perr)}
		}
		s.d.Hub.Publish(k, jobs.Event{Name: "status", Data: map[string]string{"status": "retrying"}})
	}

	translation := make([]domain.BlockTranslation, 0, len(out.Translation))
	for _, bt := range out.Translation {
		if known[bt.BlockID] {
			translation = append(translation, bt)
		}
	}
	res := domain.PageResult{
		PaperID: k.PaperID, PageNo: k.PageNo, Status: domain.ResultDone,
		Translation: translation, Explanation: out.Explanation, PageSummary: out.Summary,
		PromptVersion: llm.PagePromptVersion,
	}
	// Glossary first so its terms are visible no later than the done state.
	if len(out.Glossary) > 0 {
		if err := s.d.Glossary.AddGlossary(ctx, k.PaperID, out.Glossary); err != nil {
			log.Printf("page %s/%d: glossary: %v", k.PaperID, k.PageNo, err)
		}
	}
	if err := s.d.Results.SaveDone(ctx, res); err != nil {
		return err
	}
	s.d.Hub.Publish(k, jobs.Event{Name: "done", Data: struct{}{}})
	return nil
}

func (s *PageService) buildPageRequest(ctx context.Context, k jobs.Key) (llm.Request, domain.Page, error) {
	paper, err := s.d.Papers.GetPaper(ctx, k.PaperID)
	if err != nil {
		return llm.Request{}, domain.Page{}, err
	}
	page, err := s.d.Pages.GetPage(ctx, k.PaperID, k.PageNo)
	if err != nil {
		return llm.Request{}, domain.Page{}, err
	}
	img, err := os.ReadFile(s.imagePath(page))
	if err != nil {
		return llm.Request{}, domain.Page{}, fmt.Errorf("페이지 이미지를 읽을 수 없습니다: %w", err)
	}
	pc := llm.PageContext{Title: paper.Title, PageNo: page.PageNo, PageCount: paper.PageCount, Blocks: page.Blocks}
	if page.PageNo > 1 {
		if first, err := s.d.Pages.GetPage(ctx, k.PaperID, 1); err == nil {
			pc.Abstract = abstractOf(first.Blocks)
		}
		if prev, err := s.d.Results.GetResult(ctx, k.PaperID, page.PageNo-1); err == nil && prev.Status == domain.ResultDone {
			pc.PrevSummary = prev.PageSummary
		}
	}
	if pc.Glossary, err = s.d.Glossary.ListGlossary(ctx, k.PaperID); err != nil {
		return llm.Request{}, domain.Page{}, err
	}
	prompt, err := llm.BuildPagePrompt(pc)
	if err != nil {
		return llm.Request{}, domain.Page{}, err
	}
	return llm.Request{System: llm.PageSystem, Image: img, Prompt: prompt}, page, nil
}

// abstractOf returns roughly the first abstractChars characters of page 1.
func abstractOf(blocks []domain.Block) string {
	var sb strings.Builder
	for _, b := range blocks {
		if sb.Len() >= abstractChars {
			break
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(b.Text)
	}
	r := []rune(sb.String())
	if len(r) > abstractChars {
		r = r[:abstractChars]
	}
	return string(r)
}
