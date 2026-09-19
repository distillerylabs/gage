package gage

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
)

// TestEnrollmentScryptWorkFactorIsDeliberate pins the shipped enrollment
// factor the way TestScryptWorkFactorIsDeliberate pins the identity
// file's: a number chosen against a stated threat should fail a test when
// someone changes it rather than drift silently.
//
// It reads the *shipped* constant, not the live variable, for the same
// reason its identity-file twin does — TestMain lowers the live copy for
// this whole test binary, so checking the variable would validate the
// suite's speed hack instead of what gage ships.
//
// The two assertions after the value are what stop the number from being
// "simplified" into the identity file's: 14 is licensed by the
// enrollment code being 80 generated bits (D-ENROLL-SEAL-COST), and
// collapsing the two constants would recalibrate a security parameter as
// a side effect of a cleanup.
func TestEnrollmentScryptWorkFactorIsDeliberate(t *testing.T) {
	const want = 14
	if shippedEnrollmentScryptWorkFactor != want {
		t.Errorf("shippedEnrollmentScryptWorkFactor = %d, want %d — see D-ENROLL-SEAL-COST "+
			"before changing it; the low factor is licensed by the code being ~80 generated bits",
			shippedEnrollmentScryptWorkFactor, want)
	}
	if shippedEnrollmentScryptWorkFactor == shippedScryptWorkFactor {
		t.Errorf("shippedEnrollmentScryptWorkFactor == shippedScryptWorkFactor (%d): the two protect "+
			"different things and must not be collapsed into one constant", shippedScryptWorkFactor)
	}
	if scryptMaxWorkFactor < shippedEnrollmentScryptWorkFactor {
		t.Errorf("scryptMaxWorkFactor (%d) is below shippedEnrollmentScryptWorkFactor (%d): gage could not "+
			"open the requests it seals", scryptMaxWorkFactor, shippedEnrollmentScryptWorkFactor)
	}
}

// TestSealRoundTripsThroughItsOwnCode is the baseline every other test in
// this file narrows: a request gage sealed opens with the code gage
// returned, and the fields come back as they went in.
//
// It is deliberately not the interesting test. Round-tripping proves the
// seal is a container; what the feature actually rests on is that a wrong
// code opens nothing and a tampered blob opens nothing, which are the two
// below it.
func TestSealRoundTripsThroughItsOwnCode(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)

	req := sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)

	opened, err := openAll(t, v, req.Code)
	if err != nil {
		t.Fatalf("opening a request with its own code: %v", err)
	}
	if len(opened) != 1 {
		t.Fatalf("opened %d requests, want 1", len(opened))
	}
	got := opened[0]
	if got.ID != req.ID {
		t.Errorf("opened id = %s, want %s", got.ID, req.ID)
	}
	if got.Device != "phone" {
		t.Errorf("opened device = %q, want %q", got.Device, "phone")
	}
	if got.Pubkey != joining {
		t.Errorf("opened pubkey = %q, want %q", got.Pubkey, joining)
	}
	if got.Method != MethodPassphrase {
		t.Errorf("opened method = %q, want %q", got.Method, MethodPassphrase)
	}
	if !got.Created.Equal(req.Created) || !got.Expires.Equal(req.Expires) {
		t.Errorf("opened created/expires = %s/%s, want %s/%s",
			got.Created, got.Expires, req.Created, req.Expires)
	}
}

// TestWrongCodeOpensNothing is the property the seal is *for*. The
// plaintext is a public key, so confidentiality buys nothing; what a
// successful decryption proves is that whoever sealed the blob knew a
// code agreed out-of-band.
func TestWrongCodeOpensNothing(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	req := sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)

	wrong := generateCode(t)
	if wrong == req.Code {
		t.Fatal("generated the same code twice; this test proves nothing")
	}
	opened, err := openAll(t, v, wrong)
	if len(opened) != 0 {
		t.Errorf("a wrong code opened %d requests", len(opened))
	}
	assertEnrollmentError(t, err, ErrEnrollmentCodeWrong, exitcode.LockedOrAuth)
}

