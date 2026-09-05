package gage

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"

	"github.com/denmark/gage/internal/gage/atomicfile"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/memlock"
)

// shippedScryptWorkFactor is the log2 cost gage wraps identity files at
// — a deliberate number, not age's default by omission (the M2 plan
// calls this out as a decision to make first).
//
// age defaults to 18, about a second on a modern machine. gage uses 19,
// doubling that: this parameter is the only thing between a stolen
// identity file and an unlimited, unrate-limited offline brute force, so
// every step up doubles the attacker's cost for the same passphrase.
// It stops at 19 rather than climbing further because one-shot mode pays
// this cost on *every* invocation (see "Session model"), and a `gage
// show` that takes four seconds pushes people toward weaker passphrases
// or toward keeping a session open longer than they need — both of which
// give back more than the extra factor buys. Session mode is what
// amortizes it for interactive use.
//
// Raising this later is safe and needs no migration: the work factor is
// recorded in each file's own scrypt stanza, so existing files keep
// opening at the factor they were written with.
//
// It stays a const so the shipped number cannot be moved at runtime by
// anything, test hook included: TestScryptWorkFactorIsDeliberate checks
// this constant directly, and the compiler is what guarantees the value
// it checks is the value that ships.
const shippedScryptWorkFactor = 19

// scryptWorkFactor is the live copy of shippedScryptWorkFactor that the
// crypto path actually reads, and the only part of this that a test can
// move. It exists because the suite unlocks a real vault (real scrypt,
// real cost) hundreds of times across internal/gage and cmd/gage, which
// is otherwise several minutes of wall clock spent proving nothing
// beyond "scrypt still costs what scrypt costs."
//
// SetScryptWorkFactorForTests is the only thing permitted to write it.
// That is not merely a convention: a stray `scryptWorkFactor = 10` in a
// non-test file of this package would ship a weakened KDF while every
// other guard here stayed green, since forbidigo forbids the *function*
// and TestScryptWorkFactorIsDeliberate reads the *constant*. The
// assignment itself is what TestOnlyTheTestHookWritesScryptWorkFactor
// checks, which is the piece a const alone cannot express.
var scryptWorkFactor = shippedScryptWorkFactor

// scryptMaxWorkFactor caps the work gage will perform on an identity
// file's *claimed* factor. Set explicitly rather than inherited, so a
// damaged or hostile file claiming 2^30 costs a bounded wait instead of
// hanging the process. 22 matches age's own default ceiling and leaves
// three doublings of headroom above what gage writes today.
const scryptMaxWorkFactor = 22

// SetScryptWorkFactorForTests overrides scryptWorkFactor for the
// lifetime of the process and returns a function that restores the
// previous value. It exists only because Go gives a different package's
// tests no other way to reach unexported state: cmd/gage's CLI tests
// drive close to two hundred real vault unlocks, and internal/gage's own
// tests aren't far behind, so paying the real, deliberately expensive KDF
// cost on every one of them turns the suite into minutes spent proving
// nothing beyond "scrypt still costs what scrypt costs."
//
// It must never be called outside a _test.go file. That is enforced
// three ways: internal/gage is unimportable from outside this module at
// all (Go's own "internal/" rule); .golangci.yml's forbidigo config
// specifically forbids this identifier anywhere else in the module,
// cmd/gage's normal package-wide forbidigo exemption included (see the
// comment there); and TestOnlyTheTestHookWritesScryptWorkFactor rejects
// the spelling that sidesteps both, a direct assignment to
// scryptWorkFactor from inside this package. n has no floor or ceiling
// check: a caller weakening its own tests into meaninglessness (n == 1)
// is a test-quality problem for that caller, not one this function
// should silently second-guess.
func SetScryptWorkFactorForTests(n int) (restore func()) {
	old := scryptWorkFactor
	scryptWorkFactor = n
	return func() { scryptWorkFactor = old }
}

// ErrIdentityExists is CreateIdentity refusing to overwrite an identity
// file that already exists. Overwriting one would destroy the only copy
// of a private key with no way to get it back — the design is explicit
// that losing an identity file is a recovery problem, and silently
// manufacturing that situation is not something gage should do.
var ErrIdentityExists = errors.New("gage: an identity file for this device and vault already exists")

