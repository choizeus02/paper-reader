package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
)

const goodOutput = "@@BLOCK b0\n안녕하세요\n@@BLOCK b9\n없는 블록\n@@EXPLANATION\n## 이 페이지 요지\n인사\n@@SUMMARY\n인사하는 페이지\n@@GLOSSARY\nhello | 안녕 | 인사말\n@@END\n"

type pageFixture struct {
	svc   *PageService
	repo  *fakeRepo
	llm   *fakeLLM
	queue *fakeQueue
	hub   *jobs.Hub
}

// newPageFixture seeds paper "p1" with 3 ready pages. Page n has block b0 with
// text "text n" and an image file containing "png-n".
func newPageFixture(t *testing.T, responses ...fakeResponse) *pageFixture {
	t.Helper()
	dir := t.TempDir()
	repo := newFakeRepo()
	_ = repo.CreatePaper(context.Background(), domain.Paper{ID: "p1", Title: "Greeting Paper", Status: domain.PaperProcessing})
	var pages []domain.Page
	for n := 1; n <= 3; n++ {
		rel := filepath.ToSlash(filepath.Join("papers", "p1", "pages", "00"+string(rune('0'+n))+".png"))
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(abs), 0o755)
		_ = os.WriteFile(abs, []byte("png-"+string(rune('0'+n))), 0o644)
		pages = append(pages, domain.Page{PaperID: "p1", PageNo: n, Width: 612, Height: 792, ImagePath: rel,
			Blocks: []domain.Block{{ID: "b0", Text: "text " + string(rune('0'+n))}}})
	}
	_ = repo.SetPaperReady(context.Background(), "p1", pages)
	f := &pageFixture{repo: repo, llm: &fakeLLM{responses: responses}, queue: &fakeQueue{}, hub: jobs.NewHub()}
	f.svc = NewPageService(PageDeps{Papers: repo, Pages: repo, Results: repo, Glossary: repo, LLM: f.llm, Queue: f.queue, Hub: f.hub, DataDir: dir})
	return f
}

func key(n int) jobs.Key { return jobs.Key{PaperID: "p1", PageNo: n} }

// collect subscribes to a page's stream and returns a function that drains it.
func (f *pageFixture) collect(t *testing.T, n int) func() []jobs.Event {
	t.Helper()
	replay, ch, cancel, ok := f.hub.Subscribe(key(n))
	if !ok {
		t.Fatalf("no open stream for page %d", n)
	}
	return func() []jobs.Event {
		defer cancel()
		events := append([]jobs.Event{}, replay...)
		timeout := time.After(time.Second)
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					return events
				}
				events = append(events, e)
			case <-timeout:
				t.Fatal("stream not closed")
			}
		}
	}
}

func names(events []jobs.Event) string {
	var s []string
	for _, e := range events {
		s = append(s, e.Name)
	}
	return strings.Join(s, ",")
}

func TestGetPage(t *testing.T) {
	f := newPageFixture(t)
	v, err := f.svc.GetPage(context.Background(), "p1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if v.PageCount != 3 || v.Page.PageNo != 2 || v.Result != nil {
		t.Fatalf("view wrong: %+v", v)
	}
	if _, err := f.svc.GetPage(context.Background(), "p1", 9); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	path, err := f.svc.ImageFile(context.Background(), "p1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "png-2" {
		t.Fatalf("image path wrong: %s", path)
	}
}

func TestGenerateTwicePromotes(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()
	st, err := f.svc.Generate(ctx, "p1", 1, false)
	if err != nil || st != domain.ResultPending {
		t.Fatalf("status=%v err=%v", st, err)
	}
	if _, _, cancel, ok := f.hub.Subscribe(key(1)); !ok {
		t.Fatal("stream should be opened on enqueue")
	} else {
		cancel()
	}
	if _, err := f.svc.Generate(ctx, "p1", 1, false); err != nil {
		t.Fatal(err)
	}
	if len(f.queue.enqueued) != 1 || f.queue.enqueued[0] != (queued{key(1), jobs.High}) {
		t.Fatalf("enqueued = %+v", f.queue.enqueued)
	}
	if len(f.queue.promoted) != 1 || f.queue.promoted[0] != key(1) {
		t.Fatalf("promoted = %+v", f.queue.promoted)
	}
	if _, err := f.svc.Generate(ctx, "p1", 9, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGenerateDoneNeedsForce(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()
	_, _ = f.repo.MarkPending(ctx, "p1", 1, false)
	_ = f.repo.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 1})
	st, _ := f.svc.Generate(ctx, "p1", 1, false)
	if st != domain.ResultDone || len(f.queue.enqueued) != 0 {
		t.Fatalf("done page must not be requeued: st=%v q=%+v", st, f.queue.enqueued)
	}
	st, _ = f.svc.Generate(ctx, "p1", 1, true)
	if st != domain.ResultPending || len(f.queue.enqueued) != 1 {
		t.Fatalf("force should requeue: st=%v q=%+v", st, f.queue.enqueued)
	}
}

