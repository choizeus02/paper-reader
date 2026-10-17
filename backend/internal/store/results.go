package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func (s *Store) GetResult(ctx context.Context, paperID string, pageNo int) (domain.PageResult, error) {
	r := domain.PageResult{PaperID: paperID, PageNo: pageNo}
	var status string
	err := s.pool.QueryRow(ctx, `
		select status, coalesce(translation, '[]'::jsonb), coalesce(explanation, ''), coalesce(page_summary, ''),
		       prompt_version, coalesce(error, ''), updated_at
		  from page_results where paper_id = $1 and page_no = $2`, paperID, pageNo).
		Scan(&status, &r.Translation, &r.Explanation, &r.PageSummary, &r.PromptVersion, &r.Error, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PageResult{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.PageResult{}, err
	}
	r.Status = domain.ResultStatus(status)
	if r.Translation == nil {
		r.Translation = []domain.BlockTranslation{}
	}
	return r, nil
}

func (s *Store) MarkPending(ctx context.Context, paperID string, pageNo int, force bool) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `
		insert into page_results (paper_id, page_no, status, updated_at)
		values ($1, $2, 'pending', now())
		on conflict (paper_id, page_no) do update
		   set status = 'pending', error = null, updated_at = now()
		 where page_results.status = 'failed'
		    or ($3::boolean and page_results.status = 'done')
		returning 1`, paperID, pageNo, force).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) setStatus(ctx context.Context, paperID string, pageNo int, status domain.ResultStatus, msg *string) error {
	tag, err := s.pool.Exec(ctx,
		`update page_results set status = $3, error = $4, updated_at = now() where paper_id = $1 and page_no = $2`,
		paperID, pageNo, string(status), msg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) MarkRunning(ctx context.Context, paperID string, pageNo int) error {
	return s.setStatus(ctx, paperID, pageNo, domain.ResultRunning, nil)
}

func (s *Store) MarkFailed(ctx context.Context, paperID string, pageNo int, msg string) error {
	return s.setStatus(ctx, paperID, pageNo, domain.ResultFailed, &msg)
}

func (s *Store) SaveDone(ctx context.Context, r domain.PageResult) error {
	tr := r.Translation
	if tr == nil {
		tr = []domain.BlockTranslation{}
	}
	tag, err := s.pool.Exec(ctx, `
		update page_results
		   set status = 'done', translation = $3, explanation = $4, page_summary = $5,
		       prompt_version = $6, error = null, updated_at = now()
		 where paper_id = $1 and page_no = $2`,
		r.PaperID, r.PageNo, tr, r.Explanation, r.PageSummary, r.PromptVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) FailUnfinished(ctx context.Context, msg string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`update page_results set status = 'failed', error = $1, updated_at = now() where status in ('pending', 'running')`, msg)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
