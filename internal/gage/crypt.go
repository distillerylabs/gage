package gage

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"filippo.io/age"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// The typed errors this package's crypto layer returns. They're sentinels
// rather than distinct types because every caller wants errors.Is, and
// because they carry no payload beyond their identity. Each is wrapped in
// an exitcode-carrying error at the point it's returned, so cmd/gage gets
// both the category and the exit status from one value.
var (
	// ErrNoRecipients is Encrypt refusing to produce a file nobody can
	// read. age itself would also refuse, but failing here keeps the
	// reason attributable to gage rather than surfacing as an opaque
	// library string.
	ErrNoRecipients = errors.New("gage: refusing to encrypt to zero recipients")

	// ErrMixedScryptRecipient is Encrypt refusing to combine a
	// passphrase (scrypt) recipient with any other. age enforces this
	// too, but the identity file's whole "always exactly one recipient"
	// property rests on it (see A3 and "Local identity storage"), so
	// gage states the invariant itself instead of inheriting it silently.
	ErrMixedScryptRecipient = errors.New("gage: a passphrase recipient must be a file's only recipient")

	// ErrInvalidRecipient is a recipient string that isn't an age
	// recipient gage can encrypt to.
	ErrInvalidRecipient = errors.New("gage: not a usable age recipient")

	// ErrNotARecipient is decryption failing because the identity
	// offered isn't among the file's recipients — distinct from the file
	// being damaged, because the two mean very different things to a
	// user (M8a's clone leans on exactly this distinction).
	ErrNotARecipient = errors.New("gage: this identity is not a recipient of that ciphertext")

	// ErrCorruptCiphertext is decryption failing because the bytes
	// aren't an intact age file: a bad header, a truncated payload, a
	// failed authentication tag.
	ErrCorruptCiphertext = errors.New("gage: ciphertext is corrupt or truncated")

	// ErrIdentityClosed is any use of an Identity after Close. Using a
	// closed Identity has to fail loudly: its key material is zeroed, so
	// the alternative to an error is a confusing wrong-key failure or,
	// worse, an operation that looks like it worked.
	ErrIdentityClosed = errors.New("gage: identity is closed")
)

// Recipient is one parsed age recipient gage can encrypt to. It's a
// wrapper rather than a bare age.Recipient so Encrypt can enforce the
// scrypt sole-recipient invariant before handing anything to age, and so
// the rest of gage never imports filippo.io/age directly.
type Recipient struct {
	r age.Recipient
	// s is the recipient as written — the "age1..." string for a public
	// key, or a fixed label for a passphrase recipient, which has no
	// public spelling. Used only in error messages.
	s string
	// passphrase marks a scrypt recipient, the one kind that can't share
	// a file with any other.
	passphrase bool
}

// String renders the recipient the way error messages refer to it. A
// passphrase recipient deliberately has no spelling that could be
// mistaken for a key.
func (r Recipient) String() string { return r.s }

// ParseRecipient parses an "age1..." X25519 public key into a Recipient.
// Plugin recipients (age1yubikey1..., which .age-recipients may legally
// contain per "On-disk layout") are not encryptable by this build and are
// rejected here rather than half-supported — a plugin method is a later
// milestone's addition, not something to fake.
func ParseRecipient(s string) (Recipient, error) {
	r, err := age.ParseX25519Recipient(s)
	if err != nil {
		return Recipient{}, exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("%w: %q: %v", ErrInvalidRecipient, s, err))
	}
	return Recipient{r: r, s: s}, nil
}

// PassphraseRecipient builds age's scrypt recipient for passphrase. The
// work factor is deliberate, not the library default — see
// shippedScryptWorkFactor, of which scryptWorkFactor is the live copy a
// test binary is allowed to lower.
func PassphraseRecipient(passphrase string) (Recipient, error) {
	return passphraseRecipientAt(passphrase, scryptWorkFactor)
}

// passphraseRecipientAt is PassphraseRecipient with the work factor
// spelled out, which is what lets enrollment seal at its own calibrated
// factor without reading the identity file's.
//
// The explicit form exists because the implicit one is a trap:
// PassphraseRecipient reads scryptWorkFactor — the mutable test hook — so
// sealing through it would mean a suite that lowers the identity factor
// silently lowers the seal's too, which is precisely the collapse
// D-ENROLL-SEAL-COST forbids. Every caller now names the factor it means,
// and PassphraseRecipient is simply the caller that names
// scryptWorkFactor.
func passphraseRecipientAt(passphrase string, workFactor int) (Recipient, error) {
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return Recipient{}, exitcode.Wrap(exitcode.Usage, fmt.Errorf("gage: %w", err))
	}
	r.SetWorkFactor(workFactor)
	return Recipient{r: r, s: "<passphrase>", passphrase: true}, nil
}

