package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

// soleEntryID returns the UUID of the one entry file in a vault's
// entries/ directory, so a test can compare what the CLI printed or
// committed against the id that actually exists on disk.
func soleEntryID(t *testing.T, vaultPath string) uuid.UUID {
	t.Helper()
	des, err := os.ReadDir(filepath.Join(vaultPath, "entries"))
	if err != nil {
		t.Fatal(err)
	}
	if len(des) != 1 {
		t.Fatalf("entries/ holds %d files, want exactly 1", len(des))
	}
	id, err := uuid.Parse(strings.TrimSuffix(des[0].Name(), ".age"))
	if err != nil {
		t.Fatalf("entry filename %q is not a UUID: %v", des[0].Name(), err)
	}
	return id
}

// initEntryTestVault registers a fresh vault (via the real `gage init`
// CLI path) for entry-command tests and returns its on-disk path.
func initEntryTestVault(t *testing.T, name string) string {
	t.Helper()
	if res := runCLI(t, []string{"init", name, "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init %q failed: %s", name, res.Stderr)
	}
	return readGlobalConfigForTest(t).Vaults[name].Path
}

// runCLIWithValue runs the CLI with a fakePrompter that answers
// passphrase requests with testPassphrase and Value requests (gage
// insert's default masked prompt) with value.
func runCLIWithValue(t *testing.T, args []string, value string) (cliResult, *fakePrompter) {
	t.Helper()
	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{value}}
	res, _ := runCLIWithPrompter(t, args, "", false, p)
	return res, p
}

// TestInsertThenCatRoundTripsValueThroughTheCLI is the milestone's core
// round trip: a real `gage insert` (default masked-prompt value input)
// followed by a real `gage cat`, through the actual CLI rather than the
// library directly.
func TestInsertThenCatRoundTripsValueThroughTheCLI(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res, _ := runCLIWithValue(t, []string{"insert", "ProtonMail"}, "correcthorsebatterystaple")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	cat := runCLI(t, []string{"cat", "ProtonMail"}, "")
	if cat.Code != 0 {
		t.Fatalf("cat failed: %s", cat.Stderr)
	}
	e, err := gage.UnmarshalEntry([]byte(cat.Stdout))
	if err != nil {
		t.Fatalf("parsing cat output: %v\n%s", err, cat.Stdout)
	}
	if e.Value != "correcthorsebatterystaple" {
		t.Errorf("round-tripped value = %q, want %q", e.Value, "correcthorsebatterystaple")
	}
	if e.Title != "ProtonMail" {
		t.Errorf("round-tripped title = %q, want %q", e.Title, "ProtonMail")
	}
}

// TestInsertDefaultPromptsForValueViaPrompter: with none of
// -m/--value-stdin given, insert must ask through the Prompter (a fake in
// this test) rather than reading stdin directly.
//
// stdin carries a decoy rather than being empty: an implementation that
// quietly read stdin would store "stdin-decoy" and be caught, where
// against empty stdin it would store "" and could be mistaken for some
// unrelated failure.
func TestInsertDefaultPromptsForValueViaPrompter(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{"prompted-value"}}
	res, _ := runCLIWithPrompter(t, []string{"insert", "Site"}, "stdin-decoy\n", false, p)
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if p.valueCalls != 1 {
		t.Fatalf("Prompter.Value was called %d times, want exactly 1", p.valueCalls)
	}

	cat := runCLI(t, []string{"cat", "Site"}, "")
	if cat.Code != 0 {
		t.Fatalf("cat failed: %s", cat.Stderr)
	}
	e, err := gage.UnmarshalEntry([]byte(cat.Stdout))
	if err != nil {
		t.Fatal(err)
	}
	if e.Value != "prompted-value" {
		t.Errorf("value = %q, want the Prompter's answer %q (a stdin-reading insert would store the decoy)", e.Value, "prompted-value")
	}
}

