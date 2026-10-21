// Package pipeline runs one search-config execution end to end:
// collect → filter → classify → validate → dedupe → persist → record run.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"aj-scraper/internal/ai"
	"aj-scraper/internal/collector"
	"aj-scraper/internal/model"
	"aj-scraper/internal/store"
)

// Pipeline executes search configs.
type Pipeline struct {
	store      *store.Store
	registry   *collector.Registry
	ai         *ai.Client
	aiClassify bool
	log        *slog.Logger
	runTimeout time.Duration
}

// New creates a Pipeline. aiClassify toggles the AI_CLASSIFY env flag.
func New(st *store.Store, registry *collector.Registry, aiClient *ai.Client, aiClassify bool, log *slog.Logger, runTimeout time.Duration) *Pipeline {
	if runTimeout <= 0 {
		runTimeout = 30 * time.Minute
	}
	return &Pipeline{store: st, registry: registry, ai: aiClient, aiClassify: aiClassify, log: log, runTimeout: runTimeout}
}

// Summary reports what one config execution produced.
type Summary struct {
	SourcesRun   int
	NewJobs      int
	Duplicates   int
	Rejected     int
	RawCollected int
	Errors       []string
}

// RunConfig executes all sources for a search config. It is safe to call
// concurrently; a per-config guard in the scheduler prevents overlap.
func (p *Pipeline) RunConfig(parent context.Context, cfg *model.SearchConfig) (*Summary, error) {
	ctx, cancel := context.WithTimeout(parent, p.runTimeout)
	defer cancel()

	sum := &Summary{}
	sources, unknown := p.registry.Resolve(cfg.Sources)
	for _, u := range unknown {
		sum.Errors = append(sum.Errors, fmt.Sprintf("unknown source: %s", u))
	}
	if len(sources) == 0 {
		return sum, fmt.Errorf("no usable sources for config %q (available: %s)",
			cfg.JobType, strings.Join(p.registry.Names(), ", "))
	}

	req := collector.SearchRequest{
		JobType:        cfg.JobType,
		Keywords:       cfg.Keywords,
		Locations:      cfg.Locations,
		EmploymentType: cfg.EmploymentType,
		WorkMode:       cfg.WorkMode,
		Lookback:       time.Duration(cfg.LookbackHours) * time.Hour,
	}
	lookbackCutoff := time.Now().Add(-req.Lookback)

	for _, src := range sources {
		if ctx.Err() != nil {
			sum.Errors = append(sum.Errors, "cancelled")
			break
		}
		sum.SourcesRun++
		p.runSource(ctx, cfg, src, req, lookbackCutoff, sum)
	}

	// Record last_run_at on the config.
	if err := p.store.UpdateConfigRunTimes(ctx, cfg.ID, timePtr(time.Now()), nil); err != nil {
		p.log.Warn("update last_run_at failed", "config", cfg.JobType, "err", err)
	}

	var err error
	if len(sum.Errors) > 0 {
		err = errors.New(strings.Join(sum.Errors, "; "))
	}
	return sum, err
}

// runSource collects from one source, processes posts and records a run row.
func (p *Pipeline) runSource(ctx context.Context, cfg *model.SearchConfig, src collector.Source, req collector.SearchRequest, cutoff time.Time, sum *Summary) {
	run, err := p.store.StartRun(ctx, cfg.ID, src.Name())
	if err != nil {
		p.log.Error("start run record failed", "source", src.Name(), "err", err)
		return
	}

	posts, err := src.Search(ctx, req)
	if err != nil {
		p.log.Error("source search failed", "source", src.Name(), "config", cfg.JobType, "err", err)
		sum.Errors = append(sum.Errors, fmt.Sprintf("%s: %v", src.Name(), err))
		_ = p.store.FinishRun(ctx, run.ID, "failed", 0, 0, 0, 0, err.Error())
		return
	}

	created, dupes, rejected := 0, 0, 0
	reasons := map[string]int{}
	for _, post := range posts {
		if ctx.Err() != nil {
			break
		}
		outcome, reason := p.processPost(ctx, cfg, post, cutoff)
		switch outcome {
		case outcomeCreated:
			created++
		case outcomeDuplicate:
			dupes++
		default:
			rejected++
			reasons[reason]++
		}
	}

	status := "success"
	errText := ""
	if len(sum.Errors) > 0 {
		status = "partial"
		errText = strings.Join(sum.Errors, "; ")
	}
	if err := p.store.FinishRun(ctx, run.ID, status, len(posts), created, dupes, rejected, errText); err != nil {
		p.log.Warn("finish run record failed", "source", src.Name(), "err", err)
	}

	sum.RawCollected += len(posts)
	sum.NewJobs += created
	sum.Duplicates += dupes
	sum.Rejected += rejected

	if rejected > 0 {
		p.log.Info("rejections by reason",
			"source", src.Name(), "config", cfg.JobType, "reasons", reasons)
	}
	p.log.Info("source run complete",
		"source", src.Name(), "config", cfg.JobType,
		"raw", len(posts), "new", created, "dupes", dupes, "rejected", rejected)
}

