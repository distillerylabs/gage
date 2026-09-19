package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/devicename"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// initVaultForTest runs `gage init` under an already-isolated XDG root
// and returns the vault's path. Every M9 CLI test starts from a real
// vault created by the real command rather than a hand-built directory,
// so what these tests drive is the same thing a user drives.
func initVaultForTest(t *testing.T, name string, extraArgs ...string) string {
	t.Helper()
	args := append([]string{"init", name}, extraArgs...)
	res := runCLI(t, args, "")
	if res.Code != 0 {
		t.Fatalf("init exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	vaultsDir, err := gage.VaultsDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(vaultsDir, name)
}

// normalizedHostname is the device name `gage` picks when --device is
// omitted. Computed the same way the command does rather than hardcoded,
// since it differs on every CI machine.
func normalizedHostname(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	name, ok := devicename.Normalize(h)
	if !ok {
		t.Skipf("this machine's hostname (%q) doesn't normalize to a usable device name", h)
	}
	return name
}

// TestIdentityAddRegistersADeviceAndPrintsItsPublicKey is the plan's
// "`gage identity add` registers a device and prints a public key."
func TestIdentityAddRegistersADeviceAndPrintsItsPublicKey(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")

	res := runCLI(t, []string{"identity", "add", "--device", "laptop-2"}, "")
	if res.Code != 0 {
		t.Fatalf("identity add exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "age1") {
		t.Errorf("stdout = %q, want it to print the new public key", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "laptop-2") {
		t.Errorf("stdout = %q, want it to name the device it registered", res.Stdout)
	}

	path, err := gage.IdentityFilePath(vaultIDForTest(t, "personal"), "laptop-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no wrapped identity at %s: %v", path, err)
	}

	// The new identity becomes this device's registration, so the next
	// unlock uses it without anyone editing config by hand — the step
	// the lost-identity recovery path depends on.
	g := readGlobalConfigForTest(t)
	if got := g.Vaults["personal"].Device; got != "laptop-2" {
		t.Errorf("global config device = %q, want laptop-2", got)
	}
}

// TestIdentityAddRejectsANameTheVaultAlreadyLists is the plan's rejection
// bullet, driven through the CLI so the exit code is part of the
// contract.
func TestIdentityAddRejectsANameTheVaultAlreadyLists(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")

	before, err := os.ReadFile(identityPathForTest(t, "personal", "laptop-1"))
	if err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"identity", "add", "--device", "laptop-1"}, "")
	if res.Code == 0 {
		t.Fatal("identity add with an already-registered name succeeded")
	}
	if res.Code != int(exitcode.Conflict) {
		t.Errorf("exit code = %d, want Conflict (%d)", res.Code, exitcode.Conflict)
	}
	if !strings.Contains(res.Stderr, "laptop-1") {
		t.Errorf("stderr = %q, want it to name the colliding device", res.Stderr)
	}

	after, err := os.ReadFile(identityPathForTest(t, "personal", "laptop-1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the existing identity file was overwritten by a rejected identity add")
	}
}

// TestIdentityAddRejectsAHostnameCollisionWithoutAnyoneTypingTheName is
// the specific case the plan calls out: "two machines sharing a
// hostname, where the default name collides without anyone typing it."
//
// The vault is initialized under the normalized hostname, standing in
// for the first machine; a second `identity add` with no --device is the
// second machine reaching for the same default.
func TestIdentityAddRejectsAHostnameCollisionWithoutAnyoneTypingTheName(t *testing.T) {
	isolateXDG(t)
	host := normalizedHostname(t)
	initVaultForTest(t, "personal")

	vc := readVaultConfigForTest(t, "personal")
	if len(vc.Recipients) == 0 || vc.Recipients[0].Device != host {
		t.Fatalf("init registered %v, want the normalized hostname %q as the first recipient", vc.Recipients, host)
	}

	res := runCLI(t, []string{"identity", "add"}, "")
	if res.Code == 0 {
		t.Fatal("identity add took the hostname default that the vault already lists as a recipient")
	}
	if !strings.Contains(res.Stderr, host) {
		t.Errorf("stderr = %q, want it to name the colliding default %q", res.Stderr, host)
	}
	if !strings.Contains(res.Stderr, "--device") {
		t.Errorf("stderr = %q, want it to point at --device as the way out", res.Stderr)
	}
}

// TestIdentityAddDeviceFlagOverridesTheHostnameDefault is the plan's
// "`gage identity add --device NAME` overrides the hostname default;
// omitted, the normalized hostname is used." Both directions, in one
// vault.
func TestIdentityAddDeviceFlagOverridesTheHostnameDefault(t *testing.T) {
	isolateXDG(t)
	host := normalizedHostname(t)
	// Initialized under an explicit name, so the hostname default is
	// still free for the second half of this test.
	initVaultForTest(t, "personal", "--device", "workstation")

	if res := runCLI(t, []string{"identity", "add", "--device", "explicit-name"}, ""); res.Code != 0 {
		t.Fatalf("identity add --device: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if _, err := os.Stat(identityPathForTest(t, "personal", "explicit-name")); err != nil {
		t.Errorf("--device did not decide the identity file's name: %v", err)
	}

	if res := runCLI(t, []string{"identity", "add"}, ""); res.Code != 0 {
		t.Fatalf("identity add without --device: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if _, err := os.Stat(identityPathForTest(t, "personal", host)); err != nil {
		t.Errorf("omitting --device did not fall back to the normalized hostname %q: %v", host, err)
	}
}

// TestIdentityAddMethodDefaultsToTheVaultsAndStaysLocal is the plan's
// method bullet: no --method takes the vault's [method].default,
// --method passphrase is equivalent, any other value fails on the same
// allowlist as init — and the choice is recorded locally rather than in
// the vault (Q-METHOD-SCOPE).
func TestIdentityAddMethodDefaultsToTheVaultsAndStaysLocal(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")

	vaultDefault := readVaultConfigForTest(t, "personal").Method.Default
	if vaultDefault != gage.MethodPassphrase {
		t.Fatalf("vault [method].default = %q, want %q", vaultDefault, gage.MethodPassphrase)
	}

	if res := runCLI(t, []string{"identity", "add", "--device", "laptop-2"}, ""); res.Code != 0 {
		t.Fatalf("identity add: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if got := readGlobalConfigForTest(t).Vaults["personal"].Method; got != vaultDefault {
		t.Errorf("recorded method = %q, want the vault's default %q", got, vaultDefault)
	}

	if res := runCLI(t, []string{"identity", "add", "--device", "laptop-3", "--method", "passphrase"}, ""); res.Code != 0 {
		t.Fatalf("identity add --method passphrase: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if got := readGlobalConfigForTest(t).Vaults["personal"].Method; got != gage.MethodPassphrase {
		t.Errorf("recorded method = %q, want passphrase", got)
	}

	res := runCLI(t, []string{"identity", "add", "--device", "laptop-4", "--method", "yubikey"}, "")
	if res.Code == 0 {
		t.Fatal("identity add accepted a method outside the allowlist")
	}
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want Usage (%d), the same as init's allowlist rejection", res.Code, exitcode.Usage)
	}
	if _, err := os.Stat(identityPathForTest(t, "personal", "laptop-4")); err == nil {
		t.Error("a rejected --method still wrote an identity file")
	}

	// Nothing about any device's method reached the committed vault
	// config — the whole point of recording it locally. The only
	// occurrence of the word is the [method] table itself; a
	// per-recipient method field would tell anyone with read access
	// which recipient is the softest target (Q-METHOD-SCOPE).
	data, err := os.ReadFile(filepath.Join(vaultPath, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "method"); n != 1 {
		t.Errorf(".gage/config.toml mentions \"method\" %d times, want exactly 1 (the [method] table):\n%s", n, data)
	}
}

// TestIdentityListReportsThisDevicesIdentities is the plan's "`gage
// identity list` reports the local device's registered identities for a
// vault."
func TestIdentityListReportsThisDevicesIdentities(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")
	if res := runCLI(t, []string{"identity", "add", "--device", "laptop-2"}, ""); res.Code != 0 {
		t.Fatalf("identity add: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	res := runCLI(t, []string{"identity", "list"}, "")
	if res.Code != 0 {
		t.Fatalf("identity list exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	for _, want := range []string{"laptop-1", "laptop-2"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("identity list stdout = %q, want it to list %q", res.Stdout, want)
		}
	}
	// No private key material ever reaches stdout.
	if strings.Contains(res.Stdout, "AGE-SECRET-KEY") {
		t.Fatal("identity list printed private key material")
	}
}

// identityPathForTest resolves a device's wrapped identity path under
// the currently isolated XDG roots.
func identityPathForTest(t *testing.T, vault, device string) string {
	t.Helper()
	path, err := gage.IdentityFilePath(vaultIDForTest(t, vault), device)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
