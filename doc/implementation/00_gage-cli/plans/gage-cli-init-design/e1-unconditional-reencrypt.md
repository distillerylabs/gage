# E1 — Unconditional re-encryption

[← E0](e0-vault-id-keying.md) · [plan index](index.md) · next: [E2 — The sealed request](e2-sealed-request.md)

> **Recommended model: Sonnet.** Borderline. The specification is fully
> settled and the atomic machinery already exists — this mostly *removes*
> a code path. The care is in the pre-flight check's ordering (before the
> lock, before any confirmation) and in replacing M9's tests rather than
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

## Tests (write first)

M9's list currently pins the opposite behavior. Its three A19 bullets
are the starting point; these expand them.

- [ ] `gage recipient add` **accepts no `--reencrypt` flag** — passing
      one is a usage error, not silently ignored.
- [ ] `recipient add` makes **all pre-existing entries** decryptable by
      the new recipient. There is no invocation of `add` that produces a
      recipient who can read only part of the vault.
- [ ] `recipient add` still commits `.age-recipients` and
      `config.toml` **together**, in one commit, alongside every
      re-encrypted entry.
- [ ] An injected failure partway through the re-encryption leaves HEAD
      untouched — M9's existing atomicity guarantee, re-asserted because
      the path it runs on is now the only path.
- [ ] **An actor that cannot decrypt every entry is refused with
      `ErrCannotGrantFullAccess`**, *before* the vault write lock is
      taken and *before* the trust-cache confirmation is shown. Note the
      unlock has already happened here — `recipient add` is wrapped in
      `withUnlockedVault` — because the check is a decryption pass and
      has no identity-free form. E4 inherits the error but **not** this
      ordering; see its own list.
- [ ] The error maps to `exitcode.Conflict`.
- [ ] That error names **how many entries are unreadable**, and never
      surfaces a bare decryption failure on an entry UUID.
- [ ] The refusal leaves nothing changed: no commit, no partial
      re-encryption, no cache regeneration.
- [ ] `recipient remove` is untouched — still requires `--reencrypt`,
      still prints the revokes-future-access-only warning.
- [ ] `recipient add`'s registry `Short` reads "Authorize a public key
      and re-encrypt the vault to include it", parallel to `remove`'s
      wording. M0's registry test keeps both surfaces honest. (Three
      further `Short`s change in E3/E4 — see the table in D-ENROLL-VERBS.
      This milestone owns only `recipient add`'s.)

**Constructing the refusal state.** The only ways in are a recipient
added before this milestone, or a hand-edited `.age-recipients`. Use the
latter in tests — it is deterministic and does not depend on shipping
the old behavior first.

## Implementation

- [ ] Drop `reencrypt bool` from `Vault.AddRecipient`; always
      re-encrypt. `RemoveRecipient` keeps its parameter.
- [ ] Add `ErrCannotGrantFullAccess`, raised from a pre-flight pass that
      attempts to read every entry with the acting identity, ahead of
      `withVaultWrite`.
- [ ] Remove `--reencrypt` from `cmd/gage`'s `recipient add`; make it a
      usage error rather than a no-op, so scripts fail loudly instead of
      silently changing meaning.
- [ ] Update `recipientChangeLines` — the with/without distinction it
      renders no longer exists for `add`.
- [ ] Update M9's test list in place: replace the bullets pinning the
      old behavior rather than deleting them, so the change is legible
      to someone reading M9 later.

## Definition of done

M9's three A19 bullets green along with the ones above, `make lint` and
`make test` clean on all three platforms, and M9's doc updated so it no
longer describes a flag that does not exist.

## Affects later milestones

- **E4** calls the same `AddRecipient` and reuses
  `ErrCannotGrantFullAccess` unchanged. If this milestone is skipped,
  E4 has to define both itself, and `recipient add` remains the vector
  that keeps creating partial recipients — which is most of what A19
  was for.

  **The error is reused; its position is not.** `recipient add` unlocks
  first and runs the pre-flight before *any* confirmation it shows.
  `approve` shows a confirmation before it unlocks at all, so there the
  same check necessarily lands after that first `[y/N]` — still before
  the lock and before M10's prompt. Keep the pre-flight a separately
  callable pass rather than burying it inside `AddRecipient`, or E4
  cannot place it where it needs to go.
