package gage

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
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
// silently reverted to age's default: this number is the only thing
// standing between a stolen identity file and an offline brute force, so
// a change to it should be a change someone had to make on purpose.
//
// It checks shippedScryptWorkFactor, the constant, rather than the live
// scryptWorkFactor variable. That's the whole reason the shipped number
// is a separate const: TestMain lowers the live variable for this
// package's entire test run (see SetScryptWorkFactorForTests), so
// checking the variable would make this test validate the suite's own
// speed hack instead of the real default. A const is checkable here
// *and* unmovable at runtime, which no amount of reading the value back
// out of source could guarantee.
func TestScryptWorkFactorIsDeliberate(t *testing.T) {
	const ageDefault = 18
	if shippedScryptWorkFactor <= ageDefault {
		t.Errorf("shippedScryptWorkFactor = %d, want more than age's default of %d",
			shippedScryptWorkFactor, ageDefault)
	}
	if scryptMaxWorkFactor < shippedScryptWorkFactor {
		t.Errorf("scryptMaxWorkFactor (%d) is below shippedScryptWorkFactor (%d): gage could not open files it writes",
			scryptMaxWorkFactor, shippedScryptWorkFactor)
	}
}

// TestOnlyTheTestHooksWriteTheWorkFactors closes the one gap a const
// cannot: shippedScryptWorkFactor is unmovable, but scryptWorkFactor —
// the copy the crypto path actually reads — is a plain package variable,
// and a single line in a non-test file of this package
//
//	func init() { scryptWorkFactor = 10 }
//
// would ship a weakened KDF invisibly. forbidigo does not catch it (it
// forbids SetScryptWorkFactorForTests, a function, not an assignment),
// and TestScryptWorkFactorIsDeliberate does not catch it (the constant
// is untouched). Nothing else in the build would notice.
//
// So this walks the package's non-test files and rejects any assignment
// to the live variable outside its own sanctioned setter. It parses
// rather than greps because the spellings to catch (`=`, `+=`, an
// assignment nested in any block) are exactly what a text pattern gets
// wrong.
//
// It also pins each variable's own declaration to its shipped constant.
// That is a second, distinct hole: rewriting the declaration to a bare
// `var scryptWorkFactor = 10` is not an assignment at all, so the walk
// below would not see it, and the constant it is supposed to track would
// sit right above it, untouched and still passing
// TestScryptWorkFactorIsDeliberate.
//
// It covers *both* of gage's work factors — the identity file's and
// enrollment's — rather than being duplicated per factor. The enrollment
// factor is a security parameter of exactly the same kind, arrived at by
// exactly the same pattern (shipped const, live copy, one setter), so it
// gets the same guard by being added to this table rather than by a
// second copy of this walk that could drift from it.
func TestOnlyTheTestHooksWriteTheWorkFactors(t *testing.T) {
	factors := []struct {
		variable  string
		shipped   string
		allowedIn string
	}{
		{"scryptWorkFactor", "shippedScryptWorkFactor", "SetScryptWorkFactorForTests"},
		{"enrollmentScryptWorkFactor", "shippedEnrollmentScryptWorkFactor", "SetEnrollmentWorkFactorForTests"},
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("globbed no .go files; this test is not looking where it thinks it is")
	}

	for _, factor := range factors {
		declarations := 0
		fset := token.NewFileSet()
		checked := 0
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			checked++
			f, err := parser.ParseFile(fset, name, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", name, err)
			}
			// The declaration pin, which is position-independent: find
			// every spec declaring the live variable and require it to
			// initialize from the shipped constant.
			ast.Inspect(f, func(n ast.Node) bool {
				spec, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for i, name := range spec.Names {
					if name.Name != factor.variable {
						continue
					}
					declarations++
					var init ast.Expr
					if i < len(spec.Values) {
						init = spec.Values[i]
					}
					id, ok := init.(*ast.Ident)
					if !ok || id.Name != factor.shipped {
						t.Errorf("%s: %s must be declared as `= %s` so it tracks the shipped constant; "+
							"a literal here is a weakened KDF that the is-deliberate tests cannot see",
							fset.Position(name.Pos()), factor.variable, factor.shipped)
					}
				}
				return true
			})

			// The assignment check, walked one top-level declaration at a
			// time so the sanctioned writer is exempted by *containment*
			// rather than by a running "last function seen" marker. That
			// distinction matters: a marker would still be pointing at
			// the setter when the walk reached a later package-level
			// `var _ = func() int { scryptWorkFactor = 10; ... }()`, and
			// would wave it through.
			for _, decl := range f.Decls {
				owner := ""
				if fn, ok := decl.(*ast.FuncDecl); ok {
					if fn.Name.Name == factor.allowedIn {
						continue
					}
					owner = fn.Name.Name
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					assign, ok := n.(*ast.AssignStmt)
					if !ok {
						return true
					}
					for _, lhs := range assign.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok || id.Name != factor.variable {
							continue
						}
						t.Errorf("%s: %s assigns to %s; only %s may write it — "+
							"a non-test writer here ships a weakened KDF that every other guard misses",
							fset.Position(id.Pos()), describeFunc(owner), factor.variable, factor.allowedIn)
					}
					return true
				})
			}
		}
		if checked == 0 {
			t.Fatal("found no non-test .go files to check")
		}
		// Exactly one, or the pin above was checked against a declaration
		// that is no longer the one the crypto path reads.
		if declarations != 1 {
			t.Errorf("found %d non-test declarations of %s, want exactly 1", declarations, factor.variable)
		}
	}
}

