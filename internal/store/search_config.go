package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"qx-scraper/internal/model"
)

const searchConfigSelect = `
	id, job_type, cron_expression, timezone, enabled,
	keywords, exclude_keywords, locations, employment_type, work_mode,
	sources, lookback_hours, ai_generated, last_run_at, next_run_at,
	created_by, created_at, updated_at`

func scanSearchConfig(row pgx.Row) (*model.SearchConfig, error) {
	c := &model.SearchConfig{}
	err := row.Scan(
		&c.ID, &c.JobType, &c.CronExpression, &c.Timezone, &c.Enabled,
		&c.Keywords, &c.ExcludeKeywords, &c.Locations, &c.EmploymentType, &c.WorkMode,
		&c.Sources, &c.LookbackHours, &c.AIGenerated, &c.LastRunAt, &c.NextRunAt,
		&c.CreatedBy, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return c, nil
}

// ListSearchConfigs returns all search configs, newest first.
func (s *Store) ListSearchConfigs(ctx context.Context) ([]model.SearchConfig, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+searchConfigSelect+`
		FROM search_configs ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list search configs: %w", err)
	}
	defer rows.Close()

	var out []model.SearchConfig
	for rows.Next() {
		c, err := scanSearchConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListEnabledSearchConfigs returns only enabled configs (used on scheduler boot).
func (s *Store) ListEnabledSearchConfigs(ctx context.Context) ([]model.SearchConfig, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+searchConfigSelect+`
		FROM search_configs WHERE enabled = TRUE ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list enabled search configs: %w", err)
	}
	defer rows.Close()

	var out []model.SearchConfig
	for rows.Next() {
		c, err := scanSearchConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetSearchConfig fetches one config by ID.
func (s *Store) GetSearchConfig(ctx context.Context, id uuid.UUID) (*model.SearchConfig, error) {
	return scanSearchConfig(s.pool.QueryRow(ctx,
		`SELECT `+searchConfigSelect+` FROM search_configs WHERE id = $1`, id))
}

// CreateSearchConfig inserts a config and returns the stored row.
func (s *Store) CreateSearchConfig(ctx context.Context, c *model.SearchConfig) (*model.SearchConfig, error) {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return scanSearchConfig(s.pool.QueryRow(ctx, `
		INSERT INTO search_configs (
			id, job_type, cron_expression, timezone, enabled,
			keywords, exclude_keywords, locations, employment_type, work_mode,
			sources, lookback_hours, ai_generated, created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING `+searchConfigSelect,
		c.ID, c.JobType, c.CronExpression, c.Timezone, c.Enabled,
		nonNilSlice(c.Keywords), nonNilSlice(c.ExcludeKeywords), nonNilSlice(c.Locations),
		c.EmploymentType, c.WorkMode, nonNilSlice(c.Sources),
		c.LookbackHours, c.AIGenerated, c.CreatedBy,
	))
}

// UpdateSearchConfig replaces the editable fields of a config.
func (s *Store) UpdateSearchConfig(ctx context.Context, c *model.SearchConfig) (*model.SearchConfig, error) {
	return scanSearchConfig(s.pool.QueryRow(ctx, `
		UPDATE search_configs SET
			job_type = $2, cron_expression = $3, timezone = $4, enabled = $5,
			keywords = $6, exclude_keywords = $7, locations = $8,
			employment_type = $9, work_mode = $10, sources = $11,
			lookback_hours = $12, ai_generated = $13, updated_at = now()
		WHERE id = $1
		RETURNING `+searchConfigSelect,
		c.ID, c.JobType, c.CronExpression, c.Timezone, c.Enabled,
		nonNilSlice(c.Keywords), nonNilSlice(c.ExcludeKeywords), nonNilSlice(c.Locations),
		c.EmploymentType, c.WorkMode, nonNilSlice(c.Sources),
		c.LookbackHours, c.AIGenerated,
	))
}

// SetSearchConfigEnabled toggles a config without touching other fields.
func (s *Store) SetSearchConfigEnabled(ctx context.Context, id uuid.UUID, enabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE search_configs SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSearchConfig removes a config (hits/runs cascade).
func (s *Store) DeleteSearchConfig(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM search_configs WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateConfigRunTimes sets last_run_at / next_run_at.
func (s *Store) UpdateConfigRunTimes(ctx context.Context, id uuid.UUID, lastRun, nextRun *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE search_configs SET last_run_at = COALESCE($2, last_run_at), next_run_at = $3, updated_at = now()
		WHERE id = $1`, id, lastRun, nextRun)
	return err
}

func nonNilSlice(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
