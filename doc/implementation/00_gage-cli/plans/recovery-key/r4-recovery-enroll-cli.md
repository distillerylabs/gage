# R4 - `gage recovery enroll`

[<- R3](r3-recovery-identity-library.md) - [plan index](index.md) - next: [R5 - `gage recovery rotate` and docs](r5-recovery-rotate-and-docs.md)

> **Recommended model: Opus.** Ordering and failure paths around a key that can read everything: the same class of risk as R1, where the bugs that mattered (a key deleted by gage itself, a skipped push) were all ordering. Follow with a focused secret-handling review before merging.

## Goal

One command takes a user who has lost their only identity file, or forgotten its passphrase, and the paper recovery key, to a working device
identity, with the used key retired and a fresh recovery key in hand.

## Depends on

- **R3** - `RecoverDevice`, `RecoverSpec`, the typed errors.
- Existing `planRecoveryKey`, `deliverRecoveryKey`, `zeroBytes`
  (`cmd/gage/recoverykey.go`), `recordLocalIdentity`
  (`cmd/gage/identity.go`), `deviceNameFor`, `CreateIdentity`,
  `RemoveIdentity`.

## Design references

- [index.md](index.md) decisions D8-D12 and the R1 lessons under "Design
  facts Phase 2 relies on".
- [R1](r1-init-integration.md) - the delivery, confirmation and
  destination-check rules this reuses.

## Decisions to make first

**One, pulled in from the R3 review: what `recovery verify` should say
about a key that is a recipient but not the recovery one.**

`VerifyRecoveryKey` matches any recipient's public key;
`recoveryIdentity` (R3) additionally requires the label to be exactly
`recovery-paper-key`. So today a user whose stored key is registered under
some other label is told "that key is a recipient of vault X", and then
refused with `ErrNotRecoveryKey` at the moment it matters. Verify is the
check the docs tell people to trust, so it must not pass a key that enroll
will reject.

The two answers are both true and they are not the same claim, which is
why this needs deciding rather than just fixing:

- the key **decrypts this vault** (so `age -d -i` recovers the secrets), and
- the key **can run `recovery enroll`** (so gage itself is recoverable).

Recommendation: keep exit 0 when the key is a recipient — that is the claim
the user's backup makes, and it is true — but report the two separately,
naming the label the key is registered under and saying plainly that
`recovery enroll` needs the `recovery-paper-key` one. A non-zero exit would
tell someone whose backup genuinely works that it does not. Settle the
exact wording and exit code here, with tests, before writing the command.

Wording of the other messages is an implementation detail; pin what matters
in tests.

## Tasks

- [ ] `gage recovery enroll [--device NAME] [--replaces DEVICE]
      [--no-new-recovery-key | --recovery-key-out FILE] [--use NAME]`, one-shot
      only, in `cmd/gage/recovery.go`, registered in `registry.go`.
- [ ] Order, so nothing irreversible happens before it must:
      1. resolve the vault, the device name, and plan the *new* recovery key
         with `planRecoveryKey` (terminal on stdin and stderr, or
         `--recovery-key-out`, or `--no-new-recovery-key`); refuse before
         any prompt;
      2. refuse an existing local identity file for the device
         (`ErrLocalIdentityExists`, `Usage` — moved here from R3, since the
         library never touches a local identity file) without ever asking
         for its passphrase, and refuse a name already used by a recipient
         unless it is the `--replaces` target;
      3. paste the recovery key through masked `Value` and
         `VerifyRecoveryKey` it, so a wrong paste costs no passphrase entry;
      4. generate the fresh device identity;
      5. `RecoverDevice`, rolling the identity back on any failure;
      6. `recordLocalIdentity`, then deliver the *new* key with the error
         **held**, then the summary. The push happens inside the library
         commit.
- [ ] Zero the pasted secret on every exit path.
- [ ] `gage clone`'s no-identity message and the `ErrNoLocalIdentity` text
      point at `gage recovery enroll`.
- [ ] `--recovery-key-out` reuses the R1 destination rules unchanged.
- [ ] `recovery verify` distinguishes "is a recipient" from "can run
      `recovery enroll`", per the decision above, so it never passes a key
      enroll will refuse.

## Tests (write first, in-process via `runCLIWithPrompter`)

- [ ] End to end: `init --recovery-key-out`, delete the identity file,
      `recovery enroll`; the new identity unlocks the vault, the old
      recovery key is no longer a recipient, a new one is, and the newly
      displayed key decrypts an entry.
- [ ] Works right after `gage clone` on a fresh XDG root.
- [ ] An existing local identity file is refused with no passphrase
      prompt.
- [ ] A wrong paste costs no passphrase prompt.
- [ ] A failing `RecoverDevice` leaves no orphan identity, no registration
      change, and no commit.
- [ ] No terminal without `--recovery-key-out` or `--no-new-recovery-key`
      fails before any prompt, exit code pinned.
- [ ] `--replaces` removes the old device in the same commit; without it a
      colliding name is refused.
- [ ] A refused confirmation keeps the vault, still pushes to `--remote`,
      and does not reprint the key.
- [ ] Neither the pasted key nor the new key appears anywhere under any XDG
      root or in git objects, and each appears in the output at most once
      (the new one exactly once).
- [ ] The pasted key is never echoed on stdout or stderr.
- [ ] Registry completeness and `gage help` list the command.
- [ ] `recovery verify` on a key registered under a non-recovery label says
      so, names that label, and does not claim the key can be used to
      enroll; on the real recovery key it says both are true. Whatever exit
      codes the decision above settles on are pinned here.

## Definition of done

Every item green on all three CI platforms, `make lint` and `make test`
clean, a real-binary walkthrough in a scratch XDG root (see below), and a
focused review of secret handling.

Walkthrough: `init --recovery-key-out`, `insert` an entry, delete the
identity file, `recovery enroll`, then `show` works with the new
passphrase, `recovery verify` fails for the old key and passes for the new.
