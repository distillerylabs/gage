package main

import (
	"os"
	"testing"
)

// TestScratchDirIsAWritableDirectory holds on every platform: whatever
// scratchDir() picks, editYAML must actually be able to create a file
// there.
func TestScratchDirIsAWritableDirectory(t *testing.T) {
	dir := scratchDir()
	if dir == "" {
		t.Fatal("scratchDir() returned an empty path")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("scratchDir() = %q, which doesn't exist: %v", dir, err)
	}
	if !info.IsDir() {
		t.Fatalf("scratchDir() = %q, which is not a directory", dir)
	}

	f, err := os.CreateTemp(dir, "gage-scratchdir-test-*")
	if err != nil {
		t.Fatalf("could not create a file in scratchDir() = %q: %v", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}
