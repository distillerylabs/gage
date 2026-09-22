package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
)

// ---------------------------------------------------------------------
// `gage recipient pending` / `approve` / `deny`, end to end through the
// CLI: two machines, one remote, and the code carried between them the
// way a human would.
// ---------------------------------------------------------------------

// enrolledDevice is the whole joining side, run on a fresh machine: a
// clone, an `identity enroll`, and the code it printed. It leaves the
// process on the *owner's* machine, which is where approval happens.
func enrolledDevice(t *testing.T, name, device string) (remote string, owner, joining xdgRoot, code string) {
	t.Helper()

	remote, owner = clonedVault(t, name)
	joining = currentXDGRoot(t)
	res := runCLI(t, []string{"identity", "enroll", "--device", device}, "")
	if res.Code != 0 {
		t.Fatalf("identity enroll: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	code = enrollmentCodeIn(t, res.Stdout)

	restoreXDGRoot(t, owner)
	if res := runCLI(t, []string{"pull"}, ""); res.Code != 0 {
		t.Fatalf("catching the owner up to the request: %s", res.Stderr)
	}
	return remote, owner, joining, code
}

// enrollAnotherDevice publishes a second request against the same
// remote, from a third machine, and leaves the process back where it
// started.
func enrollAnotherDevice(t *testing.T, remote, name, device string) string {
	t.Helper()

	here := currentXDGRoot(t)
	isolateXDG(t)
	if res := runCLI(t, []string{"clone", remote, "--name", name}, ""); res.Code != 0 {
		t.Fatalf("cloning as %q: %s", device, res.Stderr)
	}
	res := runCLI(t, []string{"identity", "enroll", "--device", device}, "")
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

// vaultPathForTest is where the vault this machine has registered under
// name actually lives.
func vaultPathForTest(t *testing.T, name string) string {
	t.Helper()
	entry, ok := readGlobalConfigForTest(t).Vaults[name]
	if !ok {
		t.Fatalf("vault %q is not registered on this machine", name)
	}
	return entry.Path
}

// pendingIDs is what `recipient pending` lists, read back off disk so a
// test can address a request without parsing rendered output.
func pendingIDs(t *testing.T, name ...string) []string {
	t.Helper()
	vault := "personal"
	if len(name) > 0 {
		vault = name[0]
	}
	dir := filepath.Join(vaultPathForTest(t, vault), ".gage", "pending")
	des, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading pending/: %v", err)
	}
	var out []string
	for _, de := range des {
		if id, _, ok := parsePublishedName(de.Name()); ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func firstPendingID(t *testing.T) string {
	t.Helper()
	ids := pendingIDs(t)
	if len(ids) == 0 {
		t.Fatal("no pending requests")
	}
	return ids[0]
}

// commonHexDigit finds a digit every id contains, so an ambiguity can be
// produced from ids the test did not choose.
func commonHexDigit(t *testing.T, ids []string) string {
	t.Helper()
	for _, c := range "0123456789abcdef" {
		hits := 0
		for _, id := range ids {
			if strings.ContainsRune(strings.ReplaceAll(id, "-", ""), c) {
				hits++
			}
		}
		if hits == len(ids) {
			return string(c)
		}
	}
	t.Fatal("no hex digit is common to every pending request id")
	return ""
}

// movePendingInto copies one vault's pending requests into another and
// commits them there — one person with two vaults, and a code that
// belongs to the other one.
func movePendingInto(t *testing.T, from, toPath string) {
	t.Helper()

	src := filepath.Join(vaultPathForTest(t, from), ".gage", "pending")
	dst := filepath.Join(toPath, ".gage", "pending")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	des, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		b, err := os.ReadFile(filepath.Join(src, de.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, de.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := gitrepo.CommitAll(toPath, "test: a request for another vault"); err != nil {
		t.Fatal(err)
	}
}

// TestRecipientPendingNeedsNoUnlockAndShowsNoDeviceNames. The listing
// shows exactly what is knowable without a code: an id and an expiry.
// Device names are under the seal, and showing them would leak who is
// joining to everyone with read access.
func TestRecipientPendingNeedsNoUnlockAndShowsNoDeviceNames(t *testing.T) {
	enrolledDevice(t, "personal", "phone-1")

	res, _ := runCLIWithPrompter(t, []string{"recipient", "pending"}, "", false, noInteractionPrompter{t})
	if res.Code != 0 {
		t.Fatalf("recipient pending: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stdout, "phone-1") {
		t.Errorf("the listing names the joining device, which is sealed:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "expires") {
		t.Errorf("the listing does not show an expiry:\n%s", res.Stdout)
	}
}

// TestRecipientPendingWithNothingPendingExitsZero.
func TestRecipientPendingWithNothingPendingExitsZero(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")

	res, _ := runCLIWithPrompter(t, []string{"recipient", "pending"}, "", false, noInteractionPrompter{t})
	if res.Code != 0 {
		t.Fatalf("recipient pending with nothing pending: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "no pending") {
		t.Errorf("stdout = %q, want a plain 'no pending requests' message", res.Stdout)
	}
}

// TestApproveEndToEndLetsTheDeviceRead is the milestone's definition of
// done, driven entirely from the CLI: a second device is enrolled and
// approved, and then reads an entry written before it existed.
func TestApproveEndToEndLetsTheDeviceRead(t *testing.T) {
	_, _, joining, code := enrolledDevice(t, "personal", "phone-1")

	// Written before the approval and never re-encrypted by hand: this
	// is the entry the joining device has no business being able to read
	// unless approval really did rewrite the whole vault to its key.
	if res := runCLI(t, []string{"insert", "Bank", "--value-stdin"}, "s3cret\n"); res.Code != 0 {
		t.Fatalf("seeding an entry that predates the approval: %s", res.Stderr)
	}

	res := runCLI(t, []string{"recipient", "approve", "--code", code}, "")
	if res.Code != 0 {
		t.Fatalf("recipient approve: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "phone-1") {
		t.Errorf("the approval does not report which device it admitted:\n%s", res.Stdout)
	}

	// Back on the joining device: sync, then read.
	restoreXDGRoot(t, joining)
	if res := runCLI(t, []string{"sync"}, ""); res.Code != 0 {
		t.Fatalf("syncing on the joining device: %s", res.Stderr)
	}
	read := runCLI(t, []string{"cat", "Bank"}, "")
	if read.Code != 0 {
		t.Fatalf("the approved device cannot read an entry written before it existed: exit %d, stderr=%s",
			read.Code, read.Stderr)
	}
	if !strings.Contains(read.Stdout, "s3cret") {
		t.Errorf("cat returned %q, want the secret written before this device was approved", read.Stdout)
	}
}

// TestApproveCostsNoUnlockOnAWrongCode: opening a request needs no
// identity, so a wrong code must cost the approver no passphrase prompt
// at all.
func TestApproveCostsNoUnlockOnAWrongCode(t *testing.T) {
	enrolledDevice(t, "personal", "phone-1")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve", "--code", "GAGE-0000-0000-0000-0000"}, "", false, p)
	if res.Code != int(exitcode.LockedOrAuth) {
		t.Fatalf("exit code = %d, want %d (LockedOrAuth); stderr=%s", res.Code, exitcode.LockedOrAuth, res.Stderr)
	}
	if len(p.requests) != 0 {
		t.Errorf("the approver was asked for a passphrase %d times on a wrong code, want 0", len(p.requests))
	}
}

// TestDecliningTheApprovalCostsNoUnlock, and leaves the request pending
// with nothing committed.
func TestDecliningTheApprovalCostsNoUnlock(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")
	vaultPath := vaultPathForTest(t, "personal")

	before, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}

	p := &decliningApprovalPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve", "--code", code}, "", false, p)
	if res.Code == 0 {
		t.Fatalf("declining the approval reported success:\n%s", res.Stdout)
	}
	if len(p.requests) != 0 {
		t.Errorf("declining still cost %d passphrase prompts, want 0", len(p.requests))
	}
	after, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("declining still committed %d times", after-before)
	}
	if res := runCLI(t, []string{"recipient", "pending"}, ""); !strings.Contains(res.Stdout, "expires") {
		t.Errorf("the request is no longer pending after a declined approval:\n%s", res.Stdout)
	}
}

// decliningApprovalPrompter answers approve's own [y/N] with no, and
// M10's recipient-change question with yes — so a test can tell the two
// apart, which is the whole point of them being different questions.
type decliningApprovalPrompter struct {
	fakePrompter
	confirms []string
}

func (p *decliningApprovalPrompter) Confirm(prompt string) (bool, error) {
	p.confirms = append(p.confirms, prompt)
	return false, nil
}

// TestApproveRefusesAWrongCodeWithoutRetryingWhenItCameFromAFlag: a
// mistyped code is likelier than a surplus one, so every supplied --code
// must open something and one that opens nothing fails the whole run —
// nothing confirmed, nothing approved, the other request still pending.
func TestApproveRefusesAWrongCodeWithoutRetryingWhenItCameFromAFlag(t *testing.T) {
	_, _, _, good := enrolledDevice(t, "personal", "phone-1")
	vaultPath := vaultPathForTest(t, "personal")

	before, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	p := &decliningApprovalPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{
		"recipient", "approve", "--code", good, "--code", "GAGE-0000-0000-0000-0000",
	}, "", false, p)

	if res.Code != int(exitcode.LockedOrAuth) {
		t.Fatalf("exit code = %d, want %d; stderr=%s", res.Code, exitcode.LockedOrAuth, res.Stderr)
	}
	if len(p.confirms) != 0 {
		t.Errorf("the approver was asked to confirm %d times before the run was refused, want 0", len(p.confirms))
	}
	after, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Error("a run with one bad code still committed something")
	}
	if res := runCLI(t, []string{"recipient", "pending"}, ""); !strings.Contains(res.Stdout, "expires") {
		t.Errorf("the good code's request was approved anyway:\n%s", res.Stdout)
	}
}

// TestApproveDeduplicatesARequestOpenedByTwoCodes.
//
// One code passed twice — a copy-paste, or two of the spellings the
// normalizer accepts — opens the same request twice. Left as two entries
// it renders the confirmation screen as "2 pending requests" showing one
// device twice, costs a [y/N] and a passphrase, and only then dies under
// the lock as a within-batch duplicate recipient: an error that is false
// (the vault lists that key nowhere) and that arrives after everything
// it should have been cheap enough to precede. The screen it corrupts is
// the one screen in gage whose correctness depends on a human reading
// it.
//
// "Every supplied --code must open something" is untouched by this, and
// the test above is what pins it: a code that opens nothing still fails
// the whole run. What is dropped here is only the second sighting of a
// request already in the batch.
func TestApproveDeduplicatesARequestOpenedByTwoCodes(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")

	p := &countingApprovalPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{
		"recipient", "approve", "--code", code, "--code", code,
	}, "", false, p)

	if res.Code != 0 {
		t.Fatalf("approve with one code repeated: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "opened 1 pending request.") {
		t.Errorf("the confirmation screen reported one request as several:\n%s", res.Stdout)
	}
	if len(p.confirms) != 1 {
		t.Fatalf("the approver was asked to confirm %d times, want 1", len(p.confirms))
	}

	list := runCLI(t, []string{"recipient", "list"}, "")
	if got := strings.Count(list.Stdout, "phone-1"); got != 1 {
		t.Errorf("phone-1 is listed %d times after approval, want 1:\n%s", got, list.Stdout)
	}
	if pend := runCLI(t, []string{"recipient", "pending"}, ""); !strings.Contains(pend.Stdout, "no pending") {
		t.Errorf("the approved request survived:\n%s", pend.Stdout)
	}
}

// TestApproveReadsTheCodeThroughThePrompterWhenNoFlagIsGiven, and
// cmd/gage — not the library — owns the retry loop around a wrong one.
func TestApproveReadsTheCodeThroughThePrompterWhenNoFlagIsGiven(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")

	p := &fakePrompter{
		passphrases: []string{testPassphrase},
		// A wrong code first, then the right one: the retry loop is what
		// makes the second attempt happen at all.
		values: []string{"GAGE-0000-0000-0000-0000", code},
	}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve"}, "", false, p)
	if res.Code != 0 {
		t.Fatalf("recipient approve with a prompted code: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if len(p.valuePrompts) != 2 {
		t.Fatalf("the code was asked for %d times, want 2 — a wrong one must be retried", len(p.valuePrompts))
	}
	if !strings.Contains(strings.ToLower(p.valuePrompts[0]), "code") {
		t.Errorf("the prompt was %q, want it to name what it is asking for", p.valuePrompts[0])
	}
}

// TestApproveWithADeviceFlagRelabels, recording the approver's name
// against the sealed key.
func TestApproveWithADeviceFlagRelabels(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")

	res := runCLI(t, []string{"recipient", "approve", "--code", code, "--device", "phone-2"}, "")
	if res.Code != 0 {
		t.Fatalf("recipient approve --device: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	list := runCLI(t, []string{"recipient", "list"}, "")
	if !strings.Contains(list.Stdout, "phone-2") {
		t.Errorf("the recipient list does not carry the approver's label:\n%s", list.Stdout)
	}
	if strings.Contains(list.Stdout, "phone-1") {
		t.Errorf("the recipient list kept the request's own label:\n%s", list.Stdout)
	}
}

// TestApproveRejectsAnInvalidDeviceNameBeforeAnythingElse. A rejection
// of the command line should cost neither a code nor a confirmation.
func TestApproveRejectsAnInvalidDeviceNameBeforeAnythingElse(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")

	p := &decliningApprovalPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{
		"recipient", "approve", "--code", code, "--device", "../escape",
	}, "", false, p)

	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if len(p.confirms) != 0 {
		t.Errorf("an invalid --device still cost %d confirmations", len(p.confirms))
	}
	if len(p.requests) != 0 {
		t.Errorf("an invalid --device still cost %d passphrase prompts", len(p.requests))
	}
}

// TestApproveRejectsADeviceFlagAgainstSeveralRequests: one flag cannot
// name which of several requests it relabels, so it is a usage error
// that says to pass an ID rather than a guess.
func TestApproveRejectsADeviceFlagAgainstSeveralRequests(t *testing.T) {
	remote, _, _, first := enrolledDevice(t, "personal", "phone-1")
	second := enrollAnotherDevice(t, remote, "personal", "tablet-1")

	res := runCLI(t, []string{
		"recipient", "approve", "--code", first, "--code", second, "--device", "renamed",
	}, "")
	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "ID") && !strings.Contains(res.Stderr, "id") {
		t.Errorf("the refusal does not name the ID form as the way out:\n%s", res.Stderr)
	}
}

// TestApproveNarrowsToAPositionalID even when the code would have opened
// several requests.
func TestApproveNarrowsToAPositionalID(t *testing.T) {
	remote, _, _, first := enrolledDevice(t, "personal", "phone-1")
	// Captured before the second device enrolls, so it is unambiguously
	// the request `first` opens.
	id := firstPendingID(t)
	enrollAnotherDevice(t, remote, "personal", "tablet-1")

	res := runCLI(t, []string{"recipient", "approve", id, "--code", first}, "")
	if res.Code != 0 {
		t.Fatalf("recipient approve <ID>: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	// The other request is untouched.
	pending := runCLI(t, []string{"recipient", "pending"}, "")
	if !strings.Contains(pending.Stdout, "expires") {
		t.Errorf("the second request was approved too:\n%s", pending.Stdout)
	}
}

// TestDenyRemovesARequestAndNeedsNothing.
func TestDenyRemovesARequestAndNeedsNothing(t *testing.T) {
	enrolledDevice(t, "personal", "phone-1")
	id := firstPendingID(t)

	res, _ := runCLIWithPrompter(t, []string{"recipient", "deny", id[:8]}, "", false,
		&fakePrompter{passphrases: []string{testPassphrase}})
	if res.Code != 0 {
		t.Fatalf("recipient deny: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	pending := runCLI(t, []string{"recipient", "pending"}, "")
	if !strings.Contains(pending.Stdout, "no pending") {
		t.Errorf("the request survived deny:\n%s", pending.Stdout)
	}
	list := runCLI(t, []string{"recipient", "list"}, "")
	if strings.Contains(list.Stdout, "phone-1") {
		t.Error("deny granted something")
	}
}

// TestDenyOnAnUnmatchedIDIsNotFound.
func TestDenyOnAnUnmatchedIDIsNotFound(t *testing.T) {
	enrolledDevice(t, "personal", "phone-1")

	res := runCLI(t, []string{"recipient", "deny", "ffffffff"}, "")
	if res.Code != int(exitcode.NotFound) {
		t.Fatalf("exit code = %d, want %d (NotFound); stderr=%s", res.Code, exitcode.NotFound, res.Stderr)
	}
	if pending := runCLI(t, []string{"recipient", "pending"}, ""); !strings.Contains(pending.Stdout, "expires") {
		t.Error("an unmatched deny still removed something")
	}
}

// TestDenyOnAnAmbiguousIDRendersTheCandidates. The library returns the
// matches as a value; rendering them is cmd/gage's job.
func TestDenyOnAnAmbiguousIDRendersTheCandidates(t *testing.T) {
	remote, _, _, _ := enrolledDevice(t, "personal", "phone-1")
	enrollAnotherDevice(t, remote, "personal", "tablet-1")

	ids := pendingIDs(t)
	if len(ids) != 2 {
		t.Fatalf("set up %d requests, want 2", len(ids))
	}
	shared := commonHexDigit(t, ids)

	res := runCLI(t, []string{"recipient", "deny", shared}, "")
	if res.Code != int(exitcode.Ambiguous) {
		t.Fatalf("exit code = %d, want %d (Ambiguous); stderr=%s", res.Code, exitcode.Ambiguous, res.Stderr)
	}
	for _, id := range ids {
		if !strings.Contains(res.Stderr, id[:8]) {
			t.Errorf("the candidate list does not name %s:\n%s", id[:8], res.Stderr)
		}
	}
	if len(pendingIDs(t)) != 2 {
		t.Error("an ambiguous deny deleted something")
	}
}

// TestApproveUnderScriptStaysARealQuestion: --yes answers only M10's
// recipient-change question, so approve's own [y/N] is still a real one
// and defaults to no.
func TestApproveUnderScriptStaysARealQuestion(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")
	vaultPath := vaultPathForTest(t, "personal")

	before, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	p := &decliningApprovalPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"--yes", "recipient", "approve", "--code", code}, "", false, p)

	if res.Code == 0 {
		t.Fatalf("--yes answered approve's own question:\n%s", res.Stdout)
	}
	if len(p.confirms) != 1 {
		t.Fatalf("approve's own [y/N] was asked %d times, want 1 — --yes must not answer it", len(p.confirms))
	}
	after, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Error("a declined approval still committed something")
	}
	if res.Stderr == "" {
		t.Error("the refusal said nothing about why")
	}
}

// TestPendingAndDenyWorkUnderScript — neither asks anything a script
// cannot answer.
func TestPendingAndDenyWorkUnderScript(t *testing.T) {
	enrolledDevice(t, "personal", "phone-1")
	id := firstPendingID(t)

	if res := runCLI(t, []string{"--yes", "recipient", "pending"}, ""); res.Code != 0 {
		t.Fatalf("recipient pending under --yes: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if res := runCLI(t, []string{"--yes", "recipient", "deny", id}, ""); res.Code != 0 {
		t.Fatalf("recipient deny under --yes: exit %d, stderr=%s", res.Code, res.Stderr)
	}
}

// TestApproveSurfacesAWrongVaultRequestWithoutRetrying. The code worked;
// re-prompting would ask for a code that is already correct.
func TestApproveSurfacesAWrongVaultRequestWithoutRetrying(t *testing.T) {
	_, _, _, code := enrolledDevice(t, "personal", "phone-1")

	// A second vault on the approver's machine, and the request moved
	// into it — one person, two vaults, two codes on screen.
	otherPath := initVaultForTest(t, "work", "--device", "laptop-1")
	movePendingInto(t, "personal", otherPath)

	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{code}}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve", "--use", "work", "--code", code}, "", false, p)

	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "personal") {
		t.Errorf("the refusal does not name the vault the request is for:\n%s", res.Stderr)
	}
	if len(p.valuePrompts) != 0 {
		t.Errorf("a wrong-vault request re-prompted for a code %d times, want 0", len(p.valuePrompts))
	}
	// A refused request is never rendered. The approve confirmation is
	// the one screen in gage whose correctness depends on a human
	// reading it, and a refusal has nothing to show them.
	if strings.Contains(res.Stdout, "phone-1") || strings.Contains(res.Stdout, "age1") {
		t.Errorf("the refused request's contents were rendered anyway:\n%s", res.Stdout)
	}
}

// TestApproveWithAnEmptyPromptedCodeApprovesNothing: an empty answer at
// the code prompt is how a human says "cancel" — the alternative is a
// prompt with no way out short of a signal. It must end the command
// having granted nothing, rather than being retried as a wrong code.
func TestApproveWithAnEmptyPromptedCodeApprovesNothing(t *testing.T) {
	_, _, _, _ = enrolledDevice(t, "personal", "phone-1")

	p := &fakePrompter{
		passphrases: []string{testPassphrase},
		values:      []string{""},
	}
	res, _ := runCLIWithPrompter(t, []string{"recipient", "approve"}, "", false, p)

	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "nothing was approved") {
		t.Errorf("stderr = %q, want it to say nothing was granted", res.Stderr)
	}
	// Asked once and stopped: an empty answer is a cancellation, not a
	// wrong code to retry.
	if len(p.valuePrompts) != 1 {
		t.Errorf("the code was asked for %d times, want 1 — an empty answer cancels", len(p.valuePrompts))
	}

	// And the request is still pending, so cancelling costs nothing.
	pending := runCLI(t, []string{"recipient", "pending"}, "")
	if pending.Code != 0 {
		t.Fatalf("recipient pending: exit %d, stderr=%s", pending.Code, pending.Stderr)
	}
	if strings.Contains(pending.Stdout, "no pending") {
		t.Errorf("a cancelled approval cleared the request:\n%s", pending.Stdout)
	}
}
