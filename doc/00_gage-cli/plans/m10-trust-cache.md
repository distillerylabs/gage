# M10 — Local trust cache

[← M9](m9-recipients.md) · [plan index](index.md) · next: [M11 — Cross-vault sharing](m11-cross-vault-sharing.md)

> **Recommended model: Sonnet.** Borderline. The two-outcome cache-regeneration rule is subtle but fully specified; the hook placement (encrypt methods, `Unlock`, and `sync` separately) is the part to get right.

## Goal

Detect unreviewed recipient changes and surface them as a plaintext diff
before the next encrypt. This is the concrete backstop behind the design's
weakest guarantee — that anyone with git write access to a vault can
append a public key to `.age-recipients` and be included in *future*
writes.

Builds on M9's recipient add/remove — needs recipient-list changes to
exist to detect changes against — but only needs those files to differ
from a locally-cached copy, not `--reencrypt`'s crash-safety machinery
specifically. Kept as its own milestone so that atomicity work stays
isolated from this one's detection/UX logic, the same way M2 isolates
crypto risk and M8a/M8b isolate sync risk.

Two halves of the cache, and both matter: a verbatim copy of
`.gage/config.toml` (the file that's *diffed and shown*, because device
names make a legible warning) and the content hash of `.age-recipients`
(the file that's actually *consulted at encryption time*). Diffing only
the first would miss someone who edits only the second.

## Depends on

- **M5** — the encrypting commands the blocking check hooks.
- **M6** — `Vault.Unlock`, which the opportunistic check hooks.
- **M8b** — `sync`, which needs its own hook since a clean fast-forward
  never unlocks anything.
- **M9** — recipient changes to detect, and `verify`'s comparison logic.

## Design references

- ["Trust boundaries"](../tdds/gage-cli-design.md) — the two questions
  that get conflated, and why this is guarantee #2's mitigation
- ["Local trust cache"](../tdds/gage-cli-design.md) — what's cached, when
  it's checked, and the two different resolution outcomes
- ["Cache regeneration isn't one-size-fits-all"](../tdds/gage-cli-design.md)

## Decisions to make first

- **A recipient change over a vault that fails `verify` is refused, not
  silently repaired.** Resolved: refuse. M9's `AddRecipient` and
  `RemoveRecipient` rebuild `.age-recipients` from `config.toml`'s list,
  so running either against an already-diverged vault overwrites the
  stray key out of existence and commits the result as an ordinary
  recipient change. That is exactly backwards for this milestone: a key
  in one file and not the other is the tampering signature the whole
  trust cache exists to surface, and the silent repair destroys the
  evidence, produces a clean `verify`, and regenerates the cache against
  a list nobody reviewed — laundering an unreviewed key into a trusted
  one through a command the operator thought was about something else.
  So both verbs run `VerifyRecipients()` before they write anything and
  fail with the differences when it reports out of sync, rendered the
  same way `recipient verify` already renders them. This revises code M9
  shipped; it is recorded here rather than there because the reason for
  it is M10's premise, not M9's.
  - **Open: how a diverged vault is repaired**, since the refusal
    otherwise has no exit — and "refuse with no way forward" is worse
    than either alternative. Both files are plaintext by design, so
    hand-editing always works and may be the honest answer for a state
    that is supposed to be unreachable. The alternative is an explicit
    verb — `gage recipient verify --repair`, writing `.age-recipients`
    from `config.toml` behind a confirmation naming every key it drops —
    which performs the same overwrite, but as a deliberate, visible act
    rather than a side effect of an unrelated `add`. Decide before
    implementing the refusal, since the two ship together.
- **What `--yes` does in the mismatched case.** `--yes` bypasses the
  confirmation for scripting/CI. In the routine case that's clearly fine.
  In the *mismatched* case — the actual tampering signature — does
  `--yes` also proceed? Recommend: `--yes` proceeds but never regenerates
  the cache on a mismatch, matching the interactive behavior. Needs
  deciding because CI is exactly where nobody reads the warning.
- **Where the cache lives when a vault is renamed or re-cloned.**
  `$GAGE_STATE/<vault>/known-config.toml` is keyed by local vault name.
  Cloning the same remote under a different name gets a fresh cache and
  no warning. Accept, or key by something more stable?
- **Does `vault remove` delete the cache?** If not, re-adding a vault
  silently inherits an old approval.
- **First-use bootstrap.** The first time a device uses a vault there's
  no cache, so there's nothing to warn about — the cache is written
  silently. Confirm that's right (it is, but it means the very first
  recipient list is trusted on faith).

## Tests (write first)

- [ ] After a device's first successful use of a vault, `known-config.toml`
      is written to local state; a subsequent unreviewed recipient change
      triggers a diff warning before the next encrypt
- [ ] The cache stores both halves — the verbatim `config.toml` copy and
      the `.age-recipients` content hash
