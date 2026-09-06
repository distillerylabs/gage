package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/recipients"
	"github.com/denmark/gage/internal/gage/vaultconfig"
	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// decliningPrompter answers the trust-cache question with no and
// everything else normally.
//
// It is the CLI-side stand-in for both a human typing n and a
// non-interactive run, where terminalPrompter's bare-Enter default is
// already no. Every --yes test below runs against it on purpose: with an
// agreeable fake, a passing --yes test would prove nothing about --yes.
type decliningPrompter struct {
	fakePrompter

	// confirms records the plain yes/no questions, which --yes must
	// never answer on the user's behalf.
	confirms []string
}

func (p *decliningPrompter) ConfirmRecipientChange(w gage.RecipientChangeWarning) (bool, error) {
	p.recipientChanges = append(p.recipientChanges, w)
	return false, nil
}

func (p *decliningPrompter) Confirm(prompt string) (bool, error) {
	p.confirms = append(p.confirms, prompt)
	return false, nil
}

func newDecliningPrompter() *decliningPrompter {
	return &decliningPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
}

// bootstrapTrustCache runs one command that unlocks, so the vault has a
// reviewed baseline to change *from*. Without it every later edit is a
// first use, which is silently trusted by design.
func bootstrapTrustCache(t *testing.T) {
	t.Helper()
	if res := runCLI(t, []string{"ls"}, ""); res.Code != 0 {
		t.Fatalf("bootstrapping the trust cache with `ls` failed: %d %s", res.Code, res.Stderr)
	}
}

// commitRoutineRecipientChangeCLI appends a recipient to both files and
// commits — the shape a legitimate `recipient add` on another device
// arrives in after a pull.
func commitRoutineRecipientChangeCLI(t *testing.T, vaultPath, device, pubkey string) {
	t.Helper()

	keys, err := recipients.Read(filepath.Join(vaultPath, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	if err := recipients.Write(filepath.Join(vaultPath, ".age-recipients"), append(keys, pubkey)); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(vaultPath, ".gage", "config.toml")
	vc, err := vaultconfig.Read(configPath)
	if err != nil {
		t.Fatal(err)
	}
	vc.Recipients = append(vc.Recipients, vaultconfig.Recipient{Device: device, Pubkey: pubkey})
	if err := vaultconfig.Write(configPath, vc); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(vaultPath, "gage: recipient add "+device); err != nil {
		t.Fatal(err)
	}
}

// tamperRecipientsFileCLI edits only the file encryption reads, and
// commits it — the divergence `verify` reports and the trust cache
// refuses to forget.
func tamperRecipientsFileCLI(t *testing.T, vaultPath, pubkey string) {
	t.Helper()

	path := filepath.Join(vaultPath, ".age-recipients")
	keys, err := recipients.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := recipients.Write(path, append(keys, pubkey)); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(vaultPath, "add a key"); err != nil {
		t.Fatal(err)
	}
}

// trustCacheDirForTest is where this device's cache for a vault lives,
// under the roots isolateXDG set up.
func trustCacheDirForTest(t *testing.T, vault string) string {
	t.Helper()
	state, err := xdgpaths.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(state, vault)
}

// TestTerminalPrompterRendersTheRecipientChangeAsTheDesignDocPrompt is
// the rendering half of "the warning is a typed value, not printed
// text": the library hands over a RecipientChangeWarning, and this is
// the CLI turning it into the terminal diff + [y/N] the design doc
// shows.
func TestTerminalPrompterRendersTheRecipientChangeAsTheDesignDocPrompt(t *testing.T) {
	w := gage.RecipientChangeWarning{
		Vault: "personal",
		Diff: "--- known-config.toml (last confirmed 2026-08-14)\n" +
			"+++ .gage/config.toml (current)\n" +
			"+[[recipients]]\n" +
			"+device = \"unknown-device\"\n",
		Added:         []gage.VaultRecipient{{Device: "unknown-device", Pubkey: "age1qz8x2"}},
		LastConfirmed: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
	}

	t.Run("routine change", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader("y\n"), &out)
		w.Verification = gage.RecipientVerification{InSync: true}

		ok, err := p.ConfirmRecipientChange(w)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Error("answering y did not confirm")
		}
		for _, want := range []string{"personal", "known-config.toml", "unknown-device", "[y/N]"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("the prompt is missing %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("mismatched change is rendered as its own, more severe line", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader("y\n"), &out)
		w.Verification = gage.RecipientVerification{
			InSync:               false,
			OnlyInRecipientsFile: []string{"age1stray"},
		}

		if _, err := p.ConfirmRecipientChange(w); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "age1stray") {
			t.Errorf("the mismatch prompt does not name the key only .age-recipients has:\n%s", out.String())
		}
		if !strings.Contains(out.String(), ".age-recipients") {
			t.Errorf("the mismatch prompt does not name the diverged file:\n%s", out.String())
		}
	})

	t.Run("a bare Enter means no", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader("\n"), &out)
		w.Verification = gage.RecipientVerification{InSync: true}

		ok, err := p.ConfirmRecipientChange(w)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("a bare Enter approved an unreviewed recipient list; the safe answer is the default")
		}
	})
}

