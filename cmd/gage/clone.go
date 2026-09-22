package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/config"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
)

// newCloneCommand builds `gage clone`: join a vault that already exists
// somewhere else.
//
// Cloning does not grant access. The vault's ciphertext is encrypted to
// its recipients, and a brand-new device is not one of them — so this
// command's job is to leave you with a working, registered vault *and* to
// say plainly when you can't read it yet, rather than leaving behind a
// directory that silently decrypts nothing. See "Vault lifecycle".
func newCloneCommand(app *App) *cobra.Command {
	var (
		nameFlag   string
		dirFlag    string
		deviceFlag string
	)

	cmd := &cobra.Command{
		Use:   "clone <remote-url>",
		Short: commandShort("clone"),
		Long: commandShort("clone") + ".\n\n" +
			"Cloning gets you the vault's files and history; it does NOT grant you access\n" +
			"to their contents. If this device isn't already a recipient, gage says so and\n" +
			"points at the command that generates a key to be added from a device that is.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClone(app, cloneOptions{
				url:    args[0],
				name:   nameFlag,
				dir:    dirFlag,
				device: deviceFlag,
			})
		},
	}

	cmd.Flags().StringVar(&nameFlag, "name", "", "local name for the vault (default: inferred from the remote URL)")
	cmd.Flags().StringVar(&dirFlag, "dir", "", "target directory (default: $GAGE_DATA/vaults/<name>)")
	cmd.Flags().StringVar(&deviceFlag, "device", "", "this device's name (default: normalized hostname)")
	return cmd
}

type cloneOptions struct {
	url    string
	name   string
	dir    string
	device string
}

func runClone(app *App, opt cloneOptions) error {
	name := opt.name
	if name == "" {
		inferred, err := vaultNameFromURL(opt.url)
		if err != nil {
			return err
		}
		name = inferred
	}
	if err := checkVaultName(name); err != nil {
		return err
	}

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
	if _, exists := g.Vaults[name]; exists {
		return exitcode.Newf(exitcode.Conflict,
			"gage: %q is already a registered vault; pass --name to clone it under a different name", name)
	}

	path := opt.dir
	if path == "" {
		vaultsDir, err := gage.VaultsDir()
		if err != nil {
			return exitcode.Wrap(exitcode.Internal, err)
		}
		path = filepath.Join(vaultsDir, name)
	}

	ctx, cancel := syncContext()
	defer cancel()
	if err := gitrepo.Clone(ctx, opt.url, path); err != nil {
		return err
	}

	// Read the clone's own config before registering anything. An
	// unrecognized format_version means this gage is too old to operate
	// on the vault safely, and registering it anyway would trade one
	// clear failure now for a confusing one on every later command.
	vc, err := readVaultConfig(path)
	if err != nil {
		// The directory is this command's own work, seconds old, and
		// leaving it behind would block the retry that follows fixing the
		// problem — the same rollback `gage init` does for an identity it
		// generated before failing.
		if rmErr := os.RemoveAll(path); rmErr != nil {
			return exitcode.Newf(exitcode.Internal,
				"gage: %v (and removing the partial clone at %s failed: %v)", err, path, rmErr)
		}
		return err
	}

	if g.Vaults == nil {
		g.Vaults = map[string]config.VaultEntry{}
	}
	g.Vaults[name] = config.VaultEntry{
		Path: path,
		// Copied from the config this clone just fetched — no ordering
		// problem here, unlike `init`: the vault already exists and
		// already carries its id, and this file has just been read to
		// register the vault at all.
		//
		// Pubkey is deliberately *not* set. A clone holds no identity, and
		// it refuses to unlock in order to derive one (see accessLines) —
		// so there is no public key to record. A freshly cloned vault
		// legitimately lands in removeOrphanedIdentity's "pubkey missing,
		// so keep the file and say why" branch until `identity add` or
		// enrollment puts a key here.
		ID:     vc.Vault.ID,
		Type:   vc.Vault.Type,
		Device: device,
		// The cloned vault's default method is a suggestion for devices
		// joining it, not a constraint on this one (Q-METHOD-SCOPE) — it's
		// recorded as what a subsequent `gage identity add` will offer.
		Method: vc.Method.Default,
		Git:    config.GitMeta{Origin: opt.url},
	}
	if g.Current == "" {
		g.Current = name
	}
	if err := writeGlobalConfig(g); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}

	writeOut(app.Out, []string{fmt.Sprintf("gage: cloned %q to %s", name, path)})
	return offerEnrollment(app, &gage.Vault{Name: name, ID: vc.Vault.ID, Path: path}, device)
}

