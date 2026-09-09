package gage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// The shared fixtures every enrollment test builds on. They write into
// pending/ directly rather than going through a command, which is the
// point of E2: the primitives are proven before anything is layered on
// them, and there is no command to go through yet.

// testKeypair2 returns a fresh age public key, which is what a joining
// device would put in a request: a key this vault knows nothing about
// and is not yet encrypting to.
//
// Named for what it returns rather than reusing crypt_test.go's
// testKeypair, which hands back a parsed Recipient — enrollment carries
// the recipient as the string a human could paste, since that is what
// crosses the seal.
func testKeypair2(t *testing.T) string {
	t.Helper()
	ident, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generating a keypair: %v", err)
	}
	return ident.Recipient().String()
}

// registerAlso adds a vault entry to whatever global config the process
// is currently pointed at, leaving the existing entries alone.
//
// It exists for the wrong-vault message, which names the vault a request
// belongs to by looking its id up in this machine's registry. That lookup
// is only meaningful on a machine that has both vaults registered — which
// is precisely the situation the refusal is for: one person, two vaults,
// two codes on screen.
func registerAlso(t *testing.T, name, id string) {
	t.Helper()
	path := filepath.Join(configDirForTest(t), "config.toml")
	g, err := config.Read(path)
	if err != nil {
		t.Fatalf("reading global config: %v", err)
	}
	if g.Vaults == nil {
		g.Vaults = map[string]config.VaultEntry{}
	}
	g.Vaults[name] = config.VaultEntry{Path: filepath.Join(t.TempDir(), name), ID: id, Type: TypeGit}
	if err := config.Write(path, g); err != nil {
		t.Fatalf("writing global config: %v", err)
	}
}

// pendingPath returns the path a file with this name would have in the
// vault's pending/, creating the directory if it is not there yet.
//
// gage itself creates pending/ lazily on the first enroll, so a test
// fixture creates it the same way rather than assuming a vault has one.
func pendingPath(t *testing.T, v *Vault, name string) string {
	t.Helper()
	if err := os.MkdirAll(v.pendingDir(), 0o750); err != nil {
		t.Fatalf("creating pending/: %v", err)
	}
	return filepath.Join(v.pendingDir(), name)
}

// writePending drops raw bytes into pending/ under name — the move any
// git writer can make, and the one most of these tests are about.
func writePending(t *testing.T, v *Vault, name string, content []byte) string {
	t.Helper()
	path := pendingPath(t, v, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("writing pending/%s: %v", name, err)
	}
	return path
}

// sealInto seals a request for v and writes it into pending/ under the
// filename gage would give it, returning the request (code included).
func sealInto(t *testing.T, v *Vault, device, pubkey string, ttl time.Duration) EnrollmentRequest {
	t.Helper()
	req, sealed, err := v.sealEnrollment(device, pubkey, MethodPassphrase, ttl)
	if err != nil {
		t.Fatalf("sealing an enrollment request: %v", err)
	}
	writePending(t, v, pendingFileName(req.ID, req.Expires), sealed)
	return req
}

// handSeal seals a payload chosen field by field, which is what both a
// git writer and a legitimate code-holder can do. The code is the
// displayed form; it is normalized here so callers can pass either.
func handSeal(t *testing.T, p enrollmentPayload, code string) []byte {
	t.Helper()
	normalized, ok := normalizeEnrollmentCode(code)
	if !ok {
		t.Fatalf("test wants to seal under %q, which is not a valid code", code)
	}
	sealed, err := sealEnrollmentPayload(p, normalized)
	if err != nil {
		t.Fatalf("hand-sealing a payload: %v", err)
	}
	return sealed
}

// goodPayload is a payload for v that passes every check, so a test can
// spoil exactly one field and know the refusal came from that field.
func goodPayload(v *Vault, id, device, pubkey string, created, expires time.Time) enrollmentPayload {
	return enrollmentPayload{
		RequestID: id,
		VaultID:   v.ID,
		Device:    device,
		Pubkey:    pubkey,
		Method:    MethodPassphrase,
		Created:   created.UTC().Format(time.RFC3339),
		Expires:   expires.UTC().Format(time.RFC3339),
	}
}

// countDecryptions wires the decrypt-count seam and returns a function
// reporting how many scrypt attempts the open path has made. Several of
// enrollment's claims are about work *not* done, which no return value
// can show.
func countDecryptions(v *Vault) func() int {
	n := 0
	v.onEnrollmentDecrypt = func() { n++ }
	return func() int { return n }
}

