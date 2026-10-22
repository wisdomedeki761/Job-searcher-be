package collector

import (
	"context"
	"fmt"
	"time"

	"qx-scraper/internal/model"
)

// Mock is an offline source for local development and tests. It emits a small
// mix of plausible jobs, a duplicate, and a non-job post so the pipeline's
// filtering and dedupe logic can be observed without external APIs.
type Mock struct {
	name string
}

// NewMock creates the mock source.
func NewMock(name string) *Mock { return &Mock{name: name} }

func (m *Mock) Name() string { return m.name }

func (m *Mock) Search(_ context.Context, req SearchRequest) ([]model.RawPost, error) {
	now := time.Now().UTC()
	title := req.JobType
	if title == "" {
		title = "Software Engineer"
	}
	loc := "Remote"
	if len(req.Locations) > 0 {
		loc = req.Locations[0]
	}

	return []model.RawPost{
		{
			Source: m.name, SourcePostID: "mock-1",
			SourceURL:      "https://example.com/jobs/1",
			JobTitle:       title,
			CompanyName:    "Acme Corp",
			Location:       loc,
			EmploymentType: "full_time",
			WorkMode:       "remote",
			Description:    fmt.Sprintf("We are hiring a %s. Apply now!", title),
			ApplicationURL: "https://example.com/jobs/1/apply",
			PostedAt:       ptr(now.Add(-2 * time.Hour)),
		},
		{
			Source: m.name, SourcePostID: "mock-2",
			SourceURL:      "https://example.com/jobs/2",
			JobTitle:       title + " (Contract)",
			CompanyName:    "Globex",
			Location:       loc,
			EmploymentType: "contract",
			WorkMode:       "hybrid",
			Description:    "Long-term contract role. " + title + " needed.",
			ApplicationURL: "https://example.com/jobs/2/apply",
			PostedAt:       ptr(now.Add(-6 * time.Hour)),
		},
		{ // duplicate of mock-1 (same URL) — should bump last_seen, not insert
			Source: m.name, SourcePostID: "mock-1",
			SourceURL:      "https://example.com/jobs/1",
			JobTitle:       title,
			CompanyName:    "Acme Corp",
			Location:       loc,
			EmploymentType: "full_time",
			WorkMode:       "remote",
			Description:    fmt.Sprintf("We are hiring a %s. Apply now!", title),
			ApplicationURL: "https://example.com/jobs/1/apply",
			PostedAt:       ptr(now.Add(-2 * time.Hour)),
		},
		{ // no posted_at and non-job content — should be rejected
			Source:       m.name,
			SourcePostID: "mock-3",
			SourceURL:    "https://example.com/posts/3",
			JobTitle:     "Lunch with the team today",
			CompanyName:  "Acme Corp",
			Description:  "Great food, great company. Hiring managers nearby!",
		},
	}, nil
}

func ptr(t time.Time) *time.Time { return &t }