// ErrCorruptIdentityFile is an identity file that decrypted but doesn't
// contain a usable private key, or that isn't an intact age file at all.
// Distinct from a wrong passphrase, which is a user error rather than a
// damaged file.
var ErrCorruptIdentityFile = errors.New("gage: identity file is corrupt")

// ErrNoLocalIdentity is Unlock finding no identity registered for this
// device: no entry in global config, or no file where that entry points.
// M8a's clone renders this as "this device isn't a recipient yet."
var ErrNoLocalIdentity = errors.New("gage: no local identity is registered for this device")

// ErrWrongPassphrase is the passphrase not opening the identity file. It
// is deliberately its own value: cmd/gage's retry policy keys off it, and
// no other failure should cause a re-prompt.
var ErrWrongPassphrase = errors.New("gage: wrong passphrase")

// ErrUnsupportedMethod is this device's locally-recorded unlock method
// naming something this build can't perform.
var ErrUnsupportedMethod = errors.New("gage: unsupported identity method")

// identityFileSecretPrefix is the bech32 human-readable part age uses for
// a private key, and the marker that finds the key line in a decrypted
// identity file.
const identityFileSecretPrefix = "AGE-SECRET-KEY-1"

// CreateIdentity generates this device's identity for a vault: a fresh
// X25519 keypair whose private half is wrapped with a passphrase obtained
// through p and written to $GAGE_DATA/identities/<vault>/<device>.age
// (directory 0700, file 0600). It returns the public half, the only part
// that ever leaves this machine.
//
// The wrapped file has exactly one recipient by construction — age
// refuses to combine a scrypt recipient with any other, and Encrypt
// refuses first (see ErrMixedScryptRecipient). There is deliberately no
// "also let my other device open this" variant: the recovery story for a
// lost identity file is registering a fresh identity and being re-added
// as a recipient. See "Local identity storage" and A3.
func CreateIdentity(vault, device string, p Prompter) (string, error) {
	path, err := IdentityFilePath(vault, device)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return "", exitcode.Wrap(exitcode.Conflict, fmt.Errorf("%w: %s", ErrIdentityExists, path))
	} else if !os.IsNotExist(err) {
		return "", exitcode.Wrap(exitcode.Internal, err)
	}

	passphrase, err := requestPassphrase(p, UnlockRequest{
		Kind:    KindPassphrase,
		Purpose: PurposeCreate,
		Vault:   vault,
		Device:  device,
		Attempt: 1,
	})
	if err != nil {
		return "", err
	}

	ident, err := age.GenerateX25519Identity()
	if err != nil {
		return "", exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: generating a keypair: %w", err))
	}

	plaintext := formatIdentityFile(ident, time.Now().UTC())
	recipient, err := PassphraseRecipient(passphrase)
	if err != nil {
		zero(plaintext)
		return "", err
	}
	wrapped, err := Encrypt(plaintext, recipient)
	// The plaintext key leaves memory as soon as it's been wrapped,
	// whether or not wrapping succeeded.
	zero(plaintext)
	if err != nil {
		return "", err
	}

	if err := writeIdentityFile(path, wrapped); err != nil {
		return "", err
	}
	return ident.Recipient().String(), nil
}

// RemoveIdentity deletes this device's wrapped identity file for a vault,
// and reports success if there was nothing there to delete.
//
// This exists for exactly one caller: rolling back a `gage init` that
// generated an identity and then failed before anything referenced its
// public key. Without it, a failed init leaves an orphan that
// CreateIdentity's own overwrite refusal then blocks forever — a retry
// after fixing whatever went wrong would be permanently stuck.
//
// It is deliberately narrow and deliberately not offered to users as a
// command: deleting an identity file that a vault *does* list as a
// recipient loses access to that vault's existing ciphertext, and the
// answer there is registering a fresh identity, not deleting the old one.
func RemoveIdentity(vault, device string) error {
	path, err := IdentityFilePath(vault, device)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	// Prune the vault's identities directory if it's now empty. os.Remove
	// on a directory succeeds only when it is, which is exactly the
	// wanted semantics: a directory still holding another device's
	// identity is left alone, and the error is dropped because a leftover
	// empty directory is not a failure worth reporting over whatever
	// prompted the rollback.
	_ = os.Remove(filepath.Dir(path))
	return nil
}

