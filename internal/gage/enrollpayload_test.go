package gage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// The sealed payload is untrusted input. Authenticated is not the same as
// well-formed: the seal proves the bytes are unaltered since sealing, not
// that the fields mean anything, and two people can supply a malformed
// one with no attack at all — a git writer can drop a file into pending/,
// and anyone legitimately holding a code can seal whatever they like.
//
// So every payload here is hand-sealed with a known code and one field
// spoiled, and every one of them must be refused as malformed rather than
// as a wrong code: the code worked.

// spoiled seals p under a fresh code and files it, returning the code.
func spoiled(t *testing.T, v *Vault, id string, expires time.Time, p enrollmentPayload) string {
	t.Helper()
	code := generateCode(t)
	writePending(t, v, pendingFileName(id, expires), handSeal(t, p, code))
	return code
}

func TestMalformedPayloadsAreRefusedAsMalformed(t *testing.T) {
	joining := testKeypair2(t)
	created := time.Now().UTC().Truncate(time.Second)
	expires := created.Add(DefaultEnrollmentTTL)

	for _, tc := range []struct {
		name   string
		spoil  func(v *Vault, p *enrollmentPayload)
		reason string
	}{
		{
			name:   "a device name outside the allowlist",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.Device = "../../../../etc/cron.d/x" },
			reason: "it would be a path component, and a filename in config.toml, before anyone reviewed it",
		},
		{
			name:   "a device name carrying ANSI escapes",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.Device = "phone\x1b[2K\x1b[1GApprove? [Y/n] " },
			reason: "a name that can move the cursor can rewrite the question the human is answering",
		},
		{
			name:   "a device name carrying a newline",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.Device = "phone\nthis is a second line" },
			reason: "the name is printed directly above the prompt that grants vault-wide access",
		},
		{
			name:   "an empty device name",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.Device = "" },
			reason: "gage never writes one",
		},
		{
			name:   "a pubkey that is not an age recipient",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.Pubkey = "not-a-key" },
			reason: "it is shown to the approver before AddRecipient would ever see it",
		},
		{
			name: "a pubkey that is an age *private* key",
			spoil: func(_ *Vault, p *enrollmentPayload) {
				p.Pubkey = "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
			},
			reason: "a private key is not a recipient",
		},
		{
			name:   "a method outside the allowlist",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.Method = "yubikey" },
			reason: "the same treatment --type and --method already get",
		},
		{
			name:   "an empty method",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.Method = "" },
			reason: "an absent field is not a default",
		},
		{
			name:   "a request id that is not a uuid",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.RequestID = "request-1" },
			reason: "it is compared against the filename's uuid portion",
		},
		{
			name:   "a created that is not RFC 3339",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.Created = "yesterday" },
			reason: "gage writes RFC 3339",
		},
		{
			name:   "an expires that is not RFC 3339",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.Expires = "soon" },
			reason: "gage writes RFC 3339",
		},
		{
			name:   "an absent vault id",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.VaultID = "" },
			reason: "that one genuinely is a payload gage could not have produced",
		},
		{
			name:   "a vault id that is not a uuid",
			spoil:  func(_ *Vault, p *enrollmentPayload) { p.VaultID = "personal" },
			reason: "likewise; it is a mismatch only once it is an id at all",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, _ := newRecipientTestVault(t, "personal", "laptop")
			id := uuid.NewString()
			p := goodPayload(v, id, "phone", joining, created, expires)
			tc.spoil(v, &p)
			code := spoiled(t, v, id, expires, p)

			_, err := openAll(t, v, code)
			assertEnrollmentError(t, err, ErrEnrollmentMalformedRequest, exitcode.Conflict)
			// Never a wrong code. cmd/gage's retry loop keys on that
			// value and would sit re-prompting for a correct code against
			// a file no code can ever make valid.
			if errors.Is(err, ErrEnrollmentCodeWrong) {
				t.Errorf("a malformed payload was reported as a wrong code (%s): %v", tc.reason, err)
			}
		})
	}
}

