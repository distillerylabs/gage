package main

import (
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
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

// TestShowMultilineValueHasNoExtraTrailingBlankLine: -m/--multiline
// stores the value with its trailing newline intact (unlike
// --value-stdin, which trims one), so show must not add a second one on
// top — it should print exactly the stored bytes, not value+"\n".
func TestShowMultilineValueHasNoExtraTrailingBlankLine(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	ins := runCLI(t, []string{"insert", "Recovery codes", "-m"}, "line1\nline2\n")
	if ins.Code != 0 {
		t.Fatalf("insert failed: %s", ins.Stderr)
	}

	show := runCLI(t, []string{"show", "Recovery codes"}, "")
	if show.Code != 0 {
		t.Fatalf("show failed: %s", show.Stderr)
	}
	if show.Stdout != "line1\nline2\n" {
		t.Errorf("show output = %q, want %q", show.Stdout, "line1\nline2\n")
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
	e, err := gage.UnmarshalEntry([]byte(cat.Stdout))
	if err != nil {
		t.Fatalf("parsing cat output: %v\n%s", err, cat.Stdout)
	}
	if e.Title != "GitHub" {
		t.Errorf("title = %q, want %q", e.Title, "GitHub")
	}
	if e.Description != "personal account" {
		t.Errorf("description = %q, want %q", e.Description, "personal account")
	}
	if e.Value != "hunter2" {
		t.Errorf("value = %q, want %q", e.Value, "hunter2")
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

// TestShowFieldExtractsNamedField: --field NAME prints one entry from
// `fields` instead of the value, and nothing else.
func TestShowFieldExtractsNamedField(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntryWithFields(t, "Proton", "the-password", map[string]string{
		"username":  "me@example.com",
		"totp_seed": "JBSWY3DPEHPK3PXP",
	})

	for name, want := range map[string]string{
		"username":  "me@example.com",
		"totp_seed": "JBSWY3DPEHPK3PXP",
	} {
		res := runCLI(t, []string{"show", "Proton", "--field", name}, "")
		if res.Code != 0 {
			t.Fatalf("show --field %s failed: %s", name, res.Stderr)
		}
		if got := strings.TrimRight(res.Stdout, "\n"); got != want {
			t.Errorf("show --field %s = %q, want %q", name, got, want)
		}
		if strings.Contains(res.Stdout, "the-password") {
			t.Errorf("show --field %s leaked the primary value", name)
		}
	}
}

// TestShowUnknownFieldFailsClearly: an unknown --field name is NotFound,
// names the fields the entry does have, and prints no value at all —
// the human can't see inside the entry to check the spelling themselves.
func TestShowUnknownFieldFailsClearly(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntryWithFields(t, "Proton", "the-password", map[string]string{
		"username": "me@example.com",
	})

	res := runCLI(t, []string{"show", "Proton", "--field", "passwrod"}, "")
	if res.Code != int(exitcode.NotFound) {
		t.Errorf("exit code = %d, want %d (NotFound)", res.Code, exitcode.NotFound)
	}
	if !strings.Contains(res.Stderr, "passwrod") || !strings.Contains(res.Stderr, "username") {
		t.Errorf("stderr should name both the missing field and the available ones: %q", res.Stderr)
	}
	if strings.Contains(res.Stdout, "the-password") || strings.Contains(res.Stderr, "the-password") {
		t.Error("a failed --field lookup leaked the entry's value")
	}
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("a failed --field lookup wrote %q to stdout", res.Stdout)
	}
}

// TestShowFieldOnEntryWithNoFields: the message says the entry has none
// at all, rather than listing an empty set.
func TestShowFieldOnEntryWithNoFields(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"show", "GitHub", "--field", "username"}, "")
	if res.Code != int(exitcode.NotFound) {
		t.Errorf("exit code = %d, want %d (NotFound)", res.Code, exitcode.NotFound)
	}
	if !strings.Contains(res.Stderr, "no fields") {
		t.Errorf("stderr should say the entry has no fields: %q", res.Stderr)
	}
}

// TestShowClipCopiesTheNamedFieldNotTheValue: --field composes with -c
// the same way it composes with -q — it chooses which value, and the
// output flag chooses where it goes.
func TestShowClipCopiesTheNamedFieldNotTheValue(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntryWithFields(t, "Proton", "the-password", map[string]string{
		"username": "me@example.com",
	})

	run := runCLIWithClipboard(t, []string{"show", "Proton", "--field", "username", "-c"})
	if run.Code != 0 {
		t.Fatalf("show --field -c failed: %s", run.Stderr)
	}
	if len(run.cb.writes) == 0 || run.cb.writes[0] != "me@example.com" {
		t.Errorf("clipboard writes = %q, want the field value first", run.cb.writes)
	}
}