// TestEveryScryptIdentityCapsItsWorkFactor pins the *first* part of
// D-ENROLL-SEAL-COST, which is the one that fails as a hang rather than
// as an error: a decryption path that builds an age scrypt identity and
// forgets SetMaxWorkFactor will spend hours on a single committed file
// claiming 2^30, with nothing to show for it.
//
// A behavioural test proves the enrollment path is bounded today (see
// TestHostileWorkFactorCostsABoundedWait), but it cannot prove the call
// is what bounds it: age's own default ceiling is 22, so removing the
// call leaves that test green and leaves the next decryption path in this
// package inheriting a limit nobody chose. Enrollment inherited nothing
// automatically, and neither will whatever comes after it.
//
// So this walks the package's non-test files and requires every function
// that constructs an age scrypt identity to call SetMaxWorkFactor
// somewhere in the same function. Scoped to the function rather than the
// exact next statement, since the construction and the cap are two
// statements with an error check between them.
func TestEveryScryptIdentityCapsItsWorkFactor(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			constructs, caps := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "NewScryptIdentity":
					constructs = true
				case "SetMaxWorkFactor":
					caps = true
				}
				return true
			})
			if !constructs {
				continue
			}
			found++
			if !caps {
				t.Errorf("%s: %s builds an age scrypt identity without calling SetMaxWorkFactor; "+
					"a file claiming 2^30 would cost a hang rather than an error (D-ENROLL-SEAL-COST)",
					fset.Position(fn.Pos()), fn.Name.Name)
			}
		}
	}
	// Two today: the identity-file unlock path and the enrollment open
	// path. Zero would mean this walk stopped finding what it checks.
	if found == 0 {
		t.Error("found no non-test function constructing an age scrypt identity; this test is no longer looking where it thinks it is")
	}
}

// describeFunc names the function an offending assignment was found in,
// for a failure message that points at a place rather than a file.
func describeFunc(name string) string {
	if name == "" {
		return "a package-level declaration"
	}
	return name
}

// TestShippedWorkFactorReachesARealAgeFile is the end-to-end half of the
// pair above, and the only test in the suite that observes the real
// shipped cost reaching a real age header. Every other stanza assertion
// (TestScryptWorkFactorIsActuallyApplied, and the identity-file check in
// unlock_test.go) compares against the live scryptWorkFactor, which
// TestMain has lowered — those still catch a dropped or hardcoded
// SetWorkFactor, but none of them any longer proves that *19* is what a
// user's file gets wrapped at.
//
// It pays one real scrypt pass at the shipped factor, which is the point:
// roughly a second, once, in exchange for the suite actually exercising
// the number gage ships. It also proves scryptMaxWorkFactor admits it,
// on a real file rather than by comparing two constants.
func TestShippedWorkFactorReachesARealAgeFile(t *testing.T) {
	restore := SetScryptWorkFactorForTests(shippedScryptWorkFactor)
	defer restore()

	r, err := PassphraseRecipient("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("wrapped at the factor gage actually ships")
	ct, err := Encrypt(payload, r)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := scryptStanzaWorkFactor(string(ct))
	if !ok {
		t.Fatalf("no scrypt stanza found in the header:\n%q", firstLines(string(ct), 3))
	}
	if got != shippedScryptWorkFactor {
		t.Errorf("age wrapped at work factor %d, want the shipped %d", got, shippedScryptWorkFactor)
	}

	// And gage can read back what it writes at that factor: the max is a
	// ceiling on a file's *claimed* cost, so a max below the shipped
	// factor would make gage unable to open its own identity files.
	id, err := age.NewScryptIdentity("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	id.SetMaxWorkFactor(scryptMaxWorkFactor)
	back, err := decryptBytes(ct, id)
	if err != nil {
		t.Fatalf("decrypting a file wrapped at the shipped factor %d: %v", shippedScryptWorkFactor, err)
	}
	if !bytes.Equal(back, payload) {
		t.Errorf("round-tripped payload = %q, want %q", back, payload)
	}
}

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