// offerEnrollment is what happens after a successful clone when this
// device can't read the vault: interactively, an offer to enroll;
// otherwise the message saying so.
//
// There is deliberately no --enroll flag. clone has always detected this
// exact condition and already reports it, so a flag opting into acting
// on a fact the tool just printed would carry no information — the only
// people who would pass it are the ones who least need it. It is a
// prompt rather than an automatic action because enrollment commits and
// pushes: silently turning "fetch a copy" into "fetch a copy and publish
// a request naming this machine" would widen what clone does in a way
// its name doesn't suggest. See "Why there is no `--enroll` flag".
func offerEnrollment(app *App, v *gage.Vault, device string) error {
	lines := accessLines(v.ID, v.Name, device)
	if lines == nil {
		// This device already holds an identity for the vault. It was set
		// up deliberately and gage can't cheaply tell whether it is
		// already a recipient, so it is left alone — no message, and
		// certainly no prompt. See "What 'not a recipient yet' actually
		// means".
		return nil
	}
	if !canAnswerNewPassphrase(app) {
		writeOut(app.Out, lines)
		return nil
	}

	yes, err := app.Prompter.ConfirmDefaultYes(
		fmt.Sprintf("This device holds no identity for %q, so it can't read anything here yet.\n"+
			"Set up an enrollment request now?", v.Name))
	if err != nil {
		return err
	}
	if !yes {
		writeOut(app.Out, lines)
		return nil
	}
	// Under the device name clone already resolved, not one derived a
	// second time: `clone --device X` followed by a `y` must publish a
	// request naming X.
	return enrollDevice(app, v, device, gage.DefaultEnrollmentTTL)
}

// canAnswerNewPassphrase reports whether a brand-new identity's
// passphrase can be answered in this run, which is the condition clone's
// offer is gated on — one rule rather than a TTY test of its own.
//
// Enrolling on a device with no identity requires a PurposeCreate
// exchange, and scriptPrompter already decides where those can be
// answered: a human when --script FILE runs at a terminal, and nowhere
// at all under --stdin or with no terminal, where noCreatePrompter
// refuses. Offering to enroll in a context that must then refuse the
// passphrase would be asking a question whose only outcome is a failure.
//
// It is the same expression scriptPrompter computes as canPrompt, named
// here for what it means to a caller that is not a session.
func canAnswerNewPassphrase(app *App) bool {
	return !app.ScriptStdin && app.IsTerminal()
}

// accessLines says whether this device can actually read what it just
// cloned.
//
// The question is answered from whether this device has a wrapped
// identity for the vault at all, which needs no passphrase to check —
// asking someone to unlock during a clone, only to tell them the unlock
// was pointless, would be exactly backwards. A device with no identity
// certainly isn't a recipient; one that has an identity was set up
// deliberately and is left alone.
func accessLines(vaultID, name, device string) []string {
	hasIdentity, err := gage.HasIdentity(vaultID, device)
	if err != nil || hasIdentity {
		return nil
	}
	return []string{
		fmt.Sprintf("gage: this device (%q) has no identity for %q, so it cannot decrypt anything in it yet.", device, name),
		"gage: run `gage identity enroll` here to generate a key and publish a request to join,",
		"gage: then give the code it prints to someone who can already read the vault.",
		"gage: or, if this device cannot write to the remote: run `gage identity add` here and",
		"gage: have someone with access add the printed key with `gage recipient add`.",
		"gage: if you hold this vault's recovery key and there is nobody else to ask, run",
		"gage: `gage recovery enroll` — it admits this device and retires the key it used.",
	}
}

// vaultNameFromURL infers a local vault name from a remote URL: the last
// path segment, minus a .git suffix. It handles the three spellings a
// remote can arrive in — https://host/user/repo.git, git@host:user/repo.git,
// and a plain local path — because they all end the same way.
func vaultNameFromURL(url string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(url), "/")
	if trimmed == "" {
		return "", exitcode.New(exitcode.Usage, "gage: a remote URL is required")
	}
	// Both separators, since a Windows path and a URL can both appear
	// here and only one of them is filepath's idea of a separator.
	trimmed = strings.ReplaceAll(trimmed, `\`, "/")
	// An scp-style remote's path starts after the colon; for a URL there
	// is no colon left this late in the string.
	if _, after, found := strings.Cut(trimmed, ":"); found {
		if !strings.HasPrefix(after, "//") {
			trimmed = after
		}
	}

	base := strings.TrimSuffix(path.Base(trimmed), ".git")
	if base == "" || base == "." || base == "/" {
		return "", exitcode.Newf(exitcode.Usage,
			"gage: cannot infer a vault name from %q; pass --name", url)
	}
	return base, nil
}

// checkVaultName refuses a name that wouldn't stay a single path
// component. It matters more here than for `init`, where the name is
// something a human typed: a cloned vault's name can be *inferred from
// the remote URL*, so it is attacker-influenced input on its way to
// becoming a directory under $GAGE_DATA/vaults.
func checkVaultName(name string) error {
	switch {
	case name == "", name == ".", name == "..":
	case strings.ContainsAny(name, `/\`):
	case strings.ContainsRune(name, 0):
	case name != filepath.Clean(name):
	default:
		return nil
	}
	return exitcode.Newf(exitcode.Usage,
		"gage: %q cannot be used as a vault name; pass --name with a plain name", name)
}
