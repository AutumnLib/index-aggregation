package indexaggregation

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
)

func TestDiscoverRepositoriesPaginatesDeduplicatesAndSorts(t *testing.T) {
	readme := base64.StdEncoding.EncodeToString([]byte("# Original library\nReusable VEX motion code."))
	searchRequests := 0
	readmeRequests := make(map[string]int)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/api/v3"))
		switch {
		case path == "/search/repositories":
			searchRequests++
			if got := r.URL.Query().Get("sort"); got != "stars" {
				t.Errorf("sort = %q, want stars", got)
			}
			if got := r.URL.Query().Get("order"); got != "desc" {
				t.Errorf("order = %q, want desc", got)
			}
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.URL.Query().Get("q") == "first" && r.URL.Query().Get("page") == "1":
				w.Header().Set("Link", fmt.Sprintf("<%s/api/v3/search/repositories?q=first&page=2&per_page=100&sort=stars&order=desc>; rel=\"next\"", server.URL))
				fmt.Fprintf(w, `{"total_count":3,"incomplete_results":false,"items":[%s,%s]}`,
					repositoryJSON("Org/a", 5, `["z","a"]`),
					repositoryJSON("Org/b", 2, `[]`))
			case r.URL.Query().Get("q") == "first" && r.URL.Query().Get("page") == "2":
				fmt.Fprintf(w, `{"total_count":3,"incomplete_results":false,"items":[%s,%s]}`,
					repositoryJSON("Org/c", 7, `[]`),
					repositoryJSON("Org/a", 5, `[]`))
			case r.URL.Query().Get("q") == "second":
				fmt.Fprintf(w, `{"total_count":1,"incomplete_results":false,"items":[%s]}`,
					repositoryJSON("Org/d", 7, `[]`))
			default:
				t.Errorf("unexpected search request: %s", r.URL.String())
				http.Error(w, "unexpected request", http.StatusBadRequest)
			}
		case strings.HasPrefix(path, "/repos/"):
			name := strings.TrimSuffix(strings.TrimPrefix(path, "/repos/org/"), "/readme")
			readmeRequests[name]++
			if name == "b" {
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"name":"README.md","path":"README.md","encoding":"base64","content":%q}`, readme)
		default:
			t.Errorf("unexpected request: %s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := github.NewClient(
		github.WithHTTPClient(server.Client()),
		github.WithEnterpriseURLs(server.URL+"/", server.URL+"/"),
	)
	if err != nil {
		t.Fatalf("NewClient returned an error: %v", err)
	}

	catalog, err := DiscoverRepositories(context.Background(), client, []string{"first", "second"})
	if err != nil {
		t.Fatalf("DiscoverRepositories returned an error: %v", err)
	}
	if searchRequests != 3 {
		t.Errorf("search requests = %d, want 3", searchRequests)
	}
	if len(catalog.Searches) != 2 || catalog.Searches[0].ResultsRetrieved != 4 {
		t.Fatalf("search reports = %#v, want two reports and four first-query results", catalog.Searches)
	}
	if got, want := len(catalog.Repositories), 4; got != want {
		t.Fatalf("candidate count = %d, want %d", got, want)
	}

	gotNames := make([]string, len(catalog.Repositories))
	for i, candidate := range catalog.Repositories {
		gotNames[i] = candidate.FullName
		if candidate.ReviewStatus != "candidate" {
			t.Errorf("%s review status = %q, want candidate", candidate.FullName, candidate.ReviewStatus)
		}
	}
	if got, want := strings.Join(gotNames, ","), "Org/c,Org/d,Org/a,Org/b"; got != want {
		t.Errorf("repository order = %q, want %q", got, want)
	}
	if got := strings.Join(catalog.Repositories[2].Topics, ","); got != "a,z" {
		t.Errorf("topics = %q, want a,z", got)
	}
	if !catalog.Repositories[0].ReadmeEvidence.Available ||
		!strings.Contains(catalog.Repositories[0].ReadmeEvidence.Excerpt, "Reusable VEX motion code.") {
		t.Errorf("README evidence = %#v, want available excerpt", catalog.Repositories[0].ReadmeEvidence)
	}
	if got := catalog.Repositories[3].ReadmeEvidence.Reason; got != "README not found" {
		t.Errorf("missing README reason = %q, want README not found", got)
	}
	for _, name := range []string{"a", "b", "c", "d"} {
		if readmeRequests[name] != 1 {
			t.Errorf("README requests for %s = %d, want 1", name, readmeRequests[name])
		}
	}

	queriesByName := make(map[string][]string)
	for _, candidate := range catalog.Repositories {
		queriesByName[candidate.FullName] = candidate.DiscoveryQueries
	}
	if got := strings.Join(queriesByName["Org/a"], ","); got != "first" {
		t.Errorf("Org/a discovery queries = %q, want first", got)
	}
	if got := strings.Join(queriesByName["Org/d"], ","); got != "second" {
		t.Errorf("Org/d discovery queries = %q, want second", got)
	}
}

func TestDiscoverRepositoriesRejectsEmptyQueries(t *testing.T) {
	client, err := github.NewClient()
	if err != nil {
		t.Fatalf("NewClient returned an error: %v", err)
	}
	if _, err := DiscoverRepositories(context.Background(), client, nil); err == nil {
		t.Fatal("DiscoverRepositories succeeded without search queries")
	}
}

func TestPreserveReviewDecisions(t *testing.T) {
	current := &Catalog{Repositories: []RepositoryCandidate{
		{FullName: "Org/kept", ReviewStatus: "candidate", ReviewNote: "Verify reusable or original logic before inclusion."},
		{FullName: "Org/new", ReviewStatus: "candidate"},
	}}
	previous := &Catalog{Repositories: []RepositoryCandidate{
		{FullName: "Org/kept", ReviewStatus: "included", ReviewNote: "Reusable odometry implementation."},
		{FullName: "Org/gone", ReviewStatus: "excluded", ReviewNote: "Application only."},
	}}

	if err := PreserveReviewDecisions(current, previous); err != nil {
		t.Fatalf("PreserveReviewDecisions returned an error: %v", err)
	}
	if got := current.Repositories[0].ReviewStatus; got != "included" {
		t.Errorf("review status = %q, want included", got)
	}
	if got := current.Repositories[0].ReviewNote; got != "Reusable odometry implementation." {
		t.Errorf("review note = %q, want preserved note", got)
	}
	if got := current.Repositories[1].ReviewStatus; got != "candidate" {
		t.Errorf("new repository review status = %q, want candidate", got)
	}
}

func TestRetryGitHubCallRetriesServerError(t *testing.T) {
	calls := 0
	want := "success"
	got, _, err := retryGitHubCall(context.Background(), func() (*string, *github.Response, error) {
		calls++
		if calls == 1 {
			return nil, &github.Response{Response: &http.Response{StatusCode: http.StatusServiceUnavailable}}, errors.New("temporary failure")
		}
		return &want, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil
	})
	if err != nil {
		t.Fatalf("retryGitHubCall returned an error: %v", err)
	}
	if calls != 2 {
		t.Errorf("call count = %d, want 2", calls)
	}
	if got == nil || *got != want {
		t.Errorf("result = %v, want %q", got, want)
	}
}

func repositoryJSON(fullName string, stars int, topics string) string {
	return fmt.Sprintf(
		`{"full_name":%q,"html_url":%q,"description":"robotics repository","stargazers_count":%d,"topics":%s,"language":"Go"}`,
		fullName,
		"https://github.com/"+url.PathEscape(fullName),
		stars,
		topics,
	)
}