// TestValueStdinAndPromptPathsStoreIdenticalEntries is the "round-trips
// through cat identically to the default prompt path" half of the
// --value-stdin bullet: the two input modes are different ways of
// getting the same bytes, so the stored entry they produce must not
// differ in anything but the fields that are legitimately per-entry.
func TestValueStdinAndPromptPathsStoreIdenticalEntries(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	const value = "same-secret"
	if res, _ := runCLIWithValue(t, []string{"insert", "ViaPrompt"}, value); res.Code != 0 {
		t.Fatalf("insert via prompt failed: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"insert", "ViaStdin", "--value-stdin"}, value+"\n"); res.Code != 0 {
		t.Fatalf("insert via --value-stdin failed: %s", res.Stderr)
	}

	viaPrompt := catEntry(t, "ViaPrompt")
	viaStdin := catEntry(t, "ViaStdin")

	if viaPrompt.Value != viaStdin.Value {
		t.Errorf("value differs between input modes: prompt=%q stdin=%q", viaPrompt.Value, viaStdin.Value)
	}
	if viaPrompt.Value != value {
		t.Errorf("value = %q, want %q", viaPrompt.Value, value)
	}
	// Everything else the two entries carry must agree too — only the
	// title (deliberately different here) and the timestamps may differ.
	if viaPrompt.Description != viaStdin.Description {
		t.Errorf("description differs: prompt=%q stdin=%q", viaPrompt.Description, viaStdin.Description)
	}
	if viaPrompt.UpdatedBy != viaStdin.UpdatedBy {
		t.Errorf("updated_by differs: prompt=%q stdin=%q", viaPrompt.UpdatedBy, viaStdin.UpdatedBy)
	}
	if len(viaPrompt.Fields) != 0 || len(viaStdin.Fields) != 0 {
		t.Errorf("fields should be empty for both: prompt=%v stdin=%v", viaPrompt.Fields, viaStdin.Fields)
	}
}

// catEntry runs `gage cat <query>` and parses the entry it prints.
func catEntry(t *testing.T, query string) gage.Entry {
	t.Helper()
	res := runCLI(t, []string{"cat", query}, "")
	if res.Code != 0 {
		t.Fatalf("cat %q failed: %s", query, res.Stderr)
	}
	e, err := gage.UnmarshalEntry([]byte(res.Stdout))
	if err != nil {
		t.Fatalf("parsing cat %q output: %v\n%s", query, err, res.Stdout)
	}
	return e
}

// TestInsertValueStdinTrimsOneTrailingNewlineAndRoundTrips covers
// --value-stdin's exact trimming rule and that it round-trips identically
// to the default prompt path.
func TestInsertValueStdinTrimsOneTrailingNewlineAndRoundTrips(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"insert", "Site", "--value-stdin"}, "stdin-value\n")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	cat := runCLI(t, []string{"cat", "Site"}, "")
	if cat.Code != 0 {
		t.Fatalf("cat failed: %s", cat.Stderr)
	}
	e, err := gage.UnmarshalEntry([]byte(cat.Stdout))
	if err != nil {
		t.Fatal(err)
	}
	if e.Value != "stdin-value" {
		t.Errorf("value = %q, want %q (exactly one trailing newline trimmed)", e.Value, "stdin-value")
	}
}

// TestInsertValueStdinTrimsExactlyOneNewline: a value that itself ends in
// a blank line must keep it — only one trailing newline is ever trimmed.
func TestInsertValueStdinTrimsExactlyOneNewline(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"insert", "Site", "--value-stdin"}, "line1\n\n")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	cat := runCLI(t, []string{"cat", "Site"}, "")
	e, err := gage.UnmarshalEntry([]byte(cat.Stdout))
	if err != nil {
		t.Fatal(err)
	}
	if e.Value != "line1\n" {
		t.Errorf("value = %q, want %q (exactly one trailing newline trimmed)", e.Value, "line1\n")
	}
}

// TestInsertMutuallyExclusiveValueFlagsRejectedBeforeAnyIO: passing both
// -m and --value-stdin is a usage error before any prompt, read, or
// unlock happens.
//
// A vault is registered first on purpose. Without one, "no current vault
// is set" is *also* a Usage rejection reached before any prompt, so the
// test would pass just as well with the mutual-exclusion check deleted
// outright — it would be asserting the wrong refusal. With a working
// vault registered, the flag conflict is the only thing left that can
// refuse. The error message is checked for the same reason, and stdin
// carries a payload no correct implementation may consume: explodingIn
// fails the test if the command reads it, which is what "before any
// read" means beyond "before any prompt".
func TestInsertMutuallyExclusiveValueFlagsRejectedBeforeAnyIO(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &explodingPrompter{t: t}
	in := &explodingReader{t: t}
	res, _ := runCLIWithPrompterAndStdin(t, []string{"insert", "Site", "-m", "--value-stdin"}, in, false, p)
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if !strings.Contains(res.Stderr, "mutually exclusive") {
		t.Errorf("stderr = %q, want it to explain the flags are mutually exclusive", res.Stderr)
	}

	// Sanity check on the guard above: the same vault, same prompter, with
	// only one of the two flags must get past the rejection and reach the
	// prompter — otherwise "rejected" above could mean anything.
	ok, _ := runCLIWithValue(t, []string{"insert", "Site"}, "v")
	if ok.Code != 0 {
		t.Fatalf("a single value-input mode should be accepted, but insert exited %d: %s", ok.Code, ok.Stderr)
	}
}

