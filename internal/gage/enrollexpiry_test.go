package gage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// Expiry is always enforced from the sealed copy. The filename's epoch is
// a housekeeping hint — it drives the listing's display and pruning, and
// nothing else — so the two can disagree and both directions are safe.

// TestExpiryComesFromTheSealNotTheFilename is the direction that would be
// unsafe if it were the other way round: a filename claiming a far-future
// epoch on a request whose seal has expired must not extend its life by a
// second.
func TestExpiryComesFromTheSealNotTheFilename(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	id := uuid.NewString()

	created := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	expires := created.Add(DefaultEnrollmentTTL) // a day ago
	p := goodPayload(v, id, "phone", joining, created, expires)

	// Filed under a name claiming it is good for another six days, which
	// is inside the ceiling — so the listing shows it and only the seal
	// can refuse it.
	code := generateCode(t)
	writePending(t, v, pendingFileName(id, time.Now().Add(6*24*time.Hour)), handSeal(t, p, code))

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("listing returned %+v, want the one request its filename claims is live", pending)
	}

	_, err = v.OpenEnrollment(pending, []string{code})
	assertEnrollmentError(t, err, ErrEnrollmentExpired, exitcode.Conflict)
}

// TestFilenameClaimingExpiryOnALiveSealGrantsNothing is the converse, and
// it is safe rather than merely tolerable: gage prunes a request that was
// still live, which is a denial of service but not a new one — anyone who
// can rename the file can equally delete it.
//
// What must not happen is the request being *approved* off a filename
// gage is not listing, so both halves are asserted: it is invisible to a
// broad run, and pruning removes it.
func TestFilenameClaimingExpiryOnALiveSealGrantsNothing(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	id := uuid.NewString()

	created := time.Now().UTC().Truncate(time.Second)
	expires := created.Add(DefaultEnrollmentTTL)
	code := generateCode(t)
	name := pendingFileName(id, time.Now().Add(-time.Hour))
	writePending(t, v, name, handSeal(t, goodPayload(v, id, "phone", joining, created, expires), code))

	opened, err := openAll(t, v, code)
	if len(opened) != 0 {
		t.Fatalf("a request whose filename says expired opened: %+v", opened)
	}
	assertEnrollmentError(t, err, ErrEnrollmentCodeWrong, exitcode.LockedOrAuth)

	removed, err := v.prunePendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != name {
		t.Fatalf("pruned %v, want %s", removed, name)
	}
}

// TestSealedExpiryBeyondTheCeilingIsExpired is the clamp applied to the
// copy approval actually enforces. Clamping the filename alone left the
// authoritative value unbounded: a hand-sealed payload can claim any
// expiry at all, and the ceiling is the statement of what gage could have
// produced.
//
// It is ErrEnrollmentExpired and not a malformed-payload error, because
// nothing about the file is ill-formed — the claim is simply outside what
// gage honours, which is the definition of expired. Minting a separate
// error would split one outcome ("ask for a fresh request") across two
// names.
func TestSealedExpiryBeyondTheCeilingIsExpired(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	id := uuid.NewString()

	created := time.Now().UTC().Truncate(time.Second)
	tenYears := created.Add(10 * 365 * 24 * time.Hour)
	code := generateCode(t)
	// Filed under an in-range epoch, so the filename cannot be what
	// catches it.
	writePending(t, v, pendingFileName(id, time.Now().Add(time.Hour)),
		handSeal(t, goodPayload(v, id, "phone", joining, created, tenYears), code))

	_, err := openAll(t, v, code)
	assertEnrollmentError(t, err, ErrEnrollmentExpired, exitcode.Conflict)
	if errors.Is(err, ErrEnrollmentMalformedRequest) {
		t.Error("a beyond-ceiling expiry was reported as a malformed payload; nothing about the file " +
			"is ill-formed, and a human would go looking for damage that does not exist")
	}
}