func TestPrefetch(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()
	_, _ = f.svc.Generate(ctx, "p1", 2, false) // already pending
	n, err := f.svc.Prefetch(ctx, "p1", 2, 5)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v; want 1 (page 3 only)", n, err)
	}
	last := f.queue.enqueued[len(f.queue.enqueued)-1]
	if last != (queued{key(3), jobs.Low}) {
		t.Fatalf("last enqueued = %+v", last)
	}
	if _, err := f.svc.Prefetch(ctx, "p1", 0, 1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for from=0, got %v", err)
	}
	if _, err := f.svc.Prefetch(ctx, "nope", 1, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRunJobSuccess(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: goodOutput})
	ctx := context.Background()
	_, _ = f.repo.MarkPending(ctx, "p1", 1, false)
	_ = f.repo.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 1, PageSummary: "첫 페이지 요약"})
	_ = f.repo.AddGlossary(ctx, "p1", []domain.GlossaryTerm{{En: "paper", Ko: "논문"}})
	_, _ = f.svc.Generate(ctx, "p1", 2, false)
	drain := f.collect(t, 2)

	if err := f.svc.RunJob(ctx, key(2)); err != nil {
		t.Fatal(err)
	}
	events := drain()
	if got := names(events); got != "status,block,explanation,explanation,done" {
		t.Fatalf("events = %s", got)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 2)
	if res.Status != domain.ResultDone || res.PromptVersion != 1 || res.PageSummary != "인사하는 페이지" || res.Explanation != "## 이 페이지 요지\n인사" {
		t.Fatalf("result wrong: %+v", res)
	}
	if !reflect.DeepEqual(res.Translation, []domain.BlockTranslation{{BlockID: "b0", Ko: "안녕하세요"}}) {
		t.Fatalf("unknown block ids must be dropped: %+v", res.Translation)
	}
	gl, _ := f.repo.ListGlossary(ctx, "p1")
	if len(gl) != 2 || gl[1].En != "hello" {
		t.Fatalf("glossary not updated: %+v", gl)
	}
	req := f.llm.requests[0]
	if string(req.Image) != "png-2" {
		t.Fatalf("wrong image sent: %q", req.Image)
	}
	for _, want := range []string{"Greeting Paper", "Page 2 of 3", "text 1", "첫 페이지 요약", "paper → 논문", "[b0] text 2"} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if _, _, _, ok := f.hub.Subscribe(key(2)); ok {
		t.Fatal("stream should be closed after the job")
	}
}

func TestRunJobPageWithoutBlocks(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: "@@EXPLANATION\n그림 페이지\n@@SUMMARY\n그림\n@@GLOSSARY\n@@END\n"})
	ctx := context.Background()
	f.repo.mu.Lock()
	pg := f.repo.pages[key(3)]
	pg.Blocks = []domain.Block{}
	f.repo.pages[key(3)] = pg
	f.repo.mu.Unlock()
	_, _ = f.svc.Generate(ctx, "p1", 3, false)
	if err := f.svc.RunJob(ctx, key(3)); err != nil {
		t.Fatal(err)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 3)
	if res.Status != domain.ResultDone || res.Translation == nil || len(res.Translation) != 0 || res.Explanation != "그림 페이지" {
		t.Fatalf("result wrong: %+v", res)
	}
	if !strings.Contains(f.llm.requests[0].Prompt, "no extractable text") {
		t.Fatal("prompt should flag missing text")
	}
}

func TestRunJobRetriesOnceOnBadFormat(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: "@@BLOCK b0\n잘림"}, fakeResponse{text: goodOutput})
	ctx := context.Background()
	_, _ = f.svc.Generate(ctx, "p1", 1, false)
	drain := f.collect(t, 1)
	if err := f.svc.RunJob(ctx, key(1)); err != nil {
		t.Fatal(err)
	}
	if f.llm.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.llm.calls())
	}
	if got := names(drain()); !strings.HasPrefix(got, "status,block,status,block") || !strings.HasSuffix(got, "done") {
		t.Fatalf("events = %s", got)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 1)
	if res.Status != domain.ResultDone {
		t.Fatalf("want done, got %+v", res)
	}
}

