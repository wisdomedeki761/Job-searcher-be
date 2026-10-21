package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aj-scraper/internal/model"
)

// apifyBaseURL is a var so tests can point it at an httptest server.
var apifyBaseURL = "https://api.apify.com"

// BuildInput assembles the actor input for a search run.
//
// When tpl is nil a generic default is used. Otherwise tpl is a per-source
// template whose string values may contain placeholders substituted at run
// time. Two forms exist:
//
//   - Exact match ("{{keywords}}") — replaced by the NATIVE value: an array
//     for {{keywords}}/{{locations}}, a string for the rest.
//   - Embedded ("q={{query}}") — replaced by text (arrays become CSV),
//     JSON-escaped so the template stays valid JSON.
//
// Placeholders: {{query}} {{keywords}} {{keywords_str}} {{locations}}
// {{locations_str}} {{job_type}} {{employment_type}} {{work_mode}}
// {{since}} {{lookback_hours}}
//
// Example (from APIFY_INPUTS):
//
//	{"startUrls": ["https://www.reddit.com/search/?q={{query}}"], "maxItems": 50}
func BuildInput(req SearchRequest, tpl map[string]any) map[string]any {
	ph := placeholders(req)
	if tpl == nil {
		tpl = defaultInputTpl
	}
	out, _ := substitute(tpl, ph).(map[string]any)
	return out
}

// defaultInputTpl: query as a string, keywords/locations as arrays.
// This is the shape job-search (and most job actors) require — LinkedIn's
// actor, which wants strings, is configured via APIFY_INPUTS instead.
var defaultInputTpl = map[string]any{
	"query":     "{{query}}",
	"keywords":  "{{keywords}}",
	"locations": "{{locations}}",
	"maxItems":  100,
}

// substitution carries both the native value (for exact-match placeholders)
// and its text form (for interpolation inside larger strings).
type substitution struct {
	typed  any    // string or []string — becomes a JSON string/array
	asText string // CSV/joined text used when embedded in a larger string
}

func placeholders(req SearchRequest) map[string]substitution {
	kw := req.Keywords
	if len(kw) == 0 {
		kw = []string{req.JobType}
	}
	locs := req.Locations
	if locs == nil {
		locs = []string{}
	}
	query := req.Query() // joined keywords, or job type when no keywords

	str := func(s string) substitution { return substitution{typed: s, asText: s} }
	arr := func(list []string) substitution {
		return substitution{typed: list, asText: strings.Join(list, ", ")}
	}
	// num becomes a JSON number on exact match (e.g. "postedWithinDays").
	num := func(n int) substitution {
		return substitution{typed: n, asText: strconv.Itoa(n)}
	}

	days := int(req.Lookback.Hours()) / 24
	if int(req.Lookback.Hours())%24 != 0 {
		days++
	}
	if days < 1 {
		days = 1
	}

	return map[string]substitution{
		"{{query}}":           str(query),
		"{{keywords}}":        arr(kw),
		"{{keywords_str}}":    str(strings.Join(kw, ", ")),
		"{{locations}}":       arr(locs),
		"{{locations_str}}":   str(strings.Join(locs, ", ")),
		"{{job_type}}":        str(req.JobType),
		"{{employment_type}}": str(req.EmploymentType),
		"{{work_mode}}":       str(req.WorkMode),
		"{{since}}":           str(time.Now().Add(-req.Lookback).UTC().Format(time.RFC3339)),
		"{{lookback_hours}}":  str(fmt.Sprintf("%d", int(req.Lookback.Hours()))),
		"{{lookback_days}}":   num(days),
	}
}

// substitute walks a JSON structure replacing placeholder strings.
func substitute(v any, ph map[string]substitution) any {
	switch t := v.(type) {
	case string:
		// Exact match → native typed value (array stays an array).
		if s, ok := ph[t]; ok {
			return s.typed
		}
		// Embedded → text interpolation, JSON-escaped.
		for placeholder, s := range ph {
			if strings.Contains(t, placeholder) {
				t = strings.ReplaceAll(t, placeholder, jsonEscape(s.asText))
			}
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = substitute(val, ph)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = substitute(val, ph)
		}
		return out
	default:
		return v
	}
}

// jsonEscape escapes a value for safe embedding inside a JSON string.
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	// marshal returns a quoted string; strip the surrounding quotes.
	return strings.TrimSuffix(strings.TrimPrefix(string(b), `"`), `"`)
}