// TestRetypedCodesOpenTheSameRequest is D-ENROLL-CODE-FORMAT's
// normalization, asserted end to end rather than against the normalizer:
// what matters to a human retyping a code off paper is that the request
// opens, not that an internal function agreed with itself.
//
// The Crockford substitutions are the half that corrects rather than
// accepts — I and L for 1, O for 0 — so the fixture code contains both
// digits, which a randomly generated one might not.
func TestRetypedCodesOpenTheSameRequest(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)

	// A code with a 1 and a 0 in it, sealed by hand so the substitutions
	// have something to substitute for.
	const code = "GAGE-10AB-CDEF-GHJK-MNPQ"
	id := uuid.NewString()
	created := time.Now().UTC().Truncate(time.Second)
	expires := created.Add(DefaultEnrollmentTTL)
	sealed := handSeal(t, goodPayload(v, id, "phone", joining, created, expires), code)
	writePending(t, v, pendingFileName(id, expires), sealed)

	for _, tc := range []struct {
		name  string
		typed string
	}{
		{"as displayed", code},
		{"lowercase", "gage-10ab-cdef-ghjk-mnpq"},
		{"spaces for hyphens", "GAGE 10AB CDEF GHJK MNPQ"},
		{"no prefix", "10AB-CDEF-GHJK-MNPQ"},
		{"I typed for 1", "GAGE-I0AB-CDEF-GHJK-MNPQ"},
		{"L typed for 1", "GAGE-L0AB-CDEF-GHJK-MNPQ"},
		{"O typed for 0", "GAGE-1OAB-CDEF-GHJK-MNPQ"},
		{"lowercase l and o", "gage-lOab-cdef-ghjk-mnpq"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened, err := openAll(t, v, tc.typed)
			if err != nil {
				t.Fatalf("opening with %q: %v", tc.typed, err)
			}
			if len(opened) != 1 || opened[0].ID != id {
				t.Fatalf("opened %+v, want the one request %s", opened, id)
			}
		})
	}
}

// TestMalformedCodeIsRejectedBeforeAnyDecryption is what makes a typo
// cost nothing rather than one scrypt run per pending request.
//
// Asserted by counting decryptions, because the answer is deliberately
// identical either way: a malformed code and a well-formed one that opens
// nothing both return ErrEnrollmentCodeWrong, since cmd/gage's retry loop
// keys on that value and a typo is exactly what the loop is for. The
// validation buys cost, not a different answer — so cost is the only
// thing there is to assert.
func TestMalformedCodeIsRejectedBeforeAnyDecryption(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	for i := 0; i < 5; i++ {
		sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)
	}
	decryptions := countDecryptions(v)

	for _, typed := range []string{"", "GAGE-", "nope", "7K4M9QX2P3RH8WVU", "7K4M-9QX2-P3RH"} {
		_, err := openAll(t, v, typed)
		assertEnrollmentError(t, err, ErrEnrollmentCodeWrong, exitcode.LockedOrAuth)
	}
	if got := decryptions(); got != 0 {
		t.Errorf("a malformed code cost %d decryption attempts, want 0", got)
	}

	// A well-formed code that opens nothing does pay, which is the
	// comparison that makes the count above meaningful rather than an
	// artifact of nothing being pending.
	if _, err := openAll(t, v, generateCode(t)); err == nil {
		t.Fatal("a randomly generated code opened a request")
	}
	if got := decryptions(); got != 5 {
		t.Errorf("a well-formed wrong code cost %d attempts across 5 requests, want 5", got)
	}
}

// TestTamperedSealDoesNotOpen is the AEAD property the authentication
// claim rests on. Without it the seal would prove only that a blob was
// once written by a code-holder, not that these bytes are what they
// wrote — and a request's pubkey is the field an attacker would want to
// change.
func TestTamperedSealDoesNotOpen(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	req := sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)

	path := filepath.Join(v.pendingDir(), pendingFileName(req.ID, req.Expires))
	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Flip a bit in the payload rather than the header, so the file is
	// still a well-formed age file and the failure is authentication
	// rather than parsing.
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}

	opened, err := openAll(t, v, req.Code)
	if len(opened) != 0 {
		t.Fatalf("a tampered blob opened, yielding %+v", opened)
	}
	if err == nil {
		t.Fatal("a tampered blob opened without error")
	}
}

