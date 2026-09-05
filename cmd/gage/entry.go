package main

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
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

// withUnlockedVault is every entry command's handler, in both invocation
// modes, and it is the only place that differs between them.
//
// One-shot: resolve --use, Unlock, run fn, Close the Identity —
// guaranteed on every exit path, including fn's own error return. In a
// session: borrow the Identity the Session already holds for that vault
// (unlocking it there, once, if this is the first command to need it)
// and leave it open, since Lock, the idle timeout, and session exit are
// what close it.
//
// Every entry command goes through this rather than repeating the
// Unlock/defer-Close pair, which is what makes "the same Vault methods
// run in both modes" structural: fn is handed a *gage.Vault and a
// *gage.Identity and cannot tell which mode produced them. See the M4
// plan's Unlock -> use -> Close contract, which M6 reuses unchanged.
func withUnlockedVault(app *App, use string, fn func(v *gage.Vault, ident *gage.Identity) error) error {
	if app.Session != nil {
		v, ident, err := app.Session.Vault(use)
		if err != nil {
			return err
		}
		return fn(v, ident)
	}

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
		editFlag        bool
		forceFlag       bool
	)

	cmd := &cobra.Command{
		Use:   "insert <title>",
		Short: commandShort("insert"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := args[0]

			// Validated before any I/O — prompt, read, or unlock. Extends
			// M4's two-flag check to three modes: at most one of
			// -m/--value-stdin/-e may be given.
			modes := 0
			for _, on := range []bool{multilineFlag, valueStdinFlag, editFlag} {
				if on {
					modes++
				}
			}
			if modes > 1 {
				return exitcode.New(exitcode.Usage,
					"gage: -m/--multiline, --value-stdin, and -e/--edit are mutually exclusive")
			}

			// Both stdin-reading modes read to EOF, and inside a session
			// stdin is the terminal the session itself is reading — so
			// they would consume the rest of it and end the session as a
			// side effect of inserting one entry. Refused with somewhere
			// to go instead: -e/--edit is the in-session way to author a
			// multi-line value, and one-shot mode is where a pipe
			// belongs. Conservative and reversible, the same posture
			// init/clone's one-shot-only rule takes.
			if app.Session != nil && (multilineFlag || valueStdinFlag) {
				return exitcode.New(exitcode.Usage,
					"gage: -m/--multiline and --value-stdin read stdin to EOF, which is this session's own input; use -e/--edit here, or run `gage insert` from your shell")
			}

			// Unlock -> use -> Close, per the one-shot handler contract:
			// prove access before asking the human to type the new
			// secret, so a failed unlock never makes them type it for
			// nothing.
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				if editFlag {
					return runInsertEdit(app, v, ident, title, descriptionFlag, forceFlag)
				}

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
				noteIndexEntry(app, v, id, e)

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
	cmd.Flags().BoolVarP(&editFlag, "edit", "e", false,
		"open a template in $EDITOR to fill in value/fields (and optionally title/description)")
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "allow inserting a duplicate title")
	return cmd
}

