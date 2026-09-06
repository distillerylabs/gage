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
  - **How a diverged vault is repaired: `gage recipient verify
    --repair`.** Resolved: ship the explicit verb alongside the refusal.
    "Hand-edit the plaintext file" is not actually an exit, because M9's
    `withVaultWrite` resets a dirty working tree before every write: an
    edit to `.age-recipients` that the operator does not also commit is
    silently discarded by their next `gage insert`, so the documented
    escape hatch would be a thing gage itself undoes. `--repair` rewrites
    `.age-recipients` from `config.toml`'s list under the vault lock,
    behind a `Prompter.Confirm` naming every key it drops *and* every key
    it adds, and commits the result as `gage: recipient repair`. Two
    things it deliberately does not do: it does not regenerate the trust
    cache — repair fixes the *inconsistency*, not the *recipient change*,
    and the config diff still has to be reviewed at the next encrypt —
    and it takes no `--reencrypt`, since rewriting a plaintext key list
    says nothing about whether existing ciphertext should be rewritten;
    that stays an explicit `recipient add`/`remove --reencrypt`
    afterwards. A `Prompter` that answers no writes and commits nothing,
    which includes every non-interactive one.
- **What `--yes` does in the mismatched case.** Resolved: it proceeds and
  never regenerates the cache — and that falls out structurally rather
  than as a special case, because **`--yes` is not a library concept at
  all**. The library asks through a new
  `Prompter.ConfirmRecipientChange(RecipientChangeWarning) (bool, error)`
  — typed value in, decision out, exactly as `Choose(CandidateList)`
  already works — and `--yes` is `cmd/gage`'s prompter answering yes
  without rendering the question, the same way "how many passphrase
  attempts does a human get" is a `cmd/gage` policy (see M2's
  `maxUnlockAttempts`). The two-outcome regeneration rule is decided
  library-side off `VerifyRecipients`, so *who* answered cannot change
  whether the cache is regenerated. Two consequences worth stating:
  `--yes` is a persistent root flag, so it covers `insert`/`edit`/
  `generate`/`rename` in CI and not only the design's `mv`/`cp`; and it
  answers *only* `ConfirmRecipientChange`, never plain
  `Prompter.Confirm`, so M9's "remove this device's own key?" stays a
  real question in a scripted run.
  - **Settled during implementation: `--yes` is refused inside a
    session**, both when typed on a session line and on the way into one
    (`gage --yes` with a terminal). It is a flag for runs with nobody to
    show a diff to, and a session is the opposite; it would also not mean
    what it looks like. A `--yes` on a session line never reaches the
    check at all, because the blocking check asks through
    `ident.frontend()` — the Prompter the *session* unlocked with — and
    not through `app.Prompter`, which is what the flag wraps. And
    `gage --yes` on the way in would wrap the Prompter the session then
    holds for its whole life, silently approving every recipient change
    until the session ended. Refusing beats either. `-m`/`--value-stdin`
    are already refused in a session for the same shape of reason.
