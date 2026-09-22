package gage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
)

// ---------------------------------------------------------------------
// Fixture: a vault with a device and a recovery recipient, the shape
// `gage init` now produces.
// ---------------------------------------------------------------------

type recoverFixture struct {
	v     *Vault
	owner testDevice
	key   RecoveryKey
}

func newRecoverFixture(t *testing.T, name, device string) recoverFixture {
	t.Helper()
	d := newTestDevice(t, name, device)
	key, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}

	var v *Vault
	withXDGRoot(t, d.root, func() {
		v, err = Create(CreateSpec{
			Name:       name,
			ID:         d.vaultID,
			Path:       filepath.Join(t.TempDir(), name),
			Type:       TypeGit,
			Method:     MethodPassphrase,
			Device:     device,
			Recipients: []string{d.pubkey},
			ExtraRecipients: []LabelledRecipient{
				{Device: RecoveryDeviceLabel, Pubkey: key.Pubkey},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	return recoverFixture{v: v, owner: d, key: key}
}

// freshPubkey is a public key belonging to nothing in particular — the
// stand-in for "the device being enrolled" in tests that don't need the
// private half.
func freshPubkey(t *testing.T) string {
	t.Helper()
	k, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	return k.Pubkey
}

// recoverAs runs RecoverDevice under the owner's XDG root, which is where
// the vault is registered.
func recoverAs(t *testing.T, f recoverFixture, spec RecoverSpec, secret []byte, p Prompter) (RecoverResult, error) {
	t.Helper()
	var (
		res RecoverResult
		err error
	)
	withXDGRoot(t, f.owner.root, func() {
		res, err = f.v.RecoverDevice(spec, secret, p)
	})
	return res, err
}

// labels reports the recipient list as "device=pubkey" pairs in order, so
// a test can assert the whole list rather than poke at it one entry at a
// time.
func labels(t *testing.T, v *Vault) []string {
	t.Helper()
	rs, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Device+"="+r.Pubkey)
	}
	return out
}

func deviceLabels(t *testing.T, v *Vault) []string {
	t.Helper()
	rs, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Device)
	}
	return out
}

