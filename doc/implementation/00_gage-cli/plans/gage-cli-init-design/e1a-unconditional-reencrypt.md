# E1a — Unconditional re-encryption

[← E0](e0-vault-id-keying.md) · [plan index](index.md) · next: [E1b — The shared recipient write](e1b-shared-recipient-write.md)

> **Recommended model: Sonnet.** The specification is fully settled and
> the atomic machinery already exists — this mostly *removes* a code
> path. The care is in the pre-flight check's ordering (before the lock,
> before any confirmation) and in replacing M9's tests rather than
> deleting them.

## Goal

Apply [A19](../gage-cli-design/open-questions.md#a19): `gage recipient
add` re-encrypts every entry, always. Remove `--reencrypt` from `add`;
it stays mandatory on `remove`, where it means something different.

Add the refusal this creates: an actor who cannot read every entry can
no longer add a recipient, and must be told why before anything happens.

**Not enrollment.** This is an amendment to M9. It is sequenced here
because E4's approval reuses both the behavior and the error, and
landing this first makes them existing machinery rather than two
commands inventing the same rule independently.

**The other half of that amendment is [E1b](e1b-shared-recipient-write.md).**
This milestone changes what `recipient add` *does*; E1b reshapes the
function it does it in, so that E4's batch approval has something to call.
They were one milestone in an earlier draft — see E1b's "Why this is its
own milestone" for why they are two.

## Depends on

**M9** — `AddRecipient`, `reencryptTo`, and the all-or-nothing commit.

## Design references

- [A19](../gage-cli-design/open-questions.md#a19) — the three
  compounding problems with partial recipients, and the accepted cost
- ["Why adding a recipient always re-encrypts"](../../tdds/gage-cli-design.md)
  — the same argument as applied design text
- ["Approval always re-encrypts"](../../tdds/gage-cli-init-design.md) —
  E4's mirror of this decision

## Decisions

Settled in A19. One thing worth restating because it shapes the tests:
partial access is **contagious** — `reencryptTo` fails on the first
entry the acting identity cannot read, so a partially-admitted device
can neither repair itself nor grant full access to anyone else. The
refusal below is what makes that legible instead of a mid-write failure
on an opaque UUID.

**Leave `AddRecipient`'s shape alone.** The pre-flight below is a
*separate* pass this milestone adds beside it, not a restructuring of the
method — [E1b](e1b-shared-recipient-write.md) is what reshapes the body,
and doing both at once is what made an earlier draft of this milestone
hard to review.

## Tests (write first)

M9's list currently pins the opposite behavior. Its three A19 bullets
are the starting point; these expand them.

- [x] `gage recipient add` **accepts no `--reencrypt` flag** — passing
      one is a usage error, not silently ignored.
- [x] `recipient add` makes **all pre-existing entries** decryptable by
      the new recipient. There is no invocation of `add` that produces a
      recipient who can read only part of the vault.
- [x] `recipient add` still commits `.age-recipients` and
      `config.toml` **together**, in one commit, alongside every
      re-encrypted entry.
- [x] An injected failure partway through the re-encryption leaves HEAD
      untouched — M9's existing atomicity guarantee, re-asserted because
      the path it runs on is now the only path.
- [x] **An actor that cannot decrypt every entry is refused with
      `ErrCannotGrantFullAccess`**, *before* the vault write lock is
      taken and *before* the trust-cache confirmation is shown. Note the
      unlock has already happened here — `recipient add` is wrapped in
      `withUnlockedVault` — because the check is a decryption pass and
      has no identity-free form. E4 inherits the error but **not** this
      ordering; see its own list.
- [x] The error maps to `exitcode.Conflict`.
- [x] That error names **how many entries are unreadable**, and never
      surfaces a bare decryption failure on an entry UUID.
- [x] The refusal leaves nothing changed: no commit, no partial
      re-encryption, no cache regeneration.
- [x] `recipient remove` is untouched — still requires `--reencrypt`,
      still prints the revokes-future-access-only warning.
- [x] `recipient add`'s registry `Short` reads "Authorize a public key
      and re-encrypt the vault to include it", parallel to `remove`'s
      wording. M0's registry test keeps both surfaces honest. (Three
      further `Short`s change in E3/E4 — see the table in D-ENROLL-VERBS.
      This milestone owns only `recipient add`'s.)
- [x] **`identity add`'s printed next-command no longer names a flag
      that fails.** Assert on the output, not just on the parser: the
      line `identity add` prints is the one a user copies, so a test that
      only proves `--reencrypt` is rejected leaves the tool actively
      instructing people to pass it.

**Constructing the refusal state.** The only ways in are a recipient
added before this milestone, or a hand-edited `.age-recipients`. Use the
latter in tests — it is deterministic and does not depend on shipping
the old behavior first.

## Implementation

- [x] Drop `reencrypt bool` from `Vault.AddRecipient`; always
      re-encrypt. `RemoveRecipient` keeps its parameter.
- [x] Add `ErrCannotGrantFullAccess`, raised from a pre-flight pass that
      attempts to read every entry with the acting identity, ahead of
      `withVaultWrite`.
- [x] Remove `--reencrypt` from `cmd/gage`'s `recipient add`; make it a
      usage error rather than a no-op, so scripts fail loudly instead of
      silently changing meaning.
- [x] Update `recipientChangeLines` — the with/without distinction it
      renders no longer exists for `add`.
- [x] **Grep the whole tree for `--reencrypt` before calling this done.**
      Removing the flag from the parser is the small half; the flag is
      also *taught* in prose that will otherwise keep telling people to
      pass an argument that now fails. At least three sites outside
      `recipient add` itself:
      - `identity add`'s closing instruction, which prints the literal
        next command to run: `gage recipient add <pubkey> --device NAME
        --reencrypt` (`cmd/gage/identity.go`). This one is the worst of
        them — it is copy-pasteable, and following it verbatim now
        produces a usage error at the end of a successful onboarding.
      - `identity add`'s `Long` help, which repeats the same instruction
        in prose a few lines above.
      - `removeOrphanedIdentity`'s "remove it as a recipient first (from
        another device, with `--reencrypt`)" message
        (`cmd/gage/vault.go`) — this one is about `recipient remove`,
        where the flag *stays*, so it is correct as written. Check it
        rather than assume, and leave it alone.
      A grep is the check, not reading this list: the point is that the
      flag's removal is a documentation change as much as a parser one.
- [x] Update M9's test list in place: replace the bullets pinning the
      old behavior rather than deleting them, so the change is legible
      to someone reading M9 later.

## Definition of done

M9's three A19 bullets green along with the ones above, `make lint` and
`make test` clean on all three platforms, and M9's doc updated so it no
longer describes a flag that does not exist.

## Affects later milestones

- **E1b** reshapes the function this milestone changes the behavior of.
  Landing this first means the extraction never has to thread a
  `reencrypt bool` through a body it is about to remove it from.

- **E4** reuses `ErrCannotGrantFullAccess` unchanged. If this milestone
  is skipped, E4 has to define it itself, and `recipient add` remains the
  vector that keeps creating partial recipients — which is most of what
  A19 was for.

  **The error is reused; its position is not.** `recipient add` unlocks
  first and runs the pre-flight before *any* confirmation it shows.
  `approve` shows a confirmation before it unlocks at all, so there the
  same check necessarily lands after that first `[y/N]` — still before
  the lock and before M10's prompt. Keep the pre-flight a separately
  callable pass rather than burying it inside `AddRecipient`, or E4
  cannot place it where it needs to go.