// explodingReader fails the test if anything reads from it — used to
// prove a rejection happens before stdin is consumed.
type explodingReader struct{ t *testing.T }

func (r *explodingReader) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Error("stdin was read; expected the command to fail before reading any input")
	return 0, io.EOF
}

// explodingPrompter fails the test the moment anything asks it for
// anything — used to prove a code path never reaches the point of
// prompting, unlocking, or reading.
type explodingPrompter struct{ t *testing.T }

func (p *explodingPrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	p.t.Helper()
	p.t.Fatal("Unlock was called; expected the command to fail before ever unlocking")
	return gage.UnlockResponse{}, nil
}
func (p *explodingPrompter) Confirm(prompt string) (bool, error) { return true, nil }
func (p *explodingPrompter) Choose(list gage.CandidateList) (string, error) {
	return "", nil
}
func (p *explodingPrompter) Warn(msg string) {}
func (p *explodingPrompter) Value(prompt string) (string, error) {
	p.t.Helper()
	p.t.Fatal("Value was called; expected the command to fail before ever prompting")
	return "", nil
}

// TestInsertDescriptionStoredAndShownByCat.
func TestInsertDescriptionStoredAndShownByCat(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res, _ := runCLIWithValue(t, []string{"insert", "GitHub", "--description", "personal account"}, "v")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	cat := runCLI(t, []string{"cat", "GitHub"}, "")
	if cat.Code != 0 {
		t.Fatalf("cat failed: %s", cat.Stderr)
	}
	if !strings.Contains(cat.Stdout, "description: personal account") {
		t.Errorf("cat output missing the description: %q", cat.Stdout)
	}
}

// TestInsertProducesExactlyOneCommit.
func TestInsertProducesExactlyOneCommit(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	before, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}

	res, _ := runCLIWithValue(t, []string{"insert", "Site"}, "v")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	after, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("commit count = %d, want %d (exactly one new commit)", after, before+1)
	}
}

// TestInsertCommitMessageIsUUIDOnly is the confidentiality decision,
// checked through the real CLI.
//
// The assertion is equality against the id that actually exists under
// entries/, not merely "contains no title": checking only for absent
// plaintext would pass for a message of "x", or an empty one, which is
// not what the decision says. The absence checks stay as the direct
// statement of the confidentiality property.
func TestInsertCommitMessageIsUUIDOnly(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	const title = "ProtonMail — very secret"
	res, _ := runCLIWithValue(t, []string{"insert", title, "--description", "leaky description"}, "v")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	want := soleEntryID(t, path)

	message, authorName, authorEmail, err := gitrepo.HeadCommit(path)
	if err != nil {
		t.Fatal(err)
	}
	if message != want.String() {
		t.Errorf("commit message = %q, want exactly the entry's UUID %q", message, want)
	}
	for _, leak := range []string{title, "ProtonMail", "leaky description", "insert", "rm"} {
		if strings.Contains(message, leak) {
			t.Errorf("commit message %q leaks %q", message, leak)
		}
	}
	if authorName != "gage" || authorEmail != "gage@localhost" {
		t.Errorf("commit author = %q <%s>, want the fixed anonymous identity", authorName, authorEmail)
	}
}

// TestCommitAuthorIgnoresTheUsersGitConfig is the other half of the
// author decision: "never the user's git config, regardless of who runs
// the command." The vault's own repo-local config names a user, which is
// what go-git would otherwise pick up — the commit must still be
// attributed to the fixed anonymous identity, for rm as well as insert.
func TestCommitAuthorIgnoresTheUsersGitConfig(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	cfgPath := filepath.Join(path, ".git", "config")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("[user]\n\tname = Real Human\n\temail = human@example.invalid\n")...)
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	ins, _ := runCLIWithValue(t, []string{"insert", "Site"}, "v")
	if ins.Code != 0 {
		t.Fatalf("insert failed: %s", ins.Stderr)
	}
	assertAnonymousAuthor(t, path, "after insert")

	rm := runCLI(t, []string{"rm", "Site"}, "")
	if rm.Code != 0 {
		t.Fatalf("rm failed: %s", rm.Stderr)
	}
	assertAnonymousAuthor(t, path, "after rm")
}

