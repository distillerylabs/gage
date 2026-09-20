package gage

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/distillerylabs/gage/internal/gage/agekey"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/recipients"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
)

func TestNewRecoveryKeyReturnsAValidRecipient(t *testing.T) {
	k, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := agekey.ValidateRecipient(k.Pubkey); err != nil {
		t.Fatalf("Pubkey %q is not a valid recipient: %v", k.Pubkey, err)
	}
	if !strings.HasPrefix(string(k.Secret), "AGE-SECRET-KEY-1") {
		t.Errorf("Secret does not look like an age secret key")
	}
}

func TestNewRecoveryKeySecretParsesBackToTheSamePubkey(t *testing.T) {
	k, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	ident, err := age.ParseX25519Identity(string(k.Secret))
	if err != nil {
		t.Fatal(err)
	}
	if got := ident.Recipient().String(); got != k.Pubkey {
		t.Errorf("secret's recipient = %s, want %s", got, k.Pubkey)
	}
}

func TestNewRecoveryKeyIsDifferentEachCall(t *testing.T) {
	a, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	if a.Pubkey == b.Pubkey || bytes.Equal(a.Secret, b.Secret) {
		t.Error("two calls returned the same key")
	}
}

func TestZeroingRecoverySecretLeavesPubkeyIntact(t *testing.T) {
	k, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	want := k.Pubkey
	zero(k.Secret)
	if k.Pubkey != want {
		t.Errorf("Pubkey changed after zeroing Secret: %q, want %q", k.Pubkey, want)
	}
	for _, b := range k.Secret {
		if b != 0 {
			t.Fatal("Secret not fully zeroed")
		}
	}
}

func requireSingleCommit(t *testing.T, dir string) {
	t.Helper()
	n, err := gitrepo.CommitCount(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("commit count = %d, want 1", n)
	}
}

func recoverySpec(t *testing.T, k RecoveryKey) CreateSpec {
	t.Helper()
	spec := validSpec(t, "myvault")
	spec.ExtraRecipients = []LabelledRecipient{{Device: RecoveryDeviceLabel, Pubkey: k.Pubkey}}
	return spec
}

func TestCreateWritesLabelledExtraRecipientToBothFilesInOneCommit(t *testing.T) {
	k, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	spec := recoverySpec(t, k)
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}

	vf, err := vaultconfig.Read(filepath.Join(spec.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(vf.Recipients) != 2 ||
		vf.Recipients[0].Device != "laptop-1" || vf.Recipients[0].Pubkey != testRecipient1 ||
		vf.Recipients[1].Device != RecoveryDeviceLabel || vf.Recipients[1].Pubkey != k.Pubkey {
		t.Errorf("config recipients = %+v", vf.Recipients)
	}
	keys, err := recipients.Read(filepath.Join(spec.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != testRecipient1 || keys[1] != k.Pubkey {
		t.Errorf(".age-recipients = %v", keys)
	}
	requireSingleCommit(t, spec.Path)
}

func TestCreateOrdersDeviceThenLabelledExtrasThenGenericRecipients(t *testing.T) {
	k, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	spec := recoverySpec(t, k)
	spec.Recipients = []string{testRecipient1, testRecipient2}
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}
	vf, err := vaultconfig.Read(filepath.Join(spec.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := []vaultconfig.Recipient{
		{Device: "laptop-1", Pubkey: testRecipient1},
		{Device: RecoveryDeviceLabel, Pubkey: k.Pubkey},
		{Device: "recipient-2", Pubkey: testRecipient2},
	}
	if len(vf.Recipients) != len(want) {
		t.Fatalf("recipients = %+v, want %+v", vf.Recipients, want)
	}
	for i := range want {
		if vf.Recipients[i] != want[i] {
			t.Errorf("recipients[%d] = %+v, want %+v", i, vf.Recipients[i], want[i])
		}
	}
	keys, err := recipients.Read(filepath.Join(spec.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if keys[i] != want[i].Pubkey {
			t.Errorf(".age-recipients[%d] = %s, want %s", i, keys[i], want[i].Pubkey)
		}
	}
}

func TestCreateRejectsDuplicateOrInvalidExtraRecipients(t *testing.T) {
	k, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*CreateSpec)
		want   error
		code   exitcode.Code
	}{
		{"pubkey duplicates the device key", func(s *CreateSpec) {
			s.ExtraRecipients[0].Pubkey = testRecipient1
		}, ErrRecipientExists, exitcode.Conflict},
		{"pubkey duplicates a --recipient key", func(s *CreateSpec) {
			s.Recipients = []string{testRecipient1, k.Pubkey}
		}, ErrRecipientExists, exitcode.Conflict},
		{"pubkey duplicates another extra", func(s *CreateSpec) {
			s.ExtraRecipients = append(s.ExtraRecipients, LabelledRecipient{Device: "second", Pubkey: k.Pubkey})
		}, ErrRecipientExists, exitcode.Conflict},
		{"label duplicates another extra", func(s *CreateSpec) {
			s.ExtraRecipients = append(s.ExtraRecipients, LabelledRecipient{Device: RecoveryDeviceLabel, Pubkey: other.Pubkey})
		}, ErrRecipientExists, exitcode.Conflict},
		{"device named like the recovery label", func(s *CreateSpec) {
			s.Device = RecoveryDeviceLabel
		}, ErrRecipientExists, exitcode.Conflict},
		{"label collides with a generic recipient-N label", func(s *CreateSpec) {
			s.Recipients = []string{testRecipient1, testRecipient2}
			s.ExtraRecipients[0].Device = "recipient-2"
		}, ErrRecipientExists, exitcode.Conflict},
		{"extra label is not a valid device name", func(s *CreateSpec) {
			s.ExtraRecipients[0].Device = "Not Valid!"
		}, nil, exitcode.Usage},
		{"extra pubkey is malformed", func(s *CreateSpec) {
			s.ExtraRecipients[0].Pubkey = "age1nope"
		}, nil, exitcode.Usage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := recoverySpec(t, k)
			tc.mutate(&spec)
			_, err := Create(spec)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("errors.Is(err, %v) = false: %v", tc.want, err)
			}
			if got := exitcode.CodeOf(err); got != tc.code {
				t.Errorf("CodeOf(err) = %v, want %v", got, tc.code)
			}
			requireNothingCreated(t, spec.Path)
		})
	}
}

func TestCreateWithoutExtraRecipientsStillAllowsRepeatedRecipientKeys(t *testing.T) {
	// Pinned deliberately: duplicate checking is scoped to ExtraRecipients
	// so --recipient behavior is unchanged (see
	// TestCreateRepeatedRecipientsAllWritten).
	spec := validSpec(t, "myvault")
	spec.Recipients = []string{testRecipient1, testRecipient1}
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}
}

