package handler

import (
	"encoding/json"
	"net/http"
)

func (h *api) getPage(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := h.pages.GetPage(r.Context(), id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPageResponse(v))
}

func (h *api) pageImage(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	path, err := h.pages.ImageFile(r.Context(), id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

func (h *api) generate(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	st, err := h.pages.Generate(r.Context(), id, n, r.URL.Query().Get("force") == "1")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": string(st)})
}

func (h *api) prefetch(w http.ResponseWriter, r *http.Request) {
	var req prefetchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, invalid("잘못된 요청 본문"))
		return
	}
	n, err := h.pages.Prefetch(r.Context(), r.PathValue("id"), req.From, req.Count)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"queued": n})
}

func isTerminal(name string) bool { return name == "done" || name == "error" }

// stream forwards page events and returns right after the terminal
// done/error event so the connection closes.
func (h *api) stream(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	st, err := h.pages.Stream(r.Context(), id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	if st.Cancel != nil {
		defer st.Cancel()
	}
	sse, err := startSSE(w)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, e := range st.Replay {
		if sse.send(e.Name, e.Data) != nil || isTerminal(e.Name) {
			return
		}
	}
	if st.Final != nil {
		_ = sse.send(st.Final.Name, st.Final.Data)
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-st.Events:
			if !ok {
				return // dropped or reopened: the client reconnects
			}
			if sse.send(e.Name, e.Data) != nil || isTerminal(e.Name) {
				return
			}
		}
	}
}
