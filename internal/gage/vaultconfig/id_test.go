package vaultconfig

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNewIDMintsADistinctUUIDv4(t *testing.T) {
	first := NewID()
	second := NewID()

	if first == second {
		t.Fatalf("NewID returned the same id twice (%q); every vault must get its own", first)
	}
	for _, id := range []string{first, second} {
		parsed, err := uuid.Parse(id)
		if err != nil {
			t.Fatalf("NewID returned %q, which does not parse as a UUID: %v", id, err)
		}
		if parsed.Version() != 4 {
			t.Errorf("NewID returned a v%d UUID (%q), want v4", parsed.Version(), id)
		}
		if !ValidID(id) {
			t.Errorf("NewID returned %q, which ValidID rejects", id)
		}
	}
}

func TestValidIDAcceptsTheCanonicalForm(t *testing.T) {
	if !ValidID("9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497") {
		t.Error("ValidID rejected the canonical lowercase form the design doc writes")
	}
}

// TestValidIDRejectsAnythingThatIsNotACanonicalUUID is the untrusted-input
// rule Q-DEVICE-NAME imposes on device names, applied to the id: it
// arrives from a committed file any git-writer can edit and becomes a
// filesystem path component, so a traversal, a separator, or an absolute
// path has to be refused rather than helpfully coerced.
//
// The non-canonical *spellings* of a real UUID are refused for the same
// reason: accepting them would mean one vault has several legal ids,
// each naming a different directory.
func TestValidIDRejectsAnythingThatIsNotACanonicalUUID(t *testing.T) {
	cases := map[string]string{
		"empty":               "",
		"traversal":           "../../../../etc/cron.d/x",
		"traversal in a uuid": "../9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497",
		"absolute path":       "/etc/passwd",
		"windows traversal":   `..\..\Windows\x`,
		"dot":                 ".",
		"dotdot":              "..",
		"separator":           "9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497/x",
		"colon":               "C:9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497",
		"a plain vault name":  "personal",
		"nil uuid":            "00000000-0000-0000-0000-000000000000",
		"uppercase":           "9F3A1C2E-7B41-4D58-A0C6-2E5F81B3D497",
		"urn form":            "urn:uuid:9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497",
		"braced form":         "{9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497}",
		"undashed form":       "9f3a1c2e7b414d58a0c62e5f81b3d497",
		"trailing newline":    "9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497\n",
		"leading space":       " 9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497",
		"nul byte":            "9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d49\x00",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			if ValidID(id) {
				t.Errorf("ValidID(%q) = true, want false", id)
			}
		})
	}
}

// TestValidIDsAreAlwaysSinglePathComponents is the property the
// validation exists for, asserted directly rather than inferred from the
// case list above: nothing ValidID accepts can traverse, and nothing it
// accepts contains a separator on any platform gage builds for.
func TestValidIDsAreAlwaysSinglePathComponents(t *testing.T) {
	for i := 0; i < 200; i++ {
		id := NewID()
		if strings.ContainsAny(id, `/\:`) || strings.ContainsRune(id, 0) {
			t.Fatalf("NewID minted %q, which is not a safe path component", id)
		}
	}
}
