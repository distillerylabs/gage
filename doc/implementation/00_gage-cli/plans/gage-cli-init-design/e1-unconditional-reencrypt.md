# E1 — Unconditional re-encryption

[← E0](e0-vault-id-keying.md) · [plan index](index.md) · next: [E2 — The sealed request](e2-sealed-request.md)

> **Recommended model: Sonnet\*.** Borderline, and it moved. The
> specification is fully settled and the atomic machinery already exists;
> most of this *removes* a code path. What raised it is the N-recipient
> extraction, which reshapes M9's central write path — additive and
> mechanical, but on the function every recipient change goes through.
> Start on Sonnet; switch if the extraction turns awkward. The care is in
> the pre-flight's ordering (before the lock, before any confirmation),
> in keeping `AddRecipient`'s observable behavior identical, and in
> replacing M9's tests rather than deleting them.

## Goal

Apply [A19](../gage-cli-design/open-questions.md#a19): `gage recipient
add` re-encrypts every entry, always. Remove `--reencrypt` from `add`;
it stays mandatory on `remove`, where it means something different.

Add the refusal this creates: an actor who cannot read every entry can
no longer add a recipient, and must be told why before anything happens.

**And extract the N-recipient form of `AddRecipient`'s body**, which E4's
batch approval needs and which this milestone is already the one opening
that function. See "The extraction" below.

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

### The extraction

`AddRecipient` today is a complete write: validate → `withVaultWrite`
(lock + dirty-tree reset) → `requireRecipientsInSync` →
`confirmRecipientTrust` → duplicate check → append **one** recipient →
`commitRecipientList` (one commit) → `noteRecipientsReviewed`
(`internal/gage/recipient.go`).

E4 needs the same sequence for N recipients under one lock, one trust
question, one re-encryption pass and one commit — and with the pending
files it is clearing deleted *in that same commit*. Calling `AddRecipient`
N times cannot produce that; it produces N of everything, which is
precisely the shape batch approval exists to avoid.

So this milestone lands the shared inner form: the append takes a slice,
and the commit takes the extra paths to remove alongside the entries it
rewrites. `AddRecipient` becomes its one-recipient caller — unchanged
externally, and still the thing whose duplicate check under the lock is
authoritative.

**Why here and not in E4.** Three reasons, in order of weight. This
milestone is already inside that function, so the alternative is opening
it twice. E1's whole purpose is to make the machinery E4 reuses
*existing* rather than co-invented — the same argument the plan already
makes for `ErrCannotGrantFullAccess`. And E4 is described everywhere as
wiring settled primitives to commands; leaving a refactor of M9's central
write path inside it would make that description false, which is the kind
of quiet scope growth the plan index warns about.

**The visible behavior of `recipient add` does not change**, which is
what keeps this from being speculative: it is one caller of a function
shaped for two, landing in the milestone that has the function open.

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
- [ ] **The N-recipient form adds several recipients in one commit**,
      with one trust question and one re-encryption pass, and every added
      key can read every pre-existing entry. Exercise it directly at the
      library level — E4 is what puts a command on it, and a test that
      waits for E4 leaves this milestone shipping an untested function.
- [ ] **It deletes the extra paths it is handed in the same commit** as
      the recipient files and the re-encrypted entries. E4 passes pending
      requests here; assert the mechanism with an ordinary file, so the
      test does not depend on a feature two milestones away.
- [ ] **An injected failure partway leaves HEAD untouched** in the
      N-recipient form too — no recipients added, no entries rewritten,
      no paths deleted. The one-recipient atomicity guarantee has to hold
      for the batch or approval's all-or-nothing claim is unfounded.
- [ ] **`AddRecipient` still behaves exactly as before**, including its
      duplicate check under the lock returning `ErrRecipientExists`. It
      is now a caller of the batch form, and the regression to guard
      against is the wrapper quietly changing what it does.
- [ ] `recipient add`'s registry `Short` reads "Authorize a public key
      and re-encrypt the vault to include it", parallel to `remove`'s
      wording. M0's registry test keeps both surfaces honest. (Three
      further `Short`s change in E3/E4 — see the table in D-ENROLL-VERBS.
      This milestone owns only `recipient add`'s.)
- [ ] **`identity add`'s printed next-command no longer names a flag
      that fails.** Assert on the output, not just on the parser: the
      line `identity add` prints is the one a user copies, so a test that
      only proves `--reencrypt` is rejected leaves the tool actively
      instructing people to pass it.

**Constructing the refusal state.** The only ways in are a recipient
added before this milestone, or a hand-edited `.age-recipients`. Use the
latter in tests — it is deterministic and does not depend on shipping
the old behavior first.

## Implementation

- [ ] Drop `reencrypt bool` from `Vault.AddRecipient`; always
      re-encrypt. `RemoveRecipient` keeps its parameter.
- [ ] **Extract the N-recipient, lock-held form** of `AddRecipient`'s
      body, per "The extraction" above: the append takes a slice of
      recipients, and `commitRecipientList` gains the extra paths to
      delete in the same commit. `AddRecipient` becomes the
      one-recipient caller. Unexported is right — E4's
      `ApproveEnrollments` is its only other caller and lives in the same
      package.
- [ ] Add `ErrCannotGrantFullAccess`, raised from a pre-flight pass that
      attempts to read every entry with the acting identity, ahead of
      `withVaultWrite`.
- [ ] Remove `--reencrypt` from `cmd/gage`'s `recipient add`; make it a
      usage error rather than a no-op, so scripts fail loudly instead of
      silently changing meaning.
- [ ] Update `recipientChangeLines` — the with/without distinction it
      renders no longer exists for `add`.
- [ ] **Grep the whole tree for `--reencrypt` before calling this done.**
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

  It also calls the N-recipient form extracted here. Without it,
  `ApproveEnrollments` either re-implements M9's central write path or
  falls back to N `AddRecipient` calls, which is N locks, N trust
  prompts, N re-encryption passes and N commits — not the single atomic
  commit approval is specified to produce.

  **The error is reused; its position is not.** `recipient add` unlocks
  first and runs the pre-flight before *any* confirmation it shows.
  `approve` shows a confirmation before it unlocks at all, so there the
  same check necessarily lands after that first `[y/N]` — still before
  the lock and before M10's prompt. Keep the pre-flight a separately
  callable pass rather than burying it inside `AddRecipient`, or E4
  cannot place it where it needs to go.
