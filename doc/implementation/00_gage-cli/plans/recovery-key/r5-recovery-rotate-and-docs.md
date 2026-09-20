# R5 - `gage recovery rotate` and documentation

[<- R4](r4-recovery-enroll-cli.md) - [plan index](index.md)

> **Recommended model: Sonnet.** A thin CLI over R3's swap primitive, plus prose that must be rewritten to say what is now true.

## Goal

Replace or create a vault's recovery key from an unlocked device, in one
commit, and rewrite the docs that currently say gage cannot use the
recovery key.

## Depends on

- **R3** - the shared swap primitive. Lands after R4 so the docs describe
  shipped behavior.

## Design references

- [index.md](index.md) decision D11.
- [R1](r1-init-integration.md) - key delivery rules reused as-is.

## Decisions to make first

None open. `rotate` also serves a vault created with `--no-recovery-key`
(it creates the recipient when there is none), which is the natural way to
add one later and needs no separate command.

## Tasks

- [ ] `(*Vault).RotateRecoveryKey(newPubkey, ident)`: the same swap driven
      by a normal unlocked identity; adds the recovery recipient when the
      vault has none.
- [ ] `gage recovery rotate [--recovery-key-out FILE] [--use NAME]`,
      one-shot only: unlock, generate, swap, then deliver the new key under
      the R1 rules (terminal on both streams, held error, confirmation).
- [ ] Rewrite the leak-response instructions in
      `doc/cli/identities-and-recipients.md` around `recovery rotate`; they
      currently tell the user to add a second `recovery-paper-key`, which
      collides on the fixed label.
- [ ] Rewrite "Using it" around `recovery enroll`; keep the honest statement
      of what the key still cannot do (it is not an unlock method).
- [ ] Update `getting-started.md`, `vaults.md`, `command-reference.md`, and
      the design doc's "Recovery key at `init`" section, including its
      "gage cannot unlock with it" bullet.
- [ ] Reword `Q-RECOVERY-KEY`'s known limit in `open-questions.md` to what
      is now true, and update the status columns in [index.md](index.md).

## Tests (write first)

- [ ] `rotate` swaps the recovery recipient in one commit; the old key
      stops opening new ciphertext and the new one opens everything.
- [ ] `rotate` on a vault with no recovery recipient creates one.
- [ ] `rotate` needs an unlock and refuses without a terminal or
      `--recovery-key-out`, before any prompt.
- [ ] The new key is never stored, and appears in the output exactly once.
- [ ] Registry completeness and `gage help` list the command.

## Definition of done

Every item green on all three CI platforms, `make lint` and `make test`
clean, and every document listed above updated.
