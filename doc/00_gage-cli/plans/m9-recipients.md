# M9 — Identity & recipient management

[← M8b](m8b-sync-conflicts.md) · [plan index](index.md) · next: [M10 — Local trust cache](m10-trust-cache.md)

## Goal

The first place multi-device/multi-recipient scenarios become testable —
naturally after single-user CRUD and sync are solid. `gage identity
add/list`, `gage recipient add/remove/list/verify`, and the atomic,
all-or-nothing `--reencrypt` pass.

`--reencrypt`'s crash-safety is the substance of this milestone: every
entry is re-encrypted in the working tree first, and the recipient-list
files and every touched entry land in **exactly one commit** — nothing
commits until all of it succeeds. A crash mid-`--reencrypt` leaves HEAD
untouched rather than half-migrated.

## Depends on

- **M2** — identity generation (reused for additional devices).
- **M3** — entry encrypt/decrypt, iterated over by `--reencrypt`.
- **M4** — commit-per-write under the vault lock.
- **M8a** — sync, so a recipient change made on one device reaches another.

## Design references

- ["Recipient / access management"](../tdds/gage-cli-design.md) — the
  command surface and `--reencrypt`'s all-or-nothing contract
- ["Local identity storage"](../tdds/gage-cli-design.md) — why losing an
  identity file is a recovery problem, not a backup problem
- ["`--reencrypt` is all-or-nothing, on purpose"](../tdds/gage-cli-design.md)
  — why incremental commits are worse than a long single one
- ["`identity` vs `recipient` stay separate"](../tdds/gage-cli-design.md)

## Decisions to make first

- **What `--reencrypt` does about the vault lock on a long run.**
  Re-encrypting a large vault could hold the lock for minutes, blocking
  every other `gage` process. Hold throughout (simple, correct, blocks),
  or something staged? Recommend holding throughout with a clear
  contention message — correctness first.
- **`recipient add` without `--reencrypt` still commits** the two changed
  files. Unstated in the design; confirm and test.
- **Interaction between `--reencrypt` and push.** The single commit is
  atomic locally, but the push after it can fail. Confirm that's
  acceptable (it is — the guarantee is about HEAD) and that the message
  says so.
- **Does `recipient remove` refuse to remove the last recipient**, or to
  remove the local device's own key (locking yourself out)? Neither is
  specified.

## Tests (write first)

**Identity**

- [ ] `gage identity add` registers a device and prints a public key
- [ ] Each device registered via `gage identity add` gets its own wrapped
      identity file at `$GAGE_DATA/identities/<vault>/<device>.age`;
      deleting one device's file doesn't affect another device's ability
      to decrypt
- [ ] `gage identity add` with a device name already registered as a
      recipient of that vault is rejected rather than silently
      overwriting an existing identity file — including the common case
      of two machines sharing a hostname, where the default name
      collides without anyone typing it
- [ ] `gage identity add --device NAME` overrides the hostname default;
      omitted, the normalized hostname is used
- [ ] `gage identity add` with no `--method` takes the vault's
      `[method].default`; `--method passphrase` is equivalent, and any
      other value fails on the same allowlist as `init` — the flag sets
      *this device's* method, which is per-device by design and recorded
      locally rather than in the vault (Q-METHOD-SCOPE)
- [ ] `gage identity list` reports the local device's registered
      identities for a vault
- [ ] Recovering from a lost identity file needs no file restore: a fresh
      `gage identity add` on the affected device plus `gage recipient add`
      from any surviving recipient restores access under a new identity

**Recipients**

- [ ] `gage recipient add` (no `--reencrypt`) affects only future writes —
      entries that existed before the add remain undecryptable by the new
      recipient's key
- [ ] `gage recipient add` without `--reencrypt` still commits the updated
      `.age-recipients` and `config.toml` together
- [ ] `gage recipient add --reencrypt` makes all pre-existing entries
      decryptable by the new recipient
- [ ] `gage recipient remove` without `--reencrypt` is rejected outright;
      with `--reencrypt` it re-encrypts every entry, excluding the
      removed key