// decryptsWith reports whether secret opens every entry in the vault.
func decryptsWith(t *testing.T, v *Vault, secret []byte) bool {
	t.Helper()
	ident, err := age.ParseX25519Identity(strings.TrimSpace(string(secret)))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatal("the vault has no entries, so this proves nothing")
	}
	for _, id := range ids {
		ct, err := os.ReadFile(v.entryPath(id)) // #nosec G304 -- test fixture path
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decryptBytes(ct, ident); err != nil {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------
// recoveryIdentity: which keys are accepted, and what the refusals say
// ---------------------------------------------------------------------

func TestRecoveryIdentityAcceptsOnlyTheRecoveryRecipient(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")

	id, err := f.v.recoveryIdentity(f.key.Secret, &fakePrompter{})
	if err != nil {
		t.Fatalf("the vault's own recovery key was refused: %v", err)
	}
	defer func() { _ = id.Close() }()

	if id.Device() != RecoveryDeviceLabel {
		t.Errorf("Device() = %q, want %q", id.Device(), RecoveryDeviceLabel)
	}
	if id.Recipient() != f.key.Pubkey {
		t.Errorf("Recipient() = %s, want %s", id.Recipient(), f.key.Pubkey)
	}
}

// TestRecoveryIdentityRefusesADeviceKey is D12 in one test: the recovery
// path is for the recovery key, not for any key that happens to be a
// recipient. A device's own key goes through Unlock, which is wrapped and
// passphrase-protected; letting a bare device key in here would make
// "recovery only" a comment rather than a property.
func TestRecoveryIdentityRefusesADeviceKey(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")

	// The owner's private key, read out of its wrapped file.
	var ownerSecret []byte
	withXDGRoot(t, f.owner.root, func() {
		id := unlockAs(t, f.v, f.owner)
		defer func() { _ = id.Close() }()
		ownerSecret = append(ownerSecret, id.st.secret...)
	})

	_, err := f.v.recoveryIdentity(ownerSecret, &fakePrompter{})
	if !errors.Is(err, ErrNotRecoveryKey) {
		t.Fatalf("err = %v, want ErrNotRecoveryKey", err)
	}
	if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("CodeOf = %v, want LockedOrAuth", exitcode.CodeOf(err))
	}
}

func TestRecoveryIdentityRefusesStrangersAndGarbage(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	stranger, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		secret []byte
		want   error
		code   exitcode.Code
	}{
		{"a valid key that is no recipient", stranger.Secret, ErrNotRecoveryKey, exitcode.LockedOrAuth},
		{"garbage", []byte("not a key"), ErrMalformedRecoveryKey, exitcode.Usage},
		{"empty", nil, ErrMalformedRecoveryKey, exitcode.Usage},
		{"a public key", []byte(f.key.Pubkey), ErrMalformedRecoveryKey, exitcode.Usage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.v.recoveryIdentity(tc.secret, &fakePrompter{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got := exitcode.CodeOf(err); got != tc.code {
				t.Errorf("CodeOf = %v, want %v", got, tc.code)
			}
		})
	}
}

// TestRecoveryErrorsNeverQuoteTheSecret: age's own parse errors can echo
// what they were handed, and a malformed paste is usually most of a real
// key.
func TestRecoveryErrorsNeverQuoteTheSecret(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	stranger, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	mangled := append([]byte("X"), f.key.Secret...)

	for _, secret := range [][]byte{stranger.Secret, mangled} {
		_, err := f.v.recoveryIdentity(secret, &fakePrompter{})
		if err == nil {
			t.Fatal("expected a refusal")
		}
		if strings.Contains(err.Error(), string(secret)) {
			t.Errorf("the error quotes the secret: %v", err)
		}
		// The tail alone is enough to be worth not printing.
		if tail := string(secret[len(secret)-12:]); strings.Contains(err.Error(), tail) {
			t.Errorf("the error quotes the end of the secret: %v", err)
		}
	}
}

func TestRecoveryIdentityToleratesPastedWhitespace(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")

	padded := append(append([]byte("  \n"), f.key.Secret...), '\n')
	id, err := f.v.recoveryIdentity(padded, &fakePrompter{})
	if err != nil {
		t.Fatal(err)
	}
	_ = id.Close()
}

// ---------------------------------------------------------------------
// RecoverDevice: the swap itself
// ---------------------------------------------------------------------

func TestRecoverDeviceSwapsTheListInOneCommit(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")
	insertAs(t, f.v, f.owner, "second")

	newDevice := freshPubkey(t)
	newRecovery, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	before := headHash(t, f.v)

	res, err := recoverAs(t, f, RecoverSpec{
		Device:            "laptop-2",
		Pubkey:            newDevice,
		NewRecoveryPubkey: newRecovery.Pubkey,
	}, f.key.Secret, &fakePrompter{})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"laptop-1=" + f.owner.pubkey,
		"laptop-2=" + newDevice,
		RecoveryDeviceLabel + "=" + newRecovery.Pubkey,
	}
	if got := labels(t, f.v); !equalStrings(got, want) {
		t.Errorf("recipients =\n  %v\nwant\n  %v", got, want)
	}

	// Exactly one commit, and it is the one reported.
	n, err := gitrepo.CommitCount(f.v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if head := headHash(t, f.v); head == before {
		t.Fatal("nothing was committed")
	} else if res.Commit != head {
		t.Errorf("reported commit %s, HEAD is %s", res.Commit, head)
	}
	// Create's initial commit, two inserts, and this one.
	if n != 4 {
		t.Errorf("commit count = %d, want 4 — the swap must be a single commit", n)
	}
	if res.Reencrypted != 2 {
		t.Errorf("Reencrypted = %d, want 2", res.Reencrypted)
	}
	if res.RetiredRecovery != f.key.Pubkey {
		t.Errorf("RetiredRecovery = %s, want %s", res.RetiredRecovery, f.key.Pubkey)
	}

	clean, err := gitrepo.IsClean(f.v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("the working tree is dirty after a completed swap")
	}
}

// TestRecoverDeviceRetiresTheUsedKeyForFutureWrites is the security claim
// the whole feature rests on: the key that was pasted into a terminal
// stops being able to read what the vault writes next.
func TestRecoverDeviceRetiresTheUsedKeyForFutureWrites(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")

	newRecovery, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	used := append([]byte(nil), f.key.Secret...)

	if _, err := recoverAs(t, f, RecoverSpec{
		Device:            "laptop-2",
		Pubkey:            freshPubkey(t),
		NewRecoveryPubkey: newRecovery.Pubkey,
	}, f.key.Secret, &fakePrompter{}); err != nil {
		t.Fatal(err)
	}

	if decryptsWith(t, f.v, used) {
		t.Error("the retired recovery key still opens the vault's entries after the swap")
	}
	if !decryptsWith(t, f.v, newRecovery.Secret) {
		t.Error("the new recovery key does not open the vault's entries")
	}
}

// TestRecoverDeviceLetsTheNewDeviceRead: the point of the exercise.
func TestRecoverDeviceLetsTheNewDeviceRead(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")

	replacement := newTestDevice(t, "personal", "laptop-2")
	newRecovery, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := recoverAs(t, f, RecoverSpec{
		Device:            "laptop-2",
		Pubkey:            replacement.pubkey,
		Replaces:          "laptop-1",
		NewRecoveryPubkey: newRecovery.Pubkey,
	}, f.key.Secret, &fakePrompter{}); err != nil {
		t.Fatal(err)
	}

	// The replaced device is gone and the new one is in.
	if got := deviceLabels(t, f.v); !equalStrings(got, []string{"laptop-2", RecoveryDeviceLabel}) {
		t.Fatalf("recipients = %v, want [laptop-2 %s]", got, RecoveryDeviceLabel)
	}

	// And the new device can actually read, through the ordinary unlock.
	withXDGRoot(t, replacement.root, func() {
		id, err := f.v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("the enrolled device cannot unlock: %v", err)
		}
		defer func() { _ = id.Close() }()
		ids, err := f.v.EntryIDs()
		if err != nil {
			t.Fatal(err)
		}
		for _, entryID := range ids {
			if _, err := f.v.ReadEntry(entryID, &id); err != nil {
				t.Errorf("the enrolled device cannot read %s: %v", entryID, err)
			}
		}
	})
}

