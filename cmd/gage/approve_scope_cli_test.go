package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/recipients"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
)

// ---------------------------------------------------------------------
// What the command passes to the library, and what the library's
// refusals look like once a command has surfaced them.
// ---------------------------------------------------------------------

// TestApprovalPromptsOnceForTheApproversPassphrase. Approval needs the
// approver's own identity — a git-writer holding no key of this vault
// cannot approve an enrollment — and it needs it exactly once.
func TestApprovalPromptsOnceForTheApproversPassphrase(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve", "--code", code}, "", false, p)
	if res.Code != 0 {
		t.Fatalf("recipient approve: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if len(p.requests) != 1 {
		t.Fatalf("the approver was asked for a passphrase %d times, want exactly 1", len(p.requests))
	}
}

// TestTheFullAccessRefusalCostsOneUnlockNotZero is the documented cost
// of the late unlock, pinned so it is not later "fixed" into an ordering
// that gives the late unlock away.
//
// The pre-flight decrypts every entry, so it needs the unlocked
// identity; an age file's X25519 stanzas carry an ephemeral share rather
// than a recipient key, so there is no cheaper form of the question. A
// partially-admitted approver therefore types their passphrase before
// learning they cannot grant what they were asked to grant. A test
// asserting zero prompts here would be asserting the impossible.
func TestTheFullAccessRefusalCostsOneUnlockNotZero(t *testing.T) {
	_, owner, _, code := enrolledDevice(t, "personal", "phone-1")
	_ = owner

	// A partially-admitted device: listed as a recipient, but no entry
	// was ever re-encrypted to it. A19 left no supported way to produce
	// this, so it is produced the way the design says it still can be —
	// by editing the recipient files directly.
	partial := partiallyAdmittedDevice(t, "personal", "desktop-1")

	restoreXDGRoot(t, partial)
	p := &fakePrompter{passphrases: []string{testPassphrase}}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve", "--code", code}, "", false, p)

	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "cannot read every entry") {
		t.Errorf("the refusal does not name the real problem:\n%s", res.Stderr)
	}
	if len(p.requests) != 1 {
		t.Fatalf("the passphrase was asked for %d times, want exactly 1 — the pre-flight is a "+
			"decryption pass and has no identity-free form", len(p.requests))
	}
}

