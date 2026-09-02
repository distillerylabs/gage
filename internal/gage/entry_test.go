package gage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/recipients"
)

// testDevice is one simulated machine: its own isolated XDG roots (so it
// has its own local identity record, independent of any other device
// testing against the same vault) plus the public key CreateIdentity
// generated for it.
type testDevice struct {
	name   string
	root   string
	pubkey string
}

// newTestDevice generates a fresh identity for device under its own
// isolated XDG roots, without switching the process's current
// environment to it — see unlockAs for that.
func newTestDevice(t *testing.T, vault, device string) testDevice {
	t.Helper()
	root := t.TempDir()
	withXDGRoot(t, root, func() {
		registerVault(t, vault, device, MethodPassphrase)
	})

	var pubkey string
	withXDGRoot(t, root, func() {
		var err error
		pubkey, err = CreateIdentity(vault, device, &fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatal(err)
		}
	})
	return testDevice{name: device, root: root, pubkey: pubkey}
}

// withXDGRoot repoints the process's XDG environment at root and runs
// fn under it. The switch outlives fn — t.Setenv restores at test end,
// not at fn's return — so a test that drives several devices must finish
// with one before switching to the next, which is why unlockAs's result
// has to be closed before the next device's turn.
func withXDGRoot(t *testing.T, root string, fn func()) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	fn()
}

// unlockAs unlocks v as d, switching the process's XDG environment to d's
// roots first. The returned Identity is only valid for the duration of
// this device's turn — callers must Close it before moving to another
// device, since the next withXDGRoot call repoints global config out from
// under it.
func unlockAs(t *testing.T, v *Vault, d testDevice) Identity {
	t.Helper()
	var id Identity
	withXDGRoot(t, d.root, func() {
		var err error
		id, err = v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("unlocking as %s: %v", d.name, err)
		}
	})
	return id
}

