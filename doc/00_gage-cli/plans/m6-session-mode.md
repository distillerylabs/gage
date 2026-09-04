# M6 — Session mode

[← M5](m5-query-resolution.md) · [plan index](index.md) · next: [M7 — Metadata index](m7-metadata-index.md)

> **Recommended model: Sonnet.** Borderline — switch to Opus if the idle-timeout clock seam or the readline integration turns awkward. The `Session` type itself is well-specified; the REPL wiring is where it could get fiddly.

## Goal

The `Session` library type: holds one or more vaults' `Identity` values in
memory for longer than a single command. Multi-vault `use`/`lock`/`status`,
with all M4/M5 commands working against a "current" vault.

**`Session` adds no new memory-protection mechanics.** Page-locking and
zeroing (`mlock`/`VirtualLock`, `Identity.Close()`) are M2's, established
the moment any `Identity` exists. `Session.Use` calls the same
`Vault.Unlock`; `Session.Lock` and the idle timeout call the same
`Identity.Close()`. No M4/M5 `Vault` method signature changes — only how
many times `Unlock`/`Close` run around them. That invariant is the point
of the milestone and worth asserting directly in the tests.

The REPL (`use`, `lock`, `status`, `exit`, `help`) is `cmd/gage`'s
terminal rendering of that type — tests cover `Session` directly wherever
possible, with a thinner REPL-wiring test on top. Windows is an explicit
target here like every other platform. No metadata index yet — still
decrypt-on-demand per command.

## Depends on

- **M2** — `Unlock`/`Identity`/`Close`.
- **M4** — the one-shot handler shape this deliberately mirrors.
- **M5** — the resolver's candidate list, which the REPL prompts with.

## Design references

- ["Session model"](../tdds/gage-cli-design.md) — the two invocation
  modes, the REPL transcript, and why there's no daemon
- ["Why an idle timeout still matters despite process-scoped keys"](../tdds/gage-cli-design.md)
- ["Session-only commands"](../tdds/gage-cli-design.md)
- ["A few decisions worth calling out"](../tdds/gage-cli-design.md) — the
  history file's plaintext-query-only rule

## Decisions to make first

- **[Q-ROOT-CMD](open-questions.md)** — resolved: bare `gage` on a TTY
  enters the session prompt, piped stdin prints help. Wired in M0;
  confirm that dispatch actually reaches the REPL now that the REPL
  exists.
- **[Q-HELP-SURFACES](open-questions.md)** — resolved: in-session `help`
  renders from M0's command registry filtered to session availability;
  `help <command>` is supported, mirroring `gage help <subcommand>`;
  `-u|--use` appears in per-command help but not in top-level session
  help, which leads with bare `use <vault>`. Nothing left to decide here
  — just don't hand-maintain a second list.
- **[Q-CMD-AVAILABILITY](open-questions.md)** — resolved: everything
  except `init` and `clone` works in-session, including the `vault`,
  `identity`, and `recipient` families. Those commands land in M1, M9,
  and M9 respectively, so M6 only needs the registry filter to be
  correct; the commands themselves tag in as they arrive.
