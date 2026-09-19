package gage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/distillerylabs/gage/internal/gage/config"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/gittest"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
)

// ---------------------------------------------------------------------
// E3 shared test support
//
// A joining device is the one setup none of the earlier milestones
// needed: a real clone of a real vault, registered on a machine that
// holds no identity for it and is not one of its recipients. That is the
// only state `Enroll` is ever run in, so every test below starts from it
// rather than from a vault its own device can already read.
// ---------------------------------------------------------------------

// joiner is one machine that has cloned a vault it cannot read.
type joiner struct {
	vault *Vault
	// root is this machine's isolated XDG root — its own global config
	// and its own identities directory, independent of the vault owner's.
	root string
	name string
}

// newJoiningDevice clones remote into a fresh machine's data directory
// and registers it there the way `gage clone` does: path, id, type,
// device and method, and deliberately **no** pubkey. A clone holds no
// identity and refuses to unlock in order to derive one, so the
// already-a-recipient check legitimately has nothing to compare against
// until an enroll records a key.
func newJoiningDevice(t *testing.T, remote, vaultName, device string) joiner {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(t.TempDir(), vaultName)
	if err := gitrepo.Clone(context.Background(), remote, path); err != nil {
		t.Fatalf("cloning the vault as a joining device: %v", err)
	}
	vc, err := vaultconfig.Read(filepath.Join(path, ".gage", "config.toml"))
	if err != nil {
		t.Fatalf("reading the cloned vault's config: %v", err)
	}

	withXDGRoot(t, root, func() {
		writeGlobalEntry(t, vaultName, config.VaultEntry{
			Path:   path,
			ID:     vc.Vault.ID,
			Type:   TypeGit,
			Device: device,
			Method: MethodPassphrase,
		})
	})
	return joiner{vault: &Vault{Name: vaultName, ID: vc.Vault.ID, Path: path}, root: root, name: device}
}

// writeGlobalEntry records one vault in whatever global config the
// process is currently pointed at, replacing any entry under that name.
func writeGlobalEntry(t *testing.T, name string, entry config.VaultEntry) {
	t.Helper()
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	g, err := config.Read(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading global config: %v", err)
	}
	if g.Vaults == nil {
		g.Vaults = map[string]config.VaultEntry{}
	}
	g.Vaults[name] = entry
	if g.Current == "" {
		g.Current = name
	}
	if err := config.Write(path, g); err != nil {
		t.Fatalf("writing global config: %v", err)
	}
}

// recordPubkey is the local half `cmd/gage` performs after a successful
// `identity add` or `identity enroll`: this device's public key for this
// vault, in global config. The library only ever reads it.
func recordPubkey(t *testing.T, name, pubkey string) {
	t.Helper()
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	g, err := config.Read(path)
	if err != nil {
		t.Fatalf("reading global config: %v", err)
	}
	entry := g.Vaults[name]
	entry.Pubkey = pubkey
	g.Vaults[name] = entry
	if err := config.Write(path, g); err != nil {
		t.Fatalf("writing global config: %v", err)
	}
}

// enrollAs runs Enroll on j's machine, with j's XDG roots installed for
// the duration — which is what makes the identity file land in *this*
// device's data directory rather than the vault owner's.
func enrollAs(t *testing.T, j joiner, ttl time.Duration, p Prompter) (req EnrollmentRequest, err error) {
	t.Helper()
	withXDGRoot(t, j.root, func() {
		req, err = j.vault.Enroll(context.Background(), j.name, ttl, p)
	})
	return req, err
}

// pendingFiles lists the request filenames in a vault's pending/, or
// nothing at all when the directory has never been created.
func pendingFiles(t *testing.T, v *Vault) []string {
	t.Helper()
	des, err := os.ReadDir(v.pendingDir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading pending/: %v", err)
	}
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	return out
}

// identityFilesFor lists the wrapped identity files this machine holds
// for a vault — the assertion behind every "wrote no identity" bullet.
func identityFilesFor(t *testing.T, j joiner) []string {
	t.Helper()
	var out []string
	withXDGRoot(t, j.root, func() {
		dir, err := IdentitiesDir(j.vault.ID)
		if err != nil {
			t.Fatal(err)
		}
		des, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatalf("reading the identities directory: %v", err)
		}
		for _, de := range des {
			if strings.HasSuffix(de.Name(), identityFileExt) {
				out = append(out, de.Name())
			}
		}
	})
	return out
}

