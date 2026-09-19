package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage"
)

// Clone's offer to enroll. There is deliberately no --enroll flag: clone
// already detects this exact condition and already reports it, so a flag
// opting into acting on a fact the tool just printed would carry no
// information. See "Why there is no `--enroll` flag".

// runCloneWithTerminalPrompter drives a clone through the *real*
// terminal prompter over a scripted stdin, which is what lets a test see
// the prompt as it is rendered rather than only the answer a fake gave.
func runCloneWithTerminalPrompter(t *testing.T, args []string, stdin string) cliResult {
	t.Helper()

	var res cliResult
	res, _ = runCLIWithApp(t, args, strings.NewReader(stdin), true, nil, func(app *App) {
		p := newTerminalPrompter(app.In, app.Err)
		p.interactive = true
		app.Prompter = p
	})
	return res
}

// answers is the scripted stdin for an accepted offer: the answer to the
// offer itself, then the new passphrase twice.
func answers(offer string) string {
	return offer + testPassphrase + "\n" + testPassphrase + "\n"
}

// TestInteractiveCloneOffersToEnrollAndYesPublishes: answering yes
// produces the same end state as `clone` followed by `identity enroll`.
func TestInteractiveCloneOffersToEnrollAndYesPublishes(t *testing.T) {
	remote, _ := ownedVault(t, "personal")

	res := runCloneWithTerminalPrompter(t,
		[]string{"clone", remote, "--name", "personal", "--device", "phone-1"}, answers("y\n"))
	if res.Code != 0 {
		t.Fatalf("clone exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "enrollment request") && !strings.Contains(res.Stdout, "enrollment request") {
		t.Errorf("clone did not offer to enroll:\nstdout=%s\nstderr=%s", res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "GAGE-") {
		t.Errorf("stdout = %q, want the enrollment code", res.Stdout)
	}
	if files := pendingOnRemote(t, remote); len(files) != 1 {
		t.Errorf("the remote holds %v, want the one published request", files)
	}
	if identityCount(t, "personal") != 1 {
		t.Error("the accepted offer wrote no identity file")
	}
	if got := readGlobalConfigForTest(t).Vaults["personal"].Pubkey; got == "" {
		t.Error("the accepted offer recorded no pubkey in global config")
	}
}

// TestCloneOfferDefaultsToYes pins both the default and the rendering. A
// ConfirmDefaultYes accidentally wired to Confirm still compiles, still
// passes a yes-answering test, and silently makes Enter mean no — so the
// bare Enter and the [Y/n] are asserted together.
func TestCloneOfferDefaultsToYes(t *testing.T) {
	remote, _ := ownedVault(t, "personal")

	res := runCloneWithTerminalPrompter(t,
		[]string{"clone", remote, "--name", "personal", "--device", "phone-1"}, answers("\n"))
	if res.Code != 0 {
		t.Fatalf("clone exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "[Y/n]") {
		t.Errorf("the offer rendered as %q, want [Y/n] — this is the one question in gage that defaults to yes",
			res.Stderr)
	}
	if files := pendingOnRemote(t, remote); len(files) != 1 {
		t.Errorf("a bare Enter did not enroll; the remote holds %v", files)
	}
}

// TestCloneOfferDeclinedLeavesTheVaultCloned: answering n writes no
// identity and pushes nothing, and a later `identity enroll` still
// works — the offer is a convenience, not the only route.
func TestCloneOfferDeclinedLeavesTheVaultCloned(t *testing.T) {
	remote, _ := ownedVault(t, "personal")

	res := runCloneWithTerminalPrompter(t,
		[]string{"clone", remote, "--name", "personal", "--device", "phone-1"}, "n\n")
	if res.Code != 0 {
		t.Fatalf("clone exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; !ok {
		t.Fatal("declining the offer left the vault unregistered")
	}
	if identityCount(t, "personal") != 0 {
		t.Error("a declined offer still wrote an identity file")
	}
	if files := pendingOnRemote(t, remote); len(files) != 0 {
		t.Errorf("a declined offer still published %v", files)
	}
	assertAccessLines(t, res.Stdout)

	// And the route the message points at still works.
	if res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("enrolling after declining the offer: %s", res.Stderr)
	}
	if files := pendingOnRemote(t, remote); len(files) != 1 {
		t.Errorf("the remote holds %v, want the request the later enroll published", files)
	}
}

// TestNonInteractiveCloneNeverOffersAndExitsZero is the behavior a
// scripted clone has today, with one deliberate difference: the message
// it prints is the reworded accessLines, which now names `identity
// enroll` alongside the manual path.
func TestNonInteractiveCloneNeverOffersAndExitsZero(t *testing.T) {
	remote, _ := ownedVault(t, "personal")

	res, p := runCLIWithPrompter(t, []string{"clone", remote, "--name", "personal", "--device", "phone-1"},
		"", false, &fakePrompter{passphrases: []string{testPassphrase}})
	if res.Code != 0 {
		t.Fatalf("clone exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	fake, ok := p.(*fakePrompter)
	if !ok {
		t.Fatalf("the run used a %T, not the fake this assertion reads", p)
	}
	if len(fake.requests) != 0 {
		t.Errorf("a non-interactive clone asked for a passphrase %d times, want 0", len(fake.requests))
	}
	if identityCount(t, "personal") != 0 {
		t.Error("a non-interactive clone wrote an identity file")
	}
	if files := pendingOnRemote(t, remote); len(files) != 0 {
		t.Errorf("a non-interactive clone published %v", files)
	}
	assertAccessLines(t, res.Stdout)
}

// assertAccessLines pins the message a user copies out of a declined or
// no-TTY clone. `identity enroll` is the one that also publishes and is
// what someone in that position should be pointed at; the manual path
// stays for the read-only-remote and air-gapped cases.
func assertAccessLines(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{
		"cannot decrypt",
		"gage identity enroll",
		"gage identity add",
		"gage recipient add",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("clone said %q, want it to name %q", out, want)
		}
	}
}

// TestTheOfferTracksWhereACreateCanBeAnswered: the offer is made exactly
// where the passphrase behind it can be answered, which is one rule
// rather than a TTY test of its own. Offering to enroll and then
// refusing the passphrase the offer requires would be asking a question
// whose only outcome is an error.
func TestTheOfferTracksWhereACreateCanBeAnswered(t *testing.T) {
	for _, tc := range []struct {
		name       string
		app        App
		wantOffer  bool
		wantReason string
	}{
		{
			name:      "a terminal",
			app:       App{IsTerminal: func() bool { return true }},
			wantOffer: true,
		},
		{
			name:       "no terminal",
			app:        App{IsTerminal: func() bool { return false }},
			wantReason: "there is nobody to type a new passphrase",
		},
		{
			name:       "--stdin",
			app:        App{IsTerminal: func() bool { return true }, ScriptStdin: true},
			wantReason: "stdin is the command stream, so a create can never be answered on it",
		},
		{
			name:      "--script FILE at a terminal",
			app:       App{IsTerminal: func() bool { return true }, ScriptFile: "commands.gage"},
			wantOffer: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := tc.app
			if got := canAnswerNewPassphrase(&app); got != tc.wantOffer {
				t.Errorf("canAnswerNewPassphrase = %v, want %v (%s)", got, tc.wantOffer, tc.wantReason)
			}
		})
	}
}

// TestCloneDoesNotOfferWhenThisDeviceAlreadyHoldsAnIdentity: a local
// identity was set up deliberately, and clone can't cheaply tell whether
// it is already a recipient — so it is left alone, and no unlock is
// attempted during the clone.
func TestCloneDoesNotOfferWhenThisDeviceAlreadyHoldsAnIdentity(t *testing.T) {
	remote, _ := ownedVault(t, "personal")

	// A machine that declined the offer and then registered a key by
	// hand — one of the three states this rule is deliberately
	// conservative about.
	if res := runCloneWithTerminalPrompter(t,
		[]string{"clone", remote, "--name", "personal", "--device", "phone-1"}, "n\n"); res.Code != 0 {
		t.Fatalf("the first clone: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"identity", "add", "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("identity add: %s", res.Stderr)
	}

	// Cloning the same vault again under a second local name. The
	// identity file is keyed by the vault's id, so this device holds one
	// for the vault being cloned.
	res, p := runCLIWithPrompter(t,
		[]string{"clone", remote, "--name", "personal-again", "--dir", filepath.Join(t.TempDir(), "again"),
			"--device", "phone-1"},
		"", true, &fakePrompter{passphrases: []string{testPassphrase}})
	if res.Code != 0 {
		t.Fatalf("re-cloning: %s", res.Stderr)
	}
	fake, ok := p.(*fakePrompter)
	if !ok {
		t.Fatalf("the run used a %T, not the fake this assertion reads", p)
	}
	if len(fake.requests) != 0 {
		t.Errorf("the re-clone attempted %d unlocks, want 0 — prompting during a clone to report that "+
			"the unlock was pointless is exactly backwards", len(fake.requests))
	}
	if strings.Contains(res.Stdout, "cannot decrypt") {
		t.Errorf("clone said %q; a device that already holds an identity is left alone", res.Stdout)
	}
	if files := pendingOnRemote(t, remote); len(files) != 0 {
		t.Errorf("the re-clone published %v", files)
	}
}

// TestCloneDeviceFlagCarriesThroughToTheOffer: clone resolves the device
// name already, and the offer has to use the one it resolved rather than
// deriving a second one from the hostname.
func TestCloneDeviceFlagCarriesThroughToTheOffer(t *testing.T) {
	remote, _ := ownedVault(t, "personal")

	res := runCloneWithTerminalPrompter(t,
		[]string{"clone", remote, "--name", "personal", "--device", "deliberately-named"}, answers("y\n"))
	if res.Code != 0 {
		t.Fatalf("clone: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "deliberately-named") {
		t.Errorf("stdout = %q, want the request to name the device clone resolved", res.Stdout)
	}
	if got := readGlobalConfigForTest(t).Vaults["personal"].Device; got != "deliberately-named" {
		t.Errorf("global config device = %q, want deliberately-named", got)
	}

	// And the sealed request agrees, not just the printed line.
	code := enrollmentCodeIn(t, res.Stdout)
	entry := readGlobalConfigForTest(t).Vaults["personal"]
	v := &gage.Vault{Name: "personal", ID: entry.ID, Path: entry.Path}
	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	opened, err := v.OpenEnrollment(pending, []string{code})
	if err != nil {
		t.Fatalf("opening the published request: %v", err)
	}
	if len(opened) != 1 || opened[0].Device != "deliberately-named" {
		t.Errorf("the sealed request = %+v, want one naming deliberately-named", opened)
	}
}

// TestConfirmsDefaultIsUnchangedEverywhereElse. Adding a default-yes
// question must not have been done by changing the default of the one
// that was already there, so this drives an existing default-no question
// — removing this device's own key, which locks it out of the vault —
// through the same real terminal prompter and asserts a bare Enter still
// declines.
func TestConfirmsDefaultIsUnchangedEverywhereElse(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")
	if res := runCLI(t, []string{"recipient", "add", newRecipientKey(t), "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("recipient add: %s", res.Stderr)
	}
	mine := readGlobalConfigForTest(t).Vaults["personal"].Pubkey
	if mine == "" {
		t.Fatal("global config records no pubkey for this device")
	}

	var res cliResult
	res, _ = runCLIWithApp(t, []string{"recipient", "remove", "laptop-1", "--reencrypt"},
		// The unlock comes first, then the confirmation this test is about.
		strings.NewReader(testPassphrase+"\n\n"), true, nil, func(app *App) {
			p := newTerminalPrompter(app.In, app.Err)
			p.interactive = true
			app.Prompter = p
		})

	if !strings.Contains(res.Stderr, "[y/N]") {
		t.Errorf("the confirmation rendered as %q, want [y/N]", res.Stderr)
	}
	if !hasKey(ageRecipientsFile(t, vaultPath), mine) {
		t.Error("a bare Enter removed this device's own key; Confirm still means no on Enter")
	}
}