// TestInsertBlocksOnAnUnreviewedRecipientChange is the blocking check
// end to end: a change nobody here reviewed stops the write, and
// declining leaves the vault exactly as it was.
func TestInsertBlocksOnAnUnreviewedRecipientChange(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")
	bootstrapTrustCache(t)

	commitRoutineRecipientChangeCLI(t, vaultPath, "phone-1", newRecipientKey(t))
	before, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}

	p := newDecliningPrompter()
	res, _ := runCLIWithPrompter(t, []string{"insert", "ProtonMail", "-m"}, "hunter2\n", false, p)

	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("insert over a declined recipient change exit code = %d, want Conflict (%d); stderr=%s",
			res.Code, exitcode.Conflict, res.Stderr)
	}
	if len(p.recipientChanges) != 1 {
		t.Fatalf("the frontend was asked %d times, want exactly 1", len(p.recipientChanges))
	}
	if !strings.Contains(p.recipientChanges[0].Diff, "phone-1") {
		t.Errorf("the warning handed to the frontend has no usable diff: %q", p.recipientChanges[0].Diff)
	}

	after, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("commit count = %d, want %d; a declined write commits nothing", after, before)
	}
	if res := runCLI(t, []string{"ls"}, ""); strings.Contains(res.Stdout, "ProtonMail") {
		t.Error("the entry was written despite the refusal")
	}
}

