# Configuration and file locations

`gage` follows the XDG Base Directory conventions on Linux/macOS, and maps
the same three roles onto native per-user directories on Windows:

| Role | Env var (wins on any OS if set) | Linux/macOS default | Windows default |
|---|---|---|---|
| Config | `XDG_CONFIG_HOME` | `~/.config` | `%APPDATA%` |
| Data (vaults, local identities) | `XDG_DATA_HOME` | `~/.local/share` | `%LOCALAPPDATA%` |
| State (history, trust cache, tokens) | `XDG_STATE_HOME` | `~/.local/state` | `%LOCALAPPDATA%\state` |

This documentation refers to `$GAGE_CONFIG`, `$GAGE_DATA`, `$GAGE_STATE` as
shorthand for `<role root>/gage` — e.g. `$GAGE_CONFIG` is
`~/.config/gage` on Linux/macOS and `%APPDATA%\gage` on Windows.

## The global config file

`$GAGE_CONFIG/config.toml` tracks known vaults and shell preferences:

```toml
current = "personal"

[vaults.personal]
path = "$GAGE_DATA/vaults/personal"
id = "208a0837-10f0-4024-b564-ffadc204c69f"   # the vault's own id, from its committed config
type = "git"
device = "laptop-1"       # this device's identity name in that vault
pubkey = "age1futpye..." # the public key this device holds for it
method = "passphrase"     # how THIS device unlocks it

[vaults.personal.git]
origin = "https://github.com/you/personal-vault.git"

[vaults.work]
path = "$GAGE_DATA/vaults/work"
id = "b41c9f02-7d55-4a18-9a30-6f2c8e1b7d44"
type = "git"
device = "laptop-1"
pubkey = "age1qz8x2..."
method = "passphrase"

[vaults.work.git]
origin = "https://git.internal.example/secrets/work-vault.git"

[shell]
prompt = "[{vault}{lock}] gage> "    # {vault}, {lock} (🔓/🔒), {dirty} tokens available
idle_timeout = "10m"                 # auto re-lock a session vault after inactivity
history_file = "$GAGE_STATE/history"   # command names/paths only, never values
clipboard_timeout = "45s"            # how long `show -c` leaves a value on the clipboard
```

`device` and `method` are answers to "who am I in that vault, and how do I
unlock it" — both are local to this machine, and both can legitimately
differ per vault: one laptop might unlock `personal` by passphrase and
`work` by a hardware key.

`id` is the vault's own UUID, minted at `init` and committed inside the
vault, so every clone of a vault agrees on it. **Everything this device
stores about a vault is keyed by that id, not by the local name** — so two
unrelated vaults both called `personal` on one machine never share identity
files or trust caches, and renaming a vault locally strands nothing. A
registration written before ids existed simply has no `id`, and the
commands that need one say so rather than guessing.

`pubkey` records which public key this device holds for the vault, which is
what lets `gage identity enroll` notice that this device is already a
recipient (by key, not by label) without unlocking anything, and what
`gage vault remove` compares before offering to delete an identity file.

This file is small, plaintext, and safe to keep under your own dotfiles
management (`chezmoi`, `yadm`, etc.) — that's exactly why it lives under
`$GAGE_CONFIG` rather than anywhere else.

## Where things live, and why

- **`$GAGE_DATA/vaults/<name>/`** — an actual vault's git repository.
- **`$GAGE_DATA/identities/<vault-id>/<device>.age`** — this device's
  wrapped private key for a vault (passphrase/age-key methods only).
  Directory mode `0700`, file mode `0600`. Keyed by the vault's **id**, not
  its local name, so renaming a vault or registering two different vaults
  under the same name never crosses keys. This is **not** inside the vault's
  own git tree (it would risk being committed and pushed, exposing a
  passphrase-wrapped key to offline brute-forcing by anyone with mere read
  access) and **not** under `$GAGE_CONFIG` (which dotfile managers sync by
  convention — the last place a private key should ride along to). Losing
  this file means registering a fresh identity and being re-added as a
  recipient; it is not something `gage` backs up for you automatically.