// openAll is the broad run: every live request, one code.
func openAll(t *testing.T, v *Vault, code string) ([]OpenedRequest, error) {
	t.Helper()
	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatalf("listing pending requests: %v", err)
	}
	return v.OpenEnrollment(pending, []string{code})
}

// assertEnrollmentError checks both halves of the contract at once: the
// sentinel a caller matches on, and the exit code the taxonomy assigns
// it. The two are asserted together because a milestone that adds an
// error adds its code, and an error with the wrong code is as broken as
// the wrong error.
func assertEnrollmentError(t *testing.T, err error, want error, wantCode exitcode.Code) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if got := exitcode.CodeOf(err); got != wantCode {
		t.Errorf("exit code = %s, want %s", got, wantCode)
	}
}

// TestPendingEnrollmentsReportsZeroWithoutTheDirectory is the normal
// state, not an edge case: git tracks no empty directories, so every
// vault has no pending/ until the first enroll creates it — including
// every vault that predates this feature.
func TestPendingEnrollmentsReportsZeroWithoutTheDirectory(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")

	if _, err := os.Stat(v.pendingDir()); !os.IsNotExist(err) {
		t.Fatalf("a freshly created vault already has a pending/ directory; nothing should create it speculatively")
	}

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatalf("listing pending requests on a vault with no pending/: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("got %d pending requests, want 0", len(pending))
	}
}

// TestPendingEnrollmentsSkipsStrays is the ListIdentities posture applied
// to a directory that is, by design, writable by anyone who can write to
// the repository: a file gage did not write is skipped, and never fatal.
func TestPendingEnrollmentsSkipsStrays(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop")
	req := sealInto(t, v, "phone", d.pubkey, DefaultEnrollmentTTL)

	live := time.Now().Add(time.Hour)
	writePending(t, v, "README", []byte("not a request"))
	writePending(t, v, "notes.txt", []byte("also not a request"))
	writePending(t, v, uuid.NewString()+".age", []byte("no epoch in the name"))
	writePending(t, v, "not-a-uuid-"+pendingFileName("x", live)[2:], []byte("no uuid in the name"))
	writePending(t, v, uuid.NewString()+"-tomorrow.age", []byte("no epoch gage would write"))
	writePending(t, v, pendingFileName(uuid.NewString(), live)+".bak", []byte("an editor's leftover"))
	// A well-named file that is not an age file at all: skipped by the
	// *opener*, not the listing, so it still appears here.
	garbage := pendingFileName(uuid.NewString(), live)
	writePending(t, v, garbage, []byte("garbage, not ciphertext"))

	if err := os.MkdirAll(filepath.Join(v.pendingDir(), pendingFileName(uuid.NewString(), live)), 0o750); err != nil {
		t.Fatalf("creating a directory in pending/: %v", err)
	}

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatalf("listing over a directory full of strays: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("got %d pending requests, want 2 (the sealed one and the well-named garbage): %+v", len(pending), pending)
	}
	found := false
	for _, p := range pending {
		if p.ID == req.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("the real request %s is missing from a listing that survived the strays", req.ID)
	}
	if _, err := openAll(t, v, req.Code); err != nil {
		t.Errorf("opening with the right code across a directory of strays: %v", err)
	}
}

// TestPendingEnrollmentsFiltersExpiredWithoutDeleting is the split the
// whole expiry story rests on: a listing is a read, so it takes no lock
// and writes nothing.
//
// Both halves matter. Filtering is what makes "recipient pending never
// shows an expired request" and "the 32-request bound counts live
// requests" true without a write having come along first; not deleting is
// what keeps a read from needing the lock a delete would require.
func TestPendingEnrollmentsFiltersExpiredWithoutDeleting(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop")
	live := sealInto(t, v, "phone", d.pubkey, DefaultEnrollmentTTL)

	expired := pendingFileName(uuid.NewString(), time.Now().Add(-time.Minute))
	writePending(t, v, expired, []byte("expired by its own name"))
	beyond := pendingFileName(uuid.NewString(), time.Now().Add(MaxEnrollmentTTL+time.Hour))
	writePending(t, v, beyond, []byte("claiming an expiry gage would never write"))

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != live.ID {
		t.Fatalf("listing returned %+v, want only the live request %s", pending, live.ID)
	}

	for _, name := range []string{expired, beyond} {
		if _, err := os.Stat(filepath.Join(v.pendingDir(), name)); err != nil {
			t.Errorf("listing deleted %s; filtering is a read's job and deleting is pruning's: %v", name, err)
		}
	}
}

