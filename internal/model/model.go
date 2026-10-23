// Package model defines the domain types shared across the backend.
package model

import (
	"time"

	"github.com/google/uuid"
)

// Admin is a dashboard account. The seeded super admin has role
// "super_admin"; users created by the super admin have role "user" and can
// either manage search configs (CanManageConfigs) or only view the job feed.
type Admin struct {
	ID               uuid.UUID  `json:"id"`
	Email            string     `json:"email"`
	Name             string     `json:"name"`
	Role             string     `json:"role"`
	CanManageConfigs bool       `json:"can_manage_configs"`
	LastLoginAt      *time.Time `json:"last_login_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	PasswordHash     string     `json:"-"` // never serialized
}

// SearchConfig is an admin-defined job search: a job type, an AI-filled
// profile and a cron schedule describing when searches happen.
type SearchConfig struct {
	ID              uuid.UUID  `json:"id"`
	JobType         string     `json:"job_type"`
	CronExpression  string     `json:"cron_expression"`
	Timezone        string     `json:"timezone"`
	Enabled         bool       `json:"enabled"`
	Keywords        []string   `json:"keywords"`
	ExcludeKeywords []string   `json:"exclude_keywords"`
	Locations       []string   `json:"locations"`
	EmploymentType  string     `json:"employment_type"`
	WorkMode        string     `json:"work_mode"`
	Sources         []string   `json:"sources"`
	LookbackHours   int        `json:"lookback_hours"`
	AIGenerated     bool       `json:"ai_generated"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	CreatedBy       *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Job is a collected, validated job posting.
type Job struct {
	ID                   uuid.UUID  `json:"id"`
	SearchConfigID       uuid.UUID  `json:"search_config_id"`
	Source               string     `json:"source"`
	SourcePostID         string     `json:"source_post_id"`
	SourceURL            string     `json:"source_url"`
	JobTitle             string     `json:"job_title"`
	CompanyName          string     `json:"company_name"`
	Location             string     `json:"location"`
	EmploymentType       string     `json:"employment_type"`
	WorkMode             string     `json:"work_mode"`
	Description          string     `json:"description"`
	ApplicationURL       string     `json:"application_url"`
	PostedAt             *time.Time `json:"posted_at,omitempty"`
	FirstSeenAt          time.Time  `json:"first_seen_at"`
	LastSeenAt           time.Time  `json:"last_seen_at"`
	ContentHash          string     `json:"-"`
	ClassificationMethod string     `json:"classification_method"`
	AIConfidence         *float64   `json:"ai_confidence,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// SearchRun records one source execution inside a run of a search config.
type SearchRun struct {
	ID             uuid.UUID  `json:"id"`
	SearchConfigID uuid.UUID  `json:"search_config_id"`
	Source         string     `json:"source"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	Status         string     `json:"status"`
	RawCount       int        `json:"raw_count"`
	NewJobsCount   int        `json:"new_jobs_count"`
	DuplicateCount int        `json:"duplicate_count"`
	RejectedCount  int        `json:"rejected_count"`
	Error          string     `json:"error,omitempty"`
}

// RawPost is the normalized output of a collector before filtering.
type RawPost struct {
	Source         string
	SourcePostID   string
	SourceURL      string
	JobTitle       string
	CompanyName    string
	Location       string
	EmploymentType string
	WorkMode       string
	Description    string
	ApplicationURL string
	PostedAt       *time.Time
}

// Classification is the structured output of the AI job classifier.
type Classification struct {
	IsJob          bool    `json:"is_job"`
	JobTitle       string  `json:"job_title"`
	CompanyName    string  `json:"company_name"`
	Location       string  `json:"location"`
	EmploymentType string  `json:"employment_type"`
	WorkMode       string  `json:"work_mode"`
	Description    string  `json:"description"`
	PostedAt       *string `json:"posted_at"`
	ApplicationURL string  `json:"application_url"`
	Confidence     float64 `json:"confidence"`
}

// JobFilter drives the public jobs list endpoint.
type JobFilter struct {
	Query         string
	Location      string
	WorkMode      string
	EmploymentTyp string
	Source        string
	Since         *time.Time
	Limit         int
	Offset        int
}
