package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gittest"
)

// xdgRoot is where the process's XDG roots point right now. These tests
// drive two machines against one remote, so they have to be able to go
// back to the first one after setting up the second — which isolateXDG,
// whose whole job is to move forward to a fresh machine, cannot do.
type xdgRoot struct{ config, data, state string }

func currentXDGRoot(t *testing.T) xdgRoot {
	t.Helper()
	return xdgRoot{
		config: os.Getenv("XDG_CONFIG_HOME"),
		data:   os.Getenv("XDG_DATA_HOME"),
		state:  os.Getenv("XDG_STATE_HOME"),
	}
}

func restoreXDGRoot(t *testing.T, r xdgRoot) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", r.config)
	t.Setenv("XDG_DATA_HOME", r.data)
	t.Setenv("XDG_STATE_HOME", r.state)
}

// ownedVault publishes a vault from one machine and leaves the process
// on a second, empty one — publishedVault, plus the first machine's
// roots handed back so a test can act as the device that can already
// read the vault.
func ownedVault(t *testing.T, name string) (remote string, owner xdgRoot) {
	t.Helper()

	isolateXDG(t)
	_, remote = initVaultWithRemote(t, name)
	if res := runCLI(t, []string{"insert", "ProtonMail"}, "hunter2\n"); res.Code != 0 {
		t.Fatalf("seeding the vault: %s", res.Stderr)
	}
	owner = currentXDGRoot(t)

	isolateXDG(t)
	return remote, owner
}

// clonedVault is the joining device's starting point: a vault published
// from one machine and cloned onto a second, empty one. The process is
// left under the *cloning* machine's XDG roots.
func clonedVault(t *testing.T, name string) (remote string, owner xdgRoot) {
	t.Helper()

	remote, owner = ownedVault(t, name)
	if res := runCLI(t, []string{"clone", remote, "--name", name}, ""); res.Code != 0 {
		t.Fatalf("cloning as the joining device: %s", res.Stderr)
	}
	return remote, owner
}

// authorizeFromOwner is the manual path's second half, run on the
// machine that can already read the vault: `gage recipient add`, which
// re-encrypts and pushes. It leaves the process back on whichever
// machine called it.
func authorizeFromOwner(t *testing.T, owner xdgRoot, device, pubkey string) {
	t.Helper()

	joining := currentXDGRoot(t)
	restoreXDGRoot(t, owner)
	if res := runCLI(t, []string{"recipient", "add", pubkey, "--device", device}, ""); res.Code != 0 {
		t.Fatalf("authorizing %q from the owning device: %s", device, res.Stderr)
	}
	restoreXDGRoot(t, joining)
}

// identityCount is how many wrapped identity files this machine holds
// for a vault — the assertion behind every "wrote no identity" bullet.
func identityCount(t *testing.T, vault string) int {
	t.Helper()

	dir, err := gage.IdentitiesDir(vaultIDForTest(t, vault))
	if err != nil {
		t.Fatal(err)
	}
	des, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, de := range des {
		if strings.HasSuffix(de.Name(), ".age") {
			n++
		}
	}
	return n
}

// assertRequestLifetime checks the published filename's epoch against
// the lifetime that was asked for. The epoch is what `recipient pending`
// renders and what pruning reads, so it has to move with --ttl too.
func assertRequestLifetime(t *testing.T, remote string, want time.Duration) {
	t.Helper()

	files := pendingOnRemote(t, remote)
	if len(files) != 1 {
		t.Fatalf("the remote holds %v, want one request", files)
	}
	_, expires, ok := parsePublishedName(files[0])
	if !ok {
		t.Fatalf("published %q, which is not a filename gage can parse", files[0])
	}
	if got := time.Until(expires); got < want-time.Minute || got > want+time.Minute {
		t.Errorf("the request expires in %s, want about %s", got.Round(time.Second), want)
	}
}

