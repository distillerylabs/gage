package gage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/xdgpaths"
)

func TestIdentityFilePathShape(t *testing.T) {
	dataDir, err := xdgpaths.DataDir()
	if err != nil {
		t.Fatal(err)
	}

	got, err := IdentityFilePath("personal", "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataDir, "identities", "personal", "laptop-1.age")
	if got != want {
		t.Errorf("IdentityFilePath = %q, want %q", got, want)
	}
}

func TestIdentityFilePathIsSiblingOfVaultsDir(t *testing.T) {
	identityPath, err := IdentityFilePath("personal", "laptop-1")
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
		t.Errorf("identity path %q is not under <GAGE_DATA>/identities/<vault>/", identityPath)
	}
}