// runInsertEdit is insert -e's path: seed a stub Entry (title/description
// pre-filled, value/fields empty) into the shared editYAML round trip,
// then insert whatever comes back — using the *edited* title/description
// for both the saved entry and Insert's own duplicate-title/-f check, per
// the design doc's note that the template lets title itself be edited.
//
// Aborts with nothing written and no commit — the same "empty message
// aborts the commit" convention `git commit` uses — if the file comes
// back unchanged, or changed but with value and fields both still empty
// (title/description-only edits don't count as "something to save").
func runInsertEdit(app *App, v *gage.Vault, ident *gage.Identity, title, description string, force bool) error {
	now := gage.NewTimestamp(time.Now())
	stub := gage.Entry{
		Title:       title,
		Description: description,
		Created:     now,
		Updated:     now,
		UpdatedBy:   ident.Device(),
	}

	edited, unchanged, err := editYAML(stub)
	if err != nil {
		return err
	}
	if unchanged || (edited.Value == "" && len(edited.Fields) == 0) {
		return exitcode.New(exitcode.Usage,
			"gage: insert -e aborted: nothing to save (the file was unchanged, or value and fields were both left empty)")
	}

	stamp := gage.NewTimestamp(time.Now())
	e := gage.Entry{
		Title:       edited.Title,
		Description: edited.Description,
		Created:     stamp,
		Updated:     stamp,
		UpdatedBy:   ident.Device(),
		Value:       edited.Value,
		Fields:      edited.Fields,
		Extra:       edited.Extra,
	}
	id, err := v.Insert(e, force, ident)
	if err != nil {
		return err
	}
	noteIndexEntry(app, v, id, e)
	writeOut(app.Out, []string{fmt.Sprintf("gage: inserted %q (%s)", e.Title, id)})
	return nil
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

// resolveQuery runs the shared resolver and, on an ambiguous result,
// prints the candidate list to stderr before returning the error — the
// one-shot side of "the library returns a candidate list as a value, the
// CLI decides what to do with it" (see the M5 plan and "Addressing
// entries & the metadata index"). One-shot mode has nobody to prompt, so
// listing the candidates and failing is the whole story; M6's session
// mode is what turns the same list into an interactive picker instead.
func resolveQuery(app *App, v *gage.Vault, query string, ident *gage.Identity) (uuid.UUID, gage.Entry, error) {
	if app.Session != nil {
		// Session mode has someone to ask, so the same candidate list
		// becomes a picker instead of a failure — the decision lives on
		// Session (which calls Prompter.Choose), not here.
		id, e, err := app.Session.Resolve(v.Name, query)
		if err != nil {
			return uuid.Nil, gage.Entry{}, err
		}
		return id, e, nil
	}

	id, e, err := v.Resolve(query, ident)
	if err != nil {
		reportAmbiguous(app, err)
		return uuid.Nil, gage.Entry{}, err
	}
	return id, e, nil
}

// mutationQuery is what rm/rename pass to the library methods that
// resolve a query themselves. In one-shot mode that's the typed query,
// unchanged, and a zero Entry (there's no session index for a caller to
// update anyway). In a session it's the id the query resolved to — pre-
// resolving is what lets those two commands prompt on an ambiguous query
// like every other command, rather than being the odd pair that fails
// where `show` asks. The library then re-resolves the id through its
// exact-UUID stage, which can only match the entry just chosen.
//
// The resolved Entry is also returned so rename can build its own
// post-rename metadata for noteIndexEntry without a second decrypt — see
// newRenameCommand.
func mutationQuery(app *App, v *gage.Vault, query string, ident *gage.Identity) (string, gage.Entry, error) {
	if app.Session == nil {
		return query, gage.Entry{}, nil
	}
	id, e, err := resolveQuery(app, v, query, ident)
	if err != nil {
		return "", gage.Entry{}, err
	}
	return id.String(), e, nil
}

// noteIndexEntry tells the session (if any) about e's current
// title/description so its metadata index — if it has already built one
// for this vault — reflects the change without a full rebuild. A no-op
// in one-shot mode, and a no-op inside a session that hasn't indexed
// this vault yet; see gage.Session.NoteEntry.
func noteIndexEntry(app *App, v *gage.Vault, id uuid.UUID, e gage.Entry) {
	if app.Session != nil {
		app.Session.NoteEntry(v.Name, id, e)
	}
}

// forgetIndexEntry is noteIndexEntry's counterpart after `rm`.
func forgetIndexEntry(app *App, v *gage.Vault, id uuid.UUID) {
	if app.Session != nil {
		app.Session.ForgetEntry(v.Name, id)
	}
}

// reportAmbiguous prints err's candidate list to stderr if it wraps
// *gage.AmbiguousQueryError, and is a no-op otherwise. Every
// query-taking command's one-shot handler calls this on a resolve
// failure before returning it — including gage rm/gage rename, whose
// error comes back through Remove/Rename rather than a direct Resolve
// call — so an ambiguous query lists its candidates exactly once
// regardless of which command reached the resolver.
func reportAmbiguous(app *App, err error) {
	var amb *gage.AmbiguousQueryError
	if errors.As(err, &amb) {
		printCandidates(app, amb.List)
	}
}

// printCandidates renders an ambiguous query's candidate list to stderr —
// the design doc's numbered-picker shape, minus the "which one?" prompt
// one-shot mode never asks.
func printCandidates(app *App, list gage.CandidateList) {
	lines := []string{fmt.Sprintf("gage: %q matches %d entries:", list.Query, len(list.Candidates))}
	for i, c := range list.Candidates {
		lines = append(lines, fmt.Sprintf("  %d. %s (%s)", i+1, c.Title, shortEntryID(c.ID)))
	}
	writeOut(app.Err, lines)
}

// newCatCommand builds `gage cat`: always the full decrypted entry, for
// scripting/piping — see "Notes on show" in the design doc for how this
// differs from `gage show`.
func newCatCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "cat <query>",
		Short: commandShort("cat"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				_, e, err := resolveQuery(app, v, args[0], ident)
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

// newShowCommand builds `gage show`: the value field only, not the full
// YAML `gage cat` prints — see "Notes on show" in the design doc.
// --field/-c/-q are deferred to M12.
func newShowCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "show <query>",
		Short: commandShort("show"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				_, e, err := resolveQuery(app, v, args[0], ident)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintln(app.Out, e.Value); err != nil {
					return exitcode.Wrap(exitcode.Internal, err)
				}
				return nil
			})
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// newEditCommand builds `gage edit`: resolve query, run the shared
// editYAML round trip seeded with the decrypted entry, re-stamp
// updated/updated_by (created is never touched — the field is "set once,
// at insert time, and never touched again" regardless of what the file
// comes back showing), and commit. Unlike gage insert -e, edit has no
// "unchanged" abort: it commits every time the file parses, the same way
// `git commit --amend` with no new message still amends.
func newEditCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "edit <query>",
		Short: commandShort("edit"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				id, e, err := resolveQuery(app, v, args[0], ident)
				if err != nil {
					return err
				}

				edited, _, err := editYAML(e)
				if err != nil {
					return err
				}
				edited.Created = e.Created
				edited.Updated = gage.NewTimestamp(time.Now())
				edited.UpdatedBy = ident.Device()

				if err := v.Update(id, edited); err != nil {
					return err
				}
				noteIndexEntry(app, v, id, edited)
				writeOut(app.Out, []string{fmt.Sprintf("gage: updated %q (%s)", edited.Title, shortEntryID(id.String()))})
				return nil
			})
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// newRenameCommand builds `gage rename`: a quick metadata-only edit, no
// $EDITOR — changes only the title.
func newRenameCommand(app *App) *cobra.Command {
	var (
		useFlag   string
		forceFlag bool
	)
	cmd := &cobra.Command{
		Use:   "rename <query> <new-title>",
		Short: commandShort("rename"),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				query, resolved, err := mutationQuery(app, v, args[0], ident)
				if err != nil {
					return err
				}
				id, err := v.Rename(query, args[1], forceFlag, ident)
				if err != nil {
					reportAmbiguous(app, err)
					return err
				}
				// Rename only changes the title and re-stamps
				// updated/updated_by (see Vault.Rename); resolved is
				// otherwise still the pre-rename entry, so patching just
				// those three fields is enough to hand NoteEntry the
				// post-rename metadata without decrypting the vault
				// again to fetch it.
				resolved.Title = args[1]
				resolved.Updated = gage.NewTimestamp(time.Now())
				resolved.UpdatedBy = ident.Device()
				noteIndexEntry(app, v, id, resolved)
				writeOut(app.Out, []string{fmt.Sprintf("gage: renamed to %q (%s)", args[1], shortEntryID(id.String()))})
				return nil
			})
		},
	}
	addUseFlag(cmd, &useFlag)
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "allow renaming to a duplicate title")
	return cmd
}