- **`$GAGE_DATA/identities/<vault-id>/vault-name.txt`** — a plaintext
  marker naming the vault that opaque UUID directory belongs to, so a human
  browsing `identities/` can tell which is which.
- **`$GAGE_STATE/history`** — the session shell history: command names and
  the entries they touched, never values. Commands run via `--script`/`--stdin`
  are never written here.
- **`$GAGE_STATE/<vault-id>/known-config.toml`** and **`trust.toml`** — the
  local trust cache used to detect unreviewed recipient changes (see
  [Identities and recipients](identities-and-recipients.md#the-recipient-change-confirmation)):
  a verbatim copy of the last `.gage/config.toml` this device confirmed,
  plus a hash and that record's metadata. Local-only and disposable —
  losing it just means the next write re-establishes it, and
  `gage vault remove` deletes it deliberately.
- **`$GAGE_STATE/locks/<vault-id>.lock`** — the per-vault advisory write
  lock (see [Sync and conflicts](sync.md#concurrent-gage-processes-are-normal-not-an-edge-case)).
  Held only while a write runs and released by the OS if the process dies;
  there is no stale lock file to clean up by hand.
- **`$GAGE_STATE/tokens/<host>`** — a git host auth token, mode `0600`. Kept
  out of `$GAGE_CONFIG` for the same reason as identity files: it shouldn't
  ride along in a dotfiles sync.

## Environment variables

| Variable | Effect |
|---|---|
| `XDG_CONFIG_HOME` / `XDG_DATA_HOME` / `XDG_STATE_HOME` | Override the config/data/state roots (any OS). |
| `GAGE_PASSPHRASE` | Supplies a passphrase for unlocking an *existing* identity under `gage --script FILE` / `gage --stdin`, and only there — an ordinary one-shot command ignores it and prompts. See [Session mode and scripting](session-mode.md#where-the-passphrase-comes-from). Never used to create a key (`init`, `identity add`, `identity enroll`'s create path). |

## Inside a vault itself

A `git`-type vault's own repository looks like:

```
myvault/                          # git repo root
├── .gage/
│   ├── config.toml               # vault-level config: id + type + method + recipients (committed, plaintext)
│   └── pending/                  # sealed enrollment requests (created lazily; often absent)
│       └── <request-uuid>-<expires-epoch>.age
├── .age-recipients               # recipient public keys, one per line (committed, plaintext)
├── entries/
│   └── <uuid>.age                # one encrypted file per entry
├── .gitattributes                # marks .age-recipients / .gage/config.toml as unmergeable
└── .gitignore                    # standard OS-cruft ignores (.DS_Store, Thumbs.db)
```

`.gage/config.toml` looks like this:

```toml
[vault]
name = 'demo'
id = '208a0837-10f0-4024-b564-ffadc204c69f'
type = 'git'
format_version = 2
created = '2026-09-12'

[method]
default = 'passphrase'

[[recipients]]
device = 'laptop-1'
pubkey = 'age18srydkf39n9sx40xtut6ry89fs22zetfng7gxqjdkkz0asjv9qnsjppaqj'
```

`[method].default` is a *suggestion* for devices joining later, not a
constraint — each device's actual method is the local-only `method` in your
global config, and is never committed.

`.age-recipients` stays flat at the vault root deliberately, so the stock
`age` CLI can use it directly (`age -R .age-recipients -e file`) without
knowing anything about `gage`'s layout. `.gitattributes` marks both
recipient-defining files unmergeable so two devices each adding a different
recipient can never silently union into a list neither of them actually
reviewed — see [Sync and conflicts](sync.md#what-counts-as-a-conflict). It
deliberately does **not** cover `.gage/pending/`: those files are inert and
uniquely named, so two devices enrolling at once merge cleanly. Nothing
about entries' filenames or directory placement carries any meaning — see
[Entries](entries.md); for what's in `pending/`, see
[Adding a device](enrollment.md#what-a-pending-request-is-on-disk).
