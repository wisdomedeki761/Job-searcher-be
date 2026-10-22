// Package scheduler runs search configs on their configured cron schedules.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"qx-scraper/internal/model"
	"qx-scraper/internal/pipeline"
	"qx-scraper/internal/store"
)

// Scheduler maintains one cron entry per enabled search config.
type Scheduler struct {
	cron  *cron.Cron
	store *store.Store
	pipe  *pipeline.Pipeline
	log   *slog.Logger
	loc   *time.Location

	mu      sync.Mutex
	entries map[uuid.UUID]cron.EntryID
	running map[uuid.UUID]bool // per-config in-flight guard
}

// New creates a scheduler in the given timezone (from TZ config).
func New(st *store.Store, pipe *pipeline.Pipeline, log *slog.Logger, loc *time.Location) *Scheduler {
	if loc == nil {
		loc = time.UTC
	}
	return &Scheduler{
		cron:    cron.New(cron.WithLocation(loc)),
		store:   st,
		pipe:    pipe,
		log:     log,
		loc:     loc,
		entries: map[uuid.UUID]cron.EntryID{},
		running: map[uuid.UUID]bool{},
	}
}

// ValidateCron checks a 5-field cron expression using the scheduler's parser.
func ValidateCron(expr string) error {
	_, err := cron.ParseStandard(expr)
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return nil
}

// Start loads all enabled configs, registers cron entries and starts ticking.
func (s *Scheduler) Start(ctx context.Context) error {
	// Clean up runs left 'running' by a previous process.
	if n, err := s.store.MarkStaleRuns(ctx); err == nil && n > 0 {
		s.log.Warn("marked stale runs as failed", "count", n)
	}

	configs, err := s.store.ListEnabledSearchConfigs(ctx)
	if err != nil {
		return fmt.Errorf("load search configs: %w", err)
	}

	for i := range configs {
		cfg := configs[i]
		if err := s.register(&cfg); err != nil {
			s.log.Error("register schedule failed", "config", cfg.JobType, "err", err)
		}
	}

	s.cron.Start()
	s.log.Info("scheduler started", "jobs", len(s.entries), "timezone", s.loc)
	return nil
}

// Stop halts the cron runner, waiting for running entries to return.
func (s *Scheduler) Stop() context.Context {
	return s.cron.Stop()
}

// register adds or replaces the cron entry for a config (caller holds no lock).
func (s *Scheduler) register(cfg *model.SearchConfig) error {
	if err := ValidateCron(cfg.CronExpression); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Replace existing entry if present.
	if old, ok := s.entries[cfg.ID]; ok {
		s.cron.Remove(old)
		delete(s.entries, cfg.ID)
	}
	if !cfg.Enabled {
		return nil
	}

	id := cfg.ID
	entryID, err := s.cron.AddFunc(cfg.CronExpression, func() {
		go s.execute(id)
	})
	if err != nil {
		return fmt.Errorf("add cron entry: %w", err)
	}
	s.entries[cfg.ID] = entryID

	// Persist next_run_at for the admin dashboard.
	if next := s.cron.Entry(entryID).Next; !next.IsZero() {
		if err := s.store.UpdateConfigRunTimes(context.Background(), cfg.ID, nil, &next); err != nil {
			s.log.Warn("persist next_run_at failed", "config", cfg.JobType, "err", err)
		}
	}
	return nil
}

// Add registers (or re-registers) a config — called after create/update/enable.
func (s *Scheduler) Add(cfg *model.SearchConfig) error {
	return s.register(cfg)
}

// Remove drops the cron entry for a config — called after delete/disable.
func (s *Scheduler) Remove(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entryID, ok := s.entries[id]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, id)
	}
}

// TriggerNow runs a config immediately (manual run from the dashboard).
// It returns an error if the config is already running.
func (s *Scheduler) TriggerNow(ctx context.Context, id uuid.UUID) error {
	cfg, err := s.store.GetSearchConfig(ctx, id)
	if err != nil {
		return err
	}
	if !s.tryStart(id) {
		return fmt.Errorf("config %q is already running", cfg.JobType)
	}
	go s.executeLocked(cfg)
	return nil
}

// execute is the cron callback: load fresh config, guard, run.
func (s *Scheduler) execute(id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cfg, err := s.store.GetSearchConfig(ctx, id)
	cancel()
	if err != nil {
		if err != store.ErrNotFound {
			s.log.Error("load config for run failed", "id", id, "err", err)
		}
		return // deleted since registration
	}
	if !cfg.Enabled {
		return
	}
	if !s.tryStart(id) {
		s.log.Warn("previous run still in flight, skipping", "config", cfg.JobType)
		return
	}
	s.executeLocked(cfg)
}

// executeLocked runs the pipeline; caller must already hold the running flag.
func (s *Scheduler) executeLocked(cfg *model.SearchConfig) {
	defer s.finish(cfg.ID)

	s.log.Info("search run started", "config", cfg.JobType, "sources", cfg.Sources)
	sum, err := s.pipe.RunConfig(context.Background(), cfg)
	if err != nil {
		s.log.Error("search run finished with errors", "config", cfg.JobType, "err", err)
	}
	if sum != nil {
		s.log.Info("search run finished",
			"config", cfg.JobType, "sources", sum.SourcesRun,
			"raw", sum.RawCollected, "new", sum.NewJobs,
			"dupes", sum.Duplicates, "rejected", sum.Rejected)
	}
}

func (s *Scheduler) tryStart(id uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Scheduler) finish(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
}

// IsRunning reports whether a config currently has a run in flight.
func (s *Scheduler) IsRunning(id uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id]
}
