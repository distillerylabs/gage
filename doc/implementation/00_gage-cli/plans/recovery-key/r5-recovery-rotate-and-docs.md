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

- [x] `(*Vault).RotateRecoveryKey(newPubkey, ident)`: the same swap driven
      by a normal unlocked identity; adds the recovery recipient when the
      vault has none.
- [x] `gage recovery rotate [--recovery-key-out FILE] [--use NAME]`,
      one-shot only: unlock, generate, swap, then deliver the new key under
      the R1 rules (terminal on both streams, held error, confirmation).
- [x] Rewrite the leak-response instructions in
      `doc/cli/identities-and-recipients.md` around `recovery rotate`; they
      currently tell the user to add a second `recovery-paper-key`, which
      collides on the fixed label.
- [x] Rewrite "Using it" around `recovery enroll`; keep the honest statement
      of what the key still cannot do (it is not an unlock method).
- [x] Update `getting-started.md`, `vaults.md`, `command-reference.md`, and
      the design doc's "Recovery key at `init`" section, including its
      "gage cannot unlock with it" bullet.
- [x] Reword `Q-RECOVERY-KEY`'s known limit in `open-questions.md` to what
      is now true, and update the status columns in [index.md](index.md).

## Tests (write first)

- [x] `rotate` swaps the recovery recipient in one commit; the old key
      stops opening new ciphertext and the new one opens everything.
- [x] `rotate` on a vault with no recovery recipient creates one.
- [x] `rotate` needs an unlock and refuses without a terminal or
      `--recovery-key-out`, before any prompt.
- [x] The new key is never stored, and appears in the output exactly once.
- [x] Registry completeness and `gage help` list the command.

## Settled while implementing

- **`swapRecipients` gained `removeIfPresent` and `inspect`.** Rotate
  retires a recovery recipient a `--no-recovery-key` vault never had, which
  `remove` (where naming nobody is an error, so a `--replaces` typo cannot
  be a silent no-op) would refuse. `inspect` reads the list under the lock,
  so what is reported as retired is what the swap actually retired, not a
  stale read. Both are additive to R3's primitive.
- **`RotateRecoveryKey` refuses the key it is retiring.** R3's review found
  that the swap removes the old label before checking additions, so
  re-offering the retired key would sail through and report a rotation that
  changed nothing. `inspect` is where that check lives, since it needs the
  list the lock protects.
- **`planRecoveryKey` accepts an empty opt-out flag.** Rotate has none —
  producing a key is the whole command — and a refusal must not tell the
  user to pass a flag that does not exist.
- **The R4 stopgap wording is gone.** `warnNoRecoveryKey` and the two
  key-delivery failure messages named `age-keygen` plus `recipient add`
  because `recovery rotate` did not exist yet; all three now name it.
- **The docs' "cannot unlock with it" claim is retired**, replaced by what
  is now true: the recovery key has exactly one power inside gage, and it is
  not an unlock method. An `age-key` method stays deliberately unbuilt.

## Found by review

A `/code-review` pass found four issues; all are fixed on the same branch,
each with a test that failed first.

- **`recovery rotate` evicted whatever held the `recovery-paper-key` label,
  recovery key or not.** The label was only reserved when a key was actually
  being generated, and `recipient add --device recovery-paper-key` reserved
  nothing at all — so `init v --no-recovery-key --device recovery-paper-key`
  was accepted, and rotate on that vault (the flow this milestone's own help
  advertises) dropped the device and re-encrypted to the new paper key
  alone, locking out the machine that ran it. `RecoverDevice` was safe only
  because `recoveryIdentity` proves the pasted key carries the label; rotate
  had no equivalent and cannot have one, since a recovery key is just an
  X25519 key and **the label is the only thing that distinguishes it**.

  Fixed by reserving the label rather than by narrowing rotate:
  `checkDeviceLabelFree` now runs in `Create`, `AddIdentity`, `Enroll` and
  `addRecipientsLocked` (which covers both `recipient add` and enrollment
  approval), plus ahead of `recovery enroll`'s own collision check so its
  reason is the one reported. It is deliberately *not* part of
  `devicename.Valid`: the label is a valid name, and the recovery recipient
  is written under it by `Create`'s `ExtraRecipients` and by the swap,
  neither of which goes through those paths. `init` keeps an early check of
  its own, now unconditional rather than gated on a key being generated, so
  the refusal arrives before the passphrase prompt instead of from `Create`
  after it.

  **This is a deliberate behavior change from how R1 shipped.**
  `--no-recovery-key` used to leave the label free, and one R1 test asserted
  exactly that; it now asserts the opposite, with the reason recorded beside
  it. A vault made without a recovery key can still grow one through
  `rotate`, so the label has to stay reserved there too.

  **Removing `--no-recovery-key` was considered and rejected.** It would
  have been neither sufficient (the `recipient add` route stays open) nor
  necessary (the reservation closes both), and it would have forced every
  scripted `init` to write a plaintext key file, since that flag is one of
  the two ways past the no-terminal rule.
- **Rotate now refuses to evict the acting identity's own key**, as a
  backstop under the reservation: even if something ever slips under the
  label again, rotate must not be what locks out the device running it.
- **The out-path check moved ahead of the unlock.** It was inside
  `withUnlockedVault`, so a bad `--recovery-key-out` was rejected only after
  the passphrase prompt and a remote sync, while the comment beside it
  claimed otherwise.
- **The "revokes future access only" warning now covers `removeIfPresent`.**
  It looped over `remove` alone, so rotate never emitted it — for exactly
  the leak-response user who most needs to hear that the retired key still
  opens git history.

## Definition of done

Every item green on all three CI platforms, `make lint` and `make test`
clean, and every document listed above updated.
