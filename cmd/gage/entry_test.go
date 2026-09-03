package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

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
// this test) rather than reading stdin directly — stdin here is left
// empty, so a stdin-reading implementation would insert an empty value.
func TestInsertDefaultPromptsForValueViaPrompter(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{"prompted-value"}}
	res, _ := runCLIWithPrompter(t, []string{"insert", "Site"}, "", false, p)
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
		t.Errorf("value = %q, want the Prompter's answer %q", e.Value, "prompted-value")
	}
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
// unlock happens — asserted here by using a Prompter that fails the test
// if it's ever asked anything, and no vault even needs to be registered.
func TestInsertMutuallyExclusiveValueFlagsRejectedBeforeAnyIO(t *testing.T) {
	isolateXDG(t)

	p := &explodingPrompter{t: t}
	res, _ := runCLIWithPrompter(t, []string{"insert", "Site", "-m", "--value-stdin"}, "", false, p)
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
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
// checked through the real CLI: the commit message carries no title or
// other plaintext, only the entry's UUID.
func TestInsertCommitMessageIsUUIDOnly(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	res, _ := runCLIWithValue(t, []string{"insert", "ProtonMail — very secret"}, "v")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	message, authorName, authorEmail, err := gitrepo.HeadCommit(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(message, "ProtonMail") || strings.Contains(message, "insert") {
		t.Errorf("commit message leaks plaintext: %q", message)
	}
	// "gage: inserted %q (%s)" is what the CLI prints on success; the
	// commit message must be exactly that trailing UUID, not the whole
	// human-facing line.
	if strings.Contains(message, " ") || strings.Contains(message, "\"") {
		t.Errorf("commit message = %q, want exactly a bare UUID", message)
	}
	if authorName != "gage" || authorEmail != "gage@localhost" {
		t.Errorf("commit author = %q <%s>, want the fixed anonymous identity", authorName, authorEmail)
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

// TestLsListsInsertedTitleAlongsidePartialUUID.
func TestLsListsInsertedTitleAlongsidePartialUUID(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	ins, _ := runCLIWithValue(t, []string{"insert", "ProtonMail"}, "v")
	if ins.Code != 0 {
		t.Fatalf("insert failed: %s", ins.Stderr)
	}

	res := runCLI(t, []string{"ls"}, "")
	if res.Code != 0 {
		t.Fatalf("ls failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "ProtonMail") {
		t.Errorf("ls output missing the title: %q", res.Stdout)
	}
	// A partial id: some contiguous run of hex characters, shorter than a
	// full 36-character UUID, printed on the same line as the title.
	line := strings.TrimSpace(res.Stdout)
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("ls line %q doesn't look like \"title  id\"", line)
	}
	id := fields[len(fields)-1]
	if len(id) == 0 || len(id) >= 36 {
		t.Errorf("ls's id field = %q, want a short partial id", id)
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