// newGenerateCommand builds `gage generate`: like insert, but the value
// is drawn from gage.GenerateValue instead of coming from the human.
// -l/--no-symbols customization is deferred to M12.
func newGenerateCommand(app *App) *cobra.Command {
	var (
		useFlag         string
		descriptionFlag string
		forceFlag       bool
	)
	cmd := &cobra.Command{
		Use:   "generate <title>",
		Short: commandShort("generate"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := args[0]
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				value, err := gage.GenerateValue()
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
				noteIndexEntry(app, v, id, e)
				writeOut(app.Out, []string{fmt.Sprintf("gage: generated %q (%s)", title, id)})
				return nil
			})
		},
	}
	addUseFlag(cmd, &useFlag)
	cmd.Flags().StringVar(&descriptionFlag, "description", "", "optional description")
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "allow inserting a duplicate title")
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
				query, _, err := mutationQuery(app, v, args[0], ident)
				if err != nil {
					return err
				}
				id, err := v.Remove(query, ident)
				if err != nil {
					reportAmbiguous(app, err)
					return err
				}
				forgetIndexEntry(app, v, id)
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
//
// In a session this is index-backed (M7): Session.List builds the
// metadata cache on the first ls/show/search per vault and reuses it on
// every one after. One-shot mode has no cache to reuse, so it keeps
// M4's decrypt-every-entry-every-time loop — there is nothing else it
// could do, since one-shot mode is gone before a second call could ever
// benefit from one.
func newLsCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: commandShort("ls"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.Session != nil {
				rows, err := app.Session.List(useFlag)
				if err != nil {
					return err
				}
				writeLsRows(app, rows)
				return nil
			}
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

// writeLsRows renders Session.List's rows in the same "title, short id"
// shape the one-shot path prints, so the two are visually identical.
func writeLsRows(app *App, rows []gage.ListEntry) {
	if len(rows) == 0 {
		return
	}
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("%s  %s", r.Title, shortEntryID(r.ID.String())))
	}
	writeOut(app.Out, lines)
}

