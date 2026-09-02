package recipients

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".age-recipients")
	want := []string{
		"age1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0savhh7m",
		"age1yubikey1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0s9hkmc0",
	}

	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

func TestWriteHasNoFramingOnePerLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".age-recipients")
	keys := []string{"age1aaa", "age1bbb"}
	if err := Write(path, keys); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "age1aaa\nage1bbb\n"
	if string(data) != want {
		t.Errorf("file content = %q, want %q (one key per line, no framing)", data, want)
	}
}

func TestReadSkipsBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".age-recipients")
	if err := os.WriteFile(path, []byte("age1aaa\n\nage1bbb\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"age1aaa", "age1bbb"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Read = %v, want %v", got, want)
	}
}

func TestReadMissingFile(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error reading a nonexistent file")
	}
}
