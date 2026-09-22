package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/atomicfile"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
)

// recoveryConfirmChars is how many of the key's own last characters init
// asks for back before it considers the key delivered.
//
// Six is an attention check, not an authentication one: enough that it
// cannot be answered without the key in front of you, few enough that
// answering it from a piece of paper is not a chore. Nothing is decided by
// the answer beyond whether init exits cleanly — the key is already the
// vault's recipient by the time the question is asked.
const recoveryConfirmChars = 6

// maxRecoveryConfirmAttempts matches maxPassphraseAttempts, for the same
// reason: a typo deserves another go, an endless loop helps nobody.
const maxRecoveryConfirmAttempts = 3

// recoveryKeyFileMode is the mode --recovery-key-out writes with. The file
// holds an unencrypted private key, so it gets the identity file's mode
// rather than a config file's.
const recoveryKeyFileMode = 0o600

// recoveryKeyMode is what init decided to do about a recovery key, settled
// from the flags before anything is created.
type recoveryKeyMode int

const (
	// recoveryKeyNone is --no-recovery-key: no key is generated at all.
	recoveryKeyNone recoveryKeyMode = iota
	// recoveryKeyShow displays the key on the terminal and asks for it
	// back.
	recoveryKeyShow
	// recoveryKeyFile writes the key to --recovery-key-out instead.
	recoveryKeyFile
)

// planRecoveryKey settles what a command will do about the recovery key it
// is about to generate, and refuses now rather than later if it cannot do
// anything sensible. `init` and `recovery enroll` share it.
//
// The refusal that matters is D2's: by default a key is generated and
// shown, and "shows it" is meaningless without a human watching. A
// piped or redirected init would write an unencrypted private key into
// whatever is collecting the output — a log, a CI transcript, a file
// nobody reads — which is precisely the exposure keeping the key off disk
// exists to avoid. So a run with no terminal and no explicit choice fails,
// naming both flags that resolve it, rather than quietly picking one:
// picking "show anyway" leaks the key, and picking "no key" silently
// creates the unrecoverable vault this whole feature exists to prevent.
//
// It runs before the target directory is touched and before the passphrase
// prompt, so a flag combination that was never going to work costs nobody
// a vault directory or two blind passphrase entries.
//
// skipFlag is the name of the caller's own opt-out flag: `init` spells it
// --no-recovery-key and `recovery enroll` spells it --no-new-recovery-key,
// because one is making a vault's first key and the other is replacing a
// key it just spent. The refusals below have to name the flag the operator
// can actually pass, so it is a parameter rather than a constant.
func planRecoveryKey(app *App, skipFlag string, noRecoveryKey bool, outPath string) (recoveryKeyMode, error) {
	if noRecoveryKey && outPath != "" {
		return recoveryKeyNone, exitcode.Newf(exitcode.Usage,
			"gage: %s and --recovery-key-out are mutually exclusive; the first says not to make a key, the second says where to put one",
			skipFlag)
	}
	if noRecoveryKey {
		return recoveryKeyNone, nil
	}
	if outPath != "" {
		// The destination itself is checked separately, by
		// checkRecoveryKeyOutPath, once init knows where the vault is going.
		return recoveryKeyFile, nil
	}
	// Both streams have to be a terminal, and for two different reasons:
	// the key is written to stderr, and the confirmation is read from
	// stdin. `gage init foo 2>build.log` satisfies one and not the other,
	// and is exactly the run that would file an unencrypted private key
	// into a build log.
	if !app.IsTerminal() || !app.errIsTerminal() {
		// A command with no opt-out (rotate exists to produce a key) passes
		// an empty skipFlag, and must not be told to pass a flag it does not
		// have.
		if skipFlag == "" {
			return recoveryKeyNone, exitcode.New(exitcode.Usage,
				"gage: this generates a recovery key and shows it once, which needs a terminal.\n"+
					"gage: pass --recovery-key-out FILE to write it to a file instead.")
		}
		return recoveryKeyNone, exitcode.Newf(exitcode.Usage,
			"gage: this generates a recovery key and shows it once, which needs a terminal.\n"+
				"gage: pass --recovery-key-out FILE to write it to a file instead, or %s to\n"+
				"gage: go without one (which leaves this device's identity file as the only way\n"+
				"gage: in — if it or its passphrase is lost, the vault is unreadable forever).",
			skipFlag)
	}
	return recoveryKeyShow, nil
}

