# M11 — Cross-vault sharing

[← M10](m10-trust-cache.md) · [plan index](index.md) · next: [M12 — Polish / output modes](m12-polish.md)

> **Recommended model: Sonnet.** Small milestone. The deterministic two-vault lock ordering is the only real wrinkle, and it's called out as a decision up front.

## Goal

`gage mv --to-vault` and `gage cp --to-vault`: decrypt an entry from the
source vault and re-encrypt it for the destination vault's recipients.
This is the sharing mechanism — with one flat recipient list per vault,
there's nothing to move an entry *between* within a vault, so moving it to
another vault is literally what "share this credential with someone"
means in this design.

Mechanically it's just M2's encrypt primitive pointed at a second vault's
recipients — trivial once everything before it exists — but it's distinct
enough to be its own milestone since it's the whole sharing story, and it
has one genuinely non-obvious property: **the destination doesn't need to
be unlocked**, because encrypting *to* a set of public keys never requires
holding any of the matching private keys.

## Depends on

- **M2** — the encrypt-to-recipients primitive.
- **M4** — commit-per-write under the vault lock (two vaults, two
  commits).
- **M7** — the metadata indexes that both vaults' sessions may hold.
- **M10** — the trust-cache check, run against the destination.

## Design references

- ["Why no per-directory sharing"](../tdds/gage-cli-design.md) — why a
  second vault replaced per-subtree recipients, and the `mv --to-vault`
  example
- ["`mv`/`cp` are now cross-vault, and that's the sharing mechanism"](../tdds/gage-cli-design.md)
- ["Local trust cache"](../tdds/gage-cli-design.md) — the destination
  vault's cache is the one checked

## Decisions to make first

- **Lock ordering across two vaults.** `mv` writes to both vaults, so it
  acquires two of M0's vault locks. Two concurrent `mv`s in opposite
  directions between the same pair of vaults will deadlock unless locks
  are acquired in a deterministic order (e.g. sorted by vault path).
  Decide the ordering rule and test it.
- **Is `mv` atomic across the two vaults?** It can't be — they're
  separate git repos, so there's a window where the entry exists in both
  (or, if ordered the other way, neither). Recommend: write to the
  destination first, verify, then remove from the source, so a crash
  leaves a duplicate rather than a loss. Confirm and document.
- **What `updated_by`/`created` look like in the destination.** Is the
  copy a new entry (new UUID, new `created`) or the same entry relocated?
  Recommend a new UUID — the destination is a different repo and reusing
  a UUID implies a relationship that isn't tracked.
- **Does the source entry's title collide in the destination?** The
  duplicate-title rule from `insert` presumably applies, with `-f`.

## Tests (write first)

- [ ] `gage mv --to-vault` removes the entry from the source vault and it
      becomes decryptable in the destination vault under the destination's
      recipients
- [ ] `gage cp --to-vault` leaves the original in the source vault and
      adds a decryptable copy in the destination
- [ ] `mv`/`cp --to-vault` succeeds even when the destination vault is not
      currently unlocked (encrypting to public keys needs no private key)
- [ ] `mv`/`cp --to-vault` runs M10's trust-cache check against the
      *destination* vault's cache (not the source's) before encrypting
      there, and an unreviewed recipient change on the destination
      triggers the same diff warning a same-vault write would
- [ ] Declining that trust-cache prompt aborts the whole operation — the
      source entry is untouched and nothing is written to the destination
- [ ] `mv` produces a commit in each vault: an addition in the
      destination and a deletion in the source
- [ ] A crash between the two writes leaves the entry present in the
      destination and still present in the source (a recoverable
      duplicate), never absent from both
- [ ] Both vaults' locks are acquired in a deterministic order, so two
      concurrent opposite-direction `mv`s between the same pair cannot
      deadlock
- [ ] In a session where both vaults are unlocked, the source vault's
      metadata index drops the entry and the destination's gains it,
      without a full rebuild of either
- [ ] `--to-vault <unregistered-name>` fails cleanly before decrypting
      anything
- [ ] `--to-vault` naming the source vault itself is rejected
- [ ] A title collision in the destination behaves per the decision above

## Implementation

- [ ] `gage mv --to-vault` — resolve in the source, decrypt, run the
      destination's trust-cache check, encrypt to the destination's
      recipients, commit there, then remove and commit in the source
- [ ] `gage cp --to-vault` — the same without the source-side removal
- [ ] Deterministic two-vault lock ordering
- [ ] Index updates on both vaults where a session holds them

## Definition of done

Full test list green on all three CI platforms. An entry can be shared
into a vault with a different recipient list, without that vault being
unlocked, and without either vault's index going stale.

## Affects later milestones

Nothing downstream depends on this; M12's items are independent of it.
