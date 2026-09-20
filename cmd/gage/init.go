package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/config"
	"github.com/distillerylabs/gage/internal/gage/devicename"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/remoteauth"
	"github.com/distillerylabs/gage/internal/gage/syncerr"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
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
		dirFlag           string
		typeFlag          string
		methodFlag        string
		deviceFlag        string
		recipientFlags    []string
		remoteFlag        string
		noRecoveryKeyFlag bool
		recoveryKeyOut    string
	)

	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: commandShort("init"),
		Long: commandShort("init") + ".\n\n" +
			"Generates this device's identity, wraps its private key with a passphrase\n" +
			"you choose, and writes the public half into the vault's recipient files.\n" +
			"The wrapped key is stored outside the vault and is never committed or synced.\n\n" +
			"A vault that depends on one identity file surviving forever has no recovery\n" +
			"story, so init also generates an offline recovery key and shows it once. It is\n" +
			"not encrypted and gage keeps no copy: write it down, store it offline, and\n" +
			"treat the paper as you would the secrets it opens. --no-recovery-key skips it;\n" +
			"--recovery-key-out writes it to a file instead of the screen.\n\n" +
			"--recipient is additive: each one is written alongside this device's own key,\n" +
			"for recipients you already hold public keys for.\n\n" +
			"--remote publishes the new vault immediately. If its host needs a token and\n" +
			"none is stored yet, init asks for one on the spot (leave blank to skip and\n" +
			"try anonymously) — the same prompt as `gage auth login`, so a private repo\n" +
			"doesn't need a separate auth login round trip before the first push succeeds.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(app, initOptions{
				name:           args[0],
				dir:            dirFlag,
				typ:            typeFlag,
				method:         methodFlag,
				device:         deviceFlag,
				recipients:     recipientFlags,
				remote:         remoteFlag,
				noRecoveryKey:  noRecoveryKeyFlag,
				recoveryKeyOut: recoveryKeyOut,
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
		"additional recipient public key (repeatable; this device's own key is always included)")
	cmd.Flags().StringVar(&remoteFlag, "remote", "", "git remote (origin) URL; omit to start local-only")
	cmd.Flags().BoolVar(&noRecoveryKeyFlag, "no-recovery-key", false,
		"don't generate an offline recovery key (leaves this device's identity file the only way in)")
	cmd.Flags().StringVar(&recoveryKeyOut, "recovery-key-out", "",
		"write the recovery key to this file (mode 0600) instead of showing it")
	return cmd
}

