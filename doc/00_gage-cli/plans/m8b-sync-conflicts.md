# M8b — Sync: conflict resolution

[← M8a](m8a-sync-transport.md) · [plan index](index.md) · next: [M9 — Identity & recipient management](m9-recipients.md)

## Goal

`gage sync`'s interactive resolution: for each genuinely conflicting
entry, decrypt both sides, show who wrote each and when, and apply the
choice — **keep local / keep remote / keep both** — then land the result
as a real merge commit.

Split from M8a because detection and resolution are separable risks, the
same way M2 isolates crypto and M10 isolates the trust cache. M8a leaves
the tool fully usable: divergence is detected, classified, and reported.
This milestone is what lets you act on it without leaving `gage`.

**`keep both` is the substance here.** It writes the losing version as a
new entry under a fresh UUID, so resolving a conflict never destroys a
secret — the two versions become two entries sharing a title, which M5's
ambiguous-query resolver already knows how to present. Choosing wrong
under time pressure becomes a recoverable mistake rather than a silent
loss, which is the right default for a tool whose premise is not losing
secrets.

## Depends on

- **M3** — entry decrypt/encrypt, to show both sides and write the
  resolution.
- **M5** — the resolver, which is what makes duplicate titles survivable
  and therefore `keep both` viable.
- **M7** — the index, which resolution mutates (entries added, replaced,
  or removed).
- **M8a** — divergence detection and its classification of what
  conflicted.

## Design references

- ["What \"diverged\" actually means"](../tdds/gage-cli-design.md) — the
  five situations and which ones reach this milestone
- ["Resolving an entry conflict"](../tdds/gage-cli-design.md) — the
  prompt, `keep both`'s rationale, and why the result is a merge commit
- ["Local trust cache"](../tdds/gage-cli-design.md) — recipient-file
  conflicts resolve through the trust-cache confirmation, not this menu

## Decisions to make first

- **Where `keep both` puts the losing version's title.** Verbatim (two
  entries identically titled, resolver disambiguates by UUID and date),
  or suffixed (`ProtonMail (conflicted 2026-08-30)`)? Verbatim is
  honest and leans on machinery that already exists; suffixed is easier
  to spot in `ls` but invents a naming convention. Lean verbatim.
- **Does `keep both` preserve the losing side's `updated_by`/`updated`?**
  It should — the whole point is not losing information — but that means
  writing an entry whose `updated_by` is another device, which no other
  code path does.
- **Non-interactive behavior.** `--script`/`--stdin` (M12) and CI have no
  one to prompt. Recommend: `gage sync` fails on first conflict with a
  clear message rather than picking a side, and grows a
  `--strategy=local|remote|both` flag if a real need appears. Deciding
  now avoids `--yes` accidentally acquiring "resolve conflicts silently"
  as a meaning — it means something much narrower elsewhere.
- **Resolution and the vault lock.** Resolution is a long interactive
  operation holding a write lock. Hold throughout (blocking other
  processes for as long as the user takes to answer), or acquire per
  applied choice? Leaning hold-throughout for consistency with
  `--reencrypt`, but the human-in-the-loop duration makes this less
  obvious than it was there.

## Tests (write first)

**Resolution mechanics**

- [ ] A conflicting entry is presented with both sides' `updated` and
      `updated_by` decrypted and shown, via `Prompter` (a fake in tests)
      as a typed value — never printed from the library
- [ ] `keep local` leaves the local version in place; the remote version
      is not present afterward
- [ ] `keep remote` replaces the local version with the remote one
- [ ] `keep both` keeps the local version at its existing UUID *and*
      writes the remote version as a new entry under a fresh UUID — both
      are decryptable afterward, and neither value was lost
- [ ] After `keep both`, the two same-titled entries resolve through M5's
      ambiguous-query path (candidate list, one-shot fails, session
      prompts) rather than one shadowing the other
- [ ] `skip` leaves that entry conflicted, moves to the next, and ends
      the sync without pushing — the tree still has unresolved conflicts
- [ ] `abort` restores the exact pre-sync state: HEAD, working tree, and
      index unchanged, nothing pushed
- [ ] A delete/modify conflict (one side removed the entry, the other
      edited it) is presented with the surviving version shown, and
      resolving it either way produces a consistent tree

**History**

- [ ] A resolved sync produces a merge commit with two parents — the
      local head and the fetched remote head
- [ ] Local commit granularity survives the merge: the individual local
      write commits are still present and walkable, not flattened
- [ ] `gage log` and `history --decrypt` (M12) traverse a merge commit
      without error — asserted structurally here so M12 doesn't discover
      it late
- [ ] The merge commit's message names the number of entries resolved
      and contains no entry title or plaintext (extends M4's
      commit-message confidentiality rule to merges)

**Unlocking and interaction with other milestones**

- [ ] A sync that fast-forwards cleanly never unlocks an identity —
      resolution is what forces the unlock, not `sync` itself
- [ ] A sync whose divergence touches only *different* entries also
      completes without unlocking (M8a merges it; this milestone isn't
      reached)
- [ ] A sync reaching a real entry conflict prompts for unlock at that
      point, not before
- [ ] In a session where the vault is already unlocked, resolution reuses
      the cached `Identity` without re-prompting
- [ ] Resolution updates M7's metadata index: entries replaced by `keep
      remote` and added by `keep both` are visible to the next `ls`
      without a manual `reindex`
- [ ] A recipient-file conflict is *not* presented through this entry
      menu — it routes to M10's trust-cache confirmation instead
      (asserted here as "doesn't reach the entry resolver"; M10 owns the
      positive case)

**Non-interactive**

- [ ] `gage sync` with no interactive `Prompter` available fails on the
      first conflict with a clear message, rather than choosing a side
- [ ] `--yes` does *not* resolve conflicts — it retains only its
      trust-cache meaning

## Implementation

- [ ] Conflict presentation as a typed value: both decrypted entries plus
      their metadata and the conflict kind, returned by the library;
      `cmd/gage` renders the `[l/r/b/s/q]` prompt from the design doc
- [ ] `keep local` / `keep remote` / `keep both` application, with `keep
      both` allocating a fresh UUID for the losing side and preserving its
      `updated`/`updated_by`
- [ ] `skip` and `abort` paths, with `abort` restoring the pre-sync state
      exactly
- [ ] Delete/modify conflict handling
- [ ] Merge commit creation with both parents, message naming the count
      of resolved entries and no plaintext
- [ ] Lazy unlock: resolution triggers `Vault.Unlock` at the point a
      conflict needs decrypting, reusing a session's cached `Identity`
      when there is one
- [ ] Index updates for every applied resolution
- [ ] Non-interactive refusal, per the decision above

## Definition of done

Full test list green on all three CI platforms. Two simulated devices can
edit the same entry, and `gage sync` resolves it three different ways
with no version silently lost — leaving a real merge commit that `log`
and `history --decrypt` can walk.

## Affects later milestones

- **M10** owns the positive case for recipient-file conflicts; this
  milestone only asserts they don't fall into the entry resolver.
- **M12**'s `history --decrypt` must handle merge commits, which this
  milestone is the first to create — the structural test here is what
  keeps that from being discovered late.
