# M4 — CRUD (one-shot mode)

[← M3](m3-entry-format.md) · [plan index](index.md) · next: [M5 — Query resolution](m5-query-resolution.md)

> **Recommended model: Sonnet.** Straightforward CRUD. The two judgment calls — commit-message confidentiality and commit author identity — are already settled below (see "Decisions made"), not things to work out while coding.

## Goal

`insert`, `cat`, `rm`, `ls` — the first end-to-end usable slice: store and
retrieve an entry from the command line, decrypting everything every time
(no cache). `cat` and `rm` are addressed by UUID or exact title match only
for now; M5 upgrades both onto the shared resolver.

Every write is a git commit (no push yet), **held under M0's vault lock**
for the whole read-modify-commit sequence. This is the first milestone
where two `gage` processes could corrupt each other, and the design
actively encourages multiple processes — see
["Trade-off vs. a shared agent"](../../tdds/gage-cli-design.md).

Each `Vault` method takes the `Identity` from M2's `Vault.Unlock` as an
explicit parameter and never unlocks internally — `cmd/gage`'s one-shot
handler is what calls `Unlock` → the CRUD method → `Identity.Close()` for
each invocation. M6 reuses these same methods unchanged by caching the
`Identity` across calls instead of closing it after one.

`gage insert`'s value comes from a masked prompt (default),
`--value-stdin`, or `-m|--multiline`; `-e|--edit` (a full `$EDITOR`
template) is deferred to M5, once `gage edit`'s `$EDITOR`-on-scratch-file
round trip exists for it to share.

## Depends on

- **M0** — `vaultlock`, exit codes, the in-process CLI test harness.
- **M2** — `Unlock`/`Identity`/`Close`, and the recorded device name for
  `updated_by`.
- **M3** — entry read/write and enumeration.

## Design references

- ["Entry CRUD"](../../tdds/gage-cli-design.md) — the command surface and
  the notes on `insert`'s mutually exclusive input modes
- ["Sync model"](../../tdds/gage-cli-design.md) — "commit is local, instant,
  and always happens"; push is M8a's problem
- ["Session model"](../../tdds/gage-cli-design.md) — one-shot mode's
  unlock-use-close contract

## Decisions made

- **Commit message format: the UUID alone, nothing else.** Every write is
  a commit and the messages are permanent, greppable history. They must
  not leak entry titles — filenames are opaque UUIDs precisely so someone
  with read access learns nothing, and a commit message reading `insert:
  ProtonMail` would undo that entirely. No `insert `/`rm ` verb prefix
  either — that would still leak which operation touched an entry across
  history, so the message body is exactly the entry's UUID (e.g.
  `4b9d7710-...`) and nothing more. **This is a confidentiality decision,
  not a cosmetic one.**
- **Commit author identity: fixed and anonymous.** go-git requires a
  name/email on the commit object. Using the user's git config leaks
  their identity into a vault that may be shared, so every commit uses a
  fixed, anonymous identity — `gage <gage@localhost>` — regardless of who
  is running the command or which device made the change. That per-device
  detail already lives in `updated_by`, safely inside the ciphertext, so
  nothing is lost by keeping the git-visible author generic.
- **`ls` output format:** one entry per line, title first, sorted, with a
  partial (short, unambiguous-prefix) UUID printed alongside each title
  so entries are addressable from `ls` output without a full `cat`.
  (Extended in [M7](m7-metadata-index.md), which appends the entry's
  `created`/`updated` dates and `updated_by` — the metadata its index
  caches anyway. Title-then-partial-UUID still leads the line.)

## Tests (write first)

- [x] `gage insert` followed by `gage cat` round-trips the value through
      the actual CLI (not just the library)
- [x] `gage insert --value-stdin` reads the value from stdin (one
      trailing newline trimmed) and round-trips through `cat` identically
      to the default prompt path
- [x] `gage insert -m` captures multiple lines from the terminal until
      EOF and round-trips through `cat` byte-for-byte (PTY-driven)
- [x] With none of `-m`/`--value-stdin` given, `gage insert` prompts once
      for `value` via the library's `Prompter` (a fake in tests) rather
      than reading stdin directly
- [x] Passing more than one of `-m`/`--value-stdin` is rejected with a
      usage error before any prompt or read happens
- [x] `gage insert --description TEXT` stores the description, and `cat`
      shows it
- [x] `gage insert` produces exactly one new git commit
- [x] The commit message is exactly the entry's UUID — no verb prefix,
      title, or other plaintext metadata
- [x] Every commit's author is the fixed `gage <gage@localhost>` identity,
      never the user's git config, regardless of who runs the command
- [x] `gage insert` sets `created`, `updated`, and `updated_by`;
      `updated_by` matches the device name recorded in M2
- [x] `gage ls` lists the inserted entry's title alongside a partial UUID
- [x] `gage ls` on an empty vault succeeds with no output and exit 0 —
      not an error
- [x] `gage rm` deletes the file under `entries/` and commits the deletion; a
      subsequent `cat`/`ls` no longer shows the entry
- [x] `gage cat` on an unknown title/UUID fails with a clear error and the
      not-found exit code from M0's taxonomy
- [x] Inserting a duplicate title without `-f|--force` is rejected;
      `-f|--force` allows it
- [x] With two vaults registered in global config, `gage insert --use
      <other>` / `gage cat --use <other>` write to and read from that named
      vault specifically, not the default one
- [x] With two vaults registered, an entry command given with no `--use`
      flag operates against `current` from global config, not the other
      registered vault — the one-shot counterpart to `vault set-default`
- [x] `--use <unregistered-name>` fails with a clear error before
      prompting for a passphrase
- [x] A write holds the vault lock across the whole read-modify-commit
      sequence: a second process attempting a concurrent write observes
      contention rather than interleaving, and neither commit is lost
- [x] A read-only command (`cat`/`ls`) does not block on a lock held by
      another read-only command
- [x] The one-shot handler calls `Identity.Close()` on every exit path,
      including when the CRUD method returns an error

## Implementation

- [x] `gage insert`: masked `Prompter` prompt (default), `--value-stdin`
      (read + trim), or `-m|--multiline` (terminal capture until EOF) for
      the value; mutually exclusive, validated before any I/O happens.
      `--description TEXT`, `-f|--force`
- [x] One-shot `-u|--use NAME` flag, resolved on every entry command
      against global config's registered vaults; omitted, it falls back to
      `current` — same resolution `vault set-default` (M1) writes into
- [x] `gage cat` (exact UUID/title only)
- [x] `gage rm`
- [x] `gage ls`, printing each title with its partial UUID
- [x] Commit-per-write (go-git worktree add + commit) with the UUID-only
      message and fixed `gage <gage@localhost>` author decided above
- [x] Vault lock acquisition around every write, using M0's `vaultlock`;
      released on every exit path
- [x] One-shot command handler in `cmd/gage`: `Unlock` → CRUD method →
      `Identity.Close()`, with `Close` guaranteed on error paths

## Definition of done

Full test list green on all three CI platforms. `gage` is a usable, if
minimal, single-device secrets manager: insert, list, read, delete, all
committed, all locked against concurrent writers.

## Affects later milestones

- Commit-per-write under the lock is what M8a's auto-push sits on and what
  M9's single-commit `--reencrypt` has to preserve.
- The `Unlock` → use → `Close` handler is the shape M6's `Session`
  deliberately *doesn't* change — it only holds the `Identity` longer.
- `cat` and `rm`'s exact-match-only addressing is temporary; M5 must
  leave neither behind on it.
