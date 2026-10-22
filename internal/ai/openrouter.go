// Package ai wraps the OpenRouter chat-completions API for two jobs:
// enriching admin-entered job types into search profiles, and classifying
// ambiguous posts as real job listings.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"qx-scraper/internal/model"
)

const (
	defaultTimeout = 45 * time.Second
	// minConfidence below which AI-classified posts are rejected.
	minConfidence = 0.7
)

// openRouterURL is the chat completions endpoint. It is a var so tests can
// point it at an httptest server.
var openRouterURL = "https://openrouter.ai/api/v1/chat/completions"

// Client calls OpenRouter. When no API key is configured the client is a no-op
// and the pipeline falls back to rules only.
type Client struct {
	apiKey string
	model  string
	http   *http.Client
	log    *slog.Logger
}

// New creates an OpenRouter client. Empty apiKey disables AI features.
func New(apiKey, model string, log *slog.Logger) *Client {
	if model == "" {
		model = "openai/gpt-4o-mini"
	}
	return &Client{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: defaultTimeout},
		log:    log,
	}
}

// Enabled reports whether an API key is configured.
func (c *Client) Enabled() bool { return c.apiKey != "" }

// Profile is the AI-generated search profile for a job type.
type Profile struct {
	Keywords        []string `json:"keywords"`
	ExcludeKeywords []string `json:"exclude_keywords"`
	Locations       []string `json:"locations"`
	EmploymentType  string   `json:"employment_type"`
	WorkMode        string   `json:"work_mode"`
	Sources         []string `json:"sources"`
	LookbackHours   int      `json:"lookback_hours"`
	SuggestedCron   string   `json:"suggested_cron"`
}

// EnrichJobType asks the model to expand a job type into a full search profile.
// availableSources limits the "sources" field to sources that actually exist.
func (c *Client) EnrichJobType(ctx context.Context, jobType string, availableSources []string) (*Profile, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("AI not configured (OPENROUTER_API_KEY missing)")
	}

	system := `You are a job-search configuration assistant. Given a job type, produce a JSON object with:
- "keywords": 10-20 lowercase search phrases. They are matched LITERALLY against job title + company + location + description: punctuation is ignored, but every word of the phrase must appear together. So:
  * Write phrases exactly as employers write them in real job ads (research-grade accuracy matters more than cleverness), e.g. "frontend developer".
  * Spelling/spacing variants do NOT match each other — add each as a SEPARATE keyword:
    "frontend developer" AND "front end developer"; "full stack developer" AND "fullstack developer".
  * Add the "... engineer" variant next to "... developer" when both are common: "golang developer", "golang engineer", "python developer", "python engineer".
  * Add the bare technology word when it is unambiguous, to catch titles that lack your phrase ("Golang Engineer", "Solidity Engineer") plus descriptions that only mention the tool: "golang", "figma", "solidity".
  * For design roles use the titles people actually post: "ui ux designer", "product designer", "ux designer" — not invented phrases like "figma designer".
  * 1-4 words per keyword. Aim for ~20 high-signal keywords; each keyword costs one extra search per run, so never return near-duplicates.
- "exclude_keywords": 3-6 words for posts to skip (spam, unwanted seniority).
  WARNING: a post is rejected if the word appears ANYWHERE in it, and excludes beat keywords. An exclude word that sits inside a keyword (e.g. "contract" vs a "smart contract" keyword) silently kills every matching job. Only return exclude words that do not appear in any of your keywords.
- "locations": 0-5 common locations for this role, or empty for worldwide/remote
- "employment_type": one of full_time, part_time, contract, internship, any
- "work_mode": one of remote, hybrid, onsite, any
- "sources": pick ONLY from this exact list of available sources: ` + strings.Join(availableSources, ", ") + `
- "lookback_hours": hours to search back (24-168)
- "suggested_cron": a 5-field cron expression for when to run this search (e.g. "0 */6 * * *" = every 6 hours)
Respond with JSON only, no prose.`

	user := fmt.Sprintf("Job type: %s", jobType)

	body, err := c.chatJSON(ctx, system, user)
	if err != nil {
		return nil, err
	}

	var p Profile
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parse AI profile: %w (raw: %s)", err, truncate(string(body), 500))
	}

	// Sanitize / validate AI output.
	// Expand first, then cap — variants must survive the 20-keyword limit.
	p.Keywords = expandKeywordVariants(p.Keywords)
	p.Keywords = cleanList(p.Keywords, 20)
	p.ExcludeKeywords = cleanList(p.ExcludeKeywords, 15)
	p.ExcludeKeywords = dropExcludedConflicts(p.Keywords, p.ExcludeKeywords)
	p.ExcludeKeywords = cleanList(p.ExcludeKeywords, 15)
	p.Locations = cleanList(p.Locations, 10)
	if p.EmploymentType == "" {
		p.EmploymentType = "any"
	}
	if p.WorkMode == "" {
		p.WorkMode = "any"
	}
	if p.LookbackHours < 24 || p.LookbackHours > 168 {
		p.LookbackHours = 24
	}
	if len(p.Sources) == 0 {
		p.Sources = availableSources
	}
	// Never trust an AI-suggested schedule without validating it.
	if _, err := cron.ParseStandard(p.SuggestedCron); err != nil {
		p.SuggestedCron = "0 */6 * * *"
	}
	return &p, nil
}

