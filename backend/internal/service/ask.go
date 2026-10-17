package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
)

type AskDeps struct {
	Papers  PaperRepo
	Pages   PageRepo
	Results ResultRepo
	Threads ThreadRepo
	LLM     LLM
	DataDir string
}

type AskService struct {
	d     AskDeps
	newID func() string
	now   func() time.Time
}

func NewAskService(d AskDeps) *AskService {
	return &AskService{d: d, newID: uuid.NewString, now: time.Now}
}

func (s *AskService) List(ctx context.Context, paperID string, pageNo int) ([]domain.Thread, error) {
	return s.d.Threads.ListThreads(ctx, paperID, pageNo)
}

func (s *AskService) Start(ctx context.Context, paperID string, pageNo int, anchor domain.Anchor, question string, onDelta func(string)) (domain.Thread, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return domain.Thread{}, fmt.Errorf("%w: 질문을 입력하세요", domain.ErrInvalid)
	}
	if anchor.BlockIDs == nil {
		anchor.BlockIDs = []string{}
	}
	t := domain.Thread{ID: s.newID(), PaperID: paperID, PageNo: pageNo, Anchor: anchor, CreatedAt: s.now().UTC(), Messages: []domain.Message{}}
	// Resolve paper, page and image before persisting anything so a bad
	// request leaves no orphan thread and fails before any streaming.
	req, err := s.buildRequest(ctx, t, question)
	if err != nil {
		return domain.Thread{}, err
	}
	if err := s.d.Threads.CreateThread(ctx, t); err != nil {
		return domain.Thread{}, err
	}
	return s.answer(ctx, t, req, question, onDelta)
}

func (s *AskService) Continue(ctx context.Context, threadID, question string, onDelta func(string)) (domain.Thread, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return domain.Thread{}, fmt.Errorf("%w: 질문을 입력하세요", domain.ErrInvalid)
	}
	t, err := s.d.Threads.GetThread(ctx, threadID)
	if err != nil {
		return domain.Thread{}, err
	}
	req, err := s.buildRequest(ctx, t, question)
	if err != nil {
		return domain.Thread{}, err
	}
	return s.answer(ctx, t, req, question, onDelta)
}

func (s *AskService) answer(ctx context.Context, t domain.Thread, req llm.Request, question string, onDelta func(string)) (domain.Thread, error) {
	now := s.now().UTC()
	if _, err := s.d.Threads.AddMessage(ctx, domain.Message{ThreadID: t.ID, Role: domain.RoleUser, Content: question, Status: domain.MessageDone, CreatedAt: now}); err != nil {
		return domain.Thread{}, err
	}
	aid, err := s.d.Threads.AddMessage(ctx, domain.Message{ThreadID: t.ID, Role: domain.RoleAssistant, Status: domain.MessageStreaming, CreatedAt: now})
	if err != nil {
		return domain.Thread{}, err
	}

	text, runErr := s.d.LLM.Run(ctx, req, onDelta)
	bg := context.Background() // persist the outcome even if the client went away
	if runErr != nil {
		if err := s.d.Threads.FinishMessage(bg, aid, "답변 생성 실패: "+runErr.Error(), domain.MessageFailed); err != nil {
			log.Printf("thread %s: store failed message: %v", t.ID, err)
		}
		return domain.Thread{}, runErr
	}
	if err := s.d.Threads.FinishMessage(bg, aid, text, domain.MessageDone); err != nil {
		return domain.Thread{}, err
	}
	return s.d.Threads.GetThread(bg, t.ID)
}

func (s *AskService) buildRequest(ctx context.Context, t domain.Thread, question string) (llm.Request, error) {
	paper, err := s.d.Papers.GetPaper(ctx, t.PaperID)
	if err != nil {
		return llm.Request{}, err
	}
	page, err := s.d.Pages.GetPage(ctx, t.PaperID, t.PageNo)
	if err != nil {
		return llm.Request{}, err
	}
	img, err := os.ReadFile(filepath.Join(s.d.DataDir, filepath.FromSlash(page.ImagePath)))
	if err != nil {
		return llm.Request{}, fmt.Errorf("페이지 이미지를 읽을 수 없습니다: %w", err)
	}
	ac := llm.AskContext{Title: paper.Title, PageNo: t.PageNo, Blocks: page.Blocks, Anchor: t.Anchor, Question: question}
	if res, err := s.d.Results.GetResult(ctx, t.PaperID, t.PageNo); err == nil && res.Status == domain.ResultDone {
		ac.Translation = res.Translation
	}
	for _, m := range t.Messages {
		if m.Status == domain.MessageDone {
			ac.History = append(ac.History, m)
		}
	}
	prompt, err := llm.BuildAskPrompt(ac)
	if err != nil {
		return llm.Request{}, err
	}
	return llm.Request{System: llm.AskSystem, Image: img, Prompt: prompt}, nil
}

// Recover fails assistant replies that were still streaming at shutdown.
func (s *AskService) Recover(ctx context.Context) error {
	n, err := s.d.Threads.FailStreamingMessages(ctx, restartMsg)
	if n > 0 {
		log.Printf("marked %d unfinished answers as failed", n)
	}
	return err
}