// partiallyAdmittedDevice creates an identity on a fresh machine, lists
// its key in both of the owner's recipient files by hand without
// re-encrypting anything, and returns that machine's roots.
//
// This is the hole the design acknowledges it cannot close — anyone with
// git write access can edit `.age-recipients` — and it is the only way
// left to reach the partial-access damage ErrCannotGrantFullAccess
// exists to surface, now that `recipient add` always re-encrypts.
func partiallyAdmittedDevice(t *testing.T, vault, device string) xdgRoot {
	t.Helper()

	owner := currentXDGRoot(t)
	vaultPath := vaultPathForTest(t, vault)

	isolateXDG(t)
	if res := runCLI(t, []string{"clone", remoteOf(t, vaultPath), "--name", vault}, ""); res.Code != 0 {
		t.Fatalf("cloning as %q: %s", device, res.Stderr)
	}
	add := runCLI(t, []string{"identity", "add", "--device", device}, "")
	if add.Code != 0 {
		t.Fatalf("creating %q's identity: %s", device, add.Stderr)
	}
	pubkey := agePublicKeyIn(t, add.Stdout)
	partial := currentXDGRoot(t)

	// Back on the owner's machine, hand-list the key in both files.
	restoreXDGRoot(t, owner)
	configPath := filepath.Join(vaultPath, ".gage", "config.toml")
	cfg, err := vaultconfig.Read(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Recipients = append(cfg.Recipients, vaultconfig.Recipient{Device: device, Pubkey: pubkey})
	if err := vaultconfig.Write(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(cfg.Recipients))
	for _, r := range cfg.Recipients {
		keys = append(keys, r.Pubkey)
	}
	if err := recipients.Write(filepath.Join(vaultPath, ".age-recipients"), keys); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(vaultPath, "test: a recipient listed without re-encrypting"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Push(t.Context(), vaultPath); err != nil {
		t.Fatal(err)
	}

	// And catch the partial device up to it.
	restoreXDGRoot(t, partial)
	if res := runCLI(t, []string{"pull"}, ""); res.Code != 0 {
		t.Fatalf("catching %q up: %s", device, res.Stderr)
	}
	restoreXDGRoot(t, owner)
	return partial
}

func remoteOf(t *testing.T, vaultPath string) string {
	t.Helper()
	url, err := gitrepo.RemoteURL(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	return url
}

// agePublicKeyIn pulls the age public key out of `identity add`'s output.
func agePublicKeyIn(t *testing.T, out string) string {
	t.Helper()
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "age1") {
			return f
		}
	}
	t.Fatalf("no public key in %q", out)
	return ""
}

// TestApproveInASessionReusesTheCachedIdentity: the vault is already
// unlocked, so approval prompts for no passphrase of its own.
func TestApproveInASessionReusesTheCachedIdentity(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"recipient approve --code "+code,
		"y",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	// A session renders its prompts to stdout, which is where the
	// transcript a human sees lives.
	if got := countPassphrasePrompts(res.Stdout); got != 1 {
		t.Fatalf("the session asked for a passphrase %d times, want 1 — approval must reuse "+
			"the Identity the session already holds\nstdout=%s", got, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "phone-1") {
		t.Errorf("the approval did not happen in-session:\n%s", res.Stdout)
	}
}

// stuffPending lays n live-looking requests into a vault's pending/ and
// commits them — the directory any git writer can fill, which is what
// D-ENROLL-SEAL-COST's bound is a bound on.
//
// The contents are never opened: the bound is applied against the count
// before any decryption, which is the property being tested.
func stuffPending(t *testing.T, vault string, n int) {
	t.Helper()

	vaultPath := vaultPathForTest(t, vault)
	dir := filepath.Join(vaultPath, ".gage", "pending")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(2 * time.Hour).Unix()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s-%d.age", uuid.NewString(), expires)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not a seal anyone will open"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := gitrepo.CommitAll(vaultPath, "test: a stuffed pending directory"); err != nil {
		t.Fatal(err)
	}
}

// TestApproveWithNoIDAgainstAStuffedPendingRefuses. The bound is on
// work, not on how many devices may enrol, and it is recoverable in
// place by naming an ID.
func TestApproveWithNoIDAgainstAStuffedPendingRefuses(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")
	stuffPending(t, "personal", 40)
	vaultPath := vaultPathForTest(t, "personal")

	before, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	p := &decliningApprovalPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve", "--code", code}, "", false, p)

	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "recipient approve <ID>") {
		t.Errorf("the refusal does not name the way out:\n%s", res.Stderr)
	}
	if len(p.confirms) != 0 || len(p.requests) != 0 || len(p.valuePrompts) != 0 {
		t.Errorf("the refusal still prompted: %d confirms, %d passphrases, %d values",
			len(p.confirms), len(p.requests), len(p.valuePrompts))
	}
	after, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Error("the refusal still committed something")
	}
}

