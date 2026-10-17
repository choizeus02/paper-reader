// Package service holds the application's business logic. It defines the
// interfaces it needs; store, pdf, llm and jobs provide implementations.
package service

import (
	"context"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
)

type PaperRepo interface {
	CreatePaper(ctx context.Context, p domain.Paper) error
	SetPaperReady(ctx context.Context, id string, pages []domain.Page) error
	SetPaperFailed(ctx context.Context, id, msg string) error
	ListPapers(ctx context.Context) ([]domain.Paper, error)
	GetPaper(ctx context.Context, id string) (domain.Paper, error)
	DeletePaper(ctx context.Context, id string) error
}

type PageRepo interface {
	GetPage(ctx context.Context, paperID string, pageNo int) (domain.Page, error)
}

type ResultRepo interface {
	GetResult(ctx context.Context, paperID string, pageNo int) (domain.PageResult, error)
	MarkPending(ctx context.Context, paperID string, pageNo int, force bool) (bool, error)
	MarkRunning(ctx context.Context, paperID string, pageNo int) error
	SaveDone(ctx context.Context, r domain.PageResult) error
	MarkFailed(ctx context.Context, paperID string, pageNo int, msg string) error
	FailUnfinished(ctx context.Context, msg string) (int64, error)
}

type GlossaryRepo interface {
	ListGlossary(ctx context.Context, paperID string) ([]domain.GlossaryTerm, error)
	AddGlossary(ctx context.Context, paperID string, terms []domain.GlossaryTerm) error
}

type ThreadRepo interface {
	CreateThread(ctx context.Context, t domain.Thread) error
	GetThread(ctx context.Context, id string) (domain.Thread, error)
	ListThreads(ctx context.Context, paperID string, pageNo int) ([]domain.Thread, error)
	AddMessage(ctx context.Context, m domain.Message) (int64, error)
	FinishMessage(ctx context.Context, id int64, content string, status domain.MessageStatus) error
}

type PDFProcessor interface {
	Process(ctx context.Context, pdfPath, outDir string) ([]domain.Page, error)
}

type LLM interface {
	Run(ctx context.Context, req llm.Request, onDelta func(string)) (string, error)
}

type JobQueue interface {
	Enqueue(k jobs.Key, p jobs.Priority)
	Promote(k jobs.Key)
}

type EventHub interface {
	Open(k jobs.Key)
	Publish(k jobs.Key, e jobs.Event)
	Close(k jobs.Key)
	Subscribe(k jobs.Key) ([]jobs.Event, <-chan jobs.Event, func(), bool)
}
