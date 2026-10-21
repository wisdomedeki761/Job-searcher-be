package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aj-scraper/internal/model"
)

// handleListJobs lists recent jobs with optional filters:
// q, location, work_mode, employment_type, source, since, limit, offset.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := model.JobFilter{
		Query:         q.Get("q"),
		Location:      q.Get("location"),
		WorkMode:      q.Get("work_mode"),
		EmploymentTyp: q.Get("employment_type"),
		Source:        q.Get("source"),
	}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		filter.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			s.writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		filter.Offset = n
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "since must be RFC3339 (e.g. 2026-10-11T00:00:00Z)")
			return
		}
		filter.Since = &t
	}

	jobs, err := s.store.ListJobs(r.Context(), filter)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if jobs == nil {
		jobs = []model.Job{}
	}
	s.writeJSON(w, http.StatusOK, jobs)
}

// handleGetJob fetches one job by ID.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid job id")
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		s.writeError(w, errStatus(err), "job not found")
		return
	}
	s.writeJSON(w, http.StatusOK, job)
}

// handleListSources lists available collection sources.
func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"sources": s.sources.Names(),
		"ai":      s.ai.Enabled(),
	})
}
