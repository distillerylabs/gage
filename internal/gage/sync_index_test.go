package gage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/distillerylabs/gage/internal/gage/gittest"
)

// newSyncSession wires a Session over a vault that has a remote, the way
// cmd/gage's REPL does.
func newSyncSession(t *testing.T, v *Vault) *Session {
	t.Helper()

	return NewSession(SessionConfig{
		Open:     func(name string) (*Vault, error) { return v, nil },
		Prompter: &fakePrompter{passphrases: []string{testPassphrase, testPassphrase, testPassphrase}},
		Current:  v.Name,
	})
}

// TestUseFetchesAndFastForwards is `use`'s own half of the sync model:
// entering a vault is the "catch me up" moment, whether or not this
// session already holds its key.
func TestUseFetchesAndFastForwards(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	_ = id.Close()

	s := newSyncSession(t, v)
	defer func() { _ = s.Close() }()

	// Unlock it once, so the `use` below is the already-unlocked path —
	// the one where no Vault.Unlock runs and the pull has to be asked for
	// explicitly rather than riding along with it.
	if err := s.Use("personal"); err != nil {
		t.Fatal(err)
	}

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "notes.txt", "from elsewhere", "another device's commit")

	if err := s.Use("personal"); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v.Path, "notes.txt")); err != nil {
		t.Errorf("`use` on an already-unlocked vault did not fast-forward it: %v", err)
	}
}

// TestPullDuringASessionInvalidatesTheIndex is M7's cache meeting M8a's
// sync: entries that arrive by pull have to show up in the very next ls,
// with no manual reindex. The index is built first, deliberately, so the
// test would fail if the pull left a stale cache in place.
func TestPullDuringASessionInvalidatesTheIndex(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")

	// One entry, published, so the other device has something to build on.
	if _, err := v.Insert(sampleEntry(time.Now()), false, &id); err != nil {
		t.Fatal(err)
	}
	_ = id.Close()

	s := newSyncSession(t, v)
	defer func() { _ = s.Close() }()

	before, err := s.List("personal")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("List returned %d entries, want 1", len(before))
	}

	// Another device adds an entry of its own. It's written through the
	// same vault layout, so it is a real entry as far as this vault is
	// concerned — this device just can't decrypt it, which ls doesn't
	// need to do for the count to change.
	other := gittest.NewDevice(t, remote)
	newID := NewEntryID()
	other.Write(t, "entries/"+newID.String()+".age", string(encryptForTest(t, v)))
	other.Commit(t, newID.String())
	other.Push(t)

	// `use` on a vault this session already holds still means "catch me
	// up", and the pull it performs must invalidate the cache built above.
	if err := s.Use("personal"); err != nil {
		t.Fatalf("Use: %v", err)
	}

	after, err := s.List("personal")
	if err != nil {
		t.Fatalf("List after the pull: %v", err)
	}
	if len(after) != 2 {
		t.Errorf("List returned %d entries after a pull that added one, want 2 — "+
			"the index was not invalidated", len(after))
	}
}

// TestPullThatBringsNothingLeavesTheIndexAlone is the other half of the
// rule: rebuilding on every unlock would defeat the point of caching.
func TestPullThatBringsNothingLeavesTheIndexAlone(t *testing.T) {
	v, id, _ := newSyncVault(t, "personal", "laptop-1")

	if _, err := v.Insert(sampleEntry(time.Now()), false, &id); err != nil {
		t.Fatal(err)
	}
	_ = id.Close()

	decrypts := 0
	v.onDecrypt = func() { decrypts++ }

	s := newSyncSession(t, v)
	defer func() { _ = s.Close() }()

	if _, err := s.List("personal"); err != nil {
		t.Fatalf("List: %v", err)
	}
	afterFirstBuild := decrypts
	if afterFirstBuild == 0 {
		t.Fatal("the first List decrypted nothing; the index was never built")
	}

	// A `use` with nothing to fetch: the index must survive it.
	if err := s.Use("personal"); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if _, err := s.List("personal"); err != nil {
		t.Fatalf("List after a no-op pull: %v", err)
	}

	if decrypts != afterFirstBuild {
		t.Errorf("decrypt count went %d -> %d across a pull that brought nothing; "+
			"the index was rebuilt gratuitously", afterFirstBuild, decrypts)
	}
}

// encryptForTest produces ciphertext this vault's recipients can read, so
// a stand-in "other device" can add a genuine entry file without holding
// an identity of its own.
func encryptForTest(t *testing.T, v *Vault) []byte {
	t.Helper()

	e := sampleEntry(time.Now())
	e.Title = "Written elsewhere"
	plaintext, err := MarshalEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	to, err := v.encryptRecipients()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := Encrypt(plaintext, to...)
	if err != nil {
		t.Fatal(err)
	}
	return ciphertext
}
