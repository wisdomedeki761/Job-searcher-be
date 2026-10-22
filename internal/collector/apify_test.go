package collector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qx-scraper/internal/config"
)

func testRequest() SearchRequest {
	return SearchRequest{
		JobType:   "Golang Developer",
		Keywords:  []string{"golang", "go developer"},
		Locations: []string{"Lagos", "Remote"},
		Lookback:  24 * time.Hour,
	}
}

// TestBuildInputDefault: job-search requires keywords as an ARRAY
// ("Field input.keywords must be array") — exact-match placeholders produce
// native arrays, query stays a string.
func TestBuildInputDefault(t *testing.T) {
	in := BuildInput(testRequest(), nil)

	if q, _ := in["query"].(string); q != "golang go developer" {
		t.Errorf("query = %q, want %q", q, "golang go developer")
	}
	kw, ok := in["keywords"].([]string)
	if !ok || len(kw) != 2 || kw[0] != "golang" || kw[1] != "go developer" {
		t.Errorf("keywords = %v (%T), want array [golang go developer]", in["keywords"], in["keywords"])
	}
	locs, ok := in["locations"].([]string)
	if !ok || len(locs) != 2 || locs[0] != "Lagos" || locs[1] != "Remote" {
		t.Errorf("locations = %v (%T), want array [Lagos Remote]", in["locations"], in["locations"])
	}
	if in["maxItems"] != 100 {
		t.Errorf("maxItems = %v, want 100", in["maxItems"])
	}
	// End-to-end: the marshaled JSON must contain real arrays (job-search
	// rejects with "Field input.keywords must be array" otherwise).
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if !strings.Contains(string(body), `"keywords":["golang","go developer"]`) {
		t.Errorf("marshaled keywords not an array: %s", body)
	}
	if !strings.Contains(string(body), `"locations":["Lagos","Remote"]`) {
		t.Errorf("marshaled locations not an array: %s", body)
	}
}

// TestBuildInputStringPlaceholders: actors like linkedin-jobs-scraper require
// keywords as a STRING — the *_str variants (or embedded form) provide that.
func TestBuildInputStringPlaceholders(t *testing.T) {
	tpl := map[string]any{
		"keywords":  "{{keywords_str}}",
		"location":  "{{locations_str}}",
		"search_q":  "jobs for {{keywords}}",
		"pageCount": 1,
	}
	in := BuildInput(testRequest(), tpl)

	if k, _ := in["keywords"].(string); k != "golang, go developer" {
		t.Errorf("keywords = %v (%T), want comma-separated string", in["keywords"], in["keywords"])
	}
	if l, _ := in["location"].(string); l != "Lagos, Remote" {
		t.Errorf("location = %v (%T), want comma-separated string", in["location"], in["location"])
	}
	if s, _ := in["search_q"].(string); s != "jobs for golang, go developer" {
		t.Errorf("search_q = %q, want embedded interpolation", in["search_q"])
	}
}

func TestBuildInputTemplate(t *testing.T) {
	tpl := map[string]any{
		"startUrls": []any{"https://www.reddit.com/search/?q={{query}}"},
		"keywords":  "{{keywords}}", // exact match → array
		"count":     5,              // non-string values pass through untouched
	}
	in := BuildInput(testRequest(), tpl)

	urls, ok := in["startUrls"].([]any)
	if !ok || len(urls) != 1 {
		t.Fatalf("startUrls = %v", in["startUrls"])
	}
	want := "https://www.reddit.com/search/?q=golang go developer"
	if urls[0] != want {
		t.Errorf("startUrls[0] = %q, want %q", urls[0], want)
	}
	if kw, ok := in["keywords"].([]string); !ok || len(kw) != 2 {
		t.Errorf("keywords = %v, want array from exact-match placeholder", in["keywords"])
	}
	if in["count"] != 5 {
		t.Errorf("count = %v, want 5 (passthrough)", in["count"])
	}
}

func TestBuildInputEscapesJSON(t *testing.T) {
	// A keyword containing a quote must not produce invalid JSON.
	req := SearchRequest{JobType: "Dev", Keywords: []string{`say "hi"`}}
	in := BuildInput(req, nil)

	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("built input is not valid JSON: %v", err)
	}
	_ = body
}

