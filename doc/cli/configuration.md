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
type = "git"
device = "laptop-1"       # this device's identity name in that vault
method = "passphrase"     # how THIS device unlocks it

[vaults.personal.git]
origin = "https://github.com/you/personal-vault.git"

[vaults.work]
path = "$GAGE_DATA/vaults/work"
type = "git"
device = "laptop-1"
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

This file is small, plaintext, and safe to keep under your own dotfiles
management (`chezmoi`, `yadm`, etc.) — that's exactly why it lives under
`$GAGE_CONFIG` rather than anywhere else.

## Where things live, and why

- **`$GAGE_DATA/vaults/<name>/`** — an actual vault's git repository.
- **`$GAGE_DATA/identities/<vault>/<device>.age`** — this device's wrapped
  private key for a vault (passphrase/age-key methods only). Directory mode
  `0700`, file mode `0600`. This is **not** inside the vault's own git tree
  (it would risk being committed and pushed, exposing a passphrase-wrapped
  key to offline brute-forcing by anyone with mere read access) and **not**
  under `$GAGE_CONFIG` (which dotfile managers sync by convention — the
  last place a private key should ride along to). Losing this file means
  registering a fresh identity and being re-added as a recipient; it is not
  something `gage` backs up for you automatically.
- **`$GAGE_STATE/history`** — the session shell history: command names and
  the entries they touched, never values. Commands run via `--script`/`--stdin`
  are never written here.
- **`$GAGE_STATE/<vault>/known-config.toml`** — the local trust cache used
  to detect unreviewed recipient changes (see
  [Identities and recipients](identities-and-recipients.md#the-recipient-change-confirmation)).
  Local-only and disposable — losing it just means the next write
  re-establishes it.
- **`$GAGE_STATE/tokens/<host>`** — a git host auth token, mode `0600`. Kept
  out of `$GAGE_CONFIG` for the same reason as identity files: it shouldn't
  ride along in a dotfiles sync.

## Environment variables

| Variable | Effect |
|---|---|
| `XDG_CONFIG_HOME` / `XDG_DATA_HOME` / `XDG_STATE_HOME` | Override the config/data/state roots (any OS). |
| `GAGE_PASSPHRASE` | Supplies a passphrase for unlocking an *existing* identity non-interactively (`--script`/`--stdin`, or scripted one-shot commands). See [Session mode and scripting](session-mode.md#where-the-passphrase-comes-from). Never used for `init`/`identity add`. |

## Inside a vault itself

A `git`-type vault's own repository looks like:

```
myvault/                          # git repo root
├── .gage/
│   └── config.toml               # vault-level config: type + method + recipients (committed, plaintext)
├── .age-recipients               # recipient public keys, one per line (committed, plaintext)
├── entries/
│   └── <uuid>.age                # one encrypted file per entry
├── .gitattributes                # marks .age-recipients / .gage/config.toml as unmergeable
└── .gitignore                    # standard OS-cruft ignores (.DS_Store, Thumbs.db)
```

`.age-recipients` stays flat at the vault root deliberately, so the stock
`age` CLI can use it directly (`age -R .age-recipients -e file`) without
knowing anything about `gage`'s layout. `.gitattributes` marks both
recipient-defining files unmergeable so two devices each adding a different
recipient can never silently union into a list neither of them actually
reviewed — see [Sync and conflicts](sync.md#what-counts-as-a-conflict).
Nothing about entries' filenames or directory placement carries any
meaning — see [Entries](entries.md).