type outcome int

const (
	outcomeRejected outcome = iota
	outcomeCreated
	outcomeDuplicate
)

// processPost applies the full filter → classify → validate → dedupe → save
// chain to a single post. It returns the outcome and, for rejections, a
// machine-readable reason used for observability logs.
func (p *Pipeline) processPost(ctx context.Context, cfg *model.SearchConfig, post model.RawPost, cutoff time.Time) (outcome, string) {
	// 1. Keyword pre-filter.
	if !matchesKeywords(post, cfg.Keywords) {
		return outcomeRejected, "no_keyword_match"
	}
	if matchesAny(post, cfg.ExcludeKeywords) {
		return outcomeRejected, "excluded_keyword"
	}

	// 2. Timestamp validation — must be known and inside the lookback window.
	if post.PostedAt == nil {
		return outcomeRejected, "no_timestamp"
	}
	if post.PostedAt.Before(cutoff) {
		return outcomeRejected, "timestamp_too_old"
	}
	if post.PostedAt.After(time.Now().Add(2 * time.Hour)) {
		return outcomeRejected, "timestamp_in_future"
	}

	// 3. Field validation — title + source URL required.
	post.JobTitle = strings.TrimSpace(post.JobTitle)
	post.SourceURL = strings.TrimSpace(post.SourceURL)
	if post.JobTitle == "" || post.SourceURL == "" {
		return outcomeRejected, "missing_title_or_url"
	}

	// 4. Structured filters (skip fields the post does not declare).
	if !matchesChoice(cfg.EmploymentType, post.EmploymentType) {
		return outcomeRejected, "employment_type_mismatch"
	}
	if !matchesChoice(cfg.WorkMode, post.WorkMode) {
		return outcomeRejected, "work_mode_mismatch"
	}
	if !matchesLocation(cfg.Locations, post.Location) {
		return outcomeRejected, "location_mismatch"
	}

	// 5. Optional AI classification for ambiguous posts.
	method := "rules"
	var confidence *float64
	if p.aiClassify && p.ai.Enabled() && needsClassification(post) {
		cl, err := p.ai.Classify(ctx, post)
		if err != nil {
			p.log.Warn("ai classification failed, falling back to rules", "url", post.SourceURL, "err", err)
		} else if cl != nil {
			method = "ai"
			c := cl.Confidence
			confidence = &c
			if !cl.IsJob || cl.Confidence < ai.MinConfidence() {
				return outcomeRejected, "ai_rejected"
			}
			post = mergeClassification(post, cl)
		}
	}

	// 6. Dedupe by content hash and persist.
	job := &model.Job{
		SearchConfigID:       cfg.ID,
		Source:               post.Source,
		SourcePostID:         post.SourcePostID,
		SourceURL:            post.SourceURL,
		JobTitle:             post.JobTitle,
		CompanyName:          post.CompanyName,
		Location:             post.Location,
		EmploymentType:       normalizeEmployment(post.EmploymentType),
		WorkMode:             normalizeWorkMode(post.WorkMode),
		Description:          post.Description,
		ApplicationURL:       post.ApplicationURL,
		PostedAt:             post.PostedAt,
		ContentHash:          contentHash(post),
		ClassificationMethod: method,
		AIConfidence:         confidence,
	}

	created, err := p.store.InsertJob(ctx, job)
	if err != nil {
		p.log.Error("insert job failed", "url", post.SourceURL, "err", err)
		return outcomeRejected, "insert_error"
	}
	if created {
		return outcomeCreated, "created"
	}
	return outcomeDuplicate, "duplicate"
}