// Apify collects jobs through the Apify API (run-sync-get-dataset-items).
type Apify struct {
	name     string
	actorID  string
	token    string
	inputTpl map[string]any                     // optional per-source input template (see BuildInput)
	inputFn  func(SearchRequest) map[string]any // optional computed input (overrides inputTpl)
	client   *http.Client
}

// NewApify creates an Apify-backed source. actorID may use "/" or "~".
// inputTpl may be nil — a generic default input is used then.
func NewApify(name, actorID, token string, inputTpl map[string]any) *Apify {
	return &Apify{
		name:     name,
		actorID:  strings.ReplaceAll(actorID, "/", "~"),
		token:    token,
		inputTpl: inputTpl,
		client:   &http.Client{Timeout: 250 * time.Second},
	}
}

func (a *Apify) Name() string { return a.name }

// buildInput assembles the actor input: a computed preset wins, then a
// placeholder template, then the generic default.
func (a *Apify) buildInput(req SearchRequest) map[string]any {
	if a.inputFn != nil {
		return a.inputFn(req)
	}
	return BuildInput(req, a.inputTpl)
}

// Search runs the actor synchronously and normalizes dataset items.
func (a *Apify) Search(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
	if a.token == "" {
		return nil, fmt.Errorf("source %q: APIFY_TOKEN not configured", a.name)
	}

	input := a.buildInput(req)
	raw, status, err := a.postInput(ctx, input)
	if err != nil {
		return nil, err
	}

	// Self-healing: Apify's validation error names the field and expected
	// type ("Field input.keywords must be string"). Coerce it and retry once
	// so sources work without hand-written APIFY_INPUTS JSON.
	if status == http.StatusBadRequest {
		if field, index, wantType, ok := parseInputTypeError(string(raw)); ok {
			if coerced, changed := coerceInput(input, field, index, wantType); changed {
				raw, status, err = a.postInput(ctx, coerced)
				if err != nil {
					return nil, err
				}
			}
		}
	}

	// Apify returns 201 Created for successful synchronous runs (and 200 for
	// plain dataset reads) — accept any 2xx, not just 200.
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("source %q: apify returned %d: %s", a.name, status, truncate(string(raw), 300))
	}

	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("source %q: parse dataset: %w", a.name, err)
	}

	posts := make([]model.RawPost, 0, len(items))
	for _, item := range items {
		if p, ok := normalizeApifyItem(a.name, item); ok {
			posts = append(posts, p)
		}
	}
	return posts, nil
}

// postInput POSTs the input to the actor and returns body + status code.
// Transport failures are errors; HTTP status codes are not.
func (a *Apify) postInput(ctx context.Context, input map[string]any) ([]byte, int, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, 0, err
	}
	endpoint := fmt.Sprintf("%s/v2/acts/%s/run-sync-get-dataset-items?token=%s",
		apifyBaseURL, url.PathEscape(a.actorID), url.QueryEscape(a.token))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("source %q: %w", a.name, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("source %q: read response: %w", a.name, err)
	}
	return raw, resp.StatusCode, nil
}

// inputTypeErrorRe matches Apify validation messages like:
//
//	Input is not valid: Field input.keywords must be string
//	Input is not valid: Field input.startUrls.0 must be object
var inputTypeErrorRe = regexp.MustCompile(`Field input\.([A-Za-z0-9_-]+)(?:\.(\d+))? must be (string|array|object)`)

// parseInputTypeError extracts the field name, optional array index and the
// expected type.
func parseInputTypeError(body string) (field string, index int, wantType string, ok bool) {
	m := inputTypeErrorRe.FindStringSubmatch(body)
	if m == nil {
		return "", -1, "", false
	}
	index = -1
	if m[2] != "" {
		if n, err := strconv.Atoi(m[2]); err == nil {
			index = n
		}
	}
	return m[1], index, m[3], true
}