// Classify asks the model whether an ambiguous post is a real job and to
// extract structured fields. Returns nil when the post should be rejected.
func (c *Client) Classify(ctx context.Context, post model.RawPost) (*model.Classification, error) {
	if !c.Enabled() {
		return nil, nil
	}

	system := `You are a strict job-posting classifier. Given a social media or web post, decide if it is a genuine job opportunity.
Respond with JSON only:
{"is_job": bool, "job_title": string, "company_name": string, "location": string,
 "employment_type": "full_time"|"part_time"|"contract"|"internship"|"any",
 "work_mode": "remote"|"hybrid"|"onsite"|"any",
 "description": string, "posted_at": "RFC3339 or null", "application_url": string, "confidence": 0.0-1.0}
Never invent an application_url or posted_at — use "" and null if unknown.`

	var sb strings.Builder
	fmt.Fprintf(&sb, "Source: %s\nTitle: %s\nCompany: %s\nLocation: %s\nURL: %s\nPosted: %v\n\nPost:\n%s",
		post.Source, post.JobTitle, post.CompanyName, post.Location, post.SourceURL,
		post.PostedAt, truncate(post.Description, 3000))

	body, err := c.chatJSON(ctx, system, sb.String())
	if err != nil {
		return nil, err
	}

	var cl model.Classification
	if err := json.Unmarshal(body, &cl); err != nil {
		return nil, fmt.Errorf("parse classification: %w (raw: %s)", err, truncate(string(body), 500))
	}
	if cl.Confidence < minConfidence {
		return &cl, nil // caller rejects based on confidence
	}
	return &cl, nil
}

// MinConfidence exposes the rejection threshold.
func MinConfidence() float64 { return minConfidence }

// apiError carries the OpenRouter HTTP status and body for callers to inspect.
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("openrouter returned %d: %s", e.Status, e.Body)
}

// chat performs one chat completion and returns the message content bytes.
// If the model rejects response_format=json_object (some models do), it
// retries once without it — the prompts already demand JSON-only output.
func (c *Client) chat(ctx context.Context, system, user string) ([]byte, error) {
	body, err := c.doChat(ctx, system, user, true)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest {
			return c.doChat(ctx, system, user, false)
		}
	}
	return body, err
}

