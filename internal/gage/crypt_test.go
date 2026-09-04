package gage

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// testKeypair generates a throwaway X25519 identity and returns it
// alongside its parsed Recipient, which is the pair every wrapper test
// needs.
func testKeypair(t *testing.T) (*age.X25519Identity, Recipient) {
	t.Helper()
	ident, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseRecipient(ident.Recipient().String())
	if err != nil {
		t.Fatal(err)
	}
	return ident, r
}

// wrapIdentity builds the Identity value Decrypt takes from a bare age
// key, without going anywhere near a vault, a file, or a passphrase —
// the whole point of proving the wrapper in isolation.
func wrapIdentity(t *testing.T, ident *age.X25519Identity) *Identity {
	t.Helper()
	return &Identity{
		device: "test-device",
		st: &identityState{
			secret:    []byte(ident.String()),
			ident:     ident,
			recipient: ident.Recipient().String(),
			locker:    memLocker{},
		},
	}
}

func TestEncryptDecryptRoundTripIsByteIdentical(t *testing.T) {
	ident, recipient := testKeypair(t)

	// Includes a NUL and a multi-byte rune: the wrapper is bytes-in,
	// bytes-out and must not be quietly text-oriented.
	payload := []byte("correcthorsebatterystaple\x00\nsecond line\nπ")

	ct, err := Encrypt(payload, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, payload) {
		t.Fatal("the plaintext appears verbatim in the ciphertext")
	}

	got, err := Decrypt(ct, wrapIdentity(t, ident))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round-tripped payload = %q, want %q", got, payload)
	}
}

func TestEncryptToEmptyPayloadRoundTrips(t *testing.T) {
	ident, recipient := testKeypair(t)

	ct, err := Encrypt([]byte{}, recipient)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(ct, wrapIdentity(t, ident))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("round-tripped empty payload = %q, want empty", got)
	}
}

// TestEachRecipientCanDecryptIndependently is the property M9's
// `recipient add` and the design's whole recovery-key story depend on:
// N recipients means N independently sufficient keys, not a quorum.
func TestEachRecipientCanDecryptIndependently(t *testing.T) {
	const n = 3
	idents := make([]*age.X25519Identity, n)
	recipients := make([]Recipient, n)
	for i := range idents {
		idents[i], recipients[i] = testKeypair(t)
	}

	payload := []byte("shared across every recipient")
	ct, err := Encrypt(payload, recipients...)
	if err != nil {
		t.Fatal(err)
	}

	for i, ident := range idents {
		got, err := Decrypt(ct, wrapIdentity(t, ident))
		if err != nil {
			t.Fatalf("recipient %d could not decrypt: %v", i, err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("recipient %d decrypted %q, want %q", i, got, payload)
		}
	}
}

func TestDecryptWithANonRecipientFailsTyped(t *testing.T) {
	_, recipient := testKeypair(t)
	stranger, _ := testKeypair(t)

	ct, err := Encrypt([]byte("not for you"), recipient)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Decrypt(ct, wrapIdentity(t, stranger))
	if err == nil {
		t.Fatal("expected decryption with a non-recipient key to fail")
	}
	if !errors.Is(err, ErrNotARecipient) {
		t.Errorf("error = %v, want it to wrap ErrNotARecipient", err)
	}
	// A wrong key must never look like a damaged file: M8a's clone
	// reports the two differently.
	if errors.Is(err, ErrCorruptCiphertext) {
		t.Error("a non-recipient key was reported as corrupt ciphertext")
	}
	if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
	}
	if got != nil {
		t.Errorf("plaintext = %q, want nothing at all on failure", got)
	}
}

