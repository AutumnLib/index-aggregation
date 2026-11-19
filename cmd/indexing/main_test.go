package main

import (
	"testing"
)

func TestRunRequiresGitHubToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	if err := run(); err == nil {
		t.Fatal("run() succeeded without GITHUB_TOKEN")
	}
}
