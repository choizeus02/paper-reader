package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func (h *api) listThreads(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	threads, err := h.ask.List(r.Context(), id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	if threads == nil {
		threads = []domain.Thread{}
	}
	writeJSON(w, http.StatusOK, threads)
}

func decodeQuestion(r *http.Request) (questionRequest, error) {
	var req questionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, invalid("잘못된 요청 본문")
	}
	if strings.TrimSpace(req.Question) == "" {
		return req, invalid("질문을 입력하세요")
	}
	return req, nil
}

// streamAnswer runs fn, forwarding deltas as SSE, then sends done/error.
// SSE starts lazily: an error before the first delta becomes a normal
// 4xx/5xx JSON response; after that it is sent as an `error` event.
func streamAnswer(ctx context.Context, w http.ResponseWriter, fn func(onDelta func(string)) (domain.Thread, error)) {
	var sse *sseWriter
	var startErr error
	ensure := func() bool {
		if sse != nil {
			return true
		}
		if startErr != nil {
			return false
		}
		sse, startErr = startSSE(w)
		return startErr == nil
	}
	thread, err := fn(func(d string) {
		if ensure() {
			_ = sse.send("delta", map[string]string{"text": d})
		}
	})
	if ctx.Err() != nil {
		return
	}
	if startErr != nil {
		writeError(w, startErr)
		return
	}
	if err != nil {
		if sse == nil {
			if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalid) {
				writeError(w, err)
				return
			}
			if !ensure() {
				writeError(w, startErr)
				return
			}
		}
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	if !ensure() {
		writeError(w, startErr)
		return
	}
	_ = sse.send("done", thread)
}

func (h *api) startThread(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	req, err := decodeQuestion(r)
	if err != nil {
		writeError(w, err)
		return
	}
	streamAnswer(r.Context(), w, func(onDelta func(string)) (domain.Thread, error) {
		return h.ask.Start(r.Context(), id, n, req.Anchor, req.Question, onDelta)
	})
}

func (h *api) continueThread(w http.ResponseWriter, r *http.Request) {
	req, err := decodeQuestion(r)
	if err != nil {
		writeError(w, err)
		return
	}
	streamAnswer(r.Context(), w, func(onDelta func(string)) (domain.Thread, error) {
		return h.ask.Continue(r.Context(), r.PathValue("tid"), req.Question, onDelta)
	})
}
