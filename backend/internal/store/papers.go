package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

const paperCols = `p.id, p.title, p.filename, p.page_count, p.status, coalesce(p.error, ''), p.created_at,
	(select count(*) from page_results r where r.paper_id = p.id and r.status = 'done')`

func scanPaper(row pgx.Row) (domain.Paper, error) {
	var p domain.Paper
	var status string
	err := row.Scan(&p.ID, &p.Title, &p.Filename, &p.PageCount, &status, &p.Error, &p.CreatedAt, &p.DonePages)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Paper{}, domain.ErrNotFound
	}
	p.Status = domain.PaperStatus(status)
	return p, err
}

func (s *Store) CreatePaper(ctx context.Context, p domain.Paper) error {
	_, err := s.pool.Exec(ctx,
		`insert into papers (id, title, filename, page_count, status, created_at) values ($1, $2, $3, $4, $5, $6)`,
		p.ID, p.Title, p.Filename, p.PageCount, string(p.Status), p.CreatedAt)
	return err
}

func (s *Store) SetPaperReady(ctx context.Context, id string, pages []domain.Page) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, pg := range pages {
		blocks := pg.Blocks
		if blocks == nil {
			blocks = []domain.Block{}
		}
		if _, err := tx.Exec(ctx,
			`insert into pages (paper_id, page_no, width, height, image_path, blocks) values ($1, $2, $3, $4, $5, $6)`,
			id, pg.PageNo, pg.Width, pg.Height, pg.ImagePath, blocks); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `update papers set page_count = $2, status = 'ready', error = null where id = $1`, id, len(pages))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return tx.Commit(ctx)
}

func (s *Store) SetPaperFailed(ctx context.Context, id, msg string) error {
	tag, err := s.pool.Exec(ctx, `update papers set status = 'failed', error = $2 where id = $1`, id, msg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) ListPapers(ctx context.Context) ([]domain.Paper, error) {
	rows, err := s.pool.Query(ctx, `select `+paperCols+` from papers p order by p.created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	papers := []domain.Paper{}
	for rows.Next() {
		p, err := scanPaper(rows)
		if err != nil {
			return nil, err
		}
		papers = append(papers, p)
	}
	return papers, rows.Err()
}

func (s *Store) GetPaper(ctx context.Context, id string) (domain.Paper, error) {
	return scanPaper(s.pool.QueryRow(ctx, `select `+paperCols+` from papers p where p.id = $1`, id))
}

func (s *Store) DeletePaper(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `delete from papers where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) GetPage(ctx context.Context, paperID string, pageNo int) (domain.Page, error) {
	pg := domain.Page{PaperID: paperID, PageNo: pageNo}
	err := s.pool.QueryRow(ctx,
		`select width, height, image_path, blocks from pages where paper_id = $1 and page_no = $2`,
		paperID, pageNo).Scan(&pg.Width, &pg.Height, &pg.ImagePath, &pg.Blocks)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Page{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Page{}, err
	}
	if pg.Blocks == nil {
		pg.Blocks = []domain.Block{}
	}
	return pg, nil
}
