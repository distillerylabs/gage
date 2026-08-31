# M7 — Metadata index

[← M6](m6-session-mode.md) · [plan index](index.md) · next: [M8a — Sync: transport & detection](m8a-sync-transport.md)

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
command that matches against body text rather than just titles.

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

## Decisions to make first

- **What the index holds.** The design says "just the metadata fields
  (title, description, dates, `updated_by`)" — but `search`/`grep`
  matches body text too. Either the index holds `value`/`fields` (a much
  larger plaintext footprint held in memory, and one that probably wants
  the same page-locking treatment as key material), or `search` decrypts
  on demand and only *resolution* uses the index. Decide explicitly;
  this is a memory-exposure decision, not just a performance one.
- **Is the index page-locked?** Follows from the above. Titles and
  descriptions are sensitive; secret values more so.
- **One-shot mode gets no index** — confirm this stays true even though
  a single one-shot command decrypts everything once anyway. (It does
  build an equivalent in-memory map for the duration of one call; the
  point is it isn't persisted or shared.)

## Tests (write first)

- [ ] The first `ls`/`show`/`search` after `use` triggers exactly one
      full-decrypt pass over the vault (assert via decrypt call-count
      instrumentation)
- [ ] Subsequent `ls`/`show`/`search` calls in the same session reuse the
      index without re-decrypting unchanged entries
- [ ] `insert`/`edit`/`rm` update the in-memory index incrementally,
      without triggering a full rebuild
- [ ] `rename` updates the indexed title, and a subsequent query resolves
      against the new title and not the old one
- [ ] Locking a vault (explicit `lock` or idle timeout) discards that
      vault's index along with its `Identity` — the index must not
      outlive the key that produced it
- [ ] Each vault in a multi-vault session has its own index; unlocking a
      second vault doesn't rebuild or disturb the first's
- [ ] `gage reindex` forces a full rebuild and picks up changes made
      outside `gage` (e.g. a manual `git pull`)
- [ ] `search`/`grep` matches against title, description, and body text
- [ ] `search` output never includes a matched secret value unless the
      command is explicitly asked for it — a `grep` that prints the
      matching line would leak `value` into scrollback
- [ ] The index is never written to disk: no file appears under
      `$GAGE_STATE`, `$GAGE_DATA`, or the vault during a session that
      builds one

## Implementation

- [ ] Index type held on `Session`, keyed per vault alongside the cached
      `Identity`; discarded by the same code path that closes the
      `Identity`
- [ ] Build index on first `ls`/`show`/`search` per vault
- [ ] Incremental update on insert/edit/rm/rename
- [ ] `gage reindex`
- [ ] `gage search` / `gage grep`, with output that doesn't leak matched
      values by default

## Definition of done

Full test list green on all three CI platforms. A session that touches a
vault repeatedly decrypts it once. Nothing about correctness changed, and
the tests from M5 and M6 still pass unmodified — that's the strongest
signal this stayed a pure caching layer.

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