// newEntryTestVault builds a real, fully-laid-out vault (via Create, not
// just the bare struct newUnlockableVault returns) with one recipient
// device, and returns the vault plus that device's identity, already
// unlocked. Callers own Close-ing the returned Identity.
func newEntryTestVault(t *testing.T, vaultName, device string) (*Vault, Identity) {
	t.Helper()
	d := newTestDevice(t, vaultName, device)

	var v *Vault
	withXDGRoot(t, d.root, func() {
		var err error
		v, err = Create(CreateSpec{
			Name:       vaultName,
			Path:       filepath.Join(t.TempDir(), vaultName),
			Type:       TypeGit,
			Method:     MethodPassphrase,
			Device:     device,
			Recipients: []string{d.pubkey},
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	id := unlockAs(t, v, d)
	return v, id
}

func sampleEntry(now time.Time) Entry {
	return Entry{
		Title:       "ProtonMail",
		Description: "Personal email, 2FA via authenticator app",
		Created:     NewTimestamp(now),
		Updated:     NewTimestamp(now),
		UpdatedBy:   "yubikey-5c-nfc-1",
		Value:       "correcthorsebatterystaple",
		Fields: map[string]string{
			"username":  "me@proton.me",
			"totp_seed": "JBSWY3DPEHPK3PXP",
		},
	}
}

// TestEntryMarshalMatchesDesignDocShape pins the exact wire shape from
// "Entry format" in the design doc: field order title/description/
// created/updated/updated_by/value/fields, timestamps unquoted. Keys
// inside fields are sorted, so its two come out as totp_seed before
// username — the design doc's ordering there is illustrative, while the
// outer field order is the contract.
func TestEntryMarshalMatchesDesignDocShape(t *testing.T) {
	created, err := time.Parse(time.RFC3339, "2026-01-14T10:32:00Z")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := time.Parse(time.RFC3339, "2026-08-20T09:03:00Z")
	if err != nil {
		t.Fatal(err)
	}
	e := Entry{
		Title:       "ProtonMail",
		Description: "Personal email, 2FA via authenticator app",
		Created:     NewTimestamp(created),
		Updated:     NewTimestamp(updated),
		UpdatedBy:   "yubikey-5c-nfc-1",
		Value:       "correcthorsebatterystaple",
		Fields: map[string]string{
			"username":  "me@proton.me",
			"totp_seed": "JBSWY3DPEHPK3PXP",
		},
	}

	got, err := MarshalEntry(e)
	if err != nil {
		t.Fatal(err)
	}

	want := "title: ProtonMail\n" +
		"description: Personal email, 2FA via authenticator app\n" +
		"created: 2026-01-14T10:32:00Z\n" +
		"updated: 2026-08-20T09:03:00Z\n" +
		"updated_by: yubikey-5c-nfc-1\n" +
		"value: correcthorsebatterystaple\n" +
		"fields:\n" +
		"    totp_seed: JBSWY3DPEHPK3PXP\n" +
		"    username: me@proton.me\n"

	if string(got) != want {
		t.Errorf("marshaled entry =\n%s\nwant\n%s", got, want)
	}

	back, err := UnmarshalEntry(got)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, e) {
		t.Errorf("round-tripped entry = %+v, want %+v", back, e)
	}
}

// TestEveryEntryFieldIsMarshaled guards the seam the hand-built encoder
// creates: Entry's yaml tags drive decoding while marshalEntryNodes
// writes the document field by field, so a field added to the struct and
// forgotten there would read back fine in tests that only round-trip
// through Unmarshal, and silently never persist. Reflection over the
// struct's own tags is what makes that impossible to miss.
func TestEveryEntryFieldIsMarshaled(t *testing.T) {
	// Every field non-zero, so nothing is legitimately omitted.
	e := Entry{
		Title:       "t",
		Description: "d",
		Created:     NewTimestamp(time.Now()),
		Updated:     NewTimestamp(time.Now()),
		UpdatedBy:   "u",
		Value:       "v",
		Fields:      map[string]string{"k": "v"},
		Extra:       map[string]any{"unknown_key": "x"},
	}
	data, err := MarshalEntry(e)
	if err != nil {
		t.Fatal(err)
	}

	typ := reflect.TypeOf(e)
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("yaml")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			// The inline Extra map has no key of its own; its contents
			// are checked below instead.
			continue
		}
		if !strings.Contains(string(data), name+":") {
			t.Errorf("Entry field %s (yaml %q) never reaches the marshaled document — "+
				"marshalEntryNodes is missing a line for it:\n%s", typ.Field(i).Name, name, data)
		}
	}
	if !strings.Contains(string(data), "unknown_key:") {
		t.Errorf("inline Extra keys never reach the marshaled document:\n%s", data)
	}
}

// TestTimestampRoundTripsWithoutDriftOrPrecisionLoss covers the M3
// decision to fix "created"/"updated" at second precision on the wire: a
// sub-second component in the input is dropped, not fractionally
// preserved, and re-parsing the marshaled form recovers exactly the
// truncated value with no further drift.
func TestTimestampRoundTripsWithoutDriftOrPrecisionLoss(t *testing.T) {
	withNanos := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.FixedZone("PDT", -7*3600))
	ts := NewTimestamp(withNanos)

	wantWire := "2026-03-04T12:06:07Z" // normalized to UTC, no fractional seconds
	data, err := MarshalEntry(Entry{Title: "t", Created: ts, Updated: ts, Value: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "created: "+wantWire) {
		t.Fatalf("marshaled entry =\n%s\nwant a line containing %q", data, "created: "+wantWire)
	}

	back, err := UnmarshalEntry(data)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Created.Equal(ts.Time) || back.Created.Nanosecond() != 0 {
		t.Errorf("round-tripped Created = %v, want %v with no sub-second component", back.Created, ts.Time)
	}

	// A second round trip through the same struct must be a no-op — no
	// cumulative drift from repeated marshal/unmarshal cycles.
	again, err := MarshalEntry(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Errorf("second marshal = %q, want identical to the first %q", again, data)
	}
}

// TestEntryEmptyDescriptionAndFieldsRoundTripConsistently is the M3 test
// list's explicit case: an empty description and empty fields must not
// become `null`, and must not vanish on one round trip but reappear
// (or vice versa) on another.
func TestEntryEmptyDescriptionAndFieldsRoundTripConsistently(t *testing.T) {
	e := Entry{
		Title:     "no description or fields",
		Created:   NewTimestamp(time.Now()),
		Updated:   NewTimestamp(time.Now()),
		UpdatedBy: "laptop-1",
		Value:     "x",
	}

	data, err := MarshalEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "null") {
		t.Errorf("marshaled entry contains a literal null:\n%s", data)
	}
	if strings.Contains(string(data), "description:") || strings.Contains(string(data), "fields:") {
		t.Errorf("marshaled entry has an empty description/fields key instead of omitting it:\n%s", data)
	}

	back, err := UnmarshalEntry(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.Description != "" || back.Fields != nil {
		t.Errorf("round-tripped entry has Description=%q Fields=%v, want both empty", back.Description, back.Fields)
	}

	// Round-tripping again must reach the exact same wire form — no key
	// flickering into existence on a second pass.
	again, err := MarshalEntry(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Errorf("second marshal = %q, want identical to the first %q", again, data)
	}
}

// TestEntryPreservesUnknownFields is the M3 decision that a future
// gage's fields survive an older binary's round trip rather than being
// silently dropped on the next edit.
func TestEntryPreservesUnknownFields(t *testing.T) {
	src := "title: test\n" +
		"created: 2026-01-01T00:00:00Z\n" +
		"updated: 2026-01-01T00:00:00Z\n" +
		"updated_by: laptop-1\n" +
		"value: v\n" +
		"tags:\n" +
		"    - work\n" +
		"    - email\n" +
		"priority: 3\n"

	e, err := UnmarshalEntry([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if e.Extra["priority"] != 3 {
		t.Errorf("Extra[%q] = %v (%T), want the unrecognized scalar preserved as 3", "priority", e.Extra["priority"], e.Extra["priority"])
	}
	tags, ok := e.Extra["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "work" || tags[1] != "email" {
		t.Errorf("Extra[%q] = %v, want the unrecognized list preserved", "tags", e.Extra["tags"])
	}

	// Re-marshaling must not drop the unrecognized keys, even though
	// their position relative to the known fields isn't guaranteed.
	data, err := MarshalEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalEntry(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Extra, e.Extra) {
		t.Errorf("Extra after a re-marshal round trip = %v, want %v", back.Extra, e.Extra)
	}
}

// hostileStrings is the corpus every string-carrying field is held to.
//
// It is deliberately much wider than the M3 test list's five named
// hazards, because a benign representative of each named category passes
// against an implementation that still corrupts secrets: an earlier
// version of this package handed the struct straight to yaml.Marshal and
// passed a table of "leading dash / embedded newline / literal colon /
// trailing space / non-ASCII" while silently dropping the leading
// newline of "\nsecret" and writing a tab-containing value as YAML it
// could never parse again. The nasty cases are the point.
var hostileStrings = map[string]string{
	// The M3 test list's named hazards.
	"leading dash":     "-not-a-list-item",
	"embedded newline": "line one\nline two\nline three",
	"literal colon":    "user:pass@host:1234",
	"trailing space":   "trailing whitespace lives here   ",
	"non-ASCII":        "pässwörd 日本語 🔒",
	"empty":            "",

	// Leading newlines: a block scalar cannot represent the first blank
	// line, and the emitter drops it rather than picking another style.
	"leading newline":      "\nsecret",
	"two leading newlines": "\n\nsecret",
	"leading and trailing": "\nsecret\n",
	"only a newline":       "\n",
	"only two newlines":    "\n\n",
	"space then newline":   " \nsecret",

	// Tabs: emitted inside a block scalar they produce a document the
	// same library then refuses to parse.
	"leading tab":           "\ttab-first",
	"tab then newline":      "\tx\ny",
	"tab inside a line":     "tab\tinside",
	"tab and newline mixed": "mixed\ttab\nand newline",
	"tab only":              "\t",

	// Line endings and trailing whitespace inside multi-line values.
	"crlf":                   "line1\r\nline2",
	"lone cr":                "a\r b",
	"trailing space on line": "line1\nline2   \nline3",
	"trailing newline":       "secret\n",
	"two trailing newlines":  "secret\n\n",
	"indented first line":    "  indented\nnext",
	"indented later line":    "x\n  indented",
	"blank line between":     "para one\n\npara two\n",

	// Scalars that would resolve to some other YAML type unquoted.
	"yaml null word": "null",
	"tilde":          "~",
	"bool true":      "true",
	"bool yes":       "yes",
	"bool no":        "no",
	"bool on":        "off",
	"int-like":       "123",
	"octal-like":     "0123",
	"float-like":     "1.5",
	"hex-like":       "0x1F",

	// Indicator characters and other structural bait.
	"doc start":     "---",
	"doc end":       "...",
	"comment hash":  "#not-a-comment",
	"alias star":    "*anchor",
	"anchor amp":    "&anchor",
	"tag bang":      "!tag",
	"directive pct": "%directive",
	"flow mapping":  "{a: b}",
	"flow sequence": "[a, b]",
	"question key":  "? key",
	"pipe":          "|literal",
	"folded gt":     ">folded",
	"backtick":      "`backtick",
	"quotes":        "\"double\" and 'single'",

	// Bytes and runes that are not text-shaped.
	"nul byte":     "a\x00b",
	"ansi escape":  "\x1b[31mred",
	"single space": " ",
	"nbsp":         "a b",
	"bom":          "\ufeffbom",
	"combining":    "écombining",
	"long":         strings.Repeat("long secret ", 500),
}

// TestEntryValueRoundTripsYAMLHostileContent is the M3 test list's
// "round-trips byte-for-byte" requirement for the payload field.
func TestEntryValueRoundTripsYAMLHostileContent(t *testing.T) {
	for name, value := range hostileStrings {
		t.Run(name, func(t *testing.T) {
			e := Entry{
				Title:     "hostile value",
				Created:   NewTimestamp(time.Now()),
				Updated:   NewTimestamp(time.Now()),
				UpdatedBy: "laptop-1",
				Value:     value,
			}
			data, err := MarshalEntry(e)
			if err != nil {
				t.Fatalf("MarshalEntry: %v", err)
			}
			back, err := UnmarshalEntry(data)
			if err != nil {
				t.Fatalf("UnmarshalEntry: %v (wire form: %q)", err, data)
			}
			if back.Value != value {
				t.Errorf("round-tripped Value = %q, want %q (wire form: %q)", back.Value, value, data)
			}
		})
	}
}

// TestEntryEveryStringFieldRoundTripsHostileContent holds title,
// description, updated_by and both halves of a fields entry to the same
// standard as value. They are all user-supplied text — a title is as
// free-form as a note — so a hazard that corrupts one corrupts them all.
func TestEntryEveryStringFieldRoundTripsHostileContent(t *testing.T) {
	for name, s := range hostileStrings {
		t.Run(name, func(t *testing.T) {
			e := Entry{
				Title:       s,
				Description: s,
				Created:     NewTimestamp(time.Now()),
				Updated:     NewTimestamp(time.Now()),
				UpdatedBy:   s,
				Value:       "v",
				Fields:      map[string]string{s: s, "ordinary": s},
			}
			data, err := MarshalEntry(e)
			if err != nil {
				t.Fatalf("MarshalEntry: %v", err)
			}
			back, err := UnmarshalEntry(data)
			if err != nil {
				t.Fatalf("UnmarshalEntry: %v (wire form: %q)", err, data)
			}
			if back.Title != s {
				t.Errorf("Title = %q, want %q (wire: %q)", back.Title, s, data)
			}
			// An empty description is legitimately omitted, so it is the
			// one field the empty case cannot assert on.
			if s != "" && back.Description != s {
				t.Errorf("Description = %q, want %q (wire: %q)", back.Description, s, data)
			}
			if back.UpdatedBy != s {
				t.Errorf("UpdatedBy = %q, want %q (wire: %q)", back.UpdatedBy, s, data)
			}
			if back.Fields[s] != s {
				t.Errorf("Fields[%q] = %q, want %q (wire: %q)", s, back.Fields[s], s, data)
			}
			if back.Fields["ordinary"] != s {
				t.Errorf("Fields[ordinary] = %q, want %q (wire: %q)", back.Fields["ordinary"], s, data)
			}
		})
	}
}

// TestEntryMarshalIsDeterministic pins the "field order is stable" half
// of the M3 test list. Fields and Extra are maps, so without an explicit
// ordering the same entry would marshal to different bytes on different
// runs — which would turn every `edit` into a spurious git diff.
func TestEntryMarshalIsDeterministic(t *testing.T) {
	e := sampleEntry(time.Now())
	e.Fields = map[string]string{"z": "1", "a": "2", "m": "3", "b": "4", "q": "5"}
	e.Extra = map[string]any{"zeta": 1, "alpha": 2, "mu": 3}

	first, err := MarshalEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		got, err := MarshalEntry(e)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(first) {
			t.Fatalf("marshal #%d differs from the first:\n%s\nvs\n%s", i, got, first)
		}
	}
}

// TestEntryQuotedFallbackRoundTripsEverything exercises the safety net
// directly. MarshalEntry only reaches styleAlwaysQuoted when the
// readable styling fails its own round-trip check, which no known input
// does — so without driving it by hand this path would be untested
// precisely because it is the one that has to work when something else
// has already gone wrong.
func TestEntryQuotedFallbackRoundTripsEverything(t *testing.T) {
	for name, s := range hostileStrings {
		t.Run(name, func(t *testing.T) {
			e := Entry{
				Title:       s,
				Description: s,
				Created:     NewTimestamp(time.Now()),
				Updated:     NewTimestamp(time.Now()),
				UpdatedBy:   s,
				Value:       s,
				Fields:      map[string]string{s: s},
			}
			data, err := marshalEntryNodes(e, styleAlwaysQuoted)
			if err != nil {
				t.Fatalf("marshalEntryNodes: %v", err)
			}
			if err := verifyFaithful(data, e); err != nil {
				t.Errorf("the quoted fallback is not faithful: %v (wire: %q)", err, data)
			}
		})
	}
}

// TestEntryMultiLineValueStaysReadable guards the other half of the
// trade-off: the fix for the hostile cases must not turn every ordinary
// multi-line note into one long escaped line, because M5 shows this file
// to a human in $EDITOR.
func TestEntryMultiLineValueStaysReadable(t *testing.T) {
	e := Entry{
		Title:     "Recovery codes",
		Created:   NewTimestamp(time.Now()),
		Updated:   NewTimestamp(time.Now()),
		UpdatedBy: "laptop-1",
		Value:     "aaaa-bbbb\ncccc-dddd\neeee-ffff",
	}
	data, err := MarshalEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "value: |-\n    aaaa-bbbb\n    cccc-dddd\n    eeee-ffff\n") {
		t.Errorf("a plain multi-line value was not written as a readable block scalar:\n%s", data)
	}
}

// TestEncryptDecryptEntryRoundTripIsByteIdentical proves the whole chain
// — marshal, encrypt, decrypt — recovers the exact plaintext bytes
// MarshalEntry produced, at the crypto-wrapper level rather than through
// any file.
func TestEncryptDecryptEntryRoundTripIsByteIdentical(t *testing.T) {
	ident, recipient := testKeypair(t)
	e := sampleEntry(time.Now())

	plaintext, err := MarshalEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := Encrypt(plaintext, recipient)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(ct, wrapIdentity(t, ident))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("decrypted plaintext = %q, want byte-identical to %q", got, plaintext)
	}

	back, err := UnmarshalEntry(got)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, e) {
		t.Errorf("round-tripped entry = %+v, want %+v", back, e)
	}
}

