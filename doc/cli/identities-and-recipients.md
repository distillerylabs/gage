# Identities and recipients

`gage` separates two concerns that are easy to conflate: **who can read a
vault** (recipients — a vault-wide list of public keys) and **how a
particular device proves it holds the matching private key** (identity —
decided independently by each device, never dictated by the vault).

## Identity: how this device proves it can decrypt

```
gage identity add --use NAME [--method passphrase] [--device NAME]
                   [--key-path PATH]
gage identity list [--use NAME]
```

`identity add` registers how *this device* holds its private key and
prints the resulting public key so it can be added as a recipient — either
by you, if you're bootstrapping a new vault, or by an existing recipient
adding you to theirs.

- **`--method`** is this device's own choice, not the vault's. Omitted, it
  takes the vault's configured default method. `passphrase` is the only
  method today.
- **`--device`** names this device as a recipient label. Omitted, it
  defaults to your normalized hostname (lowercased, truncated at the first
  dot, non-`[a-z0-9._-]` characters replaced). A name already registered as
  a recipient of this vault is rejected rather than silently taken over.
- A hostname-derived name is convenient but not private — it's stored in a
  plaintext, committed file every recipient can read. If that's a concern
  for a shared vault, pass `--device` explicitly.

For the passphrase method, this also writes a wrapped identity file to
local, device-only storage (never inside the vault's own git tree — see
[Configuration](configuration.md) for exactly where). **Losing that file is
a recovery problem, not a backup problem:** the fix is the same as "this
device is gone" — run `gage identity add` again to generate a fresh
identity, and have any other recipient add it via `gage recipient add`.
This is why it's worth adding a recovery key as an extra recipient when you
create a vault (`gage init --recipient ...`) — a vault that depends on
exactly one identity file surviving forever has no real recovery story.

## Recipients: who can decrypt a vault

```
gage recipient add <pubkey-or-name> [--use NAME] [--reencrypt]
gage recipient remove <pubkey-or-name> [--use NAME] --reencrypt
gage recipient list [--use NAME]
gage recipient verify [--use NAME]
```

- **`recipient add`** authorizes a public key to read future writes to the
  vault. Add `--reencrypt` to also decrypt and re-write every existing
  entry so the new recipient can read history too — without it, they can
  only read entries written after they were added.
- **`recipient remove`** *requires* `--reencrypt` — `gage` refuses to
  silently leave old ciphertext readable by someone you just revoked. It
  prints a clear warning either way: **removing a recipient revokes future
  access only.** Anything they already read can't be unread.
- **`--reencrypt` is all-or-nothing.** Every entry is re-encrypted in the
  working tree, and the recipient list plus every touched entry land in
  exactly one commit — nothing commits until the whole operation succeeds.
  If it's interrupted (crash, kill, power loss), the vault is left exactly
  as it was before the command ran; re-running `--reencrypt` starts
  cleanly from scratch.
- **`recipient verify`** checks that `.age-recipients` and
  `.gage/config.toml` agree on who's a recipient. It needs no unlock (both
  files are plaintext), so it works before any identity is available and is
  safe to run in CI. It exits `0` and prints "in sync" on a match, or exits
  `1` and lists the specific differences otherwise.

## The recipient-change confirmation

Before encrypting to a vault's current recipient list, `gage` compares it
against a locally cached copy from the last time you used this vault (its
"local trust cache"). If the list has changed since then — a new recipient
was added, on this device or another — you get a warning and a
confirmation prompt before the write proceeds, so a compromised or
carelessly merged recipient list can't silently start encrypting your
secrets to someone new. Pass `--yes` on the write command to skip this
prompt in scripts/CI, where there's nobody to confirm anyway.
