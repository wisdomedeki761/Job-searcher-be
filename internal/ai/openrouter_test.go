package ai

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func testClient() *Client {
	return New("test-key", "test/model", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// completionResponse builds an OpenRouter-shaped response body.
func completionResponse(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"content": content}},
		},
	})
	return string(b)
}

func TestChatJSONRetriesTruncatedOutput(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// Simulate a free-tier model cutting off mid-string.
			_, _ = w.Write([]byte(completionResponse(`{"keywords": ["frontend developer", "web develop`)))
			return
		}
		_, _ = w.Write([]byte(completionResponse(`{"keywords": ["frontend developer", "web developer"]}`)))
	}))
	defer srv.Close()

	old := openRouterURL
	openRouterURL = srv.URL
	defer func() { openRouterURL = old }()

	body, err := testClient().chatJSON(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("chatJSON() error = %v", err)
	}
	if !json.Valid(body) {
		t.Fatalf("chatJSON() returned invalid JSON: %s", body)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("server called %d times, want 2 (one retry)", got)
	}
}

func TestChatJSONDoesNotRetryUnauthorized(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	old := openRouterURL
	openRouterURL = srv.URL
	defer func() { openRouterURL = old }()

	if _, err := testClient().chatJSON(context.Background(), "sys", "user"); err == nil {
		t.Fatal("chatJSON() error = nil, want error for 401")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("server called %d times, want 1 (no retry on 401)", got)
	}
}

func TestChatJSONRetriesRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		_, _ = w.Write([]byte(completionResponse(`{"keywords": ["golang developer"]}`)))
	}))
	defer srv.Close()

	old := openRouterURL
	openRouterURL = srv.URL
	defer func() { openRouterURL = old }()

	body, err := testClient().chatJSON(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("chatJSON() error = %v", err)
	}
	if !json.Valid(body) {
		t.Fatalf("chatJSON() returned invalid JSON: %s", body)
	}
}

func TestEnrichJobTypeSanitizesOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(completionResponse(
			`{"keywords":["Golang Developer","golang developer"," Go "],"exclude_keywords":[],"locations":[],
			  "employment_type":"full_time","work_mode":"remote","sources":[],"lookback_hours":999,
			  "suggested_cron":"bogus"}`)))
	}))
	defer srv.Close()

	old := openRouterURL
	openRouterURL = srv.URL
	defer func() { openRouterURL = old }()

	p, err := testClient().EnrichJobType(context.Background(), "Golang Developer", []string{"mock"})
	if err != nil {
		t.Fatalf("EnrichJobType() error = %v", err)
	}
	// Dedupe/trim collapse ["Golang Developer","golang developer"," Go "] to
	// 2 unique entries, then variant expansion adds bare "golang".
	if len(p.Keywords) != 3 {
		t.Errorf("keywords not deduped/trimmed/expanded: %v", p.Keywords)
	}
	if p.Keywords[0] != "golang developer" || p.Keywords[1] != "go" {
		t.Errorf("keywords order/dedupe wrong: %v", p.Keywords)
	}
	if p.Keywords[2] != "golang" {
		t.Errorf("bare tech variant missing: %v", p.Keywords)
	}
	if p.LookbackHours != 24 {
		t.Errorf("lookback_hours = %d, want 24 (out-of-range value reset)", p.LookbackHours)
	}
	if p.SuggestedCron == "bogus" {
		t.Error("bogus cron should have been replaced with the default")
	}
	if len(p.Sources) == 0 || p.Sources[0] != "mock" {
		t.Errorf("sources = %v, want fallback to available sources", p.Sources)
	}
}