func assertAnonymousAuthor(t *testing.T, vaultPath, when string) {
	t.Helper()
	_, name, email, err := gitrepo.HeadCommit(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if name != "gage" || email != "gage@localhost" {
		t.Errorf("commit author %s = %q <%s>, want the fixed \"gage\" <gage@localhost> identity", when, name, email)
	}
}

// TestInsertSetsTimestampsAndUpdatedBy.
func TestInsertSetsTimestampsAndUpdatedBy(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	device := readGlobalConfigForTest(t).Vaults["personal"].Device

	res, _ := runCLIWithValue(t, []string{"insert", "Site"}, "v")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	cat := runCLI(t, []string{"cat", "Site"}, "")
	e, err := gage.UnmarshalEntry([]byte(cat.Stdout))
	if err != nil {
		t.Fatal(err)
	}
	if e.Created.IsZero() || e.Updated.IsZero() {
		t.Errorf("created/updated not set: created=%v updated=%v", e.Created, e.Updated)
	}
	if e.UpdatedBy != device {
		t.Errorf("updated_by = %q, want this device's recorded name %q", e.UpdatedBy, device)
	}
}

// TestLsListsInsertedTitleAlongsidePartialUUID covers the ls-output
// decision: title first, then a partial UUID.
//
// The id is checked against the entry's real UUID on disk — a prefix of
// it, and shorter than the whole thing. Checking only "some short token
// is printed" would pass for a hardcoded string, which would make the id
// useless for the very thing the decision wants it for (addressing an
// entry straight from ls output).
func TestLsListsInsertedTitleAlongsidePartialUUID(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	ins, _ := runCLIWithValue(t, []string{"insert", "ProtonMail"}, "v")
	if ins.Code != 0 {
		t.Fatalf("insert failed: %s", ins.Stderr)
	}
	full := soleEntryID(t, path).String()

	res := runCLI(t, []string{"ls"}, "")
	if res.Code != 0 {
		t.Fatalf("ls failed: %s", res.Stderr)
	}

	line := strings.TrimSpace(res.Stdout)
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("ls line %q doesn't look like \"title  id\"", line)
	}
	// Title first, per the decision.
	if fields[0] != "ProtonMail" {
		t.Errorf("ls line %q doesn't lead with the title", line)
	}
	id := fields[len(fields)-1]
	if !strings.HasPrefix(full, id) {
		t.Errorf("ls printed id %q, which is not a prefix of the entry's UUID %q", id, full)
	}
	if len(id) >= len(full) {
		t.Errorf("ls printed id %q (%d chars), want a partial one shorter than the full UUID (%d chars)", id, len(id), len(full))
	}
	if id == "" {
		t.Error("ls printed an empty id")
	}
}

// TestLsEmptyVaultNoOutput: ls on a vault with no entries succeeds with
// no output and exit 0 — not an error.
func TestLsEmptyVaultNoOutput(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"ls"}, "")
	if res.Code != 0 {
		t.Fatalf("ls on an empty vault failed: %s", res.Stderr)
	}
	if res.Stdout != "" {
		t.Errorf("ls on an empty vault produced output: %q", res.Stdout)
	}
}

// TestRmDeletesFileAndCommitsThenCatAndLsNoLongerShowIt.
func TestRmDeletesFileAndCommitsThenCatAndLsNoLongerShowIt(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	ins, _ := runCLIWithValue(t, []string{"insert", "ProtonMail"}, "v")
	if ins.Code != 0 {
		t.Fatalf("insert failed: %s", ins.Stderr)
	}
	before, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}

	rm := runCLI(t, []string{"rm", "ProtonMail"}, "")
	if rm.Code != 0 {
		t.Fatalf("rm failed: %s", rm.Stderr)
	}

	entries, err := os.ReadDir(filepath.Join(path, "entries"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("entries/ still has %d file(s) after rm, want 0: %v", len(entries), entries)
	}

	after, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("commit count = %d, want %d (exactly one new commit for the deletion)", after, before+1)
	}

	cat := runCLI(t, []string{"cat", "ProtonMail"}, "")
	if cat.Code != int(exitcode.NotFound) {
		t.Errorf("cat after rm: exit code = %d, want %d (NotFound)", cat.Code, exitcode.NotFound)
	}

	ls := runCLI(t, []string{"ls"}, "")
	if ls.Code != 0 {
		t.Fatalf("ls failed: %s", ls.Stderr)
	}
	if strings.Contains(ls.Stdout, "ProtonMail") {
		t.Errorf("ls still shows the removed entry: %q", ls.Stdout)
	}
}

