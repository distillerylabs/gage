package gage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"filippo.io/age"

	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// maxUnlockAttempts is a runaway guard, not a retry policy. Retry policy
// belongs to cmd/gage (see "Library architecture"), and it expresses
// itself by having its Prompter stop answering — the loop below ends the
// moment a Prompter returns an error. This ceiling only exists so a
// Prompter that never stops answering (a fake in a test, a scripted
// frontend with a bug) fails instead of spinning forever. A real policy
// stops one to two orders of magnitude below it.
const maxUnlockAttempts = 100

// Unlock runs the method-specific unlock flow for *this device* and
// returns an Identity the caller threads through every subsequent Vault
// call. Callers own calling Identity.Close when they're done with it —
// Vault.Unlock never does so itself.
//
// Retries re-enter through the Prompter rather than looping inside one
// call with a fixed count: each attempt is a fresh Prompter.Unlock with
// an incremented Attempt, and the loop ends when the Prompter answers
// with an error. That keeps "how many tries does a human get" a cmd/gage
// decision, exactly as the design requires, while still letting the
// Prompter render "wrong passphrase, try again" — which it can only do if
// it's told which attempt this is. Only a wrong passphrase retries; every
// other failure returns immediately, since re-asking for a passphrase
// can't fix a corrupt file or a missing one.
func (v *Vault) Unlock(p Prompter) (Identity, error) {
	// The automatic catch-up, before anything is read: this is the hook
	// point both invocation modes share, so wiring the pull here is what
	// makes "every vault unlock fetches" true of a one-shot command's
	// implicit unlock and a session's `use` alike, rather than of
	// whichever callers remembered. It never fails the unlock — see
	// syncOnUnlock.
	v.syncOnUnlock(p)

	device, method, err := v.localIdentityRecord()
	if err != nil {
		return Identity{}, err
	}
	if method != MethodPassphrase {
		return Identity{}, exitcode.Wrap(exitcode.LockedOrAuth,
			fmt.Errorf("%w: this device unlocks %q with method %q, which this build cannot perform",
				ErrUnsupportedMethod, v.Name, method))
	}

	path, err := IdentityFilePath(v.Name, device)
	if err != nil {
		return Identity{}, err
	}
	// #nosec G304 -- path comes from IdentityFilePath, which validates
	// both components against the traversal rule (Q-DEVICE-NAME) before
	// constructing anything.
	wrapped, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Identity{}, exitcode.Wrap(exitcode.LockedOrAuth,
				fmt.Errorf("%w: expected a wrapped identity at %s", ErrNoLocalIdentity, path))
		}
		return Identity{}, exitcode.Wrap(exitcode.Internal, err)
	}

	plaintext, err := decryptIdentityFile(v.Name, device, wrapped, p)
	if err != nil {
		return Identity{}, err
	}

	ident, secret, err := parseIdentityFile(plaintext)
	// The decrypted file has served its purpose the moment the key line
	// has been copied out of it.
	zero(plaintext)
	if err != nil {
		return Identity{}, err
	}

	unlocked := v.newIdentity(device, ident, secret, p)

	// The opportunistic, non-blocking half of M10's trust cache, and the
	// first-use bootstrap that goes with it. It runs here, on the success
	// path only, for three reasons: this is the hook both invocation
	// modes share (a session's `use` and every one-shot command's
	// implicit unlock), it is after syncOnUnlock so it sees the list that
	// was just pulled rather than the one from before, and a *failed*
	// unlock must never bootstrap an approval — a wrong passphrase is not
	// a review. It never asks and never regenerates the cache; the
	// blocking check before the next encrypt does both.
	v.warnRecipientTrust(p)

	return unlocked, nil
}

// decryptIdentityFile runs the passphrase exchange until the file opens,
// the Prompter gives up, or a failure that re-prompting cannot fix.
//
// It takes a vault name rather than a *Vault because CreateIdentity's
// reuse path (see reuseIdentity) needs it before any Vault exists — the
// identity file it's opening may be all that survives of a vault `vault
// remove` only ever forgot the local registration for.
func decryptIdentityFile(vault, device string, wrapped []byte, p Prompter) ([]byte, error) {
	// wrongSoFar records that at least one attempt was a genuinely wrong
	// passphrase, so that when the Prompter finally stops answering the
	// error says *why* it was being asked again rather than only that it
	// gave up. Callers match on ErrWrongPassphrase; the prompter's own
	// error is wrapped alongside it so a CLI can still report both.
	wrongSoFar := false
	for attempt := 1; attempt <= maxUnlockAttempts; attempt++ {
		passphrase, err := requestPassphrase(p, UnlockRequest{
			Kind:    KindPassphrase,
			Purpose: PurposeUnlock,
			Vault:   vault,
			Device:  device,
			Attempt: attempt,
		})
		if err != nil {
			if wrongSoFar {
				return nil, exitcode.Wrap(exitcode.LockedOrAuth,
					fmt.Errorf("%w: %w", ErrWrongPassphrase, err))
			}
			return nil, err
		}

		scryptID, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			return nil, exitcode.Wrap(exitcode.LockedOrAuth, fmt.Errorf("gage: %w", err))
		}
		scryptID.SetMaxWorkFactor(scryptMaxWorkFactor)

		plaintext, err := decryptBytes(wrapped, scryptID)
		if err == nil {
			return plaintext, nil
		}
		if !isWrongPassphrase(err) {
			return nil, asCorruptIdentityFile(err)
		}
		wrongSoFar = true
		// Fall through and ask again; the Prompter decides when to stop.
	}
	return nil, exitcode.Wrap(exitcode.LockedOrAuth,
		fmt.Errorf("%w: giving up after %d attempts; the prompter never stopped answering",
			ErrWrongPassphrase, maxUnlockAttempts))
}