func TestRecoverDeviceWithoutAReplacementLeavesNoRecoveryRecipient(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")

	res, err := recoverAs(t, f, RecoverSpec{
		Device: "laptop-2",
		Pubkey: freshPubkey(t),
	}, f.key.Secret, &fakePrompter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := deviceLabels(t, f.v); !equalStrings(got, []string{"laptop-1", "laptop-2"}) {
		t.Errorf("recipients = %v, want [laptop-1 laptop-2]", got)
	}
	if res.NewRecoveryPubkey != "" {
		t.Errorf("NewRecoveryPubkey = %q, want empty", res.NewRecoveryPubkey)
	}
}

// TestRecoverDeviceLeavesUpdatedByAlone: re-encryption is not an edit. An
// entry's updated_by still names whoever last changed its contents.
func TestRecoverDeviceLeavesUpdatedByAlone(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")

	before := map[string]string{}
	func() {
		id := unlockAs(t, f.v, f.owner)
		defer func() { _ = id.Close() }()
		ids, err := f.v.EntryIDs()
		if err != nil {
			t.Fatal(err)
		}
		for _, entryID := range ids {
			e, err := f.v.ReadEntry(entryID, &id)
			if err != nil {
				t.Fatal(err)
			}
			before[entryID.String()] = e.UpdatedBy
		}
	}()

	newRecovery, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recoverAs(t, f, RecoverSpec{
		Device:            "laptop-2",
		Pubkey:            freshPubkey(t),
		NewRecoveryPubkey: newRecovery.Pubkey,
	}, f.key.Secret, &fakePrompter{}); err != nil {
		t.Fatal(err)
	}

	id := unlockAs(t, f.v, f.owner)
	defer func() { _ = id.Close() }()
	ids, err := f.v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, entryID := range ids {
		e, err := f.v.ReadEntry(entryID, &id)
		if err != nil {
			t.Fatal(err)
		}
		if e.UpdatedBy != before[entryID.String()] {
			t.Errorf("updated_by = %q, want %q — a recipient swap is not an edit",
				e.UpdatedBy, before[entryID.String()])
		}
	}
}

// ---------------------------------------------------------------------
// Refusals: each writes nothing
// ---------------------------------------------------------------------

func TestRecoverDeviceRefusalsWriteNothing(t *testing.T) {
	stranger := freshPubkey(t)

	tests := []struct {
		name string
		spec func(f recoverFixture) RecoverSpec
		want error
		code exitcode.Code
	}{
		{
			name: "a device name already in use",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{Device: "laptop-1", Pubkey: stranger}
			},
			want: ErrRecipientExists, code: exitcode.Conflict,
		},
		{
			name: "a public key already in use",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{Device: "laptop-2", Pubkey: f.owner.pubkey}
			},
			want: ErrRecipientExists, code: exitcode.Conflict,
		},
		{
			name: "a device claiming the recovery label",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{Device: RecoveryDeviceLabel, Pubkey: stranger}
			},
			want: nil, code: exitcode.Usage,
		},
		{
			name: "an invalid device name",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{Device: "../escape", Pubkey: stranger}
			},
			want: nil, code: exitcode.Usage,
		},
		{
			name: "a malformed device key",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{Device: "laptop-2", Pubkey: "age1nope"}
			},
			want: nil, code: exitcode.Usage,
		},
		{
			name: "a malformed replacement recovery key",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{Device: "laptop-2", Pubkey: stranger, NewRecoveryPubkey: "age1nope"}
			},
			want: nil, code: exitcode.Usage,
		},
		{
			name: "a replacement recovery key already in use",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{Device: "laptop-2", Pubkey: stranger, NewRecoveryPubkey: f.owner.pubkey}
			},
			want: ErrRecipientExists, code: exitcode.Conflict,
		},
		{
			name: "--replaces naming nobody",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{Device: "laptop-2", Pubkey: stranger, Replaces: "desktop-9"}
			},
			want: ErrRecipientNotFound, code: exitcode.NotFound,
		},
		{
			name: "--replaces naming the recovery key",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{Device: "laptop-2", Pubkey: stranger, Replaces: RecoveryDeviceLabel}
			},
			want: nil, code: exitcode.Usage,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoverFixture(t, "personal", "laptop-1")
			insertAs(t, f.v, f.owner, "first")
			before := headHash(t, f.v)
			beforeList := labels(t, f.v)

			_, err := recoverAs(t, f, tc.spec(f), f.key.Secret, &fakePrompter{})
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("errors.Is(err, %v) = false: %v", tc.want, err)
			}
			if got := exitcode.CodeOf(err); got != tc.code {
				t.Errorf("CodeOf = %v, want %v", got, tc.code)
			}
			if got := headHash(t, f.v); got != before {
				t.Error("a refused swap moved HEAD")
			}
			if got := labels(t, f.v); !equalStrings(got, beforeList) {
				t.Errorf("a refused swap changed the recipient list: %v", got)
			}
		})
	}
}

