// Package api exposes the REST endpoints (auth, admin dashboard, public jobs).
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"aj-scraper/internal/ai"
	"aj-scraper/internal/auth"
	"aj-scraper/internal/collector"
	"aj-scraper/internal/config"
	"aj-scraper/internal/scheduler"
	"aj-scraper/internal/store"
)

// Server wires dependencies into HTTP handlers.
type Server struct {
	cfg     *config.Config
	store   *store.Store
	ai      *ai.Client
	sources *collector.Registry
	sched   *scheduler.Scheduler
	log     *slog.Logger
}

// NewServer constructs the API server.
func NewServer(cfg *config.Config, st *store.Store, aiClient *ai.Client, sources *collector.Registry, sched *scheduler.Scheduler, log *slog.Logger) *Server {
	return &Server{cfg: cfg, store: st, ai: aiClient, sources: sources, sched: sched, log: log}
}

// Router builds the chi router with all routes.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.cors)

	// Health
	r.Get("/health", s.handleHealth)

	// Auth
	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/login", s.handleLogin)
		r.With(auth.Middleware(s.cfg.JWTSecret, false)).Get("/me", s.handleMe)
	})

	// Public jobs API
	r.Route("/api", func(r chi.Router) {
		r.Get("/jobs", s.handleListJobs)
		r.Get("/jobs/{id}", s.handleGetJob)
		r.Get("/sources", s.handleListSources)
	})

	// Admin dashboard API (super admin only)
	r.Route("/api/admin", func(r chi.Router) {
		r.Use(auth.Middleware(s.cfg.JWTSecret, true))

		r.Get("/search-configs", s.handleListConfigs)
		r.Post("/search-configs", s.handleCreateConfig)
		r.Post("/search-configs/enrich", s.handleEnrich)
		r.Get("/search-configs/{id}", s.handleGetConfig)
		r.Put("/search-configs/{id}", s.handleUpdateConfig)
		r.Delete("/search-configs/{id}", s.handleDeleteConfig)
		r.Post("/search-configs/{id}/enable", s.handleEnableConfig)
		r.Post("/search-configs/{id}/disable", s.handleDisableConfig)
		r.Post("/search-configs/{id}/run", s.handleRunConfig)
		r.Get("/search-configs/{id}/runs", s.handleConfigRuns)
		r.Get("/runs", s.handleAllRuns)
	})

	return r
}

// --- helpers ---------------------------------------------------------------

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error("encode response failed", "err", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// oneOf reports whether v equals any of the allowed values.
func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// errStatus maps domain errors to HTTP status codes.
func errStatus(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

// cors allows all origins: the frontend is separate and public API is read-only.
// Credentials (JWT) are never sent via cookies, so this is safe for v1.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
