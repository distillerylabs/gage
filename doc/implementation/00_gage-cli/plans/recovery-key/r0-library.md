# R0 — Recovery key: library

[plan index](index.md) · next: [R1 — `init` integration](r1-init-integration.md)

> **Recommended model: Sonnet.** Small, additive and well specified; the tests are easy to state up front and the work follows existing patterns in `vault_create.go`.

## Goal

Everything `internal/gage` needs for a recovery recipient, with no CLI
change: generate the key, register it under a fixed label at vault
creation, refuse label/key collisions, and check a pasted secret against
a vault's recipients. The library only returns data; nothing here prints.

## Depends on

- Existing `Create`, `buildRecipients`, `recipients.Write`,
  `vaultconfig`, `agekey.ValidateRecipient`, `zero`.

## Design references

- [Design doc, "Local identity storage"](../../tdds/gage-cli-design.md) —
  "Losing this file is a recovery problem, not a backup problem".
- [index.md](index.md) decisions D6 and D7.

## Decisions to make first

None open; D6 (fixed label) is settled in the index.

## Tasks

- [ ] `NewRecoveryKey() (RecoveryKey, error)` in
      `internal/gage/recovery_key.go`: wraps
      `age.GenerateX25519Identity()`, returns `{Pubkey string, Secret []byte}`.
      `Secret` is a `[]byte` copy the caller can `zero()`; never touches
      disk.
- [ ] `RecoveryDeviceLabel = "recovery-paper-key"` constant.
- [ ] `CreateSpec.ExtraRecipients []LabelledRecipient{Device, Pubkey}`;
      `buildRecipients` emits device, then labelled extras, then
      `recipient-N` for `Recipients`. `Recipients []string` behavior is
      unchanged.
- [ ] `Create` rejects duplicate pubkeys and duplicate labels across the
      whole recipient list, and a `Device` equal to `RecoveryDeviceLabel`
      when a recovery recipient is present. Typed error, exit-code
      mapped (Usage/Conflict) with a test pinning the code.
- [ ] `(*Vault).VerifyRecoveryKey(secret []byte) error`: parses the
      secret, derives its public key, checks it against `Recipients()`.
      Needs no unlock and decrypts nothing. Typed errors for
      malformed-key vs not-a-recipient.

## Tests (write first)

- [ ] `NewRecoveryKey` returns a valid recipient
      (`agekey.ValidateRecipient`), and parsing `Secret` yields the same
      public key.
- [ ] Two calls return different keys.
- [ ] `Secret` is a `[]byte` whose zeroing does not affect `Pubkey`.
- [ ] `Create` with `ExtraRecipients` writes the labelled recipient to
      both `.gage/config.toml` and `.age-recipients`, in one initial
      commit.
- [ ] Order: device first, labelled extras next, `recipient-N` last.
- [ ] `Create` without `ExtraRecipients` behaves exactly as before
      (existing tests unchanged and green).
- [ ] Duplicate pubkey rejected; duplicate label rejected; device named
      `recovery-paper-key` alongside a recovery recipient rejected; each
      leaves no vault directory behind and maps to its exit code.
- [ ] An entry encrypted to the vault's recipients decrypts with the
      recovery secret alone.
- [ ] `VerifyRecoveryKey`: right key passes; a valid key that is not a
      recipient fails with the not-a-recipient error; garbage fails with
      the malformed error.

## Definition of done

Every item above green on Linux, macOS and Windows CI, `make lint` and
`make test` clean, no printing added to `internal/gage`.
