package gage

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

// ---------------------------------------------------------------------
// M11 shared test support
//
// Move/Copy's whole point is that the destination need not be
// unlockable by this device, so these tests never route the destination
// through Vault.Unlock/CreateIdentity/global config at all — only
// through Create, for the on-disk vault itself, and wrapIdentity (from
// crypt_test.go) for a bare key to verify what landed there with. The
// source side needs a real Identity to hand Move/Copy, built the same
// way over its own bare key.
// ---------------------------------------------------------------------

// newMoveTestVaultPair builds two independent, freshly created vaults —
// different names, different recipients, no relation to each other other
// than sharing this test's $GAGE_STATE (isolateXDG) the way two vaults on
// one physical device share one lock/trust-cache root.
func newMoveTestVaultPair(t *testing.T, srcName, destName string) (src, dest *Vault, srcKey, destKey *age.X25519Identity) {
	t.Helper()
	isolateXDG(t)

	var err error
	srcKey, err = age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	destKey, err = age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	src, err = Create(CreateSpec{
		Name: srcName, ID: vaultIDForTest(srcName), Path: filepath.Join(t.TempDir(), srcName),
		Type: TypeGit, Method: MethodPassphrase, Device: "laptop-1",
		Recipients: []string{srcKey.Recipient().String()},
	})
	if err != nil {
		t.Fatal(err)
	}

	dest, err = Create(CreateSpec{
		Name: destName, ID: vaultIDForTest(destName), Path: filepath.Join(t.TempDir(), destName),
		Type: TypeGit, Method: MethodPassphrase, Device: "family-member",
		Recipients: []string{destKey.Recipient().String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return src, dest, srcKey, destKey
}

// wrapIdentityFor is crypt_test.go's wrapIdentity plus a device label and
// an optional Prompter — Move/Copy's trust-cache check and dirty-tree
// warnings need one to exercise the interesting paths (a decline, which
// vault a warning names).
func wrapIdentityFor(device string, ident *age.X25519Identity, p Prompter) *Identity {
	return &Identity{
		device: device,
		st: &identityState{
			secret:    []byte(ident.String()),
			ident:     ident,
			recipient: ident.Recipient().String(),
			prompter:  p,
			locker:    memLocker{},
		},
	}
}

// ---------------------------------------------------------------------
// Move / Copy core behavior
// ---------------------------------------------------------------------

func TestMoveRemovesFromSourceAndDecryptsAtDestination(t *testing.T) {
	src, dest, srcKey, destKey := newMoveTestVaultPair(t, "personal", "shared-family")
	srcIdent := wrapIdentityFor("laptop-1", srcKey, &fakePrompter{})

	e := sampleEntry(time.Now())
	e.Title = "family-wifi"
	srcID, err := src.Insert(e, false, srcIdent)
	if err != nil {
		t.Fatalf("seeding source entry: %v", err)
	}

	res, err := src.Move("family-wifi", dest, srcIdent)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if res.SourceID != srcID {
		t.Errorf("SourceID = %s, want %s", res.SourceID, srcID)
	}

	if _, err := src.ReadEntry(srcID, srcIdent); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("source entry still readable after Move: err = %v, want ErrEntryNotFound", err)
	}
	ids, err := src.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("source vault has %d entries after Move, want 0", len(ids))
	}

	destIdent := wrapIdentityFor("family-member", destKey, &fakePrompter{})
	got, err := dest.ReadEntry(res.DestID, destIdent)
	if err != nil {
		t.Fatalf("reading the moved entry at the destination: %v", err)
	}
	if got.Title != "family-wifi" || got.Value != e.Value {
		t.Errorf("destination entry = %+v, want title/value carried over from the source", got)
	}
	if got.Description != e.Description || len(got.Fields) != len(e.Fields) {
		t.Errorf("destination entry lost description/fields: %+v", got)
	}
}

func TestCopyLeavesSourceAndAddsDecryptableCopy(t *testing.T) {
	src, dest, srcKey, destKey := newMoveTestVaultPair(t, "personal", "shared-family")
	srcIdent := wrapIdentityFor("laptop-1", srcKey, &fakePrompter{})

	e := sampleEntry(time.Now())
	srcID, err := src.Insert(e, false, srcIdent)
	if err != nil {
		t.Fatalf("seeding source entry: %v", err)
	}

	res, err := src.Copy(e.Title, dest, srcIdent)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}

	original, err := src.ReadEntry(srcID, srcIdent)
	if err != nil {
		t.Fatalf("original entry gone from the source after Copy: %v", err)
	}
	if original.Title != e.Title || original.Value != e.Value {
		t.Errorf("source entry changed after Copy: %+v", original)
	}

	destIdent := wrapIdentityFor("family-member", destKey, &fakePrompter{})
	got, err := dest.ReadEntry(res.DestID, destIdent)
	if err != nil {
		t.Fatalf("reading the copy at the destination: %v", err)
	}
	if got.Title != e.Title || got.Value != e.Value {
		t.Errorf("destination copy = %+v, want title/value from the source", got)
	}
}

