package pipeline

import (
	"testing"

	"aj-scraper/internal/model"
)

func TestMatchesKeywords(t *testing.T) {
	cases := []struct {
		name     string
		post     model.RawPost
		keywords []string
		want     bool
	}{
		{
			name:     "no keywords configured accepts everything",
			post:     model.RawPost{JobTitle: "Anything"},
			keywords: nil,
			want:     true,
		},
		{
			name:     "plain title match",
			post:     model.RawPost{JobTitle: "Senior Frontend Developer"},
			keywords: []string{"frontend developer"},
			want:     true,
		},
		{
			// Real failure from the log: keywords "nodejs developer" vs a
			// LinkedIn title spelled "Node.js Developer".
			name:     "punctuation-insensitive match",
			post:     model.RawPost{JobTitle: "Node.js Developer"},
			keywords: []string{"nodejs developer"},
			want:     true,
		},
		{
			name:     "hyphenated keyword matches spaced title",
			post:     model.RawPost{JobTitle: "Full Stack Engineer"},
			keywords: []string{"full-stack developer"},
			want:     false, // "developer" is not in the title
		},
		{
			name:     "match against description when title is generic",
			post:     model.RawPost{JobTitle: "Software Engineer", Description: "Building Figma design tools."},
			keywords: []string{"figma designer"},
			want:     false, // "figma" and "designer" must both not be required — single keyword phrase must appear
		},
		{
			name:     "description contains exact keyword",
			post:     model.RawPost{JobTitle: "Software Engineer", Description: "Our figma designer tooling team."},
			keywords: []string{"figma designer"},
			want:     true,
		},
		{
			name:     "keyword appearing in company or location counts",
			post:     model.RawPost{JobTitle: "Engineer", CompanyName: "Golang Labs"},
			keywords: []string{"golang"},
			want:     true,
		},
		{
			name:     "no match rejects",
			post:     model.RawPost{JobTitle: "Customer Assistant"},
			keywords: []string{"frontend developer", "golang developer"},
			want:     false,
		},
		{
			name:     "empty keyword strings are skipped",
			post:     model.RawPost{JobTitle: "Anything"},
			keywords: []string{"", "  "},
			want:     false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchesKeywords(c.post, c.keywords); got != c.want {
				t.Errorf("matchesKeywords() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNormalizeForMatch(t *testing.T) {
	cases := map[string]string{
		"Node.js Developer": "nodejs developer",
		"Full-Stack Dev":    "full stack dev",
		"C++ Engineer":      "c engineer",
		"Figma Designer":    "figma designer",
		"MLOps / SRE":       "mlops sre",
	}
	for in, want := range cases {
		if got := normalizeForMatch(in); got != want {
			t.Errorf("normalizeForMatch(%q) = %q, want %q", in, got, want)
		}
	}
}
