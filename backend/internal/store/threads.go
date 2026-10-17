package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func (s *Store) CreateThread(ctx context.Context, t domain.Thread) error {
	anchor := t.Anchor
	if anchor.BlockIDs == nil {
		anchor.BlockIDs = []string{}
	}
	_, err := s.pool.Exec(ctx,
		`insert into threads (id, paper_id, page_no, anchor, created_at) values ($1, $2, $3, $4, $5)`,
		t.ID, t.PaperID, t.PageNo, anchor, t.CreatedAt)
	return err
}

func (s *Store) AddMessage(ctx context.Context, m domain.Message) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`insert into messages (thread_id, role, content, status, created_at) values ($1, $2, $3, $4, $5) returning id`,
		m.ThreadID, string(m.Role), m.Content, string(m.Status), m.CreatedAt).Scan(&id)
	return id, err
}

func (s *Store) FinishMessage(ctx context.Context, id int64, content string, status domain.MessageStatus) error {
	tag, err := s.pool.Exec(ctx, `update messages set content = $2, status = $3 where id = $1`, id, content, string(status))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) GetThread(ctx context.Context, id string) (domain.Thread, error) {
	var t domain.Thread
	err := s.pool.QueryRow(ctx,
		`select id, paper_id, page_no, anchor, created_at from threads where id = $1`, id).
		Scan(&t.ID, &t.PaperID, &t.PageNo, &t.Anchor, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Thread{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Thread{}, err
	}
	msgs, err := s.messagesFor(ctx, []string{id})
	if err != nil {
		return domain.Thread{}, err
	}
	t.Messages = nonNil(msgs[id])
	return t, nil
}

func (s *Store) ListThreads(ctx context.Context, paperID string, pageNo int) ([]domain.Thread, error) {
	rows, err := s.pool.Query(ctx,
		`select id, paper_id, page_no, anchor, created_at from threads
		  where paper_id = $1 and page_no = $2 order by created_at, id`, paperID, pageNo)
	if err != nil {
		return nil, err
	}
	threads := []domain.Thread{}
	var ids []string
	for rows.Next() {
		var t domain.Thread
		if err := rows.Scan(&t.ID, &t.PaperID, &t.PageNo, &t.Anchor, &t.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		threads = append(threads, t)
		ids = append(ids, t.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return threads, nil
	}
	msgs, err := s.messagesFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range threads {
		threads[i].Messages = nonNil(msgs[threads[i].ID])
	}
	return threads, nil
}

func (s *Store) messagesFor(ctx context.Context, threadIDs []string) (map[string][]domain.Message, error) {
	rows, err := s.pool.Query(ctx,
		`select id, thread_id, role, content, status, created_at from messages where thread_id = any($1) order by id`, threadIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]domain.Message{}
	for rows.Next() {
		var m domain.Message
		var role, status string
		if err := rows.Scan(&m.ID, &m.ThreadID, &role, &m.Content, &status, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Role, m.Status = domain.MessageRole(role), domain.MessageStatus(status)
		out[m.ThreadID] = append(out[m.ThreadID], m)
	}
	return out, rows.Err()
}

func nonNil(m []domain.Message) []domain.Message {
	if m == nil {
		return []domain.Message{}
	}
	return m
}