// TestMoveGivesTheDestinationCopyFreshProvenance is the "new UUID, new
// created" decision: the copy's id, created, and updated are all fresh,
// only the payload (title/description/value/fields) carries over, and
// updated_by names the device that performed the share (this device, as
// recorded in the *source* vault) rather than anything from the
// destination.
func TestMoveGivesTheDestinationCopyFreshProvenance(t *testing.T) {
	src, dest, srcKey, destKey := newMoveTestVaultPair(t, "personal", "shared-family")
	srcIdent := wrapIdentityFor("laptop-1", srcKey, &fakePrompter{})

	originalCreated := time.Now().Add(-30 * 24 * time.Hour)
	e := sampleEntry(originalCreated)
	e.UpdatedBy = "some-other-device"
	if _, err := src.Insert(e, false, srcIdent); err != nil {
		t.Fatalf("seeding source entry: %v", err)
	}

	// NewTimestamp truncates down to the second, so the fresh stamp can
	// legitimately read as up to a second earlier than the instant
	// captured right before the call; back this off rather than compare
	// against the untruncated "now" and risk a false failure on a slow
	// run that straddles a second boundary.
	before := time.Now().Add(-2 * time.Second)
	res, err := src.Move(e.Title, dest, srcIdent)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}

	if res.Entry.Created.Before(before) {
		t.Errorf("destination Created = %s, want a fresh stamp at/after %s (not the source's %s)",
			res.Entry.Created, before, originalCreated)
	}
	if res.Entry.Updated.Before(before) {
		t.Errorf("destination Updated = %s, want a fresh stamp at/after %s", res.Entry.Updated, before)
	}
	if res.Entry.UpdatedBy != "laptop-1" {
		t.Errorf("destination UpdatedBy = %q, want the acting device %q", res.Entry.UpdatedBy, "laptop-1")
	}
	if res.DestID == res.SourceID {
		t.Error("destination id equals source id; Move must assign a fresh UUID")
	}

	destIdent := wrapIdentityFor("family-member", destKey, &fakePrompter{})
	got, err := dest.ReadEntry(res.DestID, destIdent)
	if err != nil {
		t.Fatal(err)
	}
	if got.Created.Before(before) || got.UpdatedBy != "laptop-1" {
		t.Errorf("on-disk destination entry = %+v, want fresh provenance", got)
	}
}

// TestMoveRejectsTheSourceVaultAsItsOwnDestination is the "--to-vault
// naming the source vault itself is rejected" bullet.
func TestMoveRejectsTheSourceVaultAsItsOwnDestination(t *testing.T) {
	src, _, srcKey, _ := newMoveTestVaultPair(t, "personal", "shared-family")
	srcIdent := wrapIdentityFor("laptop-1", srcKey, &fakePrompter{})

	if _, err := src.Insert(sampleEntry(time.Now()), false, srcIdent); err != nil {
		t.Fatal(err)
	}

	_, err := src.Move("ProtonMail", src, srcIdent)
	if !errors.Is(err, ErrSameVault) {
		t.Errorf("error = %v, want it to wrap ErrSameVault", err)
	}
	if exitcode.CodeOf(err) != exitcode.Usage {
		t.Errorf("exit code = %v, want %v", exitcode.CodeOf(err), exitcode.Usage)
	}

	ids, err := src.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Errorf("entries after a rejected same-vault Move = %d, want 1 (untouched)", len(ids))
	}
}

