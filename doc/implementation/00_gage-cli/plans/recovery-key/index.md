# Recovery key at `gage init` — implementation plan

Tracked by GitHub issue [#7](https://github.com/distillerylabs/gage/issues/7); one sub-issue per milestone below. R0-R2 built the recovery key and `recovery verify`; R3-R5 make it usable (see "Phase 2" below).

Three sequential milestones that make `gage init` generate an offline
**recovery key** by default: a second age X25519 keypair whose public key
is registered as the `recovery-paper-key` recipient and whose private key
is shown once and never stored. Same rules as the
[core plan](../gage-cli-design/index.md): each milestone leaves the tool
runnable and testable, none reworks a prior one, and the test list is the
definition of done.

**This plan does not restate the shared ground.** Layering rules
(library returns data, `cmd/gage` prints), library choices, test
conventions and model-selection guidance live in the
[core plan index](../gage-cli-design/index.md) and apply unchanged. The
decision register is the core plan's
[open-questions.md](../gage-cli-design/open-questions.md), entry
`Q-RECOVERY-KEY`.

## Why

A vault whose only recipient is one device's passphrase-wrapped identity
file has no recovery story ([design doc, "Local identity storage"](../../tdds/gage-cli-design.md)).
The doc already names the fix, a `recovery-paper-key` recipient, but
`init` only offers `--recipient PUBKEY`, leaving key generation to the
user. This makes the safe setup the default.

## Resolved decisions

| ID | Decision |
|---|---|
| D1 | On by default; `--no-recovery-key` opts out. |
| D2 | No terminal and neither `--recovery-key-out FILE` nor `--no-recovery-key`: fail closed (Usage) before creating anything. |
| D3 | Confirmation: re-type the last N characters through masked `Prompter.Value`. No `Prompter` interface change. |
| D4 | `gage recovery verify` is included, as the last milestone. |
| D5 | Key is written to stderr with the prompts; no screen clearing in v1; documented. |
| D6 | Label is fixed: `recovery-paper-key`. |
| D7 | Bare age secret, **not** passphrase-wrapped. Security rests on physical custody, so output and docs state the storage rules. A later `--wrap-recovery-key` would be additive. |

## Design facts the milestones rely on

- `runInit` (`cmd/gage/init.go`) creates the device identity, builds
  `[devicePub, ...--recipient]`, calls `gage.Create`, and rolls the
  identity back if `Create` fails.
- `buildRecipients` (`internal/gage/vault_create.go`) labels index 0 with
  the device name and the rest `recipient-N`; `Create` has no duplicate
  key/label check.
- The only X25519 generation site is `CreateIdentity`
  (`internal/gage/identityfile.go`).
- `Prompter` has `Confirm`, masked `Value`, `ConfirmDefaultYes` and no
  echoed line read; `fakePrompter.Confirm` always answers yes and `--yes`
  does not bypass `Confirm`.
- `init` is one-shot only and never checks `IsTerminal`.
- `renderQR` (`cmd/gage/qrcode.go`) exists and fits a 74-character key.
- `forbidigo` forbids printing in `internal/gage`.
- `recipient verify` already exists with a different meaning, so the new
  command is `recovery verify`.
- `devicename.Valid("recovery-paper-key")` is true; the label can collide
  with `--device`.

## Milestones

Legend: `[ ]` not started, `[~]` in progress, `[x]` done. Branches are
stacked: `main` <- `7-recovery-key` (these docs) <- R0 <- R1 <- R2.

| # | Milestone | Model | Depends on | Status |
|---|---|---|---|---|
| R0 | [Library](r0-library.md) | Sonnet | none | `[x]` |
| R1 | [`init` integration](r1-init-integration.md) | Opus | R0 | `[x]` |
| R2 | [`recovery verify` and docs](r2-verify-and-docs.md) | Sonnet | R0 (lands after R1) | `[x]` |

After R1, run a focused code review of secret handling before merging.
If R1 proves too large, split it into R1a (flags, non-terminal rule,
ordering, hygiene tests) and R1b (display, QR, warning text,
confirmation).

## Phase 2: making the recovery key usable (R3-R5)

R0-R2 shipped with a limit that the R2 docs state plainly: **gage cannot
unlock a vault with the recovery key.** Only the passphrase method exists,
so the key decrypts entries through stock `age -d -i` but drives no gage
command. A user who loses their only identity file can read their secrets
and cannot get a working gage back without some *other* device that still
has one.

Phase 2 closes that with a deliberately narrow power: **the recovery key
may enroll a fresh device identity, and is then retired.** It is not a
general unlock method, and raw keys never become a normal way to use gage.

Why the design is safe to keep narrow: `Identity` has unexported fields, so
a recovery-scoped identity can exist only inside one library function and
never reaches general CRUD. That is structural, not a convention.

### Decisions

| ID | Decision |
|---|---|
| D8 | The recovery key's only gage power is enrolling a fresh device identity, followed by its retirement. No general unlock method. |
| D9 | Enrollment mints a replacement recovery key in the same commit; `--no-new-recovery-key` opts out. A vault is never left without a safety net by default. |
| D10 | `--replaces DEVICE` removes the lost device's old recipient in the same commit. Without it the old entry is left alone and the new device needs a different name. |
| D11 | `gage recovery rotate` is included, as the last milestone. The current leak-response instructions cannot work as written: a second `recovery-paper-key` collides on the fixed label. |
| D12 | M10's recipient-change confirmation is kept, not bypassed. The recovery identity's label is fixed at `recovery-paper-key`, and only the recovery-labelled key is accepted (a device key pasted here is refused). |

Accepted gap, carried over from R1: a secret passed through
`Prompter.Value` or the display path is a Go string that cannot be zeroed or
page-locked. The buffers gage owns are zeroed.

### Design facts Phase 2 relies on

- `newIdentity` (`internal/gage/unlock.go`) is the one constructor for the
  page-lock and zero lifecycle; `parseIdentityFile`
  (`internal/gage/identityfile.go`) already parses a raw
  `AGE-SECRET-KEY-1...` into a `memlock.Alloc`'d buffer. No `Prompter`
  change is needed: the key arrives through masked `Value`.
- `commitRecipientList` (`internal/gage/recipient.go`) takes a complete
  final list and does re-encrypt, prune pending, write both files, one
  commit, push. It is the primitive; enrollment approval is the precedent
  for a bespoke operation built on it.
- `RemoveRecipient` is the wrong tool: `confirmSelfRemoval` would say "this
  device will no longer be able to read the vault" while retiring the
  recovery key, and add-then-remove is two commits with a window where both
  keys are live.
- `identity add` must not touch the recipient list
  (`internal/gage/identity_add.go`): the device with the lost key cannot
  authorize itself. Recovery keeps that: the recovery key authorizes, in a
  separate step.
- `CreateIdentity` reuses an existing local identity file and asks for its
  passphrase. For a forgotten passphrase that is exactly wrong, so recovery
  refuses an existing file.
- R1's lessons apply again: show a new key only after the commit exists;
  hold delivery errors so the summary and push still happen; refuse unsafe
  `--recovery-key-out` destinations; require a terminal on both stdin and
  stderr for any key display; zero what gage builds.

### Milestones

| # | Milestone | Model | Depends on | Status |
|---|---|---|---|---|
| R3 | [Recovery identity, library](r3-recovery-identity-library.md) | Opus | R2 | `[ ]` |
| R4 | [`gage recovery enroll`](r4-recovery-enroll-cli.md) | Opus | R3 | `[ ]` |
| R5 | [`gage recovery rotate` and docs](r5-recovery-rotate-and-docs.md) | Sonnet | R3 (lands after R4) | `[ ]` |

Branches continue the stack: `main` <- `7-recovery-key` <- R0 <- R1 <- R2 <-
`7-recovery-enroll-plan` (these docs) <- R3 <- R4 <- R5. After R4, run a
focused code review of secret handling before merging, as for R1.

## Process

- No milestone commits, pushes or merges are made by the implementing
  agent; the reviewer does that at the end of each phase.
- Commit the previous phase before cutting the next stacked branch;
  uncommitted changes would otherwise follow the checkout.
- Tests first, at task granularity, per the core plan's test conventions.
- `make lint` and `make test` clean before a milestone is called done.