// TestApifyAccepts201: Apify returns 201 Created for successful synchronous
// runs — the backend used to reject these and throw away real job data.
func TestApifyAccepts201(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`[{
			"id":"1","jobTitle":"Senior Golang Developer","companyName":"Acme",
			"location":"Remote","jobUrl":"https://example.com/jobs/1",
			"postedAt":"2026-10-19T09:00:00Z","description":"Hiring now, apply."
		}]`))
	}))
	defer srv.Close()

	old := apifyBaseURL
	apifyBaseURL = srv.URL
	defer func() { apifyBaseURL = old }()

	src := NewApify("job-search", "hirebase/job-search", "test-token", nil)
	posts, err := src.Search(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Search() error = %v (201 must be treated as success)", err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}
	if posts[0].JobTitle != "Senior Golang Developer" || posts[0].CompanyName != "Acme" {
		t.Errorf("normalization failed: %+v", posts[0])
	}
	if posts[0].PostedAt == nil {
		t.Error("postedAt not parsed")
	}
}

func TestApifyRejectsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Input is not valid"}}`))
	}))
	defer srv.Close()

	old := apifyBaseURL
	apifyBaseURL = srv.URL
	defer func() { apifyBaseURL = old }()

	src := NewApify("broken", "some/actor", "test-token", nil)
	if _, err := src.Search(context.Background(), testRequest()); err == nil {
		t.Fatal("Search() error = nil, want error for 400")
	}
}

func TestApifyRequiresToken(t *testing.T) {
	src := NewApify("no-token", "some/actor", "", nil)
	if _, err := src.Search(context.Background(), testRequest()); err == nil {
		t.Fatal("Search() error = nil, want error when APIFY_TOKEN missing")
	}
}

// TestApifySelfHealsTypeErrors: when Apify says "Field input.keywords must be
// string" the source must coerce and retry WITHOUT any APIFY_INPUTS config.
func TestApifySelfHealsTypeErrors(t *testing.T) {
	var calls int
	var firstInput, secondInput string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls++
		if calls == 1 {
			firstInput = string(body)
			// Apify validation: our default sends an array, actor wants string.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid-input","message":"Input is not valid: Field input.keywords must be string"}}`))
			return
		}
		secondInput = string(body)
		_, _ = w.Write([]byte(`[{"id":"1","jobTitle":"Go Dev","companyName":"X","location":"Remote","jobUrl":"https://e.com/1","postedAt":"2026-10-19T09:00:00Z"}]`))
	}))
	defer srv.Close()

	old := apifyBaseURL
	apifyBaseURL = srv.URL
	defer func() { apifyBaseURL = old }()

	src := NewApify("linkedin-jobs-scraper", "curious_coder/linkedin-jobs-scraper", "tok", nil)
	posts, err := src.Search(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Search() error = %v (should self-heal and retry)", err)
	}
	if calls != 2 {
		t.Errorf("server called %d times, want 2 (one coerced retry)", calls)
	}
	if !strings.Contains(firstInput, `"keywords":["golang","go developer"]`) {
		t.Errorf("first request should send array keywords: %s", firstInput)
	}
	if !strings.Contains(secondInput, `"keywords":"golang, go developer"`) {
		t.Errorf("retry should send string keywords: %s", secondInput)
	}
	if len(posts) != 1 {
		t.Errorf("got %d posts, want 1", len(posts))
	}
}

func TestParseInputTypeError(t *testing.T) {
	body := `{"error":{"type":"invalid-input","message":"Input is not valid: Field input.keywords must be array"}}`
	field, index, want, ok := parseInputTypeError(body)
	if !ok || field != "keywords" || index != -1 || want != "array" {
		t.Errorf("got (%q, %d, %q, %v), want (keywords, -1, array, true)", field, index, want, ok)
	}

	// Indexed element error (Apify RequestList shape).
	body = `{"error":{"type":"invalid-input","message":"Input is not valid: Field input.startUrls.0 must be object"}}`
	field, index, want, ok = parseInputTypeError(body)
	if !ok || field != "startUrls" || index != 0 || want != "object" {
		t.Errorf("got (%q, %d, %q, %v), want (startUrls, 0, object, true)", field, index, want, ok)
	}

	if _, _, _, ok := parseInputTypeError(`{"error":{"message":"Actor run did not succeed"}}`); ok {
		t.Error("unrelated errors must not be treated as type errors")
	}
}

