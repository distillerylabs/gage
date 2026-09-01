package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

const (
	testRecipient1 = "age1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0savhh7m"
	testRecipient2 = "age1yubikey1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0s9hkmc0"
)

// isolateXDG points every XDG root at a fresh temp directory for the
// duration of one test, so CLI-level vault lifecycle tests never touch
// the real machine's config/data.
func isolateXDG(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
}

func readGlobalConfigForTest(t *testing.T) config.Global {
	t.Helper()
	g, err := readGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestInitCreatesFullSkeletonAndRegisters(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, "")
	if res.Code != 0 {
		t.Fatalf("init exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	g := readGlobalConfigForTest(t)
	entry, ok := g.Vaults["personal"]
	if !ok {
		t.Fatal("vault \"personal\" not registered in global config")
	}

	for _, rel := range []string{".gage/config.toml", ".age-recipients", "entries", ".gitignore", ".gitattributes", ".git"} {
		if _, err := os.Stat(filepath.Join(entry.Path, rel)); err != nil {
			t.Errorf("expected %s to exist: %v", rel, err)
		}
	}
	count, err := gitrepo.CommitCount(entry.Path)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("commit count = %d, want 1", count)
	}
}

func TestInitWithoutDirUsesGageDataVaultsDir(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	dataDir := os.Getenv("XDG_DATA_HOME")
	want := filepath.Join(dataDir, "gage", "vaults", "personal")

	g := readGlobalConfigForTest(t)
	if g.Vaults["personal"].Path != want {
		t.Errorf("registered path = %q, want %q", g.Vaults["personal"].Path, want)
	}
}

func TestInitWithDirUsesGivenPath(t *testing.T) {
	isolateXDG(t)
	dir := filepath.Join(t.TempDir(), "somewhere-else")

	res := runCLI(t, []string{"init", "personal", "--dir", dir, "--recipient", testRecipient1}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	g := readGlobalConfigForTest(t)
	if g.Vaults["personal"].Path != dir {
		t.Errorf("registered path = %q, want %q", g.Vaults["personal"].Path, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("expected vault to exist at --dir: %v", err)
	}
}

func TestInitIntoNonEmptyNonGageDirectoryFailsWithoutTouchingFiles(t *testing.T) {
	isolateXDG(t)
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"init", "personal", "--dir", dir, "--recipient", testRecipient1}, "")
	if res.Code == 0 {
		t.Fatal("expected init into a non-empty directory to fail")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Fatalf("directory contents changed: %v", entries)
	}

	g := readGlobalConfigForTest(t)
	if _, ok := g.Vaults["personal"]; ok {
		t.Error("vault should not have been registered")
	}
}

func TestInitIntoAlreadyRegisteredNameFailsAndLeavesExistingVaultUntouched(t *testing.T) {
	isolateXDG(t)

	first := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, "")
	if first.Code != 0 {
		t.Fatalf("first init failed: %s", first.Stderr)
	}
	before := readGlobalConfigForTest(t)
	beforeEntry := before.Vaults["personal"]

	second := runCLI(t, []string{"init", "personal", "--recipient", testRecipient2}, "")
	if second.Code == 0 {
		t.Fatal("expected re-init of an already-registered name to fail")
	}

	after := readGlobalConfigForTest(t)
	if after.Vaults["personal"] != beforeEntry {
		t.Errorf("registration changed: before=%+v after=%+v", beforeEntry, after.Vaults["personal"])
	}
	// The original vault's own recipient must be untouched too.
	data, err := os.ReadFile(filepath.Join(beforeEntry.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), testRecipient1) || strings.Contains(string(data), testRecipient2) {
		t.Errorf(".age-recipients changed by the failed re-init: %q", data)
	}
}

func TestInitFormatVersionAndTypeDefaults(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	g := readGlobalConfigForTest(t)
	if g.Vaults["personal"].Type != "git" {
		t.Errorf("Type = %q, want %q", g.Vaults["personal"].Type, "git")
	}
	if g.Vaults["personal"].Method != "passphrase" {
		t.Errorf("Method = %q, want %q", g.Vaults["personal"].Method, "passphrase")
	}
}

func TestInitExplicitTypeAndMethodEquivalentToDefault(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal", "--type", "git", "--method", "passphrase", "--recipient", testRecipient1}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
}

func TestInitUnknownTypeFailsWithUsageAndListsGit(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal", "--type", "s3", "--recipient", testRecipient1}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if !strings.Contains(res.Stderr, "git") {
		t.Errorf("stderr doesn't mention the accepted value %q: %q", "git", res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "vaults", "personal")); !os.IsNotExist(err) {
		t.Error("init should not have created any files for an unknown type")
	}
}

func TestInitUnknownMethodFailsWithUsage(t *testing.T) {
	isolateXDG(t)
	for _, m := range []string{"ssh", "yubikey", "age-key", "secure-enclave", "plugin:foo"} {
		t.Run(m, func(t *testing.T) {
			res := runCLI(t, []string{"init", "personal-" + m, "--method", m, "--recipient", testRecipient1}, "")
			if res.Code != int(exitcode.Usage) {
				t.Errorf("exit code = %d, want %d (Usage) for method %q", res.Code, exitcode.Usage, m)
			}
		})
	}
}

func TestInitNoRecipientFailsClearly(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if !strings.Contains(res.Stderr, "recipient") {
		t.Errorf("stderr doesn't name the missing flag: %q", res.Stderr)
	}
}

