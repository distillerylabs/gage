package gage

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// identitiesDirEntries lists the file names in a vault's identities
// directory, or nil if the directory is gone.
func identitiesDirEntries(t *testing.T, vaultID string) []string {
	t.Helper()
	dir, err := IdentitiesDir(vaultID)
	if err != nil {
		t.Fatal(err)
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

// TestIdentitiesDirectoryCarriesAPlaintextMarkerNamingTheVault is A20's
// human-findability half: the directory is named by an opaque UUID, and
// the design doc explicitly permits backing up a device's wrapped
// identity file by hand, so the directory has to say which vault it
// belongs to.
func TestIdentitiesDirectoryCarriesAPlaintextMarkerNamingTheVault(t *testing.T) {
	isolateXDG(t)
	id := vaultIDForTest("personal")

	if _, err := CreateIdentity(id, "personal", "laptop-1", &fakePrompter{passphrases: []string{testPassphrase}}); err != nil {
		t.Fatal(err)
	}

	dir, err := IdentitiesDir(id)
	if err != nil {
		t.Fatal(err)
	}
	var marker string
	for _, name := range identitiesDirEntries(t, id) {
		if strings.HasSuffix(name, identityFileExt) {
			continue
		}
		if marker != "" {
			t.Fatalf("more than one non-identity file in %s: %v", dir, identitiesDirEntries(t, id))
		}
		marker = name
	}
	if marker == "" {
		t.Fatalf("no plaintext marker in %s; an opaque UUID directory has to say which vault it is", dir)
	}

	// #nosec G304 -- test fixture path under the isolated XDG root.
	data, err := os.ReadFile(filepath.Join(dir, marker))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "personal" {
		t.Errorf("marker %s contains %q, want it to name the vault (%q)", marker, data, "personal")
	}
}

// TestNothingReadsTheMarkerToMakeADecision is the other half of A20's
// wording: the marker is advisory. Deleting it must change no behavior
// at all — it is a label for a human reading a directory listing, never
// a second source of truth about which vault an identity belongs to.
func TestNothingReadsTheMarkerToMakeADecision(t *testing.T) {
	isolateXDG(t)
	id := registerVault(t, "personal", "laptop-1", MethodPassphrase)

	pubkey, err := CreateIdentity(id, "personal", "laptop-1", &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := IdentitiesDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, identitiesMarkerFileName)); err != nil {
		t.Fatalf("removing the marker: %v", err)
	}

	v := &Vault{Name: "personal", ID: id}

	has, err := HasIdentity(id, "laptop-1")
	if err != nil || !has {
		t.Errorf("HasIdentity without the marker = %v, %v; want true, nil", has, err)
	}
	listed, err := v.ListIdentities()
	if err != nil {
		t.Fatalf("ListIdentities without the marker: %v", err)
	}
	if len(listed) != 1 || listed[0].Device != "laptop-1" {
		t.Errorf("ListIdentities without the marker = %+v, want exactly laptop-1", listed)
	}

	unlocked, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("Unlock without the marker: %v", err)
	}
	defer func() { _ = unlocked.Close() }()
	if unlocked.Recipient() != pubkey {
		t.Errorf("unlocked key = %q, want %q", unlocked.Recipient(), pubkey)
	}

	// And the marker is *not* silently recreated by any of the above,
	// which would make it look like something was maintaining it.
	if _, err := os.Stat(filepath.Join(dir, identitiesMarkerFileName)); !os.IsNotExist(err) {
		t.Errorf("a read path recreated the marker: %v", err)
	}
}

// TestRemovingTheLastIdentityLeavesNoOrphanedDirectory is the decision
// this milestone settled rather than discovering at the keyboard: a
// marker file makes the directory never empty, so RemoveIdentity's
// trailing prune — which succeeds only on an empty directory — would
// silently become a no-op and every removed vault would leave a
// directory behind holding one marker. Nothing would break, which is
// exactly why it needs a test.
//
// The end state is asserted, not the mechanism.
func TestRemovingTheLastIdentityLeavesNoOrphanedDirectory(t *testing.T) {
	isolateXDG(t)
	id := vaultIDForTest("personal")

	if _, err := CreateIdentity(id, "personal", "laptop-1", &fakePrompter{passphrases: []string{testPassphrase}}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveIdentity(id, "laptop-1"); err != nil {
		t.Fatalf("RemoveIdentity: %v", err)
	}

	dir, err := IdentitiesDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s survived the removal of its last identity, holding %v", dir, identitiesDirEntries(t, id))
	}
}

// TestRemovingANonLastIdentityLeavesTheMarkerInPlace is the pair that
// makes the removal above deliberate rather than incidental: a directory
// that still holds keys stays self-describing.
func TestRemovingANonLastIdentityLeavesTheMarkerInPlace(t *testing.T) {
	isolateXDG(t)
	id := vaultIDForTest("personal")

	for _, device := range []string{"laptop-1", "desktop-2"} {
		if _, err := CreateIdentity(id, "personal", device, &fakePrompter{passphrases: []string{testPassphrase}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveIdentity(id, "laptop-1"); err != nil {
		t.Fatalf("RemoveIdentity: %v", err)
	}

	got := identitiesDirEntries(t, id)
	want := []string{"desktop-2" + identityFileExt, identitiesMarkerFileName}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("identities directory holds %v, want %v", got, want)
	}
}