// TestCatUnknownFailsWithNotFound covers both addressing modes.
func TestCatUnknownFailsWithNotFound(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	for _, query := range []string{"no such title", "00000000-0000-4000-8000-000000000000"} {
		t.Run(query, func(t *testing.T) {
			res := runCLI(t, []string{"cat", query}, "")
			if res.Code != int(exitcode.NotFound) {
				t.Errorf("exit code = %d, want %d (NotFound)", res.Code, exitcode.NotFound)
			}
			if res.Stderr == "" {
				t.Error("expected a clear error message on stderr")
			}
		})
	}
}

// TestInsertDuplicateTitleRejectedUnlessForced.
func TestInsertDuplicateTitleRejectedUnlessForced(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	first, _ := runCLIWithValue(t, []string{"insert", "Site"}, "v1")
	if first.Code != 0 {
		t.Fatalf("first insert failed: %s", first.Stderr)
	}

	dup, _ := runCLIWithValue(t, []string{"insert", "Site"}, "v2")
	if dup.Code != int(exitcode.Conflict) {
		t.Errorf("duplicate insert without --force: exit code = %d, want %d (Conflict)", dup.Code, exitcode.Conflict)
	}

	forced, _ := runCLIWithValue(t, []string{"insert", "Site", "--force"}, "v2")
	if forced.Code != 0 {
		t.Fatalf("forced duplicate insert failed: %s", forced.Stderr)
	}

	ls := runCLI(t, []string{"ls"}, "")
	if n := strings.Count(ls.Stdout, "Site"); n != 2 {
		t.Errorf("ls shows %d \"Site\" entries after a forced duplicate, want 2:\n%s", n, ls.Stdout)
	}
}

// TestUseFlagTargetsNamedVaultNotCurrent: with two vaults registered,
// --use <other> on insert/cat operates on that vault specifically, never
// the current one.
func TestUseFlagTargetsNamedVaultNotCurrent(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal") // becomes current
	initEntryTestVault(t, "work")

	ins, _ := runCLIWithValue(t, []string{"insert", "WorkSecret", "--use", "work"}, "work-value")
	if ins.Code != 0 {
		t.Fatalf("insert --use work failed: %s", ins.Stderr)
	}

	// Not visible in the current ("personal") vault.
	lsCurrent := runCLI(t, []string{"ls"}, "")
	if strings.Contains(lsCurrent.Stdout, "WorkSecret") {
		t.Errorf("entry inserted with --use work leaked into the current vault's ls: %q", lsCurrent.Stdout)
	}

	lsWork := runCLI(t, []string{"ls", "--use", "work"}, "")
	if !strings.Contains(lsWork.Stdout, "WorkSecret") {
		t.Errorf("ls --use work doesn't show the entry: %q", lsWork.Stdout)
	}

	cat := runCLI(t, []string{"cat", "WorkSecret", "--use", "work"}, "")
	if cat.Code != 0 {
		t.Fatalf("cat --use work failed: %s", cat.Stderr)
	}
	e, err := gage.UnmarshalEntry([]byte(cat.Stdout))
	if err != nil {
		t.Fatal(err)
	}
	if e.Value != "work-value" {
		t.Errorf("value from --use work = %q, want %q", e.Value, "work-value")
	}
}

// TestNoUseFlagOperatesAgainstCurrentVault is the mirror image: with two
// vaults registered and no --use, an entry command targets `current` —
// the one-shot counterpart to `vault set-default`.
func TestNoUseFlagOperatesAgainstCurrentVault(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal") // becomes current
	initEntryTestVault(t, "work")

	res := runCLI(t, []string{"vault", "set-default", "work"}, "")
	if res.Code != 0 {
		t.Fatalf("vault set-default failed: %s", res.Stderr)
	}

	ins, _ := runCLIWithValue(t, []string{"insert", "DefaultsToWork"}, "v")
	if ins.Code != 0 {
		t.Fatalf("insert with no --use failed: %s", ins.Stderr)
	}

	lsWork := runCLI(t, []string{"ls", "--use", "work"}, "")
	if !strings.Contains(lsWork.Stdout, "DefaultsToWork") {
		t.Errorf("entry inserted with no --use didn't land in the current vault (work): %q", lsWork.Stdout)
	}
	lsPersonal := runCLI(t, []string{"ls", "--use", "personal"}, "")
	if strings.Contains(lsPersonal.Stdout, "DefaultsToWork") {
		t.Errorf("entry inserted with no --use leaked into the non-current vault (personal): %q", lsPersonal.Stdout)
	}
}

