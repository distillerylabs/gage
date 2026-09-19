package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// editorEnvVar names the environment variable editYAML launches — the
// same one every other editor-invoking tool (git commit, crontab -e)
// honors.
const editorEnvVar = "EDITOR"

// editYAML is the one CLI-layer round trip `gage edit` and `gage insert
// -e` share: render seed as the entry YAML, launch $EDITOR against a
// scratch file, read the result back, and report whether it came back
// byte-identical to what was written. It has no library equivalent by
// design — see "A few decisions worth calling out" in the design doc —
// because a GUI would edit fields directly rather than shelling out to a
// text editor.
//
// The scratch file is removed on every exit path below the point it's
// created — a bad $EDITOR, a non-zero $EDITOR exit, unreadable content,
// content that fails to parse — with its bytes overwritten first as a
// best-effort measure against a plaintext secret surviving in a freed
// disk block. $EDITOR itself is validated *before* the file is ever
// written, so an unset or unusable $EDITOR never causes a scratch file
// holding plaintext to exist in the first place.
//
// unchanged reports whether the file came back exactly as written.
// `gage edit` doesn't use it — it re-stamps and commits regardless of
// whether anything changed, the same as `git commit --amend` with no new
// message. `gage insert -e` uses it as half of its abort condition (see
// the design doc's notes on insert).
func editYAML(seed gage.Entry) (result gage.Entry, unchanged bool, err error) {
	editorPath, args, err := resolveEditor()
	if err != nil {
		return gage.Entry{}, false, err
	}

	template, err := gage.MarshalEntry(seed)
	if err != nil {
		return gage.Entry{}, false, err
	}

	path, err := writeScratchFile(template)
	if err != nil {
		return gage.Entry{}, false, err
	}
	defer func() {
		_ = overwriteThenRemove(path)
	}()

	// #nosec G204 -- editorPath/args come from $EDITOR, which is
	// deliberately user-controlled: launching whatever editor the user
	// configured is the entire point of this function, the same as
	// git/crontab/every other $EDITOR-invoking tool. resolveEditor has
	// already run this through exec.LookPath.
	cmd := exec.Command(editorPath, append(args, path)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		return gage.Entry{}, false, exitcode.Newf(exitcode.Internal, "gage: $EDITOR exited with an error: %v", runErr)
	}

	// #nosec G304 -- path came from writeScratchFile, which created it
	// itself moments ago; nothing here is attacker-controlled input.
	edited, err := os.ReadFile(path)
	if err != nil {
		return gage.Entry{}, false, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: reading the edited scratch file: %w", err))
	}

	e, perr := gage.UnmarshalEntry(edited)
	if perr != nil {
		return gage.Entry{}, false, perr
	}

	return e, bytes.Equal(edited, template), nil
}

// resolveEditor reads and validates $EDITOR before anything touches the
// filesystem: unset, blank, or naming something exec.LookPath can't find
// all fail here, clearly, rather than surfacing later as a mysterious
// exec failure after a scratch file has already been written.
//
// Splitting on whitespace is deliberate and limited: it handles the
// common "$EDITOR=vim" and "$EDITOR=code --wait" shapes without a
// shell-quoting parser gage doesn't otherwise need.
func resolveEditor() (path string, args []string, err error) {
	editor := strings.TrimSpace(os.Getenv(editorEnvVar))
	if editor == "" {
		return "", nil, exitcode.Newf(exitcode.Internal,
			"gage: $%s is not set; gage edit and gage insert -e need it to open a scratch file", editorEnvVar)
	}
	fields := strings.Fields(editor)
	resolved, lookErr := exec.LookPath(fields[0])
	if lookErr != nil {
		return "", nil, exitcode.Newf(exitcode.Internal, "gage: $%s %q is not usable: %v", editorEnvVar, editor, lookErr)
	}
	return resolved, fields[1:], nil
}

// writeScratchFile creates a fresh, exclusively-created 0600 file in
// scratchDir() and writes data to it, returning its path.
// os.CreateTemp's O_EXCL, unpredictable-suffix creation is what makes
// this exclusive rather than merely mode-0600 after the fact.
func writeScratchFile(data []byte) (string, error) {
	f, err := os.CreateTemp(scratchDir(), "gage-edit-*.yaml")
	if err != nil {
		return "", exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: creating scratch file: %w", err))
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = overwriteThenRemove(path)
		return "", exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: writing scratch file: %w", err))
	}
	if err := f.Close(); err != nil {
		_ = overwriteThenRemove(path)
		return "", exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: closing scratch file: %w", err))
	}
	return path, nil
}

// overwriteFile zeroes path's contents in place, keeping its length, so
// the plaintext that was there is no longer readable through the
// filesystem before the file is unlinked. Kept separate from the unlink
// so the overwrite is directly testable — once overwriteThenRemove has
// run, the file is gone and there is nothing left to inspect.
//
// This is a best-effort measure, not a secure-erase guarantee: on a
// copy-on-write or log-structured filesystem the old blocks may survive
// regardless, which is exactly why editYAML prefers a tmpfs-backed
// scratch directory where one can be verified.
func overwriteFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, make([]byte, info.Size()), 0o600)
}

// overwriteThenRemove zeroes a scratch file's contents and then unlinks
// it. Both steps are best-effort on purpose: by the time this runs,
// editYAML is already on its way out (success or failure), and there is
// nothing more useful to do with a failure here than leave the file for
// the OS's normal temp-directory cleanup to eventually reclaim.
func overwriteThenRemove(path string) error {
	_ = overwriteFile(path)
	return os.Remove(path)
}