- **Where the cache lives when a vault is renamed or re-cloned.**
  Resolved: accept `$GAGE_STATE/<vault>/known-config.toml`, keyed by
  local vault name. The cache is disposable device state by design ("not
  something to carry to a new machine"), and a clone under a different
  name is a first use, which is already trusted on faith by the bullet
  below. The stabler keys are all worse: a remote URL doesn't exist for a
  local-only vault and changes when a remote moves, and the initial
  commit hash survives a rename but not a re-`init` — both buy a narrow
  case at the cost of a key that can silently point at the wrong vault.
- **Does `vault remove` delete the cache?** Resolved: yes — it removes
  `$GAGE_STATE/<vault>/` along with the registration. Keeping it means a
  re-add silently inherits an approval for a list nobody looked at in the
  interval, which is the one thing this cache exists to prevent;
  deleting it makes a re-add a first use, the documented weaker-but-
  honest bootstrap. It also matches what `vault remove` already promises
  — forget this vault locally, touch none of its own files — since the
  trust cache is precisely local memory of that vault.
- **First-use bootstrap.** Confirmed: silent, and on the first
  *successful* use — a successful `Unlock`, a successful `sync`, or a
  successful pre-encrypt check — never on a failed unlock, so a wrong
  passphrase can't bootstrap an approval. The very first recipient list a
  device sees is trusted on faith; that is inherent to a
  trust-on-first-use record and is why the mechanism is a backstop rather
  than the guarantee itself.
- **A successful local `recipient add`/`remove` regenerates the cache.**
  Resolved: yes, as its last step. The operator just reviewed that list
  by typing the command; without this, `gage recipient add` would warn
  them about their own change at their very next `insert`, which is the
  fastest way to teach someone to ignore the warning. It is the "or
  explicitly reviewed" half of the design's definition of the cache.

## Tests (write first)

- [x] After a device's first successful use of a vault, `known-config.toml`
      is written to local state; a subsequent unreviewed recipient change
      triggers a diff warning before the next encrypt
- [x] The cache stores both halves — the verbatim `config.toml` copy and
      the `.age-recipients` content hash
- [x] A change to `.age-recipients` alone, with `config.toml` untouched,
      still triggers the warning — the case that exists specifically
      because diffing `config.toml` alone would miss it
- [x] The warning fires the same way for a one-shot `insert`/`edit`/
      `generate` as for a session-mode one — the cache check hooks the
      `Vault` methods that encrypt, not `Session.Use`, since one-shot mode
      never calls `Session.Use` at all
- [x] A non-blocking opportunistic warning fires on a plain `gage show`/
      `ls` in one-shot mode when recipients have changed, even though
      neither command encrypts anything — the same unlock hook M8a's auto
      fetch+pull uses, not the mandatory pre-encrypt check
- [x] `gage sync` surfaces the opportunistic warning even when it resolves
      via a clean fast-forward with no conflicting entry — the case where
      sync never unlocks any identity at all, so the warning can't be
      riding along on a `Vault.Unlock` call
- [x] The blocking check *blocks*: declining the prompt aborts the write
      entirely, no commit is produced, and the cache stays at its old
      value so the same warning reappears next time
- [x] Confirming a *routine* recipient change (files agree) regenerates
      both halves of the cache and stops warning — one acknowledgment per
      actual change, not per command
- [x] Confirming a *mismatched* change (`.age-recipients` and
      `config.toml` disagree) does not silently clear the cache —
      `verify` keeps failing until the inconsistency is actually fixed
- [x] `--yes` bypasses the blocking prompt in the routine case, and
      behaves per the decision above in the mismatched case
- [x] the recipient-change warning is a typed value returned by the
      library (diff content, routine-vs-mismatched flag), not printed
      text — `cmd/gage` renders it as the `[y/N]` prompt shown in the
      design doc
- [x] The cache is written under `$GAGE_STATE`, never inside the vault,
      and never committed — a `git status` after a cache regeneration is
      clean
- [x] `recipient add` and `recipient remove` against a vault whose
      `.age-recipients` and `config.toml` disagree are both refused,
      naming the specific differences, and leave both files
      byte-identical — the stray key is still there afterwards for
      `verify` to keep reporting, rather than having been rebuilt away
- [x] A refused recipient change commits nothing, pushes nothing, and
      leaves the trust cache at its old value, so a mismatch cannot be
      laundered into an approved state by retrying the add
- [x] The refusal is asserted with `--reencrypt` as well as without it:
      the rebuild that erases the evidence is in the shared tail both
      paths commit through, so covering only the plain add would leave
      the more destructive verb unguarded
- [x] `recipient verify --repair` rewrites `.age-recipients` from
      `config.toml`, commits it, and makes `verify` pass — and a
      `Prompter` that declines writes and commits nothing
- [x] `--repair` does *not* regenerate the trust cache: the next encrypt
      still shows the config diff and still asks, since repair settled
      the inconsistency and not the recipient change
- [x] A successful local `recipient add`/`remove` regenerates the cache,
      so the operator is not warned about their own change on the very
      next write
- [x] `vault remove` deletes `$GAGE_STATE/<vault>/`, so re-adding the
      vault is a first use rather than an inherited approval
- [x] `--yes` is refused in a session — on a session line and on the way
      into one — rather than silently doing nothing or silently covering
      the whole session (added during implementation; see the decision
      above)

## Implementation

- [x] Trust cache storage: `$GAGE_STATE/<vault>/known-config.toml` (a
      verbatim copy) plus the stored `.age-recipients` content hash,
      written through M0's atomic-write helper
- [x] Blocking pre-encrypt check on the `Vault` methods that encrypt
      (`insert`/`edit`/`generate`/`rename`, and M11's `mv`/`cp` against
      the destination vault)
- [x] Non-blocking opportunistic check wired into `Vault.Unlock` itself
      (covering session `use` and every one-shot command's implicit
      unlock, mirroring M8a's fetch+pull hook) *and* into `sync` directly,
      since a clean fast-forward sync can complete without ever calling
      `Unlock`
- [x] Diff generation against the cached `config.toml` copy
- [x] The two-outcome resolution logic: routine changes regenerate both
      cache halves; mismatched ones record that the mismatch was seen
      without clearing the inconsistency
- [x] `RecipientChangeWarning` (or similar) structured type returned by
      the library on a trust-cache mismatch; `cmd/gage` renders it as the
      terminal diff + `[y/N]` prompt
- [x] A `VerifyRecipients()` precondition on M9's `AddRecipient` and
      `RemoveRecipient`, refusing over a failing verify instead of
      rebuilding `.age-recipients` from `config.toml` — see the resolved
      decision above. It belongs inside `withVaultWrite`, under the vault
      lock and before `commitRecipientList` writes anything, so the
      refusal can't race a concurrent repair; and it must sit ahead of
      the re-encryption rather than beside the recipient-file write,
      since aborting after every entry has been rewritten would be its
      own half-migrated state
- [x] `Vault.RepairRecipients` + `gage recipient verify --repair`: the
      refusal's exit, shipping with it — rewrite `.age-recipients` from
      `config.toml` under the vault lock, behind a `Prompter.Confirm`
      naming every key dropped and added, committed as
      `gage: recipient repair`, and *without* regenerating the cache
- [x] `Prompter.ConfirmRecipientChange(RecipientChangeWarning)` added to
      the interface (alongside `Choose`, and for the same reason), with
      `cmd/gage`'s terminal implementation rendering the diff + `[y/N]`;
      the opportunistic, non-blocking checks go through the existing
      `Warn` with the warning's one-line summary, as M8a's sync
      advisories do
- [x] A persistent root `--yes` flag answering `ConfirmRecipientChange`
      (and nothing else) without asking, wrapped once in `NewRootCmd`'s
      `PersistentPreRunE` and unwrapped when that execution ends
- [x] Cache regeneration as the last step of a successful `AddRecipient`/
      `RemoveRecipient`, and `$GAGE_STATE/<vault>/` deleted by
      `vault remove`

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
