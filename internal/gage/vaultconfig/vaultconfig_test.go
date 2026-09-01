package vaultconfig

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func validFile() File {
	return File{
		Vault: VaultMeta{
			Name:          "myvault",
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
