package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/gittest"
)

// cliResolvingPrompter is the CLI-side fake: a Prompter that can also
// answer a conflict, so an end-to-end `gage sync` can be driven without a
// terminal.
type cliResolvingPrompter struct {
	fakePrompter

	answers   []gage.Resolution
	presented []gage.EntryConflict
}

func (p *cliResolvingPrompter) ResolveConflict(c gage.EntryConflict) (gage.Resolution, error) {
	p.presented = append(p.presented, c)
	i := len(p.presented) - 1
	if i >= len(p.answers) {
		return 0, errors.New("cliResolvingPrompter: out of scripted answers")
	}
	return p.answers[i], nil
}

// sampleConflict is a presentable conflict built by hand, for the tests
// that exercise only the rendering of the design doc's menu.
func sampleConflict() gage.EntryConflict {
	local := gage.Entry{
		Title:     "ProtonMail",
		Updated:   gage.NewTimestamp(time.Date(2026, 8, 30, 14, 22, 0, 0, time.UTC)),
		UpdatedBy: "laptop-1",
		Value:     "my-rotated-password",
	}
	remote := gage.Entry{
		Title:     "ProtonMail",
		Updated:   gage.NewTimestamp(time.Date(2026, 8, 29, 9, 3, 0, 0, time.UTC)),
		UpdatedBy: "phone",
		Value:     "their-rotated-password",
	}
	return gage.EntryConflict{
		Path:   "entries/4b9d7710-0000-0000-0000-000000000000.age",
		Local:  gage.ConflictSide{Present: true, Entry: local},
		Remote: gage.ConflictSide{Present: true, Entry: remote},
	}
}

// interactivePrompter is a terminalPrompter wired the way a real terminal
// session wires one, reading answers from in.
func interactivePrompter(in string, out *bytes.Buffer) *terminalPrompter {
	p := newTerminalPrompter(strings.NewReader(in), out)
	p.interactive = true
	return p
}

// TestConflictPromptRendersTheDesignDocsMenu pins the prompt from
// "Resolving an entry conflict": the title, both sides' provenance, and
// the five options on offer.
func TestConflictPromptRendersTheDesignDocsMenu(t *testing.T) {
	var out bytes.Buffer
	p := interactivePrompter("l\n", &out)

	if _, err := p.ResolveConflict(sampleConflict()); err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}

	shown := out.String()
	for _, want := range []string{
		"ProtonMail",
		"local", "remote",
		"laptop-1", "phone",
		"2026-08-30", "2026-08-29",
		"[l/r/b/s/q]",
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("the conflict prompt = %q, want it to contain %q", shown, want)
		}
	}

	// The secret itself is never printed: the choice is made on
	// provenance, not by showing two passwords on a shared screen.
	for _, secret := range []string{"my-rotated-password", "their-rotated-password"} {
		if strings.Contains(shown, secret) {
			t.Errorf("the conflict prompt printed a secret value (%q): %q", secret, shown)
		}
	}
}

// TestConflictPromptMapsEveryKey: each documented key reaches the
// resolution it names, so a typo in the mapping can't quietly turn "keep
// both" into "keep local".
func TestConflictPromptMapsEveryKey(t *testing.T) {
	for _, tc := range []struct {
		typed string
		want  gage.Resolution
	}{
		{"l", gage.KeepLocal},
		{"r", gage.KeepRemote},
		{"b", gage.KeepBoth},
		{"s", gage.SkipConflict},
		{"q", gage.AbortSync},
	} {
		var out bytes.Buffer
		p := interactivePrompter(tc.typed+"\n", &out)

		got, err := p.ResolveConflict(sampleConflict())
		if err != nil {
			t.Fatalf("%q: ResolveConflict: %v", tc.typed, err)
		}
		if got != tc.want {
			t.Errorf("%q resolved to %v, want %v", tc.typed, got, tc.want)
		}
	}
}