// parsePublishedName splits <request-id>-<expires-epoch>.age the way the
// library does, from cmd/gage's side of the boundary.
func parsePublishedName(name string) (id string, expires time.Time, ok bool) {
	base, found := strings.CutSuffix(name, ".age")
	if !found {
		return "", time.Time{}, false
	}
	cut := strings.LastIndex(base, "-")
	if cut < 0 {
		return "", time.Time{}, false
	}
	epoch, err := strconv.ParseInt(base[cut+1:], 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	return base[:cut], time.Unix(epoch, 0), true
}

// remoteFileContents reads one committed file off the remote.
func remoteFileContents(t *testing.T, remote, path string) string {
	t.Helper()
	return gittest.NewDevice(t, remote).Read(t, path)
}

// enrollmentCodePattern matches a displayed code: the GAGE- prefix and
// four Crockford-base32 groups.
var enrollmentCodePattern = regexp.MustCompile(`GAGE-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}`)

func enrollmentCodeIn(t *testing.T, out string) string {
	t.Helper()
	code := enrollmentCodePattern.FindString(out)
	if code == "" {
		t.Fatalf("no enrollment code in %q", out)
	}
	return code
}

// pendingOnRemote lists the request filenames the remote actually holds.
func pendingOnRemote(t *testing.T, remote string) []string {
	t.Helper()

	d := gittest.NewDevice(t, remote)
	dir := filepath.Join(d.Dir, ".gage", "pending")
	des, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading the remote's pending/: %v", err)
	}
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	return out
}

// TestIdentityEnrollPublishesARequestAndPrintsTheCode is the command's
// whole job, end to end and through the real CLI.
func TestIdentityEnrollPublishesARequestAndPrintsTheCode(t *testing.T) {
	remote, _ := clonedVault(t, "personal")

	res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1"}, "")
	if res.Code != 0 {
		t.Fatalf("identity enroll exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "GAGE-") {
		t.Errorf("stdout = %q, want it to print the enrollment code", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "gage recipient approve") {
		t.Errorf("stdout = %q, want it to print the command the approver runs", res.Stdout)
	}
	if files := pendingOnRemote(t, remote); len(files) != 1 {
		t.Errorf("the remote holds %v, want the one published request", files)
	}
}

// TestIdentityEnrollRecordsThisDevicesPubkey is the local half of the
// split `identity add` already uses: the vault-side work is the
// library's, and recording device/method/pubkey in global config stays
// in cmd/gage.
//
// Without it E0's orphan check falls back to "keep the file and say why"
// on every enrolled device, and enroll's own already-a-recipient check
// has nothing to compare against on the next run.
func TestIdentityEnrollRecordsThisDevicesPubkey(t *testing.T) {
	clonedVault(t, "personal")

	res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1"}, "")
	if res.Code != 0 {
		t.Fatalf("identity enroll failed: %s", res.Stderr)
	}

	entry := readGlobalConfigForTest(t).Vaults["personal"]
	if entry.Device != "phone-1" {
		t.Errorf("global config device = %q, want phone-1", entry.Device)
	}
	if entry.Method != gage.MethodPassphrase {
		t.Errorf("global config method = %q, want %q", entry.Method, gage.MethodPassphrase)
	}
	if entry.Pubkey == "" {
		t.Fatal("global config records no pubkey for the enrolled device")
	}
	if !strings.Contains(res.Stdout, entry.Pubkey) {
		t.Errorf("stdout = %q, want it to name the public key it recorded (%s)", res.Stdout, entry.Pubkey)
	}
}

// TestIdentityEnrollTheSecondTimeIsANoOpOnceApproved is the CLI face of
// D-ENROLL-COLLISIONS' "already a recipient is a success that does no
// work": it says so, publishes nothing, and is specifically not a name
// collision telling the user to pass --device.
func TestIdentityEnrollTheSecondTimeIsANoOpOnceApproved(t *testing.T) {
	remote, owner := clonedVault(t, "personal")
	if res := runCLI(t, []string{"identity", "add", "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("identity add: %s", res.Stderr)
	}
	pubkey := readGlobalConfigForTest(t).Vaults["personal"].Pubkey
	if pubkey == "" {
		t.Fatal("identity add recorded no pubkey")
	}

	// Somebody who can already read the vault authorizes that key.
	authorizeFromOwner(t, owner, "phone-1", pubkey)

	res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1"}, "")
	if res.Code != 0 {
		t.Fatalf("identity enroll on a device that is already a recipient exit code = %d, want 0; stderr=%s",
			res.Code, res.Stderr)
	}
	if strings.Contains(res.Stderr, "--device") {
		t.Errorf("stderr = %q; a device that already has access must not be told to pass --device", res.Stderr)
	}
	if strings.Contains(res.Stdout, "GAGE-") {
		t.Errorf("stdout = %q, want no code — there was nothing to ask for", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "already") {
		t.Errorf("stdout = %q, want it to say this device can already read the vault", res.Stdout)
	}
	if files := pendingOnRemote(t, remote); len(files) != 0 {
		t.Errorf("a no-op enroll published %v", files)
	}
}

// TestIdentityEnrollRefusesANameThatLabelsSomeoneElse: the collision
// that is real, and the message names the flag that fixes it — which is
// what the library cannot know.
func TestIdentityEnrollRefusesANameThatLabelsSomeoneElse(t *testing.T) {
	remote, owner := clonedVault(t, "personal")
	authorizeFromOwner(t, owner, "phone-1", newRecipientKey(t))

	res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1"}, "")
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "--device") {
		t.Errorf("stderr = %q, want it to name --device as the way out", res.Stderr)
	}
	if files := pendingOnRemote(t, remote); len(files) != 0 {
		t.Errorf("a refused enroll published %v", files)
	}

	// The recovery the message points at.
	if res := runCLI(t, []string{"identity", "enroll", "--device", "phone-2"}, ""); res.Code != 0 {
		t.Fatalf("enrolling under a different name: %s", res.Stderr)
	}
	if files := pendingOnRemote(t, remote); len(files) != 1 {
		t.Errorf("the remote holds %v, want the request published under phone-2", files)
	}
}

// TestIdentityEnrollRefusesAnUnusableTTL: the flag reaches the library's
// rejection before anything happens, which is what E2's own test cannot
// show.
func TestIdentityEnrollRefusesAnUnusableTTL(t *testing.T) {
	for _, ttl := range []string{"0", "-1h", "8d", "169h"} {
		t.Run(ttl, func(t *testing.T) {
			remote, _ := clonedVault(t, "personal")

			res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1", "--ttl", ttl}, "")
			if res.Code != int(exitcode.Usage) {
				t.Fatalf("--ttl %s exit code = %d, want %d (usage); stderr=%s",
					ttl, res.Code, exitcode.Usage, res.Stderr)
			}
			if files := pendingOnRemote(t, remote); len(files) != 0 {
				t.Errorf("a refused --ttl published %v", files)
			}
			if identityCount(t, "personal") != 0 {
				t.Error("a refused --ttl still wrote an identity file")
			}
		})
	}
}

// TestIdentityEnrollHonorsTTLAndDefaultsTo24h.
func TestIdentityEnrollHonorsTTLAndDefaultsTo24h(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		remote, _ := clonedVault(t, "personal")
		if res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1"}, ""); res.Code != 0 {
			t.Fatalf("identity enroll: %s", res.Stderr)
		}
		assertRequestLifetime(t, remote, gage.DefaultEnrollmentTTL)
	})
	t.Run("--ttl 2h", func(t *testing.T) {
		remote, _ := clonedVault(t, "personal")
		if res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1", "--ttl", "2h"}, ""); res.Code != 0 {
			t.Fatalf("identity enroll: %s", res.Stderr)
		}
		assertRequestLifetime(t, remote, 2*time.Hour)
	})
}

