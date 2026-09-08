# Device enrollment — implementation plan

Sequential milestones for building out
[gage-cli-init-design.md](../../tdds/gage-cli-init-design.md), the
enrollment enhancement to the main design. Same rules as the
[core plan](../gage-cli-design/index.md): each milestone leaves the tool
runnable and testable, and no milestone should require reworking a prior
one.

**This plan does not restate the shared ground.** Layering rules, the
locked-in library choices, test conventions, and the model-selection
guidance all live in the [core plan index](../gage-cli-design/index.md)
and apply unchanged here. What follows is only what is specific to
enrollment.

**There is still one open-questions register**, and it is the core
plan's: [open-questions.md](../gage-cli-design/open-questions.md).
Enrollment's decisions already live there (`Q-ENROLL-VERBS`,
`Q-IDENTITY-VAULT-NAME`, `Q-ORPHAN-BY-NAME`, amendments `A19` and
`A20`). Forking a second register would defeat the thing that register
is for — being the single place a deferred problem is allowed to live.

**Design decisions are in the TDD, not here.** The eight `D-ENROLL-*`
decisions are settled in
[gage-cli-init-design.md](../../tdds/gage-cli-init-design.md); milestone
docs reference them rather than re-arguing them. If a decision needs
revisiting, it changes there and the milestone follows.

Status legend: `[ ]` not started, `[~]` in progress, `[x]` done.

---

## Milestones

| # | Milestone | Status | Model | Theme |
|---|---|---|---|---|
| E0 | [Vault-id keying](e0-vault-id-keying.md) | `[ ]` | **Opus** ⚑ | A20: identities and trust cache keyed by vault id, not local name |
| E1 | [Unconditional re-encryption](e1-unconditional-reencrypt.md) | `[ ]` | Sonnet* | A19: `recipient add` always re-encrypts |
| E2 | [The sealed request](e2-sealed-request.md) | `[ ]` | **Opus** ⚑ | Code generation, the seal, the filename scheme, expiry — library only |
| E3 | [Joining a vault](e3-joining-side.md) | `[ ]` | Sonnet | `identity enroll`, the clone prompt, publishing a request |
| E4 | [Approving a device](e4-approving-side.md) | `[ ]` | **Opus** | `recipient pending/approve/deny`, late unlock, atomic batch approval |

**E0 and E1 are prerequisites, not enrollment.** They are amendments to
already-shipped milestones (E0 → M1/M2, E1 → M9) that enrollment depends
on. They live in this plan because nothing else is going to sequence
them, and because both were raised *by* designing enrollment. The core
plan's "Accepted, not yet implemented" section points here.

**Do E0 first, and do it soon.** Its migration is "re-create the vault,"
which is free only while there is no installed base — nothing has
shipped, and `Q-RELEASE` is still open. It is the only milestone in
either plan that gets more expensive with time.

**Milestones are `E`-prefixed on purpose.** The core plan uses `M0`–`M12`;
reusing those numbers in a second plan would make every cross-reference
in prose ambiguous about which plan it meant.

### Dependency graph

```
E0 (A20) ──┬────────────────> E3 ───> E4
           │                   ↑       ↑
E2 (seal) ─┴───────────────────┘       │
                                       │
E1 (A19) ──────────────────────────────┘
```

- **E0 → E3** — enroll writes and reads identity files, so the path
  scheme has to be settled first. Re-keying them afterwards would mean
  migrating files this feature just created.
- **E2 → E3** — enroll's whole job is producing a sealed request; the
  seal has to exist and be proven before anything publishes one.
- **E1 → E4** — approval always re-encrypts and needs
  `ErrCannotGrantFullAccess`. Landing A19 first makes both *existing*
  machinery that approval reuses, rather than two commands arriving at
  the same rule independently.
- **E0 → E4** — approval relabels recipients, which is the thing that
  makes E0's `removeOrphanedIdentity` fix urgent (see below).

**E1 and E2 are mutually independent** and can be worked in either
order, or in parallel by two people.

### One ordering constraint that is easy to miss

E0 has two halves, and they want different timing:

- **The keying half** (vault id, paths, `format_version`) should land
  first, ahead of everything, for the migration-cost reason above.
- **The `removeOrphanedIdentity` half** (compare public keys, confirm
  before deleting) must land **no later than E4**, because E4 introduces
  `recipient approve --device`, and a relabeled recipient is exactly what
  makes the name comparison delete a live key. Shipping E4 without it
  would introduce a new route to an unrecoverable bug.

They are kept in one milestone because they share the `[vaults.<name>]`
schema change; splitting them would mean touching global config twice.

---

## Cross-milestone contracts

Things one milestone publishes that a later one depends on — the same
class of coupling the core plan tracks, and easy to lose across five
files.

| Established in | Contract | Consumed by |
|---|---|---|
| E0 | `[vault].id` in `.gage/config.toml`; `format_version = 2` | Every later reader of that file |
| E0 | `$GAGE_DATA/identities/<vault-id>/<device>.age` | E3 (create/reuse), E4 (nothing — approval never touches local identities) |
| E0 | `pubkey` in global config's `[vaults.<name>]` | E0's own `removeOrphanedIdentity`; available to anything else needing a key-not-name comparison |
| E1 | `recipient add` always re-encrypts; `ErrCannotGrantFullAccess` raised before the lock and before any confirmation | E4 reuses both unchanged |
| E2 | Enrollment code: generation, Crockford normalization, validation-before-decrypt | E3 displays one, E4 consumes one |
| E2 | Sealed payload + `<request-id>-<expires-epoch>.age` filename scheme | E3 writes them, E4 reads and prunes them |
| E2 | `pending/` is inert — never read at encryption time | Everything after; it is the invariant that keeps the feature from widening the trust boundary |
| E3 | `Vault.Enroll` create-or-reuse semantics, and the two prompt paths | E4's tests construct pending requests through it |
| E3 | `ErrEnrollmentNoRemote` / `ErrEnrollmentRemoteUnreachable` as distinct errors | `cmd/gage` chooses the advice for each |

---

## What this plan does not cover

Deliberately out of scope in the TDD, and therefore absent here:
`--print-only` / `--request-file` (`D-ENROLL-PRINT-ONLY`),
`identity enroll --wait`, approver-initiated invites, auto-approval of
any kind, and enrollment for methods other than `passphrase`. The TDD's
"Deliberately out of scope" section carries the reasoning for each.

One item is analysis-only: the **hardware-method predicate** (`HasIdentity`
must become registration-based before any non-passphrase method lands).
It is traced in the TDD because getting it wrong now would force a
rework, but there is nothing to implement until a second method exists,
so it has no milestone here.