// TestMoveAllowsATitleCollisionInTheDestination is the M11 plan's "Does
// the source entry's title collide in the destination?" decision: no
// check, and no -f — the destination ending up with two entries sharing
// a title is no different from what -f already permits within a single
// vault. Move/Copy write through Vault.WriteEntry directly rather than
// Vault.Insert, so titleExists's duplicate-title guard never runs against
// the destination at all.
func TestMoveAllowsATitleCollisionInTheDestination(t *testing.T) {
	src, dest, srcKey, destKey := newMoveTestVaultPair(t, "personal", "shared-family")
	srcIdent := wrapIdentityFor("laptop-1", srcKey, &fakePrompter{})
	destIdent := wrapIdentityFor("family-member", destKey, &fakePrompter{})

	e := sampleEntry(time.Now())
	if _, err := dest.Insert(e, false, destIdent); err != nil {
		t.Fatalf("seeding a same-titled entry directly into the destination: %v", err)
	}
	if _, err := src.Insert(e, false, srcIdent); err != nil {
		t.Fatalf("seeding source entry: %v", err)
	}

	res, err := src.Move(e.Title, dest, srcIdent)
	if err != nil {
		t.Fatalf("Move with a colliding title at the destination: %v", err)
	}

	ids, err := dest.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("destination entries after a colliding move = %d, want 2 (no dedup, no rejection)", len(ids))
	}
	titles := 0
	for _, id := range ids {
		got, err := dest.ReadEntry(id, destIdent)
		if err != nil {
			t.Fatal(err)
		}
		if got.Title == e.Title {
			titles++
		}
	}
	if titles != 2 {
		t.Errorf("entries titled %q in the destination = %d, want 2", e.Title, titles)
	}
	if res.Entry.Title != e.Title {
		t.Errorf("MoveResult.Entry.Title = %q, want %q", res.Entry.Title, e.Title)
	}
}

// ---------------------------------------------------------------------
// M10 trust cache: checked against the destination
// ---------------------------------------------------------------------

func TestMoveChecksTheDestinationsTrustCacheNotTheSources(t *testing.T) {
	src, dest, srcKey, _ := newMoveTestVaultPair(t, "personal", "shared-family")
	p := &trustPrompter{fakePrompter: fakePrompter{}, approve: true, confirm: true}
	srcIdent := wrapIdentityFor("laptop-1", srcKey, p)

	if _, err := src.Insert(sampleEntry(time.Now()), false, srcIdent); err != nil {
		t.Fatal(err)
	}

	// First move: the destination has no cache yet, so this bootstraps
	// one silently — no question asked.
	if _, err := src.Copy("ProtonMail", dest, srcIdent); err != nil {
		t.Fatalf("first Copy: %v", err)
	}
	if len(p.changes) != 0 {
		t.Fatalf("first use of an unseen destination asked a question: %d, want 0 (bootstrap is silent)", len(p.changes))
	}

	// Someone else adds a recipient to the destination directly (the way
	// a pulled commit from another device would arrive) — an unreviewed
	// change on the *destination*, never on the source.
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	commitRoutineRecipientChange(t, dest, "phone-1", other.Recipient().String())

	if _, err := src.Copy("ProtonMail", dest, srcIdent); err != nil {
		t.Fatalf("second Copy: %v", err)
	}
	if len(p.changes) != 1 {
		t.Fatalf("recipient-change question asked %d times, want exactly 1", len(p.changes))
	}
	if p.changes[0].Vault != dest.Name {
		t.Errorf("warning names vault %q, want the destination %q (not the source)", p.changes[0].Vault, dest.Name)
	}
}