// TestNamingAnIDIsTheWayOutOfAStuffedPending, and deny and pending are
// unaffected by it — neither opens anything, so a stuffed directory
// stays inspectable and cleanable. This is the pair that makes the
// refusal a detour rather than a dead end.
func TestNamingAnIDIsTheWayOutOfAStuffedPending(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")
	id := firstPendingID(t)
	stuffPending(t, "personal", 40)

	if res := runCLI(t, []string{"recipient", "pending"}, ""); res.Code != 0 {
		t.Fatalf("recipient pending against a stuffed directory: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if res := runCLI(t, []string{"recipient", "deny", nonMatchingID(t, id)}, ""); res.Code != 0 {
		t.Fatalf("recipient deny against a stuffed directory: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	res := runCLI(t, []string{"recipient", "approve", id, "--code", code}, "")
	if res.Code != 0 {
		t.Fatalf("recipient approve <ID> against a stuffed directory: exit %d, stderr=%s", res.Code, res.Stderr)
	}
}

// nonMatchingID returns some pending request's id other than keep.
func nonMatchingID(t *testing.T, keep string) string {
	t.Helper()
	for _, id := range pendingIDs(t) {
		if id != keep {
			return id
		}
	}
	t.Fatal("no other pending request to deny")
	return ""
}

// TestTheBoundFollowsTheScopeNotTheFlag is the property that makes the
// escape hatch safe to have: naming IDs does not switch the bound off,
// it narrows what is being asked about. Name enough of them and the same
// refusal comes back.
//
// A cmd/gage that reached for a "skip the bound" parameter instead would
// pass both tests above and lose exactly this.
func TestTheBoundFollowsTheScopeNotTheFlag(t *testing.T) {
	enrolledDevice(t, "personal", "phone-1")
	stuffPending(t, "personal", 40)

	args := append([]string{"recipient", "approve"}, pendingIDs(t)...)
	args = append(args, "--code", "GAGE-0000-0000-0000-0000")
	res := runCLI(t, args, "")

	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict) — the bound is a property of the scope, "+
			"not of whether an ID was typed; stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
}

// TestApproveOnAnAmbiguousIDRendersTheCandidates. approve and deny
// resolve through one function, so the behavior is identical.
func TestApproveOnAnAmbiguousIDRendersTheCandidates(t *testing.T) {
	remote, _, _, code := enrolledDevice(t, "personal", "phone-1")
	enrollAnotherDevice(t, remote, "personal", "tablet-1")

	ids := pendingIDs(t)
	res := runCLI(t, []string{"recipient", "approve", commonHexDigit(t, ids), "--code", code}, "")
	if res.Code != int(exitcode.Ambiguous) {
		t.Fatalf("exit code = %d, want %d (Ambiguous); stderr=%s", res.Code, exitcode.Ambiguous, res.Stderr)
	}
	for _, id := range ids {
		if !strings.Contains(res.Stderr, id[:8]) {
			t.Errorf("the candidate list does not name %s:\n%s", id[:8], res.Stderr)
		}
	}
}

// TestApproveSurfacesARequestExpiredByItsSeal.
//
// Built the way E2 proves the seal is authoritative: a request whose
// lifetime has really run out, renamed to claim a live one. The
// filename's epoch is an unauthenticated hint, so the request lists and
// is tried — and then the sealed copy refuses it. A request expired in
// both places would simply be filtered out of the listing and never
// reach this check at all.
func TestApproveSurfacesARequestExpiredByItsSeal(t *testing.T) {
	_, owner, _, _ := enrolledDevice(t, "personal", "phone-1")
	_ = owner

	// A second request with a lifetime short enough to wait out.
	code := enrollAnotherDeviceWithTTL(t, "personal", "tablet-1", "1s")
	time.Sleep(1500 * time.Millisecond)
	renamePendingToLiveEpochs(t, "personal")

	p := &decliningApprovalPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve", "--code", code}, "", false, p)

	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "expired") {
		t.Errorf("the refusal does not say the request expired:\n%s", res.Stderr)
	}
	if len(p.requests) != 0 {
		t.Errorf("an expired request still cost %d passphrase prompts, want 0", len(p.requests))
	}
	if len(p.confirms) != 0 {
		t.Errorf("an expired request still cost %d confirmations, want 0", len(p.confirms))
	}
}

// enrollAnotherDeviceWithTTL is enrollAnotherDevice with an explicit
// --ttl, for the one case that has to outlive a request's lifetime
// inside a test.
func enrollAnotherDeviceWithTTL(t *testing.T, name, device, ttl string) string {
	t.Helper()

	here := currentXDGRoot(t)
	remote := remoteOf(t, vaultPathForTest(t, name))
	isolateXDG(t)
	if res := runCLI(t, []string{"clone", remote, "--name", name}, ""); res.Code != 0 {
		t.Fatalf("cloning as %q: %s", device, res.Stderr)
	}
	res := runCLI(t, []string{"identity", "enroll", "--device", device, "--ttl", ttl}, "")
	if res.Code != 0 {
		t.Fatalf("enrolling %q: %s", device, res.Stderr)
	}
	code := enrollmentCodeIn(t, res.Stdout)

	restoreXDGRoot(t, here)
	if res := runCLI(t, []string{"pull"}, ""); res.Code != 0 {
		t.Fatalf("catching up to %q's request: %s", device, res.Stderr)
	}
	return code
}

// renamePendingToLiveEpochs gives every pending request a filename
// claiming a live expiry, whatever its seal says. The filename is an
// unauthenticated hint; this is what makes that concrete.
func renamePendingToLiveEpochs(t *testing.T, vault string) {
	t.Helper()

	vaultPath := vaultPathForTest(t, vault)
	dir := filepath.Join(vaultPath, ".gage", "pending")
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		id, _, ok := parsePublishedName(de.Name())
		if !ok {
			continue
		}
		fresh := fmt.Sprintf("%s-%d.age", id, time.Now().Add(2*time.Hour).Unix())
		if fresh == de.Name() {
			continue
		}
		if err := os.Rename(filepath.Join(dir, de.Name()), filepath.Join(dir, fresh)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := gitrepo.CommitAll(vaultPath, "test: live-looking names over the seals as they are"); err != nil {
		t.Fatal(err)
	}
}

// TestPendingRendersExpiryInTheLocalTimezone: the same request renders
// differently under two zones while naming the same instant.
func TestPendingRendersExpiryInTheLocalTimezone(t *testing.T) {
	expires := time.Date(2026, 9, 8, 10, 12, 0, 0, time.UTC)
	pending := []gage.PendingRequest{{ID: "7c1e4a90-0000-4000-8000-000000000000", Expires: expires}}

	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skipf("no timezone database on this platform: %v", err)
	}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no timezone database on this platform: %v", err)
	}

	east := renderPendingIn(t, tokyo, pending)
	west := renderPendingIn(t, newYork, pending)
	if east == west {
		t.Fatalf("the same instant rendered identically in two zones:\n%s", east)
	}
	if !strings.Contains(east, "19:12") {
		t.Errorf("Tokyo rendering = %q, want the local wall clock", east)
	}
	if !strings.Contains(west, "06:12") {
		t.Errorf("New York rendering = %q, want the local wall clock", west)
	}
}

