package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
)

// entryQueryCommands names every leaf command whose first positional
// argument addresses an existing entry — show/cat/edit/rename/generate/
// rm/mv/cp, per the M13 plan's "entry-title candidates" decision.
// generate's argument names a *new* entry, but the plan includes it
// anyway (existing titles are still useful to see, e.g. to avoid an
// accidental duplicate) — this map is what lets that decision live in
// one place rather than a scattered set of special cases.
var entryQueryCommands = map[string]bool{
	"show":     true,
	"cat":      true,
	"edit":     true,
	"rename":   true,
	"generate": true,
	"rm":       true,
	"mv":       true,
	"cp":       true,
}

// prefixMatches returns every candidate that starts with prefix,
// deduplicated and sorted — the single "what matches this prefix, among
// session-visible names" primitive both Tab completion and no-Tab
// abbreviation dispatch share (see runSessionCommand's
// resolveSessionCommandName).
//
// Prefix only, deliberately not substring-anywhere: see the M13 plan's
// "match rule: prefix only" decision. This is a different rule from
// gage.resolveTitleAndUUID's substring stages, which is why the two
// resolvers stay separate rather than one being generalized to cover
// both.
func prefixMatches(candidates []string, prefix string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		if seen[c] || !strings.HasPrefix(c, prefix) {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// topLevelSessionNames returns every top-level command word a session
// accepts: single-word registry entries and their aliases, plus (once
// each) the first word of every multi-word registry entry — "vault",
// "identity", "recipient", "git", "auth" — none of which is itself a
// registry entry, since those exist only as the grouping Cobra command
// their subcommands hang off. Filtered to SessionVisible, so init/clone
// never appear (Q-CMD-AVAILABILITY) — the same rule M6's session help
// already follows, read off the same registry rather than a second list.
func topLevelSessionNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, ci := range registry {
		if !ci.Availability.SessionVisible() {
			continue
		}
		top := ci.Name
		if i := strings.IndexByte(top, ' '); i >= 0 {
			top = top[:i]
		}
		add(top)
		for _, a := range ci.Aliases {
			add(a)
		}
	}
	return out
}

// ambiguousCommandError is what the no-Tab abbreviation dispatch returns
// when a prefix matches more than one top-level session-visible command
// name: refuse to guess, name the matches — mirroring
// gage.AmbiguousQueryError in shape, per the M13 plan's "ambiguous
// abbreviation" decision, without sharing its code (the resolvers are
// deliberately separate; see prefixMatches's doc comment).
type ambiguousCommandError struct {
	prefix     string
	candidates []string
}

func (e *ambiguousCommandError) Error() string {
	return fmt.Sprintf("gage: %q matches more than one command: %s", e.prefix, strings.Join(e.candidates, ", "))
}

// resolveSessionCommandName expands a typed first token into the
// canonical (or alias) command name it refers to: unchanged if it's
// already an exact top-level command name or alias, the sole prefix
// match if exactly one top-level session-visible name starts with it,
// and unchanged again — leaving the existing "unknown command" path to
// report it — if nothing matches at all. More than one match is an
// ambiguousCommandError.
//
// This is the no-Tab half of M13: called once, against the first token,
// from runSessionCommand — never against a subcommand or a flag, per the
// plan's "abbreviation reach: top-level commands only" decision.
func resolveSessionCommandName(word string) (string, error) {
	if word == "" {
		// Every name is a "match" for an empty prefix, which would
		// otherwise turn a stray empty first token (e.g. a bare `""`
		// typed as the whole word) into an ambiguous-everything error
		// instead of the ordinary "unknown command" one below reports.
		return word, nil
	}
	if _, ok := findCommand(word); ok {
		return word, nil
	}
	matches := prefixMatches(topLevelSessionNames(), word)
	switch len(matches) {
	case 0:
		return word, nil
	case 1:
		return matches[0], nil
	default:
		return "", &ambiguousCommandError{prefix: word, candidates: matches}
	}
}

// completionWords splits the already-typed portion of a session command
// line (everything up to the cursor) into full words and the partial
// word under the cursor.
//
// This is a plain whitespace split, not splitLine's quote-aware one:
// completion only needs to know *where* in the command the cursor sits —
// which command, which argument position, whether it follows a flag —
// and a partial word that happens to open a quote is still matched
// against candidates by its literal text. Multi-word quoted entry
// titles are outside this milestone's test list.
func completionWords(head string) (words []string, partial string) {
	if head == "" {
		return nil, ""
	}
	fields := strings.Fields(head)
	if len(fields) == 0 {
		return nil, ""
	}
	if isSpace(head[len(head)-1]) {
		return fields, ""
	}
	return fields[:len(fields)-1], fields[len(fields)-1]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// flagCandidates lists cmd's own registered flags — long and short forms
// — reading them straight off the real *cobra.Command rather than a
// hand-maintained list, per the M13 plan's flag-completion test. Its own
// flags only (LocalFlags, the same source renderCommandHelp already
// uses), not every flag inherited from a parent: `identity add`'s own
// -u/--use/-d/--device/-m/--method, not root's unrelated --yes.
func flagCandidates(cmd *cobra.Command) []string {
	var out []string
	cmd.LocalFlags().VisitAll(func(f *flag.Flag) {
		if f.Hidden {
			return
		}
		out = append(out, "--"+f.Name)
		if f.Shorthand != "" {
			out = append(out, "-"+f.Shorthand)
		}
	})
	return out
}

// vaultCompletionFlags names the flags whose value is a vault name, so
// completing right after one of them offers vault names instead of a
// command's usual positional candidates. -u/--use is every entry
// command's vault selector; the M13 plan resolves only this one (not
// mv/cp's --to-vault) as in scope.
func vaultCompletionFlags(word string) bool {
	return word == "-u" || word == "--use"
}

// vaultNameCandidates lists every vault name registered in global
// config — the `use`/`lock`/`--use` argument's candidate set, per the
// M13 plan's decision. It reads global config directly rather than
// through a Session, so it lists every registered vault "independent of
// which vaults are currently unlocked" (and needs no session at all,
// for one-shot's --use... though completion itself only ever runs in a
// session). A config that can't be read yields no candidates rather than
// an error: completion has nowhere to report one.
func vaultNameCandidates() []string {
	g, err := readGlobalConfig()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(g.Vaults))
	for name := range g.Vaults {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// entryTitleCandidates lists the current vault's cached entry titles for
// completing an entry-query argument, or nil outside a session or when
// gage.Session.EntryTitleCandidates itself has nothing to offer (no
// current vault, or one this session hasn't unlocked) — see that
// method's doc comment for the "never completes into a silent unlock"
// guarantee this relies on.
func entryTitleCandidates(app *App) []string {
	if app.Session == nil {
		return nil
	}
	titles, err := app.Session.EntryTitleCandidates()
	if err != nil {
		return nil
	}
	return titles
}

// subcommandSessionVisible reports whether parent's child command
// (looked up in the registry as "parent child") is available in a
// session. A pairing with no registry entry at all defaults to visible
// rather than hidden — every real command is registered
// (TestRegistryCompleteness enforces it), so this only matters for a
// Cobra child that isn't a real invocable command in the first place.
func subcommandSessionVisible(parent, child string) bool {
	ci, ok := findCommand(parent + " " + child)
	if !ok {
		return true
	}
	return ci.Availability.SessionVisible()
}

// subcommandCandidates lists a parent command's own child commands and
// their aliases — reading the real Cobra tree (`identity` ->
// add/enroll/list) rather than a second, hand-maintained list, per the
// M13 plan's subcommand-completion test.
func subcommandCandidates(parent *cobra.Command) []string {
	var out []string
	for _, child := range parent.Commands() {
		if child.Hidden || !subcommandSessionVisible(parent.Name(), child.Name()) {
			continue
		}
		out = append(out, child.Name())
		out = append(out, child.Aliases...)
	}
	return out
}

// leafPositionalCandidates lists the candidates for a leaf command's
// positional argument at the position `rest` (the already-typed args
// past the command name) identifies — vault names for use/lock's single
// argument, entry titles for the first positional argument of an
// entry-query command, nothing for any later position (rename's
// <new-title>, e.g., is never completed against existing titles).
func leafPositionalCandidates(app *App, target *cobra.Command, rest []string) []string {
	if len(rest) != 0 {
		return nil
	}
	switch {
	case target.Name() == "use" || target.Name() == "lock":
		return vaultNameCandidates()
	case entryQueryCommands[target.Name()]:
		return entryTitleCandidates(app)
	default:
		return nil
	}
}

// completionCandidates is the whole "what could go here" decision for
// Tab completion, given the words already typed before the cursor
// (words) and the partial word under it (partial isn't consulted here —
// prefix-matching against it is the caller's job, per prefixMatches).
//
// It's a plain, typed function over a real *cobra.Command tree
// (root — rebuilt per completion the same way runSessionCommand rebuilds
// it per line, so a previous line's flag values can't leak in) precisely
// so it's testable without a terminal; sessionCompleter.Do below is the
// only part of this milestone that needs one, and it stays a thin
// wrapper around this.
func completionCandidates(app *App, root *cobra.Command, words []string, partial string) []string {
	if len(words) == 0 {
		if strings.HasPrefix(partial, "-") {
			return flagCandidates(root)
		}
		return topLevelSessionNames()
	}

	target, rest, err := root.Find(words)
	if err != nil || target == root {
		return nil
	}

	if strings.HasPrefix(partial, "-") {
		return flagCandidates(target)
	}
	if vaultCompletionFlags(words[len(words)-1]) {
		return vaultNameCandidates()
	}
	if len(rest) == 0 && target.HasSubCommands() {
		return subcommandCandidates(target)
	}
	return leafPositionalCandidates(app, target, positionalArgs(target, rest))
}

// positionalArgs strips target's own already-typed flags (and their
// values) out of rest, so a positional argument's index is counted
// correctly even when a flag precedes it on the line — e.g. "show
// --use work <query>" is still completing the query at position 0, not
// position 2. Falls back to rest unchanged if it doesn't parse as valid
// flags for target (an incomplete or unrecognized one mid-line); target
// is a throwaway command tree rebuilt fresh for this one completion (see
// sessionCompleter.Do), so parsing into its flag variables here has
// nothing else to disturb.
func positionalArgs(target *cobra.Command, rest []string) []string {
	if err := target.ParseFlags(append([]string(nil), rest...)); err != nil {
		return rest
	}
	return target.Flags().Args()
}

// sessionCompleter is the readline.AutoCompleter this milestone wires
// into newTTYLineReader (see lineread.go). It is deliberately thin: all
// the actual candidate logic lives in completionCandidates and the
// plain, typed functions above it, which is what "candidate-producing
// functions are plain, typed, and callable without a terminal" (the
// M13 plan's test list) requires — Do itself gets exactly one test, at
// this layer, for the wiring alone.
//
// root is rebuilt on every keystroke that triggers completion, not
// cached, for the same reason runSessionCommand rebuilds it per line:
// Cobra flag values persist on a parsed command, so a cached tree could
// carry a stale --use into a later completion.
type sessionCompleter struct {
	app *App
}

func newSessionCompleter(app *App) *sessionCompleter {
	return &sessionCompleter{app: app}
}

// Do implements readline.AutoCompleter. See the interface doc comment
// (chzyer/readline's complete.go) for newLine/length's exact contract:
// each entry of newLine is the *remainder* of one candidate beyond what
// was already typed, and length is how many runes of line that already
// covers.
func (c *sessionCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	words, partial := completionWords(string(line[:pos]))
	root := NewRootCmd(c.app)
	candidates := completionCandidates(c.app, root, words, partial)
	matches := prefixMatches(candidates, partial)

	out := make([][]rune, len(matches))
	for i, m := range matches {
		out[i] = []rune(m[len(partial):])
	}
	return out, len([]rune(partial))
}
