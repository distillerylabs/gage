package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/atomicfile"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// newRecoveryCommand groups the verbs for a vault's offline recovery key.
// Proving a stored copy still works, and spending it to get a device back.
// It is its own group rather than a `recipient` subcommand because
// `recipient verify` already means something else — that the two recipient
// files agree — and two verifies under one parent would be a trap.
func newRecoveryCommand(app *App) *cobra.Command {
	parent := &cobra.Command{
		Use:   "recovery",
		Short: "Work with a vault's offline recovery key",
	}
	parent.AddCommand(newRecoveryVerifyCommand(app))
	parent.AddCommand(newRecoveryEnrollCommand(app))
	parent.AddCommand(newRecoveryRotateCommand(app))
	return parent
}

// newRecoveryVerifyCommand builds `gage recovery verify`, which checks a
// pasted key against the vault's recipient list.
//
// It needs no unlock, and that is the point: a recovery key is what you
// reach for when this device's identity file is gone, so the check cannot
// be one that depends on it. It reads only the plaintext recipient list.
func newRecoveryVerifyCommand(app *App) *cobra.Command {
	var useFlag string

	cmd := &cobra.Command{
		Use:   "verify",
		Short: commandShort("recovery verify"),
		Long: commandShort("recovery verify") + ".\n\n" +
			"Paste the key you stored offline (it is read without echo, and neither shown\n" +
			"nor kept) and gage checks that it is one of this vault's recipients. That\n" +
			"proves the copy you hold is intact and belongs to this vault. It does not\n" +
			"decrypt anything, so it cannot tell you the vault's entries are undamaged.\n\n" +
			"It also says whether `gage recovery enroll` will accept the key, which is a\n" +
			"narrower question: only the key registered as the vault's recovery key can\n" +
			"enroll a device, even though any recipient's key decrypts the entries.\n\n" +
			"It needs no passphrase: it never touches this device's identity file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultWithoutUnlocking(app, useFlag)
			if err != nil {
				return err
			}

			pasted, err := app.Prompter.Value("Paste the recovery key to check: ")
			if err != nil {
				return exitcode.Wrap(exitcode.LockedOrAuth, err)
			}
			// The string the prompter returned is a copy Go will not let us
			// erase; this one is ours, and is zeroed as soon as the check is
			// done. The same accepted gap init documents.
			secret := []byte(pasted)
			defer zeroBytes(secret)

			label, err := v.VerifyRecoveryKey(secret)
			if err != nil {
				return err
			}
			// Two claims, reported separately, because they are not the
			// same and only one of them is what `recovery enroll` needs.
			// A key registered under another label really does decrypt
			// this vault — `age -d -i` opens every entry with it — so
			// calling that a failure would be false. What it cannot do is
			// enroll a device, and saying so here is the whole point:
			// verify is the check the docs tell people to trust, and it
			// must not pass a key enroll will refuse.
			lines := []string{fmt.Sprintf(
				"gage: that key is a recipient of vault %q, registered as %q", v.Name, label)}
			if label == gage.RecoveryDeviceLabel {
				lines = append(lines, "gage: it can decrypt this vault, and `gage recovery enroll` will accept it")
			} else {
				lines = append(lines,
					"gage: it can decrypt this vault, but `gage recovery enroll` will not accept it —",
					fmt.Sprintf("gage: that needs the key registered as %q.", gage.RecoveryDeviceLabel))
			}
			writeOut(app.Out, lines)
			return nil
		},
	}

	addUseFlag(cmd, &useFlag)
	return cmd
}

// ErrLocalIdentityExists is `recovery enroll` refusing to clobber a
// wrapped identity this machine already holds for the device it was told
// to enroll.
//
// It is a cmd-level error because the question is a frontend policy: the
// library's CreateIdentity would happily *reuse* that file, asking for its
// passphrase, which is precisely the wrong move here — a forgotten
// passphrase is one of the two things this command exists to route around,
// and prompting for it would be asking the user for the thing they came
// here because they do not have.
var ErrLocalIdentityExists = errors.New("gage: this machine already holds an identity for that device")

