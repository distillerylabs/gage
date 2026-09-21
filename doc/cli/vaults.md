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
                  [--no-recovery-key | --recovery-key-out FILE]
```

- Without `--dir`, the vault is created at `$GAGE_DATA/vaults/<name>`.
- `--remote` is optional — a vault can start local-only and gain a remote
  later with `gage git set-remote` (see [Git remotes and authentication](git-and-auth.md)).
- `--method` sets the vault's **default** identity method, recorded in its
  committed `.gage/config.toml` as a suggestion for devices joining later —
  and is also what this device uses. It's a suggestion, not a constraint:
  each device's actual method is local config and can differ. `passphrase`
  is the default and, today, the only option.
- `--device` names this device as a recipient label; if omitted, it defaults
  to your normalized hostname. See [Identities and recipients](identities-and-recipients.md).
- By default `init` also generates an **offline recovery key**, registers it
  as the `recovery-paper-key` recipient, and shows the private key once
  (text and QR) on your terminal, asking you to type its last six characters
  back so you know you have it. `gage` keeps no copy. See
  [Storing your recovery key](identities-and-recipients.md#storing-your-recovery-key).
  - `--recovery-key-out FILE` writes it to a `0600` file instead of showing
    it — for a run with no terminal. The file must not already exist, and
    must not be inside a vault or under `$GAGE_DATA`, both of which `gage`
    deletes from.
  - `--no-recovery-key` skips it. That leaves this device's identity file as
    the only way into the vault; `gage recovery rotate` adds one later.
  - With no terminal and neither flag, `init` refuses before creating
    anything, rather than write an unencrypted key into a log.
  - `--device recovery-paper-key` is refused; that label is the key's.
- `--recipient` can be repeated to add extra public keys you already hold,
  in addition to this device's own key and the recovery key.

`init` and `clone` are the only vault commands that don't work inside a
session — they *create* a vault rather than operate on an existing one.

## Cloning an existing vault

```
gage clone <remote-url> [--name NAME] [--dir PATH] [--device NAME]
```

Clones the git repository and reads its `.gage/config.toml` to learn the
vault's default decryption method. Cloning does **not** grant you access.

If this machine holds no identity for the vault, `clone` says so and — on a
terminal — offers to set up an enrollment request right there:

```
gage: cloned "personal" to ~/.local/share/gage/vaults/personal
This device holds no identity for "personal", so it can't read anything here yet.
Set up an enrollment request now? [Y/n]
```

Saying yes runs exactly what `gage identity enroll` does, under the device
name `clone` already resolved — so `clone --device X` followed by `y`
publishes a request naming `X`. Saying no, or cloning without a terminal,
prints the manual path instead and exits `0`. Either way you can run
`gage identity enroll` later. See [Adding a device](enrollment.md).

The condition `clone` tests is "does this machine hold a wrapped identity
file for this vault at all" — not "is this device's key in the recipient
list", which can't be answered without unlocking, and prompting for a
passphrase to tell someone their unlock was pointless is exactly backwards.
So a machine that *does* hold an identity for the vault is left alone with
no message and no prompt, even if that key was never added as a recipient.
Run `gage identity enroll` explicitly in that case; it reuses the existing
key rather than generating a second one.

## Listing and inspecting vaults

```
gage vault list
gage vault info [<name>]
```

`vault info` shows a vault's name, id, type, decryption method, recipient
count, and (for the `git` type) its remote and clean/dirty status:

```
$ gage vault info
name: demo
id: 208a0837-10f0-4024-b564-ffadc204c69f
type: git
method: passphrase
recipients: 1
remote: https://github.com/you/personal-vault.git
status: clean
```

The **id** is a UUID minted at `init` and committed in the vault's own
`.gage/config.toml`, so every clone of a vault shares it. It's what `gage`
keys this device's local state by — identity files, the trust cache — which
is why two unrelated vaults that happen to be called `personal` on one
machine never collide. See [Configuration](configuration.md).

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

Local memory of the vault goes with the registration: the
[trust cache](identities-and-recipients.md#the-recipient-change-confirmation)
is dropped, so a later re-add is treated as a first use rather than
silently inheriting an approval for a recipient list nobody looked at in
the interval.

The local **identity file** is the one thing `gage` asks about. If the
vault's recipient list no longer contains this device's public key, the
file has nothing left to be needed for, and `gage` offers to delete it:

```
laptop's key is no longer a recipient of "personal". Delete this device's identity file for it?
  ~/.local/share/gage/identities/208a0837-.../laptop.age
This is the only copy of that private key, and deleting it cannot be undone. [y/N]
```

It compares **public keys**, never device names — a name is a label the
vault happens to store, not proof of which key it refers to, and
`recipient approve --device` deliberately produces mismatches. It defaults
to no, so a scripted `vault remove` keeps the file by construction. Every
other outcome keeps the file and says why: the key is still listed as a
recipient, the recipient list can't be read, or your config records no
public key for this device.