- **Readline implementation.** Resolved: [`chzyer/readline`](https://github.com/chzyer/readline).
  Must be able to *not* write selected lines to history, and must work
  on Windows.
- **Idle-timeout clock injection.** Resolved: a `now func() time.Time`
  field on `Session`, defaulting to `time.Now`, overridden in tests.
  No new types, no dependency — sufficient because the check is a plain
  "now vs. last-activity" read at the start of each call, never a
  background timer, so nothing needs `Sleep`/`Timer`/`After` semantics.
  (`testing/synctest`, available on this Go version, was considered and
  rejected: it fakes time only for goroutines durably blocked on
  bubble-tracked primitives, and this milestone's REPL tests may drive a
  real pty for Ctrl-C/Ctrl-D, which doesn't mix well with a synctest
  bubble.)
- **What `lock` with no argument does** — resolved: locks all vaults in
  the session, confirming the design doc's "one vault, or all, if
  omitted."
- **Does an idle re-lock interrupt an in-flight command?** Resolved: no.
  The timeout is evaluated at the start of each call, never
  mid-operation.

## Tests (write first)

- [x] `Session.Use(vault)` unlocks once; subsequent entry calls against
      the same session don't re-prompt for the passphrase
- [x] Entry commands routed through `Session` (`show`, `insert`, ...)
      call the exact same `Vault` methods as one-shot mode — no method
      gained a session-only signature or a duplicate implementation
- [x] An ambiguous query resolved through `Session` invokes `Prompter`'s
      candidate-list callback (a fake in tests) and returns the entry
      selected from it — the session-mode counterpart to M5's one-shot
      ambiguous-fails behavior, proven at the `Session` level rather than
      by driving a real terminal
- [x] `Session.Lock(vault)` drops that vault's key; the next entry call
      against it re-prompts, while other unlocked vaults in the same
      session are unaffected
- [x] `Session.Lock()` with no vault locks all of them (per the decision
      above)
- [x] `Session.Status()` reports correct lock state for multiple vaults
      touched in one session
- [x] `--use NAME` inside a session, naming a vault not yet `use`d,
      prompts to unlock it and then operates against it — without
      changing which vault is current
- [x] `exit`/EOF terminates the REPL cleanly, and every held `Identity`
      is `Close()`d on the way out
- [x] Idle timeout: with simulated/injected elapsed time past
      `idle_timeout`, the next `Session` call against that vault re-prompts
      as if `Lock` had been called
- [x] An idle timeout re-lock calls `Identity.Close()` — the same code
      path as an explicit `Lock`, not a parallel one
- [x] Session command history contains typed command lines (e.g. `show
      protonmail`) but never a decrypted `value`/`fields` value
- [x] The history file is created `0600`
- [x] The prompt renders `{vault}`, `{lock}`, and `{dirty}` tokens from
      `[shell].prompt` in global config, and reflects lock state changing
      within a session
- [x] the REPL is a thin wiring layer: it parses a typed line into the
      corresponding `Session` call and renders the result — one wiring
      test suffices here, not a re-test of `Session` behavior
- [x] Bare `gage` on a TTY reaches the REPL — the M0 dispatch decision,
      re-asserted end-to-end now that there's a session to reach
- [x] In-session `help` lists the session-only commands
      (`use`/`lock`/`status`/`exit`/`help`) *and* every command the
      registry marks session-available — entry commands and the
      `vault`/`identity`/`recipient` families alike
- [x] In-session `help` and `gage --help` derive from the same command
      registry: a command registered as available in both modes appears
      in both surfaces with the same description, asserted by comparing
      the rendered sets rather than by eyeballing two hand-written lists
- [x] No command available in a session is missing from in-session
      `help`, and no session-only command leaks into `gage --help`
- [x] `init` and `clone` do not appear in in-session `help`, and
      invoking either inside a session reports plainly that it's a
      one-shot command rather than failing obscurely
- [x] `help <command>` in-session prints that command's usage, mirroring
      `gage help <subcommand>`; `help <unknown>` reports usage without
      terminating the session
- [x] Top-level in-session `help` presents vault selection as bare
      `use <vault>`, not as `-u|--use NAME`
- [x] `help show` (per-command) *does* list `-u|--use`, which still works
      ad hoc inside a session
- [x] An unknown REPL command reports usage and does not terminate the
      session, and points at `help`
- [ ] The full M6 test suite passes unmodified on Windows, not just
      Linux/macOS — `Session`'s locking/idle-timeout behavior doesn't
      depend on any POSIX-only mechanism. *Written for it and verified as
      far as a non-Windows host can (`GOOS=windows go build`/`go vet`,
      which type-checks the test files too); the mode assertion follows
      this suite's existing `runtime.GOOS == "windows"` convention and
      the one pty test is tagged `linux || darwin`. Still needs an actual
      run of the CI matrix to check off.*

## Implementation

- [x] `Session` library type: `Use`/`Lock`/`Status`, holding each vault's
      `Identity` (already page-locked and core-dump-protected per M2)
      across multiple calls; idle timeout re-lock calls `Identity.Close()`
      the same as an explicit `Lock`
- [x] Injectable clock behind the idle timeout
- [x] REPL loop in `cmd/gage`: thin terminal wiring over `Session`
      (`use`/`lock`/`status`/`exit`/`help`), including rendering an
      ambiguous-query candidate list as the `[1-2]` prompt shown in the
      design doc — the one place this wiring is more than plain dispatch
- [x] In-session `help` and `help <command>`, rendered from M0's command
      registry filtered to session availability — not a second
      hand-maintained list
- [x] Session dispatch rejects the one-shot-only commands (`init`,
      `clone`) with a clear "one-shot only" message rather than an
      unknown-command error
- [x] Session dispatch routes the `vault`/`identity`/`recipient`
      families the same as entry commands, against the session's current
      vault (Q-CMD-AVAILABILITY)
- [x] Prompt template rendering (`{vault}`, `{lock}`, `{dirty}`) from
      `[shell].prompt`
- [x] The `Prompter` reads through the session's line editor while a
      session is running. Not in the original list, but not optional:
      `chzyer/readline` holds the terminal in raw mode and reads stdin
      from its own goroutine for the life of an instance, so a passphrase
      prompt that reached for stdin itself would race it. Both readers —
      and both writers — go through one `lineReader` (which also made the
      whole REPL testable without a pty). The same wiring correction
      applies to raw mode and to readline's masked-input config, which
      otherwise judge "is this a terminal" from the *process's*
      stdin/stdout rather than the descriptors they were handed — the
      second of those silently skips drawing the passphrase prompt when
      the two differ.
- [x] History file handling: `[shell].history_file`, `0600`, command
      lines only — never a decrypted value
- [x] Entry commands ported to work against `Session`'s current vault,
      including ad-hoc `--use NAME`
- [x] CI/test coverage on Windows in addition to Linux/macOS for this
      milestone specifically — it's the first point session state (idle
      timeout, multi-vault `Identity` caching) needs to be proven to
      behave identically across all three, not just compile. *No workflow
      change was needed: `.github/workflows/ci.yml` already runs
      `build`/`test`/`lint` on the full three-OS matrix, and M6 added
      nothing that opts out of it.*

## Definition of done

Full test list green on all three CI platforms, with the Windows run
being the unmodified suite. Both invocation modes work and share every
`Vault` method between them.

## Affects later milestones

- **M7's metadata index lives on `Session`**, keyed alongside each
  vault's cached `Identity` — never on `Vault`.
- M8a's auto fetch+pull hooks `Vault.Unlock`, which both `Session.Use` and
  the one-shot handler call — so the hook must not be wired into the
  `use` REPL command specifically.
- M10's opportunistic trust-cache warning rides the same unlock hook.