// newRecoveryEnrollCommand builds `gage recovery enroll`: spend the
// recovery key to get a working device back.
//
// This is the recovery key's one power inside gage, and the command is
// shaped so that spending it is also the end of it — the key that has just
// been typed into a terminal is retired in the same commit that admits the
// new device, and a replacement is minted and shown unless asked
// otherwise. See the library's RecoverDevice.
func newRecoveryEnrollCommand(app *App) *cobra.Command {
	var (
		useFlag        string
		deviceFlag     string
		replacesFlag   string
		noNewKeyFlag   bool
		recoveryKeyOut string
	)

	cmd := &cobra.Command{
		Use:   "enroll",
		Short: commandShort("recovery enroll"),
		Long: commandShort("recovery enroll") + ".\n\n" +
			"For the machine whose identity file is gone, or whose passphrase is. Paste the\n" +
			"recovery key you stored offline and gage generates a fresh identity for this\n" +
			"device, admits it to the vault, and retires the key you just used — all in one\n" +
			"commit. The key is retired because it has now been typed into a terminal, and\n" +
			"it reads everything.\n\n" +
			"A replacement recovery key is generated and shown once, so the vault is never\n" +
			"left without a way back. --no-new-recovery-key skips it; --recovery-key-out\n" +
			"writes it to a file instead of the screen.\n\n" +
			"--replaces names the device that was lost, removing its key in the same commit\n" +
			"so a stolen identity file stops opening anything new. Naming the device being\n" +
			"enrolled is how you rebuild a machine under the name it already had, and the\n" +
			"only case in which this replaces a local identity file rather than refusing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRecoveryEnroll(app, recoveryEnrollOptions{
				use:            useFlag,
				device:         deviceFlag,
				replaces:       replacesFlag,
				noNewKey:       noNewKeyFlag,
				recoveryKeyOut: recoveryKeyOut,
			})
		},
	}

	addUseFlag(cmd, &useFlag)
	cmd.Flags().StringVar(&deviceFlag, "device", "", "this device's name for the vault (default: normalized hostname)")
	cmd.Flags().StringVar(&replacesFlag, "replaces", "", "device whose key to remove in the same commit (the one that was lost)")
	cmd.Flags().BoolVar(&noNewKeyFlag, "no-new-recovery-key", false,
		"don't generate a replacement recovery key (leaves the vault without one)")
	cmd.Flags().StringVar(&recoveryKeyOut, "recovery-key-out", "",
		"write the replacement recovery key to this file (mode 0600) instead of showing it")
	return cmd
}

type recoveryEnrollOptions struct {
	use            string
	device         string
	replaces       string
	noNewKey       bool
	recoveryKeyOut string
}

