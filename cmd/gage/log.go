package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// logTimeFormat is how both log and history stamp a revision: local
// time, to the second, with the offset — enough to correlate a change
// with something the human remembers doing, without pretending to
// sub-second precision git doesn't record.
const logTimeFormat = "2006-01-02 15:04:05 -0700"

// newLogCommand builds `gage log`: when an entry changed, and nothing
// else.
//
// Nothing decrypted reaches this output, and that is the whole point of
// its existing separately from `history --decrypt`. It still needs the
// key, though — resolving a query means decrypting every entry to match
// titles (see "Addressing entries"), which is the cost of entries being
// opaque on disk. The distinction `log` preserves is what it *prints*,
// not what it opens.
func newLogCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "log [QUERY]",
		Short: commandShort("log"),
		Long: commandShort("log") + ".\n\n" +
			"Commit timestamps for one entry. The commit messages themselves are entry\n" +
			"UUIDs and nothing more, so this reveals no titles — see `gage history\n" +
			"--decrypt` for the command that does show past values.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				ids, err := logTargets(app, v, args, ident)
				if err != nil {
					return err
				}
				return writeLog(app, v, ids)
			})
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// logTargets resolves what `log` was pointed at: one entry when given a
// query, or every entry in the vault when given none — the design doc's
// `gage log [QUERY]`, where the optional argument narrows a vault-wide
// log to a single entry.
func logTargets(app *App, v *gage.Vault, args []string, ident *gage.Identity) ([]uuid.UUID, error) {
	if len(args) == 1 {
		id, err := resolveHistoricalQuery(app, v, args[0], ident)
		if err != nil {
			return nil, err
		}
		return []uuid.UUID{id}, nil
	}
	ids, err := v.EntryIDs()
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// resolveHistoricalQuery is resolveQuery with one fallback the two
// history commands need and no other command does: an entry that has
// been removed is no longer in the vault to resolve against, but git
// still has every commit it ever appeared in.
//
// The fallback is deliberately narrow — a *full, well-formed UUID*, and
// only after ordinary resolution has already failed to find anything.
// Keeping it after resolution preserves the title-first ordering
// ("Addressing entries"), and requiring the whole UUID keeps it from
// inventing matches: a substring can't be completed against a set of
// present entries that no longer contains the one being asked about, so
// there is nothing to disambiguate against and a guess would be exactly
// that. A removed entry's id is what `gage log` printed while it still
// existed, which is where a human gets it.
func resolveHistoricalQuery(app *App, v *gage.Vault, query string, ident *gage.Identity) (uuid.UUID, error) {
	id, _, err := resolveQuery(app, v, query, ident)
	if err == nil {
		return id, nil
	}
	if exitcode.CodeOf(err) != exitcode.NotFound {
		return uuid.Nil, err
	}
	deleted, parseErr := uuid.Parse(query)
	if parseErr != nil {
		return uuid.Nil, err
	}
	return deleted, nil
}

// writeLog renders one line per revision: when, which commit, and — for
// a multi-entry log — which opaque entry it belongs to. Never a title.
func writeLog(app *App, v *gage.Vault, ids []uuid.UUID) error {
	multi := len(ids) > 1
	type row struct {
		when time.Time
		line string
	}
	var rows []row
	for _, id := range ids {
		entries, err := v.Log(id)
		if err != nil {
			return err
		}
		for _, le := range entries {
			line := fmt.Sprintf("%s  %s", le.When.Local().Format(logTimeFormat), shortEntryID(le.Hash))
			if multi {
				line += "  " + shortEntryID(id.String())
			}
			if le.Deleted {
				line += "  (deleted)"
			}
			rows = append(rows, row{when: le.When, line: line})
		}
	}
	if len(rows) == 0 {
		writeOut(app.Err, []string{"gage: no commit history for that entry yet."})
		return nil
	}
	// Newest first across the whole set, so a vault-wide log reads as
	// one timeline rather than as entries concatenated.
	//
	// Sorted on the timestamps themselves rather than on the rendered
	// lines. The rendered form carries a UTC offset, so a vault whose
	// history spans a daylight-saving change has two offsets in it, and
	// ordering those strings would sort by wall-clock time — putting an
	// hour of commits from either side of the change in the wrong order,
	// on the one day a human is most likely to be reading a log to work
	// out what happened when. Ties keep their existing relative order so
	// commits sharing a second stay grouped by entry.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].when.After(rows[j].when) })

	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, r.line)
	}
	writeOut(app.Out, lines)
	return nil
}

