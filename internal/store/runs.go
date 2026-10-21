package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"aj-scraper/internal/model"
)

const runColumns = `
	id, search_config_id, source, started_at, finished_at, status,
	raw_count, new_jobs_count, duplicate_count, rejected_count, error`

func scanRun(row pgx.Row) (*model.SearchRun, error) {
	r := &model.SearchRun{}
	err := row.Scan(
		&r.ID, &r.SearchConfigID, &r.Source, &r.StartedAt, &r.FinishedAt, &r.Status,
		&r.RawCount, &r.NewJobsCount, &r.DuplicateCount, &r.RejectedCount, &r.Error,
	)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return r, nil
}

// StartRun creates a 'running' search_runs row and returns it.
func (s *Store) StartRun(ctx context.Context, configID uuid.UUID, source string) (*model.SearchRun, error) {
	return scanRun(s.pool.QueryRow(ctx, `
		INSERT INTO search_runs (id, search_config_id, source, status)
		VALUES ($1, $2, $3, 'running')
		RETURNING `+runColumns, uuid.New(), configID, source))
}

// FinishRun updates a run with its outcome and counters.
func (s *Store) FinishRun(ctx context.Context, runID uuid.UUID, status string, raw, created, dupes, rejected int, runErr string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE search_runs SET
			finished_at = now(), status = $2, raw_count = $3,
			new_jobs_count = $4, duplicate_count = $5, rejected_count = $6, error = $7
		WHERE id = $1`,
		runID, status, raw, created, dupes, rejected, runErr)
	return err
}

// ListRuns returns run history, optionally filtered by config, newest first.
func (s *Store) ListRuns(ctx context.Context, configID *uuid.UUID, limit int) ([]model.SearchRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var (
		rows pgx.Rows
		err  error
	)
	if configID != nil {
		rows, err = s.pool.Query(ctx, `
			SELECT `+runColumns+` FROM search_runs
			WHERE search_config_id = $1 ORDER BY started_at DESC LIMIT $2`, *configID, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT `+runColumns+` FROM search_runs
			ORDER BY started_at DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var out []model.SearchRun
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// CountRunningRuns reports whether a config has an unfinished run (guard).
func (s *Store) CountRunningRuns(ctx context.Context, configID uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM search_runs WHERE search_config_id = $1 AND status = 'running'`,
		configID).Scan(&n)
	return n, err
}

// MarkStaleRuns marks runs left in 'running' state after a restart as failed.
func (s *Store) MarkStaleRuns(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE search_runs SET status = 'failed', finished_at = now(), error = 'interrupted by restart'
		WHERE status = 'running'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
