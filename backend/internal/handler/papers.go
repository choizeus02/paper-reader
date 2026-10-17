package handler

import "net/http"

const maxUpload = 200 << 20

func (h *api) listPapers(w http.ResponseWriter, r *http.Request) {
	papers, err := h.papers.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, papers)
}

func (h *api) uploadPaper(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, invalid("file 필드에 PDF를 담아 보내세요"))
		return
	}
	defer file.Close()
	p, err := h.papers.Upload(r.Context(), hdr.Filename, file)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (h *api) getPaper(w http.ResponseWriter, r *http.Request) {
	p, err := h.papers.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *api) deletePaper(w http.ResponseWriter, r *http.Request) {
	if err := h.papers.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