// Encrypt encrypts plaintext to every recipient in to, returning a
// complete binary age file. Bytes in, bytes out: it knows nothing about
// entries, files, or what the plaintext means, which is what lets M3's
// entry layer, M9's --reencrypt, and M11's cross-vault encrypt all share
// it without any of their semantics leaking down here.
func Encrypt(plaintext []byte, to ...Recipient) ([]byte, error) {
	if len(to) == 0 {
		return nil, exitcode.Wrap(exitcode.Usage, ErrNoRecipients)
	}
	for _, r := range to {
		if r.passphrase && len(to) > 1 {
			return nil, exitcode.Wrap(exitcode.Usage,
				fmt.Errorf("%w (asked to combine it with %d other recipient(s))", ErrMixedScryptRecipient, len(to)-1))
		}
		if r.r == nil {
			return nil, exitcode.Wrap(exitcode.Usage,
				fmt.Errorf("%w: zero-value recipient", ErrInvalidRecipient))
		}
	}

	ageRecipients := make([]age.Recipient, len(to))
	for i, r := range to {
		ageRecipients[i] = r.r
	}

	var out bytes.Buffer
	w, err := age.Encrypt(&out, ageRecipients...)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: encrypting: %w", err))
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: encrypting: %w", err))
	}
	// Close is what encrypts and flushes the final chunk, so its error is
	// the write's error and must not be discarded.
	if err := w.Close(); err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: encrypting: %w", err))
	}
	return out.Bytes(), nil
}

// Decrypt decrypts ciphertext with id's private key. It returns
// ErrNotARecipient when id simply isn't one of the file's recipients and
// ErrCorruptCiphertext when the bytes aren't an intact age file — never a
// panic, and never partial plaintext.
func Decrypt(ciphertext []byte, id *Identity) ([]byte, error) {
	ageID, err := id.ageIdentity()
	if err != nil {
		return nil, err
	}
	return decryptBytes(ciphertext, ageID)
}

// decryptBytes is the one place ciphertext is turned back into plaintext,
// shared by Decrypt (an unlocked Identity) and Vault.Unlock (a passphrase
// identity, before any Identity exists). It classifies age's errors into
// gage's two categories and returns nothing at all on failure — a partial
// read is discarded rather than handed back.
func decryptBytes(ciphertext []byte, ids ...age.Identity) ([]byte, error) {
	plaintext, _, err := decryptBytesLimited(ciphertext, -1, ids...)
	return plaintext, err
}

// decryptBytesLimited is decryptBytes with a ceiling on how much
// plaintext it will hold in memory. A limit below zero means no ceiling,
// which is what every caller reading gage's own files wants: an entry is
// as large as its author made it.
//
// The ceiling exists for enrollment, where it is the one bound the other
// three in D-ENROLL-SEAL-COST do not provide. age's plaintext length is
// not bounded by its ciphertext's — a few hundred compressible bytes can
// expand — and the party who can produce a large one is a code-holder:
// trusted enough to be granted access, not trusted enough to be handed an
// unbounded allocation.
//
// overLimit is reported separately from err so the caller can tell "this
// payload is too big" apart from "these bytes are damaged" and pick its
// own error for each. Nothing partial is returned in either case.
func decryptBytesLimited(ciphertext []byte, limit int64, ids ...age.Identity) (plaintext []byte, overLimit bool, err error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), ids...)
	if err != nil {
		return nil, false, classifyDecryptError(err)
	}
	src := r
	if limit >= 0 {
		// One byte past the limit, so a payload sitting exactly on it is
		// accepted and the first byte over is detectable without ever
		// reading — or allocating — the rest.
		src = io.LimitReader(r, limit+1)
	}
	// The payload is authenticated per chunk, so an error here is a
	// damaged or truncated file rather than a wrong key: the key already
	// matched a recipient stanza for Decrypt to have returned a reader.
	out, err := io.ReadAll(src)
	if err != nil {
		return nil, false, exitcode.Wrap(exitcode.Conflict,
			fmt.Errorf("%w: %v", ErrCorruptCiphertext, err))
	}
	if limit >= 0 && int64(len(out)) > limit {
		return nil, true, nil
	}
	return out, false, nil
}

// classifyDecryptError splits age's header-stage failures into "you
// aren't a recipient" and "these bytes are damaged." age signals the
// former with a *age.NoIdentityMatchError; anything else at this stage is
// a header that didn't parse or didn't authenticate.
func classifyDecryptError(err error) error {
	var noMatch *age.NoIdentityMatchError
	if errors.As(err, &noMatch) {
		// Both errors are wrapped, not formatted in: Vault.Unlock reads
		// the *age.NoIdentityMatchError back out of the chain to tell a
		// wrong passphrase apart from a file that was never
		// passphrase-wrapped, and a %v here would flatten it to text.
		return exitcode.Wrap(exitcode.LockedOrAuth,
			fmt.Errorf("%w: %w", ErrNotARecipient, err))
	}
	return exitcode.Wrap(exitcode.Conflict,
		fmt.Errorf("%w: %w", ErrCorruptCiphertext, err))
}

// zero overwrites b in place. It's the only way key material leaves this
// process's memory deliberately rather than by being garbage collected at
// some unspecified later point, and it's what Identity.Close calls.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