// TestUseUnregisteredVaultFailsBeforePromptingForAPassphrase.
func TestUseUnregisteredVaultFailsBeforePromptingForAPassphrase(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &explodingPrompter{t: t}
	for _, args := range [][]string{
		{"insert", "Site", "--use", "nonexistent"},
		{"cat", "Site", "--use", "nonexistent"},
		{"ls", "--use", "nonexistent"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			res, _ := runCLIWithPrompter(t, args, "", false, p)
			if res.Code != int(exitcode.NotFound) {
				t.Errorf("exit code = %d, want %d (NotFound)", res.Code, exitcode.NotFound)
			}
			if !strings.Contains(res.Stderr, "nonexistent") {
				t.Errorf("stderr = %q, want it to name the unregistered vault", res.Stderr)
			}
		})
	}
}

// TestOneShotHandlerClosesIdentityOnEveryExitPathIncludingError proves
// withUnlockedVault's Close guarantee directly: fn's own error return
// still results in a Closed Identity, and so does the success path.
func TestOneShotHandlerClosesIdentityOnEveryExitPathIncludingError(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	app := &App{
		Out:      &bytes.Buffer{},
		Err:      &bytes.Buffer{},
		In:       strings.NewReader(""),
		Prompter: &fakePrompter{passphrases: []string{testPassphrase}},
	}

	boom := errors.New("boom")
	var captured *gage.Identity
	err := withUnlockedVault(app, "personal", func(v *gage.Vault, ident *gage.Identity) error {
		captured = ident
		if captured.Closed() {
			t.Fatal("Identity is already Closed before fn even returned")
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want it to wrap the sentinel returned by fn", err)
	}
	if captured == nil || !captured.Closed() {
		t.Error("Identity was not Closed after fn returned an error")
	}

	err = withUnlockedVault(app, "personal", func(v *gage.Vault, ident *gage.Identity) error {
		captured = ident
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error on the success path: %v", err)
	}
	if !captured.Closed() {
		t.Error("Identity was not Closed on the success path")
	}
}

// TestConcurrentInsertsThroughTheCLIBothSucceed is the CLI-level face of
// the vault lock: two `gage insert` invocations against the same vault at
// once must both land, not interleave or lose one.
func TestConcurrentInsertsThroughTheCLIBothSucceed(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	before, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}

	titles := []string{"Entry A", "Entry B"}
	var wg sync.WaitGroup
	codes := make([]int, len(titles))
	for i, title := range titles {
		wg.Add(1)
		go func(i int, title string) {
			defer wg.Done()
			res, _ := runCLIWithValue(t, []string{"insert", title}, "v")
			codes[i] = res.Code
		}(i, title)
	}
	wg.Wait()

	for i, code := range codes {
		if code != 0 {
			t.Errorf("concurrent insert %d exited %d, want 0", i, code)
		}
	}

	after, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+len(titles) {
		t.Errorf("commit count = %d, want %d — a concurrent write was lost", after, before+len(titles))
	}

	ls := runCLI(t, []string{"ls"}, "")
	for _, title := range titles {
		if !strings.Contains(ls.Stdout, title) {
			t.Errorf("ls is missing %q after concurrent inserts: %q", title, ls.Stdout)
		}
	}
}

// TestConcurrentReadsDoNotBlockEachOther: two read-only commands against
// the same vault at once must both complete promptly — reads never take
// the write lock, so there's nothing for them to contend on.
func TestConcurrentReadsDoNotBlockEachOther(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	ins, _ := runCLIWithValue(t, []string{"insert", "Site"}, "v")
	if ins.Code != 0 {
		t.Fatalf("insert failed: %s", ins.Stderr)
	}

	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := runCLI(t, []string{"ls"}, "")
			codes[i] = res.Code
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent reads did not all complete promptly")
	}
	for i, code := range codes {
		if code != 0 {
			t.Errorf("concurrent read %d exited %d, want 0", i, code)
		}
	}
}