// TestWriteEntryThenReadEntryRoundTrips is the milestone's definition of
// done at the vault/file level: a real vault, a real Identity from
// Unlock, a real entries/<uuid>.age file.
func TestWriteEntryThenReadEntryRoundTrips(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	entryID := NewEntryID()
	if err := v.WriteEntry(entryID, e); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}

	path := v.entryPath(entryID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected an entry file at %s: %v", path, err)
	}

	got, err := v.ReadEntry(entryID, &id)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	if !reflect.DeepEqual(got, e) {
		t.Errorf("round-tripped entry = %+v, want %+v", got, e)
	}
}

// TestEntryDecryptableByEveryRecipient writes one entry to a vault with
// three recipients and confirms every one of them can independently
// decrypt it with their own identity.
func TestEntryDecryptableByEveryRecipient(t *testing.T) {
	const vaultName = "shared"
	d1 := newTestDevice(t, vaultName, "laptop-1")
	d2 := newTestDevice(t, vaultName, "phone-1")
	d3 := newTestDevice(t, vaultName, "desktop-1")

	var v *Vault
	withXDGRoot(t, d1.root, func() {
		var err error
		v, err = Create(CreateSpec{
			Name:       vaultName,
			Path:       filepath.Join(t.TempDir(), vaultName),
			Type:       TypeGit,
			Method:     MethodPassphrase,
			Device:     d1.name,
			Recipients: []string{d1.pubkey, d2.pubkey, d3.pubkey},
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	e := sampleEntry(time.Now())
	entryID := NewEntryID()
	if err := v.WriteEntry(entryID, e); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}

	for _, d := range []testDevice{d1, d2, d3} {
		id := unlockAs(t, v, d)
		got, err := v.ReadEntry(entryID, &id)
		if err != nil {
			t.Errorf("ReadEntry as %s: %v", d.name, err)
		}
		if !reflect.DeepEqual(got, e) {
			t.Errorf("entry read as %s = %+v, want %+v", d.name, got, e)
		}
		if err := id.Close(); err != nil {
			t.Errorf("closing %s's identity: %v", d.name, err)
		}
	}
}

// TestReadEntryWithNonRecipientIdentityFailsWithTypedError: a stranger's
// identity — never a recipient of this entry at all — must fail with
// M2's ErrNotARecipient surfaced through the entry layer, not swallowed
// or relabeled.
func TestReadEntryWithNonRecipientIdentityFailsWithTypedError(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID := NewEntryID()
	if err := v.WriteEntry(entryID, sampleEntry(time.Now())); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}

	stranger, _ := testKeypair(t)
	strangerIdentity := wrapIdentity(t, stranger)

	got, err := v.ReadEntry(entryID, strangerIdentity)
	if err == nil {
		t.Fatal("expected ReadEntry with a non-recipient identity to fail")
	}
	if !errors.Is(err, ErrNotARecipient) {
		t.Errorf("error = %v, want it to wrap ErrNotARecipient", err)
	}
	if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("exit code = %v, want %v", exitcode.CodeOf(err), exitcode.LockedOrAuth)
	}
	if !reflect.DeepEqual(got, Entry{}) {
		t.Errorf("entry = %+v, want the zero value on failure", got)
	}
}

// TestReadEntryUnknownIDFails covers the "no such entry" case
// distinctly from a decrypt failure.
func TestReadEntryUnknownIDFails(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	_, err := v.ReadEntry(uuid.New(), &id)
	if err == nil {
		t.Fatal("expected ReadEntry for an unknown id to fail")
	}
	if !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("error = %v, want it to wrap ErrEntryNotFound", err)
	}
	if exitcode.CodeOf(err) != exitcode.NotFound {
		t.Errorf("exit code = %v, want %v", exitcode.CodeOf(err), exitcode.NotFound)
	}
}

