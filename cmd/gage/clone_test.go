package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gittest"
	"github.com/denmark/gage/internal/gage/vaultconfig"
)

// publishedVault creates a vault in one XDG root, publishes it to a bare
// remote, and returns the remote — the "a vault already exists somewhere
// else" starting point clone needs. The caller then gets a *fresh* XDG
// root, so the clone happens as a genuinely different device with no
// identity of its own.
func publishedVault(t *testing.T, name string) string {
	t.Helper()

	isolateXDG(t)
	_, remote := initVaultWithRemote(t, name)
	if res := runCLI(t, []string{"insert", "ProtonMail"}, "hunter2\n"); res.Code != 0 {
		t.Fatalf("seeding the vault: %s", res.Stderr)
	}

	// A second, empty environment: this is the machine doing the cloning.
	isolateXDG(t)
	return remote
}

func TestCloneProducesARegisteredWorkingVault(t *testing.T) {
	remote := publishedVault(t, "personal")

	res := runCLI(t, []string{"clone", remote, "--name", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("clone exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	g := readGlobalConfigForTest(t)
	entry, ok := g.Vaults["personal"]
	if !ok {
		t.Fatal("clone did not register the vault in global config")
	}
	if entry.Type != "git" {
		t.Errorf("registered type = %q, want git", entry.Type)
	}
	if entry.Git.Origin != remote {
		t.Errorf("registered origin = %q, want %q", entry.Git.Origin, remote)
	}

	// The committed state is all there.
	for _, rel := range []string{".gage/config.toml", ".age-recipients", "entries", ".gitattributes"} {
		if _, err := os.Stat(filepath.Join(entry.Path, rel)); err != nil {
			t.Errorf("expected %s in the clone: %v", rel, err)
		}
	}
	files, err := os.ReadDir(filepath.Join(entry.Path, "entries"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("the clone has %d entries, want the 1 that was published", len(files))
	}
}

func TestCloneInfersTheNameFromTheURLAndNameOverridesIt(t *testing.T) {
	t.Run("inferred", func(t *testing.T) {
		remote := publishedVault(t, "personal")

		res := runCLI(t, []string{"clone", remote}, "")
		if res.Code != 0 {
			t.Fatalf("clone exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
		}
		// gittest's bare remotes are named remote.git, so the inferred
		// name is "remote".
		if _, ok := readGlobalConfigForTest(t).Vaults["remote"]; !ok {
			t.Errorf("clone did not infer the vault name from the URL; registered: %v",
				readGlobalConfigForTest(t).Vaults)
		}
	})

	t.Run("--name overrides", func(t *testing.T) {
		remote := publishedVault(t, "personal")

		if res := runCLI(t, []string{"clone", remote, "--name", "work"}, ""); res.Code != 0 {
			t.Fatalf("clone failed: %s", res.Stderr)
		}
		if _, ok := readGlobalConfigForTest(t).Vaults["work"]; !ok {
			t.Error("--name did not override the inferred vault name")
		}
	})
}

func TestCloneWithoutDirLandsUnderGageData(t *testing.T) {
	remote := publishedVault(t, "personal")

	if res := runCLI(t, []string{"clone", remote, "--name", "personal"}, ""); res.Code != 0 {
		t.Fatalf("clone failed: %s", res.Stderr)
	}

	want := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "vaults", "personal")
	if got := readGlobalConfigForTest(t).Vaults["personal"].Path; got != want {
		t.Errorf("clone landed at %q, want %q — the same default as init", got, want)
	}
}

// TestCloneSaysSoWhenThisDeviceCannotDecryptAnything is the "does NOT
// grant you access" rule. A fresh device is not a recipient, and the
// whole point is that this is stated plainly rather than discovered later
// as a vault that decrypts nothing.
func TestCloneSaysSoWhenThisDeviceCannotDecryptAnything(t *testing.T) {
	remote := publishedVault(t, "personal")

	res := runCLI(t, []string{"clone", remote, "--name", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("clone exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "cannot decrypt") {
		t.Errorf("clone said %q, want it to say this device can't read the vault yet", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "gage identity add") {
		t.Errorf("clone said %q, want it to point at `gage identity add`", res.Stdout)
	}
}

// TestCloneRefusesAnUnrecognizedFormatVersion covers the forward-compat
// refusal: registering a vault this build can't safely operate on would
// trade one clear failure for a confusing one on every later command.
func TestCloneRefusesAnUnrecognizedFormatVersion(t *testing.T) {
	remote := publishedVault(t, "personal")

	// A device from the future bumps the vault's format version.
	other := gittest.NewDevice(t, remote)
	raw := other.Read(t, ".gage/config.toml")
	bumped := strings.Replace(raw,
		fmt.Sprintf("format_version = %d", vaultconfig.CurrentFormatVersion),
		"format_version = 99", 1)
	if bumped == raw {
		t.Fatalf("could not bump format_version in:\n%s", raw)
	}
	other.WriteCommitPush(t, ".gage/config.toml", bumped, "a newer gage wrote this vault")

	res := runCLI(t, []string{"clone", remote, "--name", "personal"}, "")
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("clone exit code = %d, want %d (conflict); stderr=%s",
			res.Code, exitcode.Conflict, res.Stderr)
	}
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
		t.Error("clone registered a vault it refused to open")
	}
	// The partial clone is rolled back, so retrying after upgrading gage
	// isn't blocked by a leftover directory.
	path := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "vaults", "personal")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the refused clone left %s behind: %v", path, err)
	}
}

func TestCloneRefusesAnAlreadyRegisteredName(t *testing.T) {
	remote := publishedVault(t, "personal")

	if res := runCLI(t, []string{"clone", remote, "--name", "personal"}, ""); res.Code != 0 {
		t.Fatalf("first clone failed: %s", res.Stderr)
	}
	res := runCLI(t, []string{"clone", remote, "--name", "personal"}, "")
	if res.Code != int(exitcode.Conflict) {
		t.Errorf("re-cloning under a registered name exit code = %d, want %d", res.Code, exitcode.Conflict)
	}
}

// TestCloneIsOneShotOnly: like init, clone creates a vault rather than
// operating on one, which leaves "is it now the session's vault?"
// unanswered — so it reports that plainly rather than running.
func TestCloneIsOneShotOnly(t *testing.T) {
	ci, ok := findCommand("clone")
	if !ok {
		t.Fatal("clone is not registered")
	}
	if ci.Availability.SessionVisible() {
		t.Error("clone is listed as available in a session, want one-shot only")
	}
}

func TestVaultNameFromURLHandlesEverySpelling(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/me/work-vault.git", "work-vault"},
		{"https://github.com/me/work-vault", "work-vault"},
		{"git@github.com:me/work-vault.git", "work-vault"},
		{"ssh://git@github.com/me/work-vault.git", "work-vault"},
		{"/tmp/somewhere/remote.git", "remote"},
		{"https://github.com/me/work-vault.git/", "work-vault"},
	}

	for _, tc := range tests {
		got, err := vaultNameFromURL(tc.url)
		if err != nil {
			t.Errorf("vaultNameFromURL(%s): %v", tc.url, err)
			continue
		}
		if got != tc.want {
			t.Errorf("vaultNameFromURL(%s) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// TestCloneRefusesANameThatEscapesTheVaultsDirectory covers the traversal
// case that only clone has: the vault name can be *inferred from a remote
// URL*, so it is attacker-influenced on its way to becoming a directory
// under $GAGE_DATA/vaults.
func TestCloneRefusesANameThatEscapesTheVaultsDirectory(t *testing.T) {
	for _, name := range []string{"..", "../evil", "a/b", ""} {
		if err := checkVaultName(name); err == nil {
			t.Errorf("checkVaultName(%q) was accepted, want it refused", name)
		}
	}
	if err := checkVaultName("personal"); err != nil {
		t.Errorf("checkVaultName(\"personal\") = %v, want nil", err)
	}
}

// TestCurrentFormatVersionIsWhatCloneChecks keeps the format-version test
// above honest if the current version ever moves.
func TestCurrentFormatVersionIsWhatCloneChecks(t *testing.T) {
	if vaultconfig.CurrentFormatVersion == 99 {
		t.Fatal("the format version the clone test bumps to is now the real one; pick another")
	}
}
