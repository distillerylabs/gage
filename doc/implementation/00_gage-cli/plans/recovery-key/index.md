# Recovery key at `gage init` — implementation plan

Tracked by GitHub issue [#7](https://github.com/distillerylabs/gage/issues/7); one sub-issue per milestone below.

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
| R1 | [`init` integration](r1-init-integration.md) | Opus | R0 | `[ ]` |
| R2 | [`recovery verify` and docs](r2-verify-and-docs.md) | Sonnet | R0 (lands after R1) | `[ ]` |

After R1, run a focused code review of secret handling before merging.
If R1 proves too large, split it into R1a (flags, non-terminal rule,
ordering, hygiene tests) and R1b (display, QR, warning text,
confirmation).

## Process

- No milestone commits, pushes or merges are made by the implementing
  agent; the reviewer does that at the end of each phase.
- Commit the previous phase before cutting the next stacked branch;
  uncommitted changes would otherwise follow the checkout.
- Tests first, at task granularity, per the core plan's test conventions.
- `make lint` and `make test` clean before a milestone is called done.