// chatJSON calls chat until the model returns valid JSON, retrying with
// backoff on transient failures (429/5xx) and on truncated/malformed output,
// which free-tier models produce occasionally.
func (c *Client) chatJSON(ctx context.Context, system, user string) ([]byte, error) {
	const maxAttempts = 3
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			// Simple linear backoff: 1s, 2s — respects ctx cancellation.
			select {
			case <-time.After(time.Duration(attempt-1) * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		body, err := c.chat(ctx, system, user)
		if err != nil {
			var apiErr *apiError
			if errors.As(err, &apiErr) && !retryableStatus(apiErr.Status) {
				return nil, err // 401/402/404 etc. — retrying won't help
			}
			lastErr = err
			continue
		}
		if json.Valid(body) {
			return body, nil
		}
		// Truncated or malformed JSON — retry for a fresh sample.
		lastErr = fmt.Errorf("model returned invalid/truncated JSON: %s", truncate(string(body), 200))
		c.log.Warn("ai returned invalid JSON, retrying",
			"attempt", attempt, "max", maxAttempts, "model", c.model)
	}
	return nil, lastErr
}

// retryableStatus reports whether an OpenRouter status code is worth retrying.
func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// doChat performs a single HTTP round trip to OpenRouter.
func (c *Client) doChat(ctx context.Context, system, user string, jsonFormat bool) ([]byte, error) {
	payload := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.2,
		"max_tokens":  2000,
	}
	// Ask for JSON; most models on OpenRouter honour this via json_object mode.
	if jsonFormat {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openRouterURL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("HTTP-Referer", "https://github.com/qx-scraper")
	req.Header.Set("X-Title", "QX-Scraper")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &apiError{Status: resp.StatusCode, Body: truncate(string(respBody), 300)}
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse openrouter envelope: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("openrouter returned no choices")
	}
	return []byte(strings.TrimSpace(stripFences(parsed.Choices[0].Message.Content))), nil
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

func cleanList(in []string, max int) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) >= max {
			break
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// keywordVariantPairs lists spellings that mean the same role but never match
// each other in literal keyword filtering ("frontend developer" is not
// contained in a "Front-End Developer" title). Adding one implies the other.
var keywordVariantPairs = [][2]string{
	{"frontend developer", "front end developer"},
	{"frontend engineer", "front end engineer"},
	{"front end developer", "frontend developer"},
	{"front end engineer", "frontend engineer"},
	{"full stack developer", "fullstack developer"},
	{"full stack engineer", "fullstack engineer"},
	{"fullstack developer", "full stack developer"},
	{"fullstack engineer", "full stack engineer"},
	{"nodejs developer", "node js developer"},
	{"node js developer", "nodejs developer"},
	{"web3 developer", "web 3 developer"},
	{"web 3 developer", "web3 developer"},
}

// bareTechWords are technology names worth having on their own: titles like
// "Golang Engineer" or "Solidity Smart Contracts Engineer" don't contain
// "golang developer", but descriptions mention the bare tool.
var bareTechWords = map[string]bool{
	"golang": true, "solidity": true, "figma": true,
	"react": true, "rust": true, "web3": true,
}

// expandKeywordVariants guarantees the common spelling/spacing variants of
// each keyword are present, and adds a bare technology keyword for role
// phrases like "golang developer". Output is order-preserving and deduped;
// the caller's cap still applies afterwards.
func expandKeywordVariants(keywords []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(keywords)+6)
	add := func(k string) {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, k)
	}

	for _, k := range keywords {
		add(k)
	}
	for _, k := range keywords {
		lk := strings.ToLower(strings.TrimSpace(k))
		for _, pair := range keywordVariantPairs {
			if lk == pair[0] {
				add(pair[1])
			}
		}
		// "golang developer" → also index bare "golang".
		if i := strings.LastIndex(lk, " "); i > 0 {
			if bare := lk[:i]; bareTechWords[bare] {
				add(bare)
			}
		}
	}
	return out
}

// dropExcludedConflicts removes exclude terms that appear inside a keyword:
// excludes beat keywords during filtering, so excluding "contract" would
// silently kill a "smart contract" keyword (and every job it finds).
func dropExcludedConflicts(keywords, excludes []string) []string {
	out := make([]string, 0, len(excludes))
	for _, e := range excludes {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		conflict := false
		for _, k := range keywords {
			if strings.Contains(k, e) {
				conflict = true
				break
			}
		}
		if !conflict {
			out = append(out, e)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
