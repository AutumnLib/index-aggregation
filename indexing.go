package indexaggregation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
)

const (
	searchPageSize    = 100
	maxSearchPages    = 10
	maxReadmeExcerpt  = 1200
	maxRetryAttempts  = 3
	initialRetryDelay = time.Second
)

// SearchReport describes how completely one GitHub repository search ran.
type SearchReport struct {
	Query              string `json:"query"`
	ResultsFound       int    `json:"results_found"`
	ResultsRetrieved   int    `json:"results_retrieved"`
	IncompleteResults  bool   `json:"incomplete_results"`
	ReachedSearchLimit bool   `json:"reached_search_limit"`
}

// ReadmeEvidence contains a short excerpt for human review.
type ReadmeEvidence struct {
	Available bool   `json:"available"`
	Excerpt   string `json:"excerpt,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// RepositoryCandidate is a discovered repository that has not been curated.
type RepositoryCandidate struct {
	FullName         string         `json:"full_name"`
	URL              string         `json:"url"`
	Description      string         `json:"description,omitempty"`
	Stars            int            `json:"stars"`
	Language         string         `json:"language,omitempty"`
	Topics           []string       `json:"topics,omitempty"`
	DiscoveryQueries []string       `json:"discovery_queries"`
	ReadmeEvidence   ReadmeEvidence `json:"readme_evidence"`
	ReviewStatus     string         `json:"review_status"`
	ReviewNote       string         `json:"review_note"`
}

// Catalog is a deterministic snapshot of discovered repositories and coverage.
type Catalog struct {
	Searches     []SearchReport        `json:"searches"`
	Repositories []RepositoryCandidate `json:"repositories"`
}

// DefaultSearchQueries returns the focused searches used by the weekly scan.
func DefaultSearchQueries() []string {
	return []string{
		"vex in:name,description,readme",
		"VEX robotics in:description,readme",
		"VEX V5 in:description,readme",
		"PROS VEX in:description,readme",
		"odometry VEX in:description,readme",
		"motion profiling VEX in:description,readme",
		"topic:vex",
		"2654E Echo in:name,description,readme",
		"PurdueSigBots in:name,description,readme",
		"HoloLib in:name,description,readme",
		"RWTemplate in:name,description,readme",
		"LemLib in:name,description,readme",
	}
}

// DiscoverRepositories searches GitHub and gathers evidence for each unique result.
func DiscoverRepositories(ctx context.Context, client *github.Client, queries []string) (*Catalog, error) {
	if client == nil {
		return nil, errors.New("github client is nil")
	}
	if len(queries) == 0 {
		return nil, errors.New("at least one search query is required")
	}

	byName := make(map[string]*RepositoryCandidate)
	reports := make([]SearchReport, 0, len(queries))
	for _, query := range queries {
		if strings.TrimSpace(query) == "" {
			return nil, errors.New("search query must not be empty")
		}

		report := SearchReport{Query: query}
		for page := 1; page <= maxSearchPages; page++ {
			opts := &github.SearchOptions{
				ListOptions: github.ListOptions{
					Page:    page,
					PerPage: searchPageSize,
				},
			}
			result, response, err := retryGitHubCall(ctx, func() (*github.RepositoriesSearchResult, *github.Response, error) {
				return SearchRepositories(ctx, client, query, opts)
			})
			if err != nil {
				return nil, fmt.Errorf("search GitHub repositories for %q (page %d): %w", query, page, err)
			}
			if result == nil || response == nil {
				return nil, fmt.Errorf("search GitHub repositories for %q (page %d): empty response", query, page)
			}
			report.ResultsFound = result.GetTotal()
			report.IncompleteResults = report.IncompleteResults || result.GetIncompleteResults()

			for _, repo := range result.Repositories {
				fullName := repo.GetFullName()
				if fullName == "" {
					return nil, fmt.Errorf("search GitHub repositories for %q: result has no full name", query)
				}
				candidate, exists := byName[fullName]
				if !exists {
					candidate = &RepositoryCandidate{
						FullName:     fullName,
						URL:          repo.GetHTMLURL(),
						Description:  repo.GetDescription(),
						Stars:        repo.GetStargazersCount(),
						Language:     repo.GetLanguage(),
						Topics:       append([]string(nil), repo.Topics...),
						ReviewStatus: "candidate",
						ReviewNote:   "Verify reusable or original logic before inclusion.",
					}
					sort.Strings(candidate.Topics)
					byName[fullName] = candidate
				}
				if len(candidate.DiscoveryQueries) == 0 || candidate.DiscoveryQueries[len(candidate.DiscoveryQueries)-1] != query {
					candidate.DiscoveryQueries = append(candidate.DiscoveryQueries, query)
				}
			}
			report.ResultsRetrieved += len(result.Repositories)

			if response.NextPage == 0 {
				break
			}
			if page == maxSearchPages {
				report.ReachedSearchLimit = true
			}
		}
		if report.ResultsFound >= searchPageSize*maxSearchPages ||
			report.ResultsRetrieved >= searchPageSize*maxSearchPages {
			report.ReachedSearchLimit = true
		}
		reports = append(reports, report)
	}

	repositories := make([]RepositoryCandidate, 0, len(byName))
	for _, candidate := range byName {
		owner, name, ok := strings.Cut(candidate.FullName, "/")
		if !ok || owner == "" || name == "" {
			return nil, fmt.Errorf("invalid repository full name %q", candidate.FullName)
		}
		readme, _, err := retryGitHubCall(ctx, func() (*github.RepositoryContent, *github.Response, error) {
			return client.Repositories.GetReadme(ctx, owner, name, nil)
		})
		if err != nil {
			if isGitHubNotFound(err) {
				candidate.ReadmeEvidence = ReadmeEvidence{Reason: "README not found"}
			} else {
				return nil, fmt.Errorf("fetch README for %s: %w", candidate.FullName, err)
			}
		} else {
			content, err := readme.GetContent()
			if err != nil {
				return nil, fmt.Errorf("decode README for %s: %w", candidate.FullName, err)
			}
			candidate.ReadmeEvidence = ReadmeEvidence{
				Available: true,
				Excerpt:   truncateRunes(strings.TrimSpace(content), maxReadmeExcerpt),
			}
		}
		sort.Strings(candidate.DiscoveryQueries)
		repositories = append(repositories, *candidate)
	}
	sort.Slice(repositories, func(i, j int) bool {
		if repositories[i].Stars != repositories[j].Stars {
			return repositories[i].Stars > repositories[j].Stars
		}
		return repositories[i].FullName < repositories[j].FullName
	})

	return &Catalog{Searches: reports, Repositories: repositories}, nil
}

// PreserveReviewDecisions carries reviewed statuses forward for rediscovered repositories.
func PreserveReviewDecisions(catalog, previous *Catalog) error {
	if catalog == nil {
		return errors.New("catalog is nil")
	}
	if previous == nil {
		return nil
	}

	decisions := make(map[string]RepositoryCandidate, len(previous.Repositories))
	for _, candidate := range previous.Repositories {
		switch candidate.ReviewStatus {
		case "candidate":
			continue
		case "included", "excluded":
			if strings.TrimSpace(candidate.ReviewNote) == "" {
				return fmt.Errorf("review note is required for %s repository %s", candidate.ReviewStatus, candidate.FullName)
			}
			decisions[candidate.FullName] = candidate
		default:
			return fmt.Errorf("unknown review status %q for %s", candidate.ReviewStatus, candidate.FullName)
		}
	}
	for i := range catalog.Repositories {
		if decision, ok := decisions[catalog.Repositories[i].FullName]; ok {
			catalog.Repositories[i].ReviewStatus = decision.ReviewStatus
			catalog.Repositories[i].ReviewNote = decision.ReviewNote
		}
	}
	return nil
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func isGitHubNotFound(err error) bool {
	var responseErr *github.ErrorResponse
	return errors.As(err, &responseErr) &&
		responseErr.Response != nil &&
		responseErr.Response.StatusCode == http.StatusNotFound
}

func retryGitHubCall[T any](ctx context.Context, call func() (*T, *github.Response, error)) (*T, *github.Response, error) {
	for attempt := 0; ; attempt++ {
		value, response, err := call()
		if err == nil || !retryableGitHubError(ctx, err, response) || attempt+1 >= maxRetryAttempts {
			return value, response, err
		}

		delay := githubRetryDelay(err, response, attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, response, ctx.Err()
		case <-timer.C:
		}
	}
}

func retryableGitHubError(ctx context.Context, err error, response *github.Response) bool {
	if ctx.Err() != nil {
		return false
	}
	var rateLimitErr *github.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return true
	}
	var abuseRateLimitErr *github.AbuseRateLimitError
	if errors.As(err, &abuseRateLimitErr) {
		return true
	}
	if response != nil && response.StatusCode >= 500 {
		return true
	}
	var networkErr net.Error
	return response == nil && errors.As(err, &networkErr)
}

func githubRetryDelay(err error, response *github.Response, attempt int) time.Duration {
	var rateLimitErr *github.RateLimitError
	if errors.As(err, &rateLimitErr) {
		if delay := time.Until(rateLimitErr.Rate.Reset.Time); delay > 0 {
			return delay
		}
	}
	var abuseRateLimitErr *github.AbuseRateLimitError
	if errors.As(err, &abuseRateLimitErr) && abuseRateLimitErr.RetryAfter != nil && *abuseRateLimitErr.RetryAfter > 0 {
		return *abuseRateLimitErr.RetryAfter
	}
	if response != nil && response.Header.Get("Retry-After") != "" {
		if delay, err := time.ParseDuration(response.Header.Get("Retry-After") + "s"); err == nil && delay > 0 {
			return delay
		}
	}
	return initialRetryDelay << attempt
}