// TestExpandKeywordVariants: literal keyword filtering never cross-matches
// spellings ("frontend developer" is absent from a "Front-End Developer"
// title), so each keyword must drag its variants along.
func TestExpandKeywordVariants(t *testing.T) {
	got := expandKeywordVariants([]string{
		"frontend developer", "fullstack developer", "golang developer", "nodejs developer",
	})
	have := map[string]bool{}
	for _, k := range got {
		have[k] = true
	}
	want := []string{
		"frontend developer", "front end developer",
		"fullstack developer", "full stack developer",
		"golang developer", "golang", // bare tech word
		"nodejs developer", "node js developer",
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("missing variant %q in %v", w, got)
		}
	}
	if len(have) != len(got) {
		t.Errorf("duplicates in output: %v", got)
	}
	// Idempotent: expanding twice must not grow the list.
	again := expandKeywordVariants(got)
	if len(again) != len(got) {
		t.Errorf("not idempotent: %d -> %d", len(got), len(again))
	}
}

// TestExpandKeywordVariantsBareTechOnly: only unambiguous tech words are
// added bare — "frontend developer" must NOT grow a useless "frontend"
// entry, while ordinary role phrases pass through untouched.
func TestExpandKeywordVariantsBareTechOnly(t *testing.T) {
	got := expandKeywordVariants([]string{"python developer", "product designer", "ux designer"})
	have := map[string]bool{}
	for _, k := range got {
		have[k] = true
	}
	if len(got) != 3 {
		t.Errorf("got %v, want the 3 input keywords untouched", got)
	}

	// frontend developer gets its spacing variant but never a bare "frontend".
	got = expandKeywordVariants([]string{"frontend developer"})
	for _, k := range got {
		if k == "frontend" {
			t.Errorf("bare %q added but it is not an unambiguous tech word: %v", k, got)
		}
	}
	if len(got) != 2 || !have2(got, "front end developer") {
		t.Errorf("got %v, want [frontend developer front end developer]", got)
	}
}

func have2(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestDropExcludedConflicts: an exclude word inside a keyword would veto
// every job the keyword finds (exclude beats keyword in the pipeline).
func TestDropExcludedConflicts(t *testing.T) {
	keywords := []string{"smart contract", "blockchain developer", "frontend developer"}
	got := dropExcludedConflicts(keywords, []string{"contract", "senior", "spam", "lead"})

	have := map[string]bool{}
	for _, k := range got {
		have[k] = true
	}
	if have["contract"] {
		t.Errorf("'contract' conflicts with 'smart contract' keyword and must be dropped: %v", got)
	}
	for _, keep := range []string{"senior", "spam", "lead"} {
		if !have[keep] {
			t.Errorf("non-conflicting exclude %q was dropped: %v", keep, got)
		}
	}
}

// TestEnrichJobTypeKeywordPipeline: end-to-end — model returns a naive list
// plus a self-defeating exclude; the profile must come back with variants
// expanded, capped at 20, and the conflicting exclude removed.
func TestEnrichJobTypeKeywordPipeline(t *testing.T) {
	naive := `{"keywords":["frontend developer","full stack developer","golang developer",
		"python developer","nodejs developer","solidity developer","blockchain developer",
		"smart contract","ui ux designer","product designer","ux designer","figma",
		"react developer","javascript developer","web3 developer"],
		"exclude_keywords":["contract","senior","lead","manager","internship","spam"],
		"locations":[],"employment_type":"any","work_mode":"any",
		"sources":[],"lookback_hours":48,"suggested_cron":"0 */6 * * *"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(completionResponse(naive)))
	}))
	defer srv.Close()

	old := openRouterURL
	openRouterURL = srv.URL
	defer func() { openRouterURL = old }()

	p, err := testClient().EnrichJobType(context.Background(), "multi-role", []string{"mock"})
	if err != nil {
		t.Fatalf("EnrichJobType() error = %v", err)
	}
	if len(p.Keywords) > 20 {
		t.Errorf("keywords not capped at 20: %d", len(p.Keywords))
	}
	have := map[string]bool{}
	for _, k := range p.Keywords {
		have[k] = true
	}
	for _, want := range []string{"front end developer", "full stack developer", "golang"} {
		if !have[want] {
			t.Errorf("missing expanded keyword %q in %v", want, p.Keywords)
		}
	}
	for _, e := range p.ExcludeKeywords {
		if e == "contract" {
			t.Errorf("exclude 'contract' survived despite 'smart contract' keyword: %v", p.ExcludeKeywords)
		}
	}
}
