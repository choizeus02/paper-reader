package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func TestThreads(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPaper(t, s, "p1", 2)
	now := time.Now().UTC()

	th := domain.Thread{ID: "t1", PaperID: "p1", PageNo: 1, Anchor: domain.Anchor{BlockIDs: []string{"b0"}, Quote: "QK^T"}, CreatedAt: now}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(ctx, domain.Thread{ID: "t2", PaperID: "p1", PageNo: 1, Anchor: domain.Anchor{BlockIDs: []string{}}, CreatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(ctx, domain.Thread{ID: "t3", PaperID: "p1", PageNo: 2, Anchor: domain.Anchor{BlockIDs: []string{}}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	uid, err := s.AddMessage(ctx, domain.Message{ThreadID: "t1", Role: domain.RoleUser, Content: "왜?", Status: domain.MessageDone, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	aid, err := s.AddMessage(ctx, domain.Message{ThreadID: "t1", Role: domain.RoleAssistant, Content: "", Status: domain.MessageStreaming, CreatedAt: now})
	if err != nil || aid <= uid {
		t.Fatalf("ids: user=%d assistant=%d err=%v", uid, aid, err)
	}
	if err := s.FinishMessage(ctx, aid, "그래서요.", domain.MessageDone); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishMessage(ctx, 999999, "x", domain.MessageDone); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	got, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Anchor.Quote != "QK^T" || len(got.Messages) != 2 || got.Messages[0].Role != domain.RoleUser || got.Messages[1].Content != "그래서요." || got.Messages[1].Status != domain.MessageDone {
		t.Fatalf("thread wrong: %+v", got)
	}

	list, err := s.ListThreads(ctx, "p1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "t1" || list[1].ID != "t2" || len(list[0].Messages) != 2 || list[1].Messages == nil {
		t.Fatalf("list wrong: %+v", list)
	}
	if _, err := s.GetThread(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	empty, err := s.ListThreads(ctx, "p1", 9)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("want empty slice, got %#v %v", empty, err)
	}
}

func TestDeletePaperCascades(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPaper(t, s, "p1", 2)

	if _, err := s.MarkPending(ctx, "p1", 1, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 1, Explanation: "e", PageSummary: "s", PromptVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddGlossary(ctx, "p1", []domain.GlossaryTerm{{En: "attention", Ko: "어텐션"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(ctx, domain.Thread{ID: "t1", PaperID: "p1", PageNo: 1, Anchor: domain.Anchor{BlockIDs: []string{}}, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, domain.Message{ThreadID: "t1", Role: domain.RoleUser, Content: "q", Status: domain.MessageDone, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	papers, err := s.ListPapers(ctx)
	if err != nil || len(papers) != 1 || papers[0].DonePages != 1 {
		t.Fatalf("ListPapers = %+v, %v; want DonePages == 1", papers, err)
	}

	if err := s.DeletePaper(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`select count(*) from page_results`,
		`select count(*) from glossary`,
		`select count(*) from threads`,
		`select count(*) from messages`,
	} {
		var n int
		if err := s.pool.QueryRow(ctx, q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s = %d, %v; want 0 after DeletePaper", q, n, err)
		}
	}
}
