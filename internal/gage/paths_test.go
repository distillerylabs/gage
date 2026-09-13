package gage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/vaultconfig"
	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// fixedVaultID is a canonical id for tests that only need *a* valid one.
// Tests about two different vaults mint their own with vaultconfig.NewID.
const fixedVaultID = "9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497"

func TestIdentityFilePathShape(t *testing.T) {
	dataDir, err := xdgpaths.DataDir()
	if err != nil {
		t.Fatal(err)
	}

	got, err := IdentityFilePath(fixedVaultID, "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataDir, "identities", fixedVaultID, "laptop-1.age")
	if got != want {
		t.Errorf("IdentityFilePath = %q, want %q", got, want)
	}
}

func TestIdentityFilePathIsSiblingOfVaultsDir(t *testing.T) {
	identityPath, err := IdentityFilePath(fixedVaultID, "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	vaultsDir, err := VaultsDir()
	if err != nil {
		t.Fatal(err)
	}

	// identities/ must be a sibling of vaults/ under $GAGE_DATA, never
	// nested inside a specific vault's own directory.
	if strings.HasPrefix(identityPath, vaultsDir) {
		t.Errorf("identity path %q is nested under vaults dir %q, want a sibling", identityPath, vaultsDir)
	}

	gageData := filepath.Dir(vaultsDir)
	if filepath.Dir(filepath.Dir(identityPath)) != filepath.Join(gageData, "identities") {
		t.Errorf("identity path %q is not under <GAGE_DATA>/identities/<vault-id>/", identityPath)
	}
}

func TestTrustCacheDirShape(t *testing.T) {
	stateDir, err := xdgpaths.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := TrustCacheDir(fixedVaultID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(stateDir, fixedVaultID); got != want {
		t.Errorf("TrustCacheDir = %q, want %q", got, want)
	}
}

func TestLockFilePathShape(t *testing.T) {
	stateDir, err := xdgpaths.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := LockFilePath(fixedVaultID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(stateDir, "locks", fixedVaultID+".lock"); got != want {
		t.Errorf("LockFilePath = %q, want %q", got, want)
	}
}

// TestPerVaultPathsRefuseAnythingButAValidID is A20's untrusted-input
// half, asserted at the construction sites rather than only in
// vaultconfig.ValidID: the id arrives from a committed file any git-writer can
// edit, so every path built from one validates it on the way out too.
//
// The empty string is in the list on purpose. A *Vault built without an
// ID — a construction site in cmd/gage that was missed — yields exactly
// that, and it has to fail loudly rather than quietly writing to a
// directory named "".
func TestPerVaultPathsRefuseAnythingButAValidID(t *testing.T) {
	bad := map[string]string{
		"empty":             "",
		"a local name":      "personal",
		"traversal":         "../../../../etc/cron.d/x",
		"absolute path":     "/etc/passwd",
		"windows traversal": `..\..\Windows\x`,
		"separator":         fixedVaultID + "/x",
		"uppercase":         strings.ToUpper(fixedVaultID),
	}
	builders := map[string]func(string) (string, error){
		"IdentitiesDir": IdentitiesDir,
		"IdentityFilePath": func(id string) (string, error) {
			return IdentityFilePath(id, "laptop-1")
		},
		"TrustCacheDir": TrustCacheDir,
		"LockFilePath":  LockFilePath,
	}
	for builderName, build := range builders {
		for caseName, id := range bad {
			t.Run(builderName+"/"+caseName, func(t *testing.T) {
				got, err := build(id)
				if err == nil {
					t.Fatalf("%s(%q) = %q, want an error", builderName, id, got)
				}
				if !strings.Contains(err.Error(), "vault id") {
					t.Errorf("%s(%q) error doesn't name the vault id: %v", builderName, id, err)
				}
			})
		}
	}
}

// TestNoPerVaultLocalStateIsKeyedByTheLocalName is the milestone's
// closing claim, asserted rather than asserted-in-prose: after E0 the
// local name is a label for humans, and nothing under $GAGE_DATA or
// $GAGE_STATE is addressed by it.
//
// It is a property of the three path builders together, which is why it
// is one test rather than three: the failure it rules out is one of them
// being left behind, which is exactly what happened to LockFilePath in
// an earlier draft of this milestone.
func TestNoPerVaultLocalStateIsKeyedByTheLocalName(t *testing.T) {
	first, second := vaultconfig.NewID(), vaultconfig.NewID()

	builders := map[string]func(string) (string, error){
		"IdentitiesDir": IdentitiesDir,
		"IdentityFilePath": func(id string) (string, error) {
			return IdentityFilePath(id, "laptop-1")
		},
		"TrustCacheDir": TrustCacheDir,
		"LockFilePath":  LockFilePath,
	}
	for name, build := range builders {
		a, err := build(first)
		if err != nil {
			t.Fatalf("%s(%q): %v", name, first, err)
		}
		b, err := build(second)
		if err != nil {
			t.Fatalf("%s(%q): %v", name, second, err)
		}
		if a == b {
			t.Errorf("%s gives two vaults the same path %q", name, a)
		}
		if !strings.Contains(a, first) {
			t.Errorf("%s(%q) = %q, which is not keyed by the id", name, first, a)
		}
	}
}