// TestMalformedPayloadDoesNotPoisonTheRun keeps a refusal from being a
// denial of service. Anyone who can write to the repository can drop one
// bad file into pending/; if that took the whole directory down, approval
// would be unavailable until a human found and deleted it — the same
// outcome D-ENROLL-SEAL-COST's bound exists to prevent by another route.
func TestMalformedPayloadDoesNotPoisonTheRun(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	created := time.Now().UTC().Truncate(time.Second)
	expires := created.Add(DefaultEnrollmentTTL)

	bad := uuid.NewString()
	p := goodPayload(v, bad, "phone", joining, created, expires)
	p.Device = "\x1b[2Kmalicious"
	badCode := spoiled(t, v, bad, expires, p)

	good := sealInto(t, v, "tablet", joining, DefaultEnrollmentTTL)

	opened, err := openAll(t, v, good.Code)
	if err != nil {
		t.Fatalf("opening the good request alongside a malformed one: %v", err)
	}
	if len(opened) != 1 || opened[0].ID != good.ID {
		t.Fatalf("opened %+v, want the good request %s", opened, good.ID)
	}

	// And the bad one is still refused on its own terms, so it was
	// skipped rather than quietly accepted.
	if _, err := openAll(t, v, badCode); !errors.Is(err, ErrEnrollmentMalformedRequest) {
		t.Errorf("the malformed request opened with its own code: %v", err)
	}
}

// A request sealed for one vault must not open in another. The
// request_id comparison stops a blob being replayed onto a different
// *slot*; nothing stopped it being replayed into a different *vault*, and
// that case needs no attacker at all — one person, two vaults, two codes
// on screen, and `approve --code` run against whichever is current.

// TestRequestSealedForAnotherVaultIsRefused constructs the mistake the
// way it actually happens: a file copy. The blob is committed and
// readable to everyone who can read the vault it was sealed for.
func TestRequestSealedForAnotherVaultIsRefused(t *testing.T) {
	work, workDevice := newRecipientTestVault(t, "work", "laptop")
	personal, _ := newRecipientTestVault(t, "personal", "desktop")
	if work.ID == personal.ID {
		t.Fatal("the two test vaults share an id; this test proves nothing")
	}

	// The approver's machine has both vaults registered, which is the
	// situation the refusal is for and what lets its message name the
	// vault the request is actually for.
	registerAlso(t, "work", work.ID)

	req := sealInto(t, work, "phone", workDevice.pubkey, DefaultEnrollmentTTL)
	sealed, err := os.ReadFile(filepath.Join(work.pendingDir(), pendingFileName(req.ID, req.Expires)))
	if err != nil {
		t.Fatal(err)
	}
	writePending(t, personal, pendingFileName(req.ID, req.Expires), sealed)

	_, err = openAll(t, personal, req.Code)
	assertEnrollmentError(t, err, ErrEnrollmentWrongVault, exitcode.Conflict)

	// Three errors, three different next actions — so it is none of the
	// other two, asserted rather than assumed.
	if errors.Is(err, ErrEnrollmentCodeWrong) {
		t.Error("a wrong-vault request was reported as a wrong code; a retry loop would re-prompt " +
			"for a code that is already correct")
	}
	if errors.Is(err, ErrEnrollmentMalformedRequest) {
		t.Error("a wrong-vault request was reported as malformed; the payload is well-formed and gage " +
			"wrote it, so a human would go looking for damage that does not exist")
	}

	// The id is opaque, so the message names the vault the request is
	// *for* — which is the whole content of the mistake.
	if !strings.Contains(err.Error(), `"work"`) {
		t.Errorf("the refusal does not name the vault the request belongs to: %v", err)
	}

	// And the request still opens where it belongs, which is the
	// regression this check could cause.
	opened, err := openAll(t, work, req.Code)
	if err != nil {
		t.Fatalf("the request no longer opens in its own vault: %v", err)
	}
	if len(opened) != 1 || opened[0].ID != req.ID {
		t.Fatalf("opened %+v in the vault the request is for, want %s", opened, req.ID)
	}
}

// The size bounds. D-ENROLL-SEAL-COST bounds the count of requests and
// the work factor each claims; neither bounds how large a file a git
// writer puts in pending/, which is the cheapest of the four bounds and
// the only one that runs before anything is read at all.

