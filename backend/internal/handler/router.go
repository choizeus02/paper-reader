// Package handler exposes the services over HTTP (JSON + SSE).
package handler

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/service"
)

type PaperAPI interface {
	Upload(ctx context.Context, filename string, r io.Reader) (domain.Paper, error)
	List(ctx context.Context) ([]domain.Paper, error)
	Get(ctx context.Context, id string) (domain.Paper, error)
	Delete(ctx context.Context, id string) error
}

type PageAPI interface {
	GetPage(ctx context.Context, paperID string, pageNo int) (service.PageView, error)
	ImageFile(ctx context.Context, paperID string, pageNo int) (string, error)
	Generate(ctx context.Context, paperID string, pageNo int, force bool) (domain.ResultStatus, error)
	Prefetch(ctx context.Context, paperID string, from, count int) (int, error)
	Stream(ctx context.Context, paperID string, pageNo int) (service.Stream, error)
}

type AskAPI interface {
	List(ctx context.Context, paperID string, pageNo int) ([]domain.Thread, error)
	Start(ctx context.Context, paperID string, pageNo int, anchor domain.Anchor, question string, onDelta func(string)) (domain.Thread, error)
	Continue(ctx context.Context, threadID, question string, onDelta func(string)) (domain.Thread, error)
}

type api struct {
	papers PaperAPI
	pages  PageAPI
	ask    AskAPI
}

// NewRouter wires all routes. static may be nil.
func NewRouter(papers PaperAPI, pages PageAPI, ask AskAPI, static http.Handler) http.Handler {
	h := &api{papers: papers, pages: pages, ask: ask}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/papers", h.listPapers)
	mux.HandleFunc("POST /api/papers", h.uploadPaper)
	mux.HandleFunc("GET /api/papers/{id}", h.getPaper)
	mux.HandleFunc("DELETE /api/papers/{id}", h.deletePaper)
	mux.HandleFunc("GET /api/papers/{id}/pages/{n}", h.getPage)
	mux.HandleFunc("GET /api/papers/{id}/pages/{n}/image", h.pageImage)
	mux.HandleFunc("POST /api/papers/{id}/pages/{n}/generate", h.generate)
	mux.HandleFunc("GET /api/papers/{id}/pages/{n}/stream", h.stream)
	mux.HandleFunc("POST /api/papers/{id}/prefetch", h.prefetch)
	mux.HandleFunc("GET /api/papers/{id}/pages/{n}/threads", h.listThreads)
	mux.HandleFunc("POST /api/papers/{id}/pages/{n}/threads", h.startThread)
	mux.HandleFunc("POST /api/threads/{tid}/messages", h.continueThread)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, domain.ErrNotFound)
	})
	if static != nil {
		mux.Handle("/", static)
	}
	return logRequests(mux)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush keeps SSE working through the logging wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

var (
	_ PaperAPI = (*service.PaperService)(nil)
	_ PageAPI  = (*service.PageService)(nil)
	_ AskAPI   = (*service.AskService)(nil)
)