// renderPendingIn renders the listing with time.Local pointed at loc,
// which is what a different TZ actually changes for a running process.
func renderPendingIn(t *testing.T, loc *time.Location, pending []gage.PendingRequest) string {
	t.Helper()
	saved := time.Local
	time.Local = loc
	defer func() { time.Local = saved }()
	return strings.Join(pendingLines("personal", pending), "\n")
}

// TestApproveUnderARealScriptRunFailsCleanly.
//
// `--script` supplies commands, not answers. --yes answers only M10's
// recipient-change question, so approve's own [y/N] stays a real one and
// defaults to no — which is the behavior a scripted run has to have,
// since letting a device into a vault is not something a script that was
// never asked should be able to do by omission.
func TestApproveUnderARealScriptRunFailsCleanly(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")
	vaultPath := vaultPathForTest(t, "personal")
	t.Setenv(envPassphraseVar, testPassphrase)

	before, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	res := runRealScript(t, "use personal", "recipient approve --code "+code)
	if res.Code == 0 {
		t.Fatalf("a scripted run approved the request:\n%s", res.Stdout)
	}

	after, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Error("a scripted approval still committed something")
	}
	if !strings.Contains(res.Stderr, "nothing was approved") {
		t.Errorf("the refusal does not say what happened:\n%s", res.Stderr)
	}
	list := runCLI(t, []string{"recipient", "list"}, "")
	if strings.Contains(list.Stdout, "phone-1") {
		t.Error("a scripted run let the device in")
	}
}