func TestMoveDecliningTheTrustCheckAbortsTheWholeOperation(t *testing.T) {
	src, dest, srcKey, _ := newMoveTestVaultPair(t, "personal", "shared-family")
	bootstrap := &trustPrompter{fakePrompter: fakePrompter{}, approve: true, confirm: true}
	bootstrapIdent := wrapIdentityFor("laptop-1", srcKey, bootstrap)

	srcID, err := src.Insert(sampleEntry(time.Now()), false, bootstrapIdent)
	if err != nil {
		t.Fatal(err)
	}
	// Bootstrap the destination's cache first, so the change below is a
	// genuine, reviewable diff rather than a first use.
	if _, err := src.Copy("ProtonMail", dest, bootstrapIdent); err != nil {
		t.Fatalf("bootstrap Copy: %v", err)
	}

	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	commitRoutineRecipientChange(t, dest, "phone-1", other.Recipient().String())

	beforeSrcCommits := commitCount(t, src)
	beforeDestCommits := commitCount(t, dest)

	decline := &trustPrompter{fakePrompter: fakePrompter{}, approve: false, confirm: true}
	declineIdent := wrapIdentityFor("laptop-1", srcKey, decline)

	_, err = src.Move("ProtonMail", dest, declineIdent)
	if !errors.Is(err, ErrRecipientChangeDeclined) {
		t.Errorf("error = %v, want it to wrap ErrRecipientChangeDeclined", err)
	}
	if exitcode.CodeOf(err) != exitcode.Conflict {
		t.Errorf("exit code = %v, want %v", exitcode.CodeOf(err), exitcode.Conflict)
	}

	// Nothing written to the destination, and the source entry untouched.
	if got := commitCount(t, dest); got != beforeDestCommits {
		t.Errorf("destination commit count = %d after a declined move, want %d (nothing written)", got, beforeDestCommits)
	}
	if got := commitCount(t, src); got != beforeSrcCommits {
		t.Errorf("source commit count = %d after a declined move, want %d (source untouched)", got, beforeSrcCommits)
	}
	if _, err := src.ReadEntry(srcID, bootstrapIdent); err != nil {
		t.Errorf("source entry gone after a declined move: %v", err)
	}
}

// ---------------------------------------------------------------------
// Commits, one per vault
// ---------------------------------------------------------------------

func TestMoveProducesOneCommitInEachVault(t *testing.T) {
	src, dest, srcKey, _ := newMoveTestVaultPair(t, "personal", "shared-family")
	srcIdent := wrapIdentityFor("laptop-1", srcKey, &fakePrompter{})

	srcID, err := src.Insert(sampleEntry(time.Now()), false, srcIdent)
	if err != nil {
		t.Fatal(err)
	}
	beforeSrc := commitCount(t, src)
	beforeDest := commitCount(t, dest)

	res, err := src.Move("ProtonMail", dest, srcIdent)
	if err != nil {
		t.Fatal(err)
	}

	if got := commitCount(t, src); got != beforeSrc+1 {
		t.Errorf("source commit count = %d, want %d (exactly one deletion commit)", got, beforeSrc+1)
	}
	if got := commitCount(t, dest); got != beforeDest+1 {
		t.Errorf("destination commit count = %d, want %d (exactly one addition commit)", got, beforeDest+1)
	}

	srcMsg, _, _, err := gitrepo.HeadCommit(src.Path)
	if err != nil {
		t.Fatal(err)
	}
	if srcMsg != srcID.String() {
		t.Errorf("source HEAD message = %q, want the removed entry's id %q", srcMsg, srcID.String())
	}
	destMsg, _, _, err := gitrepo.HeadCommit(dest.Path)
	if err != nil {
		t.Fatal(err)
	}
	if destMsg != res.DestID.String() {
		t.Errorf("destination HEAD message = %q, want the new entry's id %q", destMsg, res.DestID.String())
	}
}

// ---------------------------------------------------------------------
// Crash safety: a duplicate, never a loss
// ---------------------------------------------------------------------

func TestMoveCrashBetweenTheTwoWritesLeavesARecoverableDuplicate(t *testing.T) {
	src, dest, srcKey, destKey := newMoveTestVaultPair(t, "personal", "shared-family")
	srcIdent := wrapIdentityFor("laptop-1", srcKey, &fakePrompter{})

	e := sampleEntry(time.Now())
	srcID, err := src.Insert(e, false, srcIdent)
	if err != nil {
		t.Fatal(err)
	}

	src.onMoveDestCommitted = func() { panic(errSimulatedCrash) }
	crashed := runAndRecoverCrash(t, func() {
		_, _ = src.Move(e.Title, dest, srcIdent)
	})
	src.onMoveDestCommitted = nil
	if !crashed {
		t.Fatal("the crash seam never fired; Move finished without being interrupted")
	}

	// Present in the source: the removal never ran.
	if _, err := src.ReadEntry(srcID, srcIdent); err != nil {
		t.Errorf("source entry missing after a crash between the two writes: %v", err)
	}
	// Present in the destination: its commit had already landed.
	destIdent := wrapIdentityFor("family-member", destKey, &fakePrompter{})
	ids, err := dest.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("destination entries after the crash = %d, want exactly 1 (the duplicate)", len(ids))
	}
	got, err := dest.ReadEntry(ids[0], destIdent)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != e.Title || got.Value != e.Value {
		t.Errorf("destination duplicate = %+v, want the moved entry", got)
	}
}

