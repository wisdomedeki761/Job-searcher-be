package collector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qx-scraper/internal/config"
)

func TestBuildRegistryIncludesPublicAndConfiguredATSources(t *testing.T) {
	registry := BuildRegistry(&config.Config{
		GreenhouseBoards: []config.NameValue{{Name: "acme", Value: "acme-board"}},
		LeverSites:       []config.NameValue{{Name: "example", Value: "example-site"}},
	})
	for _, name := range []string{
		"arbeitnow", "arbeitnow-uk", "jobicy", "himalayas", "remoteok",
		"weworkremotely", "remotive", "greenhouse-acme", "lever-example",
	} {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("registry missing source %q", name)
		}
	}
}

func TestSearchPublicSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/arbeitnow"):
			_, _ = fmt.Fprint(w, `{"data":[{"slug":"dev-1","company_name":"Acme","title":"Developer","description":"Role","remote":true,"url":"https://arbeitnow.test/jobs/dev-1","created_at":"2026-11-06T06:00:00Z","location":"Remote","job_types":["Full-time"]}]}`)
		case r.URL.Path == "/jobicy":
			_, _ = fmt.Fprint(w, `{"nextCursor":null,"jobs":[{"id":123,"url":"https://jobicy.test/jobs/123","jobTitle":"Designer","companyName":"Acme","jobGeo":"Worldwide","jobType":["Full Time"],"jobDescription":"Role","pubDate":"2026-11-06T06:00:00Z"}]}`)
		case r.URL.Path == "/himalayas/search":
			_, _ = fmt.Fprint(w, `{"jobs":[{"guid":"g-1","title":"Engineer","companyName":"Acme","locationRestrictions":["United States"],"employmentType":"Full Time","description":"Role","applicationLink":"https://apply.test/1","pubDate":1793944800,"slug":"engineer","companySlug":"acme"}]}`)
		case r.URL.Path == "/remoteok":
			_, _ = fmt.Fprint(w, `[{"legal":"terms"},{"id":"r-1","position":"Engineer","company":"Acme","location":"Remote","url":"https://remoteok.test/jobs/r-1","date":"2026-11-06T06:00:00Z","description":"Role"}]`)
		case r.URL.Path == "/wework.rss":
			_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel><item><title>Acme: Writer</title><link>https://wwr.test/1</link><guid>wwr-1</guid><pubDate>Fri, 06 Nov 2026 06:00:00 GMT</pubDate><region>Worldwide</region><type>Full-Time</type><description><![CDATA[Role description]]></description></item></channel></rss>`)
		case r.URL.Path == "/remotive.rss":
			_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel><item><title>Support Agent</title><link>https://remotive.test/1</link><guid>rem-1</guid><jobId>rem-1</jobId><pubDate>Fri, 06 Nov 2026 06:00:00 GMT</pubDate><company>Acme</company><location>Worldwide</location><type>Full-time</type><description><![CDATA[Role description]]></description></item></channel></rss>`)
		case r.URL.Path == "/greenhouse/acme-board/jobs":
			_, _ = fmt.Fprint(w, `{"jobs":[{"id":7,"title":"Analyst","absolute_url":"https://greenhouse.test/7","first_published":"2026-11-06T06:00:00Z","location":{"name":"Remote"},"content":"Role"}]}`)
		case r.URL.Path == "/lever/example-site":
			_, _ = fmt.Fprint(w, `[{"id":"l-1","text":"Engineer","hostedUrl":"https://lever.test/1","applyUrl":"https://apply.test/1","createdAt":1793944800000,"descriptionPlain":"Role","categories":{"location":"Remote","commitment":"Full-time"},"workplaceType":"remote"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	old := []string{
		arbeitnowBaseURL, arbeitnowUKBaseURL, jobicyBaseURL, himalayasBaseURL,
		himalayasSearchURL, remoteOKURL, weWorkRemotelyURL, remotiveRSSURL,
		greenhouseBaseURL, leverBaseURL,
	}
	arbeitnowBaseURL = server.URL + "/arbeitnow"
	arbeitnowUKBaseURL = server.URL + "/arbeitnow-uk"
	jobicyBaseURL = server.URL + "/jobicy"
	himalayasBaseURL = server.URL + "/himalayas"
	himalayasSearchURL = server.URL + "/himalayas/search"
	remoteOKURL = server.URL + "/remoteok"
	weWorkRemotelyURL = server.URL + "/wework.rss"
	remotiveRSSURL = server.URL + "/remotive.rss"
	greenhouseBaseURL = server.URL + "/greenhouse"
	leverBaseURL = server.URL + "/lever"
	t.Cleanup(func() {
		arbeitnowBaseURL, arbeitnowUKBaseURL, jobicyBaseURL, himalayasBaseURL = old[0], old[1], old[2], old[3]
		himalayasSearchURL, remoteOKURL, weWorkRemotelyURL, remotiveRSSURL = old[4], old[5], old[6], old[7]
		greenhouseBaseURL, leverBaseURL = old[8], old[9]
	})

	req := SearchRequest{Keywords: []string{"engineer"}, Lookback: 24 * time.Hour}
	tests := []struct {
		name string
		run  func() (int, error)
	}{
		{"arbeitnow", func() (int, error) {
			p, e := searchArbeitnow(context.Background(), req, arbeitnowBaseURL, "arbeitnow")
			return len(p), e
		}},
		{"jobicy", func() (int, error) { p, e := searchJobicy(context.Background(), req); return len(p), e }},
		{"himalayas", func() (int, error) { p, e := searchHimalayas(context.Background(), req); return len(p), e }},
		{"remoteok", func() (int, error) { p, e := searchRemoteOK(context.Background(), req); return len(p), e }},
		{"weworkremotely", func() (int, error) {
			p, e := searchRSS(context.Background(), weWorkRemotelyURL, "weworkremotely")
			return len(p), e
		}},
		{"remotive", func() (int, error) {
			p, e := searchRSS(context.Background(), remotiveRSSURL, "remotive")
			return len(p), e
		}},
		{"greenhouse", func() (int, error) {
			p, e := searchGreenhouse(context.Background(), "acme-board", "Acme", "greenhouse-acme")
			return len(p), e
		}},
		{"lever", func() (int, error) {
			p, e := searchLever(context.Background(), "example-site", "Example", "lever-example")
			return len(p), e
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, err := tt.run()
			if err != nil {
				t.Fatalf("search failed: %v", err)
			}
			if count != 1 {
				t.Fatalf("got %d posts, want 1", count)
			}
		})
	}
}

func TestParseSourceTime(t *testing.T) {
	for _, value := range []string{
		"1793944800",
		"1793944800000",
		"Fri, 06 Nov 2026 06:00:00 GMT",
		"2026-11-06T06:00:00Z",
	} {
		if got := parseSourceTime(value); got == nil {
			t.Errorf("parseSourceTime(%q) = nil", value)
		}
	}
}