// TestCorruptedCiphertextFailsCleanly walks damage across the whole file,
// not just one spot: the header, the recipient stanza, and the payload
// fail at different stages inside age, and none of them may panic or
// return partial plaintext.
func TestCorruptedCiphertextFailsCleanly(t *testing.T) {
	ident, recipient := testKeypair(t)
	ct, err := Encrypt([]byte("intact payload, for now"), recipient)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"first byte flipped":   flipByte(ct, 0),
		"header byte flipped":  flipByte(ct, len(ct)/4),
		"payload byte flipped": flipByte(ct, len(ct)-2),
		"truncated to header":  bytes.Clone(ct[:len(ct)/2]),
		"truncated to nothing": {},
		"not an age file":      []byte("this is just some text"),
	}

	for name, damaged := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Decrypt(damaged, wrapIdentity(t, ident))
			if err == nil {
				t.Fatalf("expected damaged ciphertext to fail; got %q", got)
			}
			if got != nil {
				t.Errorf("plaintext = %q, want nothing at all on failure", got)
			}
			if !errors.Is(err, ErrCorruptCiphertext) && !errors.Is(err, ErrNotARecipient) {
				t.Errorf("error = %v, want ErrCorruptCiphertext or ErrNotARecipient", err)
			}
		})
	}
}

func flipByte(b []byte, i int) []byte {
	out := bytes.Clone(b)
	out[i] ^= 0xff
	return out
}

func TestEncryptToZeroRecipientsIsRefused(t *testing.T) {
	got, err := Encrypt([]byte("nobody could read this"))
	if err == nil {
		t.Fatal("expected encrypting to zero recipients to be refused")
	}
	if !errors.Is(err, ErrNoRecipients) {
		t.Errorf("error = %v, want it to wrap ErrNoRecipients", err)
	}
	if got != nil {
		t.Errorf("ciphertext = %q, want nothing — a file nobody can read must not be produced", got)
	}
}

// TestScryptRecipientCannotBeCombined is the invariant the identity file
// rests on (A3): a passphrase-wrapped file is passphrase-only by
// construction, so there is no "also let my other device open this"
// variant of it to accidentally create.
func TestScryptRecipientCannotBeCombined(t *testing.T) {
	_, pubkey := testKeypair(t)
	pass, err := PassphraseRecipient("hunter2")
	if err != nil {
		t.Fatal(err)
	}

	// Both orderings: gage refuses before age is reached either way, so
	// the invariant can't depend on which recipient happens to be first.
	for _, tc := range []struct {
		name string
		to   []Recipient
	}{
		{"passphrase first", []Recipient{pass, pubkey}},
		{"passphrase second", []Recipient{pubkey, pass}},
		{"two passphrases", []Recipient{pass, pass}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encrypt([]byte("x"), tc.to...)
			if err == nil {
				t.Fatal("expected combining a passphrase recipient with another to be refused")
			}
			if !errors.Is(err, ErrMixedScryptRecipient) {
				t.Errorf("error = %v, want it to wrap ErrMixedScryptRecipient", err)
			}
			if got != nil {
				t.Errorf("ciphertext = %q, want nothing", got)
			}
		})
	}
}

