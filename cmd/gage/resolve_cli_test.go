package main

import (
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// noChoicePrompter answers a passphrase normally (an ambiguous query is
// only reachable once the vault is unlocked) but fails the test the
// moment anything asks it to pick from a candidate list. That is what
// makes "one-shot mode fails *instead of prompting*" an assertion rather
// than an assumption: with an ordinary fake prompter, a command that
// wrongly called Choose would get a silent "" back and the test could
// still pass. Session mode (M6) is what may legitimately call Choose.
type noChoicePrompter struct{ t *testing.T }

func (p *noChoicePrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	return gage.UnlockResponse{Kind: gage.KindPassphrase, Passphrase: testPassphrase}, nil
}
func (p *noChoicePrompter) Confirm(prompt string) (bool, error) { return true, nil }
func (p *noChoicePrompter) Choose(list gage.CandidateList) (string, error) {
	p.t.Helper()
	p.t.Error("Prompter.Choose was called; one-shot mode must fail with the candidate list, never prompt")
	return "", nil
}
func (p *noChoicePrompter) Warn(msg string) {}
func (p *noChoicePrompter) Value(prompt string) (string, error) {
	return "", nil
}

// twoAWSEntries sets up the ambiguity fixture every ambiguous-query test
// below shares: two entries whose titles both contain "aws", neither of
// which is an exact match for it.
func twoAWSEntries(t *testing.T) {
	t.Helper()
	if res, _ := runCLIWithValue(t, []string{"insert", "AWS root account"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if res, _ := runCLIWithValue(t, []string{"insert", "AWS IAM backup admin"}, "v2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
}

// TestShowCatRmResolveIdenticallyAcrossQueryForms is the core of the
// milestone's "every query-taking command resolves identically" claim:
// the same entry is addressed by UUID prefix, exact title, and unique
// substring through show, cat, and rm in turn, and each command must
// accept all three forms. rm is exercised last per subtest (it deletes),
// so each query form gets its own freshly-populated vault.
func TestShowCatRmResolveIdenticallyAcrossQueryForms(t *testing.T) {
	for _, qf := range []struct {
		name string
		// query is built from the entry's real UUID, since the prefix
		// form isn't known until the entry exists.
		query func(fullUUID string) string
	}{
		{"exact-title", func(string) string { return "AWS root account" }},
		{"unique-substring", func(string) string { return "root" }},
		{"case-insensitive-substring", func(string) string { return "ROOT" }},
		{"exact-uuid", func(full string) string { return full }},
		{"uuid-prefix", func(full string) string { return full[:8] }},
		{"short-uuid-prefix", func(full string) string { return full[:4] }},
		{"uuid-mid-string-substring", func(full string) string {
			// Characters 9-16 of the canonical form (past the first
			// hyphen), never a prefix — proves the stage is a genuine
			// substring search, not prefix-only.
			return full[9:17]
		}},
	} {
		t.Run(qf.name, func(t *testing.T) {
			isolateXDG(t)
			path := initEntryTestVault(t, "personal")
			if res, _ := runCLIWithValue(t, []string{"insert", "AWS root account"}, "v1"); res.Code != 0 {
				t.Fatalf("insert failed: %s", res.Stderr)
			}
			query := qf.query(soleEntryID(t, path).String())

			show := runCLI(t, []string{"show", query}, "")
			if show.Code != 0 {
				t.Fatalf("show %q failed: %s", query, show.Stderr)
			}
			if strings.TrimRight(show.Stdout, "\n") != "v1" {
				t.Errorf("show %q printed %q, want the resolved entry's value %q", query, show.Stdout, "v1")
			}

			cat := runCLI(t, []string{"cat", query}, "")
			if cat.Code != 0 {
				t.Fatalf("cat %q failed: %s", query, cat.Stderr)
			}
			if !strings.Contains(cat.Stdout, "title: AWS root account") {
				t.Errorf("cat %q resolved to the wrong entry:\n%s", query, cat.Stdout)
			}

			rm := runCLI(t, []string{"rm", query}, "")
			if rm.Code != 0 {
				t.Fatalf("rm %q failed: %s", query, rm.Stderr)
			}
			ls := runCLI(t, []string{"ls"}, "")
			if strings.Contains(ls.Stdout, "AWS root account") {
				t.Errorf("rm %q did not remove the entry: %q", query, ls.Stdout)
			}
		})
	}
}

// TestCatResolvesByExactTitleEvenWhenQueryIsAlsoAUUIDSubstring is the
// CLI-level regression test for the bug this milestone's original
// implementation shipped: a query naming one entry's exact title, that
// also happens to be a substring of a *different* entry's UUID, must
// resolve to the entry it names — not silently to the other one.
//
// The collision can't be scripted by choosing a title in advance (UUIDs
// are randomly generated), so this inserts a first entry, reads back its
// real, on-disk UUID, and titles a second entry with an actual substring
// of it — guaranteeing the exact cross-stage collision that reached CI,
// through the real `gage insert`/`gage cat` commands rather than the
// library directly.
func TestCatResolvesByExactTitleEvenWhenQueryIsAlsoAUUIDSubstring(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "Website Password"}, "correcthorsebattery"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	full := soleEntryID(t, path).String()
	collidingSubstring := strings.ReplaceAll(full, "-", "")[:4] // e.g. "22ed"

	if res, _ := runCLIWithValue(t, []string{"insert", collidingSubstring}, "some-other-secret"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	cat := runCLI(t, []string{"cat", collidingSubstring}, "")
	if cat.Code != 0 {
		t.Fatalf("cat %q failed: %s", collidingSubstring, cat.Stderr)
	}
	if !strings.Contains(cat.Stdout, "title: "+collidingSubstring) {
		t.Errorf("cat %q resolved to the wrong entry (want the exact-title match, titled %q):\n%s",
			collidingSubstring, collidingSubstring, cat.Stdout)
	}
	if strings.Contains(cat.Stdout, "correcthorsebattery") {
		t.Errorf("cat %q leaked the *other* entry's value — it silently resolved to \"Website Password\" instead of %q",
			collidingSubstring, collidingSubstring)
	}
}

// TestAmbiguousQueryListsCandidatesAndFailsWithoutPromptingForEveryCommand
// covers the ambiguity half of the same claim, for every query-taking
// command: candidates listed on stderr, nonzero Ambiguous exit, nothing
// written to stdout, and — via noChoicePrompter — no prompt attempted.
func TestAmbiguousQueryListsCandidatesAndFailsWithoutPromptingForEveryCommand(t *testing.T) {
	for _, args := range [][]string{
		{"show", "aws"},
		{"cat", "aws"},
		{"rm", "aws"},
		{"edit", "aws"},
		{"rename", "aws", "Whatever"},
	} {
		t.Run(args[0], func(t *testing.T) {
			isolateXDG(t)
			initEntryTestVault(t, "personal")
			twoAWSEntries(t)

			// An $EDITOR that fails the test if it ever runs: an
			// ambiguous edit must never reach the editor at all.
			setFakeEditor(t, "fail", nil)

			res, _ := runCLIWithPrompter(t, args, "", false, &noChoicePrompter{t: t})
			if res.Code != int(exitcode.Ambiguous) {
				t.Errorf("exit code = %d, want %d (Ambiguous)", res.Code, exitcode.Ambiguous)
			}
			for _, want := range []string{"AWS root account", "AWS IAM backup admin"} {
				if !strings.Contains(res.Stderr, want) {
					t.Errorf("stderr missing candidate %q:\n%s", want, res.Stderr)
				}
			}
			if res.Stdout != "" {
				t.Errorf("%s printed to stdout on an ambiguous query: %q", args[0], res.Stdout)
			}
		})
	}
}

// TestAmbiguousRmAndRenameLeaveEveryCandidateUntouched is the
// "neither command silently acts on the first match" half: an ambiguous
// mutating command must change nothing at all.
func TestAmbiguousRmAndRenameLeaveEveryCandidateUntouched(t *testing.T) {
	for _, args := range [][]string{
		{"rm", "aws"},
		{"rename", "aws", "Whatever"},
	} {
		t.Run(args[0], func(t *testing.T) {
			isolateXDG(t)
			initEntryTestVault(t, "personal")
			twoAWSEntries(t)

			if res := runCLI(t, args, ""); res.Code != int(exitcode.Ambiguous) {
				t.Fatalf("exit code = %d, want %d (Ambiguous)", res.Code, exitcode.Ambiguous)
			}

			ls := runCLI(t, []string{"ls"}, "")
			for _, want := range []string{"AWS root account", "AWS IAM backup admin"} {
				if !strings.Contains(ls.Stdout, want) {
					t.Errorf("an ambiguous %s modified or deleted %q; ls = %q", args[0], want, ls.Stdout)
				}
			}
			if strings.Contains(ls.Stdout, "Whatever") {
				t.Errorf("an ambiguous rename renamed something: %q", ls.Stdout)
			}
		})
	}
}

// TestUnknownQueryIsDistinguishableFromAmbiguousAtTheCLI: not-found and
// ambiguous must reach the shell as different exit codes, not one
// undifferentiated failure.
func TestUnknownQueryIsDistinguishableFromAmbiguousAtTheCLI(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	twoAWSEntries(t)

	notFound := runCLI(t, []string{"cat", "no such thing at all"}, "")
	ambiguous := runCLI(t, []string{"cat", "aws"}, "")

	if notFound.Code != int(exitcode.NotFound) {
		t.Errorf("unknown query exit code = %d, want %d (NotFound)", notFound.Code, exitcode.NotFound)
	}
	if ambiguous.Code != int(exitcode.Ambiguous) {
		t.Errorf("ambiguous query exit code = %d, want %d (Ambiguous)", ambiguous.Code, exitcode.Ambiguous)
	}
	if notFound.Code == ambiguous.Code {
		t.Error("not-found and ambiguous queries produced the same exit code at the CLI")
	}
	// The not-found path must not print a candidate list, since there are
	// no candidates to pick from.
	if strings.Contains(notFound.Stderr, "AWS root account") {
		t.Errorf("a not-found query printed candidates: %q", notFound.Stderr)
	}
}
