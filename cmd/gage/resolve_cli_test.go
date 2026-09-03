package main

import (
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// TestCatResolvesByUUIDPrefixSubstringAndExact covers cat's re-wiring
// onto the shared resolver — no longer limited to M4's UUID-or-exact-
// title-only matching.
func TestCatResolvesByUUIDPrefixSubstringAndExact(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "AWS root account"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	full := soleEntryID(t, path).String()

	byPrefix := runCLI(t, []string{"cat", full[:8]}, "")
	if byPrefix.Code != 0 {
		t.Fatalf("cat by UUID prefix failed: %s", byPrefix.Stderr)
	}
	if !strings.Contains(byPrefix.Stdout, "value: v1") {
		t.Errorf("cat by UUID prefix output missing the value:\n%s", byPrefix.Stdout)
	}

	bySubstring := runCLI(t, []string{"cat", "root"}, "")
	if bySubstring.Code != 0 {
		t.Fatalf("cat by substring failed: %s", bySubstring.Stderr)
	}
	if !strings.Contains(bySubstring.Stdout, "value: v1") {
		t.Errorf("cat by substring output missing the value:\n%s", bySubstring.Stdout)
	}

	byExact := runCLI(t, []string{"cat", "AWS root account"}, "")
	if byExact.Code != 0 {
		t.Fatalf("cat by exact title failed: %s", byExact.Stderr)
	}
}

// TestCatAmbiguousQueryListsCandidatesAndFails: cat must list every
// candidate and fail with the Ambiguous exit code rather than silently
// acting on the first match or falling back to M4's stricter matching.
func TestCatAmbiguousQueryListsCandidatesAndFails(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "AWS root account"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if res, _ := runCLIWithValue(t, []string{"insert", "AWS IAM backup admin"}, "v2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"cat", "aws"}, "")
	if res.Code != int(exitcode.Ambiguous) {
		t.Errorf("exit code = %d, want %d (Ambiguous)", res.Code, exitcode.Ambiguous)
	}
	for _, want := range []string{"AWS root account", "AWS IAM backup admin"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr missing candidate %q:\n%s", want, res.Stderr)
		}
	}
	// Nothing must have been printed to stdout as if a match had been
	// picked silently.
	if res.Stdout != "" {
		t.Errorf("cat printed to stdout on an ambiguous query: %q", res.Stdout)
	}
}

// TestRmAmbiguousQueryListsCandidatesAndFailsWithoutDeletingAnything: an
// ambiguous rm must behave exactly like cat/show — list, fail — and, in
// particular, must not delete either candidate.
func TestRmAmbiguousQueryListsCandidatesAndFailsWithoutDeletingAnything(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "AWS root account"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if res, _ := runCLIWithValue(t, []string{"insert", "AWS IAM backup admin"}, "v2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"rm", "aws"}, "")
	if res.Code != int(exitcode.Ambiguous) {
		t.Errorf("exit code = %d, want %d (Ambiguous)", res.Code, exitcode.Ambiguous)
	}
	for _, want := range []string{"AWS root account", "AWS IAM backup admin"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr missing candidate %q:\n%s", want, res.Stderr)
		}
	}

	ls := runCLI(t, []string{"ls"}, "")
	for _, want := range []string{"AWS root account", "AWS IAM backup admin"} {
		if !strings.Contains(ls.Stdout, want) {
			t.Errorf("an ambiguous rm deleted something: %q missing from ls output %q", want, ls.Stdout)
		}
	}
}

// TestRmResolvesByUUIDPrefixAndSubstring is rm's half of the "resolve
// exactly like show" bullet.
func TestRmResolvesByUUIDPrefixAndSubstring(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "AWS root account"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	rm := runCLI(t, []string{"rm", "root"}, "")
	if rm.Code != 0 {
		t.Fatalf("rm by substring failed: %s", rm.Stderr)
	}

	ls := runCLI(t, []string{"ls"}, "")
	if strings.Contains(ls.Stdout, "AWS root account") {
		t.Errorf("rm by substring didn't remove the entry: %q", ls.Stdout)
	}
}
