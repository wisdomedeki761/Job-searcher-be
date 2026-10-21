// Package config loads and validates backend configuration from environment variables.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration for the backend.
type Config struct {
	// Server
	ServerAddr string
	Timezone   *time.Location

	// Database
	DatabaseURL string

	// Super admin (seeded on first start only)
	SuperAdminEmail    string
	SuperAdminPassword string
	SuperAdminName     string

	// Auth
	JWTSecret string
	JWTExpiry time.Duration

	// AI (OpenRouter)
	OpenRouterAPIKey string
	OpenRouterModel  string
	AIClassify       bool

	// Scraping
	ApifyToken  string
	ApifyActors []NameValue               // name=actorId pairs
	ApifyInputs map[string]map[string]any // per-source input templates
	MockSources bool
	RunTimeout  time.Duration
}

// NameValue is one APIFY_ACTORS entry: a source name and its actor ID.
type NameValue struct {
	Name  string
	Value string
}

// Load reads .env (if present) and then environment variables.
// It returns an error when required values are missing or invalid.
func Load() (*Config, error) {
	// Best effort: ignore error when .env does not exist, env vars still apply.
	_ = godotenv.Load()

	cfg := &Config{
		ServerAddr:         getenv("SERVER_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		SuperAdminEmail:    strings.TrimSpace(os.Getenv("SUPER_ADMIN_EMAIL")),
		SuperAdminPassword: os.Getenv("SUPER_ADMIN_PASSWORD"),
		SuperAdminName:     getenv("SUPER_ADMIN_NAME", "Super Admin"),
		JWTSecret:          os.Getenv("JWT_SECRET"),
		JWTExpiry:          24 * time.Hour,
		OpenRouterAPIKey:   os.Getenv("OPENROUTER_API_KEY"),
		OpenRouterModel:    getenv("OPENROUTER_MODEL", ""),
		AIClassify:         strings.EqualFold(os.Getenv("AI_CLASSIFY"), "true"),
		ApifyToken:         os.Getenv("APIFY_TOKEN"),
		MockSources:        strings.EqualFold(os.Getenv("MOCK_SOURCES"), "true"),
		RunTimeout:         30 * time.Minute,
	}

	if v := os.Getenv("TZ"); v != "" {
		loc, err := time.LoadLocation(v)
		if err != nil {
			return nil, fmt.Errorf("invalid TZ %q: %w", v, err)
		}
		cfg.Timezone = loc
	} else {
		cfg.Timezone = time.UTC
	}

	if v := os.Getenv("JWT_EXPIRY"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid JWT_EXPIRY %q: %w", v, err)
		}
		cfg.JWTExpiry = d
	}

	if v := os.Getenv("RUN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid RUN_TIMEOUT %q: %w", v, err)
		}
		cfg.RunTimeout = d
	}

	seenSources := map[string]string{} // name -> entry it came from
	for _, pair := range splitCSV(os.Getenv("APIFY_ACTORS")) {
		var name, id string
		if before, after, ok := strings.Cut(pair, "="); ok {
			// Explicit form: name=actorId
			name, id = strings.TrimSpace(before), strings.TrimSpace(after)
		} else {
			// Bare actor ID form: "hirebase/job-search"
			// The source name is derived from the last path segment.
			id = strings.TrimSpace(pair)
			name = id
			if i := strings.LastIndexAny(name, "/~"); i >= 0 && i+1 < len(name) {
				name = name[i+1:]
			}
		}
		if name == "" || id == "" {
			return nil, fmt.Errorf("invalid APIFY_ACTORS entry %q, want name=actorId or a bare actorId", pair)
		}
		if prev, dup := seenSources[name]; dup {
			return nil, fmt.Errorf("duplicate APIFY source name %q (from %q and %q); use the name=actorId form to disambiguate", name, prev, pair)
		}
		seenSources[name] = pair
		cfg.ApifyActors = append(cfg.ApifyActors, NameValue{Name: name, Value: id})
	}

	// Optional per-source actor input overrides:
	// APIFY_INPUTS={"source-name":{"key":"{{placeholder}}"}}
	if v := strings.TrimSpace(os.Getenv("APIFY_INPUTS")); v != "" {
		if err := json.Unmarshal([]byte(v), &cfg.ApifyInputs); err != nil {
			return nil, fmt.Errorf("invalid APIFY_INPUTS (want JSON object of source -> input): %w", err)
		}
	}

	// Required values
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}

	return cfg, nil
}

// ApifyActorNames returns just the configured actor names (for /api/sources).
func (c *Config) ApifyActorNames() []string {
	names := make([]string, 0, len(c.ApifyActors))
	for _, a := range c.ApifyActors {
		names = append(names, a.Name)
	}
	return names
}

// ApifyActorID returns the actor ID for a configured source name.
func (c *Config) ApifyActorID(name string) (string, bool) {
	for _, a := range c.ApifyActors {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
