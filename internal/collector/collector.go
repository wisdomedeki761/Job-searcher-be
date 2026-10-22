// Package collector fetches raw posts from configured sources and normalizes
// them into model.RawPost.
package collector

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"qx-scraper/internal/config"
	"qx-scraper/internal/model"
)

// SearchRequest carries the parameters of one search run for a source.
type SearchRequest struct {
	JobType        string
	Keywords       []string
	Locations      []string
	EmploymentType string
	WorkMode       string
	Lookback       time.Duration
}

// Query returns the keyword string sent to source APIs.
func (r SearchRequest) Query() string {
	if len(r.Keywords) == 0 {
		return r.JobType
	}
	return strings.Join(r.Keywords, " ")
}

// Source is a platform that can be searched for job posts.
type Source interface {
	Name() string
	Search(ctx context.Context, req SearchRequest) ([]model.RawPost, error)
}

// Registry holds all available sources by name.
type Registry struct {
	sources map[string]Source
	order   []string
}

// BuildRegistry assembles sources from configuration: Apify actors from
// APIFY_ACTORS and a mock source when MOCK_SOURCES=true.
//
// Each actor's input schema differs. Resolution order:
//  1. APIFY_INPUTS entry for the source name (explicit override, optional)
//  2. built-in preset for the actor ID (see knownActorInputs /
//     knownActorInputFns)
//  3. generic default input
//
// Additionally, when Apify rejects input with a type error, Search coerces
// the named field and retries automatically — so most sources need no
// configuration at all.
func BuildRegistry(cfg *config.Config) *Registry {
	r := &Registry{sources: map[string]Source{}}

	for _, a := range cfg.ApifyActors {
		tpl := cfg.ApifyInputs[a.Name]
		var fn func(SearchRequest) map[string]any
		if tpl == nil {
			tpl = KnownActorInput(a.Value)
			fn = KnownActorInputFn(a.Value)
		}
		src := NewApify(a.Name, a.Value, cfg.ApifyToken, tpl)
		src.inputFn = fn
		r.register(src)
	}
	if cfg.MockSources {
		r.register(NewMock("mock"))
	}
	return r
}

// knownActorInputs are built-in input shapes for well-known actors, keyed by
// actor ID with "/" separators, so popular sources work out of the box.
// APIFY_INPUTS always overrides these; they never require any .env JSON.
var knownActorInputs = map[string]map[string]any{
	// hirebase matches keywords/locations as arrays and caps with maxResults
	// (not maxItems). postedWithinDays prefilters by the config's lookback.
	"hirebase/job-search": {
		"keywords":         "{{keywords}}",
		"locations":        "{{locations}}",
		"postedWithinDays": "{{lookback_days}}",
		"maxResults":       100,
	},
	// trudax searches Reddit natively per keyword — no URLs needed.
	// (startUrls also works, but must be [{"url": "..."}] objects.)
	"trudax/reddit-scraper-lite": {
		"searches":          "{{keywords}}",
		"searchPosts":       true,
		"searchCommunities": false,
		"searchComments":    false,
		"searchUsers":       false,
		"sort":              "new",
		"maxItems":          50,
		"maxPostCount":      25,
	},
}

// knownActorInputFns are presets that need computed input (e.g. one search
// URL per keyword) rather than static placeholder templates.
var knownActorInputFns = map[string]func(SearchRequest) map[string]any{
	"curious_coder/linkedin-jobs-scraper": linkedinInput,
}

// linkedinInput builds ONE LinkedIn jobs search URL per keyword. LinkedIn
// treats a comma-joined keyword blob as a single query and returns loosely
// related jobs ("Software Engineer" for a "figma designer" search); separate
// URLs keep every search precise. The first configured location is used as
// the LinkedIn location filter (its URLs take a single location); the
// pipeline's location filter still applies on the results.
func linkedinInput(req SearchRequest) map[string]any {
	kw := req.Keywords
	if len(kw) == 0 {
		kw = []string{req.JobType}
	}
	loc := ""
	for _, l := range req.Locations { // trim + use the first non-empty
		if l = strings.TrimSpace(l); l != "" {
			loc = l
			break
		}
	}

	urls := make([]any, 0, len(kw))
	for _, k := range kw {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		u := "https://www.linkedin.com/jobs/search/?keywords=" + url.QueryEscape(k)
		if loc != "" {
			u += "&location=" + url.QueryEscape(loc)
		}
		urls = append(urls, u)
	}

	input := map[string]any{
		"urls":           urls,
		"limitPerSource": 50,
	}
	// Date-posted window (f_TPR values) when a lookback is configured.
	if h := int(req.Lookback.Hours()); h > 0 {
		switch {
		case h <= 24:
			input["datePosted"] = "r86400"
		case h <= 24*7:
			input["datePosted"] = "r604800"
		case h <= 24*30:
			input["datePosted"] = "r2592000"
		}
	}
	return input
}

// KnownActorInput returns the built-in input template for an actor ID
// (either "/" or "~" form), or nil when no preset exists.
func KnownActorInput(actorID string) map[string]any {
	return knownActorInputs[strings.ReplaceAll(actorID, "~", "/")]
}

// KnownActorInputFn returns a computed-input preset for an actor ID, or nil.
func KnownActorInputFn(actorID string) func(SearchRequest) map[string]any {
	return knownActorInputFns[strings.ReplaceAll(actorID, "~", "/")]
}

func (r *Registry) register(s Source) {
	if _, exists := r.sources[s.Name()]; exists {
		return
	}
	r.sources[s.Name()] = s
	r.order = append(r.order, s.Name())
}

// Names returns source names in registration order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Get resolves a source by name.
func (r *Registry) Get(name string) (Source, bool) {
	s, ok := r.sources[name]
	return s, ok
}

// Resolve picks requested source names, or all sources when none specified.
// Unknown names are returned separately so the caller can log them.
func (r *Registry) Resolve(names []string) (found []Source, unknown []string) {
	if len(names) == 0 {
		for _, n := range r.order {
			found = append(found, r.sources[n])
		}
		return found, nil
	}
	for _, n := range names {
		if s, ok := r.sources[n]; ok {
			found = append(found, s)
		} else {
			unknown = append(unknown, n)
		}
	}
	return found, unknown
}

// ValidateNames returns an error listing any names that are not registered.
func (r *Registry) ValidateNames(names []string) error {
	if len(names) == 0 {
		return nil
	}
	var bad []string
	for _, n := range names {
		if _, ok := r.sources[n]; !ok {
			bad = append(bad, n)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("unknown source(s): %s (available: %s)",
			strings.Join(bad, ", "), strings.Join(r.Names(), ", "))
	}
	return nil
}
