package main

import (
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// TestInsertFieldRoundTripsThroughShow: `--field NAME=VALUE` at insert
// time is readable back via `show --field NAME`, the non-interactive
// route this issue adds alongside insert -e.
func TestInsertFieldRoundTripsThroughShow(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res, _ := runCLIWithValue(t, []string{"insert", "Site", "--field", "user=bob"}, "v")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	show := runCLI(t, []string{"show", "Site", "--field", "user"}, "")
	if show.Code != 0 {
		t.Fatalf("show --field failed: %s", show.Stderr)
	}
	if got := strings.TrimRight(show.Stdout, "\n"); got != "bob" {
		t.Errorf("show --field user = %q, want %q", got, "bob")
	}
}

// TestInsertMultipleFieldFlagsEachIndependentlyReadable: several --field
// flags each land as their own key, independently readable via `show
// --field`.
func TestInsertMultipleFieldFlagsEachIndependentlyReadable(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res, _ := runCLIWithValue(t, []string{
		"insert", "Site",
		"--field", "user=bob",
		"--field", "totp_seed=JBSWY3DPEHPK3PXP",
	}, "v")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	for name, want := range map[string]string{
		"user":      "bob",
		"totp_seed": "JBSWY3DPEHPK3PXP",
	} {
		show := runCLI(t, []string{"show", "Site", "--field", name}, "")
		if show.Code != 0 {
			t.Fatalf("show --field %s failed: %s", name, show.Stderr)
		}
		if got := strings.TrimRight(show.Stdout, "\n"); got != want {
			t.Errorf("show --field %s = %q, want %q", name, got, want)
		}
	}
}

// TestInsertFieldValueContainingEqualsIsPreservedVerbatim: only the
// *first* `=` splits NAME from VALUE, so a value that itself contains
// `=` (e.g. a base64 blob) survives intact.
func TestInsertFieldValueContainingEqualsIsPreservedVerbatim(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res, _ := runCLIWithValue(t, []string{"insert", "Site", "--field", "note=a=b=c"}, "v")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	show := runCLI(t, []string{"show", "Site", "--field", "note"}, "")
	if show.Code != 0 {
		t.Fatalf("show --field failed: %s", show.Stderr)
	}
	if got := strings.TrimRight(show.Stdout, "\n"); got != "a=b=c" {
		t.Errorf("show --field note = %q, want %q", got, "a=b=c")
	}
}

// TestInsertFieldMissingEqualsIsUsageErrorBeforeUnlock: a --field arg
// with no `=` at all is rejected before any prompt or unlock, the same
// posture as the existing -m/--value-stdin/-e checks.
func TestInsertFieldMissingEqualsIsUsageErrorBeforeUnlock(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &explodingPrompter{t: t}
	in := &explodingReader{t: t}
	res, _ := runCLIWithPrompterAndStdin(t, []string{"insert", "Site", "--field", "noequals"}, in, false, p)
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if !strings.Contains(res.Stderr, "missing '='") {
		t.Errorf("stderr = %q, want it to mention the missing '='", res.Stderr)
	}
}

// TestInsertFieldEmptyNameIsUsageErrorBeforeUnlock: `--field =value` has
// an empty NAME, rejected before any prompt or unlock.
func TestInsertFieldEmptyNameIsUsageErrorBeforeUnlock(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &explodingPrompter{t: t}
	in := &explodingReader{t: t}
	res, _ := runCLIWithPrompterAndStdin(t, []string{"insert", "Site", "--field", "=value"}, in, false, p)
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if !strings.Contains(res.Stderr, "empty") {
		t.Errorf("stderr = %q, want it to mention the empty NAME", res.Stderr)
	}
}

// TestInsertDuplicateFieldNameIsUsageErrorBeforeUnlock: the same NAME
// given across two --field flags is a usage error, not silent
// last-wins — mirrors Insert's own duplicate-title guard.
func TestInsertDuplicateFieldNameIsUsageErrorBeforeUnlock(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &explodingPrompter{t: t}
	in := &explodingReader{t: t}
	res, _ := runCLIWithPrompterAndStdin(t, []string{
		"insert", "Site",
		"--field", "user=bob",
		"--field", "user=alice",
	}, in, false, p)
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if !strings.Contains(res.Stderr, "more than once") {
		t.Errorf("stderr = %q, want it to mention the duplicate NAME", res.Stderr)
	}
}

// TestInsertFieldWithEditFlagRejectedAsMutuallyExclusive: --field and
// -e/--edit are two different ways to set fields, so combining them is
// rejected rather than picking a winner.
func TestInsertFieldWithEditFlagRejectedAsMutuallyExclusive(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &explodingPrompter{t: t}
	in := &explodingReader{t: t}
	res, _ := runCLIWithPrompterAndStdin(t, []string{
		"insert", "Site", "--field", "user=bob", "-e",
	}, in, false, p)
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if !strings.Contains(res.Stderr, "mutually exclusive") {
		t.Errorf("stderr = %q, want it to explain the flags are mutually exclusive", res.Stderr)
	}
}

// TestInsertFieldWithValueStdinSetsBothValueAndFields: --field layers on
// top of --value-stdin independently, since --value-stdin only ever sets
// `value`.
func TestInsertFieldWithValueStdinSetsBothValueAndFields(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"insert", "Site", "--value-stdin", "--field", "user=bob"}, "stdin-value\n")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	e := catEntry(t, "Site")
	if e.Value != "stdin-value" {
		t.Errorf("value = %q, want %q", e.Value, "stdin-value")
	}
	if e.Fields["user"] != "bob" {
		t.Errorf("Fields[user] = %q, want %q (fields = %v)", e.Fields["user"], "bob", e.Fields)
	}
}

