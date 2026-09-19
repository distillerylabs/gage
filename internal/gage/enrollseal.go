package gage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"

	"github.com/distillerylabs/gage/internal/gage/agekey"
	"github.com/distillerylabs/gage/internal/gage/config"
	"github.com/distillerylabs/gage/internal/gage/devicename"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
)

// The enrollment seal's cost parameters, and the seal/open primitives
// built on them. See D-ENROLL-SEAL-COST and "The reframe: the seal is
// authentication, not confidentiality" in the enrollment design doc.

// shippedEnrollmentScryptWorkFactor is the log2 cost a sealed enrollment
// request is written at. It is deliberately NOT shippedScryptWorkFactor:
// the two protect different things and are calibrated against different
// attacks. Do not collapse them.
//
// A work factor buys cost *per guess*, which is what a user-chosen
// passphrase needs and what shippedScryptWorkFactor's comment is entirely
// about. An enrollment code is chosen by nobody: it is 16 Crockford
// characters from crypto/rand — ~80 bits (D-ENROLL-CODE-FORMAT) — and at
// 80 bits the guess count is already the defence, infeasible at any
// factor including none. The extra doublings would be paid in latency and
// buy nothing.
//
// Not zero, though, because that licence is a property of the generator
// and the factor should still hold up if the property is weakened by a
// bug rather than by a decision. 14 is roughly 60ms against the identity
// file's ~2s at 19: meaningful stretching as defence in depth, ~32x
// cheaper. It is also what makes the honest case — a mistyped code
// against a full pending/ — fast, which is the case D-ENROLL-SEAL-COST is
// really calibrated for.
//
// The coupling, recorded where someone would break it: the low factor is
// licensed by the code being generated, uniform, and ~80 bits. If a
// user-chosen code is ever accepted, or the length shortened, this factor
// goes back up in the same change.
//
// Raising it later needs no migration — age records the factor in each
// blob's own scrypt stanza, and requests live 24 hours anyway.
const shippedEnrollmentScryptWorkFactor = 14

// enrollmentScryptWorkFactor is the live copy of the constant above: the
// value sealing actually reads, and the only part of this a test can
// move. It exists for the same reason scryptWorkFactor does, and the
// arithmetic is no kinder here — E2's own fixtures are 32 seals, and E4
// builds pending requests through Enroll across most of its test list, on
// three platforms.
//
// Having its own hook is what preserves the independence, rather than
// weakening it: two constants, two variables, two setters, so moving
// either factor leaves the other alone. What is forbidden is enrollment
// *reading* scryptWorkFactor — a suite that lowered the identity factor
// would then silently lower the seal's, hiding exactly the calibration
// D-ENROLL-SEAL-COST is making.
//
// SetEnrollmentWorkFactorForTests is the only thing permitted to write
// it; TestOnlyTheTestHooksWriteTheWorkFactors is what enforces that.
var enrollmentScryptWorkFactor = shippedEnrollmentScryptWorkFactor

// SetEnrollmentWorkFactorForTests overrides enrollmentScryptWorkFactor
// for the lifetime of the process and returns a function restoring the
// previous value. It is the exact twin of SetScryptWorkFactorForTests,
// including the rule that it must never be called outside a _test.go
// file — enforced by .golangci.yml's forbidigo config and by the AST
// walk in crypt_test.go.
func SetEnrollmentWorkFactorForTests(n int) (restore func()) {
	old := enrollmentScryptWorkFactor
	enrollmentScryptWorkFactor = n
	return func() { enrollmentScryptWorkFactor = old }
}

// enrollmentPayload is the plaintext under the seal, as TOML.
//
// Every field is a string, timestamps included, because this type is used
// on the way *in* as well as the way out and what arrives is untrusted:
// the fields are validated after unmarshalling, not by unmarshalling, so
// a malformed timestamp is a request gage refuses rather than a parse
// error it reports as corruption. See "The sealed payload is untrusted
// input".
type enrollmentPayload struct {
	RequestID string `toml:"request_id"`
	VaultID   string `toml:"vault_id"`
	Device    string `toml:"device"`
	Pubkey    string `toml:"pubkey"`
	Method    string `toml:"method"`
	Created   string `toml:"created"`
	Expires   string `toml:"expires"`
}