// TestHostileWorkFactorCostsABoundedWait is the first part of
// D-ENROLL-SEAL-COST, and the one whose absence is a hang rather than an
// error: **one** committed file claiming 2^30 is enough, no volume
// required, and the symptom is a process that appears to have stopped.
//
// The blob is hand-built by rewriting a real request's scrypt stanza,
// because producing one honestly would mean performing the 2^30 work this
// test exists to prove gage refuses. age checks the claimed factor before
// running the KDF, so the refusal is immediate.
//
// It runs the open in a goroutine and fails on the timeout rather than
// measuring afterwards: a regression here does not return late, it does
// not return.
func TestHostileWorkFactorCostsABoundedWait(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	req := sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)

	path := filepath.Join(v.pendingDir(), pendingFileName(req.ID, req.Expires))
	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hostile := claimWorkFactor(t, sealed, 30)
	if err := os.WriteFile(path, hostile, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := openAll(t, v, req.Code)
		done <- err
	}()
	select {
	case err := <-done:
		// The file did not open, which is the correct outcome: the claim
		// is refused, not honoured.
		assertEnrollmentError(t, err, ErrEnrollmentCodeWrong, exitcode.LockedOrAuth)
	case <-time.After(30 * time.Second):
		t.Fatal("opening a request claiming a work factor of 2^30 did not return within 30s: " +
			"the open path is not calling SetMaxWorkFactor, so a single hostile file hangs approval")
	}
}

// claimWorkFactor rewrites an age file's scrypt stanza to claim logN,
// leaving everything else alone. The header's MAC no longer covers it,
// which does not matter: age checks the claimed factor while unwrapping
// the stanza, before it ever verifies the MAC, so this is exactly the
// file a hostile git writer would commit.
func claimWorkFactor(t *testing.T, sealed []byte, logN int) []byte {
	t.Helper()
	lines := strings.Split(string(sealed), "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 4 && fields[0] == "->" && fields[1] == "scrypt" {
			lines[i] = strings.Join([]string{"->", "scrypt", fields[2], strconv.Itoa(logN)}, " ")
			return []byte(strings.Join(lines, "\n"))
		}
	}
	t.Fatalf("no scrypt stanza in the sealed request:\n%q", firstLines(string(sealed), 3))
	return nil
}

// TestShippedEnrollmentWorkFactorReachesARealAgeFile is the "reaches age"
// half of the pair: the constant being right is not the same claim as the
// constant being wired up. Dropping the SetWorkFactor call would leave
// every seal at age's default of 18 with every other test still green.
//
// It reads the factor straight off the blob's own scrypt stanza, which is
// where age records it, and it restores the shipped value for the one
// seal it makes — TestMain has lowered the live copy for the rest of the
// binary, so this is the only place the number gage actually ships is
// exercised.
func TestShippedEnrollmentWorkFactorReachesARealAgeFile(t *testing.T) {
	restore := SetEnrollmentWorkFactorForTests(shippedEnrollmentScryptWorkFactor)
	defer restore()

	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	req, sealed, err := v.sealEnrollment("phone", joining, MethodPassphrase, DefaultEnrollmentTTL)
	if err != nil {
		t.Fatalf("sealing at the shipped factor: %v", err)
	}

	got, ok := scryptStanzaWorkFactor(string(sealed))
	if !ok {
		t.Fatalf("no scrypt stanza in the sealed request:\n%q", firstLines(string(sealed), 3))
	}
	if got != shippedEnrollmentScryptWorkFactor {
		t.Errorf("age sealed at work factor %d, want the shipped %d", got, shippedEnrollmentScryptWorkFactor)
	}

	// And gage can open what it seals at that factor, on a real file
	// rather than by comparing two constants.
	writePending(t, v, pendingFileName(req.ID, req.Expires), sealed)
	if _, err := openAll(t, v, req.Code); err != nil {
		t.Errorf("opening a request sealed at the shipped factor %d: %v", shippedEnrollmentScryptWorkFactor, err)
	}
}

