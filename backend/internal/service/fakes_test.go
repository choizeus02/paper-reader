package service

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
)

type fakeRepo struct {
	mu       sync.Mutex
	papers   map[string]domain.Paper
	pages    map[jobs.Key]domain.Page
	results  map[jobs.Key]domain.PageResult
	glossary map[string][]domain.GlossaryTerm
	threads  map[string]domain.Thread
	nextMsg  int64
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		papers:   map[string]domain.Paper{},
		pages:    map[jobs.Key]domain.Page{},
		results:  map[jobs.Key]domain.PageResult{},
		glossary: map[string][]domain.GlossaryTerm{},
		threads:  map[string]domain.Thread{},
	}
}

func (f *fakeRepo) CreatePaper(_ context.Context, p domain.Paper) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.papers[p.ID] = p
	return nil
}

func (f *fakeRepo) SetPaperReady(_ context.Context, id string, pages []domain.Page) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.papers[id]
	if !ok {
		return domain.ErrNotFound
	}
	p.Status, p.PageCount, p.Error = domain.PaperReady, len(pages), ""
	f.papers[id] = p
	for _, pg := range pages {
		f.pages[jobs.Key{PaperID: id, PageNo: pg.PageNo}] = pg
	}
	return nil
}

func (f *fakeRepo) SetPaperFailed(_ context.Context, id, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.papers[id]
	if !ok {
		return domain.ErrNotFound
	}
	p.Status, p.Error = domain.PaperFailed, msg
	f.papers[id] = p
	return nil
}

func (f *fakeRepo) ListPapers(context.Context) ([]domain.Paper, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Paper{}
	for _, p := range f.papers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeRepo) GetPaper(_ context.Context, id string) (domain.Paper, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.papers[id]
	if !ok {
		return domain.Paper{}, domain.ErrNotFound
	}
	return p, nil
}

func (f *fakeRepo) DeletePaper(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.papers[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.papers, id)
	for k := range f.pages {
		if k.PaperID == id {
			delete(f.pages, k)
			delete(f.results, k)
		}
	}
	return nil
}

func (f *fakeRepo) GetPage(_ context.Context, paperID string, pageNo int) (domain.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pg, ok := f.pages[jobs.Key{PaperID: paperID, PageNo: pageNo}]
	if !ok {
		return domain.Page{}, domain.ErrNotFound
	}
	return pg, nil
}

func (f *fakeRepo) GetResult(_ context.Context, paperID string, pageNo int) (domain.PageResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.results[jobs.Key{PaperID: paperID, PageNo: pageNo}]
	if !ok {
		return domain.PageResult{}, domain.ErrNotFound
	}
	return r, nil
}

func (f *fakeRepo) MarkPending(_ context.Context, paperID string, pageNo int, force bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := jobs.Key{PaperID: paperID, PageNo: pageNo}
	r, ok := f.results[k]
	if ok && !(r.Status == domain.ResultFailed || (force && r.Status == domain.ResultDone)) {
		return false, nil
	}
	if !ok {
		r = domain.PageResult{PaperID: paperID, PageNo: pageNo, Translation: []domain.BlockTranslation{}}
	}
	r.Status, r.Error = domain.ResultPending, ""
	f.results[k] = r
	return true, nil
}

func (f *fakeRepo) setStatus(paperID string, pageNo int, s domain.ResultStatus, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := jobs.Key{PaperID: paperID, PageNo: pageNo}
	r, ok := f.results[k]
	if !ok {
		return domain.ErrNotFound
	}
	r.Status, r.Error = s, msg
	f.results[k] = r
	return nil
}

func (f *fakeRepo) MarkRunning(_ context.Context, paperID string, pageNo int) error {
	return f.setStatus(paperID, pageNo, domain.ResultRunning, "")
}

func (f *fakeRepo) MarkFailed(_ context.Context, paperID string, pageNo int, msg string) error {
	return f.setStatus(paperID, pageNo, domain.ResultFailed, msg)
}

