# Command reference

Every command below works both as a one-shot invocation (`gage <cmd>`) and
inside a session (`gage> <cmd>`) unless marked **one-shot only** or
**session only**. See [Session mode and scripting](session-mode.md) for what
that distinction means, and the linked pages for full explanations — this
page is a compact index of every command and its flags.

`-u`/`--use NAME` selects which vault a one-shot command targets; inside a
session, `use <vault>` sets the session's current vault instead, and
`--use NAME` still works ad hoc against any vault already `use`d.

## Session (session only)

| Command | Description |
|---|---|
| `use <vault>` | Switch/unlock the active vault for this session. |
| `lock [vault]` | Drop key material for one vault (or all) without exiting. |
| `status` / `whoami` | List vaults touched this session and their lock state. |
| `help [command]` | Show help for gage's commands. |
| `exit` / `quit` / Ctrl-D | Leave the session. |

## Vault lifecycle

| Command | Description |
|---|---|
| `init <name> [--dir PATH] [--remote URL] [--type git] [--method passphrase] [--device NAME] [--recipient PUBKEY ...]` | Create a new vault. **One-shot only.** |
| `clone <remote-url> [--name NAME] [--dir PATH]` | Clone an existing vault from a remote. **One-shot only.** |
| `vault list` | List registered vaults. |
| `vault info [<name>]` | Show a vault's type, method, recipient count, and (for git) remote/status. |
| `vault remove <name>` | Forget a vault locally, leaving its files untouched. |
| `vault set-default <name>` | Change which vault is used when none is given. |

Details: [Vaults](vaults.md).

## Identity

| Command | Description |
|---|---|
| `identity add --use NAME [--method passphrase] [--device NAME] [--key-path PATH]` | Register a new identity for this device and print its public key. |
| `identity list [--use NAME]` | List the identities this device holds for a vault. |

Details: [Identities and recipients](identities-and-recipients.md).

## Recipients

| Command | Description |
|---|---|
| `recipient add <pubkey-or-name> [--use NAME]` | Authorize a public key and re-encrypt the vault to include it. |
| `recipient remove <pubkey-or-name> [--use NAME] --reencrypt` | Revoke a recipient and re-encrypt the vault without its key. |
| `recipient list [--use NAME]` | List every device this vault is encrypted to. |
| `recipient verify [--use NAME]` | Check `.age-recipients` and `config.toml` still agree. |

Details: [Identities and recipients](identities-and-recipients.md).

## Entry CRUD

| Command | Description |
|---|---|
| `insert <title> [--use NAME] [--description TEXT] [-m|--multiline \| --value-stdin \| -e|--edit] [-f|--force] [--yes]` | Create a new entry. |
| `show <query> [--use NAME] [-c|--clip] [-q|--qr] [--field NAME]` | Print an entry's value (or one field). |
| `cat <query> [--use NAME]` | Print an entry's full decrypted contents. |
| `edit <query> [--use NAME]` | Edit an entry's full YAML in `$EDITOR`. |
| `rename <query> <new-title> [--use NAME]` | Change an entry's title. |
| `generate <title> [--use NAME] [-l LENGTH] [--no-symbols] [-f|--force] [-c|--clip] [-q|--qr] [--yes]` | Create a new entry with a randomly generated value. |
| `rm <query> [--use NAME]` | Delete an entry. |
| `mv <query> --to-vault <name> [--use NAME] [--yes]` | Move an entry to another vault. |
| `cp <query> --to-vault <name> [--use NAME] [--yes]` | Copy an entry into another vault, keeping the original. |
| `ls [--use NAME]` | List entries with their ids, dates, and last writer. |
| `search <pattern> [--use NAME]` (alias `grep`) | Find entries by title, description, or body text. |
| `reindex [--use NAME]` | Force a session's cached metadata index to rebuild. |

Details: [Entries](entries.md), [Addressing entries](addressing-entries.md).

## Sync

| Command | Description |
|---|---|
| `sync [--use NAME]` | Pull, merge what can be merged, then push; resolves conflicts interactively. |
| `pull [--use NAME]` | Fast-forward this vault from its remote. |
| `push [--use NAME]` | Publish this vault's commits to its remote. |
| `log [QUERY] [--use NAME]` | Show an entry's commit history — timestamps only, nothing decrypted. |
| `history --decrypt <query> [--use NAME]` | Decrypt every past revision of one entry (surfaces old secret values). |

Details: [Sync and conflicts](sync.md).

## Git-specific

| Command | Description |
|---|---|
| `git set-remote <name> <url>` | Set or change a vault's git remote (origin). |
| `auth login [--host HOST]` | Store a token for a git host. |
| `auth status [--host HOST]` | Show which git hosts have a token. |
| `auth logout [--host HOST]` | Forget a git host's token. |

Details: [Git remotes and authentication](git-and-auth.md).

## Common flags

| Flag | Meaning |
|---|---|
| `-u`, `--use NAME` | Select which vault this command targets (one-shot mode). |
| `--yes` | Skip the recipient-change confirmation prompt — for scripting/CI. |
| `-f`, `--force` | Allow an action `gage` would otherwise warn about (e.g. a duplicate title on `insert`/`generate`). |
| `-c`, `--clip` | Copy a value to the clipboard instead of printing it (auto-clears). |
| `-q`, `--qr` | Render a value as a terminal QR code instead of printing it. |
| `--field NAME` | Operate on one entry from `fields` instead of `value`. |

## Non-interactive session mode

```
gage --script FILE       # read session commands from a file
gage --stdin             # read session commands from stdin
```

See [Session mode and scripting](session-mode.md).
