# M13 — Tab completion & command abbreviation (session mode)

[← M12](m12-polish.md) · [plan index](index.md)

> **Recommended model: Sonnet\*.** Mostly wiring against settled
> abstractions (M0's registry, M7's index, `chzyer/readline`), but the
> `AutoCompleter` integration is the same kind of "could get fiddly"
> library seam M6 flagged for its readline work generally — switch to
> Opus if it does.

## Goal

Two related conveniences in the interactive REPL only (not one-shot
mode, and not shell-side completion scripts — see "Out of scope"):

1. **Tab completion** of partial input at the `gage>` prompt — command
   and subcommand names, flags, vault names, and entry queries.
2. **Unambiguous command-name abbreviation as input**, with no Tab
   needed: typing `ident` and pressing Enter runs `identity` if it's the
   only session-visible command starting with `ident`.

Both opened as [#25](https://github.com/distillerylabs/gage/issues/25) and are
new scope raised after the original plan shipped in full — filed as its
own milestone rather than folded into "Post-plan changes" in the index,
because it carries its own up-front decisions and test list rather than
being a small, self-contained addition to an existing milestone's
surface.

This milestone does **not** touch the TDD on its own initiative; once
the decisions below are implemented, `gage-cli-design.md` gets a new
subsection near the session-model material recording them, the same way
every other milestone's design decisions live in the TDD rather than
only in this plan.

## Depends on

- **M0** — the command registry (`CommandInfo`, `Availability`,
  `SessionVisible()`, `GroupSession`) is the single source of truth for
  which names are completable/abbreviatable; this milestone must not
  hand-maintain a second list, the same rule M6's in-session `help`
  already follows.
- **M6** — the REPL, `runSessionCommand` (`cmd/gage/repl.go`), and the
  `ttyLineReader`/`chzyer/readline` wiring (`cmd/gage/lineread.go`) this
  milestone extends. `plainLineReader` (the non-interactive `--script`/
  `--stdin`/in-process-test reader) is untouched — completion is
  meaningless where there's no interactive line being edited.
- **M7** — `Session`'s per-vault `Index` and `Index.titles()`
  (`internal/gage/index.go`), the source of entry-title candidates. No
  new decrypt path: this reuses `ensureIndex`'s existing lazy-build,
  exactly as `show`/`search`/etc. already trigger it.

## Design references

- ["Session model"](../../tdds/gage-cli-design.md) — the REPL, and why
  there's no daemon.
- ["Session-only commands"](../../tdds/gage-cli-design.md) and
  Q-HELP-SURFACES / Q-CMD-AVAILABILITY (`open-questions.md`) — the
  availability rules this milestone's candidate sets must stay
  consistent with (`init`/`clone` excluded, etc.).
- ["Addressing entries & the metadata index"](../../tdds/gage-cli-design.md)
  — M5's title/UUID resolution order. Read alongside "Decisions to make
  first" below: this milestone's command-name resolver is deliberately
  **not** the same code path, even though the shapes rhyme.
- *New TDD subsection (to be written as part of this milestone's
  implementation, not yet in the doc)* — will record the decisions
  below once they're implemented, the same way every other milestone's
  settled decisions end up in the TDD, not only in this plan.

## Decisions to make first

- **Scope: build both behaviors.** Resolved — both Tab completion and
  no-Tab unambiguous-abbreviation dispatch land in this milestone; they
  share one underlying "what matches this prefix, among session-visible
  names" primitive. Tab renders candidates (or completes fully on a
  single match); bare dispatch auto-runs on a single match and refuses
  to guess on more than one.
- **Match rule: prefix only.** Resolved — `ident` matches `identity`
  because it starts with it; substring-anywhere matching (`enti` →
  `identity`) is rejected as a v1 rule. Standard readline/shell
  convention, and it keeps every ambiguous case easy to reason about.
  This is a genuinely different rule from M5's entry-title *substring*
  match, which is why the two resolvers stay separate (next bullet).
