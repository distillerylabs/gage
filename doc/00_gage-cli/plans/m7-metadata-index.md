# M7 — Metadata index

[← M6](m6-session-mode.md) · [plan index](index.md) · next: [M8a — Sync: transport & detection](m8a-sync-transport.md)

> **Recommended model: Sonnet.** A pure caching layer — correctness doesn't change, and M5/M6's tests passing unmodified is the proof of that.

## Goal

In-session decrypt-once cache for `ls`/`search`/`show` resolution, with
incremental updates on insert/edit/rm. A pure performance/UX layer on top
of M6 — correctness doesn't change.

The index lives on `Session` alongside each vault's cached `Identity`,
**never on `Vault`** — `Vault` stays a stateless, identity-agnostic
operator over ciphertext in both one-shot and session mode. The index is
never written to disk, so "nothing plaintext outlives the process" still
holds even though metadata browsing now requires decryption.

This milestone also introduces `search`/`grep`, which is the first
command that matches against body text rather than just titles — and the
one thing the index deliberately doesn't accelerate, since caching body
text means holding every secret value in memory for the session.

## Depends on

- **M3** — entry decryption and enumeration.
- **M5** — the resolver the index accelerates.
- **M6** — `Session`, which owns the index.

## Design references

- ["Addressing entries & the metadata index"](../tdds/gage-cli-design.md)
  — why `ls` can't be cheap anymore, and what the index holds
- ["Library architecture"](../tdds/gage-cli-design.md) — the index
  belongs to `Session`, not `Vault`
- ["A few decisions worth calling out"](../tdds/gage-cli-design.md) — the
  in-memory, session-scoped, never-written-to-disk rule

## Decisions made

- **What the index holds.** Resolved: metadata only — `title`,
  `description`, and the bookkeeping `ls` prints alongside them (dates,
  `updated_by`). Never `value`, never `fields`. So the index accelerates
  *resolution* and title/description matching, and nothing else:
  `search`/`grep`'s body-text pass decrypts every entry on demand and
  drops that plaintext before it returns, every time it runs. That makes
  body search the one command the index doesn't speed up — the cheaper
  side of the trade, because the alternative keeps an entire vault's
  secret values in memory for the length of a session, which is the
  exposure "nothing plaintext outlives the process" exists to bound.
- **Is the index page-locked?** Resolved: yes. Titles and descriptions
  are plaintext the vault exists to hide, and unlike M4/M5's
  decrypt-per-command they now stay resident for a whole session. This
  needs no new mechanics — it's M2's `memlock` package used as-is — but
  it does constrain the data structure:
  - One `memlock.Alloc` arena per vault index, holding the packed
    title/description bytes, with index entries referencing offsets into
    it. Not one `Alloc` per entry: `Alloc` burns up to two pages of
    slack per call to guarantee an unshared page, so per-entry
    allocation is pathological for a vault of any size.
  - Ordinary Go strings and maps can't hold this material — they're
    immutable and GC-copied, so they can't be zeroed reliably. Transient
    copies made while *formatting* output are out of scope; the arena
    bounds what lives for the session, not what exists for a `printf`.
  - Page-lock failure keeps M2's policy: warn once and proceed, not
    refuse. It must not produce a *second* warning when the vault's
    `Identity` already warned at unlock.
  - Index teardown is `Identity.Close()`'s shape: unlock the pages, zero
    the arena, idempotent, and driven by the same code path that closes
    the `Identity`.
- **One-shot mode gets no index.** Confirmed. A one-shot command still
  builds an equivalent in-memory map for the duration of one call — the
  point is that it isn't persisted or shared, and it dies with the
  process a few milliseconds later.

## Tests (write first)

- [x] The first `ls`/`show`/`search` after `use` triggers exactly one
      full-decrypt pass over the vault (assert via decrypt call-count
      instrumentation) — a first `search` builds the index from that same
      pass rather than decrypting twice
- [x] Subsequent `ls`/`show` calls, and the title/description half of
      `search`, reuse the index without re-decrypting unchanged entries
- [x] `insert`/`edit`/`rm` update the in-memory index incrementally,
      without triggering a full rebuild
- [x] `rename` updates the indexed title, and a subsequent query resolves
      against the new title and not the old one
- [x] Locking a vault (explicit `lock` or idle timeout) discards that
      vault's index along with its `Identity` — the index must not
      outlive the key that produced it
- [x] Each vault in a multi-vault session has its own index; unlocking a
      second vault doesn't rebuild or disturb the first's
- [x] `gage reindex` forces a full rebuild and picks up changes made
      outside `gage` (e.g. a manual `git pull`)
- [x] `search`/`grep` matches against title, description, and body text
- [x] A body-text `search` doesn't cache what it decrypted: a second
      identical `search` re-decrypts (decrypt call-count again), while a
      title-only query in between still hits the index
- [x] No secret value ever enters the index: after `ls`, `show`, and
      `search` over a vault holding a known sentinel value, that
      sentinel appears nowhere in the index's backing arena
- [x] The index arena is page-locked through the same `memlock` seam as
      `Identity` (assert via the fake locker M2's vault tests already
      use), and a locking failure warns once and proceeds rather than
      refusing to build the index — with no second warning when unlock
      already warned for the same vault
- [x] Discarding an index zeroes its arena as well as releasing the page
      lock, and is idempotent
- [x] `search` output never includes a matched secret value unless the
      command is explicitly asked for it — a `grep` that prints the
      matching line would leak `value` into scrollback
- [x] The index is never written to disk: no file appears under
      `$GAGE_STATE`, `$GAGE_DATA`, or the vault during a session that
      builds one

## Implementation

- [x] Index type held on `Session`, keyed per vault alongside the cached
      `Identity`; discarded by the same code path that closes the
      `Identity`
- [x] Backing store: one `memlock.Alloc` arena per vault holding packed
      title/description bytes, entries referencing offsets into it;
      grown or rebuilt on incremental update
- [x] Index teardown mirroring `Identity.Close()`: unlock, zero,
      idempotent
- [x] Build index on first `ls`/`show`/`search` per vault
- [x] Incremental update on insert/edit/rm/rename
- [x] `gage reindex`
- [x] `gage search` / `gage grep`: index-served for title/description,
      decrypt-on-demand for the body pass, with output that doesn't leak
      matched values by default

## Definition of done

Full test list green on all three CI platforms. A session that touches a
vault repeatedly decrypts it once for metadata, and no secret value is
ever held in the index. Nothing about correctness changed, and the tests
from M5 and M6 still pass unmodified — that's the strongest signal this
stayed a pure caching layer.

## Affects later milestones

- **M8a must invalidate or rebuild this index after a successful
  fast-forward pull**, and **M8b after each applied conflict
  resolution.** Auto-pull on unlock changes `entries/` underneath
  a live session, and nothing in the design doc currently mentions it —
  see [A7](open-questions.md). This is the single most likely thing to be
  missed in M8a.
- **M11's cross-vault `mv`/`cp`** must update the source vault's index
  (entry removed) and the destination's (entry added, if that vault is
  unlocked in the same session).
