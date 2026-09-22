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

- [ ] `--recovery-key-out` pointing under `$GAGE_DATA` → refused, with a
      message naming the data directory.
- [ ] The parent directory does not exist → `Usage`, "cannot write".
- [ ] The parent path is a regular file → "is not a directory".
- [ ] `pathIsInside` with an empty root → `(false, nil)`.
- [ ] The ancestor walk reaching the filesystem root without finding a
      vault → allowed, not an error.

### 2. Prompter — `cmd/gage/prompter.go`

Extends `prompter_test.go`.

- [ ] `recipientChangeCount` table: added-only singular, added-only
      plural, removed-only, added and removed together, and the case with
      neither → "The recipient list changed".
- [ ] `ConfirmRecipientChange` renders the `OnlyInConfig` lines when the
      two files disagree, alongside the existing `OnlyInRecipientsFile`
      ones.
- [ ] An empty passphrase at a new-passphrase prompt → "an empty
      passphrase is not accepted", then re-asks rather than failing.
- [ ] `Choose` on an empty candidate list → `Internal`.
- [ ] `Choose` with an out-of-range answer then a non-numeric one → the
      "enter a number between 1 and N" retry each time, then `Ambiguous`
      after `maxChooseAttempts`.
- [ ] `ResolveConflict` with `maxConflictAttempts` unrecognized answers →
      `Conflict`, with nothing applied. This is the "no default and no
      guessing" rule; it should have a test that fails if a default is
      ever added.

### 3. REPL — `cmd/gage/repl.go`

- [ ] `lock` with no arguments and nothing unlocked → "gage: no vaults
      are unlocked in this session."
- [ ] Usage errors: `lock a b`, `use` with zero arguments, `use` with
      two, `status extra`.
- [ ] A blank line and a whitespace-only line are skipped without
      dispatching a command.

### 4. log and history — `cmd/gage/log.go`

- [ ] `log` on an entry with no commit history → the stderr notice,
      exit 0.
- [ ] `history --decrypt` across a deletion → the "(entry deleted)"
      revision line.
- [ ] A description-only change between revisions is marked in the diff.
- [ ] A field present only in the older revision is listed.
- [ ] A query that is not a title but parses as a UUID falls through to
      the UUID path rather than reporting not-found.

### 5. auth — `cmd/gage/auth.go`

- [ ] `auth status` with no tokens stored and no current vault → "gage:
      no tokens are stored on this machine".
- [ ] `auth status` with no tokens but a current vault with a remote →
      falls back to reporting that host.
- [ ] `auth login` where the global config has no origin but the
      repository does → uses the repository's, since the repository is
      the authority.
- [ ] `auth login` on a vault with no remote and no `--host` → `Usage`,
      naming the vault.

### 6. clone URL and name parsing — `cmd/gage/clone.go`

Pure table tests; no vault needed.

- [ ] `vaultNameFromURL`: https, scp-style (`git@host:user/repo.git`), a
      plain local path, a `.git` suffix, trailing slashes, empty →
      `Usage`, and `"/"` → "cannot infer".
- [ ] `checkVaultName`: empty, `.`, `..`, `/`, `\`, a NUL byte, and a
      non-clean name (`a/../b`). This is attacker-influenced input on its
      way to becoming a directory under `$GAGE_DATA`, so each case pins
      the refusal.

### 7. Entry rendering helpers — `cmd/gage/entry.go`

- [ ] `trimOneTrailingNewline`: `"\r\n"`, `"\n"`, `"\n\n"` (exactly one
      stripped), and no trailing newline.
- [ ] `lsDate` on a zero timestamp and `lsField("")` → the placeholder,
      driven through an entry whose YAML omits the dates.
- [ ] `shortEntryID` on a string shorter than `shortEntryIDLen`.
- [ ] `alignCatOutput` on bytes that are not a YAML mapping → returned
      unchanged.
- [ ] `ls` with no entries and `search` with no results → the empty
      early-return paths, printing nothing to stdout.

### 8. Completion — `cmd/gage/complete.go`

- [ ] `resolveSessionCommandName("")` → unchanged, not an
      ambiguous-everything error.
- [ ] `completionWords("")` and a whitespace-only head → no words, no
      partial.
- [ ] A hidden flag is not offered by `flagCandidates`; a session-hidden
      subcommand is not offered by `subcommandCandidates`.
- [ ] `leafPositionalCandidates` at a second positional (`rename <title>
      <new>`) → nil, so a new title is never completed against existing
      ones.
- [ ] A bare `-` at the top level → root's flags.

### 9. help and registry — `cmd/gage/help.go`, `registry.go`

- [ ] Help renders a command's aliases sorted alongside its name.
- [ ] `help nosuchtopic` → `Usage`, "unknown help topic".
- [ ] The group listing dedupes groups rather than repeating one per
      command.

### 10. Prompter decorators — `cmd/gage/assumeyes.go`, `script.go`

- [ ] `--yes` wrapping a conflict-capable prompter still routes
      `ResolveConflict` to the inner asker — `--yes` must not answer a
      conflict.
- [ ] `unwrapPrompter` walks a `promptDecorator` chain to the real
      prompter.
- [ ] `--script` / `--stdin` invoked from inside a session → `Usage`.
- [ ] `noCreatePrompter` passes a non-create unlock request through
      unchanged.

### 11. shell, sync, clipboard

- [ ] `cmd/gage/shell.go`: `~` and `~/sub` in a configured path expand
      against `$HOME`; `isGitVault` is false for a non-git registry entry
      and for a missing path.
- [ ] `cmd/gage/sync.go`: the already-in-sync default message; the
      `"%d entries"` plural; and the recipients-conflict case adding
      "gage will never merge them for you."
- [ ] `cmd/gage/clipboard.go`: a write failure through the existing fake
      clipboard → `Internal` with a clear message; `clear` with nothing
      pending → a no-op; the timer firing through the existing fake timer
      → clears.

### 12. Coverage config

Last, so it describes shipped state.

- [ ] Add `^internal/gage/gittest/` to `exclude.paths` in
      `.testcoverage.yml` and to `ignore` in `codecov.yml`, with a
      comment matching the existing `^scripts/` one: `gittest` is test
      scaffolding, never shipped in the `gage` binary, and its uncovered
      statements are all `t.Fatalf` branches. The two files are separate
      tools with no shared config format, so keeping them in sync is a
      deliberate duplication — the existing comments say so; extend them
      rather than adding a second note.
- [ ] Leave `threshold.total` at **80** (index.md, D3). Update the
      comment above it to state the then-current measured figure and why
      the threshold is deliberately well below it.

## Definition of done

Every box above ticked, green on Linux, macOS and Windows CI. `make lint`
and `make test` clean. `make cover` total excluding `scripts/` and
`internal/gage/gittest/` at **>= 90%** — report the measured figure in
the sub-issue.

No file outside `_test.go` changed except `.testcoverage.yml` and
`codecov.yml` in task 12.
