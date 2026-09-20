package gage

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/distillerylabs/gage/internal/gage/agekey"
	"github.com/distillerylabs/gage/internal/gage/devicename"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// ErrNotRecoveryKey is a key that parses, and may even be a recipient of
// this vault, but is not the one labelled RecoveryDeviceLabel.
//
// The distinction is the feature's whole boundary. A recovery key is a
// bare secret whose only protection is where its owner keeps it, and the
// single thing it may do inside gage is enroll a replacement device (see
// RecoverDevice). A *device's* key is wrapped, passphrase-protected, and
// reaches gage through Unlock. Accepting a device key here would turn
// this into a general "unlock from a pasted key" path and quietly discard
// that protection — so a device key is refused even though it would
// decrypt everything perfectly well.
var ErrNotRecoveryKey = errors.New("gage: that key is not this vault's recovery key")

// RecoverSpec is the change RecoverDevice makes to a vault's recipient
// list, in one commit.
type RecoverSpec struct {
	// Device and Pubkey are the replacement device being enrolled. The
	// caller has already generated the identity; this is only its public
	// half and the label it will carry.
	Device string
	Pubkey string

	// Replaces optionally names a recipient to remove in the same commit
	// — the lost device, whose wrapped key may be in someone else's
	// hands. Empty leaves every other recipient alone.
	//
	// It may not name the recovery key: that one is retired
	// unconditionally, and naming it would read as if not naming it were
	// a way to keep it.
	Replaces string

	// NewRecoveryPubkey is the replacement recovery key, registered under
	// the same fixed label the retired one used. Empty leaves the vault
	// with no recovery recipient at all, which is a thing a caller may
	// deliberately ask for and never a thing it gets by default.
	NewRecoveryPubkey string
}

// RecoverResult reports what the swap did, for a frontend to render.
type RecoverResult struct {
	Device string
	Pubkey string

	// RetiredRecovery is the public key of the recovery recipient that
	// was used to authorize this and removed by it.
	RetiredRecovery string
	// NewRecoveryPubkey is the replacement, or empty if none was asked
	// for.
	NewRecoveryPubkey string
	// Replaced is the device removed via RecoverSpec.Replaces, or empty.
	Replaced string

	Commit      string
	Reencrypted int
}

// RecoverDevice is the recovery key's one power inside gage: it enrolls a
// replacement device and retires itself, in a single commit.
//
// The shape is deliberate. `identity add` cannot touch the recipient list
// — a device that has lost its key cannot authorize itself — so something
// else has to say "yes, admit this key", and on a machine whose identity
// file is gone the only thing left that can is the recovery key. This is
// that step, and it is the *only* thing the recovery key can do here:
// there is no exported way to obtain the Identity built below, so it
// cannot leak into ordinary CRUD.
//
// Retiring the key in the same commit is not tidiness. By the time this
// runs the secret has been typed into a terminal, and it is a full-read
// credential for everything the vault holds. Leaving it live would trade
// a lost identity file for a permanent one.
//
// secret is zeroed before this returns, on every path including a
// refusal: the caller pasted it in and has no way to know when the
// library has finished with it.
func (v *Vault) RecoverDevice(spec RecoverSpec, secret []byte, p Prompter) (RecoverResult, error) {
	defer zero(secret)

	// Before anything is read, for the same reason Unlock pulls first: the
	// recipient list this checks the key against, and the entries it is
	// about to rewrite, should be the current ones. This path skips
	// Unlock, so without it a recovery run could re-encrypt on a stale tip
	// — or refuse a key that a rotation elsewhere has already made
	// current.
	v.syncOnUnlock(p)

	ident, err := v.recoveryIdentity(secret, p)
	if err != nil {
		return RecoverResult{}, err
	}
	defer func() { _ = ident.Close() }()

	if err := v.checkRecoverSpec(spec); err != nil {
		return RecoverResult{}, err
	}

	add := []VaultRecipient{{Device: spec.Device, Pubkey: spec.Pubkey}}
	if spec.NewRecoveryPubkey != "" {
		add = append(add, VaultRecipient{Device: RecoveryDeviceLabel, Pubkey: spec.NewRecoveryPubkey})
	}
	remove := []string{RecoveryDeviceLabel}
	if spec.Replaces != "" {
		remove = append(remove, spec.Replaces)
	}

	res := RecoverResult{
		Device:            spec.Device,
		Pubkey:            spec.Pubkey,
		RetiredRecovery:   ident.Recipient(),
		NewRecoveryPubkey: spec.NewRecoveryPubkey,
		Replaced:          spec.Replaces,
	}
	n, hash, err := v.swapRecipients(recipientSwap{
		add:    add,
		remove: remove,
		message: fmt.Sprintf("gage: recovery enroll %s, retire %s (reencrypt)",
			spec.Device, RecoveryDeviceLabel),
	}, &ident)
	if err != nil {
		return RecoverResult{}, err
	}
	res.Commit, res.Reencrypted = hash, n
	return res, nil
}