func runRecoveryEnroll(app *App, opt recoveryEnrollOptions) error {
	v, err := vaultWithoutUnlocking(app, opt.use)
	if err != nil {
		return err
	}
	device, err := deviceNameFor(opt.device)
	if err != nil {
		return err
	}

	// Everything knowable about the replacement key first, before a single
	// prompt: a run with nowhere to show it must not get as far as asking
	// for the old one. Same rule and same helper as `init`.
	mode, err := planRecoveryKey(app, "--no-new-recovery-key", opt.noNewKey, opt.recoveryKeyOut)
	if err != nil {
		return err
	}
	if mode == recoveryKeyFile {
		if err := checkRecoveryKeyOutPath(opt.recoveryKeyOut, v.Path); err != nil {
			return err
		}
	}

	// A name the vault already lists is knowable from the plaintext
	// recipient list, so it is refused here rather than by the swap — which
	// would only say so after the paste and a full passphrase entry, for a
	// mistake visible before either.
	if err := checkDeviceNameFree(v, device, opt.replaces); err != nil {
		return err
	}

	// A local identity file for this device is replaced only when
	// --replaces says this device is being rebuilt; otherwise it is a
	// different machine's key and enrolling over it would strand a vault
	// this machine can still open.
	replacing := opt.replaces == device
	backup, err := identityBackupFor(v, device, replacing)
	if err != nil {
		return err
	}

	// The paste, checked before the new passphrase is chosen: a wrong key
	// is the likeliest mistake here, and it must not cost two blind
	// passphrase entries first. RecoverDevice checks it again for real —
	// this is only about the order of the questions.
	pasted, err := app.Prompter.Value("Paste the recovery key for " + v.Name + ": ")
	if err != nil {
		return exitcode.Wrap(exitcode.LockedOrAuth, err)
	}
	secret := []byte(pasted)
	defer zeroBytes(secret)
	if err := checkPastedRecoveryKey(v, secret); err != nil {
		return err
	}

	var newKey gage.RecoveryKey
	if mode != recoveryKeyNone {
		if newKey, err = gage.NewRecoveryKey(); err != nil {
			return err
		}
		defer zeroBytes(newKey.Secret)
	}

	if err := backup.clear(); err != nil {
		return err
	}
	pubkey, err := gage.CreateIdentity(v.ID, v.Name, device, app.Prompter)
	if err != nil {
		return backup.restoreAfter(err)
	}

	res, err := v.RecoverDevice(gage.RecoverSpec{
		Device:            device,
		Pubkey:            pubkey,
		Replaces:          opt.replaces,
		NewRecoveryPubkey: newKey.Pubkey,
	}, secret, app.Prompter)
	if err != nil {
		// The identity this call generated is referenced by nothing —
		// RecoverDevice committed nothing — so it is rolled back, and any
		// file it displaced is put back exactly as it was. Same reasoning
		// as init's rollback.
		if rmErr := gage.RemoveIdentity(v.ID, device); rmErr != nil {
			return exitcode.Newf(exitcode.Internal,
				"gage: %v (and rolling back the generated identity failed: %v)", err, rmErr)
		}
		return backup.restoreAfter(err)
	}

	// The replacement key goes out first, ahead of everything that could
	// still fail. By this point the swap has committed and pushed, so that
	// key is already the vault's and its private half exists only in this
	// process — anything that errors after RecoverDevice and before this
	// would take it with it. Its own error is held, as init holds it.
	deliveryErr := deliverRecoveryKey(app, mode, v.Name, opt.recoveryKeyOut, newKey)

	// Now the local bookkeeping. A failure here is real but recoverable —
	// the identity file exists and the vault lists it, so re-running
	// `gage identity add --device <name>` re-registers it — and it must not
	// be what swallows the key above.
	recordErr := recordLocalIdentity(v.Name, device, gage.MethodPassphrase, pubkey)

	writeOut(app.Out, recoveryEnrollLines(v.Name, device, pubkey, res))
	if mode == recoveryKeyNone {
		warnNoRecoveryKey(app, v.Name)
	}
	if deliveryErr != nil {
		// The lost key is the worse of the two, so it sets the exit code;
		// the other is reported beside it.
		if recordErr != nil {
			writeError(app.Err, recordErr)
		}
		return deliveryErr
	}
	if recordErr != nil {
		return exitcode.Newf(exitcode.CodeOf(recordErr),
			"%v\ngage: the vault change is committed and %q is a recipient; re-run "+
				"`gage identity add --device %s` to register it on this machine",
			recordErr, device, device)
	}
	return nil
}

// checkPastedRecoveryKey is the early half of the two-stage check on the
// pasted key: it must be a recipient, and it must be *the recovery
// recipient*. Any recipient's key decrypts the vault, but only the one
// under the fixed label may enroll a device, and reporting that here
// rather than after the passphrase prompt is the whole point of checking
// twice.
func checkPastedRecoveryKey(v *gage.Vault, secret []byte) error {
	label, err := v.VerifyRecoveryKey(secret)
	if err != nil {
		return err
	}
	if label != gage.RecoveryDeviceLabel {
		return exitcode.Newf(exitcode.LockedOrAuth,
			"gage: that key is registered as %q, not as %q, so it cannot enroll a device.\n"+
				"gage: it still decrypts this vault's entries with `age -d -i`.",
			label, gage.RecoveryDeviceLabel)
	}
	return nil
}

