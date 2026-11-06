package collector

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"qx-scraper/internal/config"
	"qx-scraper/internal/model"
)

type flexibleTimestamp string

func (t *flexibleTimestamp) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*t = ""
		return nil
	}
	var value string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*t = flexibleTimestamp(value)
		return nil
	}
	*t = flexibleTimestamp(string(data))
	return nil
}

var (
	arbeitnowBaseURL   = "https://www.arbeitnow.com/api/job-board-api"
	arbeitnowUKBaseURL = "https://www.arbeitnow.co.uk/api/job-board-api"
	jobicyBaseURL      = "https://jobicy.com/api/jobs"
	himalayasBaseURL   = "https://himalayas.app/jobs/api"
	himalayasSearchURL = "https://himalayas.app/jobs/api/search"
	remoteOKURL        = "https://remoteok.com/api"
	weWorkRemotelyURL  = "https://weworkremotely.com/remote-jobs.rss"
	remotiveRSSURL     = "https://remotive.com/remote-jobs/feed"
	greenhouseBaseURL  = "https://boards-api.greenhouse.io/v1/boards"
	leverBaseURL       = "https://api.lever.co/v0/postings"
)

const publicPageLimit = 5

type publicSource struct {
	name   string
	search func(context.Context, SearchRequest) ([]model.RawPost, error)
}

func (s *publicSource) Name() string { return s.name }

func (s *publicSource) Search(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
	return s.search(ctx, req)
}

func newPublicSource(name string, search func(context.Context, SearchRequest) ([]model.RawPost, error)) Source {
	return &publicSource{
		name:   name,
		search: search,
	}
}

// registerPublicSources adds public no-key feeds and APIs. Greenhouse and
// Lever require employer-specific board IDs, so those are registered only
// when configured.
func registerPublicSources(r *Registry, cfg *config.Config) {
	r.register(newPublicSource("arbeitnow", func(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
		return searchArbeitnow(ctx, req, arbeitnowBaseURL, "arbeitnow")
	}))
	r.register(newPublicSource("arbeitnow-uk", func(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
		return searchArbeitnow(ctx, req, arbeitnowUKBaseURL, "arbeitnow-uk")
	}))
	r.register(newPublicSource("jobicy", searchJobicy))
	r.register(newPublicSource("himalayas", searchHimalayas))
	r.register(newPublicSource("remoteok", searchRemoteOK))
	r.register(newPublicSource("weworkremotely", func(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
		return searchRSS(ctx, weWorkRemotelyURL, "weworkremotely")
	}))
	r.register(newPublicSource("remotive", func(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
		return searchRSS(ctx, remotiveRSSURL, "remotive")
	}))

	for _, board := range cfg.GreenhouseBoards {
		boardName, token := board.Name, board.Value
		r.register(newPublicSource("greenhouse-"+boardName, func(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
			return searchGreenhouse(ctx, token, boardName, "greenhouse-"+boardName)
		}))
	}
	for _, site := range cfg.LeverSites {
		siteName, token := site.Name, site.Value
		r.register(newPublicSource("lever-"+siteName, func(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
			return searchLever(ctx, token, siteName, "lever-"+siteName)
		}))
	}
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "AJ-Scraper/1.0 (public job feed collector)")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("GET %s returned %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 20<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode response from %s: %w", endpoint, err)
	}
	return nil
}

func searchArbeitnow(ctx context.Context, req SearchRequest, baseURL, source string) ([]model.RawPost, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	var posts []model.RawPost
	for page := 1; page <= publicPageLimit; page++ {
		u, err := url.Parse(baseURL)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("page", strconv.Itoa(page))
		u.RawQuery = q.Encode()
		var response struct {
			Data []struct {
				Slug        string            `json:"slug"`
				Company     string            `json:"company_name"`
				Title       string            `json:"title"`
				Description string            `json:"description"`
				Remote      bool              `json:"remote"`
				URL         string            `json:"url"`
				CreatedAt   flexibleTimestamp `json:"created_at"`
				Location    string            `json:"location"`
				JobTypes    []string          `json:"job_types"`
			} `json:"data"`
			Links struct {
				Next string `json:"next"`
			} `json:"links"`
		}
		if err := getJSON(ctx, client, u.String(), &response); err != nil {
			return nil, fmt.Errorf("source %q: %w", source, err)
		}
		for _, job := range response.Data {
			posted := parseSourceTime(string(job.CreatedAt))
			posts = append(posts, model.RawPost{
				Source: source, SourcePostID: job.Slug, SourceURL: job.URL,
				JobTitle: job.Title, CompanyName: job.Company, Location: job.Location,
				EmploymentType: strings.Join(job.JobTypes, ", "),
				WorkMode:       remoteMode(job.Remote, job.Location),
				Description:    job.Description, ApplicationURL: job.URL, PostedAt: posted,
			})
		}
		if len(response.Data) == 0 || response.Links.Next == "" {
			break
		}
	}
	return posts, nil
}

