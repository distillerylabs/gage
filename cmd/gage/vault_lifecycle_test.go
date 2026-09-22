package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/agekey"
	"github.com/distillerylabs/gage/internal/gage/config"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
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

// vaultIDForTest is the id global config recorded for a registered
// vault. Every per-vault local path is keyed by it since A20, so tests
// that want to look at an identity file, a trust cache or a lock file
// have to ask for it rather than spelling the vault's name.
func vaultIDForTest(t *testing.T, name string) string {
	t.Helper()
	entry, ok := readGlobalConfigForTest(t).Vaults[name]
	if !ok {
		t.Fatalf("vault %q is not registered", name)
	}
	if entry.ID == "" {
		t.Fatalf("vault %q is registered with no id", name)
	}
	return entry.ID
}

// identityFileForTest is where a registered vault's wrapped identity for
// one device lives.
func identityFileForTest(t *testing.T, name, device string) string {
	t.Helper()
	path, err := gage.IdentityFilePath(vaultIDForTest(t, name), device)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInitCreatesFullSkeletonAndRegisters(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, "")
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

	res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, "")
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

	res := runCLI(t, []string{"init", "personal", "--dir", dir, "--recipient", testRecipient1, "--no-recovery-key"}, "")
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

	res := runCLI(t, []string{"init", "personal", "--dir", dir, "--recipient", testRecipient1, "--no-recovery-key"}, "")
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

	first := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, "")
	if first.Code != 0 {
		t.Fatalf("first init failed: %s", first.Stderr)
	}
	before := readGlobalConfigForTest(t)
	beforeEntry := before.Vaults["personal"]

	second := runCLI(t, []string{"init", "personal", "--recipient", testRecipient2, "--no-recovery-key"}, "")
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
	res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, "")
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

	if res := runCLI(t, []string{"init", "implicit", "--recipient", testRecipient1, "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init (flags omitted) failed: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"init", "explicit", "--type", "git", "--method", "passphrase", "--recipient", testRecipient1, "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init (flags given) failed: %s", res.Stderr)
	}

	implicit := readVaultConfigForTest(t, "implicit")
	explicit := readVaultConfigForTest(t, "explicit")

	// Everything but the vault's own name and id — and this device's own
	// generated public key, which is freshly random per init — must
	// match. The device key is checked for shape rather than value, and
	// the ids are checked for being *different*, since a fresh one per
	// init is the whole of A20.
	if implicit.Vault.ID == explicit.Vault.ID {
		t.Errorf("both inits produced the same vault id %q; every vault gets its own", implicit.Vault.ID)
	}
	for _, f := range []*vaultconfig.File{&implicit, &explicit} {
		if !vaultconfig.ValidID(f.Vault.ID) {
			t.Errorf("init wrote a vault id that isn't a valid id: %q", f.Vault.ID)
		}
	}
	implicit.Vault.Name, explicit.Vault.Name = "", ""
	implicit.Vault.ID, explicit.Vault.ID = "", ""
	for _, f := range []*vaultconfig.File{&implicit, &explicit} {
		if len(f.Recipients) == 0 {
			t.Fatal("no recipients written")
		}
		if err := agekey.ValidateRecipient(f.Recipients[0].Pubkey); err != nil {
			t.Errorf("this device's generated key is not a valid recipient: %v", err)
		}
		f.Recipients[0].Pubkey = "<this device's generated key>"
	}
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
	res := runCLI(t, []string{"init", "personal", "--type", "s3", "--recipient", testRecipient1, "--no-recovery-key"}, "")
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
			res := runCLI(t, []string{"init", name, "--method", m, "--recipient", testRecipient1, "--no-recovery-key"}, "")

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

// TestInitWithNoRecipientGeneratesThisDevicesIdentity replaces M1's
// "init fails without --recipient": M2 is the milestone that gives init
// an identity to generate the first key from, so the flag becomes
// optional and purely additive.
func TestInitWithNoRecipientGeneratesThisDevicesIdentity(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"init", "personal", "--no-recovery-key"}, "")
	if res.Code != 0 {
		t.Fatalf("init without --recipient failed: %s", res.Stderr)
	}

	entry := readGlobalConfigForTest(t).Vaults["personal"]
	vf := readVaultConfigForTest(t, "personal")
	if len(vf.Recipients) != 1 {
		t.Fatalf("recipients = %d, want exactly this device's own key", len(vf.Recipients))
	}
	pubkey := vf.Recipients[0].Pubkey
	if err := agekey.ValidateRecipient(pubkey); err != nil {
		t.Errorf("generated key %q is not a valid recipient: %v", pubkey, err)
	}
	if vf.Recipients[0].Device != entry.Device {
		t.Errorf("recipient label = %q, want this device's name %q", vf.Recipients[0].Device, entry.Device)
	}

	// The same key, in both recipient-defining files.
	data, err := os.ReadFile(filepath.Join(entry.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != pubkey {
		t.Errorf(".age-recipients = %q, want exactly the key in config.toml (%q)", data, pubkey)
	}
}

// TestInitWritesTheWrappedIdentityFile is the storage half: the wrapped
// private key lands under $GAGE_DATA/identities/<vault-id>/<device>.age,
// outside the vault's own git tree, with tight permissions.
func TestInitWritesTheWrappedIdentityFile(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal", "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	entry := readGlobalConfigForTest(t).Vaults["personal"]

	dir := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities", vaultIDForTest(t, "personal"))
	path := filepath.Join(dir, entry.Device+".age")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected a wrapped identity at %s: %v", path, err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("identity file mode = %04o, want 0600", got)
		}
		if got := di.Mode().Perm(); got != 0o700 {
			t.Errorf("identities directory mode = %04o, want 0700", got)
		}
	}

	// It is not inside the vault, and therefore not in the git tree that
	// syncs to a remote.
	if strings.HasPrefix(path, entry.Path) {
		t.Errorf("the identity file %q is inside the vault %q", path, entry.Path)
	}
}

