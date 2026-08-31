# M3 — Entry format

[← M2](m2-identity-and-crypto.md) · [plan index](index.md) · next: [M4 — CRUD (one-shot)](m4-crud.md)

## Goal

The entry layer, at the library level, with no CLI verb wired up yet:
generate a UUID, YAML-encode an entry, encrypt it to `entries/<uuid>.age`
via M2's age wrapper against the vault's `.age-recipients`, and decrypt it
back. Small milestone by design — M2 already proved the crypto, so what's
left here is serialization and file placement.

## Depends on

- **M1** — `.age-recipients` reading.
- **M2** — the age encrypt/decrypt wrapper and `Identity`.

## Design references

- ["Entry format"](../tdds/gage-cli-design.md) — the fixed metadata
  fields and the `value`/`fields` split that replaces `pass`/`passage`'s
  positional convention
- ["On-disk layout"](../tdds/gage-cli-design.md) — why filenames are
  opaque UUIDs under `entries/`

## Decisions to make first

- **YAML library.** Not in the locked dependency list because nothing
  before now needed it. `go-yaml/yaml` v3 or `goccy/go-yaml`. Wants to
  round-trip cleanly and preserve field order on re-marshal, since M5's
  `$EDITOR` flow shows the file to a human.
- **Timestamp format on the wire.** The design shows
  `2026-01-14T10:32:00Z` — RFC 3339, UTC, second precision. Confirm
  second (not nanosecond) precision, since it round-trips through a
  human-edited file.
- **Unknown fields on unmarshal.** A future `gage` may add a field; an
  older binary reading that entry should preserve it rather than
  silently dropping it on the next `edit`. Decide now whether entries
  round-trip unknown keys — this is cheap here and impossible to
  retrofit without data loss. See the accepted risk on entry-level
  versioning in [open-questions.md](open-questions.md).

## Tests (write first)

- [ ] Entry struct marshals to the exact expected YAML shape
      (`title`/`description`/`created`/`updated`/`updated_by`/`value`/`fields`)
      and unmarshals back to an identical struct
- [ ] Field order in the marshaled YAML is stable and matches the design
      doc's example — the file is shown to a human in M5's `$EDITOR` flow
- [ ] Timestamps round-trip through marshal/unmarshal without drift or
      precision loss
- [ ] An entry with an empty `description` and empty `fields` round-trips
      without those keys becoming `null` or vanishing inconsistently
- [ ] Unknown/extra YAML keys behave per the decision above (preserved or
      rejected — not silently dropped)
- [ ] A `value` containing YAML-hostile content (leading `-`, embedded
      newlines, a literal `:`, trailing whitespace, non-ASCII) round-trips
      byte-for-byte
- [ ] Encrypt → decrypt round-trip through `entries/<uuid>.age` recovers a
      byte-identical entry
- [ ] An entry written to a vault with N recipients is independently
      decryptable by each recipient's identity
- [ ] Decrypting an entry with a non-recipient identity fails with the
      typed error from M2, surfaced at the entry layer rather than
      swallowed
- [ ] Generated filenames are valid UUIDv4 and unique across repeated
      calls (no collisions in a large-N generation test)
- [ ] Entry files are written `0600`, and `entries/` is `0700`
- [ ] An entry file is written atomically — an interrupted write leaves
      either the previous ciphertext or nothing, never a truncated
      `.age` file

## Implementation

- [ ] Entry struct + YAML (de)serialization, per the decisions above
- [ ] UUIDv4 filename generation
- [ ] Encrypt entry to `entries/<uuid>.age` — marshal, encrypt via M2's
      wrapper against the vault's parsed `.age-recipients`, write
      atomically at `0600`
- [ ] Decrypt entry from `entries/<uuid>.age` — read, decrypt via M2's
      wrapper with the caller's `Identity`, unmarshal
- [ ] Enumerate entries in a vault (list the UUID files) — needed by M4's
      `ls` and M7's index build; no decryption at this layer

## Definition of done

Full test list green on all three CI platforms. Entries can be written
and read back at the library level. No CLI verb touches them yet.

## Affects later milestones

- The entry read/write pair is the primitive every CRUD verb in M4 and
  M5 wraps, and the thing M9's `--reencrypt` iterates over.
- Whatever is decided about unknown-field preservation is effectively
  permanent once real vaults exist.