// TestYesBypassesTheRecipientPromptForScripting is `--yes` doing its
// one job: the prompter still says no, and the write goes through
// anyway, because --yes answers the question before the prompter is
// ever asked.
func TestYesBypassesTheRecipientPromptForScripting(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")
	bootstrapTrustCache(t)
	commitRoutineRecipientChangeCLI(t, vaultPath, "phone-1", newRecipientKey(t))

	p := newDecliningPrompter()
	res, _ := runCLIWithPrompter(t, []string{"insert", "ProtonMail", "--yes", "-m"}, "hunter2\n", false, p)
	if res.Code != 0 {
		t.Fatalf("insert --yes exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if len(p.recipientChanges) != 0 {
		t.Errorf("--yes still asked the frontend %d times", len(p.recipientChanges))
	}

	// Routine case: the acknowledgment sticks, exactly as an interactive
	// yes would.
	p2 := newDecliningPrompter()
	res2, _ := runCLIWithPrompter(t, []string{"insert", "Bank", "-m"}, "hunter3\n", false, p2)
	if res2.Code != 0 {
		t.Fatalf("the next write after --yes was blocked: %d %s", res2.Code, res2.Stderr)
	}
	if len(p2.recipientChanges) != 0 {
		t.Error("--yes confirmed the change but did not regenerate the cache; the warning came back")
	}
}

// TestYesDoesNotRegenerateTheCacheOnAMismatch is the resolved decision
// about the case CI is worst at: --yes proceeds, but it buys no silence
// when the two recipient files disagree. Nothing here is a special case
// for --yes — the library decides regeneration from `verify`, so the
// answer's origin cannot change it.
func TestYesDoesNotRegenerateTheCacheOnAMismatch(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")
	bootstrapTrustCache(t)

	stray := newRecipientKey(t)
	tamperRecipientsFileCLI(t, vaultPath, stray)

	if res := runCLI(t, []string{"insert", "ProtonMail", "--yes", "-m"}, "hunter2\n"); res.Code != 0 {
		t.Fatalf("insert --yes over a mismatch exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	// Still mismatched, still warning.
	p := newDecliningPrompter()
	res, _ := runCLIWithPrompter(t, []string{"insert", "Bank", "--yes", "-m"}, "hunter3\n", false, p)
	if res.Code != 0 {
		t.Fatalf("the second --yes write failed: %d %s", res.Code, res.Stderr)
	}

	verify := runCLI(t, []string{"recipient", "verify"}, "")
	if verify.Code == 0 {
		t.Error("recipient verify passes after --yes past a mismatch; the inconsistency was never fixed")
	}
	if !strings.Contains(verify.Stderr+verify.Stdout, stray) {
		t.Error("verify no longer reports the stray key")
	}
}

// TestYesDoesNotAnswerTheSelfRemovalConfirmation keeps --yes narrow.
// It answers M10's trust-cache question and nothing else: M9's "this
// device will no longer be able to read this vault" is a different
// question, and a scripted run must still fail it closed.
func TestYesDoesNotAnswerTheSelfRemovalConfirmation(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")

	// A second recipient, so the removal below isn't refused as the last
	// one for an unrelated reason.
	if res := runCLI(t, []string{"recipient", "add", newRecipientKey(t), "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("recipient add: %d %s", res.Code, res.Stderr)
	}

	p := newDecliningPrompter()
	res, _ := runCLIWithPrompter(t,
		[]string{"recipient", "remove", "laptop-1", "--reencrypt", "--yes"}, "", false, p)

	if res.Code == 0 {
		t.Fatal("--yes removed this device's own key without a real confirmation")
	}
	if len(p.confirms) == 0 {
		t.Error("--yes answered the self-removal question itself; it must only answer the recipient-change prompt")
	}
	if !hasKey(ageRecipientsFile(t, mustVaultPath(t, "personal")), mustOwnPubkey(t, "personal")) {
		t.Error("this device's key was removed despite the refusal")
	}
}

// TestRecipientAddIsRefusedOverADivergedVaultFromTheCLI is the CLI face
// of the revised M9 behavior: the add refuses, names the difference, and
// leaves the stray key in place for `verify` to keep reporting.
func TestRecipientAddIsRefusedOverADivergedVaultFromTheCLI(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")
	bootstrapTrustCache(t)

	stray := newRecipientKey(t)
	tamperRecipientsFileCLI(t, vaultPath, stray)
	before := ageRecipientsFile(t, vaultPath)

	res := runCLI(t, []string{"recipient", "add", newRecipientKey(t), "--device", "phone-1"}, "")
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("recipient add over a diverged vault exit code = %d, want Conflict (%d); stderr=%s",
			res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, stray) {
		t.Errorf("the refusal does not name the stray key:\n%s", res.Stderr)
	}

	after := ageRecipientsFile(t, vaultPath)
	if strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Error(".age-recipients was rebuilt by a refused add; the evidence must survive")
	}
	if !hasKey(after, stray) {
		t.Error("the stray key was rebuilt away")
	}
}

// TestRecipientVerifyRepairFixesTheDivergence is the exit the refusal
// ships with: the same overwrite `recipient add` used to perform
// silently, as a deliberate act that says what it drops.
func TestRecipientVerifyRepairFixesTheDivergence(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")
	bootstrapTrustCache(t)

	stray := newRecipientKey(t)
	tamperRecipientsFileCLI(t, vaultPath, stray)

	if res := runCLI(t, []string{"recipient", "verify"}, ""); res.Code == 0 {
		t.Fatal("verify passes on a diverged vault")
	}

	res := runCLI(t, []string{"recipient", "verify", "--repair"}, "")
	if res.Code != 0 {
		t.Fatalf("recipient verify --repair exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, stray) {
		t.Errorf("the repair did not say which key it dropped:\n%s%s", res.Stdout, res.Stderr)
	}

	if res := runCLI(t, []string{"recipient", "verify"}, ""); res.Code != 0 {
		t.Fatalf("verify still fails after --repair: %d %s", res.Code, res.Stderr)
	}
	if hasKey(ageRecipientsFile(t, vaultPath), stray) {
		t.Error("the stray key survived the repair")
	}
	clean, err := gitrepo.IsClean(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("the working tree is dirty after --repair; the rewrite must be committed")
	}
}

// TestVaultRemoveDeletesTheTrustCache is the resolved decision that
// deregistering a vault forgets what this device approved about it.
// Keeping the cache would let a re-add silently inherit an approval for
// a recipient list nobody looked at in the interval — the one thing this
// cache exists to prevent.
func TestVaultRemoveDeletesTheTrustCache(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")
	bootstrapTrustCache(t)

	dir := trustCacheDirForTest(t, "personal")
	if _, err := os.Stat(filepath.Join(dir, "known-config.toml")); err != nil {
		t.Fatalf("no trust cache to remove: %v", err)
	}

	if res := runCLI(t, []string{"vault", "remove", "personal"}, ""); res.Code != 0 {
		t.Fatalf("vault remove: %d %s", res.Code, res.Stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("$GAGE_STATE/personal still exists after vault remove (err=%v)", err)
	}
}

// TestYesIsRefusedInASession keeps --yes to the runs it is for.
//
// A session is a human at a prompt, which is the case a recipient diff
// exists to be shown to. The flag would also not do what it looks like
// it does: a --yes typed on one session line never reaches the check at
// all (that asks through the prompter the session unlocked with, not
// through the flag's), while `gage --yes` on the way *into* a session
// would silently approve every recipient change for as long as the
// session lasted. Refusing says so instead.
func TestYesIsRefusedInASession(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")
	bootstrapTrustCache(t)
	tamperRecipientsFileCLI(t, vaultPath, newRecipientKey(t))

	t.Run("typed on a session line", func(t *testing.T) {
		res := runSessionScript(t, script(
			"use personal",
			testPassphrase,
			"insert --yes First",
			"exit",
		))
		if !strings.Contains(res.Stderr, "isn't available in a session") {
			t.Errorf("--yes was not refused inside a session:\n%s", res.Stderr)
		}
		// It really was refused, not quietly honored: the write never
		// happened, so nothing was approved on anyone's behalf.
		if out := runCLI(t, []string{"ls"}, ""); strings.Contains(out.Stdout, "First") {
			t.Error("the entry was written by a command whose --yes was refused")
		}
	})

	t.Run("on the way into one", func(t *testing.T) {
		res := runCLIWithTerminal(t, []string{"--yes"}, "", true)
		if res.Code != int(exitcode.Usage) {
			t.Fatalf("`gage --yes` exit code = %d, want Usage (%d); stderr=%s",
				res.Code, exitcode.Usage, res.Stderr)
		}
		if !strings.Contains(res.Stderr, "isn't available in a session") {
			t.Errorf("`gage --yes` did not explain why it refused:\n%s", res.Stderr)
		}
	})
}

// mustVaultPath resolves a registered vault's on-disk path.
func mustVaultPath(t *testing.T, name string) string {
	t.Helper()
	g := readGlobalConfigForTest(t)
	entry, ok := g.Vaults[name]
	if !ok {
		t.Fatalf("vault %q is not registered", name)
	}
	return entry.Path
}

// mustOwnPubkey returns this device's own public key for a vault, as
// recorded in the committed config.
func mustOwnPubkey(t *testing.T, name string) string {
	t.Helper()
	g := readGlobalConfigForTest(t)
	device := g.Vaults[name].Device
	for _, r := range readVaultConfigForTest(t, name).Recipients {
		if r.Device == device {
			return r.Pubkey
		}
	}
	t.Fatalf("no recipient recorded for this device (%q)", device)
	return ""
}
