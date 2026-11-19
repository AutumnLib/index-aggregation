package indexaggregation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v93/github"
)

func TestSearchRepositoriesSortsByStarsAndPreservesOptions(t *testing.T) {
	var query url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":0,"incomplete_results":false,"items":[]}`))
	}))
	defer server.Close()

	client, err := github.NewClient(
		github.WithHTTPClient(server.Client()),
		github.WithEnterpriseURLs(server.URL+"/", server.URL+"/"),
	)
	if err != nil {
		t.Fatalf("NewClient returned an error: %v", err)
	}
	opts := &github.SearchOptions{
		Sort:  "updated",
		Order: "asc",
		ListOptions: github.ListOptions{
			Page:    2,
			PerPage: 25,
		},
	}

	_, _, err = SearchRepositories(context.Background(), client, "language:go", opts)
	if err != nil {
		t.Fatalf("SearchRepositories returned an error: %v", err)
	}

	if got := query.Get("sort"); got != "stars" {
		t.Errorf("sort = %q, want %q", got, "stars")
	}
	if got := query.Get("order"); got != "desc" {
		t.Errorf("order = %q, want %q", got, "desc")
	}
	if got := query.Get("q"); got != "language:go" {
		t.Errorf("q = %q, want %q", got, "language:go")
	}
	if got := query.Get("page"); got != "2" {
		t.Errorf("page = %q, want %q", got, "2")
	}
	if got := query.Get("per_page"); got != "25" {
		t.Errorf("per_page = %q, want %q", got, "25")
	}
	if opts.Sort != "updated" || opts.Order != "asc" {
		t.Errorf("SearchRepositories modified caller options: sort=%q order=%q", opts.Sort, opts.Order)
	}
}
