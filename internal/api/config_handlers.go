package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"qx-scraper/internal/ai"
	"qx-scraper/internal/auth"
	"qx-scraper/internal/model"
	"qx-scraper/internal/scheduler"
	"qx-scraper/internal/store"
)

// configRequest is the create/update payload for a search config.
// The admin supplies job_type + cron_expression; the AI profile fields are
// optional (filled via /enrich or by sending them directly).
type configRequest struct {
	JobType         string   `json:"job_type"`
	CronExpression  string   `json:"cron_expression"`
	Timezone        string   `json:"timezone"`
	Enabled         *bool    `json:"enabled"`
	Keywords        []string `json:"keywords"`
	ExcludeKeywords []string `json:"exclude_keywords"`
	Locations       []string `json:"locations"`
	EmploymentType  string   `json:"employment_type"`
	WorkMode        string   `json:"work_mode"`
	Sources         []string `json:"sources"`
	LookbackHours   int      `json:"lookback_hours"`
	AIGenerated     bool     `json:"ai_generated"`
}

type enrichRequest struct {
	JobType string `json:"job_type"`
}

func (req *configRequest) validate() string {
	if strings.TrimSpace(req.JobType) == "" {
		return "job_type is required"
	}
	if strings.TrimSpace(req.CronExpression) == "" {
		return "cron_expression is required (e.g. \"0 */6 * * *\" for every 6 hours)"
	}
	if err := scheduler.ValidateCron(req.CronExpression); err != nil {
		return err.Error()
	}
	if req.EmploymentType != "" && !oneOf(req.EmploymentType, "any", "full_time", "part_time", "contract", "internship") {
		return "employment_type must be one of: any, full_time, part_time, contract, internship"
	}
	if req.WorkMode != "" && !oneOf(req.WorkMode, "any", "remote", "hybrid", "onsite") {
		return "work_mode must be one of: any, remote, hybrid, onsite"
	}
	if req.LookbackHours != 0 && (req.LookbackHours < 1 || req.LookbackHours > 720) {
		return "lookback_hours must be between 1 and 720"
	}
	return ""
}

func (req *configRequest) toModel(id uuid.UUID, createdBy *uuid.UUID, defaults bool) *model.SearchConfig {
	cfg := &model.SearchConfig{
		ID:              id,
		JobType:         strings.TrimSpace(req.JobType),
		CronExpression:  strings.TrimSpace(req.CronExpression),
		Timezone:        req.Timezone,
		Enabled:         true,
		Keywords:        req.Keywords,
		ExcludeKeywords: req.ExcludeKeywords,
		Locations:       req.Locations,
		EmploymentType:  req.EmploymentType,
		WorkMode:        req.WorkMode,
		Sources:         req.Sources,
		LookbackHours:   req.LookbackHours,
		AIGenerated:     req.AIGenerated,
		CreatedBy:       createdBy,
	}
	if defaults {
		if cfg.EmploymentType == "" {
			cfg.EmploymentType = "any"
		}
		if cfg.WorkMode == "" {
			cfg.WorkMode = "any"
		}
		if cfg.LookbackHours == 0 {
			cfg.LookbackHours = 24
		}
	}
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if cfg.Keywords == nil {
		cfg.Keywords = []string{}
	}
	if cfg.ExcludeKeywords == nil {
		cfg.ExcludeKeywords = []string{}
	}
	if cfg.Locations == nil {
		cfg.Locations = []string{}
	}
	if cfg.Sources == nil {
		cfg.Sources = []string{}
	}
	return cfg
}

