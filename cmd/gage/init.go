package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/devicename"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// newInitCommand builds `gage init`, a thin wiring layer over
// gage.Create: it resolves the flags' defaults (device name, target
// path), rejects a name that's already registered before touching
// anything, calls Create, and — only once that on-disk skeleton exists —
// registers the new vault in global config. --type and --method default
// to (and, today, can only be) "git"/"passphrase"; gage.Create is what
// actually enforces the allowlist, so this command doesn't duplicate
// that check.
func newInitCommand(app *App) *cobra.Command {
	var (
		dirFlag        string
		typeFlag       string
		methodFlag     string
		deviceFlag     string
		recipientFlags []string
		remoteFlag     string
	)

	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: commandShort("init"),
		Long: commandShort("init") + ".\n\n" +
			"M1 requires at least one --recipient: there is no local identity to\n" +
			"generate one from yet (that arrives in M2, which makes the flag optional).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(app, initOptions{
				name:       args[0],
				dir:        dirFlag,
				typ:        typeFlag,
				method:     methodFlag,
				device:     deviceFlag,
				recipients: recipientFlags,
				remote:     remoteFlag,
			})
		},
	}

	cmd.Flags().StringVar(&dirFlag, "dir", "", "target directory (default: $GAGE_DATA/vaults/<name>)")
	cmd.Flags().StringVar(&typeFlag, "type", gage.TypeGit,
		fmt.Sprintf("vault type (accepted: %v)", gage.AllowedTypes()))
	cmd.Flags().StringVar(&methodFlag, "method", gage.MethodPassphrase,
		fmt.Sprintf("default identity method for devices joining this vault (accepted: %v)", gage.AllowedMethods()))
	cmd.Flags().StringVar(&deviceFlag, "device", "", "this device's name (default: normalized hostname)")
	cmd.Flags().StringArrayVar(&recipientFlags, "recipient", nil,
		"recipient public key (repeatable; required in M1, since there's no identity to generate one from yet)")
	cmd.Flags().StringVar(&remoteFlag, "remote", "", "git remote (origin) URL; omit to start local-only")
	return cmd
}

type initOptions struct {
	name       string
	dir        string
	typ        string
	method     string
	device     string
	recipients []string
	remote     string
}

func runInit(app *App, opt initOptions) error {
	hostname := ""
	if opt.device == "" {
		h, err := os.Hostname()
		if err != nil {
			return exitcode.Wrap(exitcode.Internal, err)
		}
		hostname = h
	}
	device, err := resolveDeviceName(opt.device, hostname)
	if err != nil {
		return err
	}

	g, err := readGlobalConfig()
	if err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	// Rejected outright rather than offered a --force: M1 has no story
	// yet for what "overwrite an existing registration" should even do
	// to the vault it's currently pointing at, and the conservative
	// choice is the one that's safe to widen later.
	if _, exists := g.Vaults[opt.name]; exists {
		return exitcode.Newf(exitcode.Conflict, "gage: %q is already a registered vault", opt.name)
	}

	path := opt.dir
	if path == "" {
		vaultsDir, err := gage.VaultsDir()
		if err != nil {
			return exitcode.Wrap(exitcode.Internal, err)
		}
		path = filepath.Join(vaultsDir, opt.name)
	}

	spec := gage.CreateSpec{
		Name:       opt.name,
		Path:       path,
		Type:       opt.typ,
		Method:     opt.method,
		Device:     device,
		Recipients: opt.recipients,
		Remote:     opt.remote,
	}
	if _, err := gage.Create(spec); err != nil {
		return err
	}

	if g.Vaults == nil {
		g.Vaults = map[string]config.VaultEntry{}
	}
	entry := config.VaultEntry{
		Path:   path,
		Type:   opt.typ,
		Device: device,
		Method: opt.method,
	}
	if opt.remote != "" {
		entry.Git.Origin = opt.remote
	}
	g.Vaults[opt.name] = entry
	if g.Current == "" {
		g.Current = opt.name
	}
	if err := writeGlobalConfig(g); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}

	writeOut(app.Out, []string{fmt.Sprintf("gage: initialized vault %q at %s", opt.name, path)})
	return nil
}

// resolveDeviceName applies --device if given (validated against the
// same allowlist Create itself checks), or falls back to normalizing
// hostname (the caller's os.Hostname() result — passed in rather than
// read here, so this stays a pure function tests can drive directly). If
// normalization leaves nothing usable, gage asks for a name rather than
// inventing one — in one-shot mode, "asking" means failing with a usage
// error that names the flag to supply, since there's no free-text prompt
// in the Prompter interface yet to drive an actual interactive question
// from here.
func resolveDeviceName(explicit, hostname string) (string, error) {
	if explicit != "" {
		if !devicename.Valid(explicit) {
			return "", exitcode.Newf(exitcode.Usage, "gage: --device %q is invalid", explicit)
		}
		return explicit, nil
	}

	normalized, ok := devicename.Normalize(hostname)
	if !ok {
		return "", exitcode.Newf(exitcode.Usage,
			"gage: this machine's hostname (%q) doesn't normalize to a usable device name; pass --device NAME", hostname)
	}
	return normalized, nil
}