func searchJobicy(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	keywords := req.Keywords
	if len(keywords) == 0 {
		keywords = []string{req.JobType}
	}
	if len(keywords) == 0 || strings.TrimSpace(keywords[0]) == "" {
		keywords = []string{""}
	}
	seen := map[string]bool{}
	var posts []model.RawPost
	for _, keyword := range keywords {
		cursor := ""
		for page := 0; page < publicPageLimit; page++ {
			u, err := url.Parse(jobicyBaseURL)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("count", "100")
			tag := strings.TrimSpace(keyword)
			if len([]rune(tag)) >= 3 && len([]rune(tag)) <= 20 {
				q.Set("tag", tag)
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			u.RawQuery = q.Encode()
			var response struct {
				NextCursor string `json:"nextCursor"`
				Jobs       []struct {
					ID          json.Number `json:"id"`
					URL         string      `json:"url"`
					Title       string      `json:"jobTitle"`
					Company     string      `json:"companyName"`
					Location    string      `json:"jobGeo"`
					JobType     []string    `json:"jobType"`
					Description string      `json:"jobDescription"`
					PostedAt    string      `json:"pubDate"`
				} `json:"jobs"`
			}
			if err := getJSON(ctx, client, u.String(), &response); err != nil {
				return nil, fmt.Errorf("source %q: %w", "jobicy", err)
			}
			for _, job := range response.Jobs {
				id := job.ID.String()
				if id == "" {
					id = job.URL
				}
				if seen[id] {
					continue
				}
				seen[id] = true
				posts = append(posts, model.RawPost{
					Source: "jobicy", SourcePostID: id, SourceURL: job.URL,
					JobTitle: job.Title, CompanyName: job.Company, Location: job.Location,
					EmploymentType: strings.Join(job.JobType, ", "),
					WorkMode:       remoteMode(true, job.Location),
					Description:    job.Description, ApplicationURL: job.URL, PostedAt: parseSourceTime(job.PostedAt),
				})
			}
			if response.NextCursor == "" || len(response.Jobs) == 0 {
				break
			}
			cursor = response.NextCursor
		}
	}
	return posts, nil
}

func searchHimalayas(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	keywords := req.Keywords
	if len(keywords) == 0 && strings.TrimSpace(req.JobType) != "" {
		keywords = []string{req.JobType}
	}
	seen := map[string]bool{}
	var posts []model.RawPost
	pagesPerKeyword := publicPageLimit
	if len(keywords) == 0 {
		pagesPerKeyword = publicPageLimit
		keywords = []string{""}
	}
	for _, keyword := range keywords {
		cursor := ""
		for page := 1; page <= pagesPerKeyword; page++ {
			base := himalayasBaseURL
			if strings.TrimSpace(keyword) != "" {
				base = himalayasSearchURL
			}
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("limit", "20")
			if strings.TrimSpace(keyword) != "" {
				q.Set("q", keyword)
				q.Set("page", strconv.Itoa(page))
			} else if cursor != "" {
				q.Set("cursor", cursor)
			}
			u.RawQuery = q.Encode()
			var response struct {
				NextCursor string `json:"nextCursor"`
				Jobs       []struct {
					GUID        string      `json:"guid"`
					Title       string      `json:"title"`
					Company     string      `json:"companyName"`
					Location    []string    `json:"locationRestrictions"`
					Employment  string      `json:"employmentType"`
					Description string      `json:"description"`
					Link        string      `json:"applicationLink"`
					PostedAt    json.Number `json:"pubDate"`
					Slug        string      `json:"slug"`
					CompanySlug string      `json:"companySlug"`
				} `json:"jobs"`
			}
			if err := getJSON(ctx, client, u.String(), &response); err != nil {
				return nil, fmt.Errorf("source %q: %w", "himalayas", err)
			}
			for _, job := range response.Jobs {
				id := job.GUID
				if id == "" {
					id = job.CompanySlug + "/" + job.Slug
				}
				if id == "" {
					id = job.Link
				}
				if seen[id] {
					continue
				}
				seen[id] = true
				posted := parseSourceTime(job.PostedAt.String())
				jobURL := ""
				if job.CompanySlug != "" && job.Slug != "" {
					jobURL = "https://himalayas.app/companies/" + url.PathEscape(job.CompanySlug) + "/jobs/" + url.PathEscape(job.Slug)
				}
				if jobURL == "" {
					jobURL = job.Link
				}
				posts = append(posts, model.RawPost{
					Source: "himalayas", SourcePostID: id, SourceURL: jobURL,
					JobTitle: job.Title, CompanyName: job.Company, Location: strings.Join(job.Location, ", "),
					EmploymentType: normalizeEmployment(job.Employment),
					WorkMode:       remoteMode(true, strings.Join(job.Location, ", ")),
					Description:    job.Description, ApplicationURL: job.Link, PostedAt: posted,
				})
			}
			if len(response.Jobs) == 0 {
				break
			}
			if strings.TrimSpace(keyword) == "" {
				if response.NextCursor == "" {
					break
				}
				cursor = response.NextCursor
			}
		}
	}
	return posts, nil
}

func searchRemoteOK(ctx context.Context, req SearchRequest) ([]model.RawPost, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	var jobs []map[string]any
	if err := getJSON(ctx, client, remoteOKURL, &jobs); err != nil {
		return nil, fmt.Errorf("source %q: %w", "remoteok", err)
	}
	posts := make([]model.RawPost, 0, len(jobs))
	for _, job := range jobs {
		if _, isNotice := job["legal"]; isNotice {
			continue
		}
		post, ok := normalizeApifyItem("remoteok", job)
		if !ok {
			continue
		}
		if post.SourcePostID == "" {
			post.SourcePostID = post.SourceURL
		}
		if post.WorkMode == "" {
			post.WorkMode = "remote"
		}
		if post.PostedAt == nil {
			if t := parseSourceTime(fmt.Sprint(job["epoch"])); t != nil {
				post.PostedAt = t
			}
		}
		posts = append(posts, post)
	}
	return posts, nil
}

type rssFeed struct {
	Items []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
	Company     string `xml:"company"`
	Location    string `xml:"location"`
	Region      string `xml:"region"`
	Category    string `xml:"category"`
	JobType     string `xml:"type"`
	JobID       string `xml:"jobId"`
}

func searchRSS(ctx context.Context, feedURL, source string) ([]model.RawPost, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	req.Header.Set("User-Agent", "AJ-Scraper/1.0 (public job feed collector)")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("source %q: RSS returned %d", source, resp.StatusCode)
	}
	var feed rssFeed
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 20<<20)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("source %q: parse RSS: %w", source, err)
	}
	posts := make([]model.RawPost, 0, len(feed.Items))
	for _, item := range feed.Items {
		link := strings.TrimSpace(item.Link)
		if link == "" && strings.HasPrefix(item.GUID, "http") {
			link = item.GUID
		}
		id := strings.TrimSpace(item.JobID)
		if id == "" {
			id = strings.TrimSpace(item.GUID)
		}
		if id == "" {
			id = link
		}
		location := strings.TrimSpace(item.Location)
		if location == "" {
			location = strings.TrimSpace(item.Region)
		}
		company := strings.TrimSpace(item.Company)
		title := strings.TrimSpace(item.Title)
		if source == "weworkremotely" && company == "" {
			company, title = splitCompanyTitle(title)
		}
		if link == "" || title == "" {
			continue
		}
		posts = append(posts, model.RawPost{
			Source: source, SourcePostID: id, SourceURL: link, JobTitle: title,
			CompanyName: company, Location: location, EmploymentType: normalizeEmployment(item.JobType),
			WorkMode: "remote", Description: item.Description, ApplicationURL: link,
			PostedAt: parseSourceTime(item.PubDate),
		})
	}
	return posts, nil
}