// TestRecoverDeviceReusingTheReplacedName: replacing a device and reusing
// its name is the ordinary case — the machine is being rebuilt under the
// name it always had — so the collision check has to look at the final
// list, not the current one.
func TestRecoverDeviceReusingTheReplacedName(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")

	fresh := freshPubkey(t)
	if _, err := recoverAs(t, f, RecoverSpec{
		Device:   "laptop-1",
		Pubkey:   fresh,
		Replaces: "laptop-1",
	}, f.key.Secret, &fakePrompter{}); err != nil {
		t.Fatalf("reusing the replaced device's name was refused: %v", err)
	}
	if got := labels(t, f.v); !equalStrings(got, []string{"laptop-1=" + fresh}) {
		t.Errorf("recipients = %v, want just the rebuilt laptop-1", got)
	}
}

// ---------------------------------------------------------------------
// The guarantees the swap borrows from the existing recipient machinery
// ---------------------------------------------------------------------

// TestInterruptedRecoverDeviceLeavesHEADUntouched. The all-or-nothing
// guarantee is about HEAD: a crash partway through the re-encryption
// commits nothing, and the old recovery key still works, so re-running is
// the whole recovery procedure.
func TestInterruptedRecoverDeviceLeavesHEADUntouched(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")
	insertAs(t, f.v, f.owner, "second")
	insertAs(t, f.v, f.owner, "third")

	before := headHash(t, f.v)
	beforeList := labels(t, f.v)

	// A copy, because RecoverDevice zeroes the buffer it is handed — the
	// fixture's own key has to survive for the retry assertion below.
	pasted := append([]byte(nil), f.key.Secret...)
	f.v.onReencryptEntry = crashAfter(2)
	crashed := runAndRecoverCrash(t, func() {
		_, _ = recoverAs(t, f, RecoverSpec{
			Device: "laptop-2",
			Pubkey: freshPubkey(t),
		}, pasted, &fakePrompter{})
	})
	f.v.onReencryptEntry = nil
	if !crashed {
		t.Fatal("the injected failure never fired, so this asserts nothing")
	}

	if got := headHash(t, f.v); got != before {
		t.Errorf("HEAD moved from %s to %s; an interrupted swap must commit nothing", before, got)
	}
	if got := labels(t, f.v); !equalStrings(got, beforeList) {
		t.Errorf("recipients = %v, want %v", got, beforeList)
	}
	requireZeroed(t, pasted)
	// And the recovery key still works, which is what makes a retry the
	// entire recovery procedure.
	id, err := f.v.recoveryIdentity(f.key.Secret, &fakePrompter{})
	if err != nil {
		t.Fatalf("the recovery key stopped working after an interrupted swap: %v", err)
	}
	_ = id.Close()
}

// TestRecoverDeviceStillAsksM10sQuestion: the recovery path is not a way
// around the trust-cache check. A recipient list that arrived from
// somewhere else and was never reviewed still gets shown.
func TestRecoverDeviceStillAsksM10sQuestion(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")
	// A recipient someone else added, which this device has never
	// reviewed — the state M10's question exists for. Without it the
	// cached list already matches and there is nothing to ask about.
	commitRoutineRecipientChange(t, f.v, "phone-1", freshPubkey(t))
	before := headHash(t, f.v)

	p := &decliningRecipientPrompter{}
	_, err := recoverAs(t, f, RecoverSpec{
		Device: "laptop-2",
		Pubkey: freshPubkey(t),
	}, f.key.Secret, p)
	if err == nil {
		t.Fatal("expected the declined recipient change to abort the swap")
	}
	if !p.asked {
		t.Error("the swap never asked M10's question")
	}
	if got := headHash(t, f.v); got != before {
		t.Error("a declined swap still committed")
	}
}

// decliningRecipientPrompter answers M10's recipient-change question with
// no. fakePrompter always says yes, which is what makes it useless for
// proving the question is actually being asked.
type decliningRecipientPrompter struct {
	fakePrompter
	asked bool
}