func recoveryEnrollLines(vault, device, pubkey string, res gage.RecoverResult) []string {
	lines := []string{
		fmt.Sprintf("gage: enrolled %q in vault %q, public key %s", device, vault, pubkey),
		fmt.Sprintf("gage: retired the recovery key you used (%s)", res.RetiredRecovery),
	}
	if res.Replaced != "" {
		lines = append(lines, fmt.Sprintf("gage: removed %q from the recipient list", res.Replaced))
	}
	if res.NewRecoveryPubkey != "" {
		lines = append(lines, fmt.Sprintf("gage: new recovery key is %s", res.NewRecoveryPubkey))
	}
	lines = append(lines, fmt.Sprintf("gage: re-encrypted %d %s", res.Reencrypted, plural(res.Reencrypted, "entry", "entries")))
	return lines
}

func warnNoRecoveryKey(app *App, vault string) {
	writeOut(app.Err, []string{
		fmt.Sprintf("gage: %q now has no recovery key. This device's identity file is the only", vault),
		"gage: way in; if it or its passphrase is lost, the vault is unreadable forever.",
		"gage: `gage recovery rotate` adds one.",
	})
}

// identityBackup holds a device's wrapped identity file while a recovery
// enroll replaces it, so a failure part-way through can put back exactly
// what was there.
//
// The file being replaced is usually unopenable — a forgotten passphrase
// is why anyone is here — but not always: rebuilding a machine under its
// own name is legitimate with a perfectly good key on disk, and destroying
// that because the swap failed afterwards would turn a recoverable
// situation into a worse one.
type identityBackup struct {
	vaultID string
	device  string
	path    string
	content []byte
}

func identityBackupFor(v *gage.Vault, device string, replacing bool) (*identityBackup, error) {
	path, err := gage.IdentityFilePath(v.ID, device)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(path) // #nosec G304 -- path comes from IdentityFilePath, which validates both components
	switch {
	case os.IsNotExist(err):
		return &identityBackup{vaultID: v.ID, device: device, path: path}, nil
	case err != nil:
		return nil, exitcode.Wrap(exitcode.Internal, err)
	case !replacing:
		return nil, exitcode.Wrap(exitcode.Usage, fmt.Errorf(
			"%w: %s already exists.\n"+
				"gage: if this machine is being rebuilt under that name, say so with --replaces %s;\n"+
				"gage: otherwise enroll under a different --device NAME",
			ErrLocalIdentityExists, path, device))
	default:
		return &identityBackup{vaultID: v.ID, device: device, path: path, content: content}, nil
	}
}

// clear removes the displaced file so CreateIdentity generates a fresh key
// instead of reusing it and asking for the passphrase nobody has.
func (b *identityBackup) clear() error {
	if b.content == nil {
		return nil
	}
	return gage.RemoveIdentity(b.vaultID, b.device)
}

// restoreAfter puts the displaced file back and returns cause, so a caller
// can `return backup.restoreAfter(err)` on every failure path.
func (b *identityBackup) restoreAfter(cause error) error {
	if b.content == nil {
		return cause
	}
	// The directory has to be remade first. RemoveIdentity prunes the
	// marker and the now-empty identities/<vault-id>/ alongside the key, so
	// on a machine holding only this identity the parent is gone by now —
	// and atomicfile.WriteFile does not create it, it writes a temp file
	// beside the target. Without this the restore fails with ENOENT and
	// destroys the very file this type exists to protect.
	if err := os.MkdirAll(filepath.Dir(b.path), identityDirMode); err != nil {
		return exitcode.Newf(exitcode.Internal,
			"gage: %v (and restoring the identity file at %s failed: %v)", cause, b.path, err)
	}
	if err := atomicfile.WriteFile(b.path, b.content, identityFileMode); err != nil {
		return exitcode.Newf(exitcode.Internal,
			"gage: %v (and restoring the identity file at %s failed: %v)", cause, b.path, err)
	}
	return cause
}

// identityFileMode and identityDirMode are what CreateIdentity writes a
// wrapped identity and its directory with, repeated here for the restore
// path. The directory is 0700 for the same reason entries/ is: its listing
// alone says which devices this machine holds keys for.
const (
	identityFileMode = 0o600
	identityDirMode  = 0o700
)