// TestPendingAndDenyUnderARealScriptRun — neither asks anything a script
// cannot answer, so both work unchanged.
func TestPendingAndDenyUnderARealScriptRun(t *testing.T) {
	enrolledDevice(t, "personal", "phone-1")
	id := firstPendingID(t)
	t.Setenv(envPassphraseVar, testPassphrase)

	res := runRealScript(t, "use personal", "recipient pending", "recipient deny "+id)
	if res.Code != 0 {
		t.Fatalf("scripted pending/deny: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "removed pending request") {
		t.Errorf("the scripted deny did not run:\n%s", res.Stdout)
	}
	if len(pendingIDs(t)) != 0 {
		t.Error("the scripted deny left the request in place")
	}
}

// TestTheApproverAnswersTwoDistinctQuestions when another device changed
// the recipient list first.
//
// They are genuinely different questions — "should this device be let
// in" versus "do you trust the list you are about to encrypt to" — and
// collapsing them would hide the second behind the first.
func TestTheApproverAnswersTwoDistinctQuestions(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")

	// Somebody else adds a recipient, so M10's question is a real one.
	partiallyAdmittedDevice(t, "personal", "desktop-1")

	p := &countingApprovalPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve", "--code", code}, "", false, p)
	_ = res

	if len(p.confirms) != 1 {
		t.Fatalf("approve's own question was asked %d times, want 1", len(p.confirms))
	}
	if len(p.recipientChanges) != 1 {
		t.Fatalf("M10's question was asked %d times, want 1 — the two must not be collapsed",
			len(p.recipientChanges))
	}
	if strings.Contains(p.confirms[0], "trust") {
		t.Errorf("approve's own question reads like M10's: %q", p.confirms[0])
	}
}

// countingApprovalPrompter answers both questions yes and records each
// separately, so a test can tell which was asked.
type countingApprovalPrompter struct {
	fakePrompter
	confirms []string
}

func (p *countingApprovalPrompter) Confirm(prompt string) (bool, error) {
	p.confirms = append(p.confirms, prompt)
	return true, nil
}

// runRealScript is runScript with the *real* terminal prompter over the
// run's own (empty, non-terminal) stdin, rather than a fake that answers
// everything.
//
// The distinction is the whole point here: a fake Prompter that confirms
// unconditionally would report that a scripted approval succeeds, which
// is precisely the behavior these tests exist to rule out. Passphrases
// still come from GAGE_PASSPHRASE, as they do in CI.
func runRealScript(t *testing.T, lines ...string) cliResult {
	t.Helper()
	res, _ := runCLIWithApp(t, []string{"--script", writeScript(t, lines...)},
		strings.NewReader(""), false, nil, func(app *App) {
			app.Prompter = newTerminalPrompter(app.In, app.Err)
		})
	return res
}

// TestApproveRefusesATakenNameBeforeTheConfirmationAndBeforeAnyUnlock.
//
// This one genuinely can come first, unlike the full-access pre-flight:
// it reads the plaintext recipient list, so it costs neither a
// confirmation nor a passphrase. The message names --device, which is
// the whole fix — the code authenticated the key, not the label.
func TestApproveRefusesATakenNameBeforeTheConfirmationAndBeforeAnyUnlock(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")

	// Somebody else takes "phone-1", with a different key, in the window
	// between the request being written and it being approved.
	if res := runCLI(t, []string{"recipient", "add", newRecipientKey(t), "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("taking the name: %s", res.Stderr)
	}

	p := &decliningApprovalPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve", "--code", code}, "", false, p)

	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "--device") {
		t.Errorf("the refusal does not name the fix:\n%s", res.Stderr)
	}
	if len(p.confirms) != 0 {
		t.Errorf("a taken name still cost %d confirmations, want 0", len(p.confirms))
	}
	if len(p.requests) != 0 {
		t.Errorf("a taken name still cost %d passphrase prompts, want 0", len(p.requests))
	}

	// And --device is a way through it, not just advice.
	relabel := runCLI(t, []string{"recipient", "approve", "--code", code, "--device", "phone-2"}, "")
	if relabel.Code != 0 {
		t.Fatalf("--device did not resolve the collision: exit %d, stderr=%s", relabel.Code, relabel.Stderr)
	}
}