// TestEnrollmentAndIdentityWorkFactorsAreIndependent is what proves the
// two factors are two factors. It asserts both directions because the
// failure being ruled out is their being collapsed into one const later —
// which would recalibrate a security parameter through a test-only hook,
// invisibly: a suite that lowered the identity factor for speed would
// silently lower the seal's too, and D-ENROLL-SEAL-COST's whole
// calibration would be hidden behind it.
func TestEnrollmentAndIdentityWorkFactorsAreIndependent(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)

	sealFactor := func() int {
		t.Helper()
		_, sealed, err := v.sealEnrollment("phone", joining, MethodPassphrase, DefaultEnrollmentTTL)
		if err != nil {
			t.Fatalf("sealing: %v", err)
		}
		got, ok := scryptStanzaWorkFactor(string(sealed))
		if !ok {
			t.Fatal("no scrypt stanza in the sealed request")
		}
		return got
	}
	identityFactor := func() int {
		t.Helper()
		r, err := PassphraseRecipient("hunter2")
		if err != nil {
			t.Fatal(err)
		}
		ct, err := Encrypt([]byte("x"), r)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := scryptStanzaWorkFactor(string(ct))
		if !ok {
			t.Fatal("no scrypt stanza in the wrapped payload")
		}
		return got
	}

	t.Run("moving the identity factor leaves the seal alone", func(t *testing.T) {
		restoreEnrollment := SetEnrollmentWorkFactorForTests(11)
		defer restoreEnrollment()
		before := sealFactor()

		restoreIdentity := SetScryptWorkFactorForTests(13)
		defer restoreIdentity()
		if after := sealFactor(); after != before {
			t.Errorf("sealed at work factor %d after the identity factor moved, want %d unchanged — "+
				"enrollment is reading scryptWorkFactor", after, before)
		}
		if got := identityFactor(); got != 13 {
			t.Errorf("identity factor = %d, want 13; the hook did not take effect and this test proves nothing", got)
		}
	})

	t.Run("moving the seal factor leaves the identity file alone", func(t *testing.T) {
		restoreIdentity := SetScryptWorkFactorForTests(13)
		defer restoreIdentity()
		before := identityFactor()

		restoreEnrollment := SetEnrollmentWorkFactorForTests(11)
		defer restoreEnrollment()
		if after := identityFactor(); after != before {
			t.Errorf("identity file wrapped at work factor %d after the enrollment factor moved, "+
				"want %d unchanged — the two are one variable", after, before)
		}
		if got := sealFactor(); got != 11 {
			t.Errorf("seal factor = %d, want 11; the hook did not take effect and this test proves nothing", got)
		}
	})
}

// TestWrongCodeAgainstAFullDirectoryIsFast is the case D-ENROLL-SEAL-COST
// chose factor 14 for: a mistyped character has to try every request
// before it can conclude it opens nothing, and cmd/gage retries on
// exactly that error. At 19 with a full directory that is over a minute
// before the retry prompt comes back.
//
// This is the one wall-clock assertion in the milestone, so the ceiling is
// deliberately generous — it exists to catch a ~32x regression (someone
// "hardening" the seal back to 19), not to measure anything. A tight bound
// here would buy nothing and cost a flaky suite on the slowest CI runner.
//
// It restores the shipped factor for its own duration, since that is the
// number the claim is about; the rest of the binary keeps TestMain's.
func TestWrongCodeAgainstAFullDirectoryIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("seals maxEnrollmentAttempts requests at the shipped work factor")
	}
	restore := SetEnrollmentWorkFactorForTests(shippedEnrollmentScryptWorkFactor)
	defer restore()

	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	for i := 0; i < maxEnrollmentAttempts; i++ {
		sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)
	}
	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != maxEnrollmentAttempts {
		t.Fatalf("built %d pending requests, want %d", len(pending), maxEnrollmentAttempts)
	}

	const ceiling = 30 * time.Second
	start := time.Now()
	if _, err := v.OpenEnrollment(pending, []string{generateCode(t)}); err == nil {
		t.Fatal("a randomly generated code opened one of the sealed requests")
	}
	if elapsed := time.Since(start); elapsed > ceiling {
		t.Errorf("a wrong code against %d pending requests took %s, over the %s ceiling — "+
			"the seal work factor has been raised back toward the identity file's",
			maxEnrollmentAttempts, elapsed, ceiling)
	}
}

