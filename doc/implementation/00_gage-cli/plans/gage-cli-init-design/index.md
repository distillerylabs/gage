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

**Exit codes are part of every milestone's definition of done.** The
taxonomy is an M0 contract, and the mapping for all of this feature's
errors is settled in one table in the TDD's "Library surface" — including
the three with a non-obvious home (`ErrEnrollmentCodeWrong` →
`LockedOrAuth`, `ErrEnrollmentNoRemote` → `Usage`, an ambiguous ID →
`Ambiguous`). A milestone that adds an error adds its code and a test
pinning it.

**There is still one open-questions register**, and it is the core
plan's: [open-questions.md](../gage-cli-design/open-questions.md).
Enrollment's decisions already live there (`Q-ENROLL-VERBS`,
`Q-IDENTITY-VAULT-NAME`, `Q-ORPHAN-BY-NAME`, amendments `A19` and
`A20`). Forking a second register would defeat the thing that register
is for — being the single place a deferred problem is allowed to live.

**Design decisions are in the TDD, not here.** All nine `D-ENROLL-*`
decisions are settled in
[gage-cli-init-design.md](../../tdds/gage-cli-init-design.md); milestone
docs reference them rather than re-arguing them. If a decision needs
revisiting, it changes there and the milestone follows.

**`D-ENROLL-SEAL-COST` is the newest and the one least likely to be
remembered**, since it was added after the milestone docs were first
written. It settles three things E2 and E4 both depend on: the open path
caps a request's *claimed* scrypt work factor (as `unlock.go` already
does for identity files), seals are written at factor 14 rather than the
identity file's 19, and one code-trying run attempts at most 32 live
requests. The first is a bug not to introduce; the other two are
calibrated numbers with reasoning attached — read it before changing
either.

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

### What this plan corrected after review

Recorded because both were wrong in a way a passing test suite would not
have caught, and someone reading the milestone docs alone would inherit
the mistake.

- **The dirty-tree reset covers the whole working tree, not `entries/`,
  and deletes untracked files.** Both TDDs said otherwise, and the
  enrollment doc built a crash-safety story on it — that a stray
  `pending/` file survives to expire on its own epoch, when in fact the
  next local write discards it. Corrected as
  [A21](../gage-cli-design/open-questions.md#a21); E3's test list asserts
  the real behavior. The knock-on: the "every commit stages explicit
  paths, never `pending/` wholesale" requirement is **dropped**, since
  the reset already provides that guarantee and honoring it literally
  would have meant replacing `gitrepo.CommitAll` across every write path
  in the codebase.
- **`ErrCannotGrantFullAccess` cannot precede `approve`'s confirmation.**
  The check is a decryption pass, so it needs the unlock — which
  `approve` deliberately defers until after the human says yes. E1's
  phrasing was correct for `recipient add` and wrong when copied into
  E4. See E4's Decisions for the ordering that replaces it.

A second review pass found three more, all of the same character —
things a passing test suite would not have caught, because the tests
would have been written against the same wrong premise:

- **Enroll cannot reach the network through `Pull`/`Push`/`tryPull`.**
  D-ENROLL-REMOTE cited `tryPull`'s `underWriteLock` as precedent for
  holding the lock across the pull, without noticing that this makes
  those entry points *uncallable* from inside enroll's own lock:
  `vaultlock` is not re-entrant, so enroll would block against itself
  and fail as contention. Enroll uses the unexported `v.pull`/`v.push`.
  Corrected in the TDD's D-ENROLL-REMOTE and in E3.
- **`Prompter` does gain a method.** The TDD drew clone's offer as
  `[Y/n]` and asserted "no new interface surface" on the same page;
  `Confirm` is specified default-no and says why. Resolved in favor of
  the transcript: `ConfirmDefaultYes` is added, E3 owns it, and
  D-ENROLL-PROMPTER is scoped so its (correct) claim about the
  enrollment *code* stops reading as a claim about the whole feature.
- **"Already a recipient" was being reported as a name collision.**
  A device that was added manually and then runs `identity enroll` hit
  `ErrDeviceNameTaken` and was told to pass `--device` — advice that
  mints a second identity to request access it already has. It is now a
  success that does no work, checked by public key before the name
  check, using the `pubkey` field E0 adds. This is the second consumer
  of E0's key-not-name comparison, which is worth noting on its own:
  the field was introduced for `removeOrphanedIdentity` and turns out
  to fix a second bug of the same shape.

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
| E0 | `$GAGE_DATA/identities/<vault-id>/<device>.age` | E3 (create/reuse). E4 never touches local identities — but it still **depends on E0**, for the `removeOrphanedIdentity` fix that `--device` makes reachable |
| E0 | `pubkey` in global config's `[vaults.<name>]`, written by `init`, `clone`, `identity add` — and by E3's `enroll` | E0's own `removeOrphanedIdentity`; **E3's already-a-recipient check**, which is the second consumer of the same key-not-name comparison; anything else needing one |
| E2 | `Vault.PendingEnrollments`, `OpenEnrollment`, `ResolveEnrollment` — **built here, not in E4** | E4 wires all three to commands and reimplements none of them |
| E3 | `Prompter.ConfirmDefaultYes` — the one default-yes question in `gage` | Nothing else today. Recorded because it is a compile-time break on every `Prompter` implementation, which is the point |
| E1 | `recipient add` always re-encrypts; `ErrCannotGrantFullAccess` exposed as a **separately callable pre-flight pass**, not buried in `AddRecipient` | E4 reuses the error and the pass, but places it differently — after its own confirmation and unlock, still before the lock and M10's prompt |
| E2 | Enrollment code: generation, Crockford normalization, validation-before-decrypt | E3 displays one, E4 consumes one |
| E2 | Sealed payload + `<request-id>-<expires-epoch>.age` filename scheme | E3 writes them, E4 reads and prunes them |
| E2 | ID resolution — exact, substring, candidate list — over the UUID portion only | E4's `approve <ID>` and `deny <ID>` |
| E2 | Pruning, including the beyond-ceiling clamp | E4 wires it into approve, deny, and both recipient verbs |
| E2 | `pending/` is inert — never read at encryption time; the directory is created lazily and its absence means zero requests | Everything after; it is the invariant that keeps the feature from widening the trust boundary |
| E3 | `Vault.Enroll` create-or-reuse semantics, and the two prompt paths | E4's tests construct pending requests through it |
| E3 | `ErrEnrollmentNoRemote` / `ErrEnrollmentRemoteUnreachable` as distinct errors | `cmd/gage` chooses the advice for each |
| E3 | `identity add` / `identity list` `Short`s reworded per D-ENROLL-VERBS | E4 rewords `recipient list` against them; M0's registry test holds all four honest |

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
