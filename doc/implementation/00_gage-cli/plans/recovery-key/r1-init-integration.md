# R1 — Recovery key: `gage init` integration

[← R0](r0-library.md) · [plan index](index.md) · next: [R2 — `recovery verify` and docs](r2-verify-and-docs.md)

> **Recommended model: Opus.** This is the security-sensitive milestone: ordering (never show before the vault is committed, never leak on a failure path), secret hygiene across stdout, stderr, disk and git objects, and non-terminal edge cases. Mistakes here are quiet, so the stronger model is worth it. Follow with a focused secret-handling review before merging.

## Goal

`gage init` generates the recovery key by default, registers it as
`recovery-paper-key`, and shows the private key exactly once after the
vault exists, behind a confirmation, with plain warnings about custody.
The key is never stored.

## Depends on

- **R0** — `NewRecoveryKey`, `ExtraRecipients`, `RecoveryDeviceLabel`.
- `renderQR`, `Prompter.Value`, `atomicfile`, `zero`.

## Design references

- [index.md](index.md) decisions D1, D2, D3, D5, D7.
- [Design doc, `gage init`](../../tdds/gage-cli-design.md) — the flag surface
  this extends.

## Decisions to make first

None open on entry. Four were settled while implementing, and are recorded
here rather than left to be re-derived from the code:

- **N = 6, and the confirmation is case-insensitive and whitespace-trimmed.**
  It is an attention check, not authentication: nothing is decided by the
  answer except whether init exits cleanly, since the key is already a
  recipient by the time it is asked. `recoveryConfirmChars` and
  `maxRecoveryConfirmAttempts` (3, matching `maxPassphraseAttempts`) are
  pinned in tests.
- **A refused confirmation fails the command but keeps the vault.** By the
  time the question is asked the vault is created, committed and
  registered; unmaking a good vault over a fumbled prompt would be worse
  than the thing the prompt guards against. The error says so plainly, and
  does not reprint the key — it is still on screen, and a second copy is
  one more thing to clear. Exit code: `Conflict`.
- **Both stdin and stderr must be terminals, not just stdin** (a widening
  of D2). The key is written to stderr and the confirmation is read from
  stdin, so `gage init foo 2>build.log` satisfies one and not the other —
  and is precisely the run that would file an unencrypted private key into
  a build log. This added `App.IsErrTerminal`, which falls back to
  `IsTerminal` when nil, so tests are unaffected and only `Run` sets it.
- **`--recovery-key-out` never overwrites an existing file**, and its path
  is checked before anything is created. The realistic thing at a path
  named `recovery.key` is another vault's recovery key.

Three more were settled by the secret-handling review, after the first
implementation pass:

- **`--recovery-key-out` refuses any destination gage manages.** Inside a
  vault was a data-loss bug, not a papercut: the next write after an
  interrupted one runs `resetDirtyWorkTree` → `gitrepo.ResetHard`, which
  removes untracked files, so gage deleted the user's only copy of an
  unencrypted key and reported it as discarding an interrupted write. The
  check now refuses the vault being created, any vault found by walking up
  for `.gage/config.toml` (so unregistered ones too), and anything under
  `$GAGE_DATA`, which `scripts/resetlocalstate` exists to delete. It runs
  first, before the existence and parent-directory stats, because it is the
  only reason that holds when nothing on the path exists yet.
- **A failed delivery no longer skips the rest of init.** The error is held
  rather than returned: the summary still prints and `--remote` still
  publishes, and the command then exits on the held error. Returning early
  left a vault created, registered and silently unpushed. When the push
  fails too, the delivery error is the one that sets the exit code — an
  unpublished vault can be pushed again, a key nobody has cannot be
  reissued — and the push failure is reported alongside it.
- **The destination's writability is proven, not assumed.** Stat says
  nothing about a read-only or full directory, so the pre-flight now creates
  and removes a probe file. Without it the failure landed after the vault
  existed with the key as a recipient, and the error blamed the disk
  changing underneath for what was deterministic.