// TestNewEntryIDsAreValidUUIDv4AndUnique generates a large number of ids
// and checks both properties the M3 test list names: every id is a
// well-formed UUIDv4, and repeated generation never collides.
func TestNewEntryIDsAreValidUUIDv4AndUnique(t *testing.T) {
	const n = 200_000
	seen := make(map[uuid.UUID]struct{}, n)
	for i := 0; i < n; i++ {
		id := NewEntryID()
		if id.Version() != 4 {
			t.Fatalf("id %d (%s) has version %d, want 4", i, id, id.Version())
		}
		if id.Variant() != uuid.RFC4122 {
			t.Fatalf("id %d (%s) has variant %v, want RFC4122", i, id, id.Variant())
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("id %d (%s) collided with an earlier generation", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestWriteEntryFilePermissions covers the M3 test list's on-disk
// tightness requirement: 0600 on the entry file, 0700 on entries/.
func TestWriteEntryFilePermissions(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID := NewEntryID()
	if err := v.WriteEntry(entryID, sampleEntry(time.Now())); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}

	fi := statPath(t, v.entryPath(entryID))
	di := statPath(t, v.entriesDir())
	assertUnixPerm(t, fi, 0o600, "entry file")
	assertUnixPerm(t, di, 0o700, "entries directory")
}

// TestWriteEntryIsAtomic mirrors atomicfile's own discriminating test at
// this layer: overwriting an existing entry replaces the destination
// path's underlying file object rather than truncating it in place, which
// is what actually backs the "never a truncated .age file" guarantee.
func TestWriteEntryIsAtomic(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID := NewEntryID()
	if err := v.WriteEntry(entryID, sampleEntry(time.Now())); err != nil {
		t.Fatalf("WriteEntry #1: %v", err)
	}
	before := statFileHandle(t, v.entryPath(entryID))

	updated := sampleEntry(time.Now())
	updated.Value = "a different secret"
	if err := v.WriteEntry(entryID, updated); err != nil {
		t.Fatalf("WriteEntry #2: %v", err)
	}
	after := statFileHandle(t, v.entryPath(entryID))

	if os.SameFile(before, after) {
		t.Error("WriteEntry wrote the destination in place; a crash mid-write would leave a truncated .age file")
	}

	got, err := v.ReadEntry(entryID, &id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "a different secret" {
		t.Errorf("Value after overwrite = %q, want the updated value", got.Value)
	}

	// No leftover temp files in entries/.
	des, err := os.ReadDir(v.entriesDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(des) != 1 {
		t.Errorf("entries/ has %d files after one overwrite, want 1 (leftover temp file?): %v", len(des), des)
	}
}

// TestEntryIDsListsFilesInEntriesDir covers enumeration: every written
// id comes back, sorted, with no decryption involved, and a vault with no
// entries yet reports none rather than erroring.
func TestEntryIDsListsFilesInEntriesDir(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	none, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("EntryIDs on a fresh vault = %v, want none", none)
	}

	var written []uuid.UUID
	for i := 0; i < 3; i++ {
		eid := NewEntryID()
		if err := v.WriteEntry(eid, sampleEntry(time.Now())); err != nil {
			t.Fatal(err)
		}
		written = append(written, eid)
	}
	// A stray file that isn't one of ours must not break enumeration.
	if err := os.WriteFile(filepath.Join(v.entriesDir(), ".DS_Store"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(written) {
		t.Fatalf("EntryIDs returned %d ids, want %d: %v", len(got), len(written), got)
	}
	wantSet := make(map[uuid.UUID]bool, len(written))
	for _, w := range written {
		wantSet[w] = true
	}
	for _, g := range got {
		if !wantSet[g] {
			t.Errorf("EntryIDs returned unexpected id %s", g)
		}
	}

	// EntryIDs promises a stable order; readdir order is not one.
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].String() < got[j].String() }) {
		t.Errorf("EntryIDs returned an unsorted listing: %v", got)
	}
}

// TestWriteEntryTightensAnExistingLooseEntriesDir covers the branch that
// exists for vaults created before entries/ was 0700 (M1 created it
// 0750) and for a umask that widened it: WriteEntry corrects the mode
// rather than trusting whatever it finds.
func TestWriteEntryTightensAnExistingLooseEntriesDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits aren't modeled on Windows")
	}
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	if err := os.Chmod(v.entriesDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := v.WriteEntry(NewEntryID(), sampleEntry(time.Now())); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	assertUnixPerm(t, statPath(t, v.entriesDir()), 0o700, "entries directory")
}

// TestEntryFileRevealsNothingInPlaintext is the property the whole
// on-disk layout exists for: with opaque UUID filenames, someone with
// read access but no key learns nothing — so no part of the entry may
// appear in the file, and the filename must carry no metadata either.
func TestEntryFileRevealsNothingInPlaintext(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	entryID := NewEntryID()
	if err := v.WriteEntry(entryID, e); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(v.entryPath(entryID))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		e.Title, e.Description, e.Value, e.UpdatedBy,
		e.Fields["username"], e.Fields["totp_seed"],
	} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("the entry file contains %q in the clear", secret)
		}
	}
	if strings.Contains(filepath.Base(v.entryPath(entryID)), e.Title) {
		t.Error("the filename leaks the entry's title")
	}
}