// newSearchCommand builds `gage search` (alias `gage grep`): matches
// pattern against every entry's title, description, and decrypted body,
// printing the same "title, short id" rows as ls — never the matched
// text itself, and never the entry's value, so a hit on a secret's own
// content doesn't put that content in scrollback. See the M7 plan's
// "Decisions made".
//
// In a session the title/description half is index-backed exactly like
// ls; the body half always decrypts fresh, on every call, since a
// secret's plaintext is never something this cache holds onto — see
// Session.Search.
func newSearchCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:     "search <pattern>",
		Aliases: []string{"grep"},
		Short:   commandShort("search"),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.Session != nil {
				results, err := app.Session.Search(useFlag, args[0])
				if err != nil {
					return err
				}
				writeSearchResults(app, results)
				return nil
			}
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				results, err := v.Search(args[0], ident)
				if err != nil {
					return err
				}
				writeSearchResults(app, results)
				return nil
			})
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// writeSearchResults renders search/grep's matches the same way ls
// renders its rows: title and a short id, nothing about where the match
// was or what it matched.
func writeSearchResults(app *App, results []gage.SearchResult) {
	if len(results) == 0 {
		return
	}
	lines := make([]string, 0, len(results))
	for _, r := range results {
		lines = append(lines, fmt.Sprintf("%s  %s", r.Title, shortEntryID(r.ID.String())))
	}
	writeOut(app.Out, lines)
}

// newReindexCommand builds `gage reindex`: forces a session's cached
// metadata index to rebuild from scratch, picking up any change to
// entries/ that didn't come through gage itself (a manual `git pull`,
// most commonly). One-shot mode never builds a cache in the first place
// — every command there already decrypts fresh — so there it only
// confirms the vault is actually reachable, and says as much rather than
// silently doing nothing.
func newReindexCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: commandShort("reindex"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.Session == nil {
				if _, _, err := resolveVaultEntry(useFlag); err != nil {
					return err
				}
				writeOut(app.Out, []string{"gage: one-shot mode does not cache metadata; nothing to reindex"})
				return nil
			}
			if err := app.Session.Reindex(useFlag); err != nil {
				return err
			}
			writeOut(app.Out, []string{"gage: index rebuilt"})
			return nil
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
