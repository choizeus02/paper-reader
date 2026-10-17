package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/service"
)

type stubPapers struct {
	uploadedName, uploadedBody string
	uploadErr                  error
}

func (s *stubPapers) Upload(_ context.Context, name string, r io.Reader) (domain.Paper, error) {
	b, _ := io.ReadAll(r)
	s.uploadedName, s.uploadedBody = name, string(b)
	if s.uploadErr != nil {
		return domain.Paper{}, s.uploadErr
	}
	return domain.Paper{ID: "p1", Title: "x", Status: domain.PaperProcessing}, nil
}
func (s *stubPapers) List(context.Context) ([]domain.Paper, error) {
	return []domain.Paper{{ID: "p1", Title: "x", Status: domain.PaperReady, PageCount: 2, DonePages: 1}}, nil
}
func (s *stubPapers) Get(_ context.Context, id string) (domain.Paper, error) {
	if id != "p1" {
		return domain.Paper{}, domain.ErrNotFound
	}
	return domain.Paper{ID: "p1"}, nil
}
func (s *stubPapers) Delete(_ context.Context, id string) error {
	if id != "p1" {
		return domain.ErrNotFound
	}
	return nil
}

type stubPages struct {
	image    string
	stream   service.Stream
	gotForce bool
	gotFrom  int
	gotCount int
}

func (s *stubPages) GetPage(_ context.Context, id string, n int) (service.PageView, error) {
	if n != 1 {
		return service.PageView{}, domain.ErrNotFound
	}
	return service.PageView{
		Page:      domain.Page{PaperID: id, PageNo: 1, Width: 612, Height: 792, Blocks: []domain.Block{{ID: "b0", Text: "Hi"}}},
		PageCount: 3,
	}, nil
}
func (s *stubPages) ImageFile(context.Context, string, int) (string, error) { return s.image, nil }
func (s *stubPages) Generate(_ context.Context, _ string, _ int, force bool) (domain.ResultStatus, error) {
	s.gotForce = force
	return domain.ResultPending, nil
}
func (s *stubPages) Prefetch(_ context.Context, _ string, from, count int) (int, error) {
	s.gotFrom, s.gotCount = from, count
	return 2, nil
}
func (s *stubPages) Stream(context.Context, string, int) (service.Stream, error) {
	return s.stream, nil
}

type stubAsk struct{ err, midErr error }

func (s *stubAsk) List(context.Context, string, int) ([]domain.Thread, error) { return nil, nil }
func (s *stubAsk) Start(_ context.Context, _ string, _ int, a domain.Anchor, q string, onDelta func(string)) (domain.Thread, error) {
	if s.err != nil {
		return domain.Thread{}, s.err
	}
	onDelta("답")
	onDelta("변")
	if s.midErr != nil {
		return domain.Thread{}, s.midErr
	}
	return domain.Thread{ID: "t1", Anchor: a, Messages: []domain.Message{{Role: domain.RoleUser, Content: q}, {Role: domain.RoleAssistant, Content: "답변"}}}, nil
}
func (s *stubAsk) Continue(_ context.Context, tid, q string, onDelta func(string)) (domain.Thread, error) {
	if tid != "t1" {
		return domain.Thread{}, domain.ErrNotFound
	}
	onDelta("더")
	return domain.Thread{ID: "t1"}, nil
}

func newTestRouter() (http.Handler, *stubPapers, *stubPages, *stubAsk) {
	p, pg, a := &stubPapers{}, &stubPages{}, &stubAsk{}
	return NewRouter(p, pg, a, nil), p, pg, a
}

