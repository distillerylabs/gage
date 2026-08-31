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
func renderHelp(w io.Writer, root *cobra.Command) {
	fmt.Fprintln(w, root.Short)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintf(w, "  %s [command]\n", root.Name())
	fmt.Fprintln(w)

	for _, group := range groupedOneShotCommands() {
		fmt.Fprintf(w, "%s:\n", group.Group)
		for _, ci := range group.Commands {
			fmt.Fprintf(w, "  %s\n", commandListLine(ci))
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, `Use "gage help <command>" (or "gage <command> --help") for more information about a command.`)
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
	if cmd.Long != "" {
		fmt.Fprintln(w, cmd.Long)
	} else if cmd.Short != "" {
		fmt.Fprintln(w, cmd.Short)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintf(w, "  %s\n", cmd.UseLine())

	if len(cmd.Aliases) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Aliases:")
		names := append([]string{cmd.Name()}, cmd.Aliases...)
		sort.Strings(names)
		fmt.Fprintf(w, "  %s\n", strings.Join(names, ", "))
	}
}
