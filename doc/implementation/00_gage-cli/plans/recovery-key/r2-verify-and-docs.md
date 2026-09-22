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

None open. `recipient verify` already exists and means something else
(config vs `.age-recipients` agreement), which is why this is
`recovery verify`.

## Tasks

- [ ] `newRecoveryCommand(app)` in a new `cmd/gage/recovery.go`, with a
      `verify` subcommand: reads the pasted key through masked
      `Prompter.Value`, calls `VerifyRecoveryKey`, reports match or no
      match, and zeroes the input.
- [ ] Registry entry (group: recipient, one-shot only) and
      `root.AddCommand` in `app.go`.
- [ ] `tdds/gage-cli-design.md`: `init` section, "Losing this file is a
      recovery problem" paragraph, and the config example; state that
      init generates the recovery recipient by default and gage keeps no
      copy.
- [ ] `doc/cli/`: `getting-started.md`, `vaults.md`,
      `identities-and-recipients.md` (with a "Storing your recovery key"
      section including the leak response: add a new recovery recipient,
      `gage recipient remove` the old one, re-encrypt, rotate the secrets
      themselves), and `command-reference.md`.
- [ ] Update the status columns in [index.md](index.md) and mark
      `Q-RECOVERY-KEY` resolved in `open-questions.md`.

## Tests (write first)

- [ ] `recovery verify` with the right key reports a match and exits 0.
- [ ] A valid key that is not a recipient reports no match with the
      pinned exit code.
- [ ] A malformed key is rejected with the pinned exit code.
- [ ] The pasted key is read through the prompter and never echoed to
      stdout or stderr.
- [ ] `TestRegistryCompleteness` passes with the new command; `gage help`
      lists it.

## Definition of done

Every item green on all three CI platforms, `make lint` and `make test`
clean, and every document listed above updated.
