package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	indexaggregation "github.com/AutumnLib/index-aggregation"
	"github.com/google/go-github/v93/github"
)

const catalogPath = "output/index.json"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return errors.New("GITHUB_TOKEN is required")
	}

	previous, err := readCatalog(catalogPath)
	if err != nil {
		return err
	}
	client, err := github.NewClient(github.WithAuthToken(token))
	if err != nil {
		return fmt.Errorf("create GitHub client: %w", err)
	}
	catalog, err := indexaggregation.DiscoverRepositories(context.Background(), client, indexaggregation.DefaultSearchQueries())
	if err != nil {
		return err
	}
	if err := indexaggregation.PreserveReviewDecisions(catalog, previous); err != nil {
		return fmt.Errorf("preserve repository review decisions: %w", err)
	}

	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("encode repository catalog: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0o755); err != nil {
		return fmt.Errorf("create catalog directory: %w", err)
	}
	if err := os.WriteFile(catalogPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write repository catalog: %w", err)
	}

	fmt.Printf("Wrote %d candidates from %d searches to %s\n", len(catalog.Repositories), len(catalog.Searches), catalogPath)
	for _, search := range catalog.Searches {
		if search.IncompleteResults || search.ReachedSearchLimit {
			fmt.Printf("Coverage warning for %q: found=%d retrieved=%d incomplete=%t limit_reached=%t\n",
				search.Query, search.ResultsFound, search.ResultsRetrieved, search.IncompleteResults, search.ReachedSearchLimit)
		}
	}
	return nil
}

func readCatalog(path string) (*indexaggregation.Catalog, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read previous repository catalog: %w", err)
	}
	var catalog indexaggregation.Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("decode previous repository catalog: %w", err)
	}
	return &catalog, nil
}