func TestCoerceInput(t *testing.T) {
	// array → string
	in := map[string]any{"keywords": []string{"golang", "go dev"}}
	out, changed := coerceInput(in, "keywords", -1, "string")
	if !changed || out["keywords"] != "golang, go dev" {
		t.Errorf("array→string: %v changed=%v", out["keywords"], changed)
	}

	// string → array (comma-separated)
	in = map[string]any{"keywords": "golang,  go dev ,"}
	out, changed = coerceInput(in, "keywords", -1, "array")
	if !changed {
		t.Fatal("string→array not changed")
	}
	if arr, ok := out["keywords"].([]string); !ok || len(arr) != 2 || arr[0] != "golang" {
		t.Errorf("string→array = %v", out["keywords"])
	}

	// string element → {"url": ...} object (Apify RequestList shape)
	in = map[string]any{"startUrls": []any{"https://reddit.com/search/?q=x"}}
	out, changed = coerceInput(in, "startUrls", 0, "object")
	if !changed {
		t.Fatal("startUrls element wrap not changed")
	}
	if obj, ok := out["startUrls"].([]any)[0].(map[string]any); !ok || obj["url"] != "https://reddit.com/search/?q=x" {
		t.Errorf("startUrls[0] = %v, want {url: ...}", out["startUrls"].([]any)[0])
	}

	// no-op conversions
	if _, changed := coerceInput(map[string]any{"keywords": "x"}, "keywords", -1, "string"); changed {
		t.Error("string→string should not be a change")
	}
	if _, changed := coerceInput(map[string]any{"other": 1}, "keywords", -1, "array"); changed {
		t.Error("missing field should not be a change")
	}
}

// TestApifySelfHealsStartUrls: "Field input.startUrls.0 must be object" must
// wrap plain URL strings as {"url": ...} and retry.
func TestApifySelfHealsStartUrls(t *testing.T) {
	var calls int
	var secondInput string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid-input","message":"Input is not valid: Field input.startUrls.0 must be object, Items in input.startUrls at positions [0] do not contain valid URLs"}}`))
			return
		}
		secondInput = string(body)
		_, _ = w.Write([]byte(`[{"id":"1","title":"Post","url":"https://reddit.com/1","createdAt":"2026-10-19T09:00:00Z"}]`))
	}))
	defer srv.Close()

	old := apifyBaseURL
	apifyBaseURL = srv.URL
	defer func() { apifyBaseURL = old }()

	tpl := map[string]any{"startUrls": []any{"https://www.reddit.com/search/?q=golang"}}
	src := NewApify("reddit", "trudax/reddit-scraper-lite", "tok", tpl)
	posts, err := src.Search(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Search() error = %v (should wrap startUrls and retry)", err)
	}
	if calls != 2 {
		t.Errorf("server called %d times, want 2", calls)
	}
	if !strings.Contains(secondInput, `"url":"https://www.reddit.com/search/?q=golang"`) {
		t.Errorf("retry should send {url:...} objects: %s", secondInput)
	}
	if len(posts) != 1 {
		t.Errorf("got %d posts, want 1", len(posts))
	}
}

// TestKnownActorInputs: presets must resolve without any APIFY_INPUTS JSON.
func TestKnownActorInputs(t *testing.T) {
	if tpl := KnownActorInput("hirebase/job-search"); tpl == nil {
		t.Error("job-search preset missing")
	}
	if tpl := KnownActorInput("trudax~reddit-scraper-lite"); tpl == nil {
		t.Error("reddit preset missing (~ form lookup)")
	}
	if fn := KnownActorInputFn("curious_coder/linkedin-jobs-scraper"); fn == nil {
		t.Error("linkedin computed-input preset missing")
	}
	if fn := KnownActorInputFn("trudax/reddit-scraper-lite"); fn != nil {
		t.Error("reddit should be a template preset, not computed")
	}
	if tpl := KnownActorInput("unknown/actor"); tpl != nil || KnownActorInputFn("unknown/actor") != nil {
		t.Error("unknown actor should have no preset (default input applies)")
	}
}

