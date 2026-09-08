# E0 — Vault-id keying

[plan index](index.md) · next: [E1 — Unconditional re-encryption](e1-unconditional-reencrypt.md)

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

All settled; see the two register entries above. Nothing to resolve
before starting.

## Tests (write first)

**The id itself**

- [ ] `gage init` writes a `[vault].id` that parses as a UUIDv4, and two
      `init`s produce different ids.
- [ ] `format_version = 2` is written, and a vault at version 1 is
      refused by the existing version check with an upgrade/re-create
      message — **not** half-parsed.
- [ ] A `.gage/config.toml` whose `id` is absent, malformed, or not a
      UUID is refused on read, before any path is constructed from it.
- [ ] **The id is untrusted input**: an `id` containing a path traversal,
      a separator, or an absolute path is rejected on the way in *and*
      on the way out, exactly as `Q-DEVICE-NAME` requires for device
      names. Construct it by hand-editing the committed file.
- [ ] `gage clone` copies the id into global config's
      `[vaults.<name>]`, and `vault info` reports a vault whose id
      matches its own committed config.

**Paths**

- [ ] Identity files land at
      `$GAGE_DATA/identities/<vault-id>/<device>.age`, still `0700`
      directory and `0600` file.
- [ ] The trust cache lands at
      `$GAGE_STATE/<vault-id>/known-config.toml`.
- [ ] **Two vaults registered under the same local name in sequence do
      not share either directory** — the whole point. Construct it:
      `init` A, `vault remove`, `clone` B under the same name, and assert
      B sees neither A's identity nor A's trust cache.
- [ ] The identities directory carries a plaintext marker naming the
      vault, and nothing reads it to make a decision (delete it and
      assert every operation still behaves identically).

**`removeOrphanedIdentity` — the destructive path**

- [ ] `gage init` / `identity add` record this device's `pubkey` in
      global config's `[vaults.<name>]`.
- [ ] **The A20 regression, constructed end to end**: `init` A, `vault
      remove` A, `clone` B under A's old name, `vault remove` B — and
      assert **A's identity file still exists**. This is the sequence
      that currently destroys the only copy of a private key.
- [ ] **The relabel case** (`Q-ORPHAN-BY-NAME`'s false delete): the
      vault lists this device's key under a device name different from
      the one in global config. `vault remove` must **not** delete the
      key, because the *pubkey* matches. Name comparison would delete it.
- [ ] **The false-keep case**: a stale local key whose device name now
      labels a *different* public key is correctly identified as an
      orphan.
- [ ] Deletion never happens without `Prompter.Confirm`, and answering
      no keeps the file.
- [ ] A **non-interactive** `vault remove` never deletes an identity —
      `Confirm` defaults to no, so assert this holds without any special
      casing.
- [ ] When `pubkey` is absent from global config (older entry,
      hand-edited), gage says so and **keeps** the file rather than
      guessing.
- [ ] `vault remove` still leaves the vault's own files and repository
      untouched in every one of the above.
- [ ] **One vault registered twice under two local names shares one
      identities directory** — the intended consequence of id-keying, and
      the case name-keying used to hide. `vault remove` of either
      registration must not delete a key the other still needs. Both
      halves matter: the device that is still a recipient (the pubkey
      comparison keeps it) and the device that holds a key but is *not*
      yet a recipient, which is exactly the state `identity enroll`
      creates while a request is pending.

## Implementation

- [ ] `[vault].id` minted in `gage init`, written to
      `.gage/config.toml`, `format_version` bumped to 2. Reject v1 in
      the existing check rather than adding a bespoke missing-field
      error — the version marker exists for exactly this.
- [ ] Validate the id as a UUID wherever a device name is validated
      today; both are path components arriving from a committed file
      any git-writer can edit.
- [ ] `IdentityFilePath` and `TrustCacheDir` take the id rather than the
      name. Grep for every caller — the compiler will not catch a
      same-typed `string` swap, so this is the step to be deliberate
      about.
- [ ] `id` and `pubkey` added to `config.VaultEntry`; written by `init`,
      `clone`, and `identity add`.
- [ ] Plaintext marker written into the identities directory at
      creation.
- [ ] `removeOrphanedIdentity` compares `r.Pubkey` against the recorded
      `pubkey`, and gates deletion behind `app.Prompter.Confirm` with a
      message naming the path. `app.Prompter` is already on the struct;
      no plumbing needed.
- [ ] The version-refusal message becomes **directional**. Today it ends
      "upgrade gage" for any mismatch (`vaultconfig.go`), which is the
      wrong advice in the direction this milestone creates: a v1 vault
      read by a v2 build needs re-creating, not a newer binary. Older
      than this build → re-create, naming `make reset-local-state`; newer
      → upgrade gage, as now.

## Definition of done

Every test above green on all three platforms, `make lint` and
`make test` clean, and a vault created before this change fails with a
clear re-create message rather than misbehaving.

## Affects later milestones

- **E3** writes identity files at the new path and relies on the id
  being available from global config without reading the vault. It is
  also a **fourth writer of `pubkey`** into `[vaults.<name>]`, alongside
  `init`, `clone`, and `identity add` — an enrolled device that never
  records its key lands in this milestone's "pubkey absent, so keep the
  file and say why" branch, which would quietly disable this fix on
  precisely the devices enrollment creates.
- **E4** introduces `recipient approve --device`, which produces exactly
  the relabel case above. E0's `removeOrphanedIdentity` fix must be in
  before E4 ships, or approval creates a new route to an unrecoverable
  deletion.