func TestRunJobFailsAfterTwoBadFormats(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: "garbage"}, fakeResponse{text: "garbage"})
	ctx := context.Background()
	_, _ = f.svc.Generate(ctx, "p1", 1, false)
	drain := f.collect(t, 1)
	if err := f.svc.RunJob(ctx, key(1)); err == nil {
		t.Fatal("want error")
	}
	events := drain()
	last := events[len(events)-1]
	if last.Name != "error" || f.llm.calls() != 2 {
		t.Fatalf("last=%+v calls=%d", last, f.llm.calls())
	}
	res, _ := f.repo.GetResult(ctx, "p1", 1)
	if res.Status != domain.ResultFailed || !strings.Contains(res.Error, "출력 형식") {
		t.Fatalf("want failed with format error, got %+v", res)
	}
}

func TestRunJobLLMErrorNoRetry(t *testing.T) {
	f := newPageFixture(t, fakeResponse{err: errors.New("claude: Claude AI usage limit reached")})
	ctx := context.Background()
	_, _ = f.svc.Generate(ctx, "p1", 1, false)
	drain := f.collect(t, 1)
	_ = f.svc.RunJob(ctx, key(1))
	events := drain()
	if f.llm.calls() != 1 {
		t.Fatalf("CLI errors must not be retried; calls=%d", f.llm.calls())
	}
	last := events[len(events)-1]
	if last.Name != "error" || !strings.Contains(last.Data.(map[string]string)["message"], "usage limit") {
		t.Fatalf("last event = %+v", last)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 1)
	if res.Status != domain.ResultFailed || !strings.Contains(res.Error, "usage limit") {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunJobTimeoutMessage(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: goodOutput})
	_, _ = f.svc.Generate(context.Background(), "p1", 1, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	_ = f.svc.RunJob(ctx, key(1))
	res, _ := f.repo.GetResult(context.Background(), "p1", 1)
	if res.Status != domain.ResultFailed || res.Error != "시간 초과" {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunJobCanceledMessage(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: goodOutput})
	_, _ = f.svc.Generate(context.Background(), "p1", 1, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = f.svc.RunJob(ctx, key(1))
	res, _ := f.repo.GetResult(context.Background(), "p1", 1)
	if res.Status != domain.ResultFailed || res.Error != "서버 재시작으로 중단됨" {
		t.Fatalf("result = %+v", res)
	}
}

func TestStreamFallsBackToDB(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()

	st, err := f.svc.Stream(ctx, "p1", 1)
	if err != nil || st.Final == nil || st.Final.Name != "error" {
		t.Fatalf("no request yet: %+v %v", st, err)
	}

	_, _ = f.repo.MarkPending(ctx, "p1", 1, false)
	_ = f.repo.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 1})
	st, _ = f.svc.Stream(ctx, "p1", 1)
	if st.Final == nil || st.Final.Name != "done" {
		t.Fatalf("done page: %+v", st)
	}

	_, _ = f.repo.MarkPending(ctx, "p1", 2, false)
	_ = f.repo.MarkFailed(ctx, "p1", 2, "boom")
	st, _ = f.svc.Stream(ctx, "p1", 2)
	if st.Final == nil || st.Final.Name != "error" || st.Final.Data.(map[string]string)["message"] != "boom" {
		t.Fatalf("failed page: %+v", st)
	}

	_, _ = f.svc.Generate(ctx, "p1", 3, false)
	f.hub.Publish(key(3), jobs.Event{Name: "status", Data: map[string]string{"status": "running"}})
	st, _ = f.svc.Stream(ctx, "p1", 3)
	defer st.Cancel()
	if st.Final != nil || st.Events == nil || len(st.Replay) != 1 {
		t.Fatalf("live page: %+v", st)
	}
}

func TestRecover(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()
	_, _ = f.repo.MarkPending(ctx, "p1", 1, false)
	if err := f.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 1)
	if res.Status != domain.ResultFailed || res.Error != "서버 재시작으로 중단됨" {
		t.Fatalf("result = %+v", res)
	}
}

type panicLLM struct{}

func (panicLLM) Run(context.Context, llm.Request, func(string)) (string, error) {
	panic("llm exploded")
}

func TestRunJobPanicClosesStreamAndFailsRow(t *testing.T) {
	f := newPageFixture(t)
	f.svc.d.LLM = panicLLM{}
	ctx := context.Background()
	_, _ = f.svc.Generate(ctx, "p1", 1, false)
	drain := f.collect(t, 1)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("RunJob should re-panic")
			}
		}()
		_ = f.svc.RunJob(ctx, key(1))
	}()

	events := drain() // fails the test if the stream was not closed
	if last := events[len(events)-1]; last.Name != "error" {
		t.Fatalf("last event = %+v", last)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 1)
	if res.Status != domain.ResultFailed {
		t.Fatalf("want failed, got %+v", res)
	}
	if _, _, _, ok := f.hub.Subscribe(key(1)); ok {
		t.Fatal("stream should be closed")
	}
}