// TestWriteEntryRejectsARecipientItCannotEncryptTo: .age-recipients may
// legally contain a plugin recipient (age1yubikey1...) that this build
// cannot encrypt to. WriteEntry must fail loudly rather than quietly
// writing a file that recipient could never open.
func TestWriteEntryRejectsARecipientItCannotEncryptTo(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	path := filepath.Join(v.Path, ".age-recipients")
	existing, err := recipients.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := recipients.Write(path, append(existing, testRecipient2)); err != nil {
		t.Fatal(err)
	}

	err = v.WriteEntry(NewEntryID(), sampleEntry(time.Now()))
	if err == nil {
		t.Fatal("expected WriteEntry to fail on a recipient this build cannot encrypt to")
	}
	if !errors.Is(err, ErrInvalidRecipient) {
		t.Errorf("error = %v, want it to wrap ErrInvalidRecipient", err)
	}
	// Nothing may have been written on the failing path.
	ids, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("a failed WriteEntry left %d entries behind: %v", len(ids), ids)
	}
}

// TestWriteEntryWithoutARecipientsFileFails: encrypting to nobody would
// produce a file no one can ever read, so a vault missing its
// .age-recipients has to be an error rather than a silent no-recipient
// write.
func TestWriteEntryWithoutARecipientsFileFails(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	if err := os.Remove(filepath.Join(v.Path, ".age-recipients")); err != nil {
		t.Fatal(err)
	}
	if err := v.WriteEntry(NewEntryID(), sampleEntry(time.Now())); err == nil {
		t.Fatal("expected WriteEntry to fail with no .age-recipients present")
	}
}

