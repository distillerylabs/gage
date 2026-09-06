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

- ["Why no per-directory sharing"](../../tdds/gage-cli-design.md) — why a
  second vault replaced per-subtree recipients, and the `mv --to-vault`
  example
- ["`mv`/`cp` are now cross-vault, and that's the sharing mechanism"](../../tdds/gage-cli-design.md)
- ["Local trust cache"](../../tdds/gage-cli-design.md) — the destination
  vault's cache is the one checked

## Decisions to make first

- **Lock ordering across two vaults.** Resolved: `mv`/`cp --to-vault`
  acquire *both* vaults' advisory write locks for the whole operation
  (not one at a time, released between steps) — that's what makes the
  crash-safety property below hold: nothing else can write to either
  vault while the pair is mid-move. They're taken in a fixed order,
  sorted by vault **name** rather than by filesystem path: `LockFilePath`
  already keys a vault's lock file by its registry name, not its path, so
  sorting by the same identifier the lock resource is actually named by
  is what keeps the ordering rule and the thing it orders in agreement.
  Two vault names are always distinct (they're map keys in global
  config), so the sort has no ties to break. A new package-level helper,
  `withTwoVaultLocks(a, b *Vault, fn func() error) error`, holds both
  locks around `fn`; `mv`/`cp` are its only callers. Two concurrent `mv`s
  in opposite directions between the same pair of vaults now always
  request the two locks in the same order, so neither can hold one while
  waiting on the other.
- **Is `mv` atomic across the two vaults?** Resolved, as recommended: no,
  it can't be — they're separate git repos. `mv`/`cp` write to the
  destination first — encrypt, write the file, `git commit` — and only
  then remove and commit in the source. "Verify" here means the
  destination's write-and-commit sequence returning without error, not a
  decrypt-and-compare read-back: the destination doesn't need to be
  unlocked (see the Goal section), so this device may hold no key that
  can decrypt what it just wrote there. A crash before the destination
  commit leaves the source untouched and nothing written to the
  destination; a crash after the destination commit but before the
  source removal leaves a recoverable duplicate — present in both — which
  is the direction a half-finished cross-vault write must fail in, per
  the plan's own recommendation. Never absent from both.
- **What `updated_by`/`created` look like in the destination.** Resolved,
  as recommended: the copy is a new entry — a fresh UUID (`NewEntryID()`)
  and a fresh `created`/`updated` stamp (both set to "now", the same as
  `insert`), not the source entry's own `created` carried over. The
  destination is a different repo, and reusing the source's id or
  timestamp would imply a relationship — "this is the same entry,
  relocated" — that nothing tracks or could keep consistent once the two
  copies diverge. `updated_by` is this device's name as recorded in the
  *source* vault (`ident.Device()`) — the only identity `mv`/`cp` ever
  hold, and the one that actually performed the share, in both vaults'
  histories. Title, description, value, and fields are carried over
  unchanged; only the provenance fields are refreshed.
- **Does the source entry's title collide in the destination?** Resolved,
  against the plan's own tentative "presumably applies, with `-f`": no
  check, and no `-f` on `mv`/`cp`. `insert`/`rename`'s duplicate-title
  guard works by decrypting every entry already in the vault being
  written to and comparing titles — but that vault is the one being
  written to *from inside itself*, always unlocked by construction. Here
  the vault being written to is the *destination*, which this milestone's
  whole premise is that this device need not be able to decrypt at all.
  Requiring the check would silently reintroduce the "destination must be
  unlocked" requirement for the common case (sharing with someone whose
  vault this device isn't a recipient of) while leaving it looking
  optional for the uncommon one (this device happens to also hold the
  destination's key) — the same query producing different guarantees
  depending on incidental recipient overlap. The design doc's own
  `mv`/`cp` syntax line already omits `-f` (`gage mv <query> --to-vault
  <name> [--use NAME] [--yes]`), unlike `insert`/`generate`, which is
  read here as confirming this rather than as an oversight. A destination
  that happens to already hold an entry with the same title ends up with
  two same-titled entries — no different from what `-f` already permits
  within a single vault.

## Tests (write first)

- [x] `gage mv --to-vault` removes the entry from the source vault and it
      becomes decryptable in the destination vault under the destination's
      recipients
- [x] `gage cp --to-vault` leaves the original in the source vault and
      adds a decryptable copy in the destination
- [x] `mv`/`cp --to-vault` succeeds even when the destination vault is not
      currently unlocked (encrypting to public keys needs no private key)
- [x] `mv`/`cp --to-vault` runs M10's trust-cache check against the
      *destination* vault's cache (not the source's) before encrypting
      there, and an unreviewed recipient change on the destination
      triggers the same diff warning a same-vault write would
- [x] Declining that trust-cache prompt aborts the whole operation — the
      source entry is untouched and nothing is written to the destination
- [x] `mv` produces a commit in each vault: an addition in the
      destination and a deletion in the source
- [x] A crash between the two writes leaves the entry present in the
      destination and still present in the source (a recoverable
      duplicate), never absent from both
- [x] Both vaults' locks are acquired in a deterministic order, so two
      concurrent opposite-direction `mv`s between the same pair cannot
      deadlock
- [x] In a session where both vaults are unlocked, the source vault's
      metadata index drops the entry and the destination's gains it,
      without a full rebuild of either
- [x] `--to-vault <unregistered-name>` fails cleanly before decrypting
      anything
- [x] `--to-vault` naming the source vault itself is rejected
- [x] A title collision in the destination behaves per the decision above

## Implementation

- [x] `gage mv --to-vault` — resolve in the source, decrypt, run the
      destination's trust-cache check, encrypt to the destination's
      recipients, commit there, then remove and commit in the source
- [x] `gage cp --to-vault` — the same without the source-side removal
- [x] Deterministic two-vault lock ordering
- [x] Index updates on both vaults where a session holds them

## Definition of done

Full test list green on all three CI platforms. An entry can be shared
into a vault with a different recipient list, without that vault being
unlocked, and without either vault's index going stale.

## Affects later milestones

Nothing downstream depends on this; M12's items are independent of it.
