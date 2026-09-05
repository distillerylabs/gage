package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/gittest"
)

// initVaultWithRemote runs a real `gage init --remote` against a fresh
// bare repository, leaving the CLI in the state every sync test starts
// from: a vault that exists on both sides. It returns the vault's path
// and the remote.
//
// Nothing here pushes by hand — init publishing its own first commit is
// part of what "created with a remote" means, and is asserted directly by
// TestInitPublishesTheNewVault.
func initVaultWithRemote(t *testing.T, name string) (vaultPath, remote string) {
	t.Helper()

	remote = gittest.NewBareRemote(t)
	res := runCLI(t, []string{"init", name, "--remote", remote}, "")
	if res.Code != 0 {
		t.Fatalf("init exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	entry, ok := readGlobalConfigForTest(t).Vaults[name]
	if !ok {
		t.Fatalf("vault %q was not registered", name)
	}
	return entry.Path, remote
}

// TestInitPublishesTheNewVault: naming a remote at creation time means
// the vault is on it, not that it will be after some later write.
func TestInitPublishesTheNewVault(t *testing.T) {
	isolateXDG(t)

	remote := gittest.NewBareRemote(t)
	res := runCLI(t, []string{"init", "personal", "--remote", remote}, "")
	if res.Code != 0 {
		t.Fatalf("init exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "published") {
		t.Errorf("init said %q, want it to report publishing the vault", res.Stdout)
	}

	// A second device can clone it, which is only true if the initial
	// commit actually reached the remote.
	other := gittest.NewDevice(t, remote)
	if !other.Exists(t, ".age-recipients") {
		t.Error("init did not publish the new vault to its remote")
	}
}

// TestInitAgainstAMissingRepositorySaysToCreateIt: gage never creates a
// repository on a host for you, and the failure when one isn't there has
// to say that rather than surface a transport error.
func TestInitAgainstAMissingRepositorySaysToCreateIt(t *testing.T) {
	isolateXDG(t)

	missing := filepath.Join(t.TempDir(), "nothing-here.git")
	res := runCLI(t, []string{"init", "personal", "--remote", missing}, "")
	if res.Code == 0 {
		t.Fatalf("init against a nonexistent repository succeeded; stdout=%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "create an empty one") {
		t.Errorf("stderr = %q, want it to say to create the repository first", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "gage push") {
		t.Errorf("stderr = %q, want it to name `gage push` as the follow-up", res.Stderr)
	}

	// The local vault is real and keeps existing: it is durable
	// regardless of whether it could be published.
	entry, ok := readGlobalConfigForTest(t).Vaults["personal"]
	if !ok {
		t.Fatal("init discarded a vault it had already created locally")
	}
	if _, err := os.Stat(filepath.Join(entry.Path, ".age-recipients")); err != nil {
		t.Errorf("the locally created vault is missing: %v", err)
	}
}

func TestPushPublishesAndPullCatchesUp(t *testing.T) {
	isolateXDG(t)
	vaultPath, remote := initVaultWithRemote(t, "personal")

	// Another device publishes something this vault doesn't have.
	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "notes.txt", "from elsewhere", "another device's commit")

	res := runCLI(t, []string{"pull"}, "")
	if res.Code != 0 {
		t.Fatalf("pull exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "caught") {
		t.Errorf("pull said %q, want it to report catching up", res.Stdout)
	}
	if _, err := os.Stat(filepath.Join(vaultPath, "notes.txt")); err != nil {
		t.Errorf("pull did not bring the remote's file into the working tree: %v", err)
	}

	res = runCLI(t, []string{"push"}, "")
	if res.Code != 0 {
		t.Fatalf("push exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
}

// TestOneShotCommandPullsOnItsImplicitUnlock ties the automatic
// fast-forward to every vault unlock rather than to the `use` verb: this
// runs a plain one-shot read, with no session anywhere in sight.
func TestOneShotCommandPullsOnItsImplicitUnlock(t *testing.T) {
	isolateXDG(t)
	vaultPath, remote := initVaultWithRemote(t, "personal")

	if res := runCLI(t, []string{"insert", "ProtonMail"}, "hunter2\n"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	other := gittest.NewDevice(t, remote)
	other.Pull(t)
	other.WriteCommitPush(t, "notes.txt", "from elsewhere", "another device's commit")

	// `show` neither syncs by name nor knows anything about the remote —
	// but it unlocks, and unlocking is what pulls.
	res := runCLI(t, []string{"show", "ProtonMail"}, "")
	if res.Code != 0 {
		t.Fatalf("show exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(vaultPath, "notes.txt")); err != nil {
		t.Errorf("a one-shot command's implicit unlock did not fast-forward the vault: %v", err)
	}
}

// TestWriteReachesTheRemoteWithoutAnyoneAskingIt is the "invisible when
// nothing has diverged" half of the sync model.
func TestWriteReachesTheRemoteWithoutAnyoneAskingIt(t *testing.T) {
	isolateXDG(t)
	_, remote := initVaultWithRemote(t, "personal")

	res := runCLI(t, []string{"insert", "ProtonMail"}, "hunter2\n")
	if res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if strings.Contains(res.Stderr, "diverged") || strings.Contains(res.Stderr, "could not reach") {
		t.Errorf("a clean write produced sync friction on stderr: %q", res.Stderr)
	}

	other := gittest.NewDevice(t, remote)
	entries := listRemoteEntries(t, other)
	if len(entries) != 1 {
		t.Errorf("the remote has %d entries after one insert, want 1", len(entries))
	}
}

func TestPullReportsADivergenceRatherThanMergingIt(t *testing.T) {
	isolateXDG(t)
	vaultPath, remote := initVaultWithRemote(t, "personal")

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "notes.txt", "from elsewhere", "another device's commit")

	// A local commit that the remote doesn't have makes this a real
	// divergence rather than a fast-forward.
	if err := os.WriteFile(filepath.Join(vaultPath, "local.txt"), []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(vaultPath, "a local commit"); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"pull"}, "")
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("pull on a divergence exit code = %d, want %d (conflict); stderr=%s",
			res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "diverged") {
		t.Errorf("pull stderr = %q, want it to name the divergence", res.Stderr)
	}
	// Fast-forward-only means it didn't touch local state.
	if _, err := os.Stat(filepath.Join(vaultPath, "notes.txt")); !os.IsNotExist(err) {
		t.Error("pull merged the remote's file despite the divergence")
	}
}

// TestSyncMergesADisjointDivergenceWithoutPrompting is the case that
// resolves itself: two devices, two different entries.
func TestSyncMergesADisjointDivergenceWithoutPrompting(t *testing.T) {
	isolateXDG(t)
	vaultPath, remote := initVaultWithRemote(t, "personal")

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "notes.txt", "from elsewhere", "another device's commit")

	if err := os.WriteFile(filepath.Join(vaultPath, "local.txt"), []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(vaultPath, "a local commit"); err != nil {
		t.Fatal(err)
	}

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	res, _ := runCLIWithPrompter(t, []string{"sync"}, "", false, p)
	if res.Code != 0 {
		t.Fatalf("sync on a disjoint divergence exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(vaultPath, "notes.txt")); err != nil {
		t.Errorf("sync did not bring in the other device's file: %v", err)
	}

	// Sync unlocks lazily: nothing here had to be decrypted, so nothing
	// should have asked for a passphrase. This is the property M8b's
	// resolution relies on to be the *only* thing that forces an unlock.
	if len(p.requests) != 0 {
		t.Errorf("a mergeable sync prompted for an unlock %d time(s), want none", len(p.requests))
	}

	fresh := gittest.NewDevice(t, remote)
	if !fresh.Exists(t, "local.txt") || !fresh.Exists(t, "notes.txt") {
		t.Error("sync did not publish the merged result")
	}
}

// TestSyncReportsAConflictingDivergenceAndNamesTheVault is what a human
// gets in M8a when two devices genuinely changed the same thing:
// detection, classification, and somewhere to go — never a guess.
func TestSyncReportsAConflictingDivergenceAndNamesTheVault(t *testing.T) {
	isolateXDG(t)
	vaultPath, remote := initVaultWithRemote(t, "personal")

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "shared.txt", "their version", "their edit")

	if err := os.WriteFile(filepath.Join(vaultPath, "shared.txt"), []byte("my version"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(vaultPath, "my edit"); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"sync"}, "")
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("sync on a real conflict exit code = %d, want %d (conflict); stderr=%s",
			res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "shared.txt") {
		t.Errorf("sync stderr = %q, want it to name the conflicting path", res.Stderr)
	}
	if !strings.Contains(res.Stderr, vaultPath) {
		t.Errorf("sync stderr = %q, want it to name the vault's location", res.Stderr)
	}

	// Nothing was resolved.
	data, err := os.ReadFile(filepath.Join(vaultPath, "shared.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "my version" {
		t.Errorf("the local file was changed by a conflicting sync: %q", data)
	}
}

func TestSyncOnAVaultWithNoRemoteSaysSoAndSucceeds(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"sync"}, "")
	if res.Code != 0 {
		t.Fatalf("sync on a local-only vault exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	// Not "already in sync with origin": there is no origin, and naming
	// one that doesn't exist is how someone concludes their remote is
	// configured when it isn't.
	if !strings.Contains(res.Stdout, "local-only") {
		t.Errorf("sync said %q, want it to say the vault is local-only", res.Stdout)
	}
	if strings.Contains(res.Stdout, "origin") && !strings.Contains(res.Stdout, "no remote") {
		t.Errorf("sync said %q, want it not to claim a relationship with an origin", res.Stdout)
	}
}

// listRemoteEntries returns the entry files present on a device's clone.
func listRemoteEntries(t *testing.T, d *gittest.Device) []string {
	t.Helper()

	dirEntries, err := os.ReadDir(filepath.Join(d.Dir, "entries"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range dirEntries {
		out = append(out, e.Name())
	}
	return out
}

// TestOfflineExitsUnreachableAndDivergedExitsConflict is M8a's whole
// contract expressed as exit status rather than prose.
//
// The two are the same failed push and need different things from
// whoever is watching: one is worth retrying unchanged when the network
// comes back, the other needs a human. Sharing exit code 1 made that
// difference visible only to someone reading the message.
func TestOfflineExitsUnreachableAndDivergedExitsConflict(t *testing.T) {
	isolateXDG(t)
	gittest.InstallRecordingTransport(t, &net.OpError{
		Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused"),
	})

	// A vault whose remote is permanently unreachable. init says so and
	// still succeeds — the vault is durable locally either way.
	res := runCLI(t, []string{"init", "personal", "--remote", gittest.URL("git.example.com", "me/vault.git")}, "")
	if res.Code != 0 {
		t.Fatalf("init against an unreachable remote exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	res = runCLI(t, []string{"push"}, "")
	if res.Code != int(exitcode.Unreachable) {
		t.Errorf("offline push exit code = %d, want %d (unreachable); stderr=%s",
			res.Code, exitcode.Unreachable, res.Stderr)
	}
	if res.Code == int(exitcode.Conflict) {
		t.Error("offline push exits as a conflict, which is what a real divergence exits as")
	}
}

// TestPushNamesTheConflictingPathsJustLikeSync: a push is where a
// divergence is discovered, so it has to be at least as informative
// about one as `sync` is. Only `sync` listing the paths made `push` the
// worse way to learn about the identical situation.
func TestPushNamesTheConflictingPathsJustLikeSync(t *testing.T) {
	isolateXDG(t)
	vaultPath, remote := initVaultWithRemote(t, "personal")

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "shared.txt", "their version", "their edit")

	if err := os.WriteFile(filepath.Join(vaultPath, "shared.txt"), []byte("my version"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(vaultPath, "my edit"); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"push"}, "")
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("push on a real conflict exit code = %d, want %d; stderr=%s",
			res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "shared.txt") {
		t.Errorf("push stderr = %q, want it to name the conflicting path the way `sync` does", res.Stderr)
	}
}

// TestSyncRefusesOverUncommittedChanges: gage commits its own writes
// immediately, so uncommitted changes in a vault mean something outside
// gage is mid-edit. Merging on top of them would sweep half-typed work
// into an automatic merge commit and push it to every other device.
func TestSyncRefusesOverUncommittedChanges(t *testing.T) {
	isolateXDG(t)
	vaultPath, remote := initVaultWithRemote(t, "personal")

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "theirs.txt", "theirs", "their edit")

	if err := os.WriteFile(filepath.Join(vaultPath, "mine.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(vaultPath, "my edit"); err != nil {
		t.Fatal(err)
	}
	// Uncommitted, and written after the last commit so nothing swept it in.
	if err := os.WriteFile(filepath.Join(vaultPath, "scratch.txt"), []byte("half-typed"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"sync"}, "")
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("sync over a dirty tree exit code = %d, want %d (conflict); stderr=%s",
			res.Code, exitcode.Conflict, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "scratch.txt") {
		t.Errorf("stderr = %q, want it to name the uncommitted file", res.Stderr)
	}

	fresh := gittest.NewDevice(t, remote)
	if fresh.Exists(t, "scratch.txt") {
		t.Error("a half-typed local file was published by an automatic merge")
	}
}
