# R1 — CLI coverage (`cmd/gage`) and the gate config

[plan index](index.md) · previous: [R0 — Library coverage](r0-library-coverage.md)

> **Recommended model: Opus.** The first task is a security boundary — where an unencrypted recovery private key is allowed to land — and the prompter, clipboard and secret-output tasks touch secret handling. The rendering tasks would not on their own justify it; the first four would.

## Goal

Close the honest gaps in `cmd/gage`: the recovery-key destination checks,
the prompter's retry and refusal policy, the REPL's usage errors, the
log/history and sync rendering branches, and the pure parsing helpers.
Then bring the coverage config in line with what it actually gates.

Everything in-process, driven through `rootCmd.Execute()` with injected
IO and a fake `Prompter` — no new pty tests.

## Depends on

- R0 (branch off it).
- Existing helpers: `runCLI`, `runCLIWithTerminal`, `runCLIWithPrompter`,
  `runCLIWithPrompterAndStdin`, `runCLIWithApp`, `runSessionScript`,
  `script`, `fakePrompter`, `isolateXDG`, `initEntryTestVault`
  (`cmd/gage/testutil_test.go`); `decliningPrompter`
  (`trustcache_cli_test.go`); the fake clipboard and fake timer already
  used by `clip_test.go`.

## Design references

- [Design doc, "Local identity storage"](../../tdds/gage-cli-design.md) —
  why a recovery key must not land where gage will delete it.
- [Recovery-key plan, R1](../recovery-key/r1-init-integration.md) — the
  destination rules being tested here were settled there.
- [index.md](index.md) decisions D2, D3, D4, D5, D6.

## Decisions to make first

None open. Two conventions to hold to:

- **Build paths with `filepath.Join`, never literal separators.** Tasks 1
  and 11 touch path semantics that differ on Windows, and CI runs there
  natively.
- **The clipboard and timer fakes already exist.** Task 11 uses them
  through the existing seams; it does not add a new one (index.md, D2).

## Tasks

Tests first, at task granularity.

### 1. Recovery-key destination safety — `cmd/gage/recoverykey.go`

Extends `init_recoverykey_test.go`, which already covers "inside the
vault being created" and the relative-path case.

- [x] ~~`--recovery-key-out` pointing under `$GAGE_DATA`~~ — already
      covered by the existing
      `TestInitRefusesToWriteTheRecoveryKeyWhereGageWillDeleteIt`, whose
      table includes a `$GAGE_DATA` case. Verified against the profile,
      not assumed.
- [x] The parent directory does not exist → `Usage`, "cannot write".
- [x] The parent path is a regular file → refused, nothing written.
      **Not** `Usage` + "is not a directory" as this bullet assumed —
      see **F1** under "Findings" below.
- [x] `pathIsInside` with an empty root → `(false, nil)`. Driven
      directly: neither caller can pass an empty root today.
- [x] ~~The ancestor walk reaching the filesystem root without finding a
      vault~~ — already covered; every accepted `--recovery-key-out`
      outside a vault walks to the root and breaks. Verified against the
      profile.

#### Findings

**F1 — a `--recovery-key-out` parent that is a regular file is refused as
`Internal`, not `Usage`.** `checkRecoveryKeyOutPath` clearly intends to
answer this with `Usage` and "cannot write X: Y is not a directory" — the
branch is right there. It is not reached on Unix: the earlier
"does the destination already exist?" `os.Stat(path)` fails first with
`ENOTDIR`, for which `os.IsNotExist` is **false**, so control takes that
check's `!os.IsNotExist` arm and returns `exitcode.Internal` wrapping a
bare `stat ...: not a directory`. Windows maps the same situation to
`ERROR_PATH_NOT_FOUND`, which `os.IsNotExist` does recognize, so the
intended branch is likely reachable there — i.e. the exit code for one
user mistake differs by platform.

Nothing unsafe follows from it: the destination is refused either way and
no key is written. What is wrong is the taxonomy (`Internal` means
"unexpected", and pointing at a file is an ordinary usage error) and the
message quality. Recorded rather than fixed, because this milestone
changes no production code (Definition of done). The fix is a one-line
reorder — stat the parent before testing the destination's existence, or
treat `ENOTDIR` alongside `IsNotExist` — and wants its own issue.
`TestRecoveryKeyOutRejectsAParentThatIsARegularFile` deliberately leaves
the exit code unpinned so it does not cement the inconsistency; tighten it
when the fix lands.

**F2 — `gage sync` reports a push even when nothing was sent.**
`RemoteSyncer.Push(ctx) error` returns only an error, and `gitSyncer.Push`
discards the `bool` that `gitrepo.Push` computes for exactly this question
("did anything actually go out?" — it returns false on
`git.NoErrAlreadyUpToDate`). `remotesync.go` then sets
`report.Pushed = true` on any successful call, so a vault that is already
in sync reports `gage: pushed "personal" to origin` on every `gage sync`.