// TestInitExtraRecipientFollowsTheDeviceKey pins the order: this
// device's own key first (so gage.Create labels it with the device
// name), then each --recipient in the order given.
func TestInitExtraRecipientFollowsTheDeviceKey(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	entry := readGlobalConfigForTest(t).Vaults["personal"]
	vf := readVaultConfigForTest(t, "personal")
	if len(vf.Recipients) != 2 {
		t.Fatalf("recipients = %d, want 2", len(vf.Recipients))
	}
	if vf.Recipients[0].Device != entry.Device {
		t.Errorf("first recipient is labeled %q, want this device (%q)", vf.Recipients[0].Device, entry.Device)
	}
	if vf.Recipients[1].Pubkey != testRecipient1 {
		t.Errorf("second recipient = %q, want the --recipient key %q", vf.Recipients[1].Pubkey, testRecipient1)
	}

	data, err := os.ReadFile(filepath.Join(entry.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(data))
	want := []string{vf.Recipients[0].Pubkey, testRecipient1}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf(".age-recipients = %v, want %v — same keys, same order as config.toml", lines, want)
	}
}

// TestInitRecordsThisDevicesDeviceAndMethod: Unlock resolves the identity
// file from the recorded device name, and dispatches on the recorded
// method (Q-DEVICE-NAME, Q-METHOD-SCOPE). Both must actually be written.
func TestInitRecordsThisDevicesDeviceAndMethod(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal", "--device", "laptop-1", "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	entry := readGlobalConfigForTest(t).Vaults["personal"]
	if entry.Device != "laptop-1" {
		t.Errorf("recorded device = %q, want laptop-1", entry.Device)
	}
	if entry.Method != "passphrase" {
		t.Errorf("recorded method = %q, want passphrase", entry.Method)
	}
}

