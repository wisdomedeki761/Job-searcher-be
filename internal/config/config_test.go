package config

import (
	"testing"
	"time"
)

// loadTestEnv sets the required env vars for a test load.
func loadTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/test")
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("SERVER_ADDR", "")
	t.Setenv("TZ", "")
	t.Setenv("JWT_EXPIRY", "")
	t.Setenv("RUN_TIMEOUT", "")
	t.Setenv("SUPER_ADMIN_EMAIL", "")
	t.Setenv("SUPER_ADMIN_PASSWORD", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("MOCK_SOURCES", "")
}

func TestLoadBareApifyActorID(t *testing.T) {
	loadTestEnv(t)
	t.Setenv("APIFY_ACTORS", "hirebase/job-search")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.ApifyActors) != 1 {
		t.Fatalf("got %d actors, want 1", len(cfg.ApifyActors))
	}
	a := cfg.ApifyActors[0]
	if a.Name != "job-search" || a.Value != "hirebase/job-search" {
		t.Errorf("got name=%q value=%q, want name=%q value=%q",
			a.Name, a.Value, "job-search", "hirebase/job-search")
	}
	if got := cfg.ApifyActorNames(); len(got) != 1 || got[0] != "job-search" {
		t.Errorf("ApifyActorNames() = %v", got)
	}
	if id, ok := cfg.ApifyActorID("job-search"); !ok || id != "hirebase/job-search" {
		t.Errorf("ApifyActorID() = %q, %v", id, ok)
	}
}

func TestLoadExplicitApifyActorName(t *testing.T) {
	loadTestEnv(t)
	t.Setenv("APIFY_ACTORS", "remote_jobs=apify/remote-jobs-scraper, li=apify/linkedin-jobs-scraper")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.ApifyActors) != 2 {
		t.Fatalf("got %d actors, want 2", len(cfg.ApifyActors))
	}
	if cfg.ApifyActors[0].Name != "remote_jobs" || cfg.ApifyActors[0].Value != "apify/remote-jobs-scraper" {
		t.Errorf("unexpected first actor: %+v", cfg.ApifyActors[0])
	}
	if cfg.ApifyActors[1].Name != "li" || cfg.ApifyActors[1].Value != "apify/linkedin-jobs-scraper" {
		t.Errorf("unexpected second actor: %+v", cfg.ApifyActors[1])
	}
}

func TestLoadMixedApifyFormats(t *testing.T) {
	loadTestEnv(t)
	t.Setenv("APIFY_ACTORS", "hirebase/job-search,custom=apify/web-scraper")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.ApifyActors) != 2 {
		t.Fatalf("got %d actors, want 2", len(cfg.ApifyActors))
	}
	if cfg.ApifyActors[0].Name != "job-search" {
		t.Errorf("bare entry name = %q, want job-search", cfg.ApifyActors[0].Name)
	}
	if cfg.ApifyActors[1].Name != "custom" {
		t.Errorf("explicit entry name = %q, want custom", cfg.ApifyActors[1].Name)
	}
}

// TestLoadRealWorldActorList uses the exact APIFY_ACTORS line from the
// project's .env (bare IDs, spaces around commas, trailing comma) to lock in
// that real-world formatting stays accepted.
func TestLoadRealWorldActorList(t *testing.T) {
	loadTestEnv(t)
	t.Setenv("APIFY_ACTORS",
		"hirebase/job-search , trudax/reddit-scraper-lite , curious_coder/linkedin-jobs-scraper , apidojo/tweet-scraper , ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v (spaces and trailing comma must be tolerated)", err)
	}
	want := []struct{ name, id string }{
		{"job-search", "hirebase/job-search"},
		{"reddit-scraper-lite", "trudax/reddit-scraper-lite"},
		{"linkedin-jobs-scraper", "curious_coder/linkedin-jobs-scraper"},
		{"tweet-scraper", "apidojo/tweet-scraper"},
	}
	if len(cfg.ApifyActors) != len(want) {
		t.Fatalf("got %d actors, want %d: %+v", len(cfg.ApifyActors), len(want), cfg.ApifyActors)
	}
	for i, w := range want {
		if cfg.ApifyActors[i].Name != w.name || cfg.ApifyActors[i].Value != w.id {
			t.Errorf("actor[%d] = {name:%q id:%q}, want {name:%q id:%q}",
				i, cfg.ApifyActors[i].Name, cfg.ApifyActors[i].Value, w.name, w.id)
		}
	}
}

func TestLoadDuplicateSourceNamesRejected(t *testing.T) {
	loadTestEnv(t)
	t.Setenv("APIFY_ACTORS", "a/job-search,b/job-search")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want duplicate name error")
	}
}

func TestLoadMissingRequiredValues(t *testing.T) {
	loadTestEnv(t)
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Error("expected error for missing DATABASE_URL")
	}

	loadTestEnv(t)
	t.Setenv("JWT_SECRET", "")
	if _, err := Load(); err == nil {
		t.Error("expected error for missing JWT_SECRET")
	}
}

func TestLoadDefaults(t *testing.T) {
	loadTestEnv(t)
	t.Setenv("APIFY_ACTORS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.JWTExpiry != 24*time.Hour {
		t.Errorf("JWTExpiry = %v, want 24h", cfg.JWTExpiry)
	}
	if cfg.ServerAddr != ":8080" {
		t.Errorf("ServerAddr = %q, want :8080", cfg.ServerAddr)
	}
	if cfg.Timezone != time.UTC {
		t.Errorf("Timezone = %v, want UTC", cfg.Timezone)
	}
	if len(cfg.ApifyActors) != 0 {
		t.Errorf("ApifyActors = %v, want empty", cfg.ApifyActors)
	}
}