- **A separate resolver from M5's, not a shared one.** M5's
  `resolveTitleAndUUID` (`internal/gage/resolve.go`) resolves entry
  queries against decrypted metadata with a specific precedence (exact
  title → substring title → exact UUID → substring UUID → ambiguous).
  This milestone's command-name resolver is new, small, and
  purpose-built for prefix matching over the static registry — reusing
  M5's function, or generalizing it to cover both, would couple two
  things that change for unrelated reasons (entry addressing vs. CLI
  ergonomics) and would import M5's substring semantics where the
  decision above just rejected them.
- **Full surface in one milestone.** Resolved — commands/aliases,
  subcommands, flags, vault names, and entry-title candidates are all
  in scope for M13, not phased into a stretch milestone. One coherent
  test list; nothing ships half-finished.
- **Abbreviation reach: top-level commands only.** Resolved — `ident` →
  `identity` resolves; `identity ad` → `identity add` does not (typed in
  full). Subcommands and flags remain **Tab-completable** (full surface,
  previous bullet) but are not eligible for no-Tab abbreviation dispatch
  in v1. Matches the issue's own example exactly and keeps the dispatch
  change's blast radius to one place (`runSessionCommand`'s first
  token).
- **Entry-title candidates: current vault only.** Resolved — completing
  an entry-query argument (for `show`/`cat`/`edit`/`rename`/`generate`/
  `rm`/`mv`/`cp`) offers titles from the session's *current* vault's
  index only, never from another vault this session happens to have
  unlocked. Matches the existing default every one of those commands
  already has ("current vault unless `--use` names another") — `--use
  NAME` arguments themselves complete against vault *names* (below),
  not against that other vault's entries.
- **Never completes into a silent unlock.** If the current vault is
  locked, entry-query completion offers no title candidates and — this
  is the part worth stating explicitly — must not call anything that
  reaches for a passphrase. UUID-prefix candidates are fine even when
  locked, since entry filenames (`entries/<uuid>.age`) are already
  visible on disk unencrypted; only titles live inside ciphertext.
  `use`/`lock`/`--use` argument completion (vault *names*, from global
  config's registry, not from `Session`) needs no unlock either way.
- **Ambiguous abbreviation: reject and list, never guess.** When the
  no-Tab abbreviation matches more than one session-visible command,
  `runSessionCommand` reports the candidates and does not run any of
  them — mirroring M5's ambiguous-query shape in spirit (refuse to
  guess, name the matches) without sharing its code (per the resolver
  decision above). This is a REPL-level rejection, not a process exit:
  it must not terminate the session, the same convention M6 already
  established for an unknown command or a Cobra usage rejection inside
  a session.
- **`init`/`clone` excluded from every candidate set.** Consistent with
  their existing exclusion from in-session `help` (Q-CMD-AVAILABILITY) —
  neither Tab-completes nor is reachable via abbreviation in a session.
- **`chzyer/readline` integration shape.** Resolved — a custom
  `readline.AutoCompleter` (the `Do(line []rune, pos int) (newLine
  [][]rune, length int)` interface), assigned to `readline.Config`'s
  `AutoComplete` field in `newTTYLineReader`
  (`cmd/gage/lineread.go`). `PrefixCompleter` (the library's static-tree
  helper) isn't enough on its own because vault names and entry titles
  are dynamic per-session data; the custom completer composes a
  registry/flag-derived static part with a `Session`-derived dynamic
  part.
- **Out of scope: one-shot shell completion.** `gage completion
  bash|zsh|fish` (Cobra's built-in generator, effectively free) is a
  different mechanism entirely — shell-side, not `chzyer/readline`-side
  — and neither of the issue's two examples is about one-shot mode.
  Tracked as a possible follow-up issue, not bundled here.
- **Windows parity is a hard requirement**, the same bar M6 set for
  session mode generally: the full test list must pass unmodified on
  the Linux/macOS/Windows CI matrix, not just compile there.

## Tests (write first)

- [x] Completing a partial session-only command (`us`, `loc`, `stat`,
      ...) returns exactly the matching candidate(s); a single
      unambiguous match completes the line fully.
- [x] Completing a partial name that prefix-matches multiple
      session-visible registry entries lists all of them rather than
      picking one.
- [x] Typing an unambiguous top-level abbreviation with no Tab (e.g.
      `ident`) and pressing Enter runs the same command as the full
      name, asserted by calling `runSessionCommand` directly.
- [x] Typing an ambiguous top-level abbreviation reports the candidates,
      runs nothing, and leaves the session running (not terminated).
- [x] Substring matches that aren't also prefix matches (e.g. `enti` for
      `identity`) are rejected by both completion and abbreviation
      dispatch — pins the "prefix only" decision against regressing
      toward M5's substring rule.