// sealEnrollment seals one enrollment request for this vault and returns
// it alongside the bytes to write into pending/. The returned
// EnrollmentRequest carries the generated code, which is the one thing a
// caller must display and this package must never persist.
//
// It is where the TTL is validated, because sealing is the only function
// in this milestone that takes a duration and because a library a GUI
// could call directly cannot rely on a command line having been checked
// first. Enroll inherits the check by calling through here rather than
// re-performing it.
//
// The sealed payload names the vault it is for. That closes the one
// replay direction request_id does not: a blob is committed and readable
// to every reader of the vault it sits in, so moving one costs an
// attacker nothing — and the case needs no attacker at all, just one
// person with two vaults and two codes on screen.
func (v *Vault) sealEnrollment(device, pubkey, method string, ttl time.Duration) (EnrollmentRequest, []byte, error) {
	if ttl <= 0 {
		return EnrollmentRequest{}, nil, exitcode.Newf(exitcode.Usage,
			"gage: an enrollment request's lifetime must be positive, not %s", ttl)
	}
	if ttl > MaxEnrollmentTTL {
		return EnrollmentRequest{}, nil, exitcode.Newf(exitcode.Usage,
			"gage: an enrollment request may live at most %s, not %s", MaxEnrollmentTTL, ttl)
	}
	if !devicename.Valid(device) {
		return EnrollmentRequest{}, nil, exitcode.Newf(exitcode.Usage,
			"gage: device name %q is invalid", device)
	}
	if err := agekey.ValidateRecipient(pubkey); err != nil {
		return EnrollmentRequest{}, nil, exitcode.Wrap(exitcode.Usage, err)
	}
	if !contains(AllowedMethods(), method) {
		return EnrollmentRequest{}, nil, exitcode.Newf(exitcode.Usage,
			"gage: unknown method %q; accepted: %v", method, AllowedMethods())
	}
	if !vaultconfig.ValidID(v.ID) {
		return EnrollmentRequest{}, nil, exitcode.Newf(exitcode.Internal,
			"gage: this vault has no valid id, so a request cannot name the vault it is for")
	}

	code, err := newEnrollmentCode()
	if err != nil {
		return EnrollmentRequest{}, nil, err
	}
	normalized, ok := normalizeEnrollmentCode(code)
	if !ok {
		// Unreachable unless the generator and the normalizer disagree,
		// which TestGeneratedCodesNormalizeToThemselves rules out — but a
		// silent mismatch here would produce a request no code opens.
		return EnrollmentRequest{}, nil, exitcode.New(exitcode.Internal,
			"gage: generated an enrollment code its own validation rejects")
	}

	created := time.Now().UTC().Truncate(time.Second)
	req := EnrollmentRequest{
		ID:      uuid.NewString(),
		Device:  device,
		Pubkey:  pubkey,
		Method:  method,
		Created: created,
		Expires: created.Add(ttl),
		Code:    code,
	}
	sealed, err := sealEnrollmentPayload(enrollmentPayload{
		RequestID: req.ID,
		VaultID:   v.ID,
		Device:    req.Device,
		Pubkey:    req.Pubkey,
		Method:    req.Method,
		Created:   req.Created.Format(time.RFC3339),
		Expires:   req.Expires.Format(time.RFC3339),
	}, normalized)
	if err != nil {
		return EnrollmentRequest{}, nil, err
	}
	return req, sealed, nil
}

// sealEnrollmentPayload encrypts p to a single scrypt recipient derived
// from code, at enrollment's own work factor.
//
// code is the *normalized* form, not the displayed one: every accepted
// spelling of a code has to reach the same passphrase or a correctly
// retyped code would fail to open its own request.
//
// What this produces is an authentication token, not a secret. The
// plaintext is a public key and some metadata, all of which a reader of
// the vault could already enumerate; what the seal proves is that whoever
// wrote the blob knew a code agreed out-of-band, and that the bytes have
// not been altered since. See "The reframe".
func sealEnrollmentPayload(p enrollmentPayload, code string) ([]byte, error) {
	plaintext, err := toml.Marshal(p)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: encoding an enrollment request: %w", err))
	}
	to, err := passphraseRecipientAt(code, enrollmentScryptWorkFactor)
	if err != nil {
		return nil, err
	}
	return Encrypt(plaintext, to)
}