// TestEnrollCreatesAnIdentityOnADeviceThatHoldsNone is the create path:
// the same CreateIdentity call `identity add` makes, reached through a
// PurposeCreate exchange because there is nothing on disk to open.
func TestEnrollCreatesAnIdentityOnADeviceThatHoldsNone(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	req, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	if err != nil {
		t.Fatalf("Enroll on a device with no identity: %v", err)
	}
	if !req.Published {
		t.Error("Published = false, want true — this device is not a recipient, so there was work to do")
	}
	if len(p.requests) != 1 {
		t.Fatalf("the prompter was asked %d times, want 1", len(p.requests))
	}
	if got := p.requests[0].Purpose; got != PurposeCreate {
		t.Errorf("unlock purpose = %q, want %q — a device with no key chooses a new passphrase",
			got, PurposeCreate)
	}
	if files := identityFilesFor(t, j); len(files) != 1 {
		t.Errorf("identity files = %v, want exactly one", files)
	}
	if files := pendingFiles(t, j.vault); len(files) != 1 {
		t.Errorf("pending/ holds %v, want exactly the one request just published", files)
	}
}

// TestEnrollReusesAnExistingIdentityAndPromptsOnce is the second path,
// and the assertion is the *exchange* rather than the end state: reuse
// is a PurposeUnlock, asked once, against a file that already exists.
//
// The unlock is load-bearing rather than incidental — it is what
// guarantees the published public key matches the private key this
// device holds — so a cached public key that skipped it would pass an
// end-state test and still be wrong.
func TestEnrollReusesAnExistingIdentityAndPromptsOnce(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	var existing string
	withXDGRoot(t, j.root, func() {
		var err error
		existing, err = CreateIdentity(j.vault.ID, j.vault.Name, j.name,
			&fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("registering an identity before enrolling: %v", err)
		}
	})

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	req, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	if err != nil {
		t.Fatalf("Enroll on a device that already holds an identity: %v", err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("the prompter was asked %d times, want 1 — reuse opens a file, it does not confirm a new secret",
			len(p.requests))
	}
	if got := p.requests[0].Purpose; got != PurposeUnlock {
		t.Errorf("unlock purpose = %q, want %q", got, PurposeUnlock)
	}
	if files := identityFilesFor(t, j); len(files) != 1 {
		t.Errorf("identity files = %v, want the one that was already there and no second one", files)
	}
	if req.Pubkey != existing {
		t.Errorf("the request names %q, want the existing identity's key %q", req.Pubkey, existing)
	}
}

// TestEnrollWithAWrongPassphraseOnTheReusePathPublishesNothing is a
// failure the create path cannot produce at all: there is nothing to
// check a new passphrase against, so only reuse can be answered wrongly.
//
// The refusal lands at step 6 of D-ENROLL-REMOTE's order, which is why
// nothing is sealed, committed or pushed — the catch-up from step 4 has
// already happened and needs no undoing.
func TestEnrollWithAWrongPassphraseOnTheReusePathPublishesNothing(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	withXDGRoot(t, j.root, func() {
		if _, err := CreateIdentity(j.vault.ID, j.vault.Name, j.name,
			&fakePrompter{passphrases: []string{testPassphrase}}); err != nil {
			t.Fatalf("registering an identity before enrolling: %v", err)
		}
	})

	before := headHash(t, j.vault)
	fake := &fakeSyncer{}
	j.vault.remoteSyncer = fake

	// One wrong answer, then the prompter stops — which is how a library
	// with no retry policy of its own is told "that is enough".
	p := &fakePrompter{passphrases: []string{"not the passphrase"}}
	_, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	assertEnrollmentError(t, err, ErrWrongPassphrase, exitcode.LockedOrAuth)

	if files := pendingFiles(t, j.vault); len(files) != 0 {
		t.Errorf("a wrong passphrase still wrote %v into pending/", files)
	}
	if got := headHash(t, j.vault); got != before {
		t.Error("a wrong passphrase still committed something")
	}
	if fake.pushes != 0 {
		t.Errorf("pushed %d times after a wrong passphrase, want 0", fake.pushes)
	}
}

// TestEnrollSealsThePublicKeyThisDeviceActuallyHolds closes the loop the
// reuse path exists for: the key inside the seal is the one derived from
// the private key on this machine, not something recorded beside it.
func TestEnrollSealsThePublicKeyThisDeviceActuallyHolds(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	req, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// Derived independently, by opening the file this device holds.
	var derived string
	withXDGRoot(t, j.root, func() {
		var err error
		derived, err = CreateIdentity(j.vault.ID, j.vault.Name, j.name,
			&fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("re-opening the identity Enroll created: %v", err)
		}
	})
	if req.Pubkey != derived {
		t.Errorf("EnrollmentRequest.Pubkey = %q, want the identity file's own key %q", req.Pubkey, derived)
	}

	opened := openTheOnlyRequest(t, j.vault, req.Code)
	if opened.Pubkey != derived {
		t.Errorf("the sealed pubkey = %q, want %q", opened.Pubkey, derived)
	}
	if opened.Device != j.name {
		t.Errorf("the sealed device = %q, want %q", opened.Device, j.name)
	}
}

// openTheOnlyRequest opens the single pending request in a vault with
// the code that sealed it, which is what the approving side will do.
func openTheOnlyRequest(t *testing.T, v *Vault, code string) OpenedRequest {
	t.Helper()
	opened, err := openAll(t, v, code)
	if err != nil {
		t.Fatalf("opening the published request: %v", err)
	}
	if len(opened) != 1 {
		t.Fatalf("the code opened %d requests, want 1", len(opened))
	}
	return opened[0]
}

// TestEnrolledRequestCarriesThisVaultsID proves the field is populated
// from the vault being enrolled into rather than left zero. E2 proves
// the refusal; a request sealed with an empty vault_id fails that
// validation, so this is what catches enroll never setting it.
func TestEnrolledRequestCarriesThisVaultsID(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	req, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	// Opening succeeds only against the vault the request names, so a
	// zero or foreign vault_id fails here rather than silently later.
	if opened := openTheOnlyRequest(t, j.vault, req.Code); opened.ID != req.ID {
		t.Errorf("opened request id = %q, want %q", opened.ID, req.ID)
	}
}

// TestEnrollHonorsTheTTL: the sealed expiry and the filename epoch both
// move with it, and 24h is what a caller that says nothing gets.
func TestEnrollHonorsTheTTL(t *testing.T) {
	for _, tc := range []struct {
		name string
		ttl  time.Duration
		want time.Duration
	}{
		{"default", DefaultEnrollmentTTL, DefaultEnrollmentTTL},
		{"custom", 2 * time.Hour, 2 * time.Hour},
		{"ceiling", MaxEnrollmentTTL, MaxEnrollmentTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, owner, remote := newSyncVault(t, "personal", "laptop-1")
			_ = owner.Close()
			j := newJoiningDevice(t, remote, "personal", "phone-1")

			req, err := enrollAs(t, j, tc.ttl, &fakePrompter{passphrases: []string{testPassphrase}})
			if err != nil {
				t.Fatalf("Enroll: %v", err)
			}
			if got := req.Expires.Sub(req.Created); got != tc.want {
				t.Errorf("expires - created = %s, want %s", got, tc.want)
			}

			files := pendingFiles(t, j.vault)
			if len(files) != 1 {
				t.Fatalf("pending/ holds %v, want one request", files)
			}
			id, expires, ok := parsePendingFileName(files[0])
			if !ok {
				t.Fatalf("Enroll wrote %q, which is not a filename gage can parse", files[0])
			}
			if id != req.ID {
				t.Errorf("filename id = %q, want %q", id, req.ID)
			}
			if !expires.Equal(req.Expires.Truncate(time.Second)) {
				t.Errorf("filename epoch = %s, want the sealed expiry %s", expires, req.Expires)
			}

			opened := openTheOnlyRequest(t, j.vault, req.Code)
			if !opened.Expires.Equal(req.Expires) {
				t.Errorf("sealed expiry = %s, want %s", opened.Expires, req.Expires)
			}
		})
	}
}