// checkDeviceNameFree refuses a device name the vault already lists,
// before anything is asked for.
//
// The swap would refuse it too, but only from inside RecoverDevice — after
// the key has been pasted and a new passphrase chosen twice. Both are
// avoidable for a mistake that is visible in a plaintext file this command
// has already read.
//
// The name being replaced is the exception, and the common one: rebuilding
// a machine under the name it always had.
func checkDeviceNameFree(v *gage.Vault, device, replaces string) error {
	if device == replaces {
		return nil
	}
	rs, err := v.Recipients()
	if err != nil {
		return err
	}
	for _, r := range rs {
		if r.Device != device {
			continue
		}
		return exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
			"%w: %q is already a recipient of vault %q.\n"+
				"gage: if this machine is being rebuilt under that name, say so with --replaces %s;\n"+
				"gage: otherwise enroll under a different --device NAME",
			gage.ErrRecipientExists, device, v.Name, device))
	}
	return nil
}

// newRecoveryRotateCommand builds `gage recovery rotate`: replace the
// vault's recovery key from a device that can already read it, or add the
// first one to a vault made without.
//
// It is the answer to two situations with no lost identity to recover
// from: a recovery key that may have leaked, and a vault created with
// --no-recovery-key that wants one after all. The instructions this
// replaces — add a second key, then remove the first — could not work: the
// label is fixed, so the add fails while the old key still holds it.
func newRecoveryRotateCommand(app *App) *cobra.Command {
	var (
		useFlag        string
		recoveryKeyOut string
	)

	cmd := &cobra.Command{
		Use:   "rotate",
		Short: commandShort("recovery rotate"),
		Long: commandShort("recovery rotate") + ".\n\n" +
			"Generates a new recovery key, registers it as the vault's recovery-paper-key,\n" +
			"and retires the old one, re-encrypting every entry in a single commit. The new\n" +
			"key is shown once, exactly as `gage init` shows one; --recovery-key-out writes\n" +
			"it to a file instead of the screen.\n\n" +
			"Use it if a recovery key may have leaked. Retiring it stops it opening anything\n" +
			"written from now on, but it still opens every version already in the vault's\n" +
			"git history, so rotate the secrets themselves too.\n\n" +
			"A vault made with --no-recovery-key has nothing to retire; this simply adds one.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRecoveryRotate(app, useFlag, recoveryKeyOut)
		},
	}

	addUseFlag(cmd, &useFlag)
	cmd.Flags().StringVar(&recoveryKeyOut, "recovery-key-out", "",
		"write the new recovery key to this file (mode 0600) instead of showing it")
	return cmd
}

func runRecoveryRotate(app *App, use, recoveryKeyOut string) error {
	// Before the unlock, so a run with nowhere to show the key never asks
	// for a passphrase. There is no opt-out to name: producing a key is the
	// whole command.
	mode, err := planRecoveryKey(app, "", false, recoveryKeyOut)
	if err != nil {
		return err
	}

	return withUnlockedVault(app, use, func(v *gage.Vault, ident *gage.Identity) error {
		if mode == recoveryKeyFile {
			if err := checkRecoveryKeyOutPath(recoveryKeyOut, v.Path); err != nil {
				return err
			}
		}

		key, err := gage.NewRecoveryKey()
		if err != nil {
			return err
		}
		defer zeroBytes(key.Secret)

		res, err := v.RotateRecoveryKey(key.Pubkey, ident)
		if err != nil {
			return err
		}

		// Held, as init's and enroll's are: the vault has changed and been
		// pushed, so the summary still has to say so.
		deliveryErr := deliverRecoveryKey(app, mode, v.Name, recoveryKeyOut, key)
		writeOut(app.Out, recoveryRotateLines(v.Name, res))
		return deliveryErr
	})
}

func recoveryRotateLines(vault string, res gage.RotateResult) []string {
	lines := []string{}
	if res.Created {
		lines = append(lines, fmt.Sprintf("gage: added a recovery key to vault %q (it had none)", vault))
	} else {
		lines = append(lines, fmt.Sprintf("gage: replaced vault %q's recovery key; retired %s", vault, res.Retired))
	}
	lines = append(lines,
		fmt.Sprintf("gage: new recovery key is %s", res.Pubkey),
		fmt.Sprintf("gage: re-encrypted %d %s", res.Reencrypted, plural(res.Reencrypted, "entry", "entries")))
	return lines
}