func splitCompanyTitle(title string) (string, string) {
	parts := strings.SplitN(title, ":", 2)
	if len(parts) != 2 {
		return "", strings.TrimSpace(title)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func searchGreenhouse(ctx context.Context, board, company, source string) ([]model.RawPost, error) {
	endpoint := strings.TrimRight(greenhouseBaseURL, "/") + "/" + url.PathEscape(board) + "/jobs?content=true"
	var response struct {
		Jobs []struct {
			ID             int64  `json:"id"`
			Title          string `json:"title"`
			Absolute       string `json:"absolute_url"`
			FirstPublished string `json:"first_published"`
			UpdatedAt      string `json:"updated_at"`
			Location       struct {
				Name string `json:"name"`
			} `json:"location"`
			Content string `json:"content"`
			Offices []struct {
				Name string `json:"name"`
			} `json:"offices"`
		} `json:"jobs"`
	}
	if err := getJSON(ctx, &http.Client{Timeout: 20 * time.Second}, endpoint, &response); err != nil {
		return nil, fmt.Errorf("source %q: %w", source, err)
	}
	posts := make([]model.RawPost, 0, len(response.Jobs))
	for _, job := range response.Jobs {
		location := job.Location.Name
		if location == "" && len(job.Offices) > 0 {
			location = job.Offices[0].Name
		}
		postedAt := job.FirstPublished
		if postedAt == "" {
			postedAt = job.UpdatedAt
		}
		posts = append(posts, model.RawPost{
			Source: source, SourcePostID: strconv.FormatInt(job.ID, 10), SourceURL: job.Absolute,
			JobTitle: job.Title, CompanyName: company, Location: location, Description: job.Content,
			ApplicationURL: job.Absolute, PostedAt: parseSourceTime(postedAt),
		})
	}
	return posts, nil
}

func searchLever(ctx context.Context, site, company, source string) ([]model.RawPost, error) {
	endpoint := strings.TrimRight(leverBaseURL, "/") + "/" + url.PathEscape(site) + "?mode=json"
	var jobs []struct {
		ID          string `json:"id"`
		Text        string `json:"text"`
		HostedURL   string `json:"hostedUrl"`
		ApplyURL    string `json:"applyUrl"`
		CreatedAt   int64  `json:"createdAt"`
		Description string `json:"descriptionPlain"`
		Categories  struct {
			Location    string   `json:"location"`
			Team        string   `json:"team"`
			Commitment  string   `json:"commitment"`
			Departments []string `json:"department"`
		} `json:"categories"`
		WorkplaceType string `json:"workplaceType"`
	}
	if err := getJSON(ctx, &http.Client{Timeout: 20 * time.Second}, endpoint, &jobs); err != nil {
		return nil, fmt.Errorf("source %q: %w", source, err)
	}
	posts := make([]model.RawPost, 0, len(jobs))
	for _, job := range jobs {
		posted := parseSourceTime(strconv.FormatInt(job.CreatedAt, 10))
		posts = append(posts, model.RawPost{
			Source: source, SourcePostID: job.ID, SourceURL: job.HostedURL,
			JobTitle: job.Text, CompanyName: company, Location: job.Categories.Location,
			EmploymentType: normalizeEmployment(job.Categories.Commitment),
			WorkMode:       normalizeWorkMode(job.WorkplaceType, job.Categories.Location),
			Description:    job.Description, ApplicationURL: job.ApplyURL, PostedAt: posted,
		})
	}
	return posts, nil
}

func parseSourceTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" || value == "<nil>" {
		return nil
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		if n > 1e12 {
			n /= 1000
		}
		t := time.Unix(n, 0).UTC()
		return &t
	}
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123,
		time.RFC822Z, time.RFC822, time.RFC850, time.ANSIC,
		"2006-01-02 15:04:05", "2006-01-02",
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return &t
		}
	}
	if t, err := parseTime(value); err == nil {
		return &t
	}
	return nil
}

func normalizeEmployment(value string) string {
	s := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(s, "full"):
		return "full_time"
	case strings.Contains(s, "part"):
		return "part_time"
	case strings.Contains(s, "intern"):
		return "internship"
	case strings.Contains(s, "contract"), strings.Contains(s, "freelance"):
		return "contract"
	default:
		return value
	}
}

func normalizeWorkMode(value, location string) string {
	s := strings.ToLower(value + " " + location)
	switch {
	case strings.Contains(s, "hybrid"):
		return "hybrid"
	case strings.Contains(s, "remote"), strings.Contains(s, "worldwide"):
		return "remote"
	case strings.Contains(s, "onsite"), strings.Contains(s, "on-site"), strings.Contains(s, "on site"):
		return "onsite"
	default:
		return ""
	}
}

func remoteMode(remote bool, location string) string {
	if remote {
		return "remote"
	}
	return normalizeWorkMode("", location)
}
