package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// installHelp wires both help surfaces described in the design doc's
// "Help" section: `gage --help`/`gage help` (equivalent, one-shot-visible
// commands only) and `gage help <command>`/`<command> --help` (that
// command's own usage). Both render from the registry, never a
// hand-written list — see Q-HELP-SURFACES.
func installHelp(app *App, root *cobra.Command) {
	// --help on root and --help on any subcommand: root gets the custom
	// grouped listing, subcommands get their own usage. HelpFunc is
	// inherited down the command tree, so one function has to handle
	// both cases — Cobra doesn't give us a way to set it on root only.
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd == root {
			renderHelp(app.Out, root)
			return
		}
		renderCommandHelp(app.Out, cmd)
	})

	// Replace Cobra's default help command with our own: unlike the
	// default (which never exits nonzero for an unknown topic — see
	// InitDefaultHelpCmd in Cobra's source), `gage help <unknown>` must
	// fail with the usage exit code.
	root.SetHelpCommand(&cobra.Command{
		Use:                   "help [command]",
		Short:                 "Help about any command",
		Hidden:                true,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				renderHelp(app.Out, root)
				return nil
			}
			target, _, err := root.Find(args)
			if err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}
			if target == root {
				return exitcode.Newf(exitcode.Usage, "unknown help topic %q", strings.Join(args, " "))
			}
			renderCommandHelp(app.Out, target)
			return nil
		},
	})
}

// renderHelp is gage --help / gage help's shared rendering — the two
// spellings must produce byte-identical output (per the M0 test list),
// so both call this one function rather than each having their own
// rendering path.
//
// The text is assembled first and written once. That keeps the rendering
// itself free of I/O (and so of error handling), and leaves exactly one
// place where a write can fail — see writeOut for why that failure is
// deliberately dropped.
func renderHelp(w io.Writer, root *cobra.Command) {
	lines := []string{
		root.Short,
		"",
		"Usage:",
		fmt.Sprintf("  %s [command]", root.Name()),
		"",
	}

	for _, group := range groupedOneShotCommands() {
		lines = append(lines, group.Group+":")
		for _, ci := range group.Commands {
			lines = append(lines, "  "+commandListLine(ci))
		}
		lines = append(lines, "")
	}

	lines = append(lines, `Use "gage help <command>" (or "gage <command> --help") for more information about a command.`)
	writeOut(w, lines)
}

// writeOut joins rendered help lines and writes them in one call.
//
// The write error is dropped on purpose: the only realistic failure is a
// closed pipe (`gage --help | head`), Cobra's HelpFunc signature has no
// error return to propagate it through anyway, and the process is about
// to exit. Dropping it in one audited place is honest; threading an
// unreportable error through every render function would not be.
func writeOut(w io.Writer, lines []string) {
	_, _ = io.WriteString(w, strings.Join(lines, "\n")+"\n")
}

// errorPrefix is how gage names itself in front of anything it reports.
const errorPrefix = "gage: "

// writeError reports one error to a human, with gage's name in front of
// it exactly once.
//
// Library errors carry the prefix in their own text, and that is not
// incidental: Prompter.Warn messages are printed verbatim, so a warning
// can only name gage by saying so itself, and errors follow the same
// convention. A printer that also prepends unconditionally is what turns
// that into "gage: gage: ...". Anything that does arrive unprefixed —
// Cobra's own usage rejections, an error straight from the standard
// library — still gets one, so every line gage prints looks the same.
//
// This is the single place errors are rendered, so the rule holds for
// one-shot mode and the REPL alike rather than at each print site's
// discretion.
func writeError(w io.Writer, err error) {
	_, _ = io.WriteString(w, prefixOnce(err.Error())+"\n")
}

// prefixOnce adds errorPrefix to a message that doesn't already open with
// it.
func prefixOnce(msg string) string {
	if strings.HasPrefix(msg, errorPrefix) {
		return msg
	}
	return errorPrefix + msg
}

// errorClause renders an error for use *inside* a larger sentence,
// dropping the "gage: " it carries for standalone reporting. Without it a
// composed message reads "gage: continuing without X: gage: opening Y
// failed" — the same doubling writeError avoids, one clause further in.
func errorClause(err error) string {
	return strings.TrimPrefix(err.Error(), errorPrefix)
}

// renderSessionHelp is in-session `help`: the same grouped listing, from
// the same registry, filtered to what a session can actually run — the
// session-only meta-verbs plus every vault-domain command available in a
// session (Q-CMD-AVAILABILITY). Vault selection appears here as the bare
// `use <vault>` command rather than the -u|--use flag, which belongs in
// per-command help (Q-HELP-SURFACES); that's why the listing renders
// UsageName rather than the bare name.
func renderSessionHelp(w io.Writer) {
	lines := []string{
		"gage session commands",
		"",
	}

	for _, group := range groupedSessionCommands() {
		lines = append(lines, group.Group+":")
		for _, ci := range group.Commands {
			lines = append(lines, "  "+commandListLine(ci))
		}
		lines = append(lines, "")
	}

	lines = append(lines, `Type "help <command>" for more information about a command.`)
	writeOut(w, lines)
}

// commandListLine renders one registry entry's line in the grouped
// listing: canonical name (with its argument syntax, if it declares
// any), aliases alongside it, then the description — per the M0 test
// list's "A command's aliases ... resolve to the same command and are
// shown alongside the canonical name rather than as separate entries."
func commandListLine(ci CommandInfo) string {
	name := ci.UsageName()
	if len(ci.Aliases) > 0 {
		names := append([]string{name}, ci.Aliases...)
		name = strings.Join(names, ", ")
	}
	// Two literal spaces, not one, between the padded name field and the
	// description — %-24s alone guarantees a minimum width but not a
	// minimum *gap*: a name (aliases joined) 24 characters or longer
	// gets no padding at all, which would leave name and description
	// separated by a single space with no reliable boundary between
	// them. The extra space keeps a real "run of 2+ spaces" boundary
	// for any name length, which is what test code parses on (see
	// nameFieldBoundary in help_test.go).
	return fmt.Sprintf("%-24s  %s", name, ci.Short)
}

// renderCommandHelp is the "gage help <command>" / "<command> --help"
// surface: one command's usage, not the full grouped listing.
func renderCommandHelp(w io.Writer, cmd *cobra.Command) {
	var lines []string
	switch {
	case cmd.Long != "":
		lines = append(lines, cmd.Long)
	case cmd.Short != "":
		lines = append(lines, cmd.Short)
	}
	lines = append(lines, "", "Usage:", "  "+cmd.UseLine())

	if len(cmd.Aliases) > 0 {
		names := append([]string{cmd.Name()}, cmd.Aliases...)
		sort.Strings(names)
		lines = append(lines, "", "Aliases:", "  "+strings.Join(names, ", "))
	}

	// Flags belong in per-command help specifically, and this is where
	// -u|--use surfaces: the design doc keeps it out of the top-level
	// session listing (which leads with bare `use <vault>`) while
	// documenting that it still works ad hoc on any command — see
	// Q-HELP-SURFACES. FlagUsages already ends in a newline per flag, so
	// it's trimmed rather than appended to as a line.
	if usages := strings.TrimRight(cmd.LocalFlags().FlagUsages(), "\n"); usages != "" {
		lines = append(lines, "", "Flags:", usages)
	}
	writeOut(w, lines)
}