// checkRecoverSpec rejects what the caller asked for before any lock is
// taken. Collisions against the existing list are left to swapRecipients,
// which checks against the *final* list — rebuilding a machine under the
// name it always had is the ordinary case, and a check against the
// current list would refuse it.
func (v *Vault) checkRecoverSpec(spec RecoverSpec) error {
	if !devicename.Valid(spec.Device) {
		return exitcode.Newf(exitcode.Usage, "gage: device name %q is invalid", spec.Device)
	}
	if spec.Device == RecoveryDeviceLabel {
		return exitcode.Newf(exitcode.Usage,
			"gage: %q is the name gage gives a vault's recovery key, so a device can't use it",
			RecoveryDeviceLabel)
	}
	if err := agekey.ValidateRecipient(spec.Pubkey); err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}
	if spec.NewRecoveryPubkey != "" {
		if err := agekey.ValidateRecipient(spec.NewRecoveryPubkey); err != nil {
			return exitcode.Wrap(exitcode.Usage, err)
		}
	}
	if spec.Replaces == RecoveryDeviceLabel {
		return exitcode.Newf(exitcode.Usage,
			"gage: the recovery key is retired by this operation whether or not it is named; "+
				"--replaces is for the device that was lost")
	}
	return nil
}

// recoveryIdentity turns a pasted recovery secret into an Identity that
// can decrypt this vault.
//
// It is unexported, and that is the enforcement rather than a convention:
// Identity's fields are unexported too, so outside this package there is
// no way to obtain one of these at all. The only callers are the
// recovery verbs in this file, each of which Closes it before returning.
//
// The key must be the recipient labelled RecoveryDeviceLabel — see
// ErrNotRecoveryKey for why a device key that would decrypt just as well
// is refused anyway.
func (v *Vault) recoveryIdentity(secret []byte, p Prompter) (Identity, error) {
	// Reuses the identity-file parser rather than age.ParseX25519Identity
	// directly: it already returns the key alongside a memlock.Alloc'd
	// copy of the key line, which is the buffer newIdentity page-locks and
	// Close zeroes. Going through age directly would leave the secret in
	// an ordinary heap slice with neither protection.
	ident, owned, err := parseIdentityFile(bytes.TrimSpace(secret))
	if err != nil {
		// Reported as a malformed recovery key rather than as a corrupt
		// identity file: there is no file here, and the caller's next move
		// is to check what they pasted. The underlying error is dropped,
		// not wrapped, because age's parse errors can quote their input.
		return Identity{}, exitcode.Wrap(exitcode.Usage, ErrMalformedRecoveryKey)
	}

	pubkey := ident.Recipient().String()
	rs, err := v.Recipients()
	if err != nil {
		zero(owned)
		return Identity{}, err
	}
	for _, r := range rs {
		if r.Pubkey != pubkey {
			continue
		}
		if r.Device != RecoveryDeviceLabel {
			zero(owned)
			return Identity{}, exitcode.Wrap(exitcode.LockedOrAuth, fmt.Errorf(
				"%w: it is %q's key, and only the %q key can enroll a device this way",
				ErrNotRecoveryKey, r.Device, RecoveryDeviceLabel))
		}
		return v.newIdentity(RecoveryDeviceLabel, ident, owned, p), nil
	}

	zero(owned)
	return Identity{}, exitcode.Wrap(exitcode.LockedOrAuth, fmt.Errorf(
		"%w: its public key %s is not a recipient of vault %q", ErrNotRecoveryKey, pubkey, v.Name))
}

// recipientSwap is one atomic change to the recipient list: some labels
// removed, some added, in a single commit.
type recipientSwap struct {
	add     []VaultRecipient
	remove  []string // device labels
	message string
}

