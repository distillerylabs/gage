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

None open; D6 (fixed label) is settled in the index. Two details were
settled while implementing, and are recorded here so they are not
re-derived:

- **Duplicate checking is scoped to `ExtraRecipients`.** The plan first
  said `Create` should reject duplicate pubkeys and labels across the
  whole list. That would have broken the existing, deliberately pinned
  `TestCreateRepeatedRecipientsAllWritten` (a repeated `--recipient` is
  legal), so the check runs only when extras are present, and only
  collisions that involve an extra are errors. `--recipient` behavior is
  unchanged.
- **Error taxonomy reuses existing sentinels.** Key or label collisions
  are `ErrRecipientExists` at `Conflict`, matching `AddRecipient`. A
  recovery secret that is not a recipient is `ErrNotARecipient` at
  `LockedOrAuth`, matching decryption. A malformed secret is the new
  `ErrMalformedRecoveryKey` at `Usage`; its text never contains the input.

## Tasks

- [x] `NewRecoveryKey() (RecoveryKey, error)` in
      `internal/gage/recovery_key.go`: wraps
      `age.GenerateX25519Identity()`, returns `{Pubkey string, Secret []byte}`.
      `Secret` is a `[]byte` copy the caller can `zero()`; never touches
      disk.
- [x] `RecoveryDeviceLabel = "recovery-paper-key"` constant.
- [x] `CreateSpec.ExtraRecipients []LabelledRecipient{Device, Pubkey}`;
      `buildRecipients` emits device, then labelled extras, then
      `recipient-N` for `Recipients`. `Recipients []string` behavior is
      unchanged.
- [x] `Create` rejects duplicate pubkeys and duplicate labels across the
      whole recipient list, and a `Device` equal to `RecoveryDeviceLabel`
      when a recovery recipient is present. Typed error, exit-code
      mapped (Usage/Conflict) with a test pinning the code.
- [x] `(*Vault).VerifyRecoveryKey(secret []byte) error`: parses the
      secret, derives its public key, checks it against `Recipients()`.
      Needs no unlock and decrypts nothing. Typed errors for
      malformed-key vs not-a-recipient.

## Tests (write first)

- [x] `NewRecoveryKey` returns a valid recipient
      (`agekey.ValidateRecipient`), and parsing `Secret` yields the same
      public key.
- [x] Two calls return different keys.
- [x] `Secret` is a `[]byte` whose zeroing does not affect `Pubkey`.
- [x] `Create` with `ExtraRecipients` writes the labelled recipient to
      both `.gage/config.toml` and `.age-recipients`, in one initial
      commit.
- [x] Order: device first, labelled extras next, `recipient-N` last.
- [x] `Create` without `ExtraRecipients` behaves exactly as before
      (existing tests unchanged and green).
- [x] Duplicate pubkey rejected; duplicate label rejected; device named
      `recovery-paper-key` alongside a recovery recipient rejected; each
      leaves no vault directory behind and maps to its exit code.
- [x] An entry encrypted to the vault's recipients decrypts with the
      recovery secret alone.
- [x] `VerifyRecoveryKey`: right key passes; a valid key that is not a
      recipient fails with the not-a-recipient error; garbage fails with
      the malformed error.

## Definition of done

Every item above green on Linux, macOS and Windows CI, `make lint` and
`make test` clean, no printing added to `internal/gage`.
