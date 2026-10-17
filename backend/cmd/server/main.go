package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/choizeus02/paper-reader/backend/internal/config"
	"github.com/choizeus02/paper-reader/backend/internal/handler"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
	"github.com/choizeus02/paper-reader/backend/internal/pdf"
	"github.com/choizeus02/paper-reader/backend/internal/service"
	"github.com/choizeus02/paper-reader/backend/internal/store"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("DATA_DIR: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL, "")
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	workDir, err := os.MkdirTemp("", "paper-reader-claude-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	runner := llm.Claude{Bin: cfg.ClaudeBin, Model: cfg.ClaudeModel, WorkDir: workDir}
	hub := jobs.NewHub()
	queue := jobs.NewQueue(cfg.Workers, cfg.JobTimeout)

	papers := service.NewPaperService(st, pdf.Extractor{Bin: cfg.MutoolBin, DPI: cfg.RenderDPI}, cfg.DataDir)
	pages := service.NewPageService(service.PageDeps{
		Papers: st, Pages: st, Results: st, Glossary: st,
		LLM: runner, Queue: queue, Hub: hub, DataDir: cfg.DataDir,
	})
	ask := service.NewAskService(service.AskDeps{
		Papers: st, Pages: st, Results: st, Threads: st, LLM: runner, DataDir: cfg.DataDir,
	})

	// Anything left in flight by a previous run can never finish; mark it failed.
	if err := papers.Recover(ctx); err != nil {
		return fmt.Errorf("recover papers: %w", err)
	}
	if err := pages.Recover(ctx); err != nil {
		return fmt.Errorf("recover pages: %w", err)
	}
	if err := ask.Recover(ctx); err != nil {
		return fmt.Errorf("recover answers: %w", err)
	}
	queue.Start(ctx, pages.RunJob)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler.NewRouter(papers, pages, ask, nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			srv.Close() // open SSE streams
		}
	}()

	log.Printf("paper-reader listening on %s (data: %s, workers: %d)", cfg.ListenAddr, cfg.DataDir, cfg.Workers)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	queue.Wait()
	return nil
}