// checkRecoveryKeyOutPath rejects an output path init could not write, or
// should not. It runs before the vault is created, so everything knowable
// about the destination is known before there is a key to lose.
//
// vaultPath is where this run's vault is about to be created, which is a
// place the key must not go and which does not exist yet to be detected
// any other way.
func checkRecoveryKeyOutPath(path, vaultPath string) error {
	// First, because it is the most specific reason to refuse and the only
	// one that holds whether or not anything on the way exists — the vault
	// being created does not, so the plain "cannot write" below would
	// otherwise answer for it and say nothing useful.
	if err := checkRecoveryKeyOutIsNotGages(path, vaultPath); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return exitcode.Newf(exitcode.Usage,
			"gage: %s already exists; refusing to overwrite it with a new recovery key", path)
	} else if !os.IsNotExist(err) {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return exitcode.Newf(exitcode.Usage, "gage: cannot write %s: %v", path, err)
	}
	if !info.IsDir() {
		return exitcode.Newf(exitcode.Usage, "gage: cannot write %s: %s is not a directory", path, dir)
	}
	return checkDirIsWritable(path, dir)
}

// checkRecoveryKeyOutIsNotGages refuses a destination gage manages, because
// gage deletes those.
//
// Inside a vault is the sharp one: the next write after an interrupted one
// resets the working tree (resetDirtyWorkTree → gitrepo.ResetHard), which
// removes untracked files. A recovery key filed there is a key gage itself
// deletes, reporting it as discarding an interrupted write — the user's
// only copy of an unencrypted private key, gone under a message about
// something else. Under $GAGE_DATA is the blunter one: that tree is gage's
// to rewrite, and scripts/resetlocalstate exists to delete all of it.
//
// Both are refused rather than warned about. The flag's whole purpose is
// to put the key somewhere it will survive, and neither of these is that.
func checkRecoveryKeyOutIsNotGages(path, vaultPath string) error {
	if inside, err := pathIsInside(path, vaultPath); err != nil {
		return err
	} else if inside {
		return exitcode.Newf(exitcode.Usage,
			"gage: %s is inside the vault being created; gage resets a vault's working tree after an\n"+
				"gage: interrupted write, which would delete the key. Write it outside any vault.", path)
	}

	// Any other vault, found the way a vault is recognizable from the
	// outside, so this catches registered and unregistered ones alike.
	//
	// The walk starts from an absolute path, and that is load-bearing
	// rather than tidiness: filepath.Dir(".") is ".", so a relative
	// destination would walk its own prefix forever-ish, never reach the
	// real ancestors, find no .gage/config.toml, and be allowed — landing
	// the key in precisely the directory the next reset deletes from. A
	// bare `--recovery-key-out recovery.key` run from inside a vault is
	// the likely spelling, not an exotic one.
	start, err := filepath.Abs(path)
	if err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}
	for dir := filepath.Dir(start); ; {
		if _, err := os.Stat(filepath.Join(dir, ".gage", "config.toml")); err == nil {
			return exitcode.Newf(exitcode.Usage,
				"gage: %s is inside the vault at %s; gage resets a vault's working tree after an\n"+
					"gage: interrupted write, which would delete the key. Write it outside any vault.", path, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	dataDir, err := xdgpaths.DataDir()
	if err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	if inside, err := pathIsInside(path, dataDir); err != nil {
		return err
	} else if inside {
		return exitcode.Newf(exitcode.Usage,
			"gage: %s is inside %s, which gage manages and rewrites; a key there is not safely\n"+
				"gage: stored. Write it outside gage's own directories.", path, dataDir)
	}
	return nil
}

// pathIsInside reports whether path is root or sits beneath it, comparing
// the two after symlinks are resolved as far as they exist — on macOS
// /var is a symlink to /private/var, so a textual comparison of a temp
// path against a resolved one silently answers no.
func pathIsInside(path, root string) (bool, error) {
	if root == "" {
		return false, nil
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, exitcode.Wrap(exitcode.Usage, err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false, exitcode.Wrap(exitcode.Usage, err)
	}
	absPath, absRoot = resolveExisting(absPath), resolveExisting(absRoot)

	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		// Different volumes on Windows: not inside, and not an error.
		return false, nil
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

// resolveExisting resolves symlinks in the deepest existing ancestor of
// path and reattaches the rest, so a not-yet-created file still compares
// against a resolved root.
func resolveExisting(path string) string {
	rest := ""
	for dir := path; ; {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// checkDirIsWritable proves the destination directory accepts a file, the
// only way to know short of writing the real one: a read-only or full
// destination stats perfectly well and fails at the write. By then the
// vault exists and the key it is encrypted to is the one being lost.
func checkDirIsWritable(path, dir string) error {
	probe, err := os.CreateTemp(dir, ".gage-write-probe-*")
	if err != nil {
		return exitcode.Newf(exitcode.Usage, "gage: cannot write %s: %v", path, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// deliverRecoveryKey hands the generated key to its human, by whichever
// route planRecoveryKey settled on.
//
// It is called only once Create has succeeded and the vault is registered:
// until then there is nothing the key opens, and a key shown for a vault
// that then failed to exist is a real secret a user now has to decide what
// to do with. The flip side is that a failure in here cannot unmake the
// vault — see confirmRecoveryKeySaved.
func deliverRecoveryKey(app *App, mode recoveryKeyMode, vaultName, outPath string, key gage.RecoveryKey) error {
	switch mode {
	case recoveryKeyFile:
		return writeRecoveryKeyFile(app, vaultName, outPath, key)
	case recoveryKeyShow:
		return showRecoveryKey(app, vaultName, key)
	default:
		return nil
	}
}

// zeroBytes overwrites b in place. It is cmd/gage's counterpart to the
// library's own zero: secret material this layer builds for rendering —
// the bytes written to --recovery-key-out, a pasted key read back from a
// prompt — is this layer's to erase.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// showRecoveryKey prints the key, its QR, and the custody rules that are
// the only protection an unencrypted key has, then asks for it back.
//
// Everything here goes to app.Err rather than app.Out, with the prompts:
// stdout is where gage puts a value someone asked for and may be
// redirecting, and a recovery key is not that — it is a one-time notice
// the human in front of the terminal has to act on. See D5.
func showRecoveryKey(app *App, vaultName string, key gage.RecoveryKey) error {
	// The conversion is a copy Go will not let us zero, the same gap
	// Identity documents for age's own parsed key. It is bounded by this
	// function: nothing keeps a reference once it returns.
	secret := string(key.Secret)

	writeOut(app.Err, []string{
		"",
		fmt.Sprintf("gage: %q now has a recovery key. This is the only time it will be shown.", vaultName),
		"",
		"    " + secret,
		"",
	})
	// A failed QR is not a failed delivery: the key is already on screen,
	// and abandoning the run here would skip the custody warnings and the
	// confirmation, which are the parts that matter. (Nothing realistic
	// fails — renderQR rejects only an empty or oversized value, and a key
	// is neither — but "already printed" makes the choice obvious.)
	if err := renderQR(app.Err, secret); err != nil {
		app.Prompter.Warn(fmt.Sprintf("gage: could not draw the key as a QR code: %s", errorClause(err)))
	}
	writeOut(app.Err, []string{
		"",
		"gage: this key is not encrypted, and anyone who has it can read every secret in",
		"gage: this vault. gage keeps no copy, so it cannot be shown again.",
		"gage:",
		"gage: store it offline: print it on a printer you trust, or write it out by hand,",
		"gage: and keep the paper somewhere you would keep a passport. An offline encrypted",
		"gage: drive works too. Do not put it in cloud-synced notes, a password manager,",
		"gage: email, or a photo. Clear this terminal's scrollback when you are done.",
		"",
	})

	return confirmRecoveryKeySaved(app, secret)
}

// confirmRecoveryKeySaved asks the human to type the key's last characters
// back, which they cannot do without having it in front of them.
//
// Exhausting the attempts fails the command but changes nothing: the vault
// exists, is registered, and has this key as a recipient by the time the
// question is asked. That is deliberate — unmaking a perfectly good vault
// because someone fumbled a prompt would be a worse outcome than the one
// this guards against — so the error's job is to be loud about what is now
// true, not to suggest anything was rolled back. The key is not reprinted:
// it is still on screen, and a second copy in the scrollback is one more
// place to have to clear.
func confirmRecoveryKeySaved(app *App, secret string) error {
	want := secret[len(secret)-recoveryConfirmChars:]
	prompt := fmt.Sprintf("Type the last %d characters of the recovery key to confirm you have saved it: ",
		recoveryConfirmChars)

	for attempt := 1; attempt <= maxRecoveryConfirmAttempts; attempt++ {
		got, err := app.Prompter.Value(prompt)
		if err != nil {
			return exitcode.Wrap(exitcode.LockedOrAuth, err)
		}
		if strings.EqualFold(strings.TrimSpace(got), want) {
			return nil
		}
		if attempt < maxRecoveryConfirmAttempts {
			app.Prompter.Warn("gage: that doesn't match the key above; try again.")
		}
	}

	return exitcode.New(exitcode.Conflict,
		"gage: the recovery key was not confirmed.\n"+
			"gage: the change is committed and that key is one of this vault's recipients, so it\n"+
			"gage: is worth saving from the screen above — gage kept no copy and it cannot be shown\n"+
			"gage: again. If it is already gone, `gage recovery rotate` replaces it with a new one.")
}

// writeRecoveryKeyFile is --recovery-key-out: the key goes to a file
// instead of a screen, for the runs that have no screen.
//
// There is no confirmation here. The prompt exists to make a human look at
// something transient; a file is not transient, and there is nobody to ask
// in the situation this flag is for. What replaces it is saying plainly
// what is now sitting on this disk.
func writeRecoveryKeyFile(app *App, vaultName, path string, key gage.RecoveryKey) error {
	// The trailing newline makes the file `age -i` reads directly, and what
	// a shell tool expects of a one-line key file.
	data := make([]byte, 0, len(key.Secret)+1)
	data = append(data, key.Secret...)
	data = append(data, '\n')
	defer zeroBytes(data)

	if err := atomicfile.WriteFile(path, data, recoveryKeyFileMode); err != nil {
		// The path was checked before the vault was created, so reaching
		// here means the disk changed under us. The vault is fine and this
		// device can still open it; what is gone is the second way in, and
		// the fix is to make another one rather than to start over.
		// Deliberately says only what is true of both callers: `init` has
		// just created the vault and `recovery enroll` has just changed an
		// existing one, and naming the wrong one mid-recovery would tell
		// the user about an operation they never ran.
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf(
			"gage: %q's recipient list is committed, but writing its recovery key to %s failed: %w\n"+
				"gage: that key is lost — `gage recovery rotate` replaces it with a new one",
			vaultName, path, err))
	}

	writeOut(app.Err, []string{
		fmt.Sprintf("gage: wrote %q's recovery key to %s", vaultName, path),
		"gage: it is not encrypted, and anyone who has it can read every secret in this vault.",
		"gage: gage keeps no other copy. Move it somewhere offline and delete this file.",
	})
	return nil
}