// TestPendingEnrollmentsReadsNoGitHistory is the property the whole
// filename scheme exists to buy. The alternative — deriving a request's
// age from the commit that introduced it — is a first-appearance search
// through history per request, which is O(history) and machinery gage has
// nowhere else.
//
// Proven by taking the repository away. A listing that consulted history
// in any form would fail without a .git directory; this one cannot tell
// the difference, which is a stronger claim than "it was fast".
func TestPendingEnrollmentsReadsNoGitHistory(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop")
	req := sealInto(t, v, "phone", d.pubkey, DefaultEnrollmentTTL)

	hidden := filepath.Join(filepath.Dir(v.Path), "git-moved-aside")
	if err := os.Rename(filepath.Join(v.Path, ".git"), hidden); err != nil {
		t.Fatalf("moving .git aside: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(hidden, filepath.Join(v.Path, ".git")) })

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatalf("listing without a .git directory: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != req.ID {
		t.Fatalf("listing returned %+v, want the one request %s", pending, req.ID)
	}
	if !pending[0].Expires.Equal(req.Expires.Truncate(time.Second)) {
		t.Errorf("listed expiry = %s, want %s — the filename is the only source there is",
			pending[0].Expires, req.Expires)
	}
}

// TestResolveEnrollmentMatchesTheUUIDPortionOnly is the hazard the base
// design's resolution order exists to prevent, in the form this filename
// scheme creates: ten digits of epoch are all legal hex, so a short id
// could match the *expiry* of one request and the *id* of another.
//
// Deny deletes. A resolver that searched whole filenames would delete a
// request the human did not name, and the thing they would have to notice
// is a UUID prefix.
func TestResolveEnrollmentMatchesTheUUIDPortionOnly(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")

	// A request whose id begins with the digits that also appear in the
	// other request's epoch.
	expires := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	epoch := pendingFileName("x", expires)
	digits := strings.TrimSuffix(strings.TrimPrefix(epoch, "x-"), pendingFileExt)
	needle := digits[:4]

	wanted := needle + uuid.NewString()[4:]
	if !canonicalUUID(wanted) {
		t.Fatalf("test fixture %q is not a canonical uuid; the epoch's leading digits are not hex", wanted)
	}
	writePending(t, v, pendingFileName(wanted, time.Now().Add(time.Hour)), []byte("the id match"))

	// And a request whose id contains those digits nowhere, but whose
	// epoch does.
	other := uuid.NewString()
	for strings.Contains(strings.ReplaceAll(other, "-", ""), needle) {
		other = uuid.NewString()
	}
	writePending(t, v, pendingFileName(other, expires), []byte("the epoch-only near-miss"))

	got, err := v.ResolveEnrollment(needle)
	if err != nil {
		t.Fatalf("resolving %q: %v", needle, err)
	}
	if got.ID != wanted {
		t.Errorf("resolved %q to %s, want %s — an id appearing only inside an epoch must match nothing",
			needle, got.ID, wanted)
	}
}

// TestResolveEnrollmentFollowsTheBaseResolutionOrder reuses M5's rule
// rather than reinventing one: exact beats substring, several matches
// come back as candidates rather than a guess, and no match is an error.
func TestResolveEnrollmentFollowsTheBaseResolutionOrder(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	live := time.Now().Add(time.Hour)

	// Two ids sharing a prefix, one of which is also spelled in full.
	exact := "aaaaaaaa-0000-4000-8000-000000000001"
	sibling := "aaaaaaaa-0000-4000-8000-000000000002"
	unrelated := "bbbbbbbb-0000-4000-8000-000000000003"
	for _, id := range []string{exact, sibling, unrelated} {
		writePending(t, v, pendingFileName(id, live), []byte("a request"))
	}

	t.Run("exact beats substring", func(t *testing.T) {
		// The whole 36 characters, which is also a substring of nothing
		// else — but the point is that the exact stage answers first.
		got, err := v.ResolveEnrollment(exact)
		if err != nil {
			t.Fatalf("resolving an exact id: %v", err)
		}
		if got.ID != exact {
			t.Errorf("resolved to %s, want %s", got.ID, exact)
		}
	})

	t.Run("a unique substring resolves", func(t *testing.T) {
		got, err := v.ResolveEnrollment("bbbb")
		if err != nil {
			t.Fatalf("resolving a unique substring: %v", err)
		}
		if got.ID != unrelated {
			t.Errorf("resolved to %s, want %s", got.ID, unrelated)
		}
	})

	t.Run("an ambiguous substring returns every match", func(t *testing.T) {
		_, err := v.ResolveEnrollment("aaaaaaaa")
		var amb *AmbiguousRequestError
		if !errors.As(err, &amb) {
			t.Fatalf("error = %v, want an *AmbiguousRequestError", err)
		}
		if got := exitcode.CodeOf(err); got != exitcode.Ambiguous {
			t.Errorf("exit code = %s, want %s", got, exitcode.Ambiguous)
		}
		if len(amb.Matches) != 2 {
			t.Fatalf("candidate list has %d entries, want 2 — a resolver that narrowed here would "+
				"delete a request the human did not name: %+v", len(amb.Matches), amb.Matches)
		}
		ids := map[string]bool{}
		for _, m := range amb.Matches {
			ids[m.ID] = true
		}
		if !ids[exact] || !ids[sibling] {
			t.Errorf("candidate list = %+v, want both %s and %s", amb.Matches, exact, sibling)
		}
	})

	t.Run("no match is an error", func(t *testing.T) {
		_, err := v.ResolveEnrollment("cccc")
		assertEnrollmentError(t, err, ErrEnrollmentNoSuchRequest, exitcode.NotFound)
	})

	t.Run("an expired request is not resolvable", func(t *testing.T) {
		dead := "cccccccc-0000-4000-8000-000000000004"
		writePending(t, v, pendingFileName(dead, time.Now().Add(-time.Hour)), []byte("expired"))
		_, err := v.ResolveEnrollment(dead)
		assertEnrollmentError(t, err, ErrEnrollmentNoSuchRequest, exitcode.NotFound)
	})
}

// TestPruneRemovesExpiredAndForgedExpiriesOnly is the housekeeping half
// of the expiry story, and the clamp is the part that is easy to leave
// out: without it a hostile git writer parks <uuid>-99999999999.age in
// the tree permanently, since its epoch never arrives.
//
// What pruning must *not* touch is asserted just as hard. gage never
// removes a file it cannot account for, so an unparseable name and an
// oversized file both survive — the conservative direction, and the same
// posture the parser and the listing already take.
func TestPruneRemovesExpiredAndForgedExpiriesOnly(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop")
	live := sealInto(t, v, "phone", d.pubkey, DefaultEnrollmentTTL)

	expired := pendingFileName(uuid.NewString(), time.Now().Add(-time.Minute))
	forged := pendingFileName(uuid.NewString(), time.Now().Add(MaxEnrollmentTTL+time.Hour))
	stray := "README"
	oversized := pendingFileName(uuid.NewString(), time.Now().Add(time.Hour))
	writePending(t, v, expired, []byte("expired"))
	writePending(t, v, forged, []byte("an epoch gage would never write"))
	writePending(t, v, stray, []byte("not something gage wrote"))
	writePending(t, v, oversized, make([]byte, maxPendingRequestBytes+1))

	removed, err := v.prunePendingEnrollments()
	if err != nil {
		t.Fatalf("pruning: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("pruned %v, want exactly the expired and the forged-expiry request", removed)
	}

	for _, name := range removed {
		if _, err := os.Stat(filepath.Join(v.pendingDir(), name)); !os.IsNotExist(err) {
			t.Errorf("%s was reported pruned but is still there", name)
		}
	}
	for _, name := range []string{
		pendingFileName(live.ID, live.Expires),
		stray,
		oversized,
	} {
		if _, err := os.Stat(filepath.Join(v.pendingDir(), name)); err != nil {
			t.Errorf("pruning removed %s, which it must leave alone: %v", name, err)
		}
	}
}

// TestPruneWithoutAPendingDirectoryIsNotAnError keeps pruning callable
// from every write path unconditionally. It rides writes that were
// happening anyway, and most vaults have no pending/ at all.
func TestPruneWithoutAPendingDirectoryIsNotAnError(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	removed, err := v.prunePendingEnrollments()
	if err != nil {
		t.Fatalf("pruning a vault with no pending/: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("pruned %v from a vault with no pending/", removed)
	}
}

// TestFilenameEpochMatchesTheSealedExpiry is what makes the listing's
// displayed expiry honest. The two copies are written from one value, and
// a drift between them would show one time and enforce another.
func TestFilenameEpochMatchesTheSealedExpiry(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop")
	req := sealInto(t, v, "phone", d.pubkey, 3*time.Hour)

	_, fromName, ok := parsePendingFileName(pendingFileName(req.ID, req.Expires))
	if !ok {
		t.Fatal("gage produced a pending filename its own parser rejects")
	}

	opened, err := openAll(t, v, req.Code)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if len(opened) != 1 {
		t.Fatalf("opened %d requests, want 1", len(opened))
	}
	if !opened[0].Expires.Equal(fromName) {
		t.Errorf("sealed expiry %s and filename epoch %s are different instants",
			opened[0].Expires, fromName)
	}
}
