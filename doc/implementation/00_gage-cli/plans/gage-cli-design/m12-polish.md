# M12 — Polish / output modes

[← M11](m11-cross-vault-sharing.md) · [plan index](index.md)

> **Recommended model: Sonnet.** Independent, individually deferrable items. All four up-front decisions are resolved below, so what's left is implementation against a settled spec.

## Goal

The remaining output modes and conveniences. These items are independent
of each other and of the trust model — good candidates to parallelize or
reorder freely, and safe to defer individually without blocking anything
else.

Two of them are more than polish despite living here: `--clip` and
`--qr` are the design's answer to principle 6 ("plaintext should be
surprising to produce"), and `history --decrypt` is explicitly the most
dangerous command in the tool.

## Depends on

- **M5** — `show`/`generate` exist and are on the resolver.
- **M6** — `Session`, which `--script`/`--stdin` drives non-interactively.
- **M8a** — commit history for `log`/`history`.

## Design references

- ["Notes on `show`"](../../tdds/gage-cli-design.md) — `--field`, `-c`, `-q`
  semantics and why `--field NAME` with `-q` scopes the QR
- ["Sync (vault-generic, git-implemented today)"](../../tdds/gage-cli-design.md)
  — `log` vs `history --decrypt`
- ["`history --decrypt` is still the one meaningfully more dangerous tier"](../../tdds/gage-cli-design.md)
- ["Non-interactive session mode"](../../tdds/gage-cli-design.md)

## Decisions to make first