- [x] `init` and `clone` never appear among in-session completion
      candidates and are never reachable via abbreviation dispatch.
- [x] Completing a `use`/`lock`/`--use` argument lists exactly the
      vault names registered in global config
      (`config.GlobalConfig.Vaults`), independent of which vaults are
      currently unlocked.
- [x] Completing an entry-query argument (e.g. for `show`) lists titles
      from the *current* vault's index when that vault is unlocked, and
      never titles from a different vault this session also holds
      unlocked.
- [x] Completing an entry-query argument when the current vault is
      locked yields no title candidates and triggers no call into the
      unlock/`Prompter` path — asserted with a fake `Prompter` that
      fails the test if invoked.
- [x] Completing an entry-query argument when the current vault is
      unlocked but its index hasn't been built yet triggers exactly one
      `ensureIndex` build (reusing M7's existing lazy-build), not a
      build per keystroke.
- [x] Flag completion for a given command matches exactly that
      command's registered flags, asserted against the real
      `*cobra.Command` rather than a hand-maintained duplicate list.
- [x] Subcommand completion works (e.g. completing `identity <Tab>`
      lists `add`/`enroll`/`list`) even though subcommands are excluded
      from no-Tab abbreviation dispatch — pins that the two features
      have different reach per the decision above.
- [x] The candidate-producing functions are plain, typed, and callable
      without a terminal (in-process, per this repo's test-driving
      convention) — the `AutoCompleter.Do` wiring itself gets one
      thin test; everything else is tested one layer down.
- [x] One real-terminal (`creack/pty`) test confirms a literal Tab
      keypress at the prompt triggers completion end-to-end, mirroring
      M6's minimal pty coverage for masked passphrase input — not a
      re-test of candidate logic already covered in-process.
- [x] Completion (rendering candidates, or a keystroke that doesn't
      submit a line) never writes to the session history file; only a
      submitted line does — re-asserting M6's existing invariant against
      this new code path through the same `lineReader`.
- [x] The full M13 test list passes unmodified on Linux, macOS, and
      Windows.

## Implementation

- [x] A registry-driven static candidate source for command/subcommand
      names and aliases, filtered to `Availability.SessionVisible()`
      (reuses M0's registry and M6's existing filter — no second list).
- [x] Cobra flag introspection for per-command flag candidates.
- [x] `Session`-derived dynamic candidate sources: vault names (from
      global config) for `use`/`lock`/`--use` arguments, and entry
      titles (from the current vault's `Index.titles()`, only when
      unlocked) for entry-query arguments.
- [x] The prefix-match resolver for no-Tab abbreviation dispatch, wired
      into `runSessionCommand`'s handling of the first token, ahead of
      handing off to the Cobra root command.
- [x] A custom `readline.AutoCompleter` in `cmd/gage`, assigned via
      `readline.Config.AutoComplete` in `newTTYLineReader`, composing
      the static and dynamic candidate sources above.
- [x] New TDD subsection in `gage-cli-design.md` recording the decisions
      above, once implemented.
- [x] `doc/cli/session-mode.md` updated with a transcript demonstrating
      both behaviors, the same way the existing transcript demonstrates
      `use`/`lock`/`status`.

## Definition of done

Full test list green on all three CI platforms. Tab completion and
unambiguous top-level abbreviation both work in the interactive REPL,
share one underlying prefix-match primitive, never trigger an unlock,
and never leak another vault's entry titles into the current vault's
candidate set.

## Affects later milestones

None currently planned depend on this. If a future milestone adds a new
session-visible command family, it inherits completion/abbreviation
automatically through the M0 registry — no new work required there,
which is the point of not hand-maintaining a second list.