// TestOversizedPendingFileIsNeverOpened asserts "never opened" rather
// than "opened and rejected", which is why it counts decryptions: the
// claim is about work not done, and a correct answer arrived at
// expensively looks exactly like a correct answer.
func TestOversizedPendingFileIsNeverOpened(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	req := sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)

	huge := pendingFileName(uuid.NewString(), time.Now().Add(time.Hour))
	writePending(t, v, huge, make([]byte, 20<<20))

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != req.ID {
		t.Fatalf("listing returned %+v, want only the real request %s", pending, req.ID)
	}

	decryptions := countDecryptions(v)
	opened, err := v.OpenEnrollment(pending, []string{req.Code})
	if err != nil {
		t.Fatalf("an oversized file poisoned the run: %v", err)
	}
	if len(opened) != 1 || opened[0].ID != req.ID {
		t.Fatalf("opened %+v, want the good request %s", opened, req.ID)
	}
	if got := decryptions(); got != 1 {
		t.Errorf("the run cost %d decryption attempts, want 1 — the oversized file was opened", got)
	}

	// Skipped, never deleted: an oversized file is by definition not
	// something gage wrote, and the parser's posture toward strays is the
	// one to match.
	if _, err := os.Stat(filepath.Join(v.pendingDir(), huge)); err != nil {
		t.Errorf("the oversized file was deleted: %v", err)
	}
}

// TestOversizedFileIsSkippedEvenWhenHandedInDirectly closes the route
// around the listing. OpenEnrollment is handed a scope rather than
// reading the directory itself, so the bound has to live on the open path
// too — otherwise a caller that built a PendingRequest another way would
// bypass a check the listing performs.
func TestOversizedFileIsSkippedEvenWhenHandedInDirectly(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	req, sealed, err := v.sealEnrollment("phone", joining, MethodPassphrase, DefaultEnrollmentTTL)
	if err != nil {
		t.Fatal(err)
	}
	// A real, openable request, padded past the bound. age ignores
	// trailing bytes, so without the check this would open.
	padded := append(append([]byte(nil), sealed...), make([]byte, maxPendingRequestBytes)...)
	name := pendingFileName(req.ID, req.Expires)
	path := writePending(t, v, name, padded)

	decryptions := countDecryptions(v)
	opened, err := v.OpenEnrollment([]PendingRequest{{ID: req.ID, Path: path, Expires: req.Expires}}, []string{req.Code})
	if len(opened) != 0 {
		t.Fatalf("an oversized file handed in directly opened: %+v", opened)
	}
	assertEnrollmentError(t, err, ErrEnrollmentCodeWrong, exitcode.LockedOrAuth)
	if got := decryptions(); got != 0 {
		t.Errorf("the oversized file cost %d decryption attempts, want 0", got)
	}
}

// TestDecryptedPayloadIsBoundedToo is the fourth bound, and the one the
// ciphertext bound does not conceptually cover: the party who can produce
// a large payload is a code-holder — trusted enough to be granted access,
// not trusted enough to be handed an unbounded allocation.
//
// It exercises the open path directly rather than through a file, because
// today the two bounds are the same number and age does not compress, so
// a multi-megabyte payload is caught by the file-size check first and the
// plaintext bound would never be reached through pending/. Asserting it
// against the function that owns it is what keeps the bound real rather
// than incidental to age's current framing.
func TestDecryptedPayloadIsBoundedToo(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	code := generateCode(t)
	id := uuid.NewString()
	created := time.Now().UTC().Truncate(time.Second)
	expires := created.Add(DefaultEnrollmentTTL)

	p := goodPayload(v, id, "phone", joining, created, expires)
	// Multi-megabyte, in a field the payload legitimately has.
	p.Pubkey = joining + strings.Repeat("a", 4<<20)
	sealed := handSeal(t, p, code)
	if len(sealed) <= maxPendingRequestBytes {
		t.Fatalf("the fixture ciphertext is %d bytes, which the file-size bound would not even notice", len(sealed))
	}

	normalized, _ := normalizeEnrollmentCode(code)
	req := PendingRequest{ID: id, Path: "unused", Expires: expires}
	out, didOpen, err := v.openSealedRequest(req, sealed, normalized, time.Now())
	if !didOpen {
		t.Fatal("the correct code did not open the request; this test is no longer about the payload bound")
	}
	if out != (OpenedRequest{}) {
		t.Errorf("an over-limit payload produced %+v, want nothing", out)
	}
	assertEnrollmentError(t, err, ErrEnrollmentMalformedRequest, exitcode.Conflict)
}