- [ ] `gage recipient remove --reencrypt` prints the "revokes future
      access only — anything already read can't be unread" warning
- [ ] `gage recipient list` prints every recipient's device name and
      public key, matching `.gage/config.toml`'s `[[recipients]]` exactly,
      and reflects an add/remove from the same test run

**`--reencrypt` atomicity**

- [ ] A simulated crash/interruption partway through `--reencrypt` leaves
      HEAD byte-identical to before the command ran — no partial commit,
      no leftover dirty `entries/` once `gage` next starts
- [ ] Retrying `--reencrypt` after such an interruption produces the same
      correct end state as an uninterrupted run
- [ ] A single entry failing during `--reencrypt` (simulated decrypt/
      encrypt error) aborts the whole operation before any commit and
      reports which entry failed; no entries are left partially migrated
- [ ] `--reencrypt` lands the recipient-list files
      (`.age-recipients`/`config.toml`) and every re-encrypted entry in
      exactly one commit — never a commit containing only one side of
      that pair
- [ ] `--reencrypt` holds the vault lock for its whole duration; a
      concurrent write observes contention rather than interleaving

**Dirty working tree**

- [ ] A dirty `entries/` working tree left by a simulated crash is reset
      to HEAD before any subsequent write (`insert`/`edit`/`generate`/
      `--reencrypt`) proceeds, rather than being folded into that write's
      commit
- [ ] Resetting a dirty `entries/` working tree emits a one-line warning
      naming the discarded paths (via `Prompter`, a fake in tests) before
      proceeding — the reset is automatic, but never silent, since the
      "only an interrupted `--reencrypt` could cause this" assumption
      isn't provable, only likely

**`verify`**

- [ ] `gage recipient verify` exits 0 and reports "in sync" when
      `.age-recipients` and `config.toml` agree; exits 1 and lists the
      specific differences when they don't
- [ ] `gage recipient verify` succeeds against a vault where the local
      device has no identity file at all (e.g. right after `clone`, before
      `gage identity add`) — no `Vault.Unlock`, no `Prompter` interaction,
      proving the "needs no unlock, safe to run in CI" claim rather than
      just asserting it

## Implementation

- [ ] `gage identity add/list` (reuses M2's passphrase identity-generation
      path for additional devices — each gets its own
      `$GAGE_DATA/identities/<vault>/<device>.age`)
- [ ] `gage recipient add/remove [--reencrypt]`: stage every re-encrypted
      entry in the working tree first; commit only after all entries
      succeed, with the recipient-list files and every touched entry in
      that same single commit. Add without `--reencrypt` still commits the
      recipient-list pair
- [ ] `gage recipient list`
- [ ] Precondition check on every write (`insert`/`edit`/`generate`/
      `rename`/`--reencrypt`): if `entries/` is unexpectedly dirty at
      start, warn once via `Prompter` (naming the discarded paths) and
      reset it to HEAD before proceeding — the only *expected* source of
      unexpected dirtiness is an interrupted `--reencrypt`, but the
      warning fires regardless of cause, the same "warn and proceed"
      posture as an unreachable network in M8a. **This runs under the vault
      lock**, so it can never race another process's in-flight write
- [ ] `gage recipient verify`: reads `.age-recipients` and
      `.gage/config.toml` directly — both plaintext — and never calls
      `Vault.Unlock`/`Prompter`, so it needs no identity to run

## Definition of done

Full test list green on all three CI platforms. Two simulated devices can
be added to a vault, one can be removed with full re-encryption, and an
interrupted re-encryption is provably invisible in committed history.

## Affects later milestones

- **M10 diffs exactly the pair this milestone keeps in sync.** The
  trust cache's whole premise is that a legitimate `recipient add` touches
  `.age-recipients` and `config.toml` together — the mismatch case is the
  tampering signature.
- The dirty-tree reset established here runs under the vault lock; if
  that ordering is ever loosened, M4's concurrency guarantee breaks.