func (p *decliningRecipientPrompter) ConfirmRecipientChange(w RecipientChangeWarning) (bool, error) {
	p.asked = true
	return false, nil
}

// ---------------------------------------------------------------------
// Secret hygiene
// ---------------------------------------------------------------------

// TestRecoverDeviceZeroesTheSecretItWasHanded: the caller pastes a key
// into this function and has no way to know when the library is done with
// it, so the library is what erases it.
func TestRecoverDeviceZeroesTheSecretItWasHanded(t *testing.T) {
	newRecovery, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("on success", func(t *testing.T) {
		f := newRecoverFixture(t, "personal", "laptop-1")
		insertAs(t, f.v, f.owner, "first")
		secret := append([]byte(nil), f.key.Secret...)
		if _, err := recoverAs(t, f, RecoverSpec{
			Device:            "laptop-2",
			Pubkey:            freshPubkey(t),
			NewRecoveryPubkey: newRecovery.Pubkey,
		}, secret, &fakePrompter{}); err != nil {
			t.Fatal(err)
		}
		requireZeroed(t, secret)
	})

	t.Run("on refusal", func(t *testing.T) {
		f := newRecoverFixture(t, "personal", "laptop-1")
		secret := append([]byte(nil), f.key.Secret...)
		if _, err := recoverAs(t, f, RecoverSpec{
			Device: "laptop-1", // already taken
			Pubkey: freshPubkey(t),
		}, secret, &fakePrompter{}); err == nil {
			t.Fatal("expected a refusal")
		}
		requireZeroed(t, secret)
	})

	t.Run("on a key that was never accepted", func(t *testing.T) {
		f := newRecoverFixture(t, "personal", "laptop-1")
		secret := []byte("not a key at all")
		if _, err := recoverAs(t, f, RecoverSpec{
			Device: "laptop-2",
			Pubkey: freshPubkey(t),
		}, secret, &fakePrompter{}); err == nil {
			t.Fatal("expected a refusal")
		}
		requireZeroed(t, secret)
	})
}

