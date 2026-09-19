package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// newIdentityEnrollCommand builds `gage identity enroll`: `identity add`
// plus publishing.
//
// It is registered under `identity` rather than as a top-level verb
// because it is literally a superset of `identity add` — the same
// CreateIdentity call, with the same create-or-reuse behavior, differing
// only in what happens once the key exists. A command belongs next to
// the command it is a superset of, and the two Shorts are deliberately
// identical up to their second clause so the help listing shows that
// relationship without prose. See D-ENROLL-VERBS.
func newIdentityEnrollCommand(app *App) *cobra.Command {
	var (
		useFlag    string
		deviceFlag string
		ttlFlag    time.Duration
	)

	cmd := &cobra.Command{
		Use:   "enroll",
		Short: commandShort("identity enroll"),
		Long: commandShort("identity enroll") + ".\n\n" +
			"Creates a local identity for this vault if there isn't one already (reusing\n" +
			"it if there is), seals this device's name and public key to a freshly\n" +
			"generated enrollment code, commits the result, pushes it, and prints the\n" +
			"code. Give the code to someone who can already read the vault, over a\n" +
			"channel where they can tell it came from you.\n\n" +
			"Requires git write access. It fetches before it commits and fails outright\n" +
			"if the remote can't be reached — unlike a read, an enrollment request that\n" +
			"was never published accomplishes nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIdentityEnroll(app, useFlag, deviceFlag, ttlFlag)
		},
	}

	addUseFlag(cmd, &useFlag)
	cmd.Flags().StringVar(&deviceFlag, "device", "", "this device's name for the vault (default: normalized hostname)")
	cmd.Flags().DurationVar(&ttlFlag, "ttl", gage.DefaultEnrollmentTTL,
		fmt.Sprintf("how long the request stays open (max %s)", gage.MaxEnrollmentTTL))
	return cmd
}

func runIdentityEnroll(app *App, use, deviceFlag string, ttl time.Duration) error {
	// vaultWithoutUnlocking, not withUnlockedVault: a joining device
	// cannot unlock this vault — that is the entire reason it is
	// enrolling. In a session this routes through
	// Session.VaultWithoutUnlocking, so `identity enroll -u NAME` works
	// against a vault `use` could never have opened.
	v, err := vaultWithoutUnlocking(app, use)
	if err != nil {
		return err
	}

	device, err := deviceNameFor(deviceFlag)
	if err != nil {
		return err
	}

	return enrollDevice(app, v, device, ttl)
}

// enrollDevice is the whole of `identity enroll` below flag parsing, and
// is shared with clone's offer so the two cannot drift — an offer that
// published under a different name, or skipped recording the pubkey,
// would be a second implementation of this command.
func enrollDevice(app *App, v *gage.Vault, device string, ttl time.Duration) error {
	ctx, cancel := syncContext()
	defer cancel()

	req, err := v.Enroll(ctx, device, ttl, app.Prompter)
	if err != nil {
		if errors.Is(err, gage.ErrDeviceNameTaken) {
			// The library's message already names the colliding device.
			// What it can't know is that a *flag* is the way out — which
			// matters most in the case nobody typed a name at all: two
			// machines whose hostnames normalize the same.
			return exitcode.Newf(exitcode.Conflict,
				"%v; pass --device NAME to enroll this machine under a different name", err)
		}
		return err
	}

	// A success that did no work: this device's recorded public key is
	// already a recipient, so there was nothing to ask for. Nothing was
	// generated, so there is no pubkey to record either.
	if !req.Published {
		writeOut(app.Out, []string{
			fmt.Sprintf("gage: this device is already a recipient of %q, so it can read the vault already.", v.Name),
			"gage: nothing was published.",
		})
		return nil
	}

	// The local half of the split `identity add` already uses: the
	// vault-side work is the library's, and recording device/method/
	// pubkey in global config stays here. Done after the push, so a
	// registration only ever describes a request that actually reached
	// the remote.
	if err := recordLocalIdentity(v.Name, device, gage.MethodPassphrase, req.Pubkey); err != nil {
		return err
	}

	writeOut(app.Out, enrollmentLines(v.Name, req))
	return nil
}

// enrollmentLines renders a published request.
//
// The code is labelled at its point of use, and so is the passphrase
// prompt that preceded it (see terminalPrompter.newPassphrase): the two
// are opposites that appear a few lines apart on one screen — one chosen
// by the user and never sent, the other produced by gage and meant to be
// sent — and the failure mode to design against is someone pasting their
// identity passphrase into a chat window because they thought it was the
// code. See "Which secret is which".
func enrollmentLines(vault string, req gage.EnrollmentRequest) []string {
	return []string{
		fmt.Sprintf("gage: this device is %q, public key %s", req.Device, req.Pubkey),
		"",
		fmt.Sprintf("gage: enrollment request published (expires in %s).", humanDuration(time.Until(req.Expires))),
		"",
		fmt.Sprintf("  Enrollment code:  %s", req.Code),
		"",
		"gage: this code is safe to send and is not your passphrase. Give it to someone who",
		fmt.Sprintf("gage: can already read %q, over a channel where they can tell it came from", vault),
		"gage: you — not through this vault's remote. They run:",
		fmt.Sprintf("gage:   gage recipient approve --code %s", req.Code),
		"",
		"gage: then run `gage sync` here.",
	}
}

// humanDuration renders a request's remaining lifetime the way a person
// would say it — "24h", "2h30m" — rather than as time.Duration's own
// "24h0m0s", which reads like a machine timestamp in a line whose whole
// job is to tell someone how long they have.
func humanDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	switch {
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// deviceNameFor resolves this device's name for a vault: the flag if it
// was given, otherwise the normalized hostname. It is the same
// resolution `identity add`, `init` and `clone` perform, factored out
// because enroll is the fourth caller.
func deviceNameFor(deviceFlag string) (string, error) {
	hostname := ""
	if deviceFlag == "" {
		h, err := os.Hostname()
		if err != nil {
			return "", exitcode.Wrap(exitcode.Internal, err)
		}
		hostname = h
	}
	return resolveDeviceName(deviceFlag, hostname)
}