// TestTooManyPendingIsRefusedBeforeAnyDecryption is the bound's other
// half: a directory any git writer can fill multiplies the work of every
// code-trying run, and an unbounded KDF loop degrades as a hang rather
// than an error.
//
// The refusal has to name the way through, because it is a detour rather
// than a wall — and the count assertion is what proves the bound runs
// before the work rather than after it.
func TestTooManyPendingIsRefusedBeforeAnyDecryption(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	live := time.Now().Add(time.Hour)
	for i := 0; i < maxEnrollmentAttempts+1; i++ {
		writePending(t, v, pendingFileName(uuid.NewString(), live), []byte("a request"))
	}
	decryptions := countDecryptions(v)

	_, err := openAll(t, v, generateCode(t))
	assertEnrollmentError(t, err, ErrEnrollmentTooManyPending, exitcode.Conflict)
	if got := decryptions(); got != 0 {
		t.Errorf("refusing an over-full directory cost %d decryption attempts, want 0", got)
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("the refusal does not name the id form as the way through: %v", err)
	}
}

// TestTheBoundCountsLiveRequestsOnly keeps ordinary neglect from looking
// like an attack. Expired requests are filtered by the listing, which is
// a read — so this holds whether or not a write has come along to prune
// them, which is the whole reason filtering and pruning are separate.
func TestTheBoundCountsLiveRequestsOnly(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)

	// Well past the bound in total, most of it long dead.
	for i := 0; i < 40; i++ {
		writePending(t, v, pendingFileName(uuid.NewString(), time.Now().Add(-time.Duration(i+1)*time.Hour)), []byte("expired"))
	}
	req := sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)
	for i := 0; i < 3; i++ {
		writePending(t, v, pendingFileName(uuid.NewString(), time.Now().Add(time.Hour)), []byte("live but unopenable"))
	}

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) > maxEnrollmentAttempts {
		t.Fatalf("listing returned %d requests over a directory of 44, want at most %d — "+
			"expired files are consuming the budget", len(pending), maxEnrollmentAttempts)
	}

	// And a broad run over it proceeds normally, with no write having
	// pruned anything first.
	opened, err := v.OpenEnrollment(pending, []string{req.Code})
	if err != nil {
		t.Fatalf("a broad open over a mostly-expired directory: %v", err)
	}
	if len(opened) != 1 || opened[0].ID != req.ID {
		t.Fatalf("opened %+v, want the one live request %s", opened, req.ID)
	}
	if _, err := os.Stat(v.pendingDir()); err != nil {
		t.Fatalf("pending/ is gone: %v", err)
	}
	names, err := os.ReadDir(v.pendingDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 44 {
		t.Errorf("pending/ holds %d files after a read-only run, want all 44 — opening must delete nothing", len(names))
	}
}

// TestIDScopedRunPassesTheBoundByConstruction is what makes
// ErrEnrollmentTooManyPending a detour rather than a wall. Note there is
// no flag involved: the broad run and the scoped run are one code path
// over different inputs, which is exactly why neither caller can opt out
// of the bound or forget to apply it.
func TestIDScopedRunPassesTheBoundByConstruction(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	req := sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)
	live := time.Now().Add(time.Hour)
	for i := 0; i < maxEnrollmentAttempts*2; i++ {
		writePending(t, v, pendingFileName(uuid.NewString(), live), []byte("a request"))
	}

	// The broad run is refused, which is the situation being escaped.
	if _, err := openAll(t, v, req.Code); !errors.Is(err, ErrEnrollmentTooManyPending) {
		t.Fatalf("a broad run over %d requests returned %v, want ErrEnrollmentTooManyPending",
			maxEnrollmentAttempts*2+1, err)
	}

	scoped, err := v.ResolveEnrollment(req.ID)
	if err != nil {
		t.Fatalf("resolving %s in a stuffed directory: %v", req.ID, err)
	}
	decryptions := countDecryptions(v)
	opened, err := v.OpenEnrollment([]PendingRequest{scoped}, []string{req.Code})
	if err != nil {
		t.Fatalf("an id-scoped run: %v", err)
	}
	if len(opened) != 1 || opened[0].ID != req.ID {
		t.Fatalf("opened %+v, want %s", opened, req.ID)
	}
	if got := decryptions(); got != 1 {
		t.Errorf("an id-scoped run with one code cost %d decryption attempts, want 1", got)
	}
}