type initOptions struct {
	name           string
	dir            string
	typ            string
	method         string
	device         string
	recipients     []string
	remote         string
	noRecoveryKey  bool
	recoveryKeyOut string
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

	// Settled before anything is created, alongside the other knowable
	// failures: an unusable --recovery-key-out, or a default run with no
	// terminal to show a key on, costs nobody a passphrase entry.
	recoveryMode, err := planRecoveryKey(app, opt.noRecoveryKey, opt.recoveryKeyOut)
	if err != nil {
		return err
	}
	// The recovery recipient's label is fixed, so a device claiming it
	// would be a collision Create rejects. Catching it here keeps the
	// refusal in the same class as an invalid --device: nothing generated,
	// nothing rolled back. With no recovery key there is no label to
	// collide with, and the name is ordinary.
	if recoveryMode != recoveryKeyNone && device == gage.RecoveryDeviceLabel {
		return exitcode.Newf(exitcode.Usage,
			"gage: %q is the name gage gives this vault's recovery key, so a device can't use it; pass --device NAME, or --no-recovery-key",
			gage.RecoveryDeviceLabel)
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

	// Checked here rather than with the other flag validation: whether a
	// destination is safe depends on where this vault is going, which is
	// only settled above. Still before anything is created, which is the
	// property that matters — a destination that cannot be written, or
	// that gage would later delete, must not be discovered after there is
	// a key riding on it.
	if recoveryMode == recoveryKeyFile {
		if err := checkRecoveryKeyOutPath(opt.recoveryKeyOut, path); err != nil {
			return err
		}
	}

	// The vault's id is minted here, before the identity, and handed to
	// Create below rather than minted there. Ordering is the whole point:
	// the identity file is filed under this id (see IdentitiesDir), so an
	// id that did not exist until after CreateIdentity had already
	// written the file would leave a working vault carrying one id and
	// this device's only key filed under another. Nothing would look
	// wrong; the key would simply be unreachable. See A20.
	id := vaultconfig.NewID()

	// The identity comes second, and with it the passphrase prompt. Every
	// later step can fail on something already knowable (a non-empty
	// target directory, a bad recipient), so asking a human to type a
	// passphrase twice and only then reporting one of those would be the
	// wrong order to fail in.
	pubkey, err := gage.CreateIdentity(id, opt.name, device, app.Prompter)
	if err != nil {
		return err
	}

	// This device's own key leads, and --recipient keys follow in the
	// order given: gage.Create labels the first recipient with the device
	// name and the rest generically, so the order is what makes the label
	// correct.
	recipients := append([]string{pubkey}, opt.recipients...)

	// The recovery key is generated here but not shown until the vault
	// exists — see deliverRecoveryKey. Nothing needs rolling back if
	// anything below fails: it was never written down, stored, or seen.
	var (
		recoveryKey    gage.RecoveryKey
		extraRecipient []gage.LabelledRecipient
	)
	if recoveryMode != recoveryKeyNone {
		recoveryKey, err = gage.NewRecoveryKey()
		if err != nil {
			return err
		}
		defer zeroBytes(recoveryKey.Secret)
		extraRecipient = []gage.LabelledRecipient{
			{Device: gage.RecoveryDeviceLabel, Pubkey: recoveryKey.Pubkey},
		}
	}

	spec := gage.CreateSpec{
		Name:            opt.name,
		ID:              id,
		Path:            path,
		Type:            opt.typ,
		Method:          opt.method,
		Device:          device,
		Recipients:      recipients,
		ExtraRecipients: extraRecipient,
		Remote:          opt.remote,
	}
	v, err := gage.Create(spec)
	if err != nil {
		// Roll back the identity this call generated, unconditionally.
		// It is referenced by nothing — Create wrote no vault at all —
		// and it sits alone under a directory named by an id that now
		// belongs to no vault, so nothing will ever look it up again.
		//
		// This used to be conditional on the file not having existed
		// beforehand, since CreateIdentity would otherwise have reused
		// someone else's key. With A20 that condition is dead: `init`
		// mints a fresh id every run, so the directory it addresses is
		// always empty and the reuse path is unreachable from here. The
		// same change makes this rollback load-bearing rather than tidy
		// — without it, repeated failed inits leave one orphaned
		// directory per attempt, each holding a private key for a vault
		// that was never created.
		if rmErr := gage.RemoveIdentity(id, device); rmErr != nil {
			return exitcode.Newf(exitcode.Internal,
				"gage: %v (and rolling back the generated identity failed: %v)", err, rmErr)
		}
		return err
	}

	if g.Vaults == nil {
		g.Vaults = map[string]config.VaultEntry{}
	}
	entry := config.VaultEntry{
		Path:   path,
		ID:     id,
		Type:   opt.typ,
		Device: device,
		// Recorded here, at the one moment gage holds this device's key
		// without needing an unlock to reach it, so `vault remove` can
		// later ask whether the local key is still a recipient by
		// comparing keys rather than device names. See Q-ORPHAN-BY-NAME.
		Pubkey: pubkey,
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

	// After registration, so a key is never shown for a vault that isn't
	// usable yet, and before the summary, so the one thing needing action
	// isn't the thing that scrolled off.
	//
	// Its error is held rather than returned: by this point the vault is
	// created, committed and registered, and init has a remote to publish
	// to and a summary to print. Returning here would abandon both, so a
	// fumbled confirmation would leave a vault that exists, is registered,
	// and was never pushed — with nothing on screen saying so. Everything
	// init was asked to do still happens; the command then exits on this.
	deliveryErr := deliverRecoveryKey(app, recoveryMode, opt.name, opt.recoveryKeyOut, recoveryKey)

	summary := []string{
		fmt.Sprintf("gage: initialized vault %q at %s", opt.name, path),
		fmt.Sprintf("gage: this device is %q, public key %s", device, pubkey),
	}
	if recoveryMode != recoveryKeyNone {
		summary = append(summary,
			fmt.Sprintf("gage: recovery key is %q, public key %s", gage.RecoveryDeviceLabel, recoveryKey.Pubkey))
	}
	writeOut(app.Out, summary)

	if opt.remote != "" {
		if err := publishNewVault(app, v, opt.remote); err != nil {
			if deliveryErr == nil {
				return err
			}
			// Both failed. The recovery key is the worse of the two — a
			// vault that exists but isn't published can be pushed again,
			// while a key nobody has cannot be reissued — so that is what
			// the exit code reports, and this is reported alongside it.
			writeError(app.Err, err)
		}
	}
	return deliveryErr
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
func publishNewVault(app *App, v *gage.Vault, remote string) error {
	name := v.Name
	if err := ensureRemoteToken(app, remote); err != nil {
		return err
	}

	ctx, cancel := syncContext()
	defer cancel()

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

// ensureRemoteToken solicits a token for remote's host, once, before
// gage attempts to publish a freshly created vault to it.
//
// Without this, `gage init --remote` against a private HTTPS host with
// no token yet would publish anonymously, fail with `gage auth login`'s
// name buried in publishNewVault's own error, and require a second
// command (auth login) plus a third (push) to finish what init was
// asked to do in one. Soliciting the token here — the same prompt `gage
// auth login` uses — collapses that back to one command.
//
// A blank answer skips storing anything and lets init try the push
// anonymously, same as before: not every remote needs a token (a public
// repository, one already reachable by ssh-agent), and init has no way
// to know which case it's in without asking. ensureRemoteToken itself
// only asks for hosts TokenHost says are token candidates at all — a
// local path or an SSH remote is never prompted for one.
func ensureRemoteToken(app *App, remote string) error {
	host, err := remoteauth.TokenHost(remote)
	if err != nil {
		return err
	}
	if host == "" {
		return nil
	}
	if _, err := remoteauth.Load(host); err == nil {
		return nil
	} else if !errors.Is(err, remoteauth.ErrNoToken) {
		return err
	}

	token, err := app.Prompter.Value(fmt.Sprintf("Token for %s (leave blank to skip): ", host))
	if err != nil {
		return exitcode.Wrap(exitcode.LockedOrAuth, err)
	}
	if strings.TrimSpace(token) == "" {
		return nil
	}
	if err := remoteauth.Store(host, token); err != nil {
		return err
	}

	writeOut(app.Out, []string{fmt.Sprintf("gage: stored a token for %s", host)})
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