// isWrongPassphrase decides whether a failed decrypt means "the human
// typed the wrong thing" or "this file is not what it should be."
//
// age reports both as a no-identity-match at the header stage, so the
// distinction is made on the file's own recipient stanzas: a
// passphrase-wrapped identity file is scrypt-only by construction (see
// A3), so a file whose stanzas aren't scrypt didn't fail because of the
// passphrase and must not cause a re-prompt.
func isWrongPassphrase(err error) bool {
	if !errors.Is(err, ErrNotARecipient) {
		return false
	}
	var noMatch *age.NoIdentityMatchError
	if !errors.As(err, &noMatch) {
		return false
	}
	for _, t := range noMatch.StanzaTypes {
		if t != "scrypt" {
			return false
		}
	}
	// A header with no stanzas at all can't reach here — age's own parser
	// rejects it as a malformed header long before any identity is tried,
	// so this is unreachable defensive code rather than a case with a
	// test. It stays because the alternative reading of an empty list
	// ("every stanza was scrypt") is the wrong one, and a future age
	// version that admits such a file should get the safe answer.
	return len(noMatch.StanzaTypes) > 0
}

// asCorruptIdentityFile relabels a crypto-layer failure as an
// identity-file failure, so callers distinguishing the three unlock
// outcomes match on ErrCorruptIdentityFile rather than having to know
// which of the wrapper's errors can surface here.
func asCorruptIdentityFile(err error) error {
	return exitcode.Wrap(exitcode.Conflict, fmt.Errorf("%w: %v", ErrCorruptIdentityFile, err))
}

// newIdentity assembles the unlocked Identity and takes the page lock on
// its key material. A lock failure warns exactly once — one Lock call per
// unlock, so "once per unlock" is structural rather than a counter that
// could drift — and proceeds with an unlocked-but-usable Identity, per
// "Session model"'s warn-and-proceed rule. Refusing to unlock at all
// because a container won't allow mlock would make gage unusable in
// exactly the environments people most often run it in.
func (v *Vault) newIdentity(device string, ident *age.X25519Identity, secret []byte, p Prompter) Identity {
	locker := lockerOrDefault(v.locker)
	pageLocked := true
	if err := locker.Lock(secret); err != nil {
		pageLocked = false
		p.Warn(fmt.Sprintf(
			"gage: could not lock %s's key material into memory (%v); continuing without it, "+
				"so this key may be written to swap. Raise the locked-memory limit (ulimit -l) to remove this warning.",
			device, err))
	}
	return Identity{
		device: device,
		st: &identityState{
			secret:     secret,
			ident:      ident,
			recipient:  ident.Recipient().String(),
			prompter:   p,
			locker:     locker,
			pageLocked: pageLocked,
		},
	}
}

// localIdentityRecord reads this device's name and unlock method for this
// vault out of global config.
//
// The method deliberately comes from here and not from the vault's
// committed [method].default. Per Q-METHOD-SCOPE a vault's method is a
// *default for devices joining it*, not a constraint on any of them, so
// the vault's file is the wrong source even though today both values are
// "passphrase" and reading the wrong one would be invisible. Keeping it
// local is also what makes a second method additive later rather than a
// change to how unlocking finds its method at all — and it keeps the
// committed config from advertising which recipient is the softest target.
func (v *Vault) localIdentityRecord() (device, method string, err error) {
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		return "", "", exitcode.Wrap(exitcode.Internal, err)
	}
	g, err := config.Read(filepath.Join(dir, "config.toml"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", exitcode.Wrap(exitcode.LockedOrAuth,
				fmt.Errorf("%w: no vault is registered on this machine", ErrNoLocalIdentity))
		}
		return "", "", exitcode.Wrap(exitcode.Internal, err)
	}
	entry, ok := g.Vaults[v.Name]
	if !ok {
		return "", "", exitcode.Wrap(exitcode.LockedOrAuth,
			fmt.Errorf("%w: %q is not registered on this machine", ErrNoLocalIdentity, v.Name))
	}
	if entry.Device == "" || entry.Method == "" {
		return "", "", exitcode.Wrap(exitcode.LockedOrAuth,
			fmt.Errorf("%w: %q's registration records no device/method for this machine", ErrNoLocalIdentity, v.Name))
	}
	return entry.Device, entry.Method, nil
}

// requestPassphrase runs one Prompter exchange and validates the answer's
// shape, so no caller has to re-check that a passphrase response actually
// carries a passphrase.
func requestPassphrase(p Prompter, req UnlockRequest) (string, error) {
	if p == nil {
		return "", exitcode.Wrap(exitcode.Internal, errors.New("gage: no prompter was supplied to unlock with"))
	}
	resp, err := p.Unlock(req)
	if err != nil {
		return "", exitcode.Wrap(exitcode.LockedOrAuth, err)
	}
	if resp.Kind != KindPassphrase {
		return "", exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: prompter answered a %q request with a %q response", req.Kind, resp.Kind))
	}
	if resp.Passphrase == "" {
		return "", exitcode.Wrap(exitcode.LockedOrAuth, errors.New("gage: an empty passphrase is not accepted"))
	}
	return resp.Passphrase, nil
}