// TestConflictPromptReAsksOnAnUnknownAnswer: there is no default here.
// Guessing what an unrecognized keystroke meant is how a secret gets
// discarded by someone who hit the wrong key.
func TestConflictPromptReAsksOnAnUnknownAnswer(t *testing.T) {
	var out bytes.Buffer
	p := interactivePrompter("x\n\nb\n", &out)

	got, err := p.ResolveConflict(sampleConflict())
	if err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}
	if got != gage.KeepBoth {
		t.Errorf("resolution = %v, want KeepBoth — the prompt took an unrecognized answer as a choice", got)
	}
	if strings.Count(out.String(), "[l/r/b/s/q]") < 2 {
		t.Errorf("the prompt was not repeated after an unrecognized answer: %q", out.String())
	}
}

// TestDeleteModifyPromptSaysOneSideDeletedIt: the menu still works when
// there is only one version to show, and says why.
func TestDeleteModifyPromptSaysOneSideDeletedIt(t *testing.T) {
	var out bytes.Buffer
	p := interactivePrompter("r\n", &out)

	c := sampleConflict()
	c.Local = gage.ConflictSide{Present: false}

	if _, err := p.ResolveConflict(c); err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}
	if !strings.Contains(out.String(), "deleted") {
		t.Errorf("the prompt = %q, want it to say the local side deleted the entry", out.String())
	}
}

// TestNonInteractivePrompterRefusesToResolve: without a terminal there is
// nobody to ask, and the refusal has to come from the frontend rather
// than from the library guessing.
func TestNonInteractivePrompterRefusesToResolve(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("l\n"), &out)
	// interactive stays false: this is `gage sync` in a script or in CI.

	_, err := p.ResolveConflict(sampleConflict())
	if err == nil {
		t.Fatal("a non-interactive prompter answered a conflict, want it to refuse")
	}
	if !errors.Is(err, gage.ErrNotInteractive) {
		t.Errorf("error = %v, want it to match gage.ErrNotInteractive", err)
	}
	if out.Len() != 0 {
		t.Errorf("a refusing prompter still rendered a menu: %q", out.String())
	}
}

// conflictingVault leaves the CLI's vault genuinely diverged on one
// entry, both versions real ciphertext the local identity can read.
//
// The other device's version is a *copy of a second entry's file*, which
// is what makes this decryptable without the test holding the crypto API:
// it is a real gage entry, encrypted to this vault's recipients, that
// simply now lives at the first entry's path.
func conflictingVault(t *testing.T) (vaultPath string, conflicted string) {
	t.Helper()

	vaultPath, remote := initVaultWithRemote(t, "personal")

	if res := runCLI(t, []string{"insert", "Alpha", "--value-stdin"}, "alpha-secret\n"); res.Code != 0 {
		t.Fatalf("insert Alpha: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"insert", "Beta", "--value-stdin"}, "beta-secret\n"); res.Code != 0 {
		t.Fatalf("insert Beta: %s", res.Stderr)
	}

	// Unlock *before* the other device pushes, so this device's own edit
	// below isn't preceded by an automatic fast-forward that would erase
	// the divergence before it exists.
	v := &gage.Vault{Name: "personal", Path: vaultPath}
	ident, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("unlocking: %v", err)
	}
	defer func() { _ = ident.Close() }()

	alphaID, alpha, err := v.Resolve("Alpha", &ident)
	if err != nil {
		t.Fatalf("resolving Alpha: %v", err)
	}
	betaID, _, err := v.Resolve("Beta", &ident)
	if err != nil {
		t.Fatalf("resolving Beta: %v", err)
	}

	conflicted = "entries/" + alphaID.String() + ".age"
	betaPath := "entries/" + betaID.String() + ".age"

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, conflicted, other.Read(t, betaPath), "the other device's edit")

	// This device edits the same entry: committed locally, and its push
	// then finds the remote has moved.
	alpha.Value = "alpha-rotated-locally"
	alpha.Updated = gage.NewTimestamp(time.Now())
	if err := v.Update(alphaID, alpha, &ident); err != nil {
		t.Fatalf("Update: %v", err)
	}
	return vaultPath, conflicted
}