// TestReadEntryOnCorruptCiphertextFails is the other half of the typed
// error surface: a damaged file must not read as "wrong key", since the
// two mean very different things to whoever has to fix it.
func TestReadEntryOnCorruptCiphertextFails(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID := NewEntryID()
	if err := v.WriteEntry(entryID, sampleEntry(time.Now())); err != nil {
		t.Fatal(err)
	}
	path := v.entryPath(entryID)
	ct, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Truncating mid-payload leaves an intact header and a damaged body.
	if err := os.WriteFile(path, ct[:len(ct)-8], 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := v.ReadEntry(entryID, &id); !errors.Is(err, ErrCorruptCiphertext) {
		t.Errorf("error = %v, want it to wrap ErrCorruptCiphertext", err)
	}
}

// statPath is os.Stat with the test-fatal boilerplate factored out.
func statPath(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi
}

// statFileHandle returns path's identity via an open handle rather than
// os.Stat(path) alone, mirroring atomicfile_test.go's statFile: on
// Windows, os.Stat defers resolving the file-index fields os.SameFile
// compares until first use, and resolves them by reopening whatever
// currently lives at path — so two path-based stats taken before and
// after a rename-replace can compare equal regardless of whether the
// underlying file object changed. An already-open handle's Stat resolves
// the file index immediately, at open time, avoiding that.
func statFileHandle(t *testing.T, path string) os.FileInfo {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// assertUnixPerm checks fi's permission bits, skipping on Windows where
// Unix permission bits aren't modeled — see unlock_test.go's identical
// pattern for CreateIdentity's own file/directory permissions.
func assertUnixPerm(t *testing.T, fi os.FileInfo, want os.FileMode, what string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %04o, want %04o", what, got, want)
	}
}