// TestScopeIsHonouredNotTreatedAsAHint closes the shortcut that would
// pass every other bullet here: reading the directory inside
// OpenEnrollment and treating the argument as advisory. That version
// would still open the right request in every test above, and would
// silently reintroduce the unbounded path the scope parameter exists to
// remove.
func TestScopeIsHonouredNotTreatedAsAHint(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	wanted := sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)
	other := sealInto(t, v, "tablet", joining, DefaultEnrollmentTTL)

	scoped, err := v.ResolveEnrollment(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := v.OpenEnrollment([]PendingRequest{scoped}, []string{wanted.Code})
	if len(opened) != 0 {
		t.Fatalf("a run scoped to %s opened %+v; the scope was widened", other.ID, opened)
	}
	assertEnrollmentError(t, err, ErrEnrollmentCodeWrong, exitcode.LockedOrAuth)
}

// TestSealedIDMustMatchTheFilename is what stops a blob being renamed
// onto a different slot or replayed into a fresh one. The filename is
// unauthenticated — anyone with git write access can rename a file —
// while the copy under the seal is not.
//
// The epoch half is deliberately excluded from the comparison, and that
// exclusion is asserted rather than left implicit: holding it to the seal
// would refuse requests over a lie that grants nothing, and both
// directions of filename/seal disagreement are already safe.
func TestSealedIDMustMatchTheFilename(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	code := generateCode(t)
	sealedID := uuid.NewString()
	created := time.Now().UTC().Truncate(time.Second)
	expires := created.Add(DefaultEnrollmentTTL)
	sealed := handSeal(t, goodPayload(v, sealedID, "phone", joining, created, expires), code)

	t.Run("a different uuid is a mismatch", func(t *testing.T) {
		v, _ := newRecipientTestVault(t, "personal", "laptop")
		writePending(t, v, pendingFileName(uuid.NewString(), expires), sealed)
		_, err := openAll(t, v, code)
		assertEnrollmentError(t, err, ErrEnrollmentIDMismatch, exitcode.Conflict)
	})

	t.Run("a different epoch is not", func(t *testing.T) {
		v, _ := newRecipientTestVault(t, "personal", "laptop")
		// The same request, filed under an epoch two hours out from the
		// sealed one — still live, so the only thing that could refuse it
		// is a comparison that should not be happening.
		writePending(t, v, pendingFileName(sealedID, expires.Add(-2*time.Hour)), sealed)
		opened, err := openAll(t, v, code)
		if err != nil {
			t.Fatalf("opening a request whose filename epoch differs from its sealed expiry: %v", err)
		}
		if len(opened) != 1 || !opened[0].Expires.Equal(expires) {
			t.Fatalf("opened %+v, want one request expiring at %s — expiry comes from the seal", opened, expires)
		}
	})
}

// TestGeneratedCodeIsReturnedAndNowhereElse is the code-hygiene claim,
// scoped to the generating side: enroll generates the code and never
// takes one as input, so there is no line for a file to record it from.
//
// The approving side is deliberately not covered, and cannot be from here
// — E2 builds no commands. `recipient approve --code GAGE-…` puts a code
// on a command line, which a session records verbatim; that is an
// accepted risk with its own register entry, not something this test can
// speak to.
func TestGeneratedCodeIsReturnedAndNowhereElse(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	req := sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)

	if req.Code == "" {
		t.Fatal("sealing returned no code; there is nothing for a caller to display")
	}
	normalized, ok := normalizeEnrollmentCode(req.Code)
	if !ok {
		t.Fatal("sealing returned a code its own validation rejects")
	}

	roots := []string{v.Path}
	if dir, err := xdgpaths.DataDir(); err == nil {
		roots = append(roots, dir)
	}
	if dir, err := xdgpaths.ConfigDir(); err == nil {
		roots = append(roots, dir)
	}
	if dir, err := xdgpaths.StateDir(); err == nil {
		roots = append(roots, dir)
	}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable path is not this test's subject
			}
			content, err := os.ReadFile(path) // #nosec G304 -- a test fixture tree
			if err != nil {
				return nil //nolint:nilerr
			}
			if bytes.Contains(content, []byte(req.Code)) || bytes.Contains(content, []byte(normalized)) {
				t.Errorf("the enrollment code appears in %s; the library must return it and persist it nowhere", path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
}
