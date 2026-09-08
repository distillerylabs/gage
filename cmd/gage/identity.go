package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// newIdentityCommand groups the device-side identity verbs. They are
// deliberately separate from `recipient`: `identity` is about what *this
// machine* holds a private key for, `recipient` about who the *vault*
// trusts, and the lost-identity recovery story only works because
// registering a key and being authorized to use it are two steps taken
// on two different devices. See "`identity` vs `recipient` stay
// separate" in the design doc.
func newIdentityCommand(app *App) *cobra.Command {
	parent := &cobra.Command{
		Use:   "identity",
		Short: "Manage this device's identities for a vault",
	}
	parent.AddCommand(newIdentityAddCommand(app))
	parent.AddCommand(newIdentityListCommand(app))
	return parent
}

// newIdentityAddCommand builds `gage identity add`: generate a new
// keypair for this device, wrap it with a passphrase, and print the
// public half.
//
// It never unlocks the vault and never touches the vault's recipient
// list. That is the point — the device most likely to run this is one
// that has just lost its key and cannot decrypt anything. Publishing the
// printed key is `gage recipient add`'s job, run from a device that
// still has access.
func newIdentityAddCommand(app *App) *cobra.Command {
	var (
		useFlag    string
		deviceFlag string
		methodFlag string
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: commandShort("identity add"),
		Long: commandShort("identity add") + ".\n\n" +
			"Generates a fresh keypair for this device, wraps its private half with a\n" +
			"passphrase you choose, and prints the public half. The vault does not yet\n" +
			"trust that key: hand it to a device that can already read the vault and run\n" +
			"`gage recipient add <pubkey> --device NAME --reencrypt` there.\n\n" +
			"This is also the recovery path for a lost identity file — register a fresh\n" +
			"identity under a new name rather than restoring the old key from a backup.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIdentityAdd(app, useFlag, deviceFlag, methodFlag)
		},
	}

	addUseFlag(cmd, &useFlag)
	cmd.Flags().StringVar(&deviceFlag, "device", "", "this device's name for the vault (default: normalized hostname)")
	cmd.Flags().StringVar(&methodFlag, "method", "",
		fmt.Sprintf("how this device unlocks the vault (accepted: %v; default: the vault's own)", gage.AllowedMethods()))
	return cmd
}

func runIdentityAdd(app *App, use, deviceFlag, methodFlag string) error {
	v, err := vaultWithoutUnlocking(app, use)
	if err != nil {
		return err
	}

	hostname := ""
	if deviceFlag == "" {
		h, err := os.Hostname()
		if err != nil {
			return exitcode.Wrap(exitcode.Internal, err)
		}
		hostname = h
	}
	device, err := resolveDeviceName(deviceFlag, hostname)
	if err != nil {
		return err
	}

	method, err := resolveIdentityMethod(v, methodFlag)
	if err != nil {
		return err
	}

	pubkey, err := v.AddIdentity(device, app.Prompter)
	if err != nil {
		if errors.Is(err, gage.ErrDeviceNameTaken) {
			// The message the library gives already names the colliding
			// device. What it can't know is that a *flag* is the way out
			// — which matters most in the case nobody typed a name at
			// all: two machines whose hostnames normalize the same.
			return exitcode.Newf(exitcode.Conflict,
				"%v; pass --device NAME to register this machine under a different name", err)
		}
		return err
	}

	if err := recordLocalIdentity(v.Name, device, method, pubkey); err != nil {
		return err
	}

	writeOut(app.Out, []string{
		fmt.Sprintf("gage: registered %q as this device's identity for vault %q", device, v.Name),
		fmt.Sprintf("gage: public key %s", pubkey),
		fmt.Sprintf("gage: from a device that can already read %q, run:", v.Name),
		fmt.Sprintf("gage:   gage recipient add %s --device %s --reencrypt", pubkey, device),
	})
	return nil
}

// resolveIdentityMethod picks how this device will unlock the vault:
// --method if given, otherwise the vault's [method].default.
//
// The vault's value is a *suggestion for devices joining*, not a
// constraint on any of them (Q-METHOD-SCOPE), which is why it is only a
// default here and why the answer is recorded locally rather than
// committed. The allowlist is the same one `init` validates against, so
// an unsupported method is rejected identically wherever it is typed.
func resolveIdentityMethod(v *gage.Vault, methodFlag string) (string, error) {
	method := methodFlag
	if method == "" {
		vc, err := readVaultConfig(v.Path)
		if err != nil {
			return "", err
		}
		method = vc.Method.Default
	}
	for _, allowed := range gage.AllowedMethods() {
		if method == allowed {
			return method, nil
		}
	}
	return "", exitcode.Newf(exitcode.Usage,
		"gage: unknown method %q; accepted: %v", method, gage.AllowedMethods())
}

// recordLocalIdentity points this machine's registration for a vault at
// the identity just created — the local half of the split `gage init`
// already uses, where the vault-side check lives in internal/gage and
// global config stays cmd/gage's.
//
// Re-pointing is deliberate rather than incidental: after a successful
// `identity add` the next unlock must use the new key without anyone
// hand-editing config, which is the step the lost-identity recovery
// story would otherwise be missing.
//
// The public key is recorded alongside them for Q-ORPHAN-BY-NAME: it is
// what lets `vault remove` decide whether the local key is still a
// recipient by comparing keys instead of device names, and this is one
// of the two moments gage holds the key without needing an unlock to
// reach it. Re-pointing must move it too — a stale pubkey beside a new
// device would answer that question about the wrong key.
func recordLocalIdentity(vault, device, method, pubkey string) error {
	g, err := readGlobalConfig()
	if err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	entry, ok := g.Vaults[vault]
	if !ok {
		return exitcode.Newf(exitcode.NotFound, "gage: no such vault %q", vault)
	}
	entry.Device = device
	entry.Method = method
	entry.Pubkey = pubkey
	g.Vaults[vault] = entry
	if err := writeGlobalConfig(g); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	return nil
}

// newIdentityListCommand builds `gage identity list`: the wrapped
// identity files this machine holds for a vault, with the one an unlock
// would use marked.
//
// It reports what this device can open, not who the vault trusts — a key
// listed here that the vault has since removed still shows up, because
// the file is still on disk. `gage recipient list` answers the other
// question.
func newIdentityListCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: commandShort("identity list"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultWithoutUnlocking(app, useFlag)
			if err != nil {
				return err
			}
			identities, err := v.ListIdentities()
			if err != nil {
				return err
			}
			if len(identities) == 0 {
				writeOut(app.Err, []string{fmt.Sprintf(
					"gage: this device holds no identity for vault %q; run `gage identity add`", v.Name)})
				return nil
			}

			lines := make([]string, 0, len(identities))
			for _, id := range identities {
				marker := "  "
				if id.Current {
					marker = "* "
				}
				lines = append(lines, fmt.Sprintf("%s%s\t%s", marker, id.Device, id.Path))
			}
			writeOut(app.Out, lines)
			return nil
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}
