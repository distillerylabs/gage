package main

import (
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// TestShowPrintsOnlyValue: gage show must print exactly the value field —
// not title, description, fields, or timestamps — unlike gage cat.
func TestShowPrintsOnlyValue(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	ins, _ := runCLIWithValue(t, []string{"insert", "GitHub", "--description", "personal account"}, "hunter2")
	if ins.Code != 0 {
		t.Fatalf("insert failed: %s", ins.Stderr)
	}

	show := runCLI(t, []string{"show", "GitHub"}, "")
	if show.Code != 0 {
		t.Fatalf("show failed: %s", show.Stderr)
	}
	if strings.TrimRight(show.Stdout, "\n") != "hunter2" {
		t.Errorf("show output = %q, want exactly the value %q", show.Stdout, "hunter2")
	}
	for _, leak := range []string{"GitHub", "personal account", "title:", "description:", "created:", "updated:"} {
		if strings.Contains(show.Stdout, leak) {
			t.Errorf("show output %q leaks metadata (%q) that only cat should print", show.Stdout, leak)
		}
	}
}

// TestCatPrintsFullYAMLDistinctFromShow: gage cat prints the full
// decrypted YAML, including metadata — the exact thing show must not.
func TestCatPrintsFullYAMLDistinctFromShow(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	ins, _ := runCLIWithValue(t, []string{"insert", "GitHub", "--description", "personal account"}, "hunter2")
	if ins.Code != 0 {
		t.Fatalf("insert failed: %s", ins.Stderr)
	}

	cat := runCLI(t, []string{"cat", "GitHub"}, "")
	if cat.Code != 0 {
		t.Fatalf("cat failed: %s", cat.Stderr)
	}
	for _, want := range []string{"title: GitHub", "description: personal account", "value: hunter2"} {
		if !strings.Contains(cat.Stdout, want) {
			t.Errorf("cat output missing %q:\n%s", want, cat.Stdout)
		}
	}
}

// TestShowAmbiguousQueryListsCandidatesAndFails mirrors cat/rm's
// ambiguous-query behavior for show.
func TestShowAmbiguousQueryListsCandidatesAndFails(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "AWS root account"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if res, _ := runCLIWithValue(t, []string{"insert", "AWS IAM backup admin"}, "v2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"show", "aws"}, "")
	if res.Code != int(exitcode.Ambiguous) {
		t.Errorf("exit code = %d, want %d (Ambiguous)", res.Code, exitcode.Ambiguous)
	}
	for _, want := range []string{"AWS root account", "AWS IAM backup admin"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr missing candidate %q:\n%s", want, res.Stderr)
		}
	}
}

// TestShowUnknownQueryFailsNotFound distinguishes the not-found case from
// the ambiguous one at the CLI level.
func TestShowUnknownQueryFailsNotFound(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"show", "no such thing"}, "")
	if res.Code != int(exitcode.NotFound) {
		t.Errorf("exit code = %d, want %d (NotFound)", res.Code, exitcode.NotFound)
	}
}