// swapRecipients applies a swap: remove, add, re-encrypt, commit, push —
// all of it or none of it.
//
// It exists because add-then-remove is not the same operation. Running
// AddRecipient and then RemoveRecipient would re-encrypt the whole vault
// twice, produce two commits, and leave a window in which both the old
// and the new recovery key are live; and the label is fixed, so the new
// recovery key could not even be added before the old one was gone.
// RemoveRecipient is also the wrong shape here for a second reason: its
// confirmSelfRemoval would warn that "this device" is losing access,
// which is exactly what retirement means and not at all what that warning
// is for.
//
// What it does not do is skip any of the checks the other recipient verbs
// make. M10's precondition and its blocking question both run, in the
// same order and for the same reasons as in addRecipientsLocked: a
// recovery run is still a recipient change, and being the recovery key is
// not a reason to bless a list nobody reviewed.
//
// It runs with the vault write lock held, taken here.
func (v *Vault) swapRecipients(sw recipientSwap, ident *Identity) (reencrypted int, commit string, err error) {
	err = v.withVaultWrite(ident.warnTo(), func() error {
		// A19's pre-flight. Unlike AddRecipient this always runs inside the
		// lock rather than before it: withVaultWrite has just reset the
		// working tree, so this reads the state the re-encryption will
		// actually read, and there is no clean/dirty case to get wrong. The
		// ordering property AddRecipient's comment cares about is preserved
		// — it is still ahead of the first byte written and of every
		// question asked.
		if err := v.RequireFullAccess(ident); err != nil {
			return err
		}
		// M10's precondition: these verbs rebuild .age-recipients from
		// config.toml, so running one over a divergence would erase the
		// stray key and commit the result as an ordinary change.
		if err := v.requireRecipientsInSync(); err != nil {
			return err
		}
		// M10's blocking question, before the first byte, so a declined
		// answer leaves nothing to undo.
		if err := v.confirmRecipientTrust(ident.frontend()); err != nil {
			return err
		}

		current, err := v.Recipients()
		if err != nil {
			return err
		}
		updated, err := applySwap(current, sw)
		if err != nil {
			return err
		}

		// Said before the work rather than after: by the time the commit
		// lands there is nothing left to reconsider, and what the warning
		// is for is that revocation is narrower than it sounds.
		for _, label := range sw.remove {
			warn(ident.warnTo(),
				"gage: removing %q revokes future access only — it still opens every version of "+
					"every entry already in this vault's git history.", label)
		}

		n, hash, err := v.commitRecipientList(updated, recipientWrite{message: sw.message}, ident)
		if err != nil {
			return err
		}
		reencrypted, commit = n, hash

		// The operator just reviewed this list by running the command, so
		// M10's cache is regenerated last — otherwise their very next write
		// would warn them about their own change.
		return v.noteRecipientsReviewed()
	})
	if err != nil {
		return 0, "", err
	}
	return reencrypted, commit, nil
}

// applySwap computes the final recipient list, refusing anything the list
// could not legally become. It is pure, so every refusal below happens
// before a single byte is written.
func applySwap(current []VaultRecipient, sw recipientSwap) ([]VaultRecipient, error) {
	updated := make([]VaultRecipient, 0, len(current)+len(sw.add))
	removed := map[string]bool{}
	for _, label := range sw.remove {
		removed[label] = true
	}
	for _, r := range current {
		if removed[r.Device] {
			delete(removed, r.Device)
			continue
		}
		updated = append(updated, r)
	}
	// Whatever is left in removed named nobody. Reported rather than
	// ignored, so a typo in --replaces is not silently a no-op.
	for label := range removed {
		return nil, exitcode.Wrap(exitcode.NotFound,
			fmt.Errorf("%w: %q", ErrRecipientNotFound, label))
	}

	// Checked against the list as it will be, not as it is: replacing a
	// device and reusing its name is the ordinary rebuild case, and a
	// check against the current list would refuse it.
	for _, r := range sw.add {
		if !devicename.Valid(r.Device) {
			return nil, exitcode.Newf(exitcode.Usage, "gage: device name %q is invalid", r.Device)
		}
		if err := agekey.ValidateRecipient(r.Pubkey); err != nil {
			return nil, exitcode.Wrap(exitcode.Usage, err)
		}
		for _, existing := range updated {
			switch {
			case existing.Pubkey == r.Pubkey:
				return nil, exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
					"%w: %s is already listed as %q", ErrRecipientExists, r.Pubkey, existing.Device))
			case existing.Device == r.Device:
				return nil, exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
					"%w: %q is already a recipient of this vault", ErrRecipientExists, r.Device))
			}
		}
		updated = append(updated, r)
	}

	if len(updated) == 0 {
		return nil, exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
			"%w: this change would leave nothing to encrypt to, and the re-encryption would "+
				"rewrite every entry to a list no key opens", ErrLastRecipient))
	}
	return updated, nil
}