func do(h http.Handler, method, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListPapers(t *testing.T) {
	h, _, _, _ := newTestRouter()
	rec := do(h, "GET", "/api/papers", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"done_pages":1`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func multipartBody(t *testing.T, field, name, content string) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(field, name)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(content))
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestUpload(t *testing.T) {
	h, papers, _, _ := newTestRouter()
	body, ct := multipartBody(t, "file", "paper.pdf", "%PDF-1.4 x")
	rec := do(h, "POST", "/api/papers", body, ct)
	if rec.Code != http.StatusCreated || papers.uploadedName != "paper.pdf" || papers.uploadedBody != "%PDF-1.4 x" {
		t.Fatalf("%d %s name=%q body=%q", rec.Code, rec.Body, papers.uploadedName, papers.uploadedBody)
	}
}

func TestUploadBadRequest(t *testing.T) {
	h, papers, _, _ := newTestRouter()
	body, ct := multipartBody(t, "wrong", "paper.pdf", "x")
	if rec := do(h, "POST", "/api/papers", body, ct); rec.Code != 400 {
		t.Fatalf("missing file field: %d", rec.Code)
	}
	papers.uploadErr = errors.Join(domain.ErrInvalid, errors.New("PDF 형식이 아닙니다"))
	body, ct = multipartBody(t, "file", "a.pdf", "nope")
	rec := do(h, "POST", "/api/papers", body, ct)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "PDF 형식이 아닙니다") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestNotFoundAndBadPage(t *testing.T) {
	h, _, _, _ := newTestRouter()
	for path, want := range map[string]int{
		"/api/papers/zzz":          404,
		"/api/papers/p1/pages/2":   404,
		"/api/papers/p1/pages/abc": 400,
		"/api/papers/p1/pages/0":   400,
		"/api/nothing":             404,
	} {
		if rec := do(h, "GET", path, nil, ""); rec.Code != want {
			t.Errorf("%s: got %d want %d (%s)", path, rec.Code, want, rec.Body)
		}
	}
	if rec := do(h, "DELETE", "/api/papers/zzz", nil, ""); rec.Code != 404 {
		t.Errorf("delete missing: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/papers/p1", nil, ""); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
}

func TestGetPage(t *testing.T) {
	h, _, _, _ := newTestRouter()
	rec := do(h, "GET", "/api/papers/p1/pages/1", nil, "")
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["image_url"] != "/api/papers/p1/pages/1/image" || got["page_count"].(float64) != 3 || got["result"] != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPageImage(t *testing.T) {
	h, _, pages, _ := newTestRouter()
	pages.image = filepath.Join(t.TempDir(), "001.png")
	os.WriteFile(pages.image, []byte("\x89PNG\r\n\x1a\nrest"), 0o644)
	rec := do(h, "GET", "/api/papers/p1/pages/1/image", nil, "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestGenerateAndPrefetch(t *testing.T) {
	h, _, pages, _ := newTestRouter()
	rec := do(h, "POST", "/api/papers/p1/pages/1/generate?force=1", nil, "")
	if rec.Code != 202 || !pages.gotForce || !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("%d %s force=%v", rec.Code, rec.Body, pages.gotForce)
	}
	rec = do(h, "POST", "/api/papers/p1/prefetch", strings.NewReader(`{"from":2,"count":3}`), "application/json")
	if rec.Code != 202 || pages.gotFrom != 2 || pages.gotCount != 3 || !strings.Contains(rec.Body.String(), `"queued":2`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/papers/p1/prefetch", strings.NewReader(`{`), "application/json"); rec.Code != 400 {
		t.Fatalf("bad json: %d", rec.Code)
	}
}

func TestStreamLive(t *testing.T) {
	h, _, pages, _ := newTestRouter()
	ch := make(chan jobs.Event, 4)
	ch <- jobs.Event{Name: "block", Data: domain.BlockTranslation{BlockID: "b0", Ko: "안녕"}}
	ch <- jobs.Event{Name: "done", Data: struct{}{}}
	ch <- jobs.Event{Name: "never", Data: 1}
	pages.stream = service.Stream{Replay: []jobs.Event{{Name: "status", Data: map[string]string{"status": "running"}}}, Events: ch, Cancel: func() {}}
	rec := do(h, "GET", "/api/papers/p1/pages/1/stream", nil, "")
	want := "event: status\ndata: {\"status\":\"running\"}\n\n" +
		"event: block\ndata: {\"block_id\":\"b0\",\"ko\":\"안녕\"}\n\n" +
		"event: done\ndata: {}\n\n"
	if rec.Header().Get("Content-Type") != "text/event-stream" || rec.Body.String() != want {
		t.Fatalf("got %q", rec.Body.String())
	}
}

func TestStreamFinal(t *testing.T) {
	h, _, pages, _ := newTestRouter()
	pages.stream = service.Stream{Cancel: func() {}, Final: &jobs.Event{Name: "done", Data: struct{}{}}}
	rec := do(h, "GET", "/api/papers/p1/pages/1/stream", nil, "")
	if rec.Body.String() != "event: done\ndata: {}\n\n" {
		t.Fatalf("got %q", rec.Body.String())
	}
}

func TestStreamStopsOnTerminalReplay(t *testing.T) {
	h, _, pages, _ := newTestRouter()
	pages.stream = service.Stream{Replay: []jobs.Event{{Name: "error", Data: map[string]string{"message": "x"}}}, Events: make(chan jobs.Event), Cancel: func() {}}
	rec := do(h, "GET", "/api/papers/p1/pages/1/stream", nil, "") // would hang if it waited on Events
	if !strings.HasPrefix(rec.Body.String(), "event: error") {
		t.Fatalf("got %q", rec.Body.String())
	}
}

func TestAskSSE(t *testing.T) {
	h, _, _, ask := newTestRouter()
	rec := do(h, "POST", "/api/papers/p1/pages/1/threads", strings.NewReader(`{"anchor":{"block_ids":["b0"],"quote":"Hi"},"question":"뭐야?"}`), "application/json")
	body := rec.Body.String()
	if !strings.Contains(body, "event: delta\ndata: {\"text\":\"답\"}\n\n") || !strings.Contains(body, "event: done\ndata: {\"id\":\"t1\"") {
		t.Fatalf("got %q", body)
	}
	if rec := do(h, "POST", "/api/papers/p1/pages/1/threads", strings.NewReader(`{"question":"  "}`), "application/json"); rec.Code != 400 {
		t.Fatalf("empty question: %d", rec.Code)
	}
	ask.err = errors.New("claude: usage limit")
	rec = do(h, "POST", "/api/papers/p1/pages/1/threads", strings.NewReader(`{"question":"q"}`), "application/json")
	if !strings.Contains(rec.Body.String(), "event: error\ndata: {\"message\":\"claude: usage limit\"}") {
		t.Fatalf("got %q", rec.Body.String())
	}
	ask.err = nil
	ask.midErr = errors.New("claude: mid")
	rec = do(h, "POST", "/api/papers/p1/pages/1/threads", strings.NewReader(`{"question":"q"}`), "application/json")
	if !strings.Contains(rec.Body.String(), "event: delta") || !strings.Contains(rec.Body.String(), "event: error\ndata: {\"message\":\"claude: mid\"}") {
		t.Fatalf("mid: %q", rec.Body.String())
	}
	ask.midErr = nil
	rec = do(h, "POST", "/api/threads/t1/messages", strings.NewReader(`{"question":"더"}`), "application/json")
	if !strings.Contains(rec.Body.String(), "event: delta") {
		t.Fatalf("continue: %q", rec.Body.String())
	}
}

func TestListThreadsNeverNull(t *testing.T) {
	h, _, _, _ := newTestRouter()
	rec := do(h, "GET", "/api/papers/p1/pages/1/threads", nil, "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("got %q", rec.Body.String())
	}
}
func TestAskPreStreamErrors(t *testing.T) {
	h, _, _, ask := newTestRouter()
	rec := do(h, "POST", "/api/threads/zzz/messages", strings.NewReader(`{"question":"더"}`), "application/json")
	if rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || !strings.Contains(rec.Body.String(), "찾을 수 없음") {
		t.Fatalf("missing thread: %d %q", rec.Code, rec.Body.String())
	}
	if rec := do(h, "POST", "/api/threads/t1/messages", strings.NewReader(`{"question":""}`), "application/json"); rec.Code != 400 {
		t.Fatalf("empty question: %d", rec.Code)
	}
	ask.err = errors.Join(domain.ErrInvalid, errors.New("bad"))
	if rec := do(h, "POST", "/api/papers/p1/pages/1/threads", strings.NewReader(`{"question":"q"}`), "application/json"); rec.Code != 400 {
		t.Fatalf("start invalid: %d", rec.Code)
	}
}

func TestAPICatchAllKorean(t *testing.T) {
	h, _, _, _ := newTestRouter()
	rec := do(h, "GET", "/api/nothing", nil, "")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "찾을 수 없음") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

type nilPapers struct{ stubPapers }

func (nilPapers) List(context.Context) ([]domain.Paper, error) { return nil, nil }

func TestListPapersNeverNull(t *testing.T) {
	h := NewRouter(&nilPapers{}, &stubPages{}, &stubAsk{}, nil)
	rec := do(h, "GET", "/api/papers", nil, "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("got %q", rec.Body.String())
	}
}

func TestUploadTooLarge(t *testing.T) {
	old := maxUpload
	maxUpload = 100
	defer func() { maxUpload = old }()
	h, _, _, _ := newTestRouter()
	body, ct := multipartBody(t, "file", "big.pdf", strings.Repeat("x", 1000))
	rec := do(h, "POST", "/api/papers", body, ct)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "파일이 너무 큽니다") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestWriteErrorHidesInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, errors.New("pq: secret detail"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") || !strings.Contains(rec.Body.String(), "서버 오류가 발생했습니다") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