**Accepted gap: the recovery secret is never page-locked**, unlike identity
secrets (`identityfile.go` uses `memlock.Alloc`), so it can reach swap.
`showRecoveryKey` has to convert it to a Go string for `renderQR` and for
printing, and that copy can be neither locked nor zeroed; locking only the
`[]byte` beside it would look like a protection it does not provide.
Closing this properly means reworking the shared QR path to take `[]byte`,
which is out of scope here. Recorded rather than quietly left.

One consequence worth flagging: with the key on by default and D2 failing
closed, every existing test that ran `init` without a terminal now states
`--no-recovery-key`. That is ~57 call sites across 10 test files,
mechanical but wide, and it is why this milestone's diff is larger than its
code.

## Tasks

- [x] `runInit`: generate the key after the device identity, pass it via
      `ExtraRecipients`, call `Create`, roll back only the identity on
      failure (the recovery key needs no rollback and has not been shown).
- [x] `--no-recovery-key` opts out; no key generated, nothing shown.
- [x] `--recovery-key-out FILE`: write the secret to a `0600` file via
      `atomicfile` instead of the terminal; nothing secret on stdout.
- [x] Fail-closed rule (D2): no terminal and neither flag given is a
      Usage error raised before anything is created.
- [x] `showRecoveryKey` in new `cmd/gage/recoverykey.go`, called only
      after `Create` succeeds: key as text plus QR (`renderQR`) on stderr;
      warning that the key is unencrypted, grants full vault access to
      anyone holding it, cannot be shown again, and must be stored
      offline (paper or offline encrypted drive; never cloud-synced
      notes, password managers, email or photos; trusted printer or
      handwrite; clear scrollback afterwards).
- [x] Confirmation (D3): re-type the last N characters via masked
      `Prompter.Value`; a wrong or declined answer is handled per the
      test below.
- [x] Zero the secret bytes on every exit path (`defer`).
- [x] Output gains one line naming the recovery recipient's public key.
- [x] Reject `--device recovery-paper-key` before any file is written.
- [x] Update `--recipient` help text so it no longer presents itself as
      the recovery mechanism; update Long help for the new default.

## Tests (write first, in-process via `runCLIWithPrompter` and `isolateXDG`)

- [x] Default `init` yields two recipients, `<device>` and
      `recovery-paper-key`, in `config.toml` and `.age-recipients`.
- [x] An entry encrypted at init time decrypts with the recovery secret
      alone.
- [x] Hygiene: after `init`, walk `XDG_DATA_HOME`, the vault directory and
      `.git` objects; the secret string appears nowhere. It appears
      exactly once across stdout+stderr.
- [x] Warning text (unencrypted, full access, offline storage) appears
      alongside the key.
- [x] Failing `Create` shows no key and leaves no identity or vault
      (extends `TestInitLeavesNoVaultBehindWhenCreateFails`).
- [x] `--no-recovery-key`: one recipient, no key output.
- [x] `--device recovery-paper-key` rejected, exit code pinned, no files
      written.
- [x] No terminal and no flags: fails closed before creating anything,
      exit code pinned.
- [x] No terminal with `--recovery-key-out`: file is `0600`, contains the
      key, and stdout contains no secret.
- [x] Confirmation: correct re-type completes init; wrong re-type is
      handled as specified (the vault already exists at that point, so
      the behavior and its message are pinned in the test) and the key is
      never re-displayed.
- [x] Existing `init` tests unchanged apart from stating their recovery
      flag or terminal fixture.

## Definition of done

Every item green on all three CI platforms, `make lint` and `make test`
clean, plus a focused review of secret handling (stdout/stderr/disk/git,
failure paths, zeroing).

Verified beyond the test suite, against the real binary: a piped `init`
refuses; `--recovery-key-out` writes a 0600 file whose key appears nowhere
under any XDG root; an entry inserted afterwards decrypts with the recovery
key alone; and a pty run renders the key, the QR, the warnings and the
confirmation prompt in that order.
