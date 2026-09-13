# E0 — Vault-id keying

[plan index](index.md) · next: [E1a — Unconditional re-encryption](e1a-unconditional-reencrypt.md)

> **Recommended model: Opus.** An on-disk contract change plus a fix to
> code that deletes private keys. Being subtly wrong here destroys key
> material, which is the failure mode this project treats as
> unrecoverable. **Worth an Opus review pass over the tests**, since the
> dangerous cases are ones a passing suite is least likely to have
> thought to construct.

## Goal

Apply [A20](../gage-cli-design/open-questions.md#a20): stop keying
per-vault local state by the *local registration name*, which is chosen
at clone time and tied to the remote by nothing, and key it by a vault
id that travels inside the vault instead.

Two halves, one milestone because they share a config-schema change:

1. **Keying** — `[vault].id`, `format_version = 2`, the identities
   directory, the trust cache directory, and the id in global config.
2. **`removeOrphanedIdentity`** — compare public keys instead of device
   names, and confirm before deleting.

**Not enrollment.** This is an amendment to M1/M2 that enrollment
depends on. It is here because nothing else sequences it.

## Depends on

Nothing new. Amends **M1** (config schema, global config) and **M2**
(identity file paths); touches **M10**'s trust-cache path.

## Design references

- [Q-IDENTITY-VAULT-NAME](../gage-cli-design/open-questions.md#q-identity-vault-name)
  — the two harms, and why an assigned id beats a derived one
- [Q-ORPHAN-BY-NAME](../gage-cli-design/open-questions.md#q-orphan-by-name)
  — why the name comparison is wrong in both directions
- ["Local identity storage"](../../tdds/gage-cli-design.md) — the path
  scheme and the untrusted-input rule the id now joins

## Decisions

The keying itself and the pubkey comparison are settled in the two
register entries above. Five further decisions were settled during
review of this plan: three are scope this milestone turned out to
include and never named, one is a correction, and one is a question an
earlier draft left open inside its own implementation checklist while
this section said "all settled" — which is the thing the project's
milestone convention exists to prevent, so it is resolved here rather
than at the keyboard.

### `Vault` carries the id, and it comes from global config

`IdentityFilePath` and `TrustCacheDir` stop taking a name and start
taking an id, but `loadTrustCache` and `storeTrustCache` are methods on
`*Vault` — and `Vault` is built across `cmd/gage` as
`&gage.Vault{Name: …, Path: …}` with nothing else in it. So the id has
to reach the struct, and where it comes from is a decision rather than a
detail:

- **`Vault` gains an `ID` field**, and every construction site sets it.
  This is the step an earlier draft left implicit, and it is the one with
  a silent failure mode: a `Vault{}` whose `ID` was simply not set yields
  `TrustCacheDir("")` rather than a compile error. `checkPathComponent`
  rejects the empty string, so it fails loudly at runtime — but only on
  the paths a test actually exercises.
- **The value comes from global config's `[vaults.<name>].id`**, not
  from reading the vault's own `.gage/config.toml`. That is exactly why
  A20 puts the id in global config: `vault remove` needs to know which
  identities directory a vault owns *after* its files may be gone or
  moved, which is the situation the destructive branch arises in.

### The two recorded ids are compared, and a mismatch is refused

The id is now written in two places — the vault's committed
`.gage/config.toml` and global config's `[vaults.<name>]`. Nothing in an
earlier draft compared them, which reintroduces a thin version of the
bug this milestone exists to kill: identity lookup would use one id and
the recipient list would come from the vault the *other* id belongs to.

They disagree in ordinary ways, not exotic ones — a vault re-created at
the same path, a registration repointed by hand, a restored backup. So
wherever a vault is resolved and its own config is readable, the two are
compared, and a mismatch is refused with a message saying the
registration points at a different vault than the one on disk and to
re-register it. Where the vault's files are unreadable — the case
`vault remove` is built for — global config's copy stands alone, which
is what it is for.

### The lock file is keyed by the id too

`LockFilePath` builds `$GAGE_STATE/locks/<vault>.lock` from the local
name (`internal/gage/paths.go`), and an earlier draft left it there while
this milestone's own test list makes "one vault registered twice under
two local names" a **supported** state. Those two facts do not compose:
two registrations of one repository get two different lock files, and the
per-vault advisory lock stops being mutual exclusion at exactly the
moment this milestone blesses the configuration that needs it. Two
`gage insert` runs, one per registration, would both take "their" lock
and both write the same working tree.

The lock protects a *repository*, so it is keyed by the thing that
identifies a repository. `LockFilePath` takes the id, like the other two.
Existing lock files under the old names are orphaned and harmless, and
the migration is "re-create the vault" regardless.

This is the last piece of per-vault local state keyed by the local name,
which is worth saying plainly: after this milestone, the local name is a
label for humans and nothing in `$GAGE_DATA` or `$GAGE_STATE` is
addressed by it.

### The marker is removed with the last identity, and the prune keeps working

A20 settles that the identities directory carries a plaintext marker
naming the vault, so an opaque UUID directory does not break the "back up
your own wrapped identity file" story. What it does not settle is what
that marker does to `RemoveIdentity`'s trailing
`os.Remove(filepath.Dir(path))`, which succeeds only on an empty
directory — "exactly the wanted semantics," per its own comment. A marker
makes the directory never empty, so that call silently becomes a no-op
and every removed vault leaves a directory behind holding one marker.

**Resolved: `RemoveIdentity` removes the marker when it removes the last
identity, and the prune keeps working.** The alternative — drop the prune
and declare the leftover deliberate — trades a real behaviour for a
comment, and the leftover is invisible, which is the property that makes
it the wrong default. The existing comment stays true rather than being
rewritten to describe something that no longer happens.

### `clone` does not write `pubkey`

An earlier draft of this milestone and of the TDD both listed `clone`
among the writers of `[vaults.<name>].pubkey`. It cannot be one: a clone
holds no identity, and it deliberately refuses to unlock in order to
derive a public key — that refusal is the entire subject of the TDD's
"What 'not a recipient yet' actually means". The writers are `init`,
`identity add`, and E3's `enroll`. A clone that accepts E3's enroll offer
records the field as *enroll*, at the point a key exists.

## Tests (write first)

**The id itself**

- [x] `gage init` writes a `[vault].id` that parses as a UUIDv4, and two
      `init`s produce different ids.
- [x] `format_version = 2` is written, and a vault at version 1 is
      refused by the existing version check with an upgrade/re-create
      message — **not** half-parsed.
- [x] A `.gage/config.toml` whose `id` is absent, malformed, or not a
      UUID is refused on read, before any path is constructed from it.
- [x] **The id is untrusted input**: an `id` containing a path traversal,
      a separator, or an absolute path is rejected on the way in *and*
      on the way out, exactly as `Q-DEVICE-NAME` requires for device
      names. Construct it by hand-editing the committed file.
- [x] `gage clone` copies the id into global config's
      `[vaults.<name>]`, and `vault info` reports a vault whose id
      matches its own committed config.
- [x] **Global config's id and the vault's committed id are compared, and
      a mismatch is refused** with a message naming re-registration —
      not silently preferred one way or the other. Construct it by
      hand-editing global config's `id` to another UUID and asserting an
      ordinary read command refuses rather than reading the vault under
      the wrong identities directory.
- [x] **An unreadable vault falls back to global config's id alone**, so
      `vault remove` still knows which identities directory the vault
      owned after its files are gone. This is the case the field exists
      for and the one the check above must not break.

**Paths**

- [x] Identity files land at
      `$GAGE_DATA/identities/<vault-id>/<device>.age`, still `0700`
      directory and `0600` file.
- [x] **The id `init` files the identity under is the id it commits.**
      Assert the directory name equals `[vault].id` in the created
      vault's own `.gage/config.toml` — the one assertion that catches
      `init` minting a second id after `CreateIdentity` has already
      written the file, which leaves a working vault and an unreachable
      key.
- [x] **A failed `init` rolls back the identity it generated.** Make
      `Create` fail after `CreateIdentity` has succeeded (an existing
      non-empty target directory does it) and assert nothing is left
      under `$GAGE_DATA/identities/`. This pins the rollback against the
      id path; against the name it silently does nothing.
- [x] **A retried `init` generates a fresh key rather than reusing one**,
      and leaves no directory behind from the failed attempt. This is a
      real behavior change and the test should say so: `init` mints a new
      id every run, so `CreateIdentity`'s reuse path — which under
      name-keying made a retried `init` pick its previous key back up —
      can no longer be reached from `init` at all. The `identityExisted`
      flag at that call site is consequently always false for `init`.
      Reuse itself is untouched and still reachable from `identity add`
      and `identity enroll`, which operate on a vault whose id already
      exists.

      **The knock-on is that rollback becomes load-bearing.** It used to
      be a tidiness measure with reuse as the backstop; it is now the only
      thing standing between repeated failed `init`s and an orphaned
      identity directory per attempt, each holding a private key for a
      vault that was never created. Worth one test of its own — two failed
      `init`s in a row leave `$GAGE_DATA/identities/` empty — rather than
      resting on the single-failure case above.
- [x] The trust cache lands at
      `$GAGE_STATE/<vault-id>/known-config.toml`.
- [x] **The vault lock lands at `$GAGE_STATE/locks/<vault-id>.lock`**,
      and — the point of it — **two registrations of one repository share
      one lock**. Construct it: register the same path under two local
      names, hold the lock through one of them, and assert a write
      through the other contends rather than proceeding. Against
      name-keying this test passes vacuously in the wrong direction:
      both writes succeed, concurrently, on one working tree.
- [x] **A `Vault` built without an `ID` fails loudly**, on every path
      that derives one — identity file, trust cache, lock. The empty
      string is already refused by `checkPathComponent`; assert it rather
      than trusting it, because the failure this rules out is a
      construction site somewhere in `cmd/gage` that was missed and only
      shows up on a path no test walks.
- [x] **Two vaults registered under the same local name in sequence do
      not share either directory** — the whole point. Construct it:
      `init` A, `vault remove`, `clone` B under the same name, and assert
      B sees neither A's identity nor A's trust cache.
- [x] The identities directory carries a plaintext marker naming the
      vault, and nothing reads it to make a decision (delete it and
      assert every operation still behaves identically).
- [x] **Removing the last identity for a vault leaves no orphaned
      directory**: `RemoveIdentity` removes the marker alongside the last
      identity and the existing prune still succeeds. Assert the end
      state — no directory under `$GAGE_DATA/identities/` — rather than
      the mechanism. Without this the marker quietly turns the prune into
      a no-op and nothing fails.
- [x] **Removing a non-last identity leaves the marker in place**, so the
      directory that still holds keys stays self-describing. The pair is
      what makes the removal deliberate rather than incidental.

**`removeOrphanedIdentity` — the destructive path**

- [x] `gage init` / `identity add` record this device's `pubkey` in
      global config's `[vaults.<name>]`, and **`clone` does not** — it
      holds no identity to derive one from. Assert the absence, since the
      "pubkey missing, so keep the file and say why" branch below is the
      one a freshly cloned vault legitimately lands in.
- [x] **The A20 regression, constructed end to end**: `init` A, `vault
      remove` A, `clone` B under A's old name, `vault remove` B — and
      assert **A's identity file still exists**. This is the sequence
      that currently destroys the only copy of a private key.
- [x] **The relabel case** (`Q-ORPHAN-BY-NAME`'s false delete): the
      vault lists this device's key under a device name different from
      the one in global config. `vault remove` must **not** delete the
      key, because the *pubkey* matches. Name comparison would delete it.
- [x] **The false-keep case**: a stale local key whose device name now
      labels a *different* public key is correctly identified as an
      orphan.
- [x] Deletion never happens without `Prompter.Confirm`, and answering
      no keeps the file.
- [x] A **non-interactive** `vault remove` never deletes an identity —
      `Confirm` defaults to no, so assert this holds without any special
      casing.
- [x] When `pubkey` is absent from global config (older entry,
      hand-edited), gage says so and **keeps** the file rather than
      guessing.
- [x] **The pending-enrollment state**, which E3 newly makes common: this
      device has a recorded `pubkey` that is *not* in the recipient list,
      because its request is still waiting for approval. That is
      indistinguishable from a genuine orphan by any check available
      here, so `vault remove` will offer to delete it — which is correct
      as far as it goes, but the confirmation is the only thing standing
      between the user and a key whose approval is in flight. Assert the
      `Confirm` fires and names the path, and that answering no keeps the
      file. This is a distinct case from the two-registrations bullet
      below, which reaches the same state by a different route; both
      matter, and only one of them existed before enrollment.
- [x] `vault remove` still leaves the vault's own files and repository
      untouched in every one of the above.
- [x] **One vault registered twice under two local names shares one
      identities directory** — the intended consequence of id-keying, and
      the case name-keying used to hide. `vault remove` of either
      registration must not delete a key the other still needs. Both
      halves matter: the device that is still a recipient (the pubkey
      comparison keeps it) and the device that holds a key but is *not*
      yet a recipient, which is exactly the state `identity enroll`
      creates while a request is pending.

## Implementation

- [x] `[vault].id` minted in `gage init`, written to
      `.gage/config.toml`, `format_version` bumped to 2. Reject v1 in
      the existing check rather than adding a bespoke missing-field
      error — the version marker exists for exactly this.
- [x] **`init` mints the id before it creates the identity, not after.**
      This is the reordering the milestone turns on, and it is not
      visible from the config schema. Today `cmd/gage/init.go` calls
      `CreateIdentity(opt.name, device, …)` and only then
      `gage.Create(spec)` — so the identity file is written before the
      vault that would carry the id exists. Three edits, and they have to
      land together:
      - Mint the UUID in `runInit`, ahead of the `CreateIdentity` call.
      - Add `ID` to `CreateSpec` so `Create` records the id it was handed
        rather than minting a second one. Two ids for one vault is the
        failure this ordering exists to prevent, and the vault would
        still look fine — the identity would simply be filed under an id
        nothing ever looks up again.
      - Pass the id to the failure-path `RemoveIdentity` call as well.
        It currently rolls back by name; against the new path that is a
        no-op that silently leaves the generated key behind, which is
        the one case `init`'s rollback comment says it deliberately
        cleans up.
- [x] `clone` takes the id from the config it just fetched, which is
      already read to register the vault — no ordering problem there, and
      worth confirming rather than assuming while `init`'s is being
      fixed.
- [x] Validate the id as a UUID wherever a device name is validated
      today; both are path components arriving from a committed file
      any git-writer can edit.
- [x] `IdentityFilePath`, `TrustCacheDir` **and `LockFilePath`** take the
      id rather than the name. Grep for every caller — the compiler will
      not catch a same-typed `string` swap, so this is the step to be
      deliberate about. `LockFilePath` is the one an earlier draft
      omitted; see "The lock file is keyed by the id too".
- [x] **`Vault` gains an `ID` field**, set at every construction site in
      `cmd/gage` from global config's `[vaults.<name>].id`. This is a
      mechanical change across roughly a dozen `&gage.Vault{…}` literals,
      and it is the one place in this milestone where the compiler *does*
      help: add the field, then let the build find the sites that need
      it rather than grepping for them.
- [x] **Compare the two recorded ids wherever a vault is resolved and its
      own config is readable**, refusing a mismatch with a re-register
      message. Where the vault's files cannot be read, global config's
      copy stands alone — do not turn `vault remove`'s whole reason for
      recording the id into a failure.
- [x] `id` added to `config.VaultEntry`, written by `init` and `clone`.
      `pubkey` added alongside it, written by `init` and `identity add`
      — **not by `clone`**, which holds no identity and derives no public
      key; see "`clone` does not write `pubkey`". E3's `enroll` is its
      third writer.
- [x] Plaintext marker written into the identities directory at
      creation.
- [x] **`RemoveIdentity` removes the marker with the last identity**, so
      its trailing `os.Remove(filepath.Dir(path))` — which succeeds only
      on an empty directory, "which is exactly the wanted semantics" per
      its own comment — keeps working. Settled in this milestone's
      Decisions rather than at the keyboard: a marker file otherwise
      makes the directory never empty, that call silently becomes a
      no-op, and every vault ever removed leaves a directory behind
      holding one marker. Nothing breaks, which is precisely the problem.
      The existing comment stays accurate; do not leave it describing
      behaviour that no longer happens.
- [x] `removeOrphanedIdentity` compares `r.Pubkey` against the recorded
      `pubkey`, and gates deletion behind `app.Prompter.Confirm` with a
      message naming the path. `app.Prompter` is already on the struct;
      no plumbing needed.
- [x] The version-refusal message becomes **directional**. Today it ends
      "upgrade gage" for any mismatch (`vaultconfig.go`), which is the
      wrong advice in the direction this milestone creates: a v1 vault
      read by a v2 build needs re-creating, not a newer binary. Older
      than this build → re-create, naming `make reset-local-state`; newer
      → upgrade gage, as now.

      **`vaultconfig`'s existing tests assert the current string.**
      `TestReadRejectsUnrecognizedFormatVersion` checks the message
      contains `"upgrade gage"` — correct for the newer-than-this-build
      direction it constructs (`format_version = 99`) and still passing
      afterwards, but it now needs a sibling for the older direction
      rather than being left as the only case covered. Update in place
      rather than deleting, the same way E1a handles M9's bullets.

## Definition of done

Every test above green on all three platforms, `make lint` and
`make test` clean, and a vault created before this change fails with a
clear re-create message rather than misbehaving.

## Affects later milestones

- **E2** now depends on this milestone, which it did not in an earlier
  draft. The sealed enrollment payload carries `vault_id` and
  `OpenEnrollment` refuses a request sealed for a different vault, so
  the primitive needs `[vault].id` and `Vault.ID` to exist. See the
  TDD's "The request names the vault it is for".
- **E3** writes identity files at the new path and relies on the id
  being available from global config without reading the vault. It is
  also the **third writer of `pubkey`** into `[vaults.<name>]`, alongside
  `init` and `identity add` — an enrolled device that never records its
  key lands in this milestone's "pubkey absent, so keep the file and say
  why" branch, which would quietly disable this fix on precisely the
  devices enrollment creates.
- **E4** introduces `recipient approve --device`, which produces exactly
  the relabel case above. E0's `removeOrphanedIdentity` fix must be in
  before E4 ships, or approval creates a new route to an unrecoverable
  deletion.
