package store

import (
	"context"
	"strings"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func (s *Store) ListGlossary(ctx context.Context, paperID string) ([]domain.GlossaryTerm, error) {
	rows, err := s.pool.Query(ctx,
		`select term_en, term_ko, coalesce(note, '') from glossary where paper_id = $1 order by term_en`, paperID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	terms := []domain.GlossaryTerm{}
	for rows.Next() {
		var t domain.GlossaryTerm
		if err := rows.Scan(&t.En, &t.Ko, &t.Note); err != nil {
			return nil, err
		}
		terms = append(terms, t)
	}
	return terms, rows.Err()
}

// AddGlossary inserts terms; an existing English term keeps its first translation.
func (s *Store) AddGlossary(ctx context.Context, paperID string, terms []domain.GlossaryTerm) error {
	for _, t := range terms {
		en, ko := strings.TrimSpace(t.En), strings.TrimSpace(t.Ko)
		if en == "" || ko == "" {
			continue
		}
		if _, err := s.pool.Exec(ctx,
			`insert into glossary (paper_id, term_en, term_ko, note) values ($1, $2, $3, nullif($4, ''))
			 on conflict (paper_id, term_en) do nothing`,
			paperID, en, ko, strings.TrimSpace(t.Note)); err != nil {
			return err
		}
	}
	return nil
}