// openSealedRequest is one attempt to open one request's bytes with one
// code, followed by the validation that turns an authenticated payload
// into a well-formed one.
//
// The two halves are deliberately one function, because the ordering is
// the security property: every field is checked before an OpenedRequest
// exists, and therefore before anything in it is rendered to the human
// deciding whether to grant vault-wide access. The device name is printed
// directly above that [y/N], so a name that can move the cursor could
// rewrite the question being answered.
//
// opened is false when the code simply did not open this blob, which is
// not an error — it is the ordinary case for every code that is not this
// request's. An err with opened true is a request the code *did* open and
// gage still refuses.
func (v *Vault) openSealedRequest(req PendingRequest, sealed []byte, code string, now time.Time) (out OpenedRequest, opened bool, err error) {
	scryptID, err := age.NewScryptIdentity(code)
	if err != nil {
		return OpenedRequest{}, false, exitcode.Wrap(exitcode.LockedOrAuth, fmt.Errorf("gage: %w", err))
	}
	// The cap on a request's *claimed* work factor, the first part of
	// D-ENROLL-SEAL-COST. Enrollment is a new decryption path and
	// inherits this from nothing: without it, one committed file claiming
	// 2^30 costs hours with no error to show for it — a hang rather than
	// a slowdown, and no volume of files needed.
	scryptID.SetMaxWorkFactor(scryptMaxWorkFactor)

	if v.onEnrollmentDecrypt != nil {
		v.onEnrollmentDecrypt()
	}
	plaintext, overLimit, err := decryptBytesLimited(sealed, maxPendingRequestBytes, scryptID)
	if err != nil {
		// Either the code is not this request's, or the bytes are not an
		// intact age file. Both mean this blob did not open; a damaged or
		// hostile file must not take the whole run down with it.
		return OpenedRequest{}, false, nil
	}
	if overLimit {
		// The code worked and the payload is larger than anything gage
		// writes. A code-holder is trusted enough to be granted access,
		// not trusted enough to be handed an unbounded allocation.
		return OpenedRequest{}, true, exitcode.Wrap(exitcode.Conflict,
			fmt.Errorf("%w: its decrypted payload is larger than %d bytes", ErrEnrollmentMalformedRequest, maxPendingRequestBytes))
	}

	out, err = v.validateSealedPayload(req, plaintext, now)
	return out, true, err
}

