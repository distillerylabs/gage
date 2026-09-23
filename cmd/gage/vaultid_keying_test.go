package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/config"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
	"github.com/distillerylabs/gage/internal/gage/vaultlock"
)

// E0 — vault-id keying (A20 / Q-ORPHAN-BY-NAME).
//
// Every per-vault piece of local state — the identities directory, the
// trust cache, the advisory lock — is keyed by a vault id that travels
// inside the vault, rather than by the local registration name, which is
// chosen at clone time and tied to the vault by nothing. These are the
// tests for the properties that keying exists to give.

// writeVaultConfigForTest replaces a registered vault's committed
// .gage/config.toml, standing in for the git-writer this file's contents
// are untrusted input from. It writes raw bytes rather than going
// through vaultconfig.Write, since several of these cases are files
// vaultconfig would refuse to produce.
func writeVaultConfigForTest(t *testing.T, name, content string) {
	t.Helper()
	entry, ok := readGlobalConfigForTest(t).Vaults[name]
	if !ok {
		t.Fatalf("vault %q is not registered", name)
	}
	path := filepath.Join(entry.Path, ".gage", "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readRawVaultConfigForTest returns a registered vault's committed
// config as text, so a test can edit one field and write it back.
func readRawVaultConfigForTest(t *testing.T, name string) string {
	t.Helper()
	entry, ok := readGlobalConfigForTest(t).Vaults[name]
	if !ok {
		t.Fatalf("vault %q is not registered", name)
	}
	// #nosec G304 -- test fixture path under the isolated XDG root.
	data, err := os.ReadFile(filepath.Join(entry.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// replaceCommittedID rewrites the `id = ...` line of a vault's committed
// config, whatever quoting style it was written with, and writes it back
// — the git-writer edit the id's untrusted-input rule exists for.
func replaceCommittedID(t *testing.T, name, id string) {
	t.Helper()
	raw := readRawVaultConfigForTest(t, name)
	lines := strings.Split(raw, "\n")
	replaced := false
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "id =") {
			continue
		}
		lines[i] = "id = '" + id + "'"
		replaced = true
	}
	if !replaced {
		t.Fatalf("no id line to replace in:\n%s", raw)
	}
	writeVaultConfigForTest(t, name, strings.Join(lines, "\n"))
}

// setGlobalVaultEntry rewrites one registration in global config, which
// is how these tests construct the states a hand-edit or an older gage
// would leave behind.
func setGlobalVaultEntry(t *testing.T, name string, mutate func(*config.VaultEntry)) {
	t.Helper()
	g := readGlobalConfigForTest(t)
	entry, ok := g.Vaults[name]
	if !ok {
		t.Fatalf("vault %q is not registered", name)
	}
	mutate(&entry)
	g.Vaults[name] = entry
	if err := writeGlobalConfig(g); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------
// The id itself
// ---------------------------------------------------------------------

// TestInitMintsADistinctUUIDv4PerVault is the base of everything else
// here: an id that is assigned rather than derived, and never shared.
func TestInitMintsADistinctUUIDv4PerVault(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal")
	initVaultForTest(t, "work")

	first := readVaultConfigForTest(t, "personal").Vault.ID
	second := readVaultConfigForTest(t, "work").Vault.ID

	if first == second {
		t.Fatalf("both vaults were minted the id %q", first)
	}
	for _, id := range []string{first, second} {
		parsed, err := uuid.Parse(id)
		if err != nil {
			t.Fatalf("init wrote id %q, which does not parse as a UUID: %v", id, err)
		}
		if parsed.Version() != 4 {
			t.Errorf("init wrote a v%d UUID (%q), want v4", parsed.Version(), id)
		}
	}
}

// TestInitWritesFormatVersionTwo pins the marker A20's migration story
// hangs on.
func TestInitWritesFormatVersionTwo(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal")

	if got := readVaultConfigForTest(t, "personal").Vault.FormatVersion; got != 2 {
		t.Errorf("init wrote format_version = %d, want 2", got)
	}
}

// TestAVaultFromBeforeThisChangeIsRefusedWithARecreateMessage is the
// milestone's definition of done for the schema change: a v1 vault fails
// with advice its owner can act on, rather than being half-parsed or
// told to upgrade a binary that is already newer than the vault.
func TestAVaultFromBeforeThisChangeIsRefusedWithARecreateMessage(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal")

	// Exactly what a v1 vault looks like: format_version 1, and no
	// [vault].id at all, since the field did not exist.
	raw := readRawVaultConfigForTest(t, "personal")
	v1 := strings.Replace(raw, "format_version = 2", "format_version = 1", 1)
	if v1 == raw {
		t.Fatalf("could not downgrade format_version in:\n%s", raw)
	}
	var kept []string
	for _, line := range strings.Split(v1, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "id =") {
			continue
		}
		kept = append(kept, line)
	}
	writeVaultConfigForTest(t, "personal", strings.Join(kept, "\n"))

	res := runCLI(t, []string{"vault", "info", "personal"}, "")
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "re-create") {
		t.Errorf("stderr = %q, want it to say the vault must be re-created", res.Stderr)
	}
	if strings.Contains(res.Stderr, "upgrade gage") {
		t.Errorf("stderr tells the operator to upgrade a gage that is already newer than the vault: %q", res.Stderr)
	}
}

// TestAnUnsafeCommittedIDIsRejectedBeforeAnyPathIsBuilt is
// Q-DEVICE-NAME's rule applied to the field A20 adds. The committed
// config is edited by hand, exactly as a git-writer would.
func TestAnUnsafeCommittedIDIsRejectedBeforeAnyPathIsBuilt(t *testing.T) {
	cases := map[string]string{
		"traversal":         "../../../../etc/cron.d/x",
		"absolute path":     "/etc/passwd",
		"windows traversal": `..\..\Windows\x`,
		"separator":         "9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497/x",
		"not a uuid":        "personal",
		"empty":             "",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			isolateXDG(t)
			initVaultForTest(t, "personal")

			replaceCommittedID(t, "personal", bad)

			res := runCLI(t, []string{"vault", "info", "personal"}, "")
			if res.Code == 0 {
				t.Fatalf("gage accepted a committed id of %q:\n%s", bad, res.Stdout)
			}
			// Nothing may have been built from it on the way out
			// either: no directory named after the injected value.
			dataDir := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage")
			if bad != "" && !strings.ContainsAny(bad, `/\`) {
				if _, err := os.Stat(filepath.Join(dataDir, "identities", bad)); !os.IsNotExist(err) {
					t.Errorf("a path was constructed from the rejected id %q: %v", bad, err)
				}
			}
		})
	}
}

// TestCloneCopiesTheVaultsIDIntoGlobalConfig: the id travels inside the
// vault, so a clone inherits it rather than minting one — which is the
// whole reason two clones of one vault agree however they are named
// locally.
func TestCloneCopiesTheVaultsIDIntoGlobalConfig(t *testing.T) {
	remote := publishedVault(t, "personal")

	if res := runCLI(t, []string{"clone", remote, "--name", "cloned"}, ""); res.Code != 0 {
		t.Fatalf("clone failed: %s", res.Stderr)
	}

	entry := readGlobalConfigForTest(t).Vaults["cloned"]
	committed := readVaultConfigForTest(t, "cloned").Vault.ID
	if !vaultconfig.ValidID(committed) {
		t.Fatalf("the cloned vault's committed id %q is not valid", committed)
	}
	if entry.ID != committed {
		t.Errorf("global config recorded id %q, want the vault's own %q", entry.ID, committed)
	}

	res := runCLI(t, []string{"vault", "info", "cloned"}, "")
	if res.Code != 0 {
		t.Fatalf("vault info failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "id: "+committed) {
		t.Errorf("vault info doesn't report the vault's id:\n%s", res.Stdout)
	}
}

// TestAMismatchedRegistrationIsRefusedRatherThanSilentlyPreferred is the
// thin version of the bug this milestone exists to kill: identity lookup
// would use one id and the recipient list would come from the vault the
// other id belongs to. The two disagree in ordinary ways — a vault
// re-created at the same path, a registration repointed by hand, a
// restored backup — so neither copy may silently win.
func TestAMismatchedRegistrationIsRefusedRatherThanSilentlyPreferred(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")
	if res := runCLI(t, []string{"insert", "ProtonMail"}, "hunter2\n"); res.Code != 0 {
		t.Fatalf("seeding an entry: %s", res.Stderr)
	}

	stranger := vaultconfig.NewID()
	setGlobalVaultEntry(t, "personal", func(e *config.VaultEntry) { e.ID = stranger })

	// An ordinary read command, which would otherwise look this device's
	// identity up under the wrong vault's directory.
	res := runCLI(t, []string{"show", "ProtonMail"}, "")
	if res.Code == 0 {
		t.Fatalf("gage read the vault under a registration pointing at a different vault:\n%s", res.Stdout)
	}
	if res.Code != int(exitcode.Conflict) {
		t.Errorf("exit code = %d, want %d (conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	for _, want := range []string{"different vault", "re-register"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", res.Stderr, want)
		}
	}
}

// TestAnUnreadableVaultFallsBackToGlobalConfigsIDAlone is the case the
// field exists for, and the one the mismatch check above must not break:
// `vault remove` has to know which identities directory a vault owned
// after its files are gone.
func TestAnUnreadableVaultFallsBackToGlobalConfigsIDAlone(t *testing.T) {
	isolateXDG(t)
	path := initVaultForTest(t, "personal", "--device", "laptop-1")
	id := vaultIDForTest(t, "personal")
	idPath := identityFileForTest(t, "personal", "laptop-1")

	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"vault", "remove", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("vault remove on a vault whose files are gone failed: %s", res.Stderr)
	}
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
		t.Error("the registration survived vault remove")
	}
	// The recipient list can't be read, so the key stays — and the
	// message has to prove gage knew which directory to talk about.
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("the identity file should have been kept: %v", err)
	}
	if !strings.Contains(res.Stderr, id) {
		t.Errorf("stderr = %q, want it to name the identities directory (%s) it resolved from global config", res.Stderr, id)
	}
}

// ---------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------

// TestTheIDInitFilesTheIdentityUnderIsTheIDItCommits is the one
// assertion that catches `init` minting a second id after CreateIdentity
// has already written the file — which leaves a working vault and an
// unreachable key, with nothing else visibly wrong.
func TestTheIDInitFilesTheIdentityUnderIsTheIDItCommits(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")

	committed := readVaultConfigForTest(t, "personal").Vault.ID
	path, err := gage.IdentityFilePath(committed, "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no identity file under the id the vault actually carries (%s): %v", committed, err)
	}
	if got := filepath.Base(filepath.Dir(path)); got != committed {
		t.Errorf("identity directory = %q, want the committed [vault].id %q", got, committed)
	}
	if got := vaultIDForTest(t, "personal"); got != committed {
		t.Errorf("global config recorded id %q, want the committed %q", got, committed)
	}
}

// TestTwoVaultsUnderOneLocalNameShareNeitherDirectory is the whole point
// of the milestone, constructed: init A, remove it, clone B under A's
// old name, and B must see neither A's identity nor A's trust cache.
func TestTwoVaultsUnderOneLocalNameShareNeitherDirectory(t *testing.T) {
	remote := publishedVault(t, "vault-b")

	// Vault A, under the local name "shared-name".
	initVaultForTest(t, "shared-name", "--device", "laptop-1")
	aID := vaultIDForTest(t, "shared-name")
	aIdentity := identityFileForTest(t, "shared-name", "laptop-1")
	aTrustCache := trustCacheDirForTest(t, "shared-name")
	if res := runCLI(t, []string{"insert", "SomethingOfAs"}, "hunter2\n"); res.Code != 0 {
		t.Fatalf("seeding A: %s", res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(aTrustCache, "known-config.toml")); err != nil {
		t.Fatalf("A has no trust cache to inherit; the test would prove nothing: %v", err)
	}

	if res := runCLI(t, []string{"vault", "remove", "shared-name"}, ""); res.Code != 0 {
		t.Fatalf("vault remove: %s", res.Stderr)
	}

	// Vault B — entirely unrelated — cloned under A's old name. Into a
	// directory of its own, since `vault remove` never touches the
	// store and A's files are still sitting at the default location.
	if res := runCLI(t, []string{"clone", remote,
		"--name", "shared-name", "--device", "laptop-1",
		"--dir", filepath.Join(t.TempDir(), "vault-b")}, ""); res.Code != 0 {
		t.Fatalf("clone under the reused name failed: %s", res.Stderr)
	}
	bID := vaultIDForTest(t, "shared-name")
	if bID == aID {
		t.Fatalf("two unrelated vaults share the id %q", bID)
	}

	// B holds no identity — it inherited none of A's.
	if _, err := os.Stat(identityFileForTest(t, "shared-name", "laptop-1")); !os.IsNotExist(err) {
		t.Errorf("B sees an identity file it never created: %v", err)
	}
	// And A's is exactly where it was, under A's own id.
	if _, err := os.Stat(aIdentity); err != nil {
		t.Errorf("A's identity file was disturbed by B taking its name: %v", err)
	}
	// Neither is B's trust cache A's.
	if got := trustCacheDirForTest(t, "shared-name"); got == aTrustCache {
		t.Errorf("B inherited A's trust cache directory %q", got)
	}
}

// TestTwoRegistrationsOfOneRepositoryShareOneLock is the reason
// LockFilePath is keyed by the id too. The lock protects a *repository*,
// and this milestone makes "one repository, two local names" a supported
// state — so two writes through the two names must contend, not both
// proceed on one working tree.
//
// Against name-keying this test passes vacuously in the wrong direction:
// both writes succeed, concurrently.
func TestTwoRegistrationsOfOneRepositoryShareOneLock(t *testing.T) {
	isolateXDG(t)
	path := initVaultForTest(t, "personal", "--device", "laptop-1")

	// A second registration of the same repository, under a second local
	// name — everything the first recorded, pointing at the same path.
	first := readGlobalConfigForTest(t).Vaults["personal"]
	g := readGlobalConfigForTest(t)
	g.Vaults["personal-alias"] = first
	if err := writeGlobalConfig(g); err != nil {
		t.Fatal(err)
	}

	lockA, err := gage.LockFilePath(vaultIDForTest(t, "personal"))
	if err != nil {
		t.Fatal(err)
	}
	lockB, err := gage.LockFilePath(vaultIDForTest(t, "personal-alias"))
	if err != nil {
		t.Fatal(err)
	}
	if lockA != lockB {
		t.Fatalf("the two registrations take different locks:\n  %s\n  %s", lockA, lockB)
	}

	// Held through one registration, contended through the other.
	if err := os.MkdirAll(filepath.Dir(lockA), 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := vaultlock.Acquire(lockA, 0)
	if err != nil {
		t.Fatalf("acquiring %q's lock: %v", "personal", err)
	}
	defer func() { _ = held.Release() }()
	_ = path

	res := runCLI(t, []string{"insert", "ProtonMail", "--use", "personal-alias"}, "hunter2\n")
	if res.Code == 0 {
		t.Fatal("a write through the second registration proceeded while the first held the lock; " +
			"both were writing the same working tree")
	}
	if res.Code != int(exitcode.Conflict) {
		t.Errorf("exit code = %d, want %d (conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
}

// TestAVaultWithoutAnIDFailsLoudlyOnEveryDerivedPath rules out a
// construction site in cmd/gage that was missed and only shows up on a
// path no test walks. The empty string is already refused by the path
// builders; this asserts it rather than trusting it.
func TestAVaultWithoutAnIDFailsLoudlyOnEveryDerivedPath(t *testing.T) {
	isolateXDG(t)
	path := initVaultForTest(t, "personal", "--device", "laptop-1")
	setGlobalVaultEntry(t, "personal", func(e *config.VaultEntry) { e.ID = "" })

	// The vault's own config still carries an id, so this is caught as a
	// mismatched registration before any path is built — which is the
	// loud failure, not a silent directory named "".
	for _, args := range [][]string{
		{"show", "ProtonMail"},
		{"insert", "Something"},
		{"identity", "list"},
	} {
		res := runCLI(t, args, "hunter2\n")
		if res.Code == 0 {
			t.Errorf("`gage %s` succeeded against a registration with no id:\n%s", strings.Join(args, " "), res.Stdout)
		}
	}

	// And with the vault itself unreadable there is no id from anywhere,
	// so the path builders themselves have to refuse.
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if _, err := gage.IdentityFilePath("", "laptop-1"); err == nil {
		t.Error("IdentityFilePath built a path from an empty vault id")
	}
	if _, err := gage.TrustCacheDir(""); err == nil {
		t.Error("TrustCacheDir built a path from an empty vault id")
	}
	if _, err := gage.LockFilePath(""); err == nil {
		t.Error("LockFilePath built a path from an empty vault id")
	}
}

// ---------------------------------------------------------------------
// removeOrphanedIdentity — the destructive path (Q-ORPHAN-BY-NAME)
// ---------------------------------------------------------------------

// keepIdentityPrompter answers every Confirm with no, which is what a
// scripted `vault remove` and a hesitant human both look like. It records
// what it was asked, since the prompt itself is the safety mechanism.
type keepIdentityPrompter struct {
	fakePrompter
	confirms []string
}

func (p *keepIdentityPrompter) Confirm(prompt string) (bool, error) {
	p.confirms = append(p.confirms, prompt)
	return false, nil
}

// deleteIdentityPrompter answers every Confirm with yes and records it.
type deleteIdentityPrompter struct {
	fakePrompter
	confirms []string
}

func (p *deleteIdentityPrompter) Confirm(prompt string) (bool, error) {
	p.confirms = append(p.confirms, prompt)
	return true, nil
}

// TestInitAndIdentityAddRecordThePubkeyAndCloneDoesNot: comparing keys
// instead of names needs the local key's public half recorded where it
// can be read without an unlock. `init` and `identity add` are the two
// moments gage holds that key; a clone holds none, and refuses to unlock
// to derive one, so it legitimately records nothing.
func TestInitAndIdentityAddRecordThePubkeyAndCloneDoesNot(t *testing.T) {
	t.Run("init", func(t *testing.T) {
		isolateXDG(t)
		initVaultForTest(t, "personal", "--device", "laptop-1")

		entry := readGlobalConfigForTest(t).Vaults["personal"]
		want := readVaultConfigForTest(t, "personal").Recipients[0].Pubkey
		if entry.Pubkey != want {
			t.Errorf("global config recorded pubkey %q, want this device's own key %q", entry.Pubkey, want)
		}
	})

	t.Run("clone", func(t *testing.T) {
		remote := publishedVault(t, "personal")
		if res := runCLI(t, []string{"clone", remote, "--name", "cloned"}, ""); res.Code != 0 {
			t.Fatalf("clone failed: %s", res.Stderr)
		}
		if got := readGlobalConfigForTest(t).Vaults["cloned"].Pubkey; got != "" {
			t.Errorf("clone recorded a pubkey (%q); it holds no identity to derive one from", got)
		}
	})

	t.Run("identity add", func(t *testing.T) {
		remote := publishedVault(t, "personal")
		if res := runCLI(t, []string{"clone", remote, "--name", "cloned", "--device", "phone-1"}, ""); res.Code != 0 {
			t.Fatalf("clone failed: %s", res.Stderr)
		}
		res := runCLI(t, []string{"identity", "add", "--use", "cloned", "--device", "phone-1"}, "")
		if res.Code != 0 {
			t.Fatalf("identity add failed: %s", res.Stderr)
		}

		entry := readGlobalConfigForTest(t).Vaults["cloned"]
		if entry.Pubkey == "" {
			t.Fatal("identity add recorded no pubkey")
		}
		if !strings.Contains(res.Stdout, entry.Pubkey) {
			t.Errorf("the recorded pubkey %q is not the one identity add printed:\n%s", entry.Pubkey, res.Stdout)
		}
	})
}

// TestVaultRemoveNeverDeletesAKeyBelongingToADifferentVault is the A20
// regression, constructed end to end. Before id-keying this sequence
// silently destroyed the only copy of A's private key: `vault remove` on
// B found A's identity file (they shared a name-keyed directory), read
// B's recipient list, did not find the device, and deleted it.
//
// Steps 2-3 are just "cloned the wrong repo, then removed it."
func TestVaultRemoveNeverDeletesAKeyBelongingToADifferentVault(t *testing.T) {
	remote := publishedVault(t, "vault-b")

	// A, under the local name "personal", with this device a recipient.
	initVaultForTest(t, "personal", "--device", "laptop-1")
	aIdentity := identityFileForTest(t, "personal", "laptop-1")
	if _, err := os.Stat(aIdentity); err != nil {
		t.Fatalf("A has no identity file: %v", err)
	}

	// Removing A keeps its key: laptop-1 is still a recipient there.
	if res := runCLI(t, []string{"vault", "remove", "personal"}, ""); res.Code != 0 {
		t.Fatalf("removing A: %s", res.Stderr)
	}
	if _, err := os.Stat(aIdentity); err != nil {
		t.Fatalf("removing A deleted its key while it was still a recipient: %v", err)
	}

	// B, cloned under A's freed-up name. This device is not a recipient
	// of B and holds no key for it.
	if res := runCLI(t, []string{"clone", remote,
		"--name", "personal", "--device", "laptop-1",
		"--dir", filepath.Join(t.TempDir(), "vault-b")}, ""); res.Code != 0 {
		t.Fatalf("cloning B: %s", res.Stderr)
	}

	// Removing B must not touch A's key.
	if res := runCLI(t, []string{"vault", "remove", "personal"}, ""); res.Code != 0 {
		t.Fatalf("removing B: %s", res.Stderr)
	}
	if _, err := os.Stat(aIdentity); err != nil {
		t.Errorf("removing vault B destroyed vault A's only private key: %v", err)
	}
}

// TestVaultRemoveKeepsARelabelledRecipientsKey is Q-ORPHAN-BY-NAME's
// false-delete case, and the one `recipient approve --device` makes
// routine in E4: the vault lists this device's key under a name other
// than the one local config records. The key matches, so it stays. A
// name comparison would delete an active recipient's only key.
func TestVaultRemoveKeepsARelabelledRecipientsKey(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")
	idPath := identityFileForTest(t, "personal", "laptop-1")

	// The vault relabels this device's key; the key itself is unchanged.
	raw := readRawVaultConfigForTest(t, "personal")
	relabelled := strings.Replace(raw, "'laptop-1'", "'laptop-1-work'", 1)
	if relabelled == raw {
		t.Fatalf("could not relabel the recipient in:\n%s", raw)
	}
	writeVaultConfigForTest(t, "personal", relabelled)

	res := runCLI(t, []string{"vault", "remove", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("vault remove deleted the key of a device that is still a recipient under another label: %v", err)
	}
	if !strings.Contains(res.Stderr, "still listed as a recipient") {
		t.Errorf("stderr = %q, want it to say the key is still a recipient", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "laptop-1-work") {
		t.Errorf("stderr = %q, want it to name the label the vault actually uses", res.Stderr)
	}
}

// TestVaultRemoveIdentifiesAStaleKeyWhoseNameNowLabelsAnother is the
// false-keep direction: `recipient remove laptop-1 --reencrypt`, then
// some other device added under that same label with a different key.
// The name matches and the key does not, so the local file is a genuine
// orphan — and a name comparison would keep it forever.
func TestVaultRemoveIdentifiesAStaleKeyWhoseNameNowLabelsAnother(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")
	idPath := identityFileForTest(t, "personal", "laptop-1")

	// Someone else's key takes over the label "laptop-1".
	raw := readRawVaultConfigForTest(t, "personal")
	mine := readVaultConfigForTest(t, "personal").Recipients[0].Pubkey
	theirs := newRecipientKey(t)
	writeVaultConfigForTest(t, "personal", strings.Replace(raw, mine, theirs, 1))

	p := &deleteIdentityPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"vault", "remove", "personal"}, "", true, p)
	if res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if len(p.confirms) != 1 {
		t.Fatalf("Confirm called %d times, want exactly 1", len(p.confirms))
	}
	if _, err := os.Stat(idPath); !os.IsNotExist(err) {
		t.Errorf("a genuinely orphaned key was kept because its old label still appears in the vault: %v", err)
	}
}

// TestVaultRemoveNeverDeletesWithoutConfirming is the load-bearing half
// of the fix. Comparing keys makes the *suggestion* accurate; a record
// that has gone stale would make it confidently wrong, which is worse
// than uncertain. The prompt has to name the path, because that is the
// whole content of the decision.
func TestVaultRemoveNeverDeletesWithoutConfirming(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")
	idPath := identityFileForTest(t, "personal", "laptop-1")
	orphanThisDevicesKey(t, "personal")

	p := &keepIdentityPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"vault", "remove", "personal"}, "", true, p)
	if res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if len(p.confirms) != 1 {
		t.Fatalf("Confirm called %d times, want exactly 1 before an irreversible delete", len(p.confirms))
	}
	if !strings.Contains(p.confirms[0], idPath) {
		t.Errorf("the confirmation doesn't name the file it would delete:\n%s", p.confirms[0])
	}
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("answering no still deleted the identity file: %v", err)
	}
}

// TestNonInteractiveVaultRemoveNeverDeletesAnIdentity holds without any
// special casing: Confirm defaults to no, so a scripted run keeps the
// file by construction rather than by a check somebody remembered.
func TestNonInteractiveVaultRemoveNeverDeletesAnIdentity(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")
	idPath := identityFileForTest(t, "personal", "laptop-1")
	orphanThisDevicesKey(t, "personal")

	// The real prompter, no terminal, empty stdin — a scripted run.
	var prompts strings.Builder
	real := newTerminalPrompter(strings.NewReader(""), &prompts)
	res, _ := runCLIWithPrompter(t, []string{"vault", "remove", "personal"}, "", false, real)
	if res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("a non-interactive vault remove deleted an identity file: %v", err)
	}
}

// TestVaultRemoveKeepsTheFileWhenNoPubkeyIsRecorded: an older
// registration, a hand-edited config, or a clone that never ran
// `identity add` leaves nothing to compare. gage says so and keeps the
// file rather than guessing — and a freshly cloned vault is exactly the
// case that lands here.
func TestVaultRemoveKeepsTheFileWhenNoPubkeyIsRecorded(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")
	idPath := identityFileForTest(t, "personal", "laptop-1")
	orphanThisDevicesKey(t, "personal")
	setGlobalVaultEntry(t, "personal", func(e *config.VaultEntry) { e.Pubkey = "" })

	p := &deleteIdentityPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"vault", "remove", "personal"}, "", true, p)
	if res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if len(p.confirms) != 0 {
		t.Errorf("gage asked whether to delete a key it cannot identify: %v", p.confirms)
	}
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("the identity file should have been kept: %v", err)
	}
	if !strings.Contains(res.Stderr, "doesn't know which public key") {
		t.Errorf("stderr = %q, want it to say gage cannot tell which key this device holds", res.Stderr)
	}
}

// TestVaultRemoveOnAPendingEnrollmentAsksBeforeDeleting is the state E3
// makes common: this device has a recorded pubkey that is not in the
// recipient list, because its enrollment request is still waiting for
// approval. That is indistinguishable from a genuine orphan by any check
// available here, so gage offers to delete — and the confirmation is the
// only thing between the user and a key whose approval is in flight.
func TestVaultRemoveOnAPendingEnrollmentAsksBeforeDeleting(t *testing.T) {
	isolateXDG(t)
	remote := publishedVault(t, "shared")
	if res := runCLI(t, []string{"clone", remote, "--name", "shared", "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("clone failed: %s", res.Stderr)
	}
	// `identity add` is the shape enrollment leaves behind: a local key,
	// recorded in global config, that the vault does not yet list.
	if res := runCLI(t, []string{"identity", "add", "--use", "shared", "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("identity add failed: %s", res.Stderr)
	}
	idPath := identityFileForTest(t, "shared", "phone-1")

	p := &keepIdentityPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"vault", "remove", "shared"}, "", true, p)
	if res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if len(p.confirms) != 1 {
		t.Fatalf("Confirm called %d times, want exactly 1", len(p.confirms))
	}
	if !strings.Contains(p.confirms[0], idPath) {
		t.Errorf("the confirmation doesn't name the file it would delete:\n%s", p.confirms[0])
	}
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("answering no still deleted a key whose approval is in flight: %v", err)
	}
}

// TestOneRepositoryRegisteredTwiceSharesOneIdentitiesDirectory is the
// intended consequence of id-keying, and the case name-keying used to
// hide. Removing either registration must not delete a key the other
// still needs — in both directions: the device that is still a recipient
// (kept by the pubkey comparison), and the device that holds a key but is
// not yet one (kept by the confirmation).
func TestOneRepositoryRegisteredTwiceSharesOneIdentitiesDirectory(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")

	g := readGlobalConfigForTest(t)
	g.Vaults["personal-alias"] = g.Vaults["personal"]
	if err := writeGlobalConfig(g); err != nil {
		t.Fatal(err)
	}

	if a, b := vaultIDForTest(t, "personal"), vaultIDForTest(t, "personal-alias"); a != b {
		t.Fatalf("two registrations of one repository got different ids (%s, %s)", a, b)
	}
	idPath := identityFileForTest(t, "personal", "laptop-1")

	t.Run("still a recipient", func(t *testing.T) {
		p := &deleteIdentityPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
		res, _ := runCLIWithPrompter(t, []string{"vault", "remove", "personal-alias"}, "", true, p)
		if res.Code != 0 {
			t.Fatalf("vault remove failed: %s", res.Stderr)
		}
		if len(p.confirms) != 0 {
			t.Errorf("gage offered to delete a key that is still a recipient: %v", p.confirms)
		}
		if _, err := os.Stat(idPath); err != nil {
			t.Errorf("removing one registration deleted the key the other still needs: %v", err)
		}
	})

	t.Run("holds a key but is not yet a recipient", func(t *testing.T) {
		// The enrollment-pending shape, reached by the other route.
		orphanThisDevicesKey(t, "personal")
		p := &keepIdentityPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
		res, _ := runCLIWithPrompter(t, []string{"vault", "remove", "personal"}, "", true, p)
		if res.Code != 0 {
			t.Fatalf("vault remove failed: %s", res.Stderr)
		}
		if len(p.confirms) != 1 {
			t.Fatalf("Confirm called %d times, want exactly 1", len(p.confirms))
		}
		if _, err := os.Stat(idPath); err != nil {
			t.Errorf("answering no deleted the key anyway: %v", err)
		}
	})
}

// TestVaultRemoveLeavesTheVaultsOwnFilesUntouched is the promise every
// case above runs under: `vault remove` forgets a local registration and
// never touches the store.
func TestVaultRemoveLeavesTheVaultsOwnFilesUntouched(t *testing.T) {
	isolateXDG(t)
	path := initVaultForTest(t, "personal", "--device", "laptop-1")
	if res := runCLI(t, []string{"insert", "ProtonMail"}, "hunter2\n"); res.Code != 0 {
		t.Fatalf("seeding an entry: %s", res.Stderr)
	}
	before := treeListing(t, path)
	orphanThisDevicesKey(t, "personal")

	p := &deleteIdentityPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	if res, _ := runCLIWithPrompter(t, []string{"vault", "remove", "personal"}, "", true, p); res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}

	if after := treeListing(t, path); !equalStrings(before, after) {
		t.Errorf("vault remove changed the vault's own files:\nbefore: %v\nafter:  %v", before, after)
	}
}

// orphanThisDevicesKey rewrites the vault's committed recipient list so
// this device's key is genuinely no longer in it — the state a
// `recipient remove` from another device leaves behind, produced here
// without needing that other device.
func orphanThisDevicesKey(t *testing.T, name string) {
	t.Helper()
	mine := readGlobalConfigForTest(t).Vaults[name].Pubkey
	if mine == "" {
		t.Fatalf("vault %q records no pubkey for this device; nothing to orphan", name)
	}
	replacement := newRecipientKey(t)
	writeVaultConfigForTest(t, name,
		strings.ReplaceAll(readRawVaultConfigForTest(t, name), mine, replacement))

	entry := readGlobalConfigForTest(t).Vaults[name]
	if err := os.WriteFile(filepath.Join(entry.Path, ".age-recipients"),
		[]byte(replacement+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// treeListing is every path under root, relative and sorted — enough to
// see that a command left a directory alone.
func treeListing(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