// TestMoveCrashBeforeTheDestinationCommitLeavesNothingWritten is the
// other half: a crash (or a declined trust check) that never reaches the
// destination commit leaves the source untouched and nothing at the
// destination — covered by TestMoveDecliningTheTrustCheckAbortsTheWholeOperation
// above via the trust-check refusal path, which is the only realistic
// place an interruption before that commit occurs (everything before it
// is pure decryption/comparison, nothing written yet).

// TestMoveRejectsTwoRegistrationsOfOneVaultAsSourceAndDestination is
// ErrSameVault's other shape, and it exists because A20 created it: one
// repository can now legitimately be registered twice under two local
// names, and those two registrations are the same vault however they are
// labelled. They share a working tree and — since the lock is keyed by
// the id — a single lock file, so a move between them would block on
// itself for the lock timeout instead of sharing anything.
//
// The refusal is by id rather than by name for exactly that reason.
func TestMoveRejectsTwoRegistrationsOfOneVaultAsSourceAndDestination(t *testing.T) {
	src, _, srcKey, _ := newMoveTestVaultPair(t, "personal", "shared-family")
	alias := &Vault{Name: "personal-alias", ID: src.ID, Path: src.Path}
	ident := wrapIdentityFor("laptop-1", srcKey, &fakePrompter{})

	e := sampleEntry(time.Now())
	e.Title = "family-wifi"
	if _, err := src.Insert(e, false, ident); err != nil {
		t.Fatalf("seeding source entry: %v", err)
	}

	start := time.Now()
	_, err := src.Move("family-wifi", alias, ident)
	if err == nil {
		t.Fatal("Move between two registrations of one vault was allowed")
	}
	if !errors.Is(err, ErrSameVault) {
		t.Errorf("error = %v, want it to wrap ErrSameVault", err)
	}
	// Refused rather than deadlocked: the guard has to run before the
	// locks, or this is a lock timeout wearing an error's clothes.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Move took %s to refuse; it blocked on its own lock instead of checking first", elapsed)
	}
}

// ---------------------------------------------------------------------
// Lock ordering: no deadlock between opposite-direction movers
// ---------------------------------------------------------------------

func TestTwoVaultLocksAreAcquiredInOneDeterministicOrder(t *testing.T) {
	isolateXDG(t)
	a := &Vault{Name: "aaa-vault", ID: vaultIDForTest("aaa-vault")}
	b := &Vault{Name: "zzz-vault", ID: vaultIDForTest("zzz-vault")}

	var (
		mu    sync.Mutex
		order []string
	)
	record := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}

	// Run many rounds of "a moving into b" concurrently with "b moving
	// into a." If the two ever acquired their locks in opposite orders,
	// this deadlocks and the test times out; go test's own -timeout is
	// what would catch that, so success here means every round
	// completed.
	const rounds = 50
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = withTwoVaultLocks(a, b, func() error { record("a->b"); return nil })
		}()
		go func() {
			defer wg.Done()
			_ = withTwoVaultLocks(b, a, func() error { record("b->a"); return nil })
		}()
	}
	wg.Wait()

	if len(order) != rounds*2 {
		t.Fatalf("completed %d locked sections, want %d", len(order), rounds*2)
	}
}

