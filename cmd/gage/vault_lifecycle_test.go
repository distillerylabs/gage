package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/vaultconfig"
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

	// Both halves of the bullet: the vault is *created* there, and
	// registered under exactly that path.
	if _, err := os.Stat(filepath.Join(want, ".gage", "config.toml")); err != nil {
		t.Errorf("expected the vault to exist at %s: %v", want, err)
	}
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

// readVaultConfigForTest parses a registered vault's own
// .gage/config.toml — the committed file, not the global registry. Most
// of the "what did init actually write" bullets are about this file
// specifically, so asserting against the global config instead would
// check the wrong artifact.
func readVaultConfigForTest(t *testing.T, name string) vaultconfig.File {
	t.Helper()
	g := readGlobalConfigForTest(t)
	entry, ok := g.Vaults[name]
	if !ok {
		t.Fatalf("vault %q is not registered", name)
	}
	f, err := vaultconfig.Read(filepath.Join(entry.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestInitDefaultsLandInVaultConfigAndGlobalConfig(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	// The vault's own committed config: no --type means type = "git",
	// no --method means [method].default = "passphrase", and
	// format_version is stamped.
	f := readVaultConfigForTest(t, "personal")
	if f.Vault.Type != "git" {
		t.Errorf(".gage/config.toml [vault].type = %q, want %q", f.Vault.Type, "git")
	}
	if f.Method.Default != "passphrase" {
		t.Errorf(".gage/config.toml [method].default = %q, want %q", f.Method.Default, "passphrase")
	}
	if f.Vault.FormatVersion != vaultconfig.CurrentFormatVersion {
		t.Errorf(".gage/config.toml format_version = %d, want %d", f.Vault.FormatVersion, vaultconfig.CurrentFormatVersion)
	}
	if f.Vault.Name != "personal" {
		t.Errorf(".gage/config.toml [vault].name = %q, want %q", f.Vault.Name, "personal")
	}

	// And the global registry's own copy of type/method.
	g := readGlobalConfigForTest(t)
	if g.Vaults["personal"].Type != "git" {
		t.Errorf("global config Type = %q, want %q", g.Vaults["personal"].Type, "git")
	}
	if g.Vaults["personal"].Method != "passphrase" {
		t.Errorf("global config Method = %q, want %q", g.Vaults["personal"].Method, "passphrase")
	}
}

// TestInitExplicitTypeAndMethodEquivalentToDefault asserts the actual
// claim in the plan — that passing the single accepted value is
// *equivalent to* omitting the flag — rather than merely that passing it
// exits 0, which would still hold if the flag were silently ignored or
// wrote something different.
func TestInitExplicitTypeAndMethodEquivalentToDefault(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "implicit", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init (flags omitted) failed: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"init", "explicit", "--type", "git", "--method", "passphrase", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init (flags given) failed: %s", res.Stderr)
	}

	implicit := readVaultConfigForTest(t, "implicit")
	explicit := readVaultConfigForTest(t, "explicit")

	// Everything but the vault's own name must match.
	implicit.Vault.Name = ""
	explicit.Vault.Name = ""
	if !reflect.DeepEqual(implicit, explicit) {
		t.Errorf("explicit flags produced a different vault config than omitting them:\nomitted: %+v\ngiven:   %+v", implicit, explicit)
	}

	implicitEntry := readGlobalConfigForTest(t).Vaults["implicit"]
	explicitEntry := readGlobalConfigForTest(t).Vaults["explicit"]
	if implicitEntry.Type != explicitEntry.Type || implicitEntry.Method != explicitEntry.Method {
		t.Errorf("global config differs: omitted=%+v given=%+v", implicitEntry, explicitEntry)
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

// TestInitUnknownMethodFailsWithUsage covers the plan's method bullet in
// full: the future-work method names named in the design doc must fail
// like any other unknown value (never be silently accepted and written
// to config), the error must name the one accepted value, and nothing
// may be created on disk.
func TestInitUnknownMethodFailsWithUsage(t *testing.T) {
	isolateXDG(t)
	for _, m := range []string{"ssh", "yubikey", "age-key", "secure-enclave", "plugin:foo", "bogus"} {
		t.Run(m, func(t *testing.T) {
			name := "personal-" + strings.ReplaceAll(m, ":", "-")
			res := runCLI(t, []string{"init", name, "--method", m, "--recipient", testRecipient1}, "")

			if res.Code != int(exitcode.Usage) {
				t.Errorf("exit code = %d, want %d (Usage) for method %q", res.Code, exitcode.Usage, m)
			}
			if !strings.Contains(res.Stderr, "passphrase") {
				t.Errorf("stderr doesn't list the accepted value %q: %q", "passphrase", res.Stderr)
			}
			if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "vaults", name)); !os.IsNotExist(err) {
				t.Errorf("init created files for rejected method %q", m)
			}
			if _, ok := readGlobalConfigForTest(t).Vaults[name]; ok {
				t.Errorf("init registered a vault for rejected method %q", m)
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

// TestVaultInfoReportsDirtyWorkingTree exercises the other side of the
// clean/dirty branch. Without it, only the "clean" rendering is ever
// produced, so the dirty path could be broken (or the two labels
// swapped in a way a fresh vault can't reveal) and stay green.
func TestVaultInfoReportsDirtyWorkingTree(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	path := readGlobalConfigForTest(t).Vaults["personal"].Path

	if err := os.WriteFile(filepath.Join(path, "entries", "stray.age"), []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"vault", "info", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("vault info failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "status: dirty") {
		t.Errorf("vault info didn't report a dirty tree:\n%s", res.Stdout)
	}
}

// rewriteVaultConfigLine edits one line of a vault's committed
// .gage/config.toml in place, standing in for what any git-writer can do
// to that file (see "Trust boundaries" and Q-DEVICE-NAME).
func rewriteVaultConfigLine(t *testing.T, vaultPath, prefix, replacement string) {
	t.Helper()
	configPath := filepath.Join(vaultPath, ".gage", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			lines[i] = replacement
			replaced = true
			break
		}
	}
	if !replaced {
		t.Fatalf("no line starting with %q in %s:\n%s", prefix, configPath, data)
	}
	if err := os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTamperedFormatVersionRefusedByRealCommand is the end-to-end half of
// the format_version bullet: the library refusing on Read is only
// meaningful if a real command actually goes through that path rather
// than parsing the file some other way.
func TestTamperedFormatVersionRefusedByRealCommand(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	path := readGlobalConfigForTest(t).Vaults["personal"].Path
	rewriteVaultConfigLine(t, path, "format_version", "format_version = 99")

	res := runCLI(t, []string{"vault", "info", "personal"}, "")
	if res.Code == 0 {
		t.Fatalf("vault info accepted an unrecognized format_version:\n%s", res.Stdout)
	}
	if res.Code != int(exitcode.Conflict) {
		t.Errorf("exit code = %d, want %d (Conflict) — an unrecognized format_version is an anticipated state, not an internal error", res.Code, exitcode.Conflict)
	}
	if !strings.Contains(res.Stderr, "upgrade gage") {
		t.Errorf("error doesn't tell the operator to upgrade gage: %q", res.Stderr)
	}
	// Nothing may be reported from a file this gage doesn't understand.
	if strings.Contains(res.Stdout, "type:") || strings.Contains(res.Stdout, "recipients:") {
		t.Errorf("vault info rendered fields from a config it refused: %q", res.Stdout)
	}
}

// TestTamperedDeviceNameRefusedByRealCommand is the same end-to-end check
// for the security-relevant half: .gage/config.toml is committed and any
// git-writer can edit it, so a device name that isn't a safe path
// component has to be refused by the command that reads it, not just by
// a unit test of the parser (Q-DEVICE-NAME).
func TestTamperedDeviceNameRefusedByRealCommand(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	path := readGlobalConfigForTest(t).Vaults["personal"].Path

	for _, evil := range []string{"../../../etc/cron.d/x", "/etc/passwd", `..\..\windows\x`} {
		t.Run(evil, func(t *testing.T) {
			rewriteVaultConfigLine(t, path, "device", "device = '"+evil+"'")

			res := runCLI(t, []string{"vault", "info", "personal"}, "")
			if res.Code == 0 {
				t.Fatalf("vault info accepted device name %q:\n%s", evil, res.Stdout)
			}
			if res.Code != int(exitcode.Conflict) {
				t.Errorf("exit code = %d, want %d (Conflict)", res.Code, exitcode.Conflict)
			}
			if !strings.Contains(res.Stderr, "device name") {
				t.Errorf("error doesn't explain the device name was rejected: %q", res.Stderr)
			}
			// M1 writes no identity files at all (that's M2), so the
			// assertion available here is that the rejection happens
			// before anything is created under identities/ — the
			// directory a resolved traversal would have escaped from.
			if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")); !os.IsNotExist(err) {
				t.Errorf("something was created under identities/ while handling device name %q", evil)
			}
		})
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
