# M9 — Identity & recipient management

[← M8b](m8b-sync-conflicts.md) · [plan index](index.md) · next: [M10 — Local trust cache](m10-trust-cache.md)

> **Recommended model: Opus.** `--reencrypt` atomicity and the dirty-tree reset under lock. Crash-safety reasoning where the tests simulate the crash — so they only prove what you thought to simulate.

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
  Resolved: hold the write lock for the whole run, exactly as M8b
  resolved the same question for interactive conflict resolution. The
  concurrency model the lock exists for is same-user-multiple-terminals
  (see "Concurrent processes and the vault lock"), not strangers
  contending for a shared vault, so "my other pane waits until the
  re-encryption finishes" is the expected behavior rather than a
  surprising one. Staging it — re-encrypting in batches, taking the lock
  per batch — would buy nothing and cost the guarantee: another process's
  write landing between two batches would move HEAD underneath the
  in-flight pass, so the single commit at the end would either fail or
  quietly commit a tree built against a parent that no longer exists.
  The existing lock-wait mechanism already covers the contention message:
  a contended acquire waits `vaultLockTimeout` and then fails as
  `*vaultlock.ContendedError` wrapped at `exitcode.Conflict`, so a
  blocked write reports "another gage process holds this vault" rather
  than hanging or interleaving. No new mechanism.
