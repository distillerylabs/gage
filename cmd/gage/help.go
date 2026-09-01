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

// commandListLine renders one registry entry's line in the grouped
// listing: canonical name, aliases alongside it, then the description —
// per the M0 test list's "A command's aliases ... resolve to the same
// command and are shown alongside the canonical name rather than as
// separate entries."
func commandListLine(ci CommandInfo) string {
	name := ci.Name
	if len(ci.Aliases) > 0 {
		names := append([]string{ci.Name}, ci.Aliases...)
		name = strings.Join(names, ", ")
	}
	return fmt.Sprintf("%-24s %s", name, ci.Short)
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
	writeOut(w, lines)
}