// validateSealedPayload is everything that has to be true of an
// authenticated payload before it becomes an OpenedRequest.
//
// Authenticated is not well-formed. The seal proves the bytes are
// unaltered since sealing, not that the fields mean anything, and two
// people can supply a malformed one without any attack: a git writer can
// drop a file into pending/, and anyone legitimately holding a code can
// seal whatever they like.
//
// The three refusals it can produce are kept distinct on purpose, because
// they send a human to three different places: a malformed payload is
// damage to look at, a wrong vault is a request to approve somewhere
// else, and an expiry is a request to ask for again. None of them is a
// wrong code — the code worked.
func (v *Vault) validateSealedPayload(req PendingRequest, plaintext []byte, now time.Time) (OpenedRequest, error) {
	var p enrollmentPayload
	if err := toml.Unmarshal(plaintext, &p); err != nil {
		return OpenedRequest{}, malformedRequest("its sealed contents are not valid TOML")
	}

	if !canonicalUUID(p.RequestID) {
		return OpenedRequest{}, malformedRequest("its sealed request id %q is not a uuid", p.RequestID)
	}
	if !canonicalUUID(p.VaultID) {
		return OpenedRequest{}, malformedRequest("its sealed vault id %q is not a uuid", p.VaultID)
	}
	// devicename.Valid is what keeps escape sequences and newlines out of
	// a string that is displayed above a prompt granting vault-wide
	// access, which is why it runs here rather than at AddRecipient two
	// milestones and one confirmation later.
	if !devicename.Valid(p.Device) {
		return OpenedRequest{}, malformedRequest("its sealed device name is not a name gage would write")
	}
	if err := agekey.ValidateRecipient(p.Pubkey); err != nil {
		return OpenedRequest{}, malformedRequest("its sealed public key is not an age recipient")
	}
	if !contains(AllowedMethods(), p.Method) {
		return OpenedRequest{}, malformedRequest("its sealed method %q is not one this build performs", p.Method)
	}
	created, err := time.Parse(time.RFC3339, p.Created)
	if err != nil {
		return OpenedRequest{}, malformedRequest("its sealed creation time %q is not an RFC 3339 timestamp", p.Created)
	}
	expires, err := time.Parse(time.RFC3339, p.Expires)
	if err != nil {
		return OpenedRequest{}, malformedRequest("its sealed expiry %q is not an RFC 3339 timestamp", p.Expires)
	}

	// The filename is unauthenticated — anyone with git write access can
	// rename a file — while the copy under the seal is not, so a blob
	// cannot be renamed onto a different slot or replayed into a fresh
	// one. Only the UUID portion is compared: the epoch is a hint for
	// display and pruning, and holding it to the seal would refuse
	// requests over a lie that grants nothing.
	if p.RequestID != req.ID {
		return OpenedRequest{}, exitcode.Wrap(exitcode.Conflict,
			fmt.Errorf("%w: sealed as %s, filed as %s", ErrEnrollmentIDMismatch, p.RequestID, req.ID))
	}
	if p.VaultID != v.ID {
		return OpenedRequest{}, exitcode.Wrap(exitcode.Conflict,
			fmt.Errorf("%w: it is for %s, not %q",
				ErrEnrollmentWrongVault, describeVaultID(p.VaultID), v.Name))
	}

	// Clock skew is checked before expiry because a skewed request is
	// also an expired one, and the two have unrelated fixes: fix that
	// machine's clock and enroll again, versus ask for a fresh request.
	// It is the one comparison between two sealed fields, and it is sound
	// precisely because it needs no reference: a request whose expiry
	// preceded its own creation was dead before it was written, whoever
	// wrote it.
	if expires.Before(created) {
		return OpenedRequest{}, exitcode.Wrap(exitcode.Conflict,
			fmt.Errorf("%w: sealed at %s, expiring at %s",
				ErrEnrollmentClockSkew, created.Format(time.RFC3339), expires.Format(time.RFC3339)))
	}
	// The ceiling clamps the sealed expiry too, not only the filename's.
	// The sealed copy is the one approval enforces, so clamping the
	// filename alone would leave the authoritative value unbounded — and
	// the comparison is against now rather than created because both
	// sealed timestamps are attacker-supplied, so checking one against
	// the other checks nothing.
	if !enrollmentExpiryLive(expires, now) {
		return OpenedRequest{}, exitcode.Wrap(exitcode.Conflict,
			fmt.Errorf("%w: it expired at %s", ErrEnrollmentExpired, expires.Format(time.RFC3339)))
	}

	return OpenedRequest{
		ID:      p.RequestID,
		Device:  p.Device,
		Pubkey:  p.Pubkey,
		Method:  p.Method,
		Created: created,
		Expires: expires,
	}, nil
}

// malformedRequest builds ErrEnrollmentMalformedRequest with the reason
// attached, at the exit code the taxonomy assigns it: Conflict, because
// it is a file in the vault a human must look at, and never a retryable
// code.
func malformedRequest(format string, args ...any) error {
	return exitcode.Wrap(exitcode.Conflict,
		fmt.Errorf("%w: %s", ErrEnrollmentMalformedRequest, fmt.Sprintf(format, args...)))
}