// TestLinkedInInputOneURLEach: LinkedIn treats a comma-joined keyword blob as
// one sloppy query (it returned "Software Engineer" for a figma designer
// search) — the preset must emit one precise search URL per keyword.
func TestLinkedInInputOneURLEach(t *testing.T) {
	in := linkedinInput(testRequest()) // keywords: golang, go developer; locations: Lagos, Remote

	urls, ok := in["urls"].([]any)
	if !ok || len(urls) != 2 {
		t.Fatalf("urls = %v (%T), want 2 entries", in["urls"], in["urls"])
	}
	if urls[0] != "https://www.linkedin.com/jobs/search/?keywords=golang&location=Lagos" {
		t.Errorf("urls[0] = %q", urls[0])
	}
	if urls[1] != "https://www.linkedin.com/jobs/search/?keywords=go+developer&location=Lagos" {
		t.Errorf("urls[1] = %q", urls[1])
	}
	if in["limitPerSource"] != 50 {
		t.Errorf("limitPerSource = %v, want 50", in["limitPerSource"])
	}
	// 24h lookback → last-24h date filter
	if in["datePosted"] != "r86400" {
		t.Errorf("datePosted = %v, want r86400 (24h)", in["datePosted"])
	}

	// No locations → no location param; >7d lookback → coarser filter.
	in = linkedinInput(SearchRequest{
		Keywords: []string{"figma designer"}, Lookback: 14 * 24 * time.Hour,
	})
	urls = in["urls"].([]any)
	if urls[0] != "https://www.linkedin.com/jobs/search/?keywords=figma+designer" {
		t.Errorf("no-location URL = %q", urls[0])
	}
	if in["datePosted"] != "r2592000" {
		t.Errorf("datePosted = %v, want r2592000 (30d)", in["datePosted"])
	}
}

// TestJobSearchPreset: hirebase wants maxResults (not maxItems) and takes
// postedWithinDays as a NUMBER built from the lookback.
func TestJobSearchPreset(t *testing.T) {
	req := testRequest() // 24h lookback
	in := BuildInput(req, KnownActorInput("hirebase/job-search"))

	if _, ok := in["postedWithinDays"].(int); !ok {
		t.Errorf("postedWithinDays = %v (%T), want number", in["postedWithinDays"], in["postedWithinDays"])
	}
	if in["maxResults"] != 100 {
		t.Errorf("maxResults = %v, want 100", in["maxResults"])
	}
	if _, ok := in["keywords"].([]string); !ok {
		t.Errorf("keywords = %v (%T), want array", in["keywords"], in["keywords"])
	}
	// 72h lookback rounds UP to 3 days.
	in = BuildInput(SearchRequest{Keywords: []string{"x"}, Lookback: 72 * time.Hour},
		KnownActorInput("hirebase/job-search"))
	if in["postedWithinDays"] != 3 {
		t.Errorf("postedWithinDays(72h) = %v, want 3", in["postedWithinDays"])
	}
}

// TestRedditPresetUsesSearches: reddit-scraper-lite searches natively via
// `searches` (array) — no startUrls needed, so no 400 on URL shape.
func TestRedditPresetUsesSearches(t *testing.T) {
	in := BuildInput(testRequest(), KnownActorInput("trudax/reddit-scraper-lite"))

	kw, ok := in["searches"].([]string)
	if !ok || len(kw) != 2 || kw[0] != "golang" {
		t.Errorf("searches = %v (%T), want keyword array", in["searches"], in["searches"])
	}
	if in["searchPosts"] != true {
		t.Errorf("searchPosts = %v, want true", in["searchPosts"])
	}
	if _, exists := in["startUrls"]; exists {
		t.Error("preset must not send startUrls (searches is the supported path)")
	}
}

// TestPresetsAppliedWithoutEnvJSON: BuildRegistry attaches presets by actor ID.
func TestPresetsAppliedWithoutEnvJSON(t *testing.T) {
	cfg := &config.Config{
		ApifyActors: []config.NameValue{
			{Name: "linkedin-jobs-scraper", Value: "curious_coder/linkedin-jobs-scraper"},
			{Name: "job-search", Value: "hirebase/job-search"},
		},
		ApifyToken: "tok",
	}
	reg := BuildRegistry(cfg)

	li, ok := reg.Get("linkedin-jobs-scraper")
	if !ok {
		t.Fatal("linkedin source not registered")
	}
	if in := li.(*Apify).buildInput(testRequest()); len(in["urls"].([]any)) != 2 {
		t.Errorf("linkedin computed preset not attached: %v", in)
	}

	js, ok := reg.Get("job-search")
	if !ok {
		t.Fatal("job-search source not registered")
	}
	if tpl := js.(*Apify).inputTpl; tpl == nil || tpl["maxResults"] != 100 {
		t.Errorf("job-search template preset not attached: %v", tpl)
	}
}

