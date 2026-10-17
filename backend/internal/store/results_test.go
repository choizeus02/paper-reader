package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func TestResultStateMachine(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPaper(t, s, "p1", 2)

	if _, err := s.GetResult(ctx, "p1", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	mustPending := func(force, want bool) {
		t.Helper()
		got, err := s.MarkPending(ctx, "p1", 1, force)
		if err != nil || got != want {
			t.Fatalf("MarkPending(force=%v) = %v, %v; want %v", force, got, err, want)
		}
	}
	mustPending(false, true)  // new row
	mustPending(false, false) // already pending
	if err := s.MarkRunning(ctx, "p1", 1); err != nil {
		t.Fatal(err)
	}
	mustPending(true, false) // running is never re-queued, even with force

	tr := []domain.BlockTranslation{{BlockID: "b0", Ko: "안녕"}}
	if err := s.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 1, Translation: tr, Explanation: "설명", PageSummary: "요약", PromptVersion: 1}); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetResult(ctx, "p1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != domain.ResultDone || !reflect.DeepEqual(r.Translation, tr) || r.Explanation != "설명" || r.PageSummary != "요약" || r.PromptVersion != 1 {
		t.Fatalf("done result wrong: %+v", r)
	}
	p, _ := s.GetPaper(ctx, "p1")
	if p.DonePages != 1 {
		t.Fatalf("DonePages = %d, want 1", p.DonePages)
	}

	mustPending(false, false) // done without force
	mustPending(true, true)   // done with force
	if err := s.MarkFailed(ctx, "p1", 1, "claude: usage limit"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.GetResult(ctx, "p1", 1)
	if r.Status != domain.ResultFailed || r.Error != "claude: usage limit" {
		t.Fatalf("failed result wrong: %+v", r)
	}
	mustPending(false, true) // failed → pending
	r, _ = s.GetResult(ctx, "p1", 1)
	if r.Error != "" {
		t.Fatalf("error should be cleared on re-queue: %+v", r)
	}

	if _, err := s.MarkPending(ctx, "p1", 2, false); err != nil {
		t.Fatal(err)
	}
	n, err := s.FailUnfinished(ctx, "서버 재시작으로 중단됨")
	if err != nil || n != 2 {
		t.Fatalf("FailUnfinished = %d, %v; want 2", n, err)
	}
	r, _ = s.GetResult(ctx, "p1", 2)
	if r.Status != domain.ResultFailed || r.Error != "서버 재시작으로 중단됨" || r.Translation == nil {
		t.Fatalf("unfinished not failed: %+v", r)
	}
}

func TestGlossaryFirstWins(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPaper(t, s, "p1", 1)
	if err := s.AddGlossary(ctx, "p1", []domain.GlossaryTerm{{En: "attention", Ko: "어텐션", Note: "음차"}, {En: "encoder", Ko: "인코더"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddGlossary(ctx, "p1", []domain.GlossaryTerm{{En: "attention", Ko: "주의"}, {En: "decoder", Ko: "디코더"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListGlossary(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.GlossaryTerm{{En: "attention", Ko: "어텐션", Note: "음차"}, {En: "decoder", Ko: "디코더"}, {En: "encoder", Ko: "인코더"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	empty, err := s.ListGlossary(ctx, "nope")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v %v", empty, err)
	}
}