func TestInitRepeatedRecipientWritesAll(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--recipient", testRecipient2}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	g := readGlobalConfigForTest(t)
	data, err := os.ReadFile(filepath.Join(g.Vaults["personal"].Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{testRecipient1, testRecipient2} {
		if !strings.Contains(string(data), want) {
			t.Errorf(".age-recipients missing %q:\n%s", want, data)
		}
	}
}

func TestInitMalformedRecipientFailsBeforeCreatingFiles(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal", "--recipient", "not-a-valid-key"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "vaults", "personal")); !os.IsNotExist(err) {
		t.Error("init should not have created any files for a malformed recipient")
	}
}

func TestInitDeviceFlagOverridesHostnameDefault(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal", "--device", "custom-device", "--recipient", testRecipient1}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	g := readGlobalConfigForTest(t)
	if g.Vaults["personal"].Device != "custom-device" {
		t.Errorf("global config Device = %q, want %q", g.Vaults["personal"].Device, "custom-device")
	}
}

func TestInitDeviceFlagFailingAllowlistRejectedBeforeCreatingFiles(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal", "--device", "../../../etc/x", "--recipient", testRecipient1}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "vaults", "personal")); !os.IsNotExist(err) {
		t.Error("init should not have created any files for an invalid --device")
	}
}

func TestVaultListIncludesFreshlyInitedVault(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"vault", "list"}, "")
	if res.Code != 0 {
		t.Fatalf("vault list failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "personal") {
		t.Errorf("vault list output doesn't mention \"personal\": %q", res.Stdout)
	}
}

func TestVaultInfoByNameReportsExpectedFields(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--recipient", testRecipient2}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"vault", "info", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("vault info failed: %s", res.Stderr)
	}
	for _, want := range []string{"type: git", "method: passphrase", "recipients: 2", "remote: (none)", "status: clean"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("vault info output missing %q:\n%s", want, res.Stdout)
		}
	}
}

func TestVaultInfoNoArgumentReportsOnCurrent(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"vault", "info"}, "")
	if res.Code != 0 {
		t.Fatalf("vault info (no arg) failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "name: personal") {
		t.Errorf("vault info (no arg) didn't report on current vault: %q", res.Stdout)
	}
}

func TestVaultRemoveDropsRegistrationButLeavesFiles(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	path := readGlobalConfigForTest(t).Vaults["personal"].Path

	res := runCLI(t, []string{"vault", "remove", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}

	g := readGlobalConfigForTest(t)
	if _, ok := g.Vaults["personal"]; ok {
		t.Error("vault still registered after vault remove")
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Errorf("underlying git repo should still exist: %v", err)
	}
}

func TestVaultSetDefaultUpdatesCurrent(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init personal failed: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"init", "work", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init work failed: %s", res.Stderr)
	}
	// personal became current automatically as the first vault.
	if g := readGlobalConfigForTest(t); g.Current != "personal" {
		t.Fatalf("Current = %q, want %q before set-default", g.Current, "personal")
	}

	res := runCLI(t, []string{"vault", "set-default", "work"}, "")
	if res.Code != 0 {
		t.Fatalf("vault set-default failed: %s", res.Stderr)
	}
	if g := readGlobalConfigForTest(t); g.Current != "work" {
		t.Errorf("Current = %q, want %q", g.Current, "work")
	}
}

func TestInitWithoutRemoteLeavesVaultRemoteless(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	g := readGlobalConfigForTest(t)
	entry := g.Vaults["personal"]
	if entry.Git.Origin != "" {
		t.Errorf("Git.Origin = %q, want empty", entry.Git.Origin)
	}
	url, err := gitrepo.RemoteURL(entry.Path)
	if err != nil {
		t.Fatal(err)
	}
	if url != "" {
		t.Errorf("git remote = %q, want none", url)
	}

	path, err := globalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "[vaults.personal.git]") {
		t.Errorf("global config has a [vaults.personal.git] table with no remote given:\n%s", data)
	}
}

func TestGitSetRemoteSetsOriginAndGlobalConfigTogether(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	const url = "https://example.invalid/personal-vault.git"
	res := runCLI(t, []string{"git", "set-remote", "personal", url}, "")
	if res.Code != 0 {
		t.Fatalf("git set-remote failed: %s", res.Stderr)
	}

	g := readGlobalConfigForTest(t)
	if g.Vaults["personal"].Git.Origin != url {
		t.Errorf("global config Git.Origin = %q, want %q", g.Vaults["personal"].Git.Origin, url)
	}
	got, err := gitrepo.RemoteURL(g.Vaults["personal"].Path)
	if err != nil {
		t.Fatal(err)
	}
	if got != url {
		t.Errorf("git remote origin = %q, want %q", got, url)
	}
}

func TestGitSetRemoteChangesExistingRemoteAndVaultInfoReflectsIt(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--remote", "https://example.invalid/old.git"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	const newURL = "https://example.invalid/new.git"
	if res := runCLI(t, []string{"git", "set-remote", "personal", newURL}, ""); res.Code != 0 {
		t.Fatalf("git set-remote failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"vault", "info", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("vault info failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, newURL) {
		t.Errorf("vault info doesn't reflect the new remote: %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "old.git") {
		t.Errorf("vault info still shows the old remote: %q", res.Stdout)
	}
}

func TestGitSetRemoteMissingArgsFailsWithUsageAndNoChanges(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	before := readGlobalConfigForTest(t).Vaults["personal"]

	for _, args := range [][]string{
		{"git", "set-remote"},
		{"git", "set-remote", "personal"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			res := runCLI(t, args, "")
			if res.Code != int(exitcode.Usage) {
				t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
			}
		})
	}

	after := readGlobalConfigForTest(t).Vaults["personal"]
	if after != before {
		t.Errorf("global config changed: before=%+v after=%+v", before, after)
	}
	url, err := gitrepo.RemoteURL(before.Path)
	if err != nil {
		t.Fatal(err)
	}
	if url != "" {
		t.Errorf("git remote = %q, want none (set-remote should not have run)", url)
	}
}
