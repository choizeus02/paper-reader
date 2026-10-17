package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

// newTestStore gives each test its own schema inside TEST_DATABASE_URL and
// drops it afterwards. Tests are skipped when TEST_DATABASE_URL is unset.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (run `make test-db-up` and `make test`)")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "drop schema "+schema+" cascade")
		_ = admin.Close(context.Background())
	})
	s, err := Open(ctx, url, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// seedPaper creates a ready paper with n pages; page k has one block "b0".
func seedPaper(t *testing.T, s *Store, id string, n int) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreatePaper(ctx, domain.Paper{ID: id, Title: id, Filename: id + ".pdf", Status: domain.PaperProcessing, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	pages := make([]domain.Page, n)
	for i := range pages {
		pages[i] = domain.Page{PaperID: id, PageNo: i + 1, Width: 612, Height: 792,
			ImagePath: fmt.Sprintf("papers/%s/pages/%03d.png", id, i+1),
			Blocks:    []domain.Block{{ID: "b0", Text: fmt.Sprintf("page %d", i+1)}}}
	}
	if err := s.SetPaperReady(ctx, id, pages); err != nil {
		t.Fatal(err)
	}
}