// TestInitDeviceFlagAgreesEverywhere is the M2 bullet spelling out that
// --device NAME reaches all three places that must agree: the identity
// file's path, the [[recipients]] label, and the global config record.
func TestInitDeviceFlagAgreesEverywhere(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal", "--device", "workstation-7", "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	entry := readGlobalConfigForTest(t).Vaults["personal"]
	if entry.Device != "workstation-7" {
		t.Errorf("global config device = %q, want workstation-7", entry.Device)
	}
	vf := readVaultConfigForTest(t, "personal")
	if vf.Recipients[0].Device != "workstation-7" {
		t.Errorf("[[recipients]] label = %q, want workstation-7", vf.Recipients[0].Device)
	}
	path := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities", vaultIDForTest(t, "personal"), "workstation-7.age")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected the identity file at %s: %v", path, err)
	}
}

func TestInitRepeatedRecipientWritesAll(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--recipient", testRecipient2, "--no-recovery-key"}, "")
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
	res := runCLI(t, []string{"init", "personal", "--recipient", "not-a-valid-key", "--no-recovery-key"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "vaults", "personal")); !os.IsNotExist(err) {
		t.Error("init should not have created any files for a malformed recipient")
	}
}

func TestInitDeviceFlagOverridesHostnameDefault(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"init", "personal", "--device", "custom-device", "--recipient", testRecipient1, "--no-recovery-key"}, "")
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
	res := runCLI(t, []string{"init", "personal", "--device", "../../../etc/x", "--recipient", testRecipient1, "--no-recovery-key"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "vaults", "personal")); !os.IsNotExist(err) {
		t.Error("init should not have created any files for an invalid --device")
	}
}

func TestVaultListIncludesFreshlyInitedVault(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, ""); res.Code != 0 {
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
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--recipient", testRecipient2, "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"vault", "info", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("vault info failed: %s", res.Stderr)
	}
	// Three: this device's own generated key plus the two --recipient
	// keys, which are additive to it rather than replacing it.
	for _, want := range []string{"type: git", "method: passphrase", "recipients: 3", "remote: (none)", "status: clean"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("vault info output missing %q:\n%s", want, res.Stdout)
		}
	}
}

func TestVaultInfoNoArgumentReportsOnCurrent(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, ""); res.Code != 0 {
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
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, ""); res.Code != 0 {
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
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, ""); res.Code != 0 {
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
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	path := readGlobalConfigForTest(t).Vaults["personal"].Path
	before := identityTree(t)

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
			// init legitimately wrote one identity file for this
			// device; what must not happen is anything *else* appearing,
			// under identities/ or anywhere a traversal would have
			// escaped to.
			if got := identityTree(t); !reflect.DeepEqual(got, before) {
				t.Errorf("handling device name %q changed the data directory:\nbefore: %v\nafter:  %v", evil, before, got)
			}
		})
	}
}

// identityTree lists every path under $GAGE_DATA, so a test can assert
// that handling a hostile device name created, moved, or removed nothing.
func identityTree(t *testing.T) []string {
	t.Helper()
	root := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage")
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		found = append(found, rel)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(found)
	return found
}

