// Command server is the QX-Scraper backend entrypoint.
//
// Boot order:
//  1. Load config from env / .env
//  2. Connect to PostgreSQL and run migrations
//  3. Seed the super admin (only if none exists)
//  4. Start the scheduler with all enabled search configs
//  5. Serve the HTTP API until SIGINT/SIGTERM
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"qx-scraper/internal/ai"
	"qx-scraper/internal/api"
	"qx-scraper/internal/collector"
	"qx-scraper/internal/config"
	"qx-scraper/internal/db"
	"qx-scraper/internal/pipeline"
	"qx-scraper/internal/scheduler"
	"qx-scraper/internal/seeder"
	"qx-scraper/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	// 1. Config
	cfg, err := config.Load()
	if err != nil {
		return errors.New("config: " + err.Error())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 2. Database + migrations
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}

	st := store.New(pool)

	// 3. Seed super admin (first start only)
	if err := seeder.SeedSuperAdmin(ctx, st, cfg, log); err != nil {
		return err
	}

	// 4. Dependencies
	aiClient := ai.New(cfg.OpenRouterAPIKey, cfg.OpenRouterModel, log)
	if !aiClient.Enabled() {
		log.Warn("OPENROUTER_API_KEY not set — AI enrichment and classification are disabled")
	}
	sources := collector.BuildRegistry(cfg)
	log.Info("sources registered", "sources", sources.Names())

	pipe := pipeline.New(st, sources, aiClient, cfg.AIClassify, log, cfg.RunTimeout)
	sched := scheduler.New(st, pipe, log, cfg.Timezone)
	if err := sched.Start(ctx); err != nil {
		return err
	}
	defer func() {
		cancelCtx := sched.Stop()
		select {
		case <-cancelCtx.Done():
		case <-time.After(10 * time.Second):
			log.Warn("scheduler did not stop within 10s")
		}
	}()

	// 5. HTTP server
	serverAPI := api.NewServer(cfg, st, aiClient, sources, sched, log)
	httpServer := &http.Server{
		Addr:              cfg.ServerAddr,
		Handler:           serverAPI.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("server listening", "addr", cfg.ServerAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return nil
}