func TestEntryEncryptedToTheVaultDecryptsWithTheRecoverySecretAlone(t *testing.T) {
	k, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	spec := recoverySpec(t, k)
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}
	keys, err := recipients.Read(filepath.Join(spec.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	var to []Recipient
	for _, key := range keys {
		r, err := ParseRecipient(key)
		if err != nil {
			t.Fatal(err)
		}
		to = append(to, r)
	}
	ct, err := Encrypt([]byte("hunter2"), to...)
	if err != nil {
		t.Fatal(err)
	}
	ident, err := age.ParseX25519Identity(string(k.Secret))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := decryptBytes(ct, ident)
	if err != nil {
		t.Fatalf("recovery secret could not decrypt: %v", err)
	}
	if string(pt) != "hunter2" {
		t.Errorf("plaintext = %q", pt)
	}
}

func TestVerifyRecoveryKey(t *testing.T) {
	k, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	spec := recoverySpec(t, k)
	v, err := Create(spec)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("right key passes", func(t *testing.T) {
		if err := v.VerifyRecoveryKey(k.Secret); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("surrounding whitespace from a paste is ignored", func(t *testing.T) {
		padded := append(append([]byte("  "), k.Secret...), '\n')
		if err := v.VerifyRecoveryKey(padded); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("valid key that is not a recipient", func(t *testing.T) {
		err := v.VerifyRecoveryKey(stranger.Secret)
		if !errors.Is(err, ErrNotARecipient) {
			t.Fatalf("err = %v, want ErrNotARecipient", err)
		}
		if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
			t.Errorf("CodeOf = %v, want LockedOrAuth", exitcode.CodeOf(err))
		}
	})
	t.Run("malformed key", func(t *testing.T) {
		err := v.VerifyRecoveryKey([]byte("not a key"))
		if !errors.Is(err, ErrMalformedRecoveryKey) {
			t.Fatalf("err = %v, want ErrMalformedRecoveryKey", err)
		}
		if exitcode.CodeOf(err) != exitcode.Usage {
			t.Errorf("CodeOf = %v, want Usage", exitcode.CodeOf(err))
		}
	})
	t.Run("error text never contains the secret", func(t *testing.T) {
		err := v.VerifyRecoveryKey(append([]byte("x"), k.Secret...))
		if err == nil || strings.Contains(err.Error(), string(k.Secret)) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("does not modify the input", func(t *testing.T) {
		in := append([]byte(nil), k.Secret...)
		_ = v.VerifyRecoveryKey(in)
		if !bytes.Equal(in, k.Secret) {
			t.Error("input mutated")
		}
	})
}