// TestEnrollRefusesAnUnusableTTLBeforeAnythingHappens: E2 proves the
// library rejects the duration; this proves the rejection lands before a
// key is generated or anything is published, which is what makes the
// flag safe to get wrong.
func TestEnrollRefusesAnUnusableTTLBeforeAnythingHappens(t *testing.T) {
	for _, tc := range []struct {
		name string
		ttl  time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Hour},
		{"beyond the ceiling", MaxEnrollmentTTL + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, owner, remote := newSyncVault(t, "personal", "laptop-1")
			_ = owner.Close()
			j := newJoiningDevice(t, remote, "personal", "phone-1")

			p := &fakePrompter{passphrases: []string{testPassphrase}}
			_, err := enrollAs(t, j, tc.ttl, p)
			if err == nil {
				t.Fatal("Enroll accepted a TTL sealEnrollment refuses")
			}
			if got := exitcode.CodeOf(err); got != exitcode.Usage {
				t.Errorf("exit code = %s, want %s", got, exitcode.Usage)
			}
			if len(p.requests) != 0 {
				t.Errorf("the prompter was asked %d times, want 0", len(p.requests))
			}
			if files := identityFilesFor(t, j); len(files) != 0 {
				t.Errorf("a refused TTL still wrote %v", files)
			}
			if files := pendingFiles(t, j.vault); len(files) != 0 {
				t.Errorf("a refused TTL still published %v", files)
			}
		})
	}
}