// TestExpiryIsComparedAgainstNowNotCreated is why the clamp needs no
// trust in `created`. Both timestamps are attacker-supplied, so hanging
// the check on one of them checks nothing: a payload claiming it was
// created ten years ago and expires nine years ago is internally
// consistent and still dead, and one claiming it expires ten years from
// now is internally consistent and still beyond what gage would write.
func TestExpiryIsComparedAgainstNowNotCreated(t *testing.T) {
	joining := testKeypair2(t)

	for _, tc := range []struct {
		name    string
		created time.Time
		expires time.Time
	}{
		{
			name:    "a forged past, consistent with itself",
			created: time.Now().Add(-10 * 365 * 24 * time.Hour),
			expires: time.Now().Add(-9 * 365 * 24 * time.Hour),
		},
		{
			name:    "a forged future, consistent with itself",
			created: time.Now().Add(-10 * 365 * 24 * time.Hour),
			expires: time.Now().Add(10 * 365 * 24 * time.Hour),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, _ := newRecipientTestVault(t, "personal", "laptop")
			id := uuid.NewString()
			code := generateCode(t)
			writePending(t, v, pendingFileName(id, time.Now().Add(time.Hour)),
				handSeal(t, goodPayload(v, id, "phone", joining, tc.created, tc.expires), code))

			_, err := openAll(t, v, code)
			assertEnrollmentError(t, err, ErrEnrollmentExpired, exitcode.Conflict)
		})
	}
}

// TestClockSkewIsItsOwnError separates the two failures whose fixes share
// nothing: fix that machine's clock and enroll again, versus ask for a
// fresh request. A request whose expiry preceded its own creation was
// dead before it was written, which is a wrong clock however carefully
// anyone waits.
func TestClockSkewIsItsOwnError(t *testing.T) {
	joining := testKeypair2(t)

	t.Run("expired before it was created", func(t *testing.T) {
		v, _ := newRecipientTestVault(t, "personal", "laptop")
		id := uuid.NewString()
		code := generateCode(t)
		created := time.Now().UTC().Truncate(time.Second)
		writePending(t, v, pendingFileName(id, time.Now().Add(time.Hour)),
			handSeal(t, goodPayload(v, id, "phone", joining, created, created.Add(-DefaultEnrollmentTTL)), code))

		_, err := openAll(t, v, code)
		assertEnrollmentError(t, err, ErrEnrollmentClockSkew, exitcode.Conflict)
		if errors.Is(err, ErrEnrollmentExpired) {
			t.Error("a skewed clock was reported as a stale request; the fixes share nothing")
		}
	})

	t.Run("merely sat too long", func(t *testing.T) {
		v, _ := newRecipientTestVault(t, "personal", "laptop")
		id := uuid.NewString()
		code := generateCode(t)
		created := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
		writePending(t, v, pendingFileName(id, time.Now().Add(time.Hour)),
			handSeal(t, goodPayload(v, id, "phone", joining, created, created.Add(DefaultEnrollmentTTL)), code))

		_, err := openAll(t, v, code)
		assertEnrollmentError(t, err, ErrEnrollmentExpired, exitcode.Conflict)
		if errors.Is(err, ErrEnrollmentClockSkew) {
			t.Error("a stale request was reported as a skewed clock; the fixes share nothing")
		}
	})
}

// TestForgedFilenameEpochIsPrunedAsExpired is what stops a hostile git
// writer parking <uuid>-99999999999.age in the tree permanently: its
// epoch never arrives, so without the clamp nothing would ever remove it.
//
// A legitimate request is created with an expiry at most now+7d, so at
// any later moment its epoch is at most 7d in the future. Only clock skew
// beyond roughly six days trips this, which is well outside what a
// working git remote tolerates anyway.
func TestForgedFilenameEpochIsPrunedAsExpired(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop")
	live := sealInto(t, v, "phone", d.pubkey, DefaultEnrollmentTTL)
	forged := uuid.NewString() + "-99999999999" + pendingFileExt
	writePending(t, v, forged, []byte("parked forever"))

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != live.ID {
		t.Fatalf("listing returned %+v, want only the live request %s", pending, live.ID)
	}

	removed, err := v.prunePendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != forged {
		t.Fatalf("pruned %v, want %s", removed, forged)
	}
	if _, err := os.Stat(filepath.Join(v.pendingDir(), pendingFileName(live.ID, live.Expires))); err != nil {
		t.Errorf("pruning removed the live request: %v", err)
	}
}
