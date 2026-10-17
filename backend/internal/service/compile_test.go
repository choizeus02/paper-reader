package service_test

import (
	"github.com/choizeus02/paper-reader/backend/internal/handler"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
	"github.com/choizeus02/paper-reader/backend/internal/pdf"
	"github.com/choizeus02/paper-reader/backend/internal/service"
	"github.com/choizeus02/paper-reader/backend/internal/store"
)

var (
	_ service.PaperRepo    = (*store.Store)(nil)
	_ service.PageRepo     = (*store.Store)(nil)
	_ service.ResultRepo   = (*store.Store)(nil)
	_ service.GlossaryRepo = (*store.Store)(nil)
	_ service.ThreadRepo   = (*store.Store)(nil)
	_ service.PDFProcessor = pdf.Extractor{}
	_ service.LLM          = llm.Claude{}
	_ service.JobQueue     = (*jobs.Queue)(nil)
	_ service.EventHub     = (*jobs.Hub)(nil)
	_ jobs.Handler         = (*service.PageService)(nil).RunJob

	_ handler.PaperAPI = (*service.PaperService)(nil)
	_ handler.PageAPI  = (*service.PageService)(nil)
	_ handler.AskAPI   = (*service.AskService)(nil)
)