- [ ] A change to `.age-recipients` alone, with `config.toml` untouched,
      still triggers the warning — the case that exists specifically
      because diffing `config.toml` alone would miss it
- [ ] The warning fires the same way for a one-shot `insert`/`edit`/
      `generate` as for a session-mode one — the cache check hooks the
      `Vault` methods that encrypt, not `Session.Use`, since one-shot mode
      never calls `Session.Use` at all
- [ ] A non-blocking opportunistic warning fires on a plain `gage show`/
      `ls` in one-shot mode when recipients have changed, even though
      neither command encrypts anything — the same unlock hook M8a's auto
      fetch+pull uses, not the mandatory pre-encrypt check
- [ ] `gage sync` surfaces the opportunistic warning even when it resolves
      via a clean fast-forward with no conflicting entry — the case where
      sync never unlocks any identity at all, so the warning can't be
      riding along on a `Vault.Unlock` call
- [ ] The blocking check *blocks*: declining the prompt aborts the write
      entirely, no commit is produced, and the cache stays at its old
      value so the same warning reappears next time
- [ ] Confirming a *routine* recipient change (files agree) regenerates
      both halves of the cache and stops warning — one acknowledgment per
      actual change, not per command
- [ ] Confirming a *mismatched* change (`.age-recipients` and
      `config.toml` disagree) does not silently clear the cache —
      `verify` keeps failing until the inconsistency is actually fixed
- [ ] `--yes` bypasses the blocking prompt in the routine case, and
      behaves per the decision above in the mismatched case
- [ ] the recipient-change warning is a typed value returned by the
      library (diff content, routine-vs-mismatched flag), not printed
      text — `cmd/gage` renders it as the `[y/N]` prompt shown in the
      design doc
- [ ] The cache is written under `$GAGE_STATE`, never inside the vault,
      and never committed — a `git status` after a cache regeneration is
      clean
- [ ] `recipient add` and `recipient remove` against a vault whose
      `.age-recipients` and `config.toml` disagree are both refused,
      naming the specific differences, and leave both files
      byte-identical — the stray key is still there afterwards for
      `verify` to keep reporting, rather than having been rebuilt away
- [ ] A refused recipient change commits nothing, pushes nothing, and
      leaves the trust cache at its old value, so a mismatch cannot be
      laundered into an approved state by retrying the add
- [ ] The refusal is asserted with `--reencrypt` as well as without it:
      the rebuild that erases the evidence is in the shared tail both
      paths commit through, so covering only the plain add would leave
      the more destructive verb unguarded

## Implementation

- [ ] Trust cache storage: `$GAGE_STATE/<vault>/known-config.toml` (a
      verbatim copy) plus the stored `.age-recipients` content hash,
      written through M0's atomic-write helper
- [ ] Blocking pre-encrypt check on the `Vault` methods that encrypt
      (`insert`/`edit`/`generate`/`rename`, and M11's `mv`/`cp` against
      the destination vault)
- [ ] Non-blocking opportunistic check wired into `Vault.Unlock` itself
      (covering session `use` and every one-shot command's implicit
      unlock, mirroring M8a's fetch+pull hook) *and* into `sync` directly,
      since a clean fast-forward sync can complete without ever calling
      `Unlock`
- [ ] Diff generation against the cached `config.toml` copy
- [ ] The two-outcome resolution logic: routine changes regenerate both
      cache halves; mismatched ones record that the mismatch was seen
      without clearing the inconsistency
- [ ] `RecipientChangeWarning` (or similar) structured type returned by
      the library on a trust-cache mismatch; `cmd/gage` renders it as the
      terminal diff + `[y/N]` prompt
- [ ] A `VerifyRecipients()` precondition on M9's `AddRecipient` and
      `RemoveRecipient`, refusing over a failing verify instead of
      rebuilding `.age-recipients` from `config.toml` — see the resolved
      decision above. It belongs inside `withVaultWrite`, under the vault
      lock and before `commitRecipientList` writes anything, so the
      refusal can't race a concurrent repair; and it must sit ahead of
      the re-encryption rather than beside the recipient-file write,
      since aborting after every entry has been rewritten would be its
      own half-migrated state
- [ ] Whatever the open sub-decision above settles on for repairing a
      diverged vault — the refusal and its exit ship together

## Definition of done

Full test list green on all three CI platforms. A recipient added by
another device produces a legible diff and a prompt on this one, once —
and a hand-edited `.age-recipients` produces a warning that doesn't go
away by clicking yes — or by running `recipient add`, which refuses over
the divergence instead of rebuilding it away.

## Affects later milestones

- **M11 runs this check against the *destination* vault's cache**, not
  the source's — an entry moving into a vault gets encrypted to that
  vault's recipients, so that's the list whose changes matter.
