# M6 — Session mode

[← M5](m5-query-resolution.md) · [plan index](index.md) · next: [M7 — Metadata index](m7-metadata-index.md)

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

- **[Q-ROOT-CMD](open-questions.md)** — settled in M0, consumed here.
  Confirm the M0 wiring actually reaches the REPL.
- **Readline implementation.** History, line editing, and Ctrl-C/Ctrl-D
  handling in the REPL. Not in the locked dependency list; needs one
  (`chzyer/readline`, `peterh/liner`, or hand-rolled over `x/term`).
  Whatever's chosen must be able to *not* write selected lines to
  history, and must work on Windows.
- **Idle-timeout clock injection.** The tests need simulated elapsed
  time, so the timeout must read from an injectable clock rather than
  `time.Now()` directly. Decide the seam before writing the timer.
- **What `lock` with no argument does** — the design says "one vault, or
  all, if omitted." Confirm.
- **Does an idle re-lock interrupt an in-flight command?** Recommend no:
  the timeout is evaluated at the start of each call, never mid-operation.

## Tests (write first)

- [ ] `Session.Use(vault)` unlocks once; subsequent entry calls against
      the same session don't re-prompt for the passphrase
- [ ] Entry commands routed through `Session` (`show`, `insert`, ...)
      call the exact same `Vault` methods as one-shot mode — no method
      gained a session-only signature or a duplicate implementation
- [ ] An ambiguous query resolved through `Session` invokes `Prompter`'s
      candidate-list callback (a fake in tests) and returns the entry
      selected from it — the session-mode counterpart to M5's one-shot
      ambiguous-fails behavior, proven at the `Session` level rather than
      by driving a real terminal
- [ ] `Session.Lock(vault)` drops that vault's key; the next entry call
      against it re-prompts, while other unlocked vaults in the same
      session are unaffected
- [ ] `Session.Lock()` with no vault locks all of them (per the decision
      above)
- [ ] `Session.Status()` reports correct lock state for multiple vaults
      touched in one session
- [ ] `--use NAME` inside a session, naming a vault not yet `use`d,
      prompts to unlock it and then operates against it — without
      changing which vault is current
- [ ] `exit`/EOF terminates the REPL cleanly, and every held `Identity`
      is `Close()`d on the way out
- [ ] Idle timeout: with simulated/injected elapsed time past
      `idle_timeout`, the next `Session` call against that vault re-prompts
      as if `Lock` had been called
- [ ] An idle timeout re-lock calls `Identity.Close()` — the same code
      path as an explicit `Lock`, not a parallel one
- [ ] Session command history contains typed command lines (e.g. `show
      protonmail`) but never a decrypted `value`/`fields` value
- [ ] The history file is created `0600`
- [ ] The prompt renders `{vault}`, `{lock}`, and `{dirty}` tokens from
      `[shell].prompt` in global config, and reflects lock state changing
      within a session
- [ ] the REPL is a thin wiring layer: it parses a typed line into the
      corresponding `Session` call and renders the result — one wiring
      test suffices here, not a re-test of `Session` behavior
- [ ] An unknown REPL command reports usage and does not terminate the
      session
- [ ] The full M6 test suite passes unmodified on Windows, not just
      Linux/macOS — `Session`'s locking/idle-timeout behavior doesn't
      depend on any POSIX-only mechanism

## Implementation

- [ ] `Session` library type: `Use`/`Lock`/`Status`, holding each vault's
      `Identity` (already page-locked and core-dump-protected per M2)
      across multiple calls; idle timeout re-lock calls `Identity.Close()`
      the same as an explicit `Lock`
- [ ] Injectable clock behind the idle timeout
- [ ] REPL loop in `cmd/gage`: thin terminal wiring over `Session`
      (`use`/`lock`/`status`/`exit`/`help`), including rendering an
      ambiguous-query candidate list as the `[1-2]` prompt shown in the
      design doc — the one place this wiring is more than plain dispatch
- [ ] Prompt template rendering (`{vault}`, `{lock}`, `{dirty}`) from
      `[shell].prompt`
- [ ] History file handling: `[shell].history_file`, `0600`, command
      lines only — never a decrypted value
- [ ] Entry commands ported to work against `Session`'s current vault,
      including ad-hoc `--use NAME`
- [ ] CI/test coverage on Windows in addition to Linux/macOS for this
      milestone specifically — it's the first point session state (idle
      timeout, multi-vault `Identity` caching) needs to be proven to
      behave identically across all three, not just compile

## Definition of done

Full test list green on all three CI platforms, with the Windows run
being the unmodified suite. Both invocation modes work and share every
`Vault` method between them.

## Affects later milestones

- **M7's metadata index lives on `Session`**, keyed alongside each
  vault's cached `Identity` — never on `Vault`.
- M8's auto fetch+pull hooks `Vault.Unlock`, which both `Session.Use` and
  the one-shot handler call — so the hook must not be wired into the
  `use` REPL command specifically.
- M10's opportunistic trust-cache warning rides the same unlock hook.