// needsClassification flags posts with weak signals for AI review.
func needsClassification(post model.RawPost) bool {
	return post.CompanyName == "" ||
		post.EmploymentType == "" ||
		post.WorkMode == "" ||
		len(post.Description) < 40
}

func mergeClassification(post model.RawPost, cl *model.Classification) model.RawPost {
	if post.CompanyName == "" {
		post.CompanyName = strings.TrimSpace(cl.CompanyName)
	}
	if post.Location == "" {
		post.Location = strings.TrimSpace(cl.Location)
	}
	if post.EmploymentType == "" {
		post.EmploymentType = cl.EmploymentType
	}
	if post.WorkMode == "" {
		post.WorkMode = cl.WorkMode
	}
	if post.Description == "" || len(cl.Description) > len(post.Description) {
		post.Description = strings.TrimSpace(cl.Description)
	}
	if post.ApplicationURL == "" {
		post.ApplicationURL = strings.TrimSpace(cl.ApplicationURL)
	}
	if post.JobTitle == "" {
		post.JobTitle = strings.TrimSpace(cl.JobTitle)
	}
	return post
}

// matchesKeywords requires at least one keyword in the post text (or no
// keywords configured). Matching is punctuation-insensitive, so "nodejs
// developer" matches a "Node.js Developer" title.
func matchesKeywords(post model.RawPost, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	text := postText(post)
	normText := normalizeForMatch(text)
	for _, k := range keywords {
		k := strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if strings.Contains(text, k) || strings.Contains(normText, normalizeForMatch(k)) {
			return true
		}
	}
	return false
}

// normalizeForMatch makes matching tolerant of punctuation/space variants:
// "Node.js" == "nodejs", "full-stack" == "full stack", "C++" == "c".
func normalizeForMatch(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == '.' || r == '+' || r == '#' || r == '_':
			// join: "Node.js" → "nodejs"
		default:
			// '-', '/', '(', ')', other punctuation and spaces → separator
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func matchesAny(post model.RawPost, words []string) bool {
	text := postText(post)
	for _, k := range words {
		if k := strings.ToLower(strings.TrimSpace(k)); k != "" && strings.Contains(text, k) {
			return true
		}
	}
	return false
}

func postText(post model.RawPost) string {
	return strings.ToLower(strings.Join([]string{
		post.JobTitle, post.CompanyName, post.Location, post.Description,
	}, " "))
}

// matchesChoice: empty/"any" config or empty post value means accept.
func matchesChoice(configured, actual string) bool {
	configured = strings.ToLower(strings.TrimSpace(configured))
	if configured == "" || configured == "any" {
		return true
	}
	actual = normalizeChoice(actual)
	if actual == "" {
		return true // post does not declare it — don't reject
	}
	return actual == configured
}

func matchesLocation(configured []string, actual string) bool {
	if len(configured) == 0 {
		return true
	}
	actual = strings.ToLower(strings.TrimSpace(actual))
	if actual == "" {
		return true
	}
	for _, l := range configured {
		l = strings.ToLower(strings.TrimSpace(l))
		if l != "" && strings.Contains(actual, l) {
			return true
		}
	}
	return false
}

func normalizeChoice(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	switch s {
	case "full_time", "fulltime", "ft":
		return "full_time"
	case "part_time", "parttime", "pt":
		return "part_time"
	case "contract", "contractor", "freelance":
		return "contract"
	case "internship", "intern":
		return "internship"
	case "remote", "wfh":
		return "remote"
	case "hybrid":
		return "hybrid"
	case "onsite", "on_site", "in_office":
		return "onsite"
	case "any", "":
		return ""
	}
	return s
}

func normalizeEmployment(s string) string {
	if v := normalizeChoice(s); v != "" {
		return v
	}
	return "any"
}

func normalizeWorkMode(s string) string {
	if v := normalizeChoice(s); v != "" {
		return v
	}
	return "any"
}

// contentHash is the dedup fingerprint over title+company+location+source URL.
func contentHash(post model.RawPost) string {
	norm := func(s string) string {
		return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
	}
	h := sha256.Sum256([]byte(strings.Join([]string{
		norm(post.JobTitle),
		norm(post.CompanyName),
		norm(post.Location),
		norm(post.SourceURL),
	}, "|")))
	return hex.EncodeToString(h[:])
}

func timePtr(t time.Time) *time.Time { return &t }
