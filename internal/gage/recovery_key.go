package gage

import (
	"bytes"
	"errors"
	"fmt"

	"filippo.io/age"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// RecoveryDeviceLabel is the fixed recipient label for a vault's offline
// recovery key. It is a constant rather than a choice so that `init`,
// `recovery verify`, and the docs all name the same thing — see the
// recovery-key plan's D6.
const RecoveryDeviceLabel = "recovery-paper-key"

// ErrMalformedRecoveryKey is VerifyRecoveryKey being handed something
// that is not an age secret key. The error text never includes the input:
// a malformed paste may still be most of a real secret.
var ErrMalformedRecoveryKey = errors.New("gage: that is not a valid age secret key")

// RecoveryKey is a freshly generated recovery keypair. Pubkey is what
// goes into the vault's recipient list; Secret is the private key, which
// the caller shows once and must zero afterwards. Nothing here is ever
// written to disk by the library.
type RecoveryKey struct {
	Pubkey string
	// Secret is the "AGE-SECRET-KEY-1..." text as bytes, so the caller can
	// zero it. The string age hands back while it is being built cannot be
	// zeroed — the same accepted gap Identity documents.
	Secret []byte
}

// NewRecoveryKey generates a new X25519 keypair for use as an offline
// recovery recipient. It performs no I/O.
func NewRecoveryKey() (RecoveryKey, error) {
	ident, err := age.GenerateX25519Identity()
	if err != nil {
		return RecoveryKey{}, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: generating recovery key: %w", err))
	}
	return RecoveryKey{
		Pubkey: ident.Recipient().String(),
		Secret: []byte(ident.String()),
	}, nil
}

// VerifyRecoveryKey reports whether secret is the private half of one of
// this vault's recipients, and returns the label it is registered under.
// It reads only the plaintext recipient list, so it needs no unlock and
// decrypts nothing: it proves a stored copy of the key is readable and
// matches, not that the vault's entries are intact.
//
// The label is returned rather than swallowed because being *a* recipient
// and being the key RecoverDevice will accept are two different claims —
// only the one labelled RecoveryDeviceLabel can enroll a device — and a
// caller that could not tell them apart would report a key as fine and
// then watch it be refused where it mattered.
//
// Surrounding whitespace is ignored, since the key usually arrives by
// paste. secret is neither modified nor retained — deliberately unlike
// RecoverDevice, which zeroes what it is handed. The difference is how
// long each holds the key: this returns immediately having only compared
// a public key, while RecoverDevice keeps the secret live across a lock,
// a full re-encryption and a push, and is the one worth erasing eagerly.
func (v *Vault) VerifyRecoveryKey(secret []byte) (string, error) {
	ident, err := age.ParseX25519Identity(string(bytes.TrimSpace(secret)))
	if err != nil {
		// err is deliberately not wrapped in: age's parse errors can quote
		// the input.
		return "", exitcode.Wrap(exitcode.Usage, ErrMalformedRecoveryKey)
	}
	want := ident.Recipient().String()

	rs, err := v.Recipients()
	if err != nil {
		return "", err
	}
	for _, r := range rs {
		if r.Pubkey == want {
			return r.Device, nil
		}
	}
	return "", exitcode.Wrap(exitcode.LockedOrAuth,
		fmt.Errorf("%w: its public key %s is not in vault %q's recipient list", ErrNotARecipient, want, v.Name))
}
