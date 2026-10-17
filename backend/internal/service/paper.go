package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

const extractTimeout = 10 * time.Minute

type PaperService struct {
	repo    PaperRepo
	pdf     PDFProcessor
	dataDir string

	newID func() string
	spawn func(func())
	now   func() time.Time
}

func NewPaperService(repo PaperRepo, pdf PDFProcessor, dataDir string) *PaperService {
	return &PaperService{
		repo: repo, pdf: pdf, dataDir: dataDir,
		newID: uuid.NewString,
		spawn: func(f func()) { go f() },
		now:   time.Now,
	}
}

func paperDir(dataDir, id string) string { return filepath.Join(dataDir, "papers", id) }

// Upload stores the PDF and starts page extraction in the background.
func (s *PaperService) Upload(ctx context.Context, filename string, r io.Reader) (domain.Paper, error) {
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	if !strings.EqualFold(ext, ".pdf") {
		return domain.Paper{}, fmt.Errorf("%w: PDF 파일만 올릴 수 있습니다", domain.ErrInvalid)
	}
	br := bufio.NewReader(r)
	if head, _ := br.Peek(5); string(head) != "%PDF-" {
		return domain.Paper{}, fmt.Errorf("%w: PDF 형식이 아닙니다", domain.ErrInvalid)
	}

	id := s.newID()
	dir := paperDir(s.dataDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return domain.Paper{}, err
	}
	if err := writeFile(filepath.Join(dir, "original.pdf"), br); err != nil {
		os.RemoveAll(dir)
		return domain.Paper{}, err
	}
	p := domain.Paper{
		ID: id, Title: strings.TrimSuffix(base, ext), Filename: base,
		Status: domain.PaperProcessing, CreatedAt: s.now().UTC(),
	}
	if err := s.repo.CreatePaper(ctx, p); err != nil {
		os.RemoveAll(dir)
		return domain.Paper{}, err
	}
	s.spawn(func() { s.process(id) })
	return p, nil
}

func (s *PaperService) process(id string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("paper %s: extraction panic: %v\n%s", id, r, debug.Stack())
			if ferr := s.repo.SetPaperFailed(context.Background(), id, "PDF 처리 실패: 내부 오류"); ferr != nil {
				log.Printf("paper %s: mark failed: %v", id, ferr)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), extractTimeout)
	defer cancel()
	dir := paperDir(s.dataDir, id)
	pages, err := s.pdf.Process(ctx, filepath.Join(dir, "original.pdf"), filepath.Join(dir, "pages"))
	if err != nil {
		err = fmt.Errorf("PDF 처리 실패: %w", err)
	} else if len(pages) == 0 {
		err = fmt.Errorf("페이지가 없는 PDF입니다")
	}
	if err == nil {
		for i := range pages {
			pages[i].PaperID = id
			pages[i].ImagePath = filepath.ToSlash(filepath.Join("papers", id, "pages", pages[i].ImagePath))
		}
		err = s.repo.SetPaperReady(ctx, id, pages)
	}
	if err != nil {
		log.Printf("paper %s: extraction failed: %v", id, err)
		if ferr := s.repo.SetPaperFailed(context.Background(), id, err.Error()); ferr != nil {
			log.Printf("paper %s: mark failed: %v", id, ferr)
		}
	}
}

func (s *PaperService) List(ctx context.Context) ([]domain.Paper, error) {
	return s.repo.ListPapers(ctx)
}

func (s *PaperService) Get(ctx context.Context, id string) (domain.Paper, error) {
	return s.repo.GetPaper(ctx, id)
}

func (s *PaperService) Delete(ctx context.Context, id string) error {
	if err := s.repo.DeletePaper(ctx, id); err != nil {
		return err
	}
	return os.RemoveAll(paperDir(s.dataDir, id))
}

func writeFile(path string, r io.Reader) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Recover fails papers whose extraction was cut off by a restart.
func (s *PaperService) Recover(ctx context.Context) error {
	n, err := s.repo.FailProcessingPapers(ctx, restartMsg)
	if n > 0 {
		log.Printf("marked %d unfinished papers as failed", n)
	}
	return err
}