// coerceInput converts input[field] to the expected type.
// It reports changed=true only when a real conversion happened.
func coerceInput(input map[string]any, field string, index int, wantType string) (map[string]any, bool) {
	v, exists := input[field]
	if !exists {
		// Field not in our input — inject it from sibling context is unsafe,
		// so nothing to coerce.
		return input, false
	}

	// "Field input.startUrls.0 must be object": actors using Apify's standard
	// RequestList shape want [{"url": "..."}] where users/templates often write
	// plain strings.
	if wantType == "object" && index >= 0 {
		if arr, ok := v.([]any); ok && index < len(arr) {
			if s, isStr := arr[index].(string); isStr {
				arr[index] = map[string]any{"url": s}
				return input, true
			}
		}
		return input, false
	}

	switch wantType {
	case "string":
		switch t := v.(type) {
		case []string:
			input[field] = strings.Join(t, ", ")
		case []any:
			parts := make([]string, 0, len(t))
			for _, e := range t {
				parts = append(parts, fmt.Sprintf("%v", e))
			}
			input[field] = strings.Join(parts, ", ")
		default:
			return input, false // already a non-array value
		}
	case "array":
		s, isStr := v.(string)
		if !isStr {
			return input, false // already a non-string value
		}
		var parts []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) == 0 {
			parts = []string{strings.TrimSpace(s)}
		}
		input[field] = parts
	default:
		return input, false
	}
	return input, true
}

// normalizeApifyItem maps an actor dataset item to RawPost using common
// field aliases across popular job-scraper actors. Unknown shapes are skipped.
func normalizeApifyItem(source string, item map[string]any) (model.RawPost, bool) {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := item[k]; ok {
				if s, ok := toString(v); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		}
		return ""
	}

	post := model.RawPost{
		Source:         source,
		SourcePostID:   get("id", "jobId", "postId"),
		SourceURL:      get("url", "sourceUrl", "link", "jobUrl", "applyUrl", "applicationLink", "jobBoardLink"),
		JobTitle:       get("title", "jobTitle", "position", "name"),
		CompanyName:    get("company", "companyName", "employer"),
		Location:       get("location", "jobLocation", "jobLocationText", "city", "region"),
		EmploymentType: get("employmentType", "jobType", "type"),
		WorkMode:       get("workMode", "locationType", "workplaceType", "remote", "schedule"),
		Description:    get("description", "descriptionText", "snippet", "text", "body"),
		ApplicationURL: get("applyUrl", "applicationUrl", "applicationLink", "jobUrl", "url"),
	}
	if posted := get("postedAt", "publishedAt", "date", "createdAt",
		"formattedDate", "datePosted", "posted_date", "relativeTime", "pubDate"); posted != "" {
		if t, err := parseTime(posted); err == nil {
			post.PostedAt = &t
		}
	}

	if post.JobTitle == "" || post.SourceURL == "" {
		return model.RawPost{}, false
	}
	return post, true
}

func toString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case float64:
		// JSON numbers (e.g. timestamps)
		if t > 1e12 { // millis
			return time.UnixMilli(int64(t)).UTC().Format(time.RFC3339), true
		}
		return fmt.Sprintf("%.0f", t), true
	case bool:
		return fmt.Sprintf("%t", t), true
	}
	return "", false
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	formats := []string{
		time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05",
		"2006-01-02 15:04:05", "2006-01-02", "02 Jan 2006", "Jan 2, 2006",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	// Relative dates are common on social/LinkedIn scrapers:
	// "3 days ago", "30+ days ago", "just now", "yesterday".
	if t, ok := parseRelativeTime(s); ok {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q", s)
}

// relativeTimeRe matches e.g. "3 days ago" and "30+ days ago".
var relativeTimeRe = regexp.MustCompile(`^(\d+)\+?\s*(seconds?|minutes?|hours?|days?|weeks?|months?|years?)\s+ago$`)

func parseRelativeTime(s string) (time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))
	now := time.Now()
	switch lower {
	case "just now", "now", "today":
		return now, true
	case "yesterday":
		return now.AddDate(0, 0, -1), true
	}
	m := relativeTimeRe.FindStringSubmatch(lower)
	if m == nil {
		return time.Time{}, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 0 {
		return time.Time{}, false
	}
	unit := strings.TrimSuffix(m[2], "s")
	switch unit {
	case "second":
		return now.Add(-time.Duration(n) * time.Second), true
	case "minute":
		return now.Add(-time.Duration(n) * time.Minute), true
	case "hour":
		return now.Add(-time.Duration(n) * time.Hour), true
	case "day":
		return now.AddDate(0, 0, -n), true
	case "week":
		return now.AddDate(0, 0, -7*n), true
	case "month":
		return now.AddDate(0, -n, 0), true
	case "year":
		return now.AddDate(-n, 0, 0), true
	}
	return time.Time{}, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