// OpenEnrollment tries each code against each request in the scope it is
// handed, and returns the ones that opened.
//
// The scope is a parameter rather than "every pending request" because
// that is what makes ErrEnrollmentTooManyPending's escape hatch work *by
// construction* rather than by a second code path. A broad run passes
// PendingEnrollments()' result and is refused when that exceeds the
// bound; an ID-scoped run passes the one PendingRequest
// ResolveEnrollment returned, and one request never exceeds it. Neither
// caller opts into or out of the bound — they differ only in what they
// ask about, which is the difference a human expressed by typing an ID.
// Reading the directory in here and treating the argument as advisory
// would quietly reintroduce the unbounded path.
//
// It takes no Identity: opening is keyed by the code. That signature is
// what makes a late unlock possible on the approving side.
//
// A single bad file never takes the run down. A malformed, oversized,
// expired or wrong-vault request is skipped in favour of anything that
// did open, and its error is reported only when nothing did — otherwise
// any git writer would hold a denial of service on approval, which is the
// thing D-ENROLL-SEAL-COST's bound exists to prevent by another route.
func (v *Vault) OpenEnrollment(requests []PendingRequest, codes []string) ([]OpenedRequest, error) {
	// Before any decryption, and against the scope handed in.
	if len(requests) > maxEnrollmentAttempts {
		return nil, exitcode.Wrap(exitcode.Conflict,
			fmt.Errorf("%w: %d are pending and the limit is %d",
				ErrEnrollmentTooManyPending, len(requests), maxEnrollmentAttempts))
	}

	// Normalization and validation run before any decryption too, which
	// is what makes a typo cost nothing rather than N scrypt runs. A code
	// that cannot be one of gage's is dropped here; if none survives, the
	// answer is the same ErrEnrollmentCodeWrong a well-formed code that
	// opens nothing gets, deliberately.
	valid := make([]string, 0, len(codes))
	for _, c := range codes {
		if normalized, ok := normalizeEnrollmentCode(c); ok {
			valid = append(valid, normalized)
		}
	}
	if len(valid) == 0 || len(requests) == 0 {
		return nil, wrongCode()
	}

	now := time.Now()
	var opened []OpenedRequest
	var refused error
	for _, req := range requests {
		sealed, ok := readPendingFile(req.Path)
		if !ok {
			continue
		}
		for _, code := range valid {
			out, didOpen, err := v.openSealedRequest(req, sealed, code, now)
			if !didOpen {
				continue
			}
			if err != nil {
				if refused == nil {
					refused = err
				}
				break
			}
			opened = append(opened, out)
			break
		}
	}

	if len(opened) > 0 {
		return opened, nil
	}
	if refused != nil {
		return nil, refused
	}
	return nil, wrongCode()
}

// wrongCode builds ErrEnrollmentCodeWrong at LockedOrAuth — a secret that
// didn't open what it was meant to open, which is the same shape as a
// wrong passphrase and what a retry loop keys on.
func wrongCode() error {
	return exitcode.Wrap(exitcode.LockedOrAuth, ErrEnrollmentCodeWrong)
}

// readPendingFile reads one sealed request, refusing anything larger than
// gage would have written before it is held in memory.
//
// The size bound is applied here as well as at listing time because
// OpenEnrollment is handed a scope rather than reading the directory
// itself: a caller that built a PendingRequest some other way must not be
// able to route around a check the listing performs. A file that fails it
// is skipped, never deleted — it is by definition not something gage
// wrote.
func readPendingFile(path string) ([]byte, bool) {
	// #nosec G304 -- path comes from a PendingRequest, whose filename was
	// validated as <canonical uuid>-<digits>.age before it was built.
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()

	// One byte past the limit, so a file sitting exactly on it is
	// accepted and the first byte over is detected without reading the
	// rest.
	sealed, err := io.ReadAll(io.LimitReader(f, maxPendingRequestBytes+1))
	if err != nil || len(sealed) > maxPendingRequestBytes {
		return nil, false
	}
	return sealed, true
}

// describeVaultID renders a vault id the way the wrong-vault refusal
// needs it: by the name this machine has the vault registered under, if
// it has one, and by the bare id otherwise.
//
// The id alone is opaque, and "this request is for \"work\", not
// \"personal\"" is the whole content of the mistake — the case needs no
// attacker, just one person with two vaults and two codes on screen. A
// registry that is missing or unreadable costs the name and nothing else:
// this is a message, and failing to build one must not turn a refusal
// into a different error.
func describeVaultID(id string) string {
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		return fmt.Sprintf("the vault with id %s", id)
	}
	g, err := config.Read(filepath.Join(dir, "config.toml"))
	if err != nil {
		return fmt.Sprintf("the vault with id %s", id)
	}
	for name, entry := range g.Vaults {
		if entry.ID == id {
			return fmt.Sprintf("%q (id %s)", name, id)
		}
	}
	return fmt.Sprintf("the vault with id %s", id)
}
