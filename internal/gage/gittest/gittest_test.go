package gittest

import (
	"os"
	"testing"

	"github.com/go-git/go-git/v5"
)

func TestNewBareRemoteIsABareRepo(t *testing.T) {
	path := NewBareRemote(t)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("remote path does not exist: %v", err)
	}

	repo, err := git.PlainOpen(path)
	if err != nil {
		t.Fatalf("opening remote as a git repo: %v", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("reading repo config: %v", err)
	}
	if !cfg.Core.IsBare {
		t.Error("repo is not bare")
	}
}

func TestNewBareRemoteProducesIndependentRepos(t *testing.T) {
	first := NewBareRemote(t)
	second := NewBareRemote(t)

	if first == second {
		t.Fatalf("two calls returned the same path: %s", first)
	}

	if _, err := git.PlainOpen(first); err != nil {
		t.Errorf("first remote not a valid repo: %v", err)
	}
	if _, err := git.PlainOpen(second); err != nil {
		t.Errorf("second remote not a valid repo: %v", err)
	}
}
