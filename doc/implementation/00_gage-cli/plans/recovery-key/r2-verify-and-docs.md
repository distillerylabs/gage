# R2 — `gage recovery verify` and documentation

[← R1](r1-init-integration.md) · [plan index](index.md)

> **Recommended model: Sonnet.** Mostly mechanical: one small command on top of R0's `VerifyRecoveryKey`, registry wiring, and prose.

## Goal

Let a user prove their paper copy is readable without unlocking
anything, and bring every document in line with the new default.

## Depends on

- **R0** — `VerifyRecoveryKey`. Lands after R1 so the docs describe
  shipped behavior.

## Design references

- [index.md](index.md) decisions D4 and D7.
- `cmd/gage/registry.go` and `TestRegistryCompleteness` — a command must
  be registered in one place and exist in Cobra.

## Decisions to make first

None open on entry. `recipient verify` already exists and means something else
(config vs `.age-recipients` agreement), which is why this is
`recovery verify`.

One thing surfaced while writing the docs and is recorded rather than
buried: **`gage` cannot unlock a vault with the recovery key.** Only the
passphrase method exists (`AllowedMethods`, `Unlock`), and the recovery key
is a bare age key. It decrypts entries through stock `age -d -i`, so it
recovers the *secrets*, not a running gage; `recipient add` and every other
command still need an identity gage can unlock. The plan and the PR
descriptions had implied more. The docs now say so plainly (see
"Using it" in `identities-and-recipients.md`); an `age-key` method would
close it and is out of scope for this feature.

## Tasks

- [x] `newRecoveryCommand(app)` in a new `cmd/gage/recovery.go`, with a
      `verify` subcommand: reads the pasted key through masked
      `Prompter.Value`, calls `VerifyRecoveryKey`, reports match or no
      match, and zeroes the input.
- [x] Registry entry (group: recipient, one-shot only) and
      `root.AddCommand` in `app.go`.
- [x] `tdds/gage-cli-design.md`: `init` section, "Losing this file is a
      recovery problem" paragraph, and the config example; state that
      init generates the recovery recipient by default and gage keeps no
      copy.
- [x] `doc/cli/`: `getting-started.md`, `vaults.md`,
      `identities-and-recipients.md` (with a "Storing your recovery key"
      section including the leak response: add a new recovery recipient,
      `gage recipient remove` the old one, re-encrypt, rotate the secrets
      themselves), and `command-reference.md`.
- [x] Update the status columns in [index.md](index.md) and mark
      `Q-RECOVERY-KEY` resolved in `open-questions.md`.

## Tests (write first)

- [x] `recovery verify` with the right key reports a match and exits 0.
- [x] A valid key that is not a recipient reports no match with the
      pinned exit code.
- [x] A malformed key is rejected with the pinned exit code.
- [x] The pasted key is read through the prompter and never echoed to
      stdout or stderr.
- [x] `TestRegistryCompleteness` passes with the new command; `gage help`
      lists it.

## Definition of done

Every item green on all three CI platforms, `make lint` and `make test`
clean, and every document listed above updated.
