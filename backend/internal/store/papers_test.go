package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func TestPaperLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	created := time.Now().UTC().Truncate(time.Microsecond)

	if err := s.CreatePaper(ctx, domain.Paper{ID: "p1", Title: "Attention", Filename: "Attention.pdf", Status: domain.PaperProcessing, CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPaper(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.PaperProcessing || got.PageCount != 0 || got.Title != "Attention" || !got.CreatedAt.Equal(created) {
		t.Fatalf("unexpected paper: %+v", got)
	}

	page1Blocks := []domain.Block{{ID: "b0", BBox: domain.BBox{X: 1, Y: 2, W: 3, H: 4}, Text: "Hello"}}
	pages := []domain.Page{
		{PaperID: "p1", PageNo: 1, Width: 612, Height: 792, ImagePath: "papers/p1/pages/001.png", Blocks: page1Blocks},
		{PaperID: "p1", PageNo: 2, Width: 612, Height: 792, ImagePath: "papers/p1/pages/002.png", Blocks: nil},
	}
	if err := s.SetPaperReady(ctx, "p1", pages); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPaper(ctx, "p1")
	if got.Status != domain.PaperReady || got.PageCount != 2 {
		t.Fatalf("paper not ready: %+v", got)
	}

	pg1, err := s.GetPage(ctx, "p1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pg1.Blocks, page1Blocks) || pg1.ImagePath != "papers/p1/pages/001.png" || pg1.Width != 612 {
		t.Fatalf("page 1 wrong: %+v", pg1)
	}
	pg2, err := s.GetPage(ctx, "p1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if pg2.Blocks == nil || len(pg2.Blocks) != 0 {
		t.Fatalf("page 2 blocks must be empty slice, got %#v", pg2.Blocks)
	}
	if _, err := s.GetPage(ctx, "p1", 3); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	list, err := s.ListPapers(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "p1" || list[0].DonePages != 0 {
		t.Fatalf("list wrong: %+v %v", list, err)
	}

	if err := s.DeletePaper(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPaper(ctx, "p1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
	if _, err := s.GetPage(ctx, "p1", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pages must cascade, got %v", err)
	}
	if err := s.DeletePaper(ctx, "p1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete: want ErrNotFound, got %v", err)
	}
}

func TestSetPaperFailedAndOrdering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-time.Hour)
	_ = s.CreatePaper(ctx, domain.Paper{ID: "old", Title: "old", Filename: "old.pdf", Status: domain.PaperProcessing, CreatedAt: old})
	_ = s.CreatePaper(ctx, domain.Paper{ID: "new", Title: "new", Filename: "new.pdf", Status: domain.PaperProcessing, CreatedAt: old.Add(time.Minute)})
	if err := s.SetPaperFailed(ctx, "old", "mutool: cannot open"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetPaper(ctx, "old")
	if p.Status != domain.PaperFailed || p.Error != "mutool: cannot open" {
		t.Fatalf("not failed: %+v", p)
	}
	list, _ := s.ListPapers(ctx)
	if len(list) != 2 || list[0].ID != "new" {
		t.Fatalf("want newest first, got %+v", list)
	}
	if _, err := s.GetPaper(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSetPaperReadyMissingPaper(t *testing.T) {
	s := newTestStore(t)
	pages := []domain.Page{{PaperID: "nope", PageNo: 1, Width: 1, Height: 1, ImagePath: "x"}}
	if err := s.SetPaperReady(context.Background(), "nope", pages); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
