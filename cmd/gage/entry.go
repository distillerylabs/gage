package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// addUseFlag wires the one-shot -u|--use NAME flag every entry command
// takes: resolved against global config's registered vaults, falling
// back to `current` when omitted — the one-shot counterpart to `vault
// set-default`.
func addUseFlag(cmd *cobra.Command, use *string) {
	cmd.Flags().StringVarP(use, "use", "u", "", "vault to operate on (default: the current vault)")
}

// resolveVaultEntry picks which vault an entry command targets — use if
// given, else global config's current — and looks up its registration.
// It fails before any prompt or decryption happens, so an unregistered
// --use name (or no current vault) is reported without ever asking for a
// passphrase.
func resolveVaultEntry(use string) (name string, entry config.VaultEntry, err error) {
	g, err := readGlobalConfig()
	if err != nil {
		return "", config.VaultEntry{}, exitcode.Wrap(exitcode.Internal, err)
	}
	name = use
	if name == "" {
		name = g.Current
	}
	if name == "" {
		return "", config.VaultEntry{}, exitcode.New(exitcode.Usage,
			"gage: no vault given (--use) and no current vault is set")
	}
	entry, ok := g.Vaults[name]
	if !ok {
		return "", config.VaultEntry{}, exitcode.Newf(exitcode.NotFound, "gage: no such vault %q", name)
	}
	return name, entry, nil
}

// withUnlockedVault is every entry command's one-shot handler: resolve
// --use, Unlock, run fn, Close the Identity — guaranteed on every exit
// path, including fn's own error return. Every entry command goes
// through this rather than repeating the Unlock/defer-Close pair, so
// "Close always runs" is structural rather than a convention each
// command has to remember (see the M4 plan's Unlock -> use -> Close
// contract, which M6's Session reuses unchanged).
func withUnlockedVault(app *App, use string, fn func(v *gage.Vault, ident *gage.Identity) error) error {
	name, entry, err := resolveVaultEntry(use)
	if err != nil {
		return err
	}
	v := &gage.Vault{Name: name, Path: entry.Path}
	ident, err := v.Unlock(app.Prompter)
	if err != nil {
		return err
	}
	defer func() { _ = ident.Close() }()
	return fn(v, &ident)
}

// newInsertCommand builds `gage insert`, the first (and so far only)
// entry-creating command. See "Entry CRUD" and the M4 plan's notes on
// insert's mutually exclusive input modes.
func newInsertCommand(app *App) *cobra.Command {
	var (
		useFlag         string
		descriptionFlag string
		multilineFlag   bool
		valueStdinFlag  bool
		forceFlag       bool
	)

	cmd := &cobra.Command{
		Use:   "insert <title>",
		Short: commandShort("insert"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := args[0]

			// Validated before any I/O — prompt, read, or unlock.
			if multilineFlag && valueStdinFlag {
				return exitcode.New(exitcode.Usage,
					"gage: -m/--multiline and --value-stdin are mutually exclusive")
			}

			// Unlock -> use -> Close, per the one-shot handler contract:
			// prove access before asking the human to type the new
			// secret, so a failed unlock never makes them type it for
			// nothing.
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				value, err := resolveInsertValue(app, title, multilineFlag, valueStdinFlag)
				if err != nil {
					return err
				}

				now := gage.NewTimestamp(time.Now())
				e := gage.Entry{
					Title:       title,
					Description: descriptionFlag,
					Created:     now,
					Updated:     now,
					UpdatedBy:   ident.Device(),
					Value:       value,
				}
				id, err := v.Insert(e, forceFlag, ident)
				if err != nil {
					return err
				}

				writeOut(app.Out, []string{fmt.Sprintf("gage: inserted %q (%s)", title, id)})
				return nil
			})
		},
	}

	addUseFlag(cmd, &useFlag)
	cmd.Flags().StringVar(&descriptionFlag, "description", "", "optional description")
	cmd.Flags().BoolVarP(&multilineFlag, "multiline", "m", false,
		"read the value as multiple lines from the terminal until EOF")
	cmd.Flags().BoolVar(&valueStdinFlag, "value-stdin", false,
		"read the value verbatim from stdin (one trailing newline trimmed)")
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "allow inserting a duplicate title")
	return cmd
}

// resolveInsertValue gets insert's value from whichever of the three
// input modes applies: --value-stdin and -m/--multiline both read app.In
// to EOF (piped/redirected in one-shot mode), differing only in whether a
// single trailing newline is trimmed; with neither given, it prompts once
// through the Prompter rather than reading stdin directly.
func resolveInsertValue(app *App, title string, multiline, valueStdin bool) (string, error) {
	switch {
	case valueStdin:
		data, err := io.ReadAll(app.In)
		if err != nil {
			return "", exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: reading value from stdin: %w", err))
		}
		return trimOneTrailingNewline(string(data)), nil
	case multiline:
		data, err := io.ReadAll(app.In)
		if err != nil {
			return "", exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: reading multiline value: %w", err))
		}
		return string(data), nil
	default:
		return app.Prompter.Value(fmt.Sprintf("Value for %q: ", title))
	}
}

// trimOneTrailingNewline strips exactly one trailing line ending — "\r\n"
// or "\n" — never more, so a value that itself ends in a blank line keeps
// it.
func trimOneTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\r\n") {
		return strings.TrimSuffix(s, "\r\n")
	}
	return strings.TrimSuffix(s, "\n")
}

// newCatCommand builds `gage cat`: always the full decrypted entry, for
// scripting/piping — see "Notes on show" in the design doc for how this
// differs from the (later) `gage show`.
func newCatCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "cat <query>",
		Short: commandShort("cat"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				_, e, err := v.Resolve(args[0], ident)
				if err != nil {
					return err
				}

				data, err := gage.MarshalEntry(e)
				if err != nil {
					return err
				}
				if _, err := app.Out.Write(data); err != nil {
					return exitcode.Wrap(exitcode.Internal, err)
				}
				return nil
			})
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// newRmCommand builds `gage rm`.
func newRmCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "rm <query>",
		Short: commandShort("rm"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				id, err := v.Remove(args[0], ident)
				if err != nil {
					return err
				}
				writeOut(app.Out, []string{fmt.Sprintf("gage: removed %s", id)})
				return nil
			})
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// newLsCommand builds `gage ls`: title first, then a partial (8-hex-char)
// id, sorted by title — see the M4 plan's "Decisions made" on ls output.
func newLsCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: commandShort("ls"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				ids, err := v.EntryIDs()
				if err != nil {
					return err
				}

				type row struct{ title, id string }
				rows := make([]row, 0, len(ids))
				for _, id := range ids {
					e, err := v.ReadEntry(id, ident)
					if err != nil {
						return err
					}
					rows = append(rows, row{title: e.Title, id: shortEntryID(id.String())})
				}
				sort.Slice(rows, func(i, j int) bool { return rows[i].title < rows[j].title })

				if len(rows) == 0 {
					return nil
				}
				lines := make([]string, 0, len(rows))
				for _, r := range rows {
					lines = append(lines, fmt.Sprintf("%s  %s", r.title, r.id))
				}
				writeOut(app.Out, lines)
				return nil
			})
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// shortEntryIDLen is how many leading characters of a UUID's canonical
// string form `ls` prints alongside a title — the same idea as a short
// git commit hash: enough to address an entry (via `cat`/`rm`'s UUID
// path once M5 accepts prefixes; an exact match today) without printing
// all 36 characters on every line.
const shortEntryIDLen = 8

func shortEntryID(id string) string {
	if len(id) <= shortEntryIDLen {
		return id
	}
	return id[:shortEntryIDLen]
}