func (f *fakeRepo) SaveDone(_ context.Context, r domain.PageResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := jobs.Key{PaperID: r.PaperID, PageNo: r.PageNo}
	if _, ok := f.results[k]; !ok {
		return domain.ErrNotFound
	}
	r.Status, r.Error = domain.ResultDone, ""
	f.results[k] = r
	return nil
}

func (f *fakeRepo) FailUnfinished(_ context.Context, msg string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for k, r := range f.results {
		if r.Status == domain.ResultPending || r.Status == domain.ResultRunning {
			r.Status, r.Error = domain.ResultFailed, msg
			f.results[k] = r
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) ListGlossary(_ context.Context, paperID string) ([]domain.GlossaryTerm, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.GlossaryTerm{}, f.glossary[paperID]...), nil
}

func (f *fakeRepo) AddGlossary(_ context.Context, paperID string, terms []domain.GlossaryTerm) error {
	f.mu.Lock()
	defer f.mu.Unlock()
outer:
	for _, t := range terms {
		for _, e := range f.glossary[paperID] {
			if e.En == t.En {
				continue outer
			}
		}
		f.glossary[paperID] = append(f.glossary[paperID], t)
	}
	return nil
}

func (f *fakeRepo) CreateThread(_ context.Context, t domain.Thread) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Messages = []domain.Message{}
	f.threads[t.ID] = t
	return nil
}

func (f *fakeRepo) GetThread(_ context.Context, id string) (domain.Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.threads[id]
	if !ok {
		return domain.Thread{}, domain.ErrNotFound
	}
	t.Messages = append([]domain.Message{}, t.Messages...)
	return t, nil
}

func (f *fakeRepo) ListThreads(_ context.Context, paperID string, pageNo int) ([]domain.Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Thread{}
	for _, t := range f.threads {
		if t.PaperID == paperID && t.PageNo == pageNo {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeRepo) AddMessage(_ context.Context, m domain.Message) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.threads[m.ThreadID]
	if !ok {
		return 0, domain.ErrNotFound
	}
	f.nextMsg++
	m.ID = f.nextMsg
	t.Messages = append(t.Messages, m)
	f.threads[m.ThreadID] = t
	return m.ID, nil
}

func (f *fakeRepo) FinishMessage(_ context.Context, id int64, content string, status domain.MessageStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for tid, t := range f.threads {
		for i, m := range t.Messages {
			if m.ID == id {
				t.Messages[i].Content, t.Messages[i].Status = content, status
				f.threads[tid] = t
				return nil
			}
		}
	}
	return domain.ErrNotFound
}

type fakePDF struct {
	pages []domain.Page
	err   error
}

func (f *fakePDF) Process(context.Context, string, string) ([]domain.Page, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domain.Page(nil), f.pages...), nil
}

type fakeResponse struct {
	text string
	err  error
}

// fakeLLM replays scripted responses in order, streaming them in 7-byte chunks.
type fakeLLM struct {
	mu        sync.Mutex
	responses []fakeResponse
	requests  []llm.Request
}

func (f *fakeLLM) Run(ctx context.Context, req llm.Request, onDelta func(string)) (string, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	i := len(f.requests) - 1
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if i >= len(f.responses) {
		return "", errors.New("fakeLLM: no response scripted")
	}
	r := f.responses[i]
	if r.err != nil {
		return "", r.err
	}
	for start := 0; start < len(r.text); start += 7 {
		end := start + 7
		if end > len(r.text) {
			end = len(r.text)
		}
		if onDelta != nil {
			onDelta(r.text[start:end])
		}
	}
	return r.text, nil
}

func (f *fakeLLM) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

type queued struct {
	key jobs.Key
	pri jobs.Priority
}

type fakeQueue struct {
	mu       sync.Mutex
	enqueued []queued
	promoted []jobs.Key
}

func (q *fakeQueue) Enqueue(k jobs.Key, p jobs.Priority) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.enqueued = append(q.enqueued, queued{k, p})
}

func (q *fakeQueue) Promote(k jobs.Key) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.promoted = append(q.promoted, k)
}
