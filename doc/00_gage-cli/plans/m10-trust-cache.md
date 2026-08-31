# M10 — Local trust cache

[← M9](m9-recipients.md) · [plan index](index.md) · next: [M11 — Cross-vault sharing](m11-cross-vault-sharing.md)

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
crypto risk and M8 isolates sync risk.

Two halves of the cache, and both matter: a verbatim copy of
`.gage/config.toml` (the file that's *diffed and shown*, because device
names make a legible warning) and the content hash of `.age-recipients`
(the file that's actually *consulted at encryption time*). Diffing only
the first would miss someone who edits only the second.

## Depends on

- **M5** — the encrypting commands the blocking check hooks.
- **M6** — `Vault.Unlock`, which the opportunistic check hooks.
- **M8** — `sync`, which needs its own hook since a clean fast-forward
  never unlocks anything.
- **M9** — recipient changes to detect, and `verify`'s comparison logic.

## Design references

- ["Trust boundaries"](../tdds/gage-cli-design.md) — the two questions
  that get conflated, and why this is guarantee #2's mitigation
- ["Local trust cache"](../tdds/gage-cli-design.md) — what's cached, when
  it's checked, and the two different resolution outcomes
- ["Cache regeneration isn't one-size-fits-all"](../tdds/gage-cli-design.md)

## Decisions to make first

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
      neither command encrypts anything — the same unlock hook M8's auto
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

## Implementation

- [ ] Trust cache storage: `$GAGE_STATE/<vault>/known-config.toml` (a
      verbatim copy) plus the stored `.age-recipients` content hash,
      written through M0's atomic-write helper
- [ ] Blocking pre-encrypt check on the `Vault` methods that encrypt
      (`insert`/`edit`/`generate`/`rename`, and M11's `mv`/`cp` against
      the destination vault)
- [ ] Non-blocking opportunistic check wired into `Vault.Unlock` itself
      (covering session `use` and every one-shot command's implicit
      unlock, mirroring M8's fetch+pull hook) *and* into `sync` directly,
      since a clean fast-forward sync can complete without ever calling
      `Unlock`
- [ ] Diff generation against the cached `config.toml` copy
- [ ] The two-outcome resolution logic: routine changes regenerate both
      cache halves; mismatched ones record that the mismatch was seen
      without clearing the inconsistency
- [ ] `RecipientChangeWarning` (or similar) structured type returned by
      the library on a trust-cache mismatch; `cmd/gage` renders it as the
      terminal diff + `[y/N]` prompt

## Definition of done

Full test list green on all three CI platforms. A recipient added by
another device produces a legible diff and a prompt on this one, once —
and a hand-edited `.age-recipients` produces a warning that doesn't go
away by clicking yes.

## Affects later milestones

- **M11 runs this check against the *destination* vault's cache**, not
  the source's — an entry moving into a vault gets encrypted to that
  vault's recipients, so that's the list whose changes matter.