// TestInsertFieldWithMultilineSetsBothValueAndFields: --field layers on
// top of -m/--multiline independently too, and -m's value keeps its
// trailing newline (unlike --value-stdin, -m trims nothing).
func TestInsertFieldWithMultilineSetsBothValueAndFields(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"insert", "Site", "-m", "--field", "user=bob"}, "line1\nline2\n")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	// show, not catEntry: cat's display format dedents this multi-line
	// value's block-scalar content, which no longer round-trips through
	// gage.UnmarshalEntry — show prints the raw value/field verbatim.
	value := runCLI(t, []string{"show", "Site"}, "")
	if value.Code != 0 {
		t.Fatalf("show failed: %s", value.Stderr)
	}
	if value.Stdout != "line1\nline2\n" {
		t.Errorf("value = %q, want %q", value.Stdout, "line1\nline2\n")
	}

	field := runCLI(t, []string{"show", "Site", "--field", "user"}, "")
	if field.Code != 0 {
		t.Fatalf("show --field user failed: %s", field.Stderr)
	}
	if field.Stdout != "bob\n" {
		t.Errorf("Fields[user] = %q, want %q", field.Stdout, "bob\n")
	}
}

// TestInsertFieldEmptyValueIsStored: `--field NAME=` (nothing after the
// `=`) is a field with an empty value, not an error — only an empty NAME
// is rejected.
func TestInsertFieldEmptyValueIsStored(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res, _ := runCLIWithValue(t, []string{"insert", "Site", "--field", "user="}, "v")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	e := catEntry(t, "Site")
	got, ok := e.Fields["user"]
	if !ok {
		t.Fatalf("Fields has no %q key (fields = %v)", "user", e.Fields)
	}
	if got != "" {
		t.Errorf("Fields[user] = %q, want empty", got)
	}
}

// TestInsertFieldNameWhitespaceIsStripped: every whitespace character in
// NAME — leading, trailing, or interior — is removed, so a stray space
// can't create a key that `show --field` then can't be typed to match.
func TestInsertFieldNameWhitespaceIsStripped(t *testing.T) {
	for _, raw := range []string{" bob", "b ob", "bob ", "\tb o\tb\n"} {
		t.Run(raw, func(t *testing.T) {
			isolateXDG(t)
			initEntryTestVault(t, "personal")

			res, _ := runCLIWithValue(t, []string{"insert", "Site", "--field", raw + "=v"}, "x")
			if res.Code != 0 {
				t.Fatalf("insert failed: %s", res.Stderr)
			}

			e := catEntry(t, "Site")
			if len(e.Fields) != 1 || e.Fields["bob"] != "v" {
				t.Errorf("fields = %v, want exactly map[bob:v]", e.Fields)
			}
		})
	}
}

// TestInsertFieldValueWhitespaceIsPreserved: stripping applies to NAME
// only — VALUE is stored verbatim, whitespace included.
func TestInsertFieldValueWhitespaceIsPreserved(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res, _ := runCLIWithValue(t, []string{"insert", "Site", "--field", "note= a b "}, "v")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	e := catEntry(t, "Site")
	if e.Fields["note"] != " a b " {
		t.Errorf("Fields[note] = %q, want %q", e.Fields["note"], " a b ")
	}
}

// TestInsertFieldWhitespaceOnlyNameIsUsageErrorBeforeUnlock: a NAME that
// is nothing but whitespace is empty once stripped, and rejected the
// same way `--field =value` is.
func TestInsertFieldWhitespaceOnlyNameIsUsageErrorBeforeUnlock(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &explodingPrompter{t: t}
	in := &explodingReader{t: t}
	res, _ := runCLIWithPrompterAndStdin(t, []string{"insert", "Site", "--field", " \t =value"}, in, false, p)
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if !strings.Contains(res.Stderr, "empty") {
		t.Errorf("stderr = %q, want it to mention the empty NAME", res.Stderr)
	}
}

// TestInsertFieldNamesEqualAfterStrippingAreDuplicates: duplicate
// detection runs on the stripped NAME, so `user` and `us er` collide
// rather than one silently overwriting the other.
func TestInsertFieldNamesEqualAfterStrippingAreDuplicates(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &explodingPrompter{t: t}
	in := &explodingReader{t: t}
	res, _ := runCLIWithPrompterAndStdin(t, []string{
		"insert", "Site",
		"--field", "user=bob",
		"--field", "us er=alice",
	}, in, false, p)
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if !strings.Contains(res.Stderr, `"user" given more than once`) {
		t.Errorf("stderr = %q, want it to name the stripped duplicate %q", res.Stderr, "user")
	}
}

// TestInsertFieldWithoutValueModeStillPromptsOnceAndStoresFields:
// --field with none of -m/--value-stdin/-e still prompts exactly once
// for `value` via the Prompter, and stores the given fields alongside
// it.
//
// stdin carries a decoy for the same reason as
// TestInsertDefaultPromptsForValueViaPrompter: an insert that quietly
// read stdin instead of prompting would store the decoy and be caught.
func TestInsertFieldWithoutValueModeStillPromptsOnceAndStoresFields(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{"prompted-value"}}
	res, _ := runCLIWithPrompter(t, []string{"insert", "Site", "--field", "user=bob"}, "stdin-decoy\n", false, p)
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if p.valueCalls != 1 {
		t.Fatalf("Prompter.Value was called %d times, want exactly 1", p.valueCalls)
	}

	e := catEntry(t, "Site")
	if e.Value != "prompted-value" {
		t.Errorf("value = %q, want %q", e.Value, "prompted-value")
	}
	if e.Fields["user"] != "bob" {
		t.Errorf("Fields[user] = %q, want %q (fields = %v)", e.Fields["user"], "bob", e.Fields)
	}
}