- **`recipient add` without `--reencrypt` still commits** the two changed
  files. Resolved: yes — one commit containing `.age-recipients` and
  `.gage/config.toml` together, never one without the other. This is the
  pair M10's trust cache diffs, and a recipient change that sat
  uncommitted in the working tree would be silently discarded by the very
  dirty-tree reset this milestone adds. It also keeps `add` and
  `add --reencrypt` structurally identical: same files, same single
  commit, differing only in how many entries ride along in it.
  - **Commit messages** (unstated in the design, needed to assert "exactly
    one commit"): `gage: recipient add <device>`,
    `gage: recipient add <device> (reencrypt)`, and
    `gage: recipient remove <device> (reencrypt)`. Unlike an entry
    commit — whose message is the bare UUID because a title would leak
    plaintext (M4) — a recipient commit's own diff is plaintext by
    design, so naming the device in the message reveals nothing the
    commit does not already contain.
- **Interaction between `--reencrypt` and push.** Resolved: acceptable,
  and said out loud. The guarantee is about HEAD: every entry and both
  recipient-list files land in exactly one local commit or none at all.
  Push is the ordinary post-write push (M4's `pushAfterWrite`, M8a's
  warn-and-proceed posture), so an unreachable remote warns and leaves
  the vault correct locally, to publish on the next successful sync. The
  success message says the re-encryption is committed locally, so a
  failed push is visibly a publishing problem rather than a half-done
  migration.
- **Does `recipient remove` refuse to remove the last recipient**, or to
  remove the local device's own key (locking yourself out)? Resolved,
  differently for the two cases, because they differ in recoverability:
  - **The last recipient: refused outright**, always, with no `--force`.
    A vault with an empty recipient list cannot be encrypted to at all —
    `encryptRecipients` would return nothing and the next write would
    fail — and `--reencrypt` would have already rewritten every entry to
    a list nobody holds a key for, destroying the vault's contents
    irrecoverably. There is no state in which this is what someone meant.
  - **The local device's own key: allowed, behind an explicit
    `Prompter.Confirm`** naming the consequence ("this device will no
    longer be able to read this vault"). It is a legitimate operation —
    decommissioning this laptop from a vault other devices still use —
    and it works, because `--reencrypt` decrypts every entry with the
    still-valid local identity *before* re-encrypting to the reduced
    list. A `Prompter` that answers no (which includes every
    non-interactive `Prompter`, since `Confirm` defaults to no) aborts
    with nothing written and nothing committed.

## Tests (write first)

**Identity**

- [x] `gage identity add` registers a device and prints a public key
- [x] Each device registered via `gage identity add` gets its own wrapped
      identity file at `$GAGE_DATA/identities/<vault>/<device>.age`;
      deleting one device's file doesn't affect another device's ability
      to decrypt
- [x] `gage identity add` with a device name already registered as a
      recipient of that vault is rejected rather than silently
      overwriting an existing identity file — including the common case
      of two machines sharing a hostname, where the default name
      collides without anyone typing it
- [x] `gage identity add --device NAME` overrides the hostname default;
      omitted, the normalized hostname is used
- [x] `gage identity add` with no `--method` takes the vault's
      `[method].default`; `--method passphrase` is equivalent, and any
      other value fails on the same allowlist as `init` — the flag sets
      *this device's* method, which is per-device by design and recorded
      locally rather than in the vault (Q-METHOD-SCOPE)
- [x] `gage identity list` reports the local device's registered
      identities for a vault
- [x] Recovering from a lost identity file needs no file restore: a fresh
      `gage identity add` on the affected device plus `gage recipient add`
      from any surviving recipient restores access under a new identity

**Recipients**

- [x] `gage recipient add` (no `--reencrypt`) affects only future writes —
      entries that existed before the add remain undecryptable by the new
      recipient's key
- [x] `gage recipient add` without `--reencrypt` still commits the updated
      `.age-recipients` and `config.toml` together
- [x] `gage recipient add --reencrypt` makes all pre-existing entries
      decryptable by the new recipient
- [x] `gage recipient remove` without `--reencrypt` is rejected outright;
      with `--reencrypt` it re-encrypts every entry, excluding the
      removed key
- [x] `gage recipient remove --reencrypt` prints the "revokes future
      access only — anything already read can't be unread" warning
- [x] `gage recipient list` prints every recipient's device name and
      public key, matching `.gage/config.toml`'s `[[recipients]]` exactly,
      and reflects an add/remove from the same test run

**`--reencrypt` atomicity**

- [x] A simulated crash/interruption partway through `--reencrypt` leaves
      HEAD byte-identical to before the command ran — no partial commit,
      no leftover dirty `entries/` once `gage` next starts
- [x] Retrying `--reencrypt` after such an interruption produces the same
      correct end state as an uninterrupted run
- [x] A single entry failing during `--reencrypt` (simulated decrypt/
      encrypt error) aborts the whole operation before any commit and
      reports which entry failed; no entries are left partially migrated
- [x] `--reencrypt` lands the recipient-list files
      (`.age-recipients`/`config.toml`) and every re-encrypted entry in
      exactly one commit — never a commit containing only one side of
      that pair
- [x] `--reencrypt` holds the vault lock for its whole duration; a
      concurrent write observes contention rather than interleaving

**Dirty working tree**

- [x] A dirty `entries/` working tree left by a simulated crash is reset
      to HEAD before any subsequent write (`insert`/`edit`/`generate`/
      `--reencrypt`) proceeds, rather than being folded into that write's
      commit
- [x] Resetting a dirty `entries/` working tree emits a one-line warning
      naming the discarded paths (via `Prompter`, a fake in tests) before
      proceeding — the reset is automatic, but never silent, since the
      "only an interrupted `--reencrypt` could cause this" assumption
      isn't provable, only likely
- [x] A crash in the window *after* the recipient files are written but
      before the commit is reset too, in both shapes it occurs: an
      interrupted `--reencrypt` (entries dirty as well) and an
      interrupted plain `recipient add` (the pair the only dirty thing in
      the tree). Added during review — see "The reset's coverage beyond
      `entries/` needed its own test" below

**`verify`**

- [x] `gage recipient verify` exits 0 and reports "in sync" when
      `.age-recipients` and `config.toml` agree; exits 1 and lists the
      specific differences when they don't
- [x] `gage recipient verify` succeeds against a vault where the local
      device has no identity file at all (e.g. right after `clone`, before
      `gage identity add`) — no `Vault.Unlock`, no `Prompter` interaction,
      proving the "needs no unlock, safe to run in CI" claim rather than
      just asserting it

## Implementation

The library surface the tests are written against — decided here so the
tests and the implementation agree rather than being reconciled after the
fact:

```go
// internal/gage — identity (device-side)
func (v *Vault) AddIdentity(device string, p Prompter) (pubkey string, err error)
func ListIdentities(vault string) ([]LocalIdentity, error)
type LocalIdentity struct{ Device, Path string; Current bool }
var ErrDeviceNameTaken error // device already a recipient of this vault

// internal/gage — recipients (vault-side)
func (v *Vault) Recipients() ([]VaultRecipient, error)          // no unlock
func (v *Vault) VerifyRecipients() (RecipientVerification, error) // no unlock, no Prompter
func (v *Vault) AddRecipient(device, pubkey string, reencrypt bool, ident *Identity) (RecipientChange, error)
func (v *Vault) RemoveRecipient(query string, reencrypt bool, ident *Identity) (RecipientChange, error)
type VaultRecipient struct{ Device, Pubkey string }
type RecipientVerification struct {
    InSync               bool
    OnlyInRecipientsFile []string // in .age-recipients, missing from config.toml
    OnlyInConfig         []string // in config.toml, missing from .age-recipients
}
type RecipientChange struct{ Device, Pubkey, Commit string; Reencrypted int }
var ErrLastRecipient, ErrRecipientExists, ErrRecipientNotFound, ErrReencryptRequired error
```

- [x] `gage identity add/list` (reuses M2's passphrase identity-generation
      path for additional devices — each gets its own
      `$GAGE_DATA/identities/<vault>/<device>.age`). `Vault.AddIdentity`
      owns the vault-side collision check (the name must not already be a
      `[[recipients]]` label) and calls `CreateIdentity`; `cmd/gage`
      records `device`/`method` in global config, the same split `init`
      already uses. `--method` defaults to the vault's `[method].default`
      and is validated against `AllowedMethods()`
- [x] `gage recipient add/remove [--reencrypt]`: stage every re-encrypted
      entry in the working tree first; commit only after all entries
      succeed, with the recipient-list files and every touched entry in
      that same single commit. Add without `--reencrypt` still commits the
      recipient-list pair. **Order matters for crash-safety**: entries are
      written first, against the new recipient list passed explicitly
      (a `writeEntryTo(id, e, []Recipient)` variant of `WriteEntry`, since
      `encryptRecipients()` reads the on-disk file), and
      `.age-recipients`/`.gage/config.toml` are written last, immediately
      before the single `CommitAll`. That keeps the crash window's dirty
      surface to `entries/`, which is what the dirty-tree reset expects
- [x] `gage recipient list`
- [x] Precondition check on every write (`insert`/`edit`/`generate`/
      `rename`/`--reencrypt`): if the working tree is unexpectedly dirty at
      start, warn once via `Prompter` (naming the discarded paths) and
      reset it to HEAD before proceeding — the only *expected* source of
      unexpected dirtiness is an interrupted `--reencrypt`, but the
      warning fires regardless of cause, the same "warn and proceed"
      posture as an unreachable network in M8a. **This runs under the vault
      lock**, so it can never race another process's in-flight write.
      The reset covers the whole vault working tree rather than only
      `entries/` — a crash in the narrow window after the recipient files
      are written but before the commit dirties those two as well, and a
      reset that skipped them would leave exactly the half-migrated state
      this milestone exists to rule out. Untracked files under `entries/`
      (an interrupted `insert`) are discarded too, so verify go-git's
      `HardReset` actually removes them and remove them explicitly if not
- [x] `gage recipient verify`: reads `.age-recipients` and
      `.gage/config.toml` directly — both plaintext — and never calls
      `Vault.Unlock`/`Prompter`, so it needs no identity to run
- [x] Register `identity add/list` and `recipient add/remove/list/verify`
      in `cmd/gage`'s command registry (`GroupIdentity`/`GroupRecipient`,
      `AvailBoth`) — M0's registry-completeness test fails otherwise

## Settled during implementation

- [x] **`withVaultWrite` is where the dirty-tree reset lives.** Every
      mutating `Vault` method now opens with it instead of
      `withWriteLock`: take the lock, discard an unexpectedly dirty tree
      (warning through the `Prompter` first), then run the write. Putting
      it in one preamble rather than in each verb makes "no write folds a
      previous write's leftovers into its own commit" structural, the same
      way `withWriteLock` already makes "a write holds the lock across its
      whole sequence" structural. `generate` gets it for free by going
      through `Insert`. The sync paths deliberately keep the bare
      `withWriteLock` — a fast-forward or a merge *refuses* over a dirty
      tree rather than discarding it, because there the uncommitted work
      could be something outside gage is mid-edit; here the caller is gage
      itself, about to commit, and a leftover can only have come from a
      gage write that died
- [x] **`gitrepo.ResetHard` discards untracked files explicitly.** The
      pinned go-git clears them during a hard reset, so the loop that
      removes them is redundant *today* — but `git reset --hard` itself
      leaves untracked files alone, so that is a side effect of how go-git
      rebuilds the worktree rather than a property to depend on. The shape
      that matters is an interrupted `insert`, which leaves an
      `entries/<uuid>.age` that was never committed.
      `TestResetHardDiscardsModifiedAndUntrackedFiles` asserts the outcome
      either way, so a go-git upgrade that changes its mind is a red test
      rather than a vault quietly folding an orphan into the next commit
- [x] **`ErrReencryptRequired` is checked twice, on purpose.**
      `RemoveRecipient` rejects it before taking the lock, and the CLI
      rejects it before `withUnlockedVault`. Only the library's is the
      contract; the CLI's exists so the refusal arrives *before* the
      passphrase prompt, since being asked to unlock a vault and only then
      told the command line was wrong is the wrong order to fail in
- [x] **`Identity.frontend()` names the Prompter for asking rather than
      telling.** `warnTo` already returned it, but M9 is the first
      milestone where the library asks a *question* whose "no" is
      load-bearing, and routing a `Confirm` through something called
      `warnTo` read as a mistake. Same value, honest name — the design
      rule that human interaction rides the `Identity` rather than growing
      a `Prompter` parameter on every mutating method is unchanged
- [x] **The last-recipient refusal is checked before the lock-yourself-out
      confirmation.** Removing the only recipient is refused outright, so
      it must never reach a question a "yes" could answer — otherwise
      confirming would look like it should work
- [x] **The reset's coverage beyond `entries/` needed its own test.**
      `onReencryptEntry` fires only while entries are being written, so
      every atomicity test built on it crashes with `.age-recipients` and
      `config.toml` still clean — the one window this bullet's own
      implementation note calls out was the one the seam structurally
      could not reach. Narrowing `gitrepo.ResetHard` to `entries/` left
      the entire suite green, so the coverage was asserted in a comment
      and nowhere else. `TestInterruptedRecipientWriteLeavesNoHalfMigratedState`
      closes it by reproducing the window through `writeRecipientFiles`
      itself — the genuine last act before either verb's single commit —
      rather than by hand-writing two lookalike files. The plain-`add`
      shape is the one that bites: it touches no entry, so a reset keyed
      off `entries/` finds nothing to do, warns about nothing, and lets
      the next write commit the abandoned recipient list, leaving a vault
      whose recipient files name a key no entry is encrypted to. The test
      asserts the new entry is *unreadable* by the never-added device,
      which pins the reset ahead of the encrypt rather than merely ahead
      of the commit. `TestResetHardDiscardsChangesOutsideEntries` pins
      the same property at the `gitrepo` level
- [x] **cmd/gage's test `fakePrompter` now echoes warnings to stderr.**
      The real `terminalPrompter` writes them there (prompts and warnings
      go to stderr so a redirected stdout carries only the secret), and a
      fake that only recorded them made every library warning invisible to
      any CLI test that looks at stderr — including this milestone's
      "revokes future access only", which a command could have stopped
      emitting with nothing going red

## Definition of done

Full test list green on all three CI platforms. Two simulated devices can
be added to a vault, one can be removed with full re-encryption, and an
interrupted re-encryption is provably invisible in committed history.

## Affects later milestones

- **M10 diffs exactly the pair this milestone keeps in sync.** The
  trust cache's whole premise is that a legitimate `recipient add` touches
  `.age-recipients` and `config.toml` together — the mismatch case is the
  tampering signature.
  - **M10 revises `AddRecipient`/`RemoveRecipient` as shipped here.**
    Both rebuild `.age-recipients` from `config.toml`'s list, so running
    either against an already-diverged vault quietly overwrites the stray
    key and commits a clean pair. M10 refuses over a failing
    `VerifyRecipients()` instead: erasing the divergence destroys the
    evidence its trust cache exists to surface, and a green `verify`
    obtained that way launders an unreviewed key into an approved one
    through a command the operator thought was about adding a device.
    The check lands ahead of the re-encryption rather than beside the
    recipient-file write, so the refusal cannot itself leave the
    half-migrated state this milestone rules out. See M10's "Decisions to
    make first" — including the open question of how a diverged vault is
    repaired, since the refusal needs an exit to ship with.
- The dirty-tree reset established here runs under the vault lock; if
  that ordering is ever loosened, M4's concurrency guarantee breaks.