All four are resolved. Two of them extend what the design doc currently
says rather than just reading it back, so they're also filed as
amendments [A17](open-questions.md#a17) and [A18](open-questions.md#a18).

- **Clipboard clear semantics.** Resolved: **the clear always happens
  inside the process that wrote the clipboard, and never in a forked
  child.** Principle 5 decides it — a clearer that outlives `gage` is the
  daemon the design spends a whole section refusing, and it would have to
  carry either the secret or a fingerprint of it past the lifetime of the
  process that unlocked. There's a platform fact pushing the same way: on
  X11/Wayland the clipboard is *owned* by the process that set it, so a
  one-shot that exits immediately doesn't reliably leave anything on the
  clipboard at all. That's why `pass -c` forks; `gage` blocks instead.
  Which shape that takes falls out of the mode, so neither mode is a
  compromise:
  - **One-shot** `gage show -c`: copy, write a one-line notice to
    **stderr** (`copied to clipboard; clears in 45s — Ctrl-C to clear
    now`), block until the timeout, clear, exit 0. Ctrl-C during the wait
    clears immediately and also exits 0 — it's the "done pasting" escape,
    not an abort. Nothing plaintext ever reaches stdout under `-c`.
  - **Session** `[personal🔓] gage> show foo -c`: the session process is
    already long-lived, so the clear is a timer rather than a block. The
    prompt returns immediately, and session exit (`exit`, Ctrl-D, kill)
    clears any still-pending copy the same way it drops key material.

  "Blocking is surprising in a script" is answered by scope rather than by
  weakening the guarantee: `-c` is an interactive affordance, and the
  scripting spellings are plain `show` (stdout) and `cat`.

  **Clear-only-if-unchanged: yes**, keyed on a SHA-256 of the bytes gage
  wrote, not on the bytes themselves — the pending-clear record must not
  be a second copy of the secret sitting in memory for the whole timeout.
  At clear time: read the clipboard, hash, compare, and clear only on a
  match. Accepted risk, to be recorded as such: a SIGKILL leaves the value
  on the clipboard. That's the same posture as the documented Windows
  core-dump gap — a stated limit, not a guarantee gage can't back.

- **Clipboard timeout value.** Resolved: **45 seconds, configurable as
  `clipboard_timeout` in `[shell]`.** 45s is `pass`'s long-established
  default (`PASSWORD_STORE_CLIP_TIME`), so it's the number users already
  have a feel for. `[shell]` is the right table because it already holds
  exactly this class of setting — `prompt`, `idle_timeout`,
  `history_file` are all CLI-terminal state a GUI frontend wouldn't
  share. **No `0`/disable value for now**, on the project's own
  reversible-choice rule (the one `init`/`clone` session-availability
  cites): adding an opt-out later is additive, taking one away is
  breaking. A configured duration below `1s` is a config error, not a
  silently-rounded value.

- **`--script`/`--stdin` and unlock.** Resolved: **`GAGE_PASSPHRASE`,
  with a fixed source order and a fail-fast on the miss.** The two modes
  differ in a way that decides most of this — `--script FILE` leaves
  stdin free, so the existing terminal prompter already works when a
  human runs it, while `--stdin` has consumed stdin as the command
  stream and can therefore never prompt (this is precisely why the
  design refused to name the per-value flag `--stdin`). The order:
  1. `GAGE_PASSPHRASE` set → use it.
  2. Otherwise, stdin is a terminal → prompt as today. This covers
     `--script` run by a human.
  3. Otherwise → fail immediately with `exitcode.LockedOrAuth` (5) and a
     message naming the variable. Never a read that can't be answered,
     and never an EOF misreported as a wrong passphrase.

  Three properties fixed as part of the decision:
  - **It's a `cmd/gage` decorator, not a library concept** — an
    `envPrompter` shaped exactly like `assumeYesPrompter`. Nothing in
    `internal/gage` learns what an environment variable is.
  - **`PurposeUnlock` only, never `PurposeCreate`.** A new identity's
    passphrase can't be checked against anything, so a typo there is
    unrecoverable; `init`/`identity add` keep asking a human, twice. The
    existing `UnlockPurpose` type already draws this line, so the carve-
    out is a `switch` on data that's there rather than a special case.
  - **No retry.** The variable gives the same answer every time, so the
    decorator answers attempt 1 and returns an error on attempt 2 —
    mirroring how `maxPassphraseAttempts` already expresses a retry
    policy frontend-side by refusing the next request.

  **No per-vault `GAGE_PASSPHRASE_<VAULT>`**, decided against on cost:
  it buys only the multi-vault script, and that script can just reassign
  `GAGE_PASSPHRASE` before it `use`s the second vault. Not worth a
  vault-name-to-env-name mangling rule to specify, validate and test. A
  single `GAGE_PASSPHRASE` offered to the wrong vault simply fails to
  decrypt — it's a failed unlock, not a disclosure.

  **No `/dev/tty` (or `CONIN$`) fallback.** It would make
  `cat cmds.txt | gage --stdin` promptable from a terminal, but that's
  new per-platform code for the one case the env var already covers, and
  it's additive later if the ergonomics turn out to matter.

- **Does `history --decrypt` require confirmation?** Resolved: **no
  prompt, and no TTY gate either.** The design doc already names its own
  mitigation — it "stays an explicit, separately-named subcommand rather
  than a flag on `log`." The naming *is* the guard, and it's already
  three deliberate acts (the subcommand, the mandatory `--decrypt`, a
  query). A `[y/N]` on top is either unbypassable — breaking CI and the
  recovery case the command exists for — or it needs `--yes`, and
  `assumeyes.go` is emphatic that `--yes` answers *only*
  `ConfirmRecipientChange`, deliberately leaving `Confirm`'s genuinely
  different questions real in a scripted run. Overloading it would undo
  that. A prompt fired on every single invocation also trains the habit
  of hitting `y`, which cheapens the recipient-change prompt that
  actually carries information.

  The rejected alternative, recorded because it's the closest call here:
  refusing to run when stdout isn't a terminal unless forced, which would
  stop `gage history --decrypt foo > audit.txt` from silently writing
  every past value of a secret to disk. Rejected because `gage cat` is
  documented as "always full raw plaintext, for scripting/piping" with no
  such gate (its bytes get a display-only `id` line and column alignment,
  but nothing is withheld or gated), and gating one but not the other is
  an inconsistency users would have to memorize.

  What the decision does fix, both testable: **the query stays
  mandatory** — there is no bulk "decrypt the history of everything"
  form, ever — and nothing it decrypts reaches the session history file,
  which only ever records the query as typed.

- **`ls --header` (post-milestone, #54).** Resolved: **a new `-H`/
  `--header` flag, default off, additive only.** #54 asked for `ls` to
  print a labelled table — a header row, a row of dashes under it, and
  `|`-delimited columns — but M4's original `ls` decision (extended by
  M7) is explicit that the columns are unlabelled and positional
  specifically so the output stays greppable, and that decision is load-
  bearing for anyone piping `ls` into `cut`/`awk`/`wc -l` today. Rather
  than override that default, `--header` sits next to it: plain `gage
  ls` is byte-for-byte unchanged (`TestLsDefaultOutputUnchangedByHeaderFeature`),
  and `-H`/`--header` opts into the table shape for a human reading a
  terminal. Rejected: making headers the default (breaks every existing
  pipeline built on today's rows, and reverses a decision the codebase
  reaffirmed twice) and a TTY-conditional header (would make one-shot
  and session-mode `ls` diverge depending on whether stdout is a
  terminal, breaking the M7 "one renderer, byte-identical in both modes"
  guarantee `TestLsRendersIdenticallyInBothModes` already locks in).

  Table shape: the last column (`updated_by`) is never padded on any
  row, header included, so a long device name never leaves trailing
  whitespace on a data line the way padding every other column does;
  its dash segment is therefore sized off the header label
  (`"updated by"`, 10 chars) rather than the widest device name, so the
  separator lines up with the header text exactly rather than
  overshooting it. Column separator is `" | "` everywhere `--header` is
  set (replacing the default's double-space), and the separator row
  breaks to `+` at each `|` boundary, matching common ASCII-table
  rendering (`psql`/`mysql` CLIs). Empty vault: `--header` still prints
  nothing, matching `ls`'s existing "no rows, no output" rule — a header
  describing zero entries is noise, not a table.

## Tests (write first)

- [x] `--clip` copies plaintext to the clipboard and clears it after the
      configured timeout (clipboard/clock faked in tests)
- [x] `--clip`'s clear does not wipe clipboard contents that changed
      after `gage` wrote them
- [x] One-shot `show -c` blocks until the timeout, then clears and exits
      0; an interrupt during the wait clears immediately and also exits 0
- [x] One-shot `show -c` writes no plaintext to stdout — the "copied"
      notice goes to stderr
- [x] In-session `show -c` returns to the prompt without blocking, and
      the clear fires on the session's own timer
- [x] Exiting a session clears a copy whose timeout hasn't elapsed yet
- [x] `[shell] clipboard_timeout` overrides the 45s default; a value
      below `1s` is rejected as a config error
- [x] `--qr` renders a QR code that decodes back to exactly the requested
      value; `--field NAME --qr` encodes only that field, not the whole
      entry
- [x] `--qr` prints no plaintext alongside the code — it renders
      *instead of* printing the value
- [x] `--field NAME` on `show` extracts the correct value from `fields`
      and errors clearly on an unknown field name
- [x] `generate` respects `-l LENGTH` and `--no-symbols` in the produced
      secret
- [x] `generate -l` with an unreasonably small length is rejected rather
      than producing a trivially weak secret
- [x] `gage log` shows commit timestamps/history only — asserts no
      decrypted content appears in its output
- [x] `gage log` on a vault whose commit messages contain only UUIDs (per
      M4's decision) reveals no titles
- [x] `gage history --decrypt` walks revisions and produces a diff of
      decrypted content across commits
- [x] `gage history --decrypt` with no query is a usage error, not a
      whole-vault dump
- [x] `--script`/`--stdin` runs a sequence of commands non-interactively,
      unlocking each vault at most once
- [x] `GAGE_PASSPHRASE` unlocks under `--stdin` with no prompt and no
      read from the command stream
- [x] A wrong `GAGE_PASSPHRASE` fails after one attempt, not three
- [x] `--stdin` with no `GAGE_PASSPHRASE` and no terminal fails
      immediately with exit code 5, naming the variable — it does not
      block, and does not report a wrong passphrase
- [x] `--script` with a terminal on stdin still prompts normally, env var
      unset
- [x] `GAGE_PASSPHRASE` is ignored for `PurposeCreate` — `init` and
      `identity add` still ask a human, twice
- [x] `--script` aborts on the first failing command rather than
      continuing through the rest of the file
- [x] `--script`/`--stdin` writes nothing to the session history file
- [x] Plain `gage ls` (no `--header`) is unchanged: no `|`, still exactly
      one line per entry — pins #54's `--header` addition as additive
- [x] `gage ls --header` (and its `-H` shorthand) prints a header row
      naming every column, a dash row broken at each `|` boundary, and
      `|`-delimited rows with the right title/id/dates/updated_by
- [x] `gage ls --header` on an empty vault still prints nothing
- [x] `gage ls --header` renders identically in session and one-shot
      mode, same as plain `ls` (M7's one-renderer guarantee)

## Implementation

- [x] `--clip`: clipboard write plus the hash-guarded clear — blocking in
      one-shot, a session timer in-session, cleared on session exit
- [x] `[shell] clipboard_timeout` config plumbing (45s default, `<1s`
      rejected)
- [x] `--qr` (terminal QR, `--field` scoped)
- [x] `--field NAME` extraction
- [x] `generate`'s `-l LENGTH`/`--no-symbols` password-generation logic
      (command itself already exists from M5)
- [x] `gage log`
- [x] `gage history --decrypt`
- [x] Non-interactive session (`--script`, `--stdin`)
- [x] `envPrompter` in `cmd/gage` — `GAGE_PASSPHRASE`, unlock-purpose
      only, single attempt
- [x] `ls --header`/`-H` (#54): labelled, `|`-delimited table rendering
      alongside the existing default rows, one renderer for both

**Implementation note, flagged so it isn't discovered late:** there is no
portable, pure-Go way to touch a system clipboard. The realistic options
are a cgo-backed library (`golang.design/x/clipboard`), which complicates
the three-platform CI build, or shelling out to
`pbcopy`/`xclip`/`wl-copy` (`atotto/clipboard`), which puts plaintext
through a child process's stdin. The "no shelling out" rule in this
project is specifically about `git`, so the second is not forbidden here
— but it is a real trade-off and should be settled deliberately when
`--clip` is picked up, with the plaintext passed on stdin and never as an
argv element.

## Definition of done

Full test list green on all three CI platforms. Every command in the
design doc's command reference now exists.

## Affects later milestones

None — this is the last planned milestone. Remaining scope lives in the
[deferred list](index.md) and [open-questions.md](open-questions.md),
notably [Q-RELEASE](open-questions.md#q-release), which is what stands
between this and something a user can actually install.