Two consequences. The user-visible one: someone running `gage sync` to
confirm a change went out is told it did, whether or not it did. The
structural one: `syncLine`'s already-in-sync arm is dead for any vault
that has a remote, which is why this milestone pins it against `syncLine`
directly rather than end to end.

The fix is to widen the seam — `Push(ctx) (bool, error)` — and thread the
answer into `report.Pushed`; the injected fakes would need the same
signature. Recorded rather than fixed, for the same reason as F1: this
milestone changes no production code.
`TestSyncReportsAPushEvenWhenNothingWasSent` pins today's behaviour and
skips with an instruction the moment it changes, so the fix is
self-announcing.

### 2. Prompter — `cmd/gage/prompter.go`

Extends `prompter_test.go`.

- [x] `recipientChangeCount` table: added-only singular, added-only
      plural, removed-only, added and removed together, and the case with
      neither → "The recipient list changed".
- [x] `ConfirmRecipientChange` renders the `OnlyInConfig` lines when the
      two files disagree, alongside the existing `OnlyInRecipientsFile`
      ones.
- [x] An empty passphrase at a new-passphrase prompt → "an empty
      passphrase is not accepted", then re-asks rather than failing.
- [x] `Choose` on an empty candidate list → `Internal`.
- [x] `Choose` with an out-of-range answer then a non-numeric one → the
      "enter a number between 1 and N" retry each time, then `Ambiguous`
      after `maxChooseAttempts`.
- [x] `ResolveConflict` with `maxConflictAttempts` unrecognized answers →
      `Conflict`, with nothing applied. This is the "no default and no
      guessing" rule; it should have a test that fails if a default is
      ever added.

### 3. REPL — `cmd/gage/repl.go`

- [x] `lock` with no arguments and nothing unlocked → "gage: no vaults
      are unlocked in this session."
- [x] Usage errors: `lock a b`, `use` with zero arguments, `use` with
      two, `status extra`.
- [x] A blank line and a whitespace-only line are skipped without
      dispatching a command.

### 4. log and history — `cmd/gage/log.go`

- [x] `log` on an entry with no commit history → the stderr notice,
      exit 0.
- [x] `history --decrypt` across a deletion → the "(entry deleted)"
      revision line.
- [x] A description-only change between revisions is marked in the diff.
- [x] A field present only in the older revision is listed.
- [x] A query that is not a title but parses as a UUID falls through to
      the UUID path rather than reporting not-found — already covered by
      the existing `TestLogShowsADeletedEntrysEnd`. Its uncovered sibling
      was the *other* arm (neither a title nor a UUID → the original
      not-found), which is what the new test pins.

### 5. auth — `cmd/gage/auth.go`

- [x] `auth status` with no tokens stored and no current vault → "gage:
      no tokens are stored on this machine".
- [x] `auth status` with no tokens but a current vault with a remote →
      falls back to reporting that host.
- [x] `auth login` where the global config has no origin but the
      repository does → uses the repository's, since the repository is
      the authority.
- [x] `auth login` on a vault with no remote and no `--host` → `Usage`,
      naming the vault.

### 6. clone URL and name parsing — `cmd/gage/clone.go`

Pure table tests; no vault needed.

- [x] `vaultNameFromURL`: https, scp-style (`git@host:user/repo.git`), a
      plain local path, a `.git` suffix, trailing slashes, empty →
      `Usage`. Correction: `"/"` is *not* "cannot infer" — stripping its
      trailing slash leaves an empty string, so it takes the empty-URL
      arm. The "cannot infer" arm is reached by `git@host:` and the bare
      relative forms.