// HasIdentity reports whether this device holds a wrapped identity for a
// vault — the question `gage clone` answers to tell someone whether they
// can actually read what they just cloned.
//
// It deliberately checks only for the file's presence, not that its key
// is one of the vault's current recipients: opening it would need the
// passphrase, and prompting for an unlock during a clone in order to
// report that the unlock was pointless is exactly backwards. A device
// with no identity file certainly cannot decrypt anything, which is the
// case worth telling someone about.
func HasIdentity(vault, device string) (bool, error) {
	path, err := IdentityFilePath(vault, device)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, exitcode.Wrap(exitcode.Internal, err)
	}
	return true, nil
}

// writeIdentityFile creates the 0700 identities directory and writes the
// wrapped key 0600 through the shared atomic-write helper.
func writeIdentityFile(path string, wrapped []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	// MkdirAll leaves an already-existing directory's mode alone, so an
	// identities/ directory created before this rule existed (or by a
	// umask that widened it) is tightened here rather than trusted.
	// #nosec G302 -- 0700 is the tightest mode a *directory* can have and
	// still be usable: a directory without its owner-execute bit cannot
	// be traversed, so the file inside it could not be opened at all.
	if err := os.Chmod(dir, 0o700); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	if err := atomicfile.WriteFile(path, wrapped, 0o600); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	return nil
}

// formatIdentityFile renders a keypair in age-keygen's own file format:
// two comment lines and the secret key. Matching that format exactly is
// what lets the stock age CLI open a gage identity file directly
// (`age -d -i <device>.age ...` handles a passphrase-encrypted identity
// file), the same interop property .age-recipients has by being a plain
// list of keys.
func formatIdentityFile(ident *age.X25519Identity, now time.Time) []byte {
	// Assembled with Sprintf rather than Fprintf even though the
	// destination is an in-memory buffer: the library layer's no-writers
	// rule is enforced by a linter that (correctly) can't tell a
	// bytes.Buffer from a terminal, and the rule is worth more than the
	// convenience.
	var b bytes.Buffer
	b.WriteString(fmt.Sprintf("# created: %s\n", now.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("# public key: %s\n", ident.Recipient().String()))
	b.WriteString(ident.String())
	b.WriteByte('\n')
	return b.Bytes()
}

// parseIdentityFile pulls the secret key line out of a decrypted identity
// file. It returns the parsed key and a freshly allocated copy of the key
// line — the buffer the caller page-locks and eventually zeroes — so the
// decrypted plaintext can be wiped immediately afterwards.
func parseIdentityFile(plaintext []byte) (*age.X25519Identity, []byte, error) {
	for _, raw := range bytes.Split(plaintext, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		if !bytes.HasPrefix(line, []byte(identityFileSecretPrefix)) {
			continue
		}
		// This is the one unavoidable string copy of the secret:
		// age.ParseX25519Identity takes a string, and a Go string can be
		// neither page-locked nor zeroed. Splitting on bytes above keeps
		// it to exactly one such copy rather than one per line plus a
		// copy of the whole plaintext.
		ident, err := age.ParseX25519Identity(string(line))
		if err != nil {
			return nil, nil, exitcode.Wrap(exitcode.Conflict,
				fmt.Errorf("%w: its key line does not parse: %v", ErrCorruptIdentityFile, err))
		}
		// Allocated through memlock rather than bytes.Clone so this key
		// owns every page it sits on: page locks are not reference
		// counted, so a second key sharing this page would have its
		// protection released by whichever Identity closed first. See
		// memlock.Alloc.
		secret := memlock.Alloc(len(line))
		copy(secret, line)
		return ident, secret, nil
	}
	return nil, nil, exitcode.Wrap(exitcode.Conflict,
		fmt.Errorf("%w: it contains no %s\u2026 line", ErrCorruptIdentityFile, identityFileSecretPrefix))
}