// handleListConfigs lists all search configs.
func (s *Server) handleListConfigs(w http.ResponseWriter, r *http.Request) {
	configs, err := s.store.ListSearchConfigs(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Annotate with live running state for the dashboard.
	type row struct {
		*model.SearchConfig
		Running bool `json:"running"`
	}
	out := make([]row, 0, len(configs))
	for i := range configs {
		out = append(out, row{SearchConfig: &configs[i], Running: s.sched.IsRunning(configs[i].ID)})
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleCreateConfig creates a search config and schedules it.
func (s *Server) handleCreateConfig(w http.ResponseWriter, r *http.Request) {
	var req configRequest
	if err := s.decode(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if msg := req.validate(); msg != "" {
		s.writeError(w, http.StatusBadRequest, msg)
		return
	}

	claims, _ := auth.ClaimsFrom(r.Context())
	var createdBy *uuid.UUID
	if claims != nil {
		id := claims.AdminID
		createdBy = &id
	}

	// If the admin sent no profile fields, ask the AI to fill them.
	aiGenerated := req.AIGenerated
	if len(req.Keywords) == 0 && s.ai.Enabled() {
		profile, err := s.ai.EnrichJobType(r.Context(), req.JobType, s.sources.Names())
		if err != nil {
			s.log.Error("ai enrichment failed during config create", "job_type", req.JobType, "model", s.cfg.OpenRouterModel, "err", err)
			s.writeError(w, http.StatusBadGateway, "AI enrichment failed: "+err.Error())
			return
		}
		applyProfile(&req, profile)
		aiGenerated = true
	}
	if len(req.Sources) > 0 {
		if err := s.sources.ValidateNames(req.Sources); err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	cfg := req.toModel(uuid.Nil, createdBy, true)
	cfg.AIGenerated = aiGenerated
	if cfg.Timezone == "" {
		cfg.Timezone = s.cfg.Timezone.String()
	}

	created, err := s.store.CreateSearchConfig(r.Context(), cfg)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if created.Enabled {
		if err := s.sched.Add(created); err != nil {
			s.log.Error("schedule new config failed", "config", created.JobType, "err", err)
			s.writeError(w, http.StatusInternalServerError, "saved but scheduling failed: "+err.Error())
			return
		}
		// Reload to pick up persisted next_run_at.
		if fresh, err := s.store.GetSearchConfig(r.Context(), created.ID); err == nil {
			created = fresh
		}
	}
	s.writeJSON(w, http.StatusCreated, created)
}

// handleEnrich asks the AI to fill a search profile for a job type.
func (s *Server) handleEnrich(w http.ResponseWriter, r *http.Request) {
	var req enrichRequest
	if err := s.decode(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.JobType) == "" {
		s.writeError(w, http.StatusBadRequest, "job_type is required")
		return
	}
	if !s.ai.Enabled() {
		s.writeError(w, http.StatusServiceUnavailable, "AI not configured: set OPENROUTER_API_KEY")
		return
	}
	profile, err := s.ai.EnrichJobType(r.Context(), req.JobType, s.sources.Names())
	if err != nil {
		s.log.Error("ai enrichment failed", "job_type", req.JobType, "model", s.cfg.OpenRouterModel, "err", err)
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, profile)
}

// handleGetConfig fetches one config.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid config id")
		return
	}
	cfg, err := s.store.GetSearchConfig(r.Context(), id)
	if err != nil {
		s.writeError(w, errStatus(err), "config not found")
		return
	}
	s.writeJSON(w, http.StatusOK, cfg)
}

// handleUpdateConfig edits a config and reschedules it.
func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid config id")
		return
	}
	existing, err := s.store.GetSearchConfig(r.Context(), id)
	if err != nil {
		s.writeError(w, errStatus(err), "config not found")
		return
	}

	var req configRequest
	if err := s.decode(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if msg := req.validate(); msg != "" {
		s.writeError(w, http.StatusBadRequest, msg)
		return
	}
	if len(req.Sources) > 0 {
		if err := s.sources.ValidateNames(req.Sources); err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	cfg := req.toModel(existing.ID, existing.CreatedBy, false)
	// Preserve server-managed values not in the payload.
	cfg.Timezone = existing.Timezone
	if req.Timezone != "" {
		cfg.Timezone = req.Timezone
	}
	if cfg.EmploymentType == "" {
		cfg.EmploymentType = existing.EmploymentType
	}
	if cfg.WorkMode == "" {
		cfg.WorkMode = existing.WorkMode
	}
	if cfg.LookbackHours == 0 {
		cfg.LookbackHours = existing.LookbackHours
	}
	cfg.AIGenerated = req.AIGenerated
	if req.Enabled == nil {
		cfg.Enabled = existing.Enabled
	}

	updated, err := s.store.UpdateSearchConfig(r.Context(), cfg)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Reschedule: remove old entry, add new if enabled.
	s.sched.Remove(updated.ID)
	if updated.Enabled {
		if err := s.sched.Add(updated); err != nil {
			s.log.Error("reschedule failed", "config", updated.JobType, "err", err)
			s.writeError(w, http.StatusInternalServerError, "saved but rescheduling failed: "+err.Error())
			return
		}
		if fresh, err := s.store.GetSearchConfig(r.Context(), updated.ID); err == nil {
			updated = fresh
		}
	}
	s.writeJSON(w, http.StatusOK, updated)
}

// handleDeleteConfig deletes a config and unschedules it.
func (s *Server) handleDeleteConfig(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid config id")
		return
	}
	if s.sched.IsRunning(id) {
		s.writeError(w, http.StatusConflict, "config is running; try again shortly")
		return
	}
	if err := s.store.DeleteSearchConfig(r.Context(), id); err != nil {
		s.writeError(w, errStatus(err), "config not found")
		return
	}
	s.sched.Remove(id)
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleEnableConfig(w http.ResponseWriter, r *http.Request) {
	s.toggleConfig(w, r, true)
}

func (s *Server) handleDisableConfig(w http.ResponseWriter, r *http.Request) {
	s.toggleConfig(w, r, false)
}

func (s *Server) toggleConfig(w http.ResponseWriter, r *http.Request, enable bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid config id")
		return
	}
	if err := s.store.SetSearchConfigEnabled(r.Context(), id, enable); err != nil {
		s.writeError(w, errStatus(err), "config not found")
		return
	}
	cfg, err := s.store.GetSearchConfig(r.Context(), id)
	if err != nil {
		s.writeError(w, errStatus(err), "config not found")
		return
	}
	s.sched.Remove(id)
	if enable {
		if err := s.sched.Add(cfg); err != nil {
			s.writeError(w, http.StatusInternalServerError, "enabled but scheduling failed: "+err.Error())
			return
		}
		if fresh, err := s.store.GetSearchConfig(r.Context(), id); err == nil {
			cfg = fresh
		}
	}
	s.writeJSON(w, http.StatusOK, cfg)
}

// handleRunConfig triggers an immediate manual run.
func (s *Server) handleRunConfig(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid config id")
		return
	}
	if _, err := s.store.GetSearchConfig(r.Context(), id); err != nil {
		s.writeError(w, errStatus(err), "config not found")
		return
	}
	if err := s.sched.TriggerNow(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "already running") {
			s.writeError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "config not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "run started"})
}

// handleConfigRuns lists run history for one config.
func (s *Server) handleConfigRuns(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid config id")
		return
	}
	runs, err := s.store.ListRuns(r.Context(), &id, 50)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, emptyRuns(runs))
}

// handleAllRuns lists recent runs across all configs.
func (s *Server) handleAllRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListRuns(r.Context(), nil, 100)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, emptyRuns(runs))
}

// emptyRuns guarantees a JSON [] instead of null for empty run lists.
func emptyRuns(runs []model.SearchRun) []model.SearchRun {
	if runs == nil {
		return []model.SearchRun{}
	}
	return runs
}

// applyProfile copies an AI-generated profile into the request.
func applyProfile(req *configRequest, p *ai.Profile) {
	req.Keywords = p.Keywords
	req.ExcludeKeywords = p.ExcludeKeywords
	req.Locations = p.Locations
	req.EmploymentType = p.EmploymentType
	req.WorkMode = p.WorkMode
	req.Sources = p.Sources
	req.LookbackHours = p.LookbackHours
	if p.SuggestedCron != "" && scheduler.ValidateCron(p.SuggestedCron) == nil && strings.TrimSpace(req.CronExpression) == "" {
		req.CronExpression = p.SuggestedCron
	}
}
