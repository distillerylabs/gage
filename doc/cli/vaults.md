# Vaults

A vault is the unit of trust in `gage`: it has its own git repository, its
own list of recipients (who can decrypt it), and its own decryption method.
You can have as many vaults as you want — `personal`, `work`,
`shared-family` — each with a different security posture. If you need a
different set of readers for a subset of secrets, that's a new vault, not a
subdirectory of an existing one (see [Sharing entries between vaults](sharing.md)
for the mechanism that moves secrets between vaults instead).

## Creating a vault

```
gage init <name> [--dir PATH] [--remote URL] [--type git]
                  [--method passphrase] [--device NAME]
                  [--recipient PUBKEY ...]
```

- Without `--dir`, the vault is created at `$GAGE_DATA/vaults/<name>`.
- `--remote` is optional — a vault can start local-only and gain a remote
  later with `gage git set-remote` (see [Git remotes and authentication](git-and-auth.md)).
- `--method` chooses how *this device* will prove it can decrypt the vault.
  `passphrase` is the default and, today, the only option.
- `--device` names this device as a recipient label; if omitted, it defaults
  to your normalized hostname. See [Identities and recipients](identities-and-recipients.md).
- `--recipient` can be repeated to add extra public keys (e.g. a recovery
  key) at creation time, so the vault isn't single-point-of-failure from the
  start.

`init` and `clone` are the only vault commands that don't work inside a
session — they *create* a vault rather than operate on an existing one.

## Cloning an existing vault

```
gage clone <remote-url> [--name NAME] [--dir PATH]
```

Clones the git repository and reads its `.gage/config.toml` to learn the
vault's default decryption method. Cloning does **not** grant you access —
if your device isn't already a recipient, `gage` tells you to run
`gage identity add` to generate a public key, then have an existing
recipient run `gage recipient add` with it.

## Listing and inspecting vaults

```
gage vault list
gage vault info [<name>]
```

`vault info` shows a vault's type, decryption method, recipient count, and
(for the `git` type) its remote and clean/dirty status.

## Selecting which vault a command targets

There is one concept for "which vault does this apply to," spelled two ways
depending on mode:

- **One-shot mode:** `-u|--use NAME` on any single command.
- **Session mode:** `use <name>` as its own line, which switches what every
  subsequent command in that session targets.

```
$ gage show github-token --use personal      # one-shot
gage> use personal                           # session
gage> show github-token
```

If you omit `--use` in one-shot mode, `gage` falls back to your configured
default vault.

## Changing the default vault

```
gage vault set-default <name>
```

This is intentionally a different command from `use` — `use`/`--use` select
a vault for *this* command or session; `set-default` changes what "no
`--use` given" falls back to, persistently, in your global config.

## Removing a vault (locally)

```
gage vault remove <name>
```

This forgets the vault in your local config — it does **not** delete the
underlying git repository or its remote. If you want the data gone, delete
the directory (and remote) yourself.
