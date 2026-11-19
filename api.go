package indexaggregation

import (
	"context"
	"errors"

	"github.com/google/go-github/v93/github"
)

// SearchRepositories searches GitHub repositories with the most-starred
// repositories first. Other search options, including pagination, are retained.
func SearchRepositories(ctx context.Context, client *github.Client, query string, opts *github.SearchOptions) (*github.RepositoriesSearchResult, *github.Response, error) {
	if client == nil {
		return nil, nil, errors.New("github client is nil")
	}

	searchOpts := github.SearchOptions{}
	if opts != nil {
		searchOpts = *opts
	}
	searchOpts.Sort = "stars"
	searchOpts.Order = "desc"

	return client.Search.Repositories(ctx, query, &searchOpts)
}