// TestEnrollRefusesADeviceNameOutsideTheAllowlist matches AddIdentity's
// existing treatment rather than discovering it several steps later,
// once a key has been generated.
func TestEnrollRefusesADeviceNameOutsideTheAllowlist(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")
	j.name = "../etc/passwd"

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	_, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	if err == nil {
		t.Fatal("Enroll accepted a device name outside devicename.Valid's allowlist")
	}
	if got := exitcode.CodeOf(err); got != exitcode.Usage {
		t.Errorf("exit code = %s, want %s", got, exitcode.Usage)
	}
	if len(p.requests) != 0 {
		t.Errorf("the prompter was asked %d times, want 0", len(p.requests))
	}
	if files := pendingFiles(t, j.vault); len(files) != 0 {
		t.Errorf("a refused device name still published %v", files)
	}
}

// futureCommit publishes a commit to remote whose committer timestamp is
// ahead of this machine's clock — the only signal a joining device has
// that its own clock might be behind. Written through go-git directly
// because gittest's Commit stamps time.Now(), which is exactly the thing
// under test here.
func futureCommit(t *testing.T, remote string, ahead time.Duration) {
	t.Helper()
	d := gittest.NewDevice(t, remote)
	d.Write(t, "README", "a device from the future wrote this")
	d.CommitAt(t, "gage: a commit from a faster clock", time.Now().Add(ahead))
	d.Push(t)
}

// TestEnrollWarnsWhenThisMachinesClockIsBehindHead is the enroll-time
// half of the clock-skew story: warn and proceed, never refuse. A slow
// clock seals an expiry already in the past, and gage cannot detect that
// from its own clock — but a freshly fetched vault carries timestamps
// other devices wrote, and being behind them is the available signal.
func TestEnrollWarnsWhenThisMachinesClockIsBehindHead(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	futureCommit(t, remote, 48*time.Hour)
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	req, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	if err != nil {
		t.Fatalf("Enroll refused over a clock-skew heuristic; it must warn and proceed: %v", err)
	}
	if !req.Published {
		t.Error("Published = false; the warning must not have stopped the enroll")
	}
	if !anyWarningMentions(p.warnings, "clock") {
		t.Errorf("warnings = %v, want one naming this machine's clock", p.warnings)
	}
}

// TestEnrollIsSilentWhenTheClockLooksFine: a warning on every ordinary
// enroll would train people to ignore the one that matters.
func TestEnrollIsSilentWhenTheClockLooksFine(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	if _, err := enrollAs(t, j, DefaultEnrollmentTTL, p); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if anyWarningMentions(p.warnings, "clock") {
		t.Errorf("warnings = %v, want none about the clock on an ordinary enroll", p.warnings)
	}
}

func anyWarningMentions(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
