package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func newAskSvc(t *testing.T, responses ...fakeResponse) (*AskService, *pageFixture) {
	f := newPageFixture(t)
	f.llm.responses = responses
	svc := NewAskService(AskDeps{Papers: f.repo, Pages: f.repo, Results: f.repo, Threads: f.repo, LLM: f.llm, DataDir: f.svc.d.DataDir})
	svc.newID = func() string { return "t1" }
	return svc, f
}

func TestAskStartAndContinue(t *testing.T) {
	svc, f := newAskSvc(t, fakeResponse{text: "내적입니다."}, fakeResponse{text: "스케일 때문이에요."})
	ctx := context.Background()
	_, _ = f.repo.MarkPending(ctx, "p1", 2, false)
	_ = f.repo.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 2, Translation: []domain.BlockTranslation{{BlockID: "b0", Ko: "텍스트 2"}}})

	var streamed strings.Builder
	th, err := svc.Start(ctx, "p1", 2, domain.Anchor{BlockIDs: []string{"b0"}, Quote: "QK^T"}, "  이게 뭐야?  ", func(d string) { streamed.WriteString(d) })
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "내적입니다." {
		t.Fatalf("streamed %q", streamed.String())
	}
	if th.ID != "t1" || len(th.Messages) != 2 || th.Messages[0].Content != "이게 뭐야?" || th.Messages[1].Content != "내적입니다." || th.Messages[1].Status != domain.MessageDone {
		t.Fatalf("thread wrong: %+v", th)
	}
	req := f.llm.requests[0]
	if string(req.Image) != "png-2" {
		t.Fatalf("image = %q", req.Image)
	}
	for _, want := range []string{`Quote: "QK^T"`, "[b0] 텍스트 2", "이게 뭐야?"} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("first prompt missing %q", want)
		}
	}

	th, err = svc.Continue(ctx, "t1", "왜 루트 d로 나눠?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Messages) != 4 || th.Messages[3].Content != "스케일 때문이에요." {
		t.Fatalf("thread after continue: %+v", th)
	}
	second := f.llm.requests[1].Prompt
	for _, want := range []string{"Reader: 이게 뭐야?", "Tutor: 내적입니다.", "왜 루트 d로 나눠?"} {
		if !strings.Contains(second, want) {
			t.Errorf("follow-up prompt missing %q", want)
		}
	}

	list, _ := svc.List(ctx, "p1", 2)
	if len(list) != 1 || list[0].ID != "t1" {
		t.Fatalf("list = %+v", list)
	}
}

func TestAskLLMFailureStoresFailedMessage(t *testing.T) {
	svc, f := newAskSvc(t, fakeResponse{err: errors.New("claude: usage limit")})
	ctx := context.Background()
	_, err := svc.Start(ctx, "p1", 1, domain.Anchor{}, "질문", nil)
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("want usage limit error, got %v", err)
	}
	th, _ := f.repo.GetThread(ctx, "t1")
	if len(th.Messages) != 2 || th.Messages[1].Status != domain.MessageFailed || !strings.Contains(th.Messages[1].Content, "usage limit") {
		t.Fatalf("failed message not stored: %+v", th.Messages)
	}
	if th.Anchor.BlockIDs == nil {
		t.Fatal("anchor block ids must be non-nil")
	}
}

func TestAskFailedMessagesExcludedFromHistory(t *testing.T) {
	svc, f := newAskSvc(t, fakeResponse{err: errors.New("boom")}, fakeResponse{text: "답"})
	ctx := context.Background()
	_, _ = svc.Start(ctx, "p1", 1, domain.Anchor{}, "첫 질문", nil)
	if _, err := svc.Continue(ctx, "t1", "다시", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.llm.requests[1].Prompt, "boom") {
		t.Fatal("failed assistant messages must not be sent as history")
	}
}

func TestAskValidation(t *testing.T) {
	svc, _ := newAskSvc(t)
	ctx := context.Background()
	if _, err := svc.Start(ctx, "p1", 1, domain.Anchor{}, "   ", nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty question: %v", err)
	}
	if _, err := svc.Start(ctx, "p1", 9, domain.Anchor{}, "q", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing page: %v", err)
	}
	if _, err := svc.Continue(ctx, "nope", "q", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing thread: %v", err)
	}
}

func TestAskErrorsBeforeFirstDelta(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(svc *AskService, onDelta func(string)) error
		want error
	}{
		{"empty question", func(s *AskService, d func(string)) error {
			_, err := s.Start(ctx, "p1", 1, domain.Anchor{}, " ", d)
			return err
		}, domain.ErrInvalid},
		{"missing page", func(s *AskService, d func(string)) error {
			_, err := s.Start(ctx, "p1", 9, domain.Anchor{}, "q", d)
			return err
		}, domain.ErrNotFound},
		{"missing paper", func(s *AskService, d func(string)) error {
			_, err := s.Start(ctx, "nope", 1, domain.Anchor{}, "q", d)
			return err
		}, domain.ErrNotFound},
		{"missing thread", func(s *AskService, d func(string)) error { _, err := s.Continue(ctx, "nope", "q", d); return err }, domain.ErrNotFound},
		{"empty follow-up", func(s *AskService, d func(string)) error { _, err := s.Continue(ctx, "t1", "  ", d); return err }, domain.ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, f := newAskSvc(t, fakeResponse{text: "답변"})
			_ = f.repo.CreateThread(ctx, domain.Thread{ID: "t1", PaperID: "p1", PageNo: 1})
			deltas := 0
			err := c.call(svc, func(string) { deltas++ })
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if deltas != 0 || f.llm.calls() != 0 {
				t.Fatalf("deltas=%d llm calls=%d, want none", deltas, f.llm.calls())
			}
		})
	}
}

func TestAskStartFailureCreatesNoThread(t *testing.T) {
	svc, f := newAskSvc(t)
	if _, err := svc.Start(context.Background(), "p1", 9, domain.Anchor{}, "q", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := f.repo.GetThread(context.Background(), "t1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("orphan thread created: %v", err)
	}
}

func TestAskRecover(t *testing.T) {
	svc, f := newAskSvc(t)
	ctx := context.Background()
	_ = f.repo.CreateThread(ctx, domain.Thread{ID: "t9", PaperID: "p1", PageNo: 1})
	_, _ = f.repo.AddMessage(ctx, domain.Message{ThreadID: "t9", Role: domain.RoleAssistant, Content: "부분", Status: domain.MessageStreaming})
	_, _ = f.repo.AddMessage(ctx, domain.Message{ThreadID: "t9", Role: domain.RoleUser, Content: "q", Status: domain.MessageDone})
	if err := svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	th, _ := f.repo.GetThread(ctx, "t9")
	if m := th.Messages[0]; m.Status != domain.MessageFailed || m.Content != "부분\n\n(서버 재시작으로 중단됨)" {
		t.Fatalf("streaming msg: %+v", m)
	}
	if th.Messages[1].Status != domain.MessageDone {
		t.Fatalf("done msg touched: %+v", th.Messages[1])
	}
}
