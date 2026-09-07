package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/devicename"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/syncerr"
)

// newInitCommand builds `gage init`, a thin wiring layer over
// gage.CreateIdentity and gage.Create: it resolves the flags' defaults
// (device name, target path), rejects a name that's already registered
// before touching anything, generates this device's identity, calls
// Create with its public key, and — only once that on-disk skeleton
// exists — registers the new vault in global config. --type and --method
// default to (and, today, can only be) "git"/"passphrase"; gage.Create is
// what actually enforces the allowlist, so this command doesn't duplicate
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
			"Generates this device's identity, wraps its private key with a passphrase\n" +
			"you choose, and writes the public half into the vault's recipient files.\n" +
			"The wrapped key is stored outside the vault and is never committed or synced.\n\n" +
			"--recipient is additive: each one is written alongside this device's own key,\n" +
			"which is how a recovery key gets into a vault from the start. A vault that\n" +
			"depends on exactly one identity file surviving forever has no recovery story.",
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
		"additional recipient public key, e.g. a recovery key (repeatable; this device's own key is always included)")
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

	// Checked before CreateIdentity so the rollback below knows whether a
	// failure further down would be destroying something this call made
	// or something that was already here — see there for why that
	// distinction matters.
	identityExisted, err := gage.HasIdentity(opt.name, device)
	if err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}

	// The identity comes first, and with it the passphrase prompt. Every
	// later step can fail on something already knowable (a non-empty
	// target directory, a bad recipient), so asking a human to type a
	// passphrase twice and only then reporting one of those would be the
	// wrong order to fail in. If an identity file already sits at this
	// vault/device path — typically surviving a `vault remove` that
	// couldn't prove it was safe to delete (see removeOrphanedIdentity) —
	// CreateIdentity reuses it instead of failing, which is also what
	// makes retrying the same `init` after fixing an unrelated problem an
	// ordinary retry rather than a permanent conflict.
	pubkey, err := gage.CreateIdentity(opt.name, device, app.Prompter)
	if err != nil {
		return err
	}

	// This device's own key leads, and --recipient keys follow in the
	// order given: gage.Create labels the first recipient with the device
	// name and the rest generically, so the order is what makes the label
	// correct.
	recipients := append([]string{pubkey}, opt.recipients...)

	spec := gage.CreateSpec{
		Name:       opt.name,
		Path:       path,
		Type:       opt.typ,
		Method:     opt.method,
		Device:     device,
		Recipients: recipients,
		Remote:     opt.remote,
	}
	if _, err := gage.Create(spec); err != nil {
		// Only roll back an identity file this call actually generated.
		// One that already existed came from somewhere else — possibly
		// still needed there — and CreateIdentity only reused it; deleting
		// it here would destroy access this failed init never granted and
		// has no way to restore. A freshly generated one, by contrast, is
		// referenced by nothing yet (Create wrote no vault at all), so
		// leaving it in place would just have `gage init` reuse it — with
		// its just-chosen passphrase — on the very next retry, rather than
		// letting that retry start clean.
		if !identityExisted {
			if rmErr := gage.RemoveIdentity(opt.name, device); rmErr != nil {
				return exitcode.Newf(exitcode.Internal,
					"gage: %v (and rolling back the generated identity failed: %v)", err, rmErr)
			}
		}
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

	writeOut(app.Out, []string{
		fmt.Sprintf("gage: initialized vault %q at %s", opt.name, path),
		fmt.Sprintf("gage: this device is %q, public key %s", device, pubkey),
	})

	if opt.remote == "" {
		return nil
	}
	return publishNewVault(app, opt.name, path, opt.remote)
}

// publishNewVault pushes a freshly created vault's first commit to the
// remote it was given.
//
// Without this a vault created with --remote would sit unpublished until
// its first write, which is not what naming a remote at creation time
// means. It is also where "gage never creates a repository for you" gets
// said out loud: an empty repository has to exist on the host already,
// and the failure when it doesn't is reported in those terms rather than
// as a transport error.
//
// The vault itself is already created and registered by this point, and
// stays that way regardless: it is durable locally, which is the whole
// local-durability half of the sync model. Being unable to publish is a
// thing to report, never a reason to throw away a vault that exists.
func publishNewVault(app *App, name, path, remote string) error {
	ctx, cancel := syncContext()
	defer cancel()

	v := &gage.Vault{Name: name, Path: path}
	if _, err := v.Push(ctx); err != nil {
		if errors.Is(err, syncerr.ErrUnreachable) {
			// Offline at creation time is not a failure: the next
			// successful sync publishes it.
			writeOut(app.Err, []string{fmt.Sprintf(
				"gage: could not reach %s; %q exists locally and will publish on the next successful sync",
				remote, name)})
			return nil
		}
		return exitcode.Wrap(exitcode.CodeOf(err), fmt.Errorf(
			"%q was created locally, but publishing it to %s failed: %s\n"+
				"gage: gage never creates a repository for you — create an empty one there, then run `gage push`",
			name, remote, errorClause(err)))
	}

	writeOut(app.Out, []string{fmt.Sprintf("gage: published %q to %s", name, remote)})
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
