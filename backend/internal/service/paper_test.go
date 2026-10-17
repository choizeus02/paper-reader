package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func newPaperSvc(t *testing.T, pdf *fakePDF) (*PaperService, *fakeRepo, string) {
	t.Helper()
	repo := newFakeRepo()
	dir := t.TempDir()
	s := NewPaperService(repo, pdf, dir)
	s.newID = func() string { return "p1" }
	s.spawn = func(f func()) { f() } // run extraction synchronously in tests
	return s, repo, dir
}

func TestUploadProcessesPDF(t *testing.T) {
	pdf := &fakePDF{pages: []domain.Page{
		{PageNo: 1, Width: 612, Height: 792, ImagePath: "001.png", Blocks: []domain.Block{{ID: "b0", Text: "Hi"}}},
		{PageNo: 2, Width: 612, Height: 792, ImagePath: "002.png", Blocks: []domain.Block{}},
	}}
	s, repo, dir := newPaperSvc(t, pdf)
	p, err := s.Upload(context.Background(), "Attention Is All You Need.PDF", strings.NewReader("%PDF-1.5 body"))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "p1" || p.Title != "Attention Is All You Need" || p.Filename != "Attention Is All You Need.PDF" || p.Status != domain.PaperProcessing {
		t.Fatalf("returned paper wrong: %+v", p)
	}
	data, err := os.ReadFile(filepath.Join(dir, "papers", "p1", "original.pdf"))
	if err != nil || string(data) != "%PDF-1.5 body" {
		t.Fatalf("original not stored: %q %v", data, err)
	}
	got, _ := repo.GetPaper(context.Background(), "p1")
	if got.Status != domain.PaperReady || got.PageCount != 2 {
		t.Fatalf("paper not ready: %+v", got)
	}
	pg, _ := repo.GetPage(context.Background(), "p1", 2)
	if pg.PaperID != "p1" || pg.ImagePath != "papers/p1/pages/002.png" {
		t.Fatalf("page not normalised: %+v", pg)
	}
}

func TestUploadRejectsNonPDF(t *testing.T) {
	s, repo, dir := newPaperSvc(t, &fakePDF{})
	for _, tc := range []struct{ name, body string }{
		{"notes.txt", "%PDF-1.5"},
		{"fake.pdf", "hello world"},
		{"tiny.pdf", "%P"},
	} {
		_, err := s.Upload(context.Background(), tc.name, strings.NewReader(tc.body))
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s: want ErrInvalid, got %v", tc.name, err)
		}
	}
	if list, _ := repo.ListPapers(context.Background()); len(list) != 0 {
		t.Fatalf("nothing should be stored: %+v", list)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "papers")); len(entries) != 0 {
		t.Fatalf("no directories should remain: %v", entries)
	}
}

func TestUploadMarksFailedOnExtractError(t *testing.T) {
	s, repo, _ := newPaperSvc(t, &fakePDF{err: errors.New("mutool draw: exit status 1: cannot open")})
	if _, err := s.Upload(context.Background(), "x.pdf", strings.NewReader("%PDF-1.4")); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetPaper(context.Background(), "p1")
	if got.Status != domain.PaperFailed || !strings.HasPrefix(got.Error, "PDF 처리 실패: ") || !strings.Contains(got.Error, "cannot open") {
		t.Fatalf("want failed with message, got %+v", got)
	}
}

func TestUploadMarksFailedOnZeroPages(t *testing.T) {
	s, repo, _ := newPaperSvc(t, &fakePDF{pages: []domain.Page{}})
	if _, err := s.Upload(context.Background(), "x.pdf", strings.NewReader("%PDF-1.4")); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetPaper(context.Background(), "p1")
	if got.Status != domain.PaperFailed {
		t.Fatalf("want failed, got %+v", got)
	}
}

func TestDeleteRemovesFiles(t *testing.T) {
	s, repo, dir := newPaperSvc(t, &fakePDF{pages: []domain.Page{{PageNo: 1, ImagePath: "001.png", Blocks: []domain.Block{}}}})
	if _, err := s.Upload(context.Background(), "x.pdf", strings.NewReader("%PDF-1.4")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "papers", "p1")); err != nil {
		t.Fatalf("paper directory should exist before delete: %v", err)
	}
	if err := s.Delete(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "papers", "p1")); !os.IsNotExist(err) {
		t.Fatal("paper directory should be removed")
	}
	if _, err := repo.GetPaper(context.Background(), "p1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("paper should be deleted")
	}
	if err := s.Delete(context.Background(), "p1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestPaperRecover(t *testing.T) {
	s, repo, _ := newPaperSvc(t, &fakePDF{})
	ctx := context.Background()
	_ = repo.CreatePaper(ctx, domain.Paper{ID: "a", Status: domain.PaperProcessing})
	_ = repo.CreatePaper(ctx, domain.Paper{ID: "b", Status: domain.PaperReady})
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := repo.GetPaper(ctx, "a")
	b, _ := repo.GetPaper(ctx, "b")
	if a.Status != domain.PaperFailed || a.Error != "서버 재시작으로 중단됨" || b.Status != domain.PaperReady {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}

type panicPDF struct{}

func (panicPDF) Process(context.Context, string, string) ([]domain.Page, error) {
	panic("pdf exploded")
}

func TestUploadPanicMarksFailed(t *testing.T) {
	repo := newFakeRepo()
	s := NewPaperService(repo, panicPDF{}, t.TempDir())
	s.newID = func() string { return "p1" }
	s.spawn = func(f func()) { f() }
	if _, err := s.Upload(context.Background(), "x.pdf", strings.NewReader("%PDF-1.4")); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetPaper(context.Background(), "p1")
	if got.Status != domain.PaperFailed || got.Error != "PDF 처리 실패: 내부 오류" {
		t.Fatalf("got %+v", got)
	}
}