// TestSyncResolvesAConflictInteractivelyAndPushes is the milestone's own
// definition of done, driven through the CLI: two devices edited one
// entry, a human answered, and the result reached the remote.
func TestSyncResolvesAConflictInteractivelyAndPushes(t *testing.T) {
	isolateXDG(t)
	vaultPath, _ := conflictingVault(t)

	p := &cliResolvingPrompter{
		fakePrompter: fakePrompter{passphrases: []string{testPassphrase}},
		answers:      []gage.Resolution{gage.KeepBoth},
	}
	res, _ := runCLIWithPrompter(t, []string{"sync"}, "", true, p)
	if res.Code != 0 {
		t.Fatalf("sync exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if len(p.presented) != 1 {
		t.Fatalf("%d conflicts presented, want 1", len(p.presented))
	}
	if !strings.Contains(res.Stdout, "resolved") && !strings.Contains(res.Stdout, "merged") {
		t.Errorf("sync said %q, want it to report what it did", res.Stdout)
	}

	// Keep both leaves three entries where there were two.
	entries, err := os.ReadDir(filepath.Join(vaultPath, "entries"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("%d entry files after keep both, want 3", len(entries))
	}

	// And the merge reached the remote: the next push is not rejected.
	if res := runCLI(t, []string{"push"}, ""); res.Code != 0 {
		t.Errorf("push after a resolved sync exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
}

// TestSyncWithoutATerminalRefusesRatherThanChoosing: the same divergence,
// with nobody to ask, must fail loudly and change nothing.
func TestSyncWithoutATerminalRefusesRatherThanChoosing(t *testing.T) {
	isolateXDG(t)
	vaultPath, _ := conflictingVault(t)

	before, err := gitrepo.HeadHash(vaultPath)
	if err != nil {
		t.Fatal(err)
	}

	// runCLI's prompter is a plain fakePrompter: it can unlock, but it
	// cannot resolve a conflict — a script, in other words.
	res := runCLI(t, []string{"sync"}, "")
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("non-interactive sync exit code = %d, want %d (conflict); stderr=%s",
			res.Code, exitcode.Conflict, res.Stderr)
	}

	after, err := gitrepo.HeadHash(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("HEAD moved (%s -> %s) on a sync that had nobody to ask", before, after)
	}
}

// TestYesDoesNotResolveConflicts: `--yes` means exactly one thing — the
// trust-cache confirmation — and must never quietly grow into "pick a
// side for me".
//
// Today `sync` has no `--yes` at all: M10 introduces it, and this fails
// on the unknown flag, which the first assertion pins so the test's
// current meaning isn't mistaken for more than it is. What it is really
// here for is the day M10 adds the flag — from then on the same run
// reaches a real conflict, and the assertions below are what stop it
// being answered on the human's behalf.
func TestYesDoesNotResolveConflicts(t *testing.T) {
	isolateXDG(t)
	vaultPath, _ := conflictingVault(t)

	before, err := gitrepo.HeadHash(vaultPath)
	if err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"sync", "--yes"}, "")
	if res.Code == 0 {
		t.Fatalf("`sync --yes` succeeded on a conflicting divergence; --yes must not resolve conflicts. stdout=%s", res.Stdout)
	}
	// Either shape is acceptable; a *third* one — the flag being accepted
	// and quietly resolving — is what this test exists to catch.
	if !strings.Contains(res.Stderr, "unknown flag") && res.Code != int(exitcode.Conflict) {
		t.Errorf("`sync --yes` failed with code %d (%q); want either the unknown flag or the conflict refusal",
			res.Code, res.Stderr)
	}

	after, err := gitrepo.HeadHash(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("HEAD moved (%s -> %s) under `sync --yes`; it resolved a conflict without being asked to",
			before, after)
	}
}
