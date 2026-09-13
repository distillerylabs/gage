package vaultconfig

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func validFile() File {
	return File{
		Vault: VaultMeta{
			Name:          "myvault",
			ID:            "9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497",
			Type:          "git",
			FormatVersion: CurrentFormatVersion,
			Created:       "2026-08-29",
		},
		Method: Method{Default: "passphrase"},
		Recipients: []Recipient{
			{Device: "laptop-1", Pubkey: "age1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0savhh7m"},
			{Device: "recovery-key", Pubkey: "age1yubikey1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0s9hkmc0"},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	want := validFile()

	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestRecipientsCarryOnlyDeviceAndPubkey(t *testing.T) {
	// Recipient's fields are asserted structurally: exactly two exported
	// fields, Device and Pubkey — no per-recipient method field, which
	// would leak which recipient is the softest target (Q-METHOD-SCOPE).
	typ := reflect.TypeOf(Recipient{})
	if typ.NumField() != 2 {
		t.Fatalf("Recipient has %d fields, want exactly 2 (Device, Pubkey): %+v", typ.NumField(), typ)
	}
	if _, ok := typ.FieldByName("Device"); !ok {
		t.Error("Recipient has no Device field")
	}
	if _, ok := typ.FieldByName("Pubkey"); !ok {
		t.Error("Recipient has no Pubkey field")
	}
}

func TestReadRejectsUnrecognizedFormatVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	f := validFile()
	f.Vault.FormatVersion = 99
	if err := Write(path, f); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path)
	if err == nil {
		t.Fatal("expected Read to reject an unrecognized format_version")
	}
	if !errors.Is(err, ErrUnsupportedFormatVersion) {
		t.Errorf("Read error = %v, want it to wrap ErrUnsupportedFormatVersion", err)
	}
	// The plan asks for an "upgrade gage" error specifically: a typed
	// refusal an operator can't act on is only half the requirement.
	if !strings.Contains(err.Error(), "upgrade gage") {
		t.Errorf("Read error doesn't tell the operator to upgrade gage: %v", err)
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("Read error doesn't name the version it found: %v", err)
	}
}

func TestReadRejectsMissingFormatVersion(t *testing.T) {
	// A file with no format_version at all decodes FormatVersion as the
	// zero value, which must be treated the same as "unrecognized" —
	// never best-effort parsed.
	path := filepath.Join(t.TempDir(), "config.toml")
	f := validFile()
	f.Vault.FormatVersion = 0
	if err := Write(path, f); err != nil {
		t.Fatal(err)
	}

	if _, err := Read(path); !errors.Is(err, ErrUnsupportedFormatVersion) {
		t.Errorf("Read error = %v, want ErrUnsupportedFormatVersion", err)
	}
}

func TestReadRejectsPathTraversalDeviceNameBeforeUse(t *testing.T) {
	cases := []string{
		"../../../../etc/cron.d/x",
		"/etc/passwd",
		`..\..\Windows\x`,
	}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			f := validFile()
			f.Recipients = []Recipient{{Device: bad, Pubkey: "age1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0savhh7m"}}
			if err := Write(path, f); err != nil {
				t.Fatal(err)
			}

			_, err := Read(path)
			if err == nil {
				t.Fatalf("expected Read to reject device name %q", bad)
			}
			if !errors.Is(err, ErrInvalidDeviceName) {
				t.Errorf("Read error = %v, want it to wrap ErrInvalidDeviceName", err)
			}
		})
	}
}

func TestFormatVersionCheckedBeforeDeviceNames(t *testing.T) {
	// Both the format_version and the device name are bad; the
	// format_version rejection must win, since the file must never be
	// acted on (not even far enough to look at recipients) once its
	// version is unrecognized.
	path := filepath.Join(t.TempDir(), "config.toml")
	f := validFile()
	f.Vault.FormatVersion = 99
	f.Recipients = []Recipient{{Device: "../../etc/x", Pubkey: "age1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0savhh7m"}}
	if err := Write(path, f); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path)
	if !errors.Is(err, ErrUnsupportedFormatVersion) {
		t.Errorf("Read error = %v, want ErrUnsupportedFormatVersion (checked before device names)", err)
	}
	if errors.Is(err, ErrInvalidDeviceName) {
		t.Errorf("Read error also reports ErrInvalidDeviceName; format_version should be checked and reported first")
	}
}