// TestTwoVaultLocksBothHeldForTheWholeOperation proves the locks aren't
// merely acquired-then-released one at a time: a third party trying to
// take *either* vault's lock while withTwoVaultLocks's fn is still
// running must find it contended.
func TestTwoVaultLocksBothHeldForTheWholeOperation(t *testing.T) {
	isolateXDG(t)
	a := &Vault{Name: "personal", ID: vaultIDForTest("personal")}
	b := &Vault{Name: "shared-family", ID: vaultIDForTest("shared-family")}

	inside := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- withTwoVaultLocks(a, b, func() error {
			close(inside)
			<-release
			return nil
		})
	}()

	<-inside
	if _, err := acquireVaultLock(a.ID, 0); err == nil {
		t.Error("acquired the source lock while withTwoVaultLocks still held it")
	}
	if _, err := acquireVaultLock(b.ID, 0); err == nil {
		t.Error("acquired the destination lock while withTwoVaultLocks still held it")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------
// Session index (M7): updated on both sides without a full rebuild
// ---------------------------------------------------------------------

// TestSessionIndexUpdatesBothVaultsWithoutARebuild is the M11 plan's "In
// a session where both vaults are unlocked, the source vault's metadata
// index drops the entry and the destination's gains it, without a full
// rebuild of either." Move itself is a plain library call — cmd/gage is
// what wires a session's NoteEntry/ForgetEntry to it, the same way it
// already does for insert/rm/rename (see noteIndexEntry/forgetIndexEntry
// in cmd/gage/entry.go) — so this test drives that same pair directly to
// prove the index machinery itself supports it.
func TestSessionIndexUpdatesBothVaultsWithoutARebuild(t *testing.T) {
	src, dest, srcKey, destKey := newMoveTestVaultPair(t, "personal", "shared-family")
	srcIdent := wrapIdentityFor("laptop-1", srcKey, &fakePrompter{})
	destIdent := wrapIdentityFor("family-member", destKey, &fakePrompter{})

	if _, err := src.Insert(sampleEntry(time.Now()), false, srcIdent); err != nil {
		t.Fatal(err)
	}
	if _, err := dest.Insert(sampleEntry(time.Now()), false, destIdent); err != nil {
		t.Fatal(err)
	}

	srcDecrypts, destDecrypts := 0, 0
	src.onDecrypt = func() { srcDecrypts++ }
	dest.onDecrypt = func() { destDecrypts++ }

	s := NewSession(SessionConfig{
		Open: func(name string) (*Vault, error) {
			t.Fatalf("Session tried to open %q itself; both vaults should already be held", name)
			return nil, nil
		},
		Prompter: &fakePrompter{},
		Current:  "personal",
	})
	// Both vaults "already unlocked" this session, bypassing Vault.Unlock
	// (heldVault is this package's own type) — Move/Copy never touch
	// Session at all, so what matters here is only that both sides are
	// already held, exactly as the test bullet requires.
	s.vaults["personal"] = &heldVault{vault: src, ident: srcIdent}
	s.vaults["shared-family"] = &heldVault{vault: dest, ident: destIdent}

	if _, err := s.List("personal"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List("shared-family"); err != nil {
		t.Fatal(err)
	}
	srcAfterBuild, destAfterBuild := srcDecrypts, destDecrypts
	if srcAfterBuild == 0 || destAfterBuild == 0 {
		t.Fatal("the initial List built no index on one side")
	}

	res, err := src.Move("ProtonMail", dest, srcIdent)
	if err != nil {
		t.Fatal(err)
	}
	// Move itself decrypts src fully — that's Vault.Resolve's own,
	// pre-existing full-decrypt-every-time contract (identical to what
	// Vault.Remove already does), nothing M11 changes. What must not
	// additionally decrypt anything is the *session's* index access
	// below, once NoteEntry/ForgetEntry have brought it up to date — so
	// the baseline for "no rebuild" is taken here, after Move, not
	// before it.
	srcAfterMove, destAfterMove := srcDecrypts, destDecrypts

	s.ForgetEntry("personal", res.SourceID)
	s.NoteEntry("shared-family", res.DestID, res.Entry)

	srcRows, err := s.List("personal")
	if err != nil {
		t.Fatal(err)
	}
	destRows, err := s.List("shared-family")
	if err != nil {
		t.Fatal(err)
	}

	if srcDecrypts != srcAfterMove {
		t.Errorf("source decrypt count went %d -> %d across a post-Move List; the session index was rebuilt instead of updated incrementally", srcAfterMove, srcDecrypts)
	}
	if destDecrypts != destAfterMove {
		t.Errorf("destination decrypt count went %d -> %d across a post-Move List; the session index was rebuilt instead of updated incrementally", destAfterMove, destDecrypts)
	}

	for _, r := range srcRows {
		if r.ID == res.SourceID {
			t.Errorf("source index still lists the moved entry %s", res.SourceID)
		}
	}
	found := false
	for _, r := range destRows {
		if r.ID == res.DestID {
			found = true
		}
	}
	if !found {
		t.Errorf("destination index does not list the moved entry %s", res.DestID)
	}
}