func TestPassphraseRecipientAloneRoundTrips(t *testing.T) {
	r, err := PassphraseRecipient("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("wrapped by a passphrase alone")
	ct, err := Encrypt(payload, r)
	if err != nil {
		t.Fatal(err)
	}

	id, err := age.NewScryptIdentity("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	id.SetMaxWorkFactor(scryptMaxWorkFactor)
	got, err := decryptBytes(ct, id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round-tripped payload = %q, want %q", got, payload)
	}
}

// TestScryptWorkFactorIsActuallyApplied is the half that matters. The
// constant below can be correct and never reach age: dropping the
// SetWorkFactor call would silently write every identity file at age's
// default of 18 while every other test in this milestone stayed green.
//
// age records the factor in the file's own scrypt stanza
// ("-> scrypt <salt> <logN>"), so it is readable straight off the
// ciphertext — no need to time anything or reach into the library.
func TestScryptWorkFactorIsActuallyApplied(t *testing.T) {
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
		t.Fatalf("no scrypt stanza found in the header:\n%q", firstLines(string(ct), 3))
	}
	if got != scryptWorkFactor {
		t.Errorf("identity ciphertext was wrapped at work factor %d, want %d — "+
			"the deliberate factor is not reaching age", got, scryptWorkFactor)
	}
}

// scryptStanzaWorkFactor pulls logN out of an age header's scrypt stanza,
// whose wire format is "-> scrypt <base64 salt> <logN>".
func scryptStanzaWorkFactor(header string) (int, bool) {
	for _, line := range strings.Split(header, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 || fields[0] != "->" || fields[1] != "scrypt" {
			continue
		}
		n, err := strconv.Atoi(fields[3])
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// TestScryptWorkFactorIsDeliberate guards the M2 decision from being
// silently reverted to age's default: the constant is the only thing
// standing between a stolen identity file and an offline brute force, so
// a change to it should be a change someone had to make on purpose.
//
// It reads the value straight from source rather than the live
// scryptWorkFactor variable. That's not a style choice: TestMain lowers
// the live value for this whole package's test run (see
// SetScryptWorkFactorForTests), so by the time this test runs, the
// variable no longer holds what gage actually ships with — checking it
// would make this test validate the test suite's own speed hack instead
// of the real default. Same technique as
// TestGenerateDrawsFromCryptoRandNotMathRand, for the same reason: some
// properties are only observable in source once something legitimate has
// overridden them at runtime.
func TestScryptWorkFactorIsDeliberate(t *testing.T) {
	const ageDefault = 18
	got := shippedScryptWorkFactor(t)
	if got <= ageDefault {
		t.Errorf("scryptWorkFactor's shipped default = %d, want more than age's default of %d", got, ageDefault)
	}
	if scryptMaxWorkFactor < got {
		t.Errorf("scryptMaxWorkFactor (%d) is below scryptWorkFactor's shipped default (%d): gage could not open files it writes",
			scryptMaxWorkFactor, got)
	}
}

// shippedScryptWorkFactor reads scryptWorkFactor's initializer straight
// out of identityfile.go, so this test's assertion holds regardless of
// what the live variable has been overridden to for this test run.
func shippedScryptWorkFactor(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile("identityfile.go")
	if err != nil {
		t.Fatal(err)
	}
	m := scryptWorkFactorDefaultPattern.FindSubmatch(data)
	if m == nil {
		t.Fatal("could not find scryptWorkFactor's initializer in identityfile.go")
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

var scryptWorkFactorDefaultPattern = regexp.MustCompile(`var scryptWorkFactor = (\d+)`)

func TestParseRecipientRejectsNonEncryptableStrings(t *testing.T) {
	for _, s := range []string{
		"",
		"not-an-age-key",
		// A plugin recipient: legal in .age-recipients, but not
		// something this build can encrypt to. Rejected rather than
		// half-supported.
		"age1yubikey1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0s9hkmc0",
		// An age *private* key, which is not a recipient.
		"AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ",
	} {
		t.Run(s, func(t *testing.T) {
			if _, err := ParseRecipient(s); err == nil {
				t.Errorf("ParseRecipient(%q) succeeded, want an error", s)
			} else if !errors.Is(err, ErrInvalidRecipient) {
				t.Errorf("error = %v, want it to wrap ErrInvalidRecipient", err)
			}
		})
	}
}

func TestDecryptWithAClosedIdentityFails(t *testing.T) {
	ident, recipient := testKeypair(t)
	ct, err := Encrypt([]byte("still secret"), recipient)
	if err != nil {
		t.Fatal(err)
	}

	id := wrapIdentity(t, ident)
	if err := id.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Decrypt(ct, id)
	if err == nil {
		t.Fatal("expected Decrypt with a closed Identity to fail")
	}
	if !errors.Is(err, ErrIdentityClosed) {
		t.Errorf("error = %v, want it to wrap ErrIdentityClosed", err)
	}
	if got != nil {
		t.Errorf("plaintext = %q, want nothing", got)
	}
}