// newHistoryCommand builds `gage history --decrypt <query>`.
//
// --decrypt is required and the query is mandatory, and both are the
// guard. This is the design doc's "one meaningfully more dangerous
// tier": it decrypts *past* revisions of a secret's value, including
// ones since rotated away. There is deliberately no confirmation prompt
// on top — the subcommand's name, the required flag and the required
// query are three explicit acts already, and a [y/N] fired on every
// invocation would train the habit of dismissing it, which would cost
// more at the recipient-change prompt than it buys here. See the M12
// plan's decision. There is deliberately no bulk form either: a query
// always names one entry.
func newHistoryCommand(app *App) *cobra.Command {
	var (
		useFlag     string
		decryptFlag bool
	)
	cmd := &cobra.Command{
		Use:   "history --decrypt <query>",
		Short: commandShort("history"),
		Long: commandShort("history") + ".\n\n" +
			"Walks one entry's revisions and decrypts each, so old values — including\n" +
			"passwords already rotated away — are printed. --decrypt is required and\n" +
			"names exactly that. Use `gage log` for timestamps without any plaintext.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !decryptFlag {
				return exitcode.New(exitcode.Usage,
					"gage: history requires --decrypt; it prints past values of a secret. "+
						"Use `gage log` for timestamps only")
			}
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				id, err := resolveHistoricalQuery(app, v, args[0], ident)
				if err != nil {
					return err
				}
				revs, err := v.History(id, ident)
				if err != nil {
					return err
				}
				return writeHistory(app, id, revs)
			})
		},
	}
	addUseFlag(cmd, &useFlag)
	cmd.Flags().BoolVar(&decryptFlag, "decrypt", false,
		"required: decrypt and print every past revision of the entry")
	return cmd
}

// writeHistory renders the revisions as a diff across commits: each
// revision's decrypted fields, with the lines that changed since the
// next-older one marked.
//
// A diff rather than a dump of every revision in full, because the
// question this command answers is "what did this used to be" — and an
// entry whose description never changed shouldn't reprint it once per
// commit, burying the one line that did.
func writeHistory(app *App, id uuid.UUID, revs []gage.Revision) error {
	if len(revs) == 0 {
		writeOut(app.Err, []string{"gage: no commit history for that entry yet."})
		return nil
	}

	var lines []string
	// Oldest first, so each revision is diffed against the state before
	// it and the output reads forwards the way a history does.
	for i := len(revs) - 1; i >= 0; i-- {
		rev := revs[i]
		header := fmt.Sprintf("commit %s  %s", shortEntryID(rev.Hash), rev.When.Local().Format(logTimeFormat))
		if rev.Deleted {
			lines = append(lines, header, "    (entry deleted)", "")
			continue
		}
		lines = append(lines, header)

		var older gage.Entry
		haveOlder := false
		for j := i + 1; j < len(revs); j++ {
			if !revs[j].Deleted {
				older, haveOlder = revs[j].Entry, true
				break
			}
		}
		lines = append(lines, diffEntryLines(older, rev.Entry, haveOlder)...)
		lines = append(lines, "")
	}
	writeOut(app.Out, lines)
	return nil
}

// diffEntryLines renders one revision's fields, marking each as added,
// changed, or unchanged relative to the previous revision.
func diffEntryLines(older, newer gage.Entry, haveOlder bool) []string {
	var lines []string
	mark := func(name, was, now string) {
		switch {
		case !haveOlder:
			lines = append(lines, fmt.Sprintf("  + %s: %s", name, now))
		case was == now:
			lines = append(lines, fmt.Sprintf("    %s: %s", name, now))
		case was == "":
			lines = append(lines, fmt.Sprintf("  + %s: %s", name, now))
		default:
			lines = append(lines, fmt.Sprintf("  - %s: %s", name, was))
			lines = append(lines, fmt.Sprintf("  + %s: %s", name, now))
		}
	}
	mark("title", older.Title, newer.Title)
	if newer.Description != "" || (haveOlder && older.Description != "") {
		mark("description", older.Description, newer.Description)
	}
	mark("value", older.Value, newer.Value)
	mark("updated_by", older.UpdatedBy, newer.UpdatedBy)

	// Field keys from both sides, so a key removed in this revision
	// still shows as having gone.
	seen := map[string]bool{}
	var keys []string
	for k := range newer.Fields {
		if !seen[k] {
			seen[k], keys = true, append(keys, k)
		}
	}
	if haveOlder {
		for k := range older.Fields {
			if !seen[k] {
				seen[k], keys = true, append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		mark("fields."+k, older.Fields[k], newer.Fields[k])
	}
	return lines
}