func TestVaultRemoveDropsRegistrationButLeavesFiles(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, ""); res.Code != 0 {
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

// TestVaultRemoveDeletesAnOrphanedIdentityFile is issue #39: once this
// device is no longer among the vault's own recipients, its local
// identity file has nothing left it's needed for, so `vault remove`
// prunes it rather than leaving a stale file that blocks a later `gage
// init`/`identity add` reusing this vault name.
func TestVaultRemoveDeletesAnOrphanedIdentityFile(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")
	key := newRecipientKey(t)
	if res := runCLI(t, []string{"recipient", "add", key, "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("recipient add: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	// Drop laptop-1's own recipiency, leaving phone-1 as the vault's sole
	// recipient — laptop-1's identity file is now provably orphaned.
	if res := runCLI(t, []string{"recipient", "remove", "laptop-1", "--reencrypt"}, ""); res.Code != 0 {
		t.Fatalf("recipient remove: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	idPath, err := gage.IdentityFilePath(vaultIDForTest(t, "personal"), "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(idPath); err != nil {
		t.Fatalf("identity file should exist before vault remove: %v", err)
	}

	res := runCLI(t, []string{"vault", "remove", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if _, err := os.Stat(idPath); !os.IsNotExist(err) {
		t.Errorf("identity file still present after vault remove: err=%v", err)
	}
}

// TestVaultRemoveKeepsIdentityFileStillListedAsRecipient is the safety
// half of #39: if the vault's recipient list still names this device,
// deleting its identity file would destroy the only copy of the private
// key it needs to read that vault's ciphertext, so `vault remove` leaves
// it and explains why on stderr instead.
func TestVaultRemoveKeepsIdentityFileStillListedAsRecipient(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1", "--recipient", testRecipient1)

	idPath, err := gage.IdentityFilePath(vaultIDForTest(t, "personal"), "laptop-1")
	if err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"vault", "remove", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("identity file should have been kept: %v", err)
	}
	if !strings.Contains(res.Stderr, "still listed as a recipient") {
		t.Errorf("stderr = %q, want a warning that the device is still a recipient", res.Stderr)
	}
}

// TestVaultRemoveKeepsIdentityFileWhenRecipientsUnreadable covers the
// other unsafe case: the vault's own directory is gone (or otherwise
// unreadable), so `vault remove` cannot confirm the device has been
// dropped as a recipient. Guessing wrong here is irreversible, so it
// keeps the identity file and says why rather than assuming it's safe.
func TestVaultRemoveKeepsIdentityFileWhenRecipientsUnreadable(t *testing.T) {
	isolateXDG(t)
	path := initVaultForTest(t, "personal", "--device", "laptop-1")

	idPath, err := gage.IdentityFilePath(vaultIDForTest(t, "personal"), "laptop-1")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"vault", "remove", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("identity file should have been kept: %v", err)
	}
	if !strings.Contains(res.Stderr, "could not confirm") {
		t.Errorf("stderr = %q, want a warning that recipients could not be checked", res.Stderr)
	}
}

// TestReinitAfterVaultRemoveIsAnOrdinaryRetry is #39's exact scenario,
// re-stated for A20. `vault remove` on a vault whose store is gone keeps
// the identity file (it can't confirm deleting it is safe), and
// re-running `gage init` under the same name must not dead-end on it.
//
// The mechanism changed and the outcome did not. #39 fixed this by
// having CreateIdentity reuse the leftover file; A20 makes the leftover
// unreachable instead, since the new init mints a new id and files its
// key under that. Either way the retry is an ordinary retry — and the
// id-keyed version is the better one, because the new vault is not the
// old vault and has no business inheriting its keypair.
func TestReinitAfterVaultRemoveIsAnOrdinaryRetry(t *testing.T) {
	isolateXDG(t)
	path := initVaultForTest(t, "vault-test-newinit", "--device", "laptop-1")

	idPath := identityFileForTest(t, "vault-test-newinit", "laptop-1")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if res := runCLI(t, []string{"vault", "remove", "vault-test-newinit"}, ""); res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if _, err := os.Stat(idPath); err != nil {
		t.Fatalf("identity file should have survived vault remove: %v", err)
	}

	res := runCLI(t, []string{"init", "vault-test-newinit", "--device", "laptop-1", "--no-recovery-key"}, "")
	if res.Code != 0 {
		t.Fatalf("re-init after a vault remove should be an ordinary retry, not a failure: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	g := readGlobalConfigForTest(t)
	entry, ok := g.Vaults["vault-test-newinit"]
	if !ok {
		t.Fatal("vault-test-newinit should be registered again after re-init")
	}
	// A new vault, with its own id and its own key. The leftover file is
	// still on disk under the old vault's id, untouched and unrelated.
	newPath, err := gage.IdentityFilePath(entry.ID, "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	if newPath == idPath {
		t.Fatal("the re-init filed its identity under the removed vault's id")
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("the re-init wrote no identity file at %s: %v", newPath, err)
	}
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("the re-init disturbed the removed vault's leftover identity file: %v", err)
	}
}
