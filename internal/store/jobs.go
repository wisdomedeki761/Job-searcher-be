package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"aj-scraper/internal/model"
)

const jobColumns = `
	id, search_config_id, source, source_post_id, source_url, job_title,
	company_name, location, employment_type, work_mode, description, application_url,
	posted_at, first_seen_at, last_seen_at, content_hash, classification_method,
	ai_confidence, created_at, updated_at`

func scanJob(row pgx.Row) (*model.Job, error) {
	j := &model.Job{}
	err := row.Scan(
		&j.ID, &j.SearchConfigID, &j.Source, &j.SourcePostID, &j.SourceURL, &j.JobTitle,
		&j.CompanyName, &j.Location, &j.EmploymentType, &j.WorkMode, &j.Description, &j.ApplicationURL,
		&j.PostedAt, &j.FirstSeenAt, &j.LastSeenAt, &j.ContentHash, &j.ClassificationMethod,
		&j.AIConfidence, &j.CreatedAt, &j.UpdatedAt,
	)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return j, nil
}

// InsertJob inserts a job. On conflict (same source_url or content_hash) it
// bumps last_seen_at instead of creating a duplicate. Returns created=true
// when a new row was inserted.
func (s *Store) InsertJob(ctx context.Context, j *model.Job) (bool, error) {
	if j.ID == uuid.Nil {
		j.ID = uuid.New()
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO jobs (
			id, search_config_id, source, source_post_id, source_url, job_title,
			company_name, location, employment_type, work_mode, description, application_url,
			posted_at, content_hash, classification_method, ai_confidence
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (source_url) DO UPDATE SET
			last_seen_at = now(),
			posted_at = COALESCE(jobs.posted_at, EXCLUDED.posted_at),
			updated_at = now()
		RETURNING (xmax = 0) AS created, `+jobColumns,
		j.ID, j.SearchConfigID, j.Source, j.SourcePostID, j.SourceURL, j.JobTitle,
		j.CompanyName, j.Location, j.EmploymentType, j.WorkMode, j.Description, j.ApplicationURL,
		j.PostedAt, j.ContentHash, j.ClassificationMethod, j.AIConfidence,
	)
	var created bool
	if err := row.Scan(&created,
		&j.ID, &j.SearchConfigID, &j.Source, &j.SourcePostID, &j.SourceURL, &j.JobTitle,
		&j.CompanyName, &j.Location, &j.EmploymentType, &j.WorkMode, &j.Description, &j.ApplicationURL,
		&j.PostedAt, &j.FirstSeenAt, &j.LastSeenAt, &j.ContentHash, &j.ClassificationMethod,
		&j.AIConfidence, &j.CreatedAt, &j.UpdatedAt); err != nil {
		// Unique content_hash violation means an equivalent job already exists
		// under a different URL — treat as duplicate, not an error.
		if strings.Contains(err.Error(), "uq_jobs_content_hash") {
			return false, nil
		}
		return false, fmt.Errorf("insert job: %w", err)
	}

	// Record config hit (many-to-many link).
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO job_search_config_hits (job_id, search_config_id)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`, j.ID, j.SearchConfigID)

	return created, nil
}

// ListJobs returns jobs matching the filter, newest first, with limit/offset.
func (s *Store) ListJobs(ctx context.Context, f model.JobFilter) ([]model.Job, error) {
	var (
		conds []string
		args  []any
	)
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}

	if f.Query != "" {
		p := "%" + strings.ToLower(f.Query) + "%"
		args = append(args, p)
		n := len(args)
		conds = append(conds, fmt.Sprintf(
			"(LOWER(job_title) LIKE $%d OR LOWER(company_name) LIKE $%d OR LOWER(description) LIKE $%d)", n, n, n))
	}
	if f.Location != "" {
		add("LOWER(location) LIKE $%d", "%"+strings.ToLower(f.Location)+"%")
	}
	if f.WorkMode != "" {
		add("work_mode = $%d", f.WorkMode)
	}
	if f.EmploymentTyp != "" {
		add("employment_type = $%d", f.EmploymentTyp)
	}
	if f.Source != "" {
		add("source = $%d", f.Source)
	}
	if f.Since != nil {
		add("posted_at >= $%d", *f.Since)
	}

	query := `SELECT ` + jobColumns + ` FROM jobs`
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY first_seen_at DESC, id DESC"

	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	args = append(args, limit)
	query += fmt.Sprintf(" LIMIT $%d", len(args))
	if f.Offset > 0 {
		args = append(args, f.Offset)
		query += fmt.Sprintf(" OFFSET $%d", len(args))
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var out []model.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// GetJob fetches one job by ID.
func (s *Store) GetJob(ctx context.Context, id uuid.UUID) (*model.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = $1`, id))
}

// CountJobs returns total jobs in the table.
func (s *Store) CountJobs(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&n)
	return n, err
}
