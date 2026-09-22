package recipients

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestReadRejectsALineLongerThanTheScannerCanBuffer is Read's own
// scanner.Err() branch, driven honestly rather than injected: a
// bufio.Scanner using the default split function refuses any single
// line past bufio.MaxScanTokenSize, and a stray or hostile
// .age-recipients file is exactly the kind of input that could contain
// one. This is a real failure mode of a real file, not a fault a test
// double manufactures.
func TestReadRejectsALineLongerThanTheScannerCanBuffer(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".age-recipients")
	huge := bytes.Repeat([]byte("a"), bufio.MaxScanTokenSize+1)
	if err := os.WriteFile(path, huge, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path)
	if err == nil {
		t.Fatal("expected an error reading a line longer than the scanner can buffer")
	}
	if !strings.Contains(err.Error(), "reading") {
		t.Errorf("error = %v, want it to name the read that failed", err)
	}
}

// TestWriteWrapsAnAtomicfileFailure is Write's own error path: whatever
// atomicfile.WriteFile reports comes back named as a recipients-file
// write, not as a bare atomicfile error a caller can't attribute.
func TestWriteWrapsAnAtomicfileFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does", "not", "exist", ".age-recipients")
	err := Write(path, []string{"age1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0savhh7m"})
	if err == nil {
		t.Fatal("expected an error writing into a nonexistent directory")
	}
	if !strings.Contains(err.Error(), "writing") {
		t.Errorf("error = %v, want it to name the write that failed", err)
	}
}