func requireZeroed(t *testing.T, b []byte) {
	t.Helper()
	if len(b) == 0 {
		return
	}
	if !bytes.Equal(b, make([]byte, len(b))) {
		t.Errorf("the caller's buffer was not zeroed: %q", b)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRecoverDeviceRefusesToReAddTheRetiredKey is the invariant the whole
// feature rests on, stated as a test because the swap makes it easy to
// break: the recovery label is removed before the additions are checked
// for collisions, so without an explicit check the retired key sails back
// in under a different label and "retirement" reports success while the
// pasted secret stays a full recipient forever.
func TestRecoverDeviceRefusesToReAddTheRetiredKey(t *testing.T) {
	tests := []struct {
		name string
		spec func(f recoverFixture) RecoverSpec
	}{
		{
			// The worst shape: a bare, passphrase-less secret becomes a
			// permanent *device* recipient.
			name: "as the enrolled device's key",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{Device: "laptop-2", Pubkey: f.key.Pubkey}
			},
		},
		{
			// Retired and re-registered under the same fixed label, so
			// RetiredRecovery == NewRecoveryPubkey and nothing changed.
			name: "as its own replacement",
			spec: func(f recoverFixture) RecoverSpec {
				return RecoverSpec{
					Device:            "laptop-2",
					Pubkey:            freshPubkey(t),
					NewRecoveryPubkey: f.key.Pubkey,
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoverFixture(t, "personal", "laptop-1")
			insertAs(t, f.v, f.owner, "first")
			before := headHash(t, f.v)
			beforeList := labels(t, f.v)
			used := append([]byte(nil), f.key.Secret...)

			_, err := recoverAs(t, f, tc.spec(f), f.key.Secret, &fakePrompter{})
			if err == nil {
				t.Fatal("the retired recovery key was accepted back into the recipient list")
			}
			if !errors.Is(err, ErrRecipientExists) {
				t.Errorf("errors.Is(err, ErrRecipientExists) = false: %v", err)
			}
			if got := exitcode.CodeOf(err); got != exitcode.Conflict {
				t.Errorf("CodeOf = %v, want Conflict", got)
			}
			if got := headHash(t, f.v); got != before {
				t.Error("a refused swap moved HEAD")
			}
			if got := labels(t, f.v); !equalStrings(got, beforeList) {
				t.Errorf("a refused swap changed the recipient list: %v", got)
			}
			// The refusal is not a silent partial: the key is exactly as
			// it was, still the vault's recovery key, so a corrected retry
			// works.
			if !decryptsWith(t, f.v, used) {
				t.Error("a refused swap still re-encrypted away from the recovery key")
			}
		})
	}
}

// ---------------------------------------------------------------------
// RotateRecoveryKey: the same swap, driven by an ordinary unlocked device
// ---------------------------------------------------------------------

// rotateAs runs RotateRecoveryKey as the fixture's owner.
func rotateAs(t *testing.T, f recoverFixture, newPubkey string, p Prompter) (RotateResult, error) {
	t.Helper()
	var (
		res RotateResult
		err error
	)
	withXDGRoot(t, f.owner.root, func() {
		id, uerr := f.v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
		if uerr != nil {
			t.Fatalf("unlocking as the owner: %v", uerr)
		}
		defer func() { _ = id.Close() }()
		res, err = f.v.RotateRecoveryKey(newPubkey, &id)
		_ = p
	})
	return res, err
}

func TestRotateRecoveryKeySwapsInOneCommit(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")
	insertAs(t, f.v, f.owner, "second")

	next, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	old := append([]byte(nil), f.key.Secret...)
	before := headHash(t, f.v)

	res, err := rotateAs(t, f, next.Pubkey, &fakePrompter{})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"laptop-1=" + f.owner.pubkey, RecoveryDeviceLabel + "=" + next.Pubkey}
	if got := labels(t, f.v); !equalStrings(got, want) {
		t.Errorf("recipients = %v, want %v", got, want)
	}
	if res.Retired != f.key.Pubkey {
		t.Errorf("Retired = %s, want %s", res.Retired, f.key.Pubkey)
	}
	if res.Created {
		t.Error("Created = true, but the vault already had a recovery recipient")
	}
	if head := headHash(t, f.v); head == before || res.Commit != head {
		t.Errorf("reported commit %s, HEAD %s (was %s)", res.Commit, head, before)
	}
	// Create's commit, two inserts, and this one.
	if n, err := gitrepo.CommitCount(f.v.Path); err != nil || n != 4 {
		t.Errorf("commit count = %d (err %v), want 4 — the swap must be a single commit", n, err)
	}
	if decryptsWith(t, f.v, old) {
		t.Error("the retired recovery key still opens the vault's entries")
	}
	if !decryptsWith(t, f.v, next.Secret) {
		t.Error("the new recovery key does not open the vault's entries")
	}
}

// TestRotateRecoveryKeyCreatesOneWhenThereIsNone: rotate is also how a
// vault made with --no-recovery-key gets one later.
func TestRotateRecoveryKeyCreatesOneWhenThereIsNone(t *testing.T) {
	d := newTestDevice(t, "personal", "laptop-1")
	var v *Vault
	withXDGRoot(t, d.root, func() {
		var err error
		v, err = Create(CreateSpec{
			Name: "personal", ID: d.vaultID, Path: filepath.Join(t.TempDir(), "personal"),
			Type: TypeGit, Method: MethodPassphrase, Device: "laptop-1",
			Recipients: []string{d.pubkey},
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	insertAs(t, v, d, "first")
	f := recoverFixture{v: v, owner: d}

	next, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	res, err := rotateAs(t, f, next.Pubkey, &fakePrompter{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Retired != "" {
		t.Errorf("Created = %v, Retired = %q; want Created and nothing retired", res.Created, res.Retired)
	}
	want := []string{"laptop-1=" + d.pubkey, RecoveryDeviceLabel + "=" + next.Pubkey}
	if got := labels(t, v); !equalStrings(got, want) {
		t.Errorf("recipients = %v, want %v", got, want)
	}
	if !decryptsWith(t, v, next.Secret) {
		t.Error("the new recovery key does not open the vault")
	}
}

// TestRotateRecoveryKeyRefusesTheKeyItIsRetiring is R3's review finding
// again, for the same reason: the swap removes the old label before it
// checks the additions, so re-offering the retired key would sail through
// and report a rotation that changed nothing.
func TestRotateRecoveryKeyRefusesTheKeyItIsRetiring(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")
	before := headHash(t, f.v)

	_, err := rotateAs(t, f, f.key.Pubkey, &fakePrompter{})
	if !errors.Is(err, ErrRecipientExists) {
		t.Fatalf("err = %v, want ErrRecipientExists", err)
	}
	if exitcode.CodeOf(err) != exitcode.Conflict {
		t.Errorf("CodeOf = %v, want Conflict", exitcode.CodeOf(err))
	}
	if headHash(t, f.v) != before {
		t.Error("a refused rotation moved HEAD")
	}
}

func TestRotateRecoveryKeyRefusalsWriteNothing(t *testing.T) {
	tests := []struct {
		name   string
		pubkey func(f recoverFixture) string
		want   error
		code   exitcode.Code
	}{
		{"a malformed key", func(recoverFixture) string { return "age1nope" }, nil, exitcode.Usage},
		{"a key that is a device's", func(f recoverFixture) string { return f.owner.pubkey }, ErrRecipientExists, exitcode.Conflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoverFixture(t, "personal", "laptop-1")
			insertAs(t, f.v, f.owner, "first")
			before, list := headHash(t, f.v), labels(t, f.v)

			_, err := rotateAs(t, f, tc.pubkey(f), &fakePrompter{})
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("errors.Is(err, %v) = false: %v", tc.want, err)
			}
			if got := exitcode.CodeOf(err); got != tc.code {
				t.Errorf("CodeOf = %v, want %v", got, tc.code)
			}
			if headHash(t, f.v) != before || !equalStrings(labels(t, f.v), list) {
				t.Error("a refused rotation changed the vault")
			}
		})
	}
}

func TestInterruptedRotateRecoveryKeyLeavesHEADUntouched(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")
	insertAs(t, f.v, f.owner, "second")
	insertAs(t, f.v, f.owner, "third")
	before, list := headHash(t, f.v), labels(t, f.v)

	f.v.onReencryptEntry = crashAfter(2)
	crashed := runAndRecoverCrash(t, func() {
		_, _ = rotateAs(t, f, freshPubkey(t), &fakePrompter{})
	})
	f.v.onReencryptEntry = nil
	if !crashed {
		t.Fatal("the injected failure never fired, so this asserts nothing")
	}
	if headHash(t, f.v) != before || !equalStrings(labels(t, f.v), list) {
		t.Error("an interrupted rotation committed something")
	}
	// The recovery key is still the vault's recovery recipient, so a retry
	// is the whole recovery. Checked against the committed list rather than
	// by decrypting entries off disk: an interrupted write deliberately
	// leaves entries/ holding ciphertext for the new list until the next
	// write's reset discards it, and it is HEAD that carries the guarantee.
	if label, err := f.v.VerifyRecoveryKey(f.key.Secret); err != nil || label != RecoveryDeviceLabel {
		t.Errorf("recovery key: label = %q, err = %v; want it still the recovery recipient", label, err)
	}
}

func TestRotateRecoveryKeyStillAsksM10sQuestion(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")
	commitRoutineRecipientChange(t, f.v, "phone-1", freshPubkey(t))
	before := headHash(t, f.v)

	p := &decliningRecipientPrompter{}
	p.passphrases = []string{testPassphrase}
	var err error
	withXDGRoot(t, f.owner.root, func() {
		id, uerr := f.v.Unlock(p)
		if uerr != nil {
			t.Fatal(uerr)
		}
		defer func() { _ = id.Close() }()
		_, err = f.v.RotateRecoveryKey(freshPubkey(t), &id)
	})
	if err == nil || !p.asked {
		t.Fatalf("err = %v, asked = %v; want a declined M10 question", err, p.asked)
	}
	if headHash(t, f.v) != before {
		t.Error("a declined rotation still committed")
	}
}

// ---------------------------------------------------------------------
// The label is reserved: no device may hold it
// ---------------------------------------------------------------------

// TestTheRecoveryLabelIsReservedForTheRecoveryKey. Rotate evicts whatever
// holds the label, and it has no way to tell a recovery key from any other
// X25519 key — the label *is* the distinction. So the label has to be
// unavailable to devices everywhere a device names itself, or rotate would
// drop a real device and re-encrypt to the new paper key alone, locking out
// the machine that ran it.
func TestTheRecoveryLabelIsReservedForTheRecoveryKey(t *testing.T) {
	t.Run("Create refuses it even with no recovery key", func(t *testing.T) {
		isolateXDG(t)
		path := filepath.Join(t.TempDir(), "personal")
		_, err := Create(CreateSpec{
			Name: "personal", ID: vaultIDForTest("personal"), Path: path,
			Type: TypeGit, Method: MethodPassphrase,
			Device:     RecoveryDeviceLabel,
			Recipients: []string{testRecipient1},
		})
		if !errors.Is(err, ErrReservedDeviceLabel) {
			t.Fatalf("err = %v, want ErrReservedDeviceLabel", err)
		}
		if exitcode.CodeOf(err) != exitcode.Usage {
			t.Errorf("CodeOf = %v, want Usage", exitcode.CodeOf(err))
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Error("a refused Create left the vault directory behind")
		}
	})

	t.Run("AddIdentity refuses it", func(t *testing.T) {
		f := newRecoverFixture(t, "personal", "laptop-1")
		var err error
		withXDGRoot(t, f.owner.root, func() {
			_, err = f.v.AddIdentity(RecoveryDeviceLabel, &fakePrompter{passphrases: []string{testPassphrase}})
		})
		if !errors.Is(err, ErrReservedDeviceLabel) {
			t.Fatalf("err = %v, want ErrReservedDeviceLabel", err)
		}
	})

	t.Run("AddRecipient refuses it", func(t *testing.T) {
		f := newRecoverFixture(t, "personal", "laptop-1")
		insertAs(t, f.v, f.owner, "first")
		before := headHash(t, f.v)

		var err error
		withXDGRoot(t, f.owner.root, func() {
			id := unlockAs(t, f.v, f.owner)
			defer func() { _ = id.Close() }()
			_, err = f.v.AddRecipient(RecoveryDeviceLabel, freshPubkey(t), &id)
		})
		if !errors.Is(err, ErrReservedDeviceLabel) {
			t.Fatalf("err = %v, want ErrReservedDeviceLabel", err)
		}
		if headHash(t, f.v) != before {
			t.Error("a refused AddRecipient still committed")
		}
	})

	// The one path that must still write it: the recovery key itself.
	t.Run("but the recovery recipient is still written", func(t *testing.T) {
		f := newRecoverFixture(t, "personal", "laptop-1")
		if got := deviceLabels(t, f.v); !equalStrings(got, []string{"laptop-1", RecoveryDeviceLabel}) {
			t.Errorf("recipients = %v; Create must still write the recovery recipient", got)
		}
		insertAs(t, f.v, f.owner, "first")
		next, err := NewRecoveryKey()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rotateAs(t, f, next.Pubkey, &fakePrompter{}); err != nil {
			t.Fatalf("rotate must still be able to write the label: %v", err)
		}
	})
}

// TestRotateRefusesToEvictTheActingDevice is the backstop under the
// reservation above: even if some path ever let a device hold the label,
// rotate must not be what locks out the machine running it.
func TestRotateRefusesToEvictTheActingDevice(t *testing.T) {
	// A vault whose owner's own key holds the recovery label — the state
	// the reservation now prevents, constructed directly.
	d := newTestDevice(t, "personal", "laptop-1")
	var v *Vault
	withXDGRoot(t, d.root, func() {
		var err error
		v, err = Create(CreateSpec{
			Name: "personal", ID: d.vaultID, Path: filepath.Join(t.TempDir(), "personal"),
			Type: TypeGit, Method: MethodPassphrase, Device: "laptop-1",
			Recipients:      []string{testRecipient1},
			ExtraRecipients: []LabelledRecipient{{Device: RecoveryDeviceLabel, Pubkey: d.pubkey}},
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	insertAs(t, v, d, "first")
	f := recoverFixture{v: v, owner: d}
	before := headHash(t, v)

	_, err := rotateAs(t, f, freshPubkey(t), &fakePrompter{})
	if err == nil {
		t.Fatal("rotate evicted the key it was unlocked with")
	}
	if exitcode.CodeOf(err) != exitcode.Conflict {
		t.Errorf("CodeOf = %v, want Conflict", exitcode.CodeOf(err))
	}
	if headHash(t, v) != before {
		t.Error("a refused rotation still committed")
	}
}

// TestRotateWarnsThatRetirementIsFutureOnly: the leak-response user is the
// one who most needs to hear that the retired key still opens history, and
// rotate removes through removeIfPresent, which the warning loop missed.
func TestRotateWarnsThatRetirementIsFutureOnly(t *testing.T) {
	f := newRecoverFixture(t, "personal", "laptop-1")
	insertAs(t, f.v, f.owner, "first")

	next, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePrompter{passphrases: []string{testPassphrase}}
	withXDGRoot(t, f.owner.root, func() {
		id, uerr := f.v.Unlock(p)
		if uerr != nil {
			t.Fatal(uerr)
		}
		defer func() { _ = id.Close() }()
		if _, err := f.v.RotateRecoveryKey(next.Pubkey, &id); err != nil {
			t.Fatal(err)
		}
	})

	var found bool
	for _, w := range p.warnings {
		if strings.Contains(w, "git history") {
			found = true
		}
	}
	if !found {
		t.Errorf("rotate never warned that the retired key still opens history: %v", p.warnings)
	}
}

// TestApplySwapRefusesLeavingNothingToEncryptTo is applySwap's own last
// line of defense: a swap that would leave the updated list empty. Every
// current caller (RecoverDevice, RotateRecoveryKey) always adds at least
// one recipient as part of what it's doing, so neither can reach this
// through the exported API today — this drives the pure, unexported
// function directly, which needs no vault at all, to prove the guard
// itself rather than one specific caller's current inability to trigger
// it (the same reasoning as trustcache_test.go's
// TestConfirmRecipientTrustWithNoPrompterRefuses).
func TestApplySwapRefusesLeavingNothingToEncryptTo(t *testing.T) {
	current := []VaultRecipient{{Device: "laptop-1", Pubkey: "placeholder"}}

	_, err := applySwap(current, recipientSwap{remove: []string{"laptop-1"}})
	if !errors.Is(err, ErrLastRecipient) {
		t.Errorf("error = %v, want it to wrap ErrLastRecipient", err)
	}
	if exitcode.CodeOf(err) != exitcode.Conflict {
		t.Errorf("code = %v, want Conflict", exitcode.CodeOf(err))
	}
}