// TestIdentityEnrollRefusesAnInvalidDeviceName matches AddIdentity's
// existing treatment rather than discovering it later.
func TestIdentityEnrollRefusesAnInvalidDeviceName(t *testing.T) {
	remote, _ := clonedVault(t, "personal")

	res := runCLI(t, []string{"identity", "enroll", "--device", "../etc/passwd"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if files := pendingOnRemote(t, remote); len(files) != 0 {
		t.Errorf("a refused device name published %v", files)
	}
	if identityCount(t, "personal") != 0 {
		t.Error("a refused device name still wrote an identity file")
	}
}

// TestIdentityEnrollWithNoRemoteAndUnreachableRemoteDiffer pins both
// remote errors' exit codes at the boundary a script sees.
func TestIdentityEnrollRemoteErrorsCarryTheirExitCodes(t *testing.T) {
	t.Run("no remote", func(t *testing.T) {
		isolateXDG(t)
		initVaultForTest(t, "local-only", "--device", "laptop-1")

		res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1"}, "")
		if res.Code != int(exitcode.Usage) {
			t.Fatalf("exit code = %d, want %d (usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
		}
		if !strings.Contains(res.Stderr, "set-remote") {
			t.Errorf("stderr = %q, want it to name the command that configures one", res.Stderr)
		}
	})
	t.Run("unreachable remote", func(t *testing.T) {
		remote, _ := clonedVault(t, "personal")
		// Point origin at somewhere that isn't there. A local path gage
		// can classify without a network.
		gone := filepath.Join(t.TempDir(), "gone.git")
		if res := runCLI(t, []string{"git", "set-remote", "personal", gone}, ""); res.Code != 0 {
			t.Fatalf("set-remote: %s", res.Stderr)
		}

		res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1"}, "")
		if res.Code == 0 {
			t.Fatal("identity enroll succeeded against a remote that is not there")
		}
		if identityCount(t, "personal") != 0 {
			t.Error("an unreachable remote still wrote an identity file")
		}
		if files := pendingOnRemote(t, remote); len(files) != 0 {
			t.Errorf("an unreachable remote still published %v", files)
		}
	})
}

// TestIdentityEnrollSelectsAVaultWithUse: --use works like every other
// vault-scoped command's.
func TestIdentityEnrollSelectsAVaultWithUse(t *testing.T) {
	// Two published vaults, then one joining machine that clones both —
	// ownedVault leaves the process on a fresh machine each time, so the
	// second call is the one whose roots the clones land under.
	first, _ := ownedVault(t, "personal")
	second, _ := ownedVault(t, "work")

	if res := runCLI(t, []string{"clone", first, "--name", "personal"}, ""); res.Code != 0 {
		t.Fatalf("cloning personal: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"clone", second, "--name", "work"}, ""); res.Code != 0 {
		t.Fatalf("cloning work: %s", res.Stderr)
	}

	if res := runCLI(t, []string{"identity", "enroll", "--use", "work", "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("identity enroll --use work: %s", res.Stderr)
	}
	if files := pendingOnRemote(t, second); len(files) != 1 {
		t.Errorf("work's remote holds %v, want the one request", files)
	}
	if files := pendingOnRemote(t, first); len(files) != 0 {
		t.Errorf("personal's remote holds %v; --use named the other vault", files)
	}
}

// TestTheTwoSecretsNeverCross. The identity passphrase and the
// enrollment code appear within a few lines of each other on one screen,
// which is exactly the setup for an expensive mistake: one is chosen by
// the user and stays on one machine forever, the other is produced by
// gage and is meant to be sent.
func TestTheTwoSecretsNeverCross(t *testing.T) {
	remote, _ := clonedVault(t, "personal")

	res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1"}, "")
	if res.Code != 0 {
		t.Fatalf("identity enroll: %s", res.Stderr)
	}

	// The passphrase appears in no output stream and in nothing committed.
	if strings.Contains(res.Stdout, testPassphrase) || strings.Contains(res.Stderr, testPassphrase) {
		t.Error("the identity passphrase reached an output stream")
	}
	for _, name := range pendingOnRemote(t, remote) {
		if strings.Contains(remoteFileContents(t, remote, ".gage/pending/"+name), testPassphrase) {
			t.Error("the identity passphrase reached a committed file")
		}
	}

	// And the code appears in no identity file and in nothing committed.
	code := enrollmentCodeIn(t, res.Stdout)
	entry := readGlobalConfigForTest(t).Vaults["personal"]
	path, err := gage.IdentityFilePath(entry.ID, "phone-1")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wrapped), code) {
		t.Error("the enrollment code reached this device's identity file")
	}
	for _, name := range pendingOnRemote(t, remote) {
		if strings.Contains(remoteFileContents(t, remote, ".gage/pending/"+name), code) {
			t.Error("the enrollment code reached committed plaintext")
		}
	}
}

// TestBothSecretsAreLabelledAtTheirPointOfUse: a reader of the transcript
// alone has to be able to tell them apart, since the failure mode is
// someone pasting their identity passphrase into a chat window.
func TestBothSecretsAreLabelledAtTheirPointOfUse(t *testing.T) {
	clonedVault(t, "personal")

	// The real terminal prompter renders the passphrase exchange, so the
	// label on that half is asserted against it rather than against a
	// fake that renders nothing.
	var out strings.Builder
	p := newTerminalPrompter(strings.NewReader(testPassphrase+"\n"+testPassphrase+"\n"), &out)
	if _, err := p.Unlock(gage.UnlockRequest{
		Kind: gage.KindPassphrase, Purpose: gage.PurposeCreate, Vault: "personal", Device: "phone-1", Attempt: 1,
	}); err != nil {
		t.Fatalf("the create exchange: %v", err)
	}
	if !strings.Contains(out.String(), "stays on this device") {
		t.Errorf("the passphrase prompt read %q, want it to say the answer stays on this device", out.String())
	}

	res := runCLI(t, []string{"identity", "enroll", "--device", "phone-1"}, "")
	if res.Code != 0 {
		t.Fatalf("identity enroll: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "safe to send") {
		t.Errorf("stdout = %q, want the code labelled as safe to send", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "not your passphrase") {
		t.Errorf("stdout = %q, want the code labelled as not the passphrase", res.Stdout)
	}
}

// ---------------------------------------------------------------------
// Session mode
//
// `identity enroll` is registered AvailBoth, and the session case has a
// wrinkle one-shot does not: a joining device cannot `use` the vault.
// Session.Use unlocks, and this device holds no key the vault accepts —
// which is the entire reason it is enrolling.
// ---------------------------------------------------------------------

// TestIdentityEnrollWorksInASessionAgainstAVaultThatWasNeverUnlocked.
// Without routing through Session.VaultWithoutUnlocking the command is
// registered for a mode it cannot actually run in.
func TestIdentityEnrollWorksInASessionAgainstAVaultThatWasNeverUnlocked(t *testing.T) {
	remote, _ := clonedVault(t, "personal")

	// The reuse path, which is the one a script can answer: the key
	// exists, so the unlock is an ordinary PurposeUnlock that
	// GAGE_PASSPHRASE answers.
	if res := runCLI(t, []string{"identity", "add", "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("identity add: %s", res.Stderr)
	}
	t.Setenv(envPassphraseVar, testPassphrase)

	res := runScript(t, "identity enroll -u personal --device phone-1")
	if res.Code != 0 {
		t.Fatalf("identity enroll in a session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "GAGE-") {
		t.Errorf("stdout = %q, want the enrollment code", res.Stdout)
	}
	if files := pendingOnRemote(t, remote); len(files) != 1 {
		t.Errorf("the remote holds %v, want the one published request", files)
	}
}

// TestUseStillFailsOnAVaultThisDeviceCannotDecrypt: enroll is not a way
// to unlock, it is what you run instead — so `use` fails as it always
// has, and says which command actually helps.
func TestUseStillFailsOnAVaultThisDeviceCannotDecrypt(t *testing.T) {
	clonedVault(t, "personal")
	t.Setenv(envPassphraseVar, testPassphrase)

	res := runScript(t, "use personal")
	if res.Code == 0 {
		t.Fatal("`use` succeeded on a vault this device holds no identity for")
	}
	if res.Code != int(exitcode.LockedOrAuth) {
		t.Errorf("exit code = %d, want %d (locked/auth)", res.Code, exitcode.LockedOrAuth)
	}
	if !strings.Contains(res.Stderr, "identity enroll") {
		t.Errorf("stderr = %q, want it to name `gage identity enroll`", res.Stderr)
	}
}

// ---------------------------------------------------------------------
// Non-interactive
//
// Both outcomes are inherited from scriptPrompter rather than invented
// here, but nothing pinned either — and "the same command sometimes
// works and sometimes refuses" is the kind of thing that gets "fixed"
// into a bug later.
// ---------------------------------------------------------------------

// TestScriptedEnrollRefusesTheCreatePath: a new identity's passphrase is
// a PurposeCreate answer, and GAGE_PASSPHRASE deliberately never answers
// one — there is nothing to check it against, so a typo would be
// unrecoverable and undiscovered until the next unlock.
func TestScriptedEnrollRefusesTheCreatePath(t *testing.T) {
	for _, withEnv := range []bool{false, true} {
		name := "without GAGE_PASSPHRASE"
		if withEnv {
			name = "with GAGE_PASSPHRASE"
		}
		t.Run(name, func(t *testing.T) {
			remote, _ := clonedVault(t, "personal")
			if withEnv {
				t.Setenv(envPassphraseVar, testPassphrase)
			}

			res := runScript(t, "identity enroll -u personal --device phone-1")
			if res.Code != int(exitcode.LockedOrAuth) {
				t.Fatalf("exit code = %d, want %d (locked/auth); stderr=%s",
					res.Code, exitcode.LockedOrAuth, res.Stderr)
			}
			if identityCount(t, "personal") != 0 {
				t.Error("a refused scripted enroll still wrote an identity file")
			}
			if files := pendingOnRemote(t, remote); len(files) != 0 {
				t.Errorf("a refused scripted enroll published %v", files)
			}
		})
	}
}

// TestScriptedEnrollSucceedsOnTheReusePath: opening an identity that
// already exists is an ordinary PurposeUnlock, so GAGE_PASSPHRASE
// answers it and a fully scripted enroll works end to end. This is the
// retry-after-a-failed-push path, and the reason enroll is scriptable at
// all.
func TestScriptedEnrollSucceedsOnTheReusePath(t *testing.T) {
	remote, _ := clonedVault(t, "personal")
	if res := runCLI(t, []string{"identity", "add", "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("identity add: %s", res.Stderr)
	}
	t.Setenv(envPassphraseVar, testPassphrase)

	res, _ := runCLIWithPrompter(t, []string{"--stdin"},
		"identity enroll -u personal --device phone-1\n", false,
		&fakePrompter{passphrases: []string{testPassphrase}})
	if res.Code != 0 {
		t.Fatalf("--stdin enroll on the reuse path exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "GAGE-") {
		t.Errorf("stdout = %q, want the enrollment code", res.Stdout)
	}
	if files := pendingOnRemote(t, remote); len(files) != 1 {
		t.Errorf("the remote holds %v, want the one published request", files)
	}
}

// TestARefusedScriptedEnrollLeavesTheFastForwardInPlace: the refusal
// lands at step 6 of D-ENROLL-REMOTE's order, so the lock is released
// and nothing needs undoing — what survives is the catch-up from step 4,
// which is simply the vault being more up to date than it was.
func TestARefusedScriptedEnrollLeavesTheFastForwardInPlace(t *testing.T) {
	remote, _ := clonedVault(t, "personal")

	// The remote moves after the clone, so the catch-up has something to
	// do that a test can see.
	gittest.NewDevice(t, remote).WriteCommitPush(t, "README", "moved on", "another device wrote this")

	res := runScript(t, "identity enroll -u personal --device phone-1")
	if res.Code != int(exitcode.LockedOrAuth) {
		t.Fatalf("exit code = %d, want %d (locked/auth); stderr=%s", res.Code, exitcode.LockedOrAuth, res.Stderr)
	}

	vaultPath := readGlobalConfigForTest(t).Vaults["personal"].Path
	if got := readVaultFile(t, vaultPath, "README"); !strings.Contains(got, "moved on") {
		t.Errorf("README = %q; the fast-forward from before the refusal must stand", got)
	}
	// And the lock was released: the next command can take it.
	if res := runCLI(t, []string{"identity", "add", "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("the refused enroll did not release the vault lock: %s", res.Stderr)
	}
}

func readVaultFile(t *testing.T, vaultPath, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(vaultPath, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

// TestScriptFileAtATerminalTakesTheCreatePathNormally: --script FILE
// leaves stdin free for a human, so the PurposeCreate request passes
// through to them and enroll proceeds.
func TestScriptFileAtATerminalTakesTheCreatePathNormally(t *testing.T) {
	remote, _ := clonedVault(t, "personal")

	script := writeScript(t, "identity enroll -u personal --device phone-1")
	res, _ := runCLIWithPrompter(t, []string{"--script", script}, "", true,
		&fakePrompter{passphrases: []string{testPassphrase}})
	if res.Code != 0 {
		t.Fatalf("--script FILE at a terminal exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "GAGE-") {
		t.Errorf("stdout = %q, want the enrollment code", res.Stdout)
	}
	if files := pendingOnRemote(t, remote); len(files) != 1 {
		t.Errorf("the remote holds %v, want the one published request", files)
	}
}

// TestHumanDurationReadsLikeAPersonSaidIt. The line it appears in exists
// to tell someone how long they have, and time.Duration's own "24h0m0s"
// reads like a machine timestamp there.
func TestHumanDurationReadsLikeAPersonSaidIt(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{24 * time.Hour, "24h"},
		{2*time.Hour + 30*time.Minute, "2h30m"},
		{20 * time.Minute, "20m"},
		{7 * 24 * time.Hour, "168h"},
		// A hair under, which is what time.Until reports moments after a
		// request is sealed.
		{24*time.Hour - time.Second, "24h"},
	} {
		if got := humanDuration(tc.in); got != tc.want {
			t.Errorf("humanDuration(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