- [x] `checkVaultName`: empty, `.`, `..`, `/`, `\`, a NUL byte, and a
      non-clean name (`a/../b`). This is attacker-influenced input on its
      way to becoming a directory under `$GAGE_DATA`, so each case pins
      the refusal. Added a test for the *pairing* too: `..` has a valid
      `path.Base`, so `vaultNameFromURL` infers it happily and only
      `checkVaultName` refuses it — clone runs both, and dropping the
      second call would not make either function look wrong on its own.

### 7. Entry rendering helpers — `cmd/gage/entry.go`

- [x] `trimOneTrailingNewline`: `"\r\n"`, `"\n"`, `"\n\n"` (exactly one
      stripped), and no trailing newline.
- [x] `lsDate` on a zero timestamp and `lsField("")` → the placeholder,
      driven through an entry whose YAML omits the dates.
- [x] `shortEntryID` on a string shorter than `shortEntryIDLen`.
- [x] `alignCatOutput` on bytes that are not a YAML mapping → returned
      unchanged.
- [x] `ls` with no entries and `search` with no results → the empty
      early-return paths, printing nothing to stdout.

### 8. Completion — `cmd/gage/complete.go`

- [x] `resolveSessionCommandName("")` → unchanged, not an
      ambiguous-everything error.
- [x] `completionWords("")` and a whitespace-only head → no words, no
      partial.
- [x] A hidden flag is not offered by `flagCandidates`; a session-hidden
      subcommand is not offered by `subcommandCandidates`.
- [x] `leafPositionalCandidates` at a second positional (`rename <title>
      <new>`) → nil, so a new title is never completed against existing
      ones.
- [x] A bare `-` at the top level → root's flags.

### 9. help and registry — `cmd/gage/help.go`, `registry.go`

- [x] Help renders a command's aliases sorted alongside its name (the per-command page's renderer, distinct from the top-level listing's, which `TestAliasesListedAlongsideCanonicalName` already covered).
- [x] ~~`help nosuchtopic` → `Usage`, "unknown help topic"~~ — already
      covered by `TestHelpUnknownTopicFailsWithUsageCode`, which takes
      Cobra's own "unknown command" arm. The neighbouring `target == root`
      guard could not be reached from the CLI (a bare `help --` renders
      root help instead), so it is left as the defensive check it is.
- [x] The group listing renders a group that is missing from
      `groupOrder` — correction: that fallback is not "dedupes groups"
      as this bullet said, it is "a forgotten `groupOrder` update doesn't
      hide a command". Tested through `groupCommands` with a synthetic
      entry, since every real group is in `groupOrder`.

### 10. Prompter decorators — `cmd/gage/assumeyes.go`, `script.go`

- [x] `--yes` wrapping a conflict-capable prompter still routes
      `ResolveConflict` to the inner asker — `--yes` must not answer a
      conflict.
- [x] `terminalPrompterOf` (the plan called it `unwrapPrompter`) walks a `promptDecorator` chain to the real
      prompter.
- [x] `--script` / `--stdin` invoked from inside a session → `Usage`.
      Driven against `checkScriptFlags` directly: from the prompt, a line
      of only flags is refused earlier as an unknown command, and
      `--stdin ls` takes the `cmd != root` arm, so this guard is not
      reachable through the REPL today.
- [x] `noCreatePrompter` passes a non-create unlock request through
      unchanged.

### 11. shell, sync, clipboard

- [x] `cmd/gage/shell.go`: `~` and `~/sub` in a configured path expand
      against `$HOME` (`expandGagePath`); `vaultIsDirty` (the plan called
      it `isGitVault`) is false for an unregistered vault and for one
      whose files are gone.
- [x] `cmd/gage/sync.go`: the `"%d entries"` plural and the
      recipients-conflict line. The already-in-sync default message is
      **not reachable through `gage sync`** — see **F2** below — so it is
      pinned against `syncLine` directly, with a second test documenting
      the current behaviour.
- [x] `cmd/gage/clipboard.go`: a write failure through the existing fake
      clipboard → `Internal` with a clear message (and no secret in it);
      `clear` with nothing pending → a no-op that leaves a stranger's
      clipboard alone; `scheduleClear` with nothing pending arms no
      timer. The timer *firing* was already covered by `clip_test.go`.

### 12. Coverage config

Last, so it describes shipped state.

- [x] Added `^internal/gage/gittest/` to `exclude.paths` in
      `.testcoverage.yml` and to `ignore` in `codecov.yml`, extending the
      existing cross-reference comments rather than adding a second note.
      Verified with a real `make cover-check`: the denominator drops from
      6499 to 6402 statements, so the exclusion took effect rather than
      being silently ignored.
- [x] Left `threshold.total` at **80** (index.md, D3), and rewrote the
      comment above it to state the measured figure (89.6%) and why the
      gate is deliberately well below it: a floor against a regression,
      not a ratchet that turns every change touching untested
      error-handling red.

## Definition of done

Every box above ticked. `make lint` and `make test` clean.

**Achieved: 89.6%** (5735/6402) excluding `scripts/` and
`internal/gage/gittest/`, as reported by a real `make cover-check`,
against this doc's original **>= 90%** estimate. Up from 87.95% at the
end of R0.

The shortfall is recorded rather than closed, for the same reason R0's
was: every task-list item is done, and what remains uncovered in
`cmd/gage` is, on inspection, almost entirely the
`if err != nil { return exitcode.Wrap(...) }` shape the plan index rules
out of scope (D2), plus three things it rules out for their own reasons —
`main.go` (reachable only from a real process), the `readline`-backed
line editor (D5: no new pty tests), and the `systemClipboard`/real-timer
adapters, which exist precisely so the rest is testable without them.
The 90% figure was a planning-time estimate from the task list, not a
measurement of what is reachable.

Beyond the task list, measuring the real profile turned up a handful of
in-family gaps that were closed too: `shortHash`, `recipient verify`'s
config-only direction and both `--repair` outcomes, `identity list` on a
device holding none, a bare `lock` over a session that *is* holding keys,
`padKeyLines` with no keys, `mapReadlineErr`, history's blank-line and
unreadable-file paths, and an empty answer at the enrollment-code prompt.

**Two findings are recorded above and fixed in neither place: F1** (a
`--recovery-key-out` parent that is a regular file is refused as
`Internal`, not `Usage`) and **F2** (`gage sync` reports a push when
nothing was sent). Both want their own issue.

No file outside `_test.go` changed except `.testcoverage.yml` and
`codecov.yml` in task 12.