func TestReadMissingFile(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Fatal("expected an error reading a nonexistent file")
	}
}

// TestReadRejectsAnIDThatIsNotAUUID is Q-DEVICE-NAME's untrusted-input
// rule applied to the field A20 adds. The id becomes a path component
// under $GAGE_DATA and $GAGE_STATE, and it arrives from a committed file
// any git-writer can edit, so it is refused on read — before any path is
// constructed from it — exactly as a recipient's device name is.
func TestReadRejectsAnIDThatIsNotAUUID(t *testing.T) {
	cases := map[string]string{
		"absent":            "",
		"malformed":         "not-a-uuid",
		"traversal":         "../../../../etc/cron.d/x",
		"traversal in path": "../9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497",
		"absolute path":     "/etc/passwd",
		"windows traversal": `..\..\Windows\x`,
		"a vault name":      "personal",
		"nil uuid":          "00000000-0000-0000-0000-000000000000",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			f := validFile()
			f.Vault.ID = id
			if err := Write(path, f); err != nil {
				t.Fatal(err)
			}

			_, err := Read(path)
			if err == nil {
				t.Fatalf("expected Read to reject id %q", id)
			}
			if !errors.Is(err, ErrInvalidVaultID) {
				t.Errorf("Read error = %v, want it to wrap ErrInvalidVaultID", err)
			}
		})
	}
}

// TestCurrentFormatVersionIsTwo pins the bump itself. A20's migration
// story — "re-create the vault" — only works if the marker that triggers
// it actually moved.
func TestCurrentFormatVersionIsTwo(t *testing.T) {
	if CurrentFormatVersion != 2 {
		t.Errorf("CurrentFormatVersion = %d, want 2 (A20 raised it when [vault].id was added)", CurrentFormatVersion)
	}
}

// TestReadRefusesAVaultOlderThanThisBuildWithARecreateMessage is the
// direction A20 creates: a v1 vault has no [vault].id at all, so a newer
// binary cannot read it — and telling that operator to upgrade gage
// would be advice that cannot work. The refusal is directional.
func TestReadRefusesAVaultOlderThanThisBuildWithARecreateMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	f := validFile()
	f.Vault.FormatVersion = 1
	if err := Write(path, f); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path)
	if err == nil {
		t.Fatal("expected Read to reject a format_version older than this build's")
	}
	if !errors.Is(err, ErrUnsupportedFormatVersion) {
		t.Errorf("Read error = %v, want it to wrap ErrUnsupportedFormatVersion", err)
	}
	if strings.Contains(err.Error(), "upgrade gage") {
		t.Errorf("Read tells the operator to upgrade gage for a vault OLDER than this build; that advice cannot work: %v", err)
	}
	if !strings.Contains(err.Error(), "re-create") {
		t.Errorf("Read doesn't tell the operator to re-create the vault: %v", err)
	}
	if !strings.Contains(err.Error(), "make reset-local-state") {
		t.Errorf("Read doesn't name the command that clears local state: %v", err)
	}
	if !strings.Contains(err.Error(), "1") {
		t.Errorf("Read error doesn't name the version it found: %v", err)
	}
}

// TestFormatVersionCheckedBeforeTheID is the same ordering rule
// TestFormatVersionCheckedBeforeDeviceNames asserts, for the other field
// Read validates: a v1 vault's *missing* id must be reported as a
// version mismatch, not as a malformed id. Only one of those two
// messages tells the operator what actually happened.
func TestFormatVersionCheckedBeforeTheID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	f := validFile()
	f.Vault.FormatVersion = 1
	f.Vault.ID = ""
	if err := Write(path, f); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path)
	if !errors.Is(err, ErrUnsupportedFormatVersion) {
		t.Errorf("Read error = %v, want ErrUnsupportedFormatVersion (checked before the id)", err)
	}
	if errors.Is(err, ErrInvalidVaultID) {
		t.Errorf("Read also reports ErrInvalidVaultID; format_version should be checked and reported first: %v", err)
	}
}