// TestNormalizeHirebaseItem: hirebase uses applicationLink/jobBoardLink —
// not url/link — so its items used to be dropped entirely (raw:0).
func TestNormalizeHirebaseItem(t *testing.T) {
	item := map[string]any{
		"id":              "abc",
		"jobTitle":        "Lead Software Developer",
		"companyName":     "Pinewood.AI",
		"applicationLink": "https://jobs.workable.com/view/123",
		"jobBoardLink":    "https://job-boards.greenhouse.io/x/jobs/1",
		"datePosted":      "2026-10-19",
		"location":        "London, England, United Kingdom",
		"locationType":    "Hybrid",
		"jobType":         "Full Time",
		"descriptionText": "Golang developer wanted.",
		"skills":          []any{"Golang", "Docker"},
	}
	post, ok := normalizeApifyItem("job-search", item)
	if !ok {
		t.Fatal("hirebase item dropped — applicationLink alias missing")
	}
	if post.SourceURL != "https://jobs.workable.com/view/123" {
		t.Errorf("SourceURL = %q, want applicationLink", post.SourceURL)
	}
	if post.Description != "Golang developer wanted." {
		t.Errorf("Description = %q, want descriptionText", post.Description)
	}
	if post.WorkMode != "Hybrid" {
		t.Errorf("WorkMode = %q, want locationType", post.WorkMode)
	}
	if post.EmploymentType != "Full Time" {
		t.Errorf("EmploymentType = %q, want jobType", post.EmploymentType)
	}
	if post.PostedAt == nil {
		t.Error("datePosted not parsed")
	}
}

// TestNormalizeLinkedInItem: LinkedIn puts descriptions in descriptionText
// (not description) — previously lost, which broke keyword matching.
func TestNormalizeLinkedInItem(t *testing.T) {
	item := map[string]any{
		"id":              "3692563200",
		"link":            "https://www.linkedin.com/jobs/view/3692563200",
		"title":           "Frontend Developer",
		"companyName":     "Acme",
		"location":        "London, United Kingdom",
		"postedAt":        "2026-10-19",
		"descriptionText": "React and Figma design systems.",
		"employmentType":  "Full-time",
	}
	post, ok := normalizeApifyItem("linkedin-jobs-scraper", item)
	if !ok {
		t.Fatal("linkedin item dropped")
	}
	if post.Description != "React and Figma design systems." {
		t.Errorf("Description = %q, want descriptionText", post.Description)
	}
	if post.SourceURL != "https://www.linkedin.com/jobs/view/3692563200" {
		t.Errorf("SourceURL = %q", post.SourceURL)
	}
}

// TestParseRelativeTime: LinkedIn/social scrapers often return "3 days ago"
// instead of absolute timestamps — these must become usable dates, not
// "unrecognized time" rejections.
func TestParseRelativeTime(t *testing.T) {
	cases := []struct {
		in      string
		wantAge time.Duration
		tol     time.Duration
	}{
		{"3 days ago", 72 * time.Hour, time.Minute},
		{"30+ days ago", 720 * time.Hour, time.Minute},
		{"2 hours ago", 2 * time.Hour, time.Minute},
		{"yesterday", 24 * time.Hour, time.Minute},
		{"just now", 0, time.Minute},
		{"2 weeks ago", 14 * 24 * time.Hour, time.Minute},
		{"1 month ago", 30 * 24 * time.Hour, 48 * time.Hour},
	}
	for _, c := range cases {
		got, err := parseTime(c.in)
		if err != nil {
			t.Errorf("parseTime(%q) error = %v", c.in, err)
			continue
		}
		age := time.Since(got)
		if diff := age - c.wantAge; diff < -c.tol || diff > c.tol {
			t.Errorf("parseTime(%q) age = %v, want ~%v", c.in, age, c.wantAge)
		}
	}
}
