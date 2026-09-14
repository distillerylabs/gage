# `gage` — a git-backed, age-encrypted secret & notes manager

*(working name: `gage` = "git" + "age"; rename freely)*

## Design principles

1. **A vault is the unit of trust.** Each vault has its own set of
   recipients, and that list — nothing finer-grained — is who can read it.
   You can have as many vaults as you want (`work`, `personal`,
   `shared-family`), each with a different security posture. A vault also
   records a *default* decryption method for devices joining it, but the
   method is a per-device choice rather than a vault-wide constraint (see
   "Decryption methods are per-device" below): what a vault actually
   contains is a list of public keys, and how each device holds the
   matching private key is that device's business. A vault also has a
   *type*, naming the backing store that durably holds and syncs its
   entries — today that's always `git` (see "Vault types" below), but
   making it a first-class field rather than an assumption is what lets the
   storage layer change later without the rest of `gage` noticing.
2. **Entries are just files, named opaquely.** A password, a structured note
   (API keys, recovery codes), or an unstructured note (a paragraph of text)
   are all the same thing on disk: one `.age`-encrypted file per entry,
   git-tracked, named with a random UUID rather than a human-readable path.
   Everything a human would recognize — title, description, the actual
   value — lives *inside* the encrypted payload as structured YAML. Someone
   with read access to the vault but not the decryption key sees only opaque
   filenames and ciphertext; they learn nothing about what you have stored,
   not even categories or counts-per-topic.
3. **Sync happens via the backing store, not a feature `gage` hides.** Every
   write is a commit. Standard git remotes (self-hosted, GitHub private repo,
   etc.) do the syncing today; `gage` never invents its own transport, and a
   future vault type would sync via whatever transport *it* natively uses,
   under the same principle.
4. **Decryption method is pluggable, not hardcoded.** Passphrase, raw age
   key file, SSH key, YubiKey (`age-plugin-yubikey`), Secure Enclave — the
   vault just stores *which* method and *whose* public keys; each device
   registers its own way of proving it holds the matching private key.
5. **Key material lives exactly as long as the process that holds it.**
   No background daemon, no IPC socket, no separate agent lifecycle to
   manage or forget about. Unlocking happens in-process; exiting the
   process is what destroys the key. See "Session model" below.
6. **Plaintext should be surprising to produce.** Default `show` prints to
   the terminal (you asked for it), but copy-to-clipboard and QR-to-camera
   are first-class so plaintext never has to sit in scrollback or a file.
7. **The CLI is a frontend, not the implementation.** Every command is a
   method on a small set of library types (`Vault`, `Session`, `Entry`)
   that know nothing about terminals, flags, or stdin — no `fmt.Print`, no
   reading a passphrase off stdin, no `os.Exit`. `cmd/gage` is a thin
   Cobra layer that calls into that library and renders the result. This
   isn't speculative: a GUI and/or TUI are planned on top of the same
   library, in-process, with no authentication layer between them and
   it — same binary module, same process, so principle 5's trust boundary
   ("key material lives exactly as long as the process that holds it")
   already covers this without change. See "Library architecture" below.

---

## Library architecture

`gage` is designed so the CLI is the first frontend, not the only one. A
GUI and/or TUI are expected to sit on top of the same functionality later,
and the goal is for that to be additive — new frontends consuming an
existing library — rather than a rewrite of core logic that happened to be
buried inside Cobra command handlers.

**The library layer.** All the behavior described in this document —
vault lifecycle, entry CRUD, identity/recipient management, session
unlocking, sync, the trust cache — lives in a library package
(`internal/gage`) built around a small set of types:

- **`Vault`** — a single vault's on-disk state: config, recipients,
  entries. Handles the vault-generic operations (`init`, `ls`, `insert`,
  `recipient add`, ...) whether or not any identity is unlocked.
- **`Session`** — the in-memory, process-scoped object described in
  "Session model" below: one or more unlocked `Vault`s, their decrypted
  keys, and the per-vault metadata index. `Session` is a library type,
  not a REPL — the REPL is the CLI's particular way of driving one.
- **`Entry`** — a single decrypted record.

None of these types do their own I/O. No `fmt.Print`, no reading a
passphrase from stdin, no `os.Exit`. A method either returns a value/error,
or — for the handful of operations that need a human decision mid-flight —
returns a structured request for one (see "Interactive decisions" below).
`cmd/gage` (Cobra) is a thin layer on top: it parses flags, calls into
`Session`/`Vault`, and renders the result as terminal output, prompts, or
an exit code. A future GUI or TUI would be an equally thin layer calling
the same methods and rendering the same results differently — a dialog
instead of a `[y/N]` prompt, a form instead of `$EDITOR`, a native QR
widget instead of terminal ASCII art.

**Why this needs no authentication.** The library is embedded in-process,
not served over a socket or RPC — a GUI/TUI built against it links the
same package into its own binary and calls it directly, the same way the
CLI does. There's no network boundary or IPC channel for an auth layer to
guard. This is the same trust boundary as principle 5's "no daemon": each
frontend process holds its own unlocked keys in its own memory for as
long as it runs, whether that process is `gage`, a GUI app, or a TUI.
Nothing here reopens the cross-terminal-sharing trade-off described later
in "Trade-off vs. a shared agent" — a GUI's `Session` is exactly as
isolated from the CLI's `Session` as two CLI processes are from each
other.

**Interactive decisions become data, not printed text.** A few points in
this document describe `gage` printing something and waiting for an
answer: the recipient-change diff and `[y/N]` prompt (see "Local trust
cache"), the ambiguous-query candidate list (see "Addressing entries"),
and the unlock prompt itself (passphrase entry, YubiKey touch). At the
library level, each of these is a typed result — a recipient-change
warning struct, a candidate list, an unlock-callback interface — that the
caller decides how to present. The CLI renders them as terminal prompts
exactly as described elsewhere in this document; a GUI would render the
same data as a dialog or picker. The underlying decision logic (what
counts as ambiguous, when a cache regenerates, what `--yes` skips) doesn't
change — only which layer owns the pixels.

**Identity is a parameter, not internal state.** `Vault`'s CRUD methods
(`Get`, `Insert`, `Rm`, `Edit`, ...) never unlock anything themselves —
each takes an already-produced `Identity` as an explicit argument and is
otherwise stateless between calls. The only thing that produces an
`Identity` is `Vault.Unlock(Prompter) (Identity, error)`, which runs the
method-specific unlock flow (passphrase prompt and decrypt of the wrapped
identity file, YubiKey touch, whatever the configured method needs — see
"Local identity storage") and returns a value the caller threads through
every subsequent call. One-shot mode's command handler calls `Unlock`
once, uses the result for a single `Vault` call, and calls
`Identity.Close()` before exiting; `Session.Use` calls the identical
`Unlock` once and holds the result across many calls until `Lock`, an
idle timeout, or process exit closes it (see "Session model" below).
Neither mode is a special case of the other — both run the same
`Unlock` → use → `Close` contract, just once per command versus once per
session. `Identity.Close()` is the one place responsible for releasing the page
lock on and zeroing the private key, invoked from exactly those two
trigger points. The same rule extends to the in-session metadata index (see
"Addressing entries & the metadata index" below): it lives on `Session`,
keyed alongside each vault's cached `Identity`, never on `Vault` — `Vault`
stays a stateless, identity-agnostic operator over ciphertext in every
mode, and all "how long does this stay unlocked" bookkeeping belongs to
`Session` alone.

The `Prompter` exchange behind `Unlock` is method-agnostic for the same
reason: it's one generic call, `Prompter.Unlock(req UnlockRequest)
(UnlockResponse, error)`, not a passphrase-specific method. A
passphrase-method vault's request carries `Kind: "passphrase"` and
expects a string back; a future YubiKey-method vault's request would
carry `Kind: "yubikey"` and expect nothing but a touch signal. Only the
passphrase branch exists today, but a second `Kind` is additive to the
interface, not a breaking change to it — the same "additive, not
reworked" bar the rest of this document holds itself to. `Unlock` returns
distinguishable typed errors (wrong passphrase, a corrupt identity file,
no local identity registered for this device) rather than an opaque one,
so retry policy — whether and how many times to re-prompt after a wrong
passphrase — stays a decision `cmd/gage` makes, not one the library bakes
in.

**What's deliberately CLI-only.** Some things in the command reference are
presentation choices, not library behavior, and won't have a direct GUI/TUI
equivalent — they're mentioned here so it's clear they don't need to
survive a port:

- `gage edit`'s and `gage insert -e`'s shared `$EDITOR`-on-scratch-file
  round trip. The library operation is "write this fully-formed entry"
  (create or update); the CLI happens to implement its authoring UI via
  `$EDITOR`, seeded with a decrypted entry for `edit` and an empty stub
  for `insert -e`. The scratch file itself prefers a verified tmpfs-backed
  directory where the OS provides one (Linux) and falls back to a
  tightly-permissioned, best-effort-wiped temp file elsewhere (macOS,
  Windows) — see "A few decisions worth calling out" for why. A GUI
  would just have a form calling the same underlying method.
- Terminal ASCII QR rendering (`-q`/`--qr`). The library returns the bytes
  to encode; the CLI renders them as terminal art, a GUI would render an
  actual QR image widget.
- The readline-style session history file and its plaintext-query-only
  rule (see "A few decisions worth calling out"). This is CLI-terminal
  state; a GUI wouldn't have a comparable file at all.
- `--script`/`--stdin` non-interactive mode. This is the CLI's way of
  driving a `Session` from a list of commands instead of a TTY — a
  stand-in for "just call the library directly," which is what a GUI/TUI
  does anyway.

---

## Vault types

A vault's *type* names the backing store that durably holds its entries
and handles sync — separate from its decryption *method* (how a device
proves it can read it) and its *recipients* (who can). Pulling this out
as its own field, rather than assuming git, is what keeps everything else
— entry CRUD, identity/recipient management, the session model,
query resolution — from ever needing to know or care how a given vault
is actually stored.

Today there is exactly **one** type: `git`. Every vault is a git
repository, and `type = "git"` is recorded explicitly (in both the
vault's own `.gage/config.toml` and the global config — see "Global
config" below) rather than left implicit, so that:

- **Type-specific metadata has a home.** For `git`, that's just the
  remote URL (`origin`). A future type would carry its own metadata —
  a bucket name, an endpoint, whatever it needs — namespaced under that
  type rather than bolted onto fields that assume git.
- **The vault-generic/type-specific split in the command reference is
  real, not just a mental model.** Commands that only make sense because
  the backing store *is* git — today, just setting a remote — are
  namespaced under `gage git`, not `gage vault` (see "Git-specific
  commands" below). Everything else —
  `init`, `clone`, `vault list/info/remove/set-default`, `use`, all entry
  CRUD, `identity`, `recipient` — talks about vaults in the abstract and
  would not need to change if a second type showed up.
- **Even the git-implemented verbs that stay generic are deliberately
  generic.** `sync`/`pull`/`push`/`log`/`history` remain plain `gage`
  commands, not `gage git` commands, because every vault type will need
  some notion of "catch up," "publish my changes," and "show history" —
  the *interface* is store-agnostic even though today's one
  *implementation* isn't.

`gage init` takes an optional `--type` flag, defaulting to (and, today,
only accepting) `git`. With exactly one type there's nothing real to
choose between yet, but the flag exists now — validated against a
single-element allowlist — so that adding a second type later is purely
additive to the CLI surface (a new accepted value) rather than a breaking
change introducing the flag for the first time on configs and scripts
that predate the concept of "type" at all.

---

## On-disk layout

This shows the layout of a `git`-type vault — the only type that exists
today:

```
myvault/                          # git repo root
├── .gage/
│   ├── config.toml               # vault-level config: type + method + device metadata (committed, plaintext)
│   └── pending/                  # sealed device-enrollment requests, one file each (created lazily)
├── .age-recipients               # recipients for the whole vault — the only one
├── entries/
│   ├── 4b9d7710-8e2a-4a1f-9c3d-1a2b3c4d5e6f.age
│   ├── a03e5f88-1c44-4e9a-8b77-2d3e4f5a6b7c.age
│   └── 8f3a1c2e-5566-4a11-9d22-33aa44bb55cc.age
├── .gitattributes                # stops git auto-merging the recipient files
└── .gitignore
```

Flat *inside* `entries/`, deliberately — the UUID files themselves have no
further structure. Keeping them under `entries/` rather than scattered at
the vault root is purely cosmetic (a `git status`/`ls` at the root shows
`.gage/`, `.age-recipients`, `entries/` — three things, not a growing wall
of random UUIDs) since there's no more per-directory recipient scoping to
motivate any particular placement. Filenames carry no meaning — they're
generated (UUIDv4) at `insert` time and never chosen or seen by the user
in normal use. Categorization lives entirely in each entry's metadata (see
"Entry format" below), searchable only by someone holding the key.

If you want a subset of entries to have a *different* set of people who
can read them, that's a different vault, not a subtree of this one — see
"Why no per-directory sharing" below.

**`.gage/pending/` holds device-enrollment requests**, one sealed file
per request, and the scheme is
[gage-cli-init-design.md](gage-cli-init-design.md)'s rather than restated
here. Two of its properties constrain anyone reading this layout, though,
whether or not they read that document:

- **It is created lazily**, on the first enroll. Git tracks no empty
  directories, so its absence is the normal state and means "no pending
  requests" — never an error, and true of every vault that predates the
  feature.
- **It is inert.** Nothing in it is read at encryption time: a request is
  a proposal, and encryption reads `.age-recipients` exactly as it did
  before the directory existed. That is what keeps the directory from
  widening the trust boundary, which is why it is stated here rather than
  only where the feature is designed.

Note that `.gitattributes` below deliberately does *not* cover it: a
union of two devices' pending requests is the correct merge outcome,
unlike a union of two recipient lists.

Two things live at the vault root instead of nested, and one thing doesn't
— worth being explicit about why, since it's not arbitrary:

- **`.age-recipients` stays flat at the vault root.** It needs a
  predictable, un-nested path so the stock `age`/`passage` CLI can use it
  directly (`age -R .age-recipients -e file`) without knowing anything
  about `gage`'s internal layout — that external-interop requirement is
  the whole reason it's not tucked away somewhere.
- **`config.toml` lives under `.gage/`, not at the vault root.** Nothing
  outside `gage` itself ever reads it, so there's no interop reason it
  needs a flat path. Nesting it — mirroring `.git/`'s own convention —
  makes clear it's `gage`'s control-plane data rather than vault content,
  and leaves room for more `gage`-owned files later (schema version
  markers, migration state) without cluttering the root or claiming a
  generic name like `config.toml` at the top level.

**`.gitattributes` has exactly one job, and it's a security one.**
`.age-recipients` is one public key per line — which means two devices
each adding a *different* recipient produce two different added lines,
and git merges them without any conflict at all. The result is a
recipient list neither device ever wrote, assembled silently by a merge
that looks clean, and every subsequent write encrypted to all of it.
That defeats the local trust cache in precisely the situation it exists
to catch (see "Local trust cache"), because there's no unreviewed
*change* to notice — the union is what the merge produced.

So the vault ships a `.gitattributes` marking the two recipient-defining
files as unmergeable:

```
.age-recipients   -merge
.gage/config.toml -merge
```

Any divergence on either file is then forced to a real conflict that a
human resolves through the trust-cache confirmation, rather than being
quietly reconciled by a line-based merge that has no idea what those
lines mean. Entry files need no such marking: `.age` ciphertext is
binary, so git already refuses to merge two versions of one and reports
a conflict on its own.

**`.gitignore` has deliberately little to do.** By construction, nothing
`gage` itself ever writes into a vault's working tree that isn't meant to
be committed: the trust cache lives under `$GAGE_STATE`, identity files
under `$GAGE_DATA/identities/` (see "Local identity storage"), and the
`$EDITOR` scratch file lives outside the repo entirely (see "A few
decisions worth calling out"). So the vault's `.gitignore` isn't hiding
any `gage`-generated state — it's just the standard OS-cruft
list (`.DS_Store`, `Thumbs.db`) any git repo carries, there so a
vault directory browsed in a Finder/Explorer window doesn't get one of
those accidentally committed. If a future vault type's own workflow ever
needs to stage a real gage-owned file it shouldn't commit, that's the
one thing that would grow this list — not a reason to invent one for the
`git` type today.

`.gage/config.toml` (plaintext, committed — it names the vault's type,
method, and public keys, never secret material):

```toml
[vault]
name = "myvault"
id = "9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497"   # minted once by init; never changes
type = "git"              # the only type today — see "Vault types"
format_version = 2
created = "2026-08-29"

[method]
default = "yubikey"       # passphrase | age-key | ssh | yubikey | secure-enclave | plugin:<name>
plugin = "age-plugin-yubikey"   # a default for devices joining, not a constraint

[[recipients]]
device = "yubikey-5c-nfc-1"
pubkey = "age1yubikey1q..."
[[recipients]]
device = "recovery-paper-key"
pubkey = "age1..."
```

**`format_version` is enforced, not decorative.** `gage` refuses outright
to operate on a vault whose `format_version` it doesn't recognize, with
an error saying so, rather than parsing what it understands and ignoring
the rest. A forward-compatibility marker that isn't checked from the
very first release is worse than not having one: an older binary would
half-read a newer vault, silently drop the fields it didn't know about
on the next write, and the marker would have bought nothing. Enforcing
it from v1 is what makes it possible to change this file's shape later
at all.

### Decryption methods are per-device

`[method].default` is exactly what it says: the method suggested to a
device joining this vault, not a rule every device must follow. A single
vault can have a laptop unlocking via YubiKey touch, a phone via the
Secure Enclave, and a paper recovery key that's a bare age keypair —
same recipient list, different local mechanics.

**Nothing cryptographic requires otherwise.** age recipients are just
public keys, and one `.age` file can be encrypted to a mix of X25519 and
plugin recipients — a YubiKey recipient is `age1yubikey1...` and sits in
`.age-recipients` beside any other. A vault-wide method would have been
policy dressed up as a constraint, and enforcing it would buy nothing:
the recipient list is what determines who can read the vault, and it
can't tell you how any of those keys are stored anyway.

This follows directly from the `identity`/`recipient` split (see "A few
decisions worth calling out"). A *recipient* is public, vault-side, and
committed. An *identity* is how one device proves it holds the matching
private key — device-side, and never committed. A method is an identity
concern, so:

- **Each device's actual method is recorded locally**, alongside that
  device's own identity name, and never written into the vault. Nothing
  else needs it: unlocking consults only your own method, and adding a
  recipient only ever needs a public key.
- **The vault therefore never advertises which device uses which
  method.** That's worth having on purpose — a committed
  `device = "laptop-1", method = "passphrase"` line would tell anyone
  with read access exactly which recipient is the softest target,
  without enabling anything in return.
- **`[method].default` exists for the joining experience.** `gage clone`
  reads it to know what to suggest, so a new device gets a sensible
  prompt instead of being asked to pick from a list of methods with no
  indication of what this vault's other devices do.

`gage init --method` sets both the vault's default and the first device's
own method. `gage identity add --method` sets just that device's,
defaulting to the vault's default when omitted. Both are validated
against the same single-value allowlist described under `gage init` —
today, `passphrase`.

Recipients apply to the whole vault, full stop — no per-directory overrides.
`gage init` is cheap enough that "who needs access to this?" is a vault-level
question answered once at creation, not something that needs finer-grained
plumbing inside a single vault.

### Why no per-directory sharing

An earlier version of this design let `.age-recipients` be overridden per
subtree, `passage`-style, so one vault could mix a household `shared/` tree
with a private `personal/` tree. It works, but it buys a real amount of
complexity for a use case that has a much simpler answer: **make a second
vault.**

```
gage init shared-family --method passphrase --recipient <family-member-pubkey>
gage mv family-wifi --to-vault shared-family
```

`mv --to-vault` decrypts the entry from the source vault (which has to be
unlocked) and re-encrypts it for the destination vault's recipients — and
notably, that destination doesn't need to be unlocked at all, since
encrypting *to* a set of public keys never requires holding any of the
matching private keys. `cp --to-vault` does the same but leaves the
original in place.

This isn't just simpler to implement — it removes an entire class of
problem rather than mitigating it. Per-directory recipients meant "which
directory an entry sits in" was itself a privilege boundary, which is
exactly the shape of bug your original question was probing: someone with
git write access moving a file into a directory they happen to have keys
for. With one recipient list per vault, there's no finer-grained boundary
to attack — "does this person have write access to this specific vault"
is now the entire question (today, that means git write access to its
repo), and it's a question the backing store's own hosting already
answers.

What you give up: if your `work` vault needs three different partial-access
groups, that's three vaults instead of one vault with three subtrees. In
practice this tends to be the right shape anyway — separate vaults get
separate remotes, separate access lists on the git host, and separate
audit trails, which is arguably what you wanted for genuinely different
trust groups regardless of what `gage` did internally.

---

## Entry format

Once decrypted, an entry is YAML with a fixed set of metadata fields plus
the actual content. This is the on-disk/`$EDITOR`-buffer shape only — what
`gage cat` prints to a terminal is a separate, related-but-not-identical
rendering (a synthetic `id` line after `title`, every label left-padded so
its `:` lines up with the others, and a multi-line value's block-scalar
content printed flush left with no added indentation — which means an
entry with a multi-line value no longer parses back as strict YAML from
`cat`'s output; `gage show`/`UnmarshalEntry` against the on-disk format
are unaffected), so the two are not expected to match byte for byte:

```yaml
title: ProtonMail
description: Personal email, 2FA via authenticator app
created: 2026-01-14T10:32:00Z
updated: 2026-08-20T09:03:00Z
updated_by: yubikey-5c-nfc-1
value: correcthorsebatterystaple
fields:
  username: me@proton.me
  totp_seed: JBSWY3DPEHPK3PXP
```

- **`title`** — required. What `ls`/`search` match against and display; the
  closest thing to a "name" an entry has, but it only exists post-decrypt.
- **`description`** — optional free text, also searchable.
- **`created`** — set once, at `insert` time, never touched again.
- **`updated`** / **`updated_by`** — rewritten on every `edit`. `updated_by`
  is the local device's identity name (the same one registered via
  `gage identity add`, e.g. `yubikey-5c-nfc-1`) — no new concept needed,
  it's just recording which already-known identity made the change.
- **`value`** / **`fields`** — the actual payload. `value` holds the
  entry's primary payload (a password, or the body of an unstructured note);
  `fields` holds structured key/value metadata (`--field NAME` on
  `show`/generate extracts from here). This replaces the old positional
  "line 1 = secret, rest = key:value" convention from `pass`/`passage` with
  explicit keys, since the file is already structured YAML for the metadata
  anyway.

`gage insert`/`gage generate` populate `title`/`created`/`updated_by`
automatically; `gage edit` opens the full YAML in `$EDITOR` and re-stamps
`updated`/`updated_by` on save. `gage insert -e` shares that same
`$EDITOR` round trip at creation time, so `fields` can be populated
immediately instead of requiring a follow-up `edit`.

---

## Global config

Everything outside a vault's own git repo follows the XDG Base Directory
conventions — config, data, and state get their own root instead of one
`~/.gage/` catch-all. On Linux and macOS that's the familiar
`$XDG_CONFIG_HOME` / `$XDG_DATA_HOME` / `$XDG_STATE_HOME` split; those
environment variables don't exist natively on Windows, so `gage` maps
the same three roles onto the per-user directories Windows already
provides, rather than transplanting a Unix dotfolder onto a platform
where it looks out of place:

| Role | Env var (if set, wins on any OS) | Linux default | macOS default | Windows default |
|---|---|---|---|---|
| Config | `XDG_CONFIG_HOME` | `~/.config` | `~/.config` | `%APPDATA%` |
| Data (vaults, local identities) | `XDG_DATA_HOME` | `~/.local/share` | `~/.local/share` | `%LOCALAPPDATA%` |
| State (history, trust cache) | `XDG_STATE_HOME` | `~/.local/state` | `~/.local/state` | `%LOCALAPPDATA%\state` |

Every path `gage` owns is `<role root>/gage/...`. The rest of this doc
writes those as `$GAGE_CONFIG`, `$GAGE_DATA`, `$GAGE_STATE` — e.g.
`$GAGE_CONFIG` resolves to `~/.config/gage` on Linux/macOS and
`%APPDATA%\gage` on Windows.

`$GAGE_CONFIG/config.toml` tracks known vaults and shell preferences:

```toml
current = "personal"

[vaults.personal]
path = "$GAGE_DATA/vaults/personal"
id = "9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497"   # copied from the vault's own config
type = "git"
device = "laptop-1"       # this device's identity name in that vault
pubkey = "age1qz8x2..."   # this device's public key for that vault
method = "passphrase"     # how THIS device unlocks it — see "Decryption
                          # methods are per-device"

[vaults.personal.git]
origin = "https://github.com/you/personal-vault.git"

[vaults.work]
path = "$GAGE_DATA/vaults/work"
id = "c41d8e07-52b9-4a36-9f18-7d0ae6c25b83"
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

`id` is copied from the vault's own committed config so this machine can
find that vault's identities directory without reading the vault at all
— which matters when the vault's files have moved or been removed, the
situation A20 exists to make safe.

`pubkey` is this device's own public key for that vault, recorded when
the identity is created. It exists so a question like "is the key I hold
locally still a recipient here?" can be answered by comparing *keys*
rather than device names — the name is a label, and a label is not proof
of which key it refers to. Deriving the public key from the wrapped
identity file would need an unlock, so it is captured at the one moment
gage holds the key anyway. It is public, and already committed inside
the vault, so a local plaintext copy discloses nothing new.

`device` and `method` are this machine's answers to "who am I in that
vault, and how do I unlock it." They're per-vault because both can
legitimately differ between vaults on one machine — a laptop might be
`laptop-1` unlocking `personal` by passphrase and `work-laptop`
unlocking `work` by YubiKey. Both live here, in local config, rather
than in the vault's own committed `config.toml`: `device` is what maps
this machine to one of the vault's recipients, and `method` is an
identity concern that the vault has no business recording (see
"Decryption methods are per-device").

Type-specific metadata (`origin`, for `git`) lives in its own nested
table — `[vaults.<name>.<type>]` — rather than flat fields on
`[vaults.<name>]`, for the same reason as the type-specific command
namespacing above: it keeps "generic vault fields" and "this-type-only
fields" visibly separate, so a second vault type only ever adds a new
nested table, never touches the shape of an existing one.

Splitting config/data/state across the three XDG roots instead of one
flat directory buys two things a single root can't:

- **Dotfile managers already expect this shape.** `config.toml` is
  small, plaintext, and worth version-controlling alongside the rest of
  a user's dotfiles — tools like `chezmoi`/`yadm` sync `$XDG_CONFIG_HOME`
  by convention, and now they pick up exactly that file. Vaults (today,
  always git repos, synced by `gage` itself via their own remotes) and
  state (this device's own history and trust cache — explicitly *not*
  something to carry to a new machine, see "Local trust cache") land in
  `$XDG_DATA_HOME`/`$XDG_STATE_HOME` instead, so a dotfiles sync of
  `~/.config` can't accidentally sweep up either.
- **Windows gets a layout that looks native, not ported.**
  `%APPDATA%`/`%LOCALAPPDATA%` are where Windows users already expect a
  well-behaved CLI tool's files to live.

Within each root the substructure is unchanged in spirit from the old
single-root design: `$GAGE_DATA/vaults/` holds actual vaults (git repos,
today), `$GAGE_STATE/` holds local-only, never-synced device state (the
history file and, per-vault, `$GAGE_STATE/<vault-id>/known-config.toml` —
the trust cache used to detect unreviewed recipient changes, see "Local
trust cache"). Both per-vault roots — identities under `$GAGE_DATA` and
the trust cache under `$GAGE_STATE` — are keyed by the vault's `id`
rather than its local registration name, for the reason given under
"Local identity storage": a local name is chosen at clone time and can
be reused for a different vault.

### Local identity storage

Some methods need `gage` itself to persist private key material on this
device; others don't. `ssh`, `yubikey`, and `secure-enclave` methods never
give `gage` the private key at all — proving possession happens by asking
ssh-agent, touching hardware, or prompting the OS keychain, and the key
never leaves that external holder. `passphrase` and `age-key` methods are
different: there's no external holder, so `gage` has to keep something on
disk between invocations for the method's proof-of-possession step to have
anything to check against.

That something is a per-device, per-vault identity file:

```
$GAGE_DATA/identities/<vault-id>/<device>.age
```

— an age-encrypted file wrapping this device's X25519 private key (for
`passphrase`, wrapped via age's own scrypt passphrase recipient; for
`age-key`, the file *is* the raw key material, protected only by
filesystem permissions). `<device>` is the same identity name registered
via `gage identity add` and listed under `[[recipients]]` in
`.gage/config.toml`. `<vault-id>` is that vault's `[vault].id` — **not**
its local registration name, for reasons worth stating because the
obvious choice is wrong (see A20).

A vault's local name is chosen at clone time and tied to the remote by
nothing, so two unrelated vaults can be registered under one name in
sequence — and then share an identities directory. That produces two
failures: a second vault silently reusing the first's keypair, and
`vault remove` deleting a key belonging to a vault it is not removing.
The second is unrecoverable. Keying by an id that travels *inside* the
vault makes the collision unreachable: every clone of a vault agrees on
its id however it was named locally, and two vaults never share one.

Hashing the origin URL would not do: a URL has many spellings for one
remote, `git set-remote` legitimately changes it, and a local-only vault
has none. An id is assigned rather than derived, so it is stable by
construction.

For human findability — the design permits backing up an identity file
by hand — the directory carries a small plaintext marker naming the
vault. Nothing reads it to make a decision.

The directory is created `0700`, the file `0600`.

**Where the device name comes from.** `gage init` and `gage identity add`
both take an optional `--device NAME`. Omitted, it defaults to this
machine's hostname, normalized: lowercased, truncated at the first dot
(so `Andrews-MacBook-Pro.local` becomes `andrews-macbook-pro`), any
character outside `[a-z0-9._-]` replaced with `-`, runs collapsed, and
length-capped. If normalization leaves nothing usable, `gage` asks for a
name rather than inventing one. The chosen name is recorded in local
config under `[vaults.<name>].device` and, as a recipient label, in the
vault's committed `.gage/config.toml`.

A hostname default is convenient rather than private: it puts something
like `andrews-macbook-pro` into a plaintext file every recipient of the
vault can read. That's usually fine — the people who can read a vault
generally know whose devices are on it — but `--device` is there for
when it isn't, and shared vaults are exactly where it's worth using.

**Device names — and the vault id — are validated before they're ever
used as a path.** Both are filename components, and both arrive from
`.gage/config.toml` — a committed file that, per "Trust boundaries,"
anyone with git write access can edit. A recipient entry reading
`device = "../../../../etc/cron.d/x"` must be rejected on read, not
helpfully resolved. `gage` therefore validates every device name against
the same character allowlist above, on the way in *and* on the way out,
and refuses to construct a path from one that doesn't match. The vault
id gets the identical treatment against the UUID form: a config whose
`id` does not parse is refused rather than helpfully coerced. This is
the one place a vault's plaintext metadata reaches the local
filesystem, so both components get checked like the untrusted input
they are.

**This file always has exactly one recipient.** age refuses to encrypt to
a scrypt passphrase recipient combined with any other recipient, so a
passphrase-wrapped identity file is necessarily passphrase-only — there's
no "also let my other device open this" variant of it, by construction.
That's not a limitation to work around: the recovery story for a lost or
inaccessible identity file is registering a fresh identity and being
re-added as a recipient (see below), not sharing one device's wrapped
key with another.

This lives under `$GAGE_DATA`, not `$GAGE_STATE` or `$GAGE_CONFIG`, and
that placement is deliberate, not just "closest available root":

- **Not `$GAGE_STATE`.** That root is documented as local-only,
  *disposable* device state (history file, trust cache) — explicitly
  "not something to carry to a new machine." Losing the trust cache is a
  non-event; losing your only copy of a device's identity file is not.
  Filing it there would send the wrong signal about what's safe to lose.
- **Not `$GAGE_CONFIG`.** That root is explicitly meant to be swept up by
  dotfile managers (`chezmoi`/`yadm` sync `$XDG_CONFIG_HOME` by
  convention). A wrapped private key riding along in a dotfiles sync —
  often pushed to a git remote of its own, sometimes a public one — hands
  an attacker the same offline, unlimited-attempt brute-force target that
  keeping it *out* of the vault (below) was meant to avoid.
- **Not inside `$GAGE_DATA/vaults/<name>/`.** Nesting it inside a vault's
  own git-tracked tree risks it being `git add`ed by accident, and would
  put a passphrase-wrapped private key inside the very repo that syncs to
  remotes — reopening exactly the exposure keeping it device-local avoids:
  anyone with *read* access to the vault (not write access — read) could
  pull down the wrapped blob and brute-force the passphrase offline, at
  whatever speed their hardware allows, with no rate limiting. That would
  quietly weaken "can someone decrypt ciphertext that already exists" (see
  "Trust boundaries") from "airtight, doesn't depend on git access at all"
  down to "as strong as this passphrase, given unlimited guesses."
  `identities/` is therefore a sibling of `vaults/` under `$GAGE_DATA`,
  never a child of one.

**Losing this file is a recovery problem, not a backup problem.** `gage`
doesn't try to make this file un-losable, and deliberately doesn't offer
to sync or back it up itself — the fix for "this device's identity file
is gone" is the same fix as "this device is gone": generate a fresh
identity (`gage identity add`) and have any *other* recipient add it
(`gage recipient add`). This is why `gage init --recipient` and the
`config.toml` example both show room for a `recovery-paper-key` alongside
a device's own key — a vault that depends on exactly one identity file
surviving forever has no real recovery story, regardless of where that
file lives. A user is free to back up a device's wrapped identity file
themselves (it's already passphrase-protected ciphertext, so copying it
isn't unsafe), but that's a manual, user-owned choice, not something
`gage` automates.

---

## Session model: `gage` as its own agent

Instead of a background daemon with a socket, `gage` has **two invocation
modes**, and they're deliberately independent — nothing bridges them, so
there's no persistent listener to secure or forget about:

(`Session` here is also the name of the underlying library type — see
"Library architecture" above. What follows describes the CLI's REPL
rendering of it, but the same object, unlocked the same way, is what a
future GUI/TUI would hold too.)

**1. One-shot mode** — `gage show protonmail`. Unlocks the identity,
decrypts every entry once to resolve the title against what you typed,
shows the match, drops the key from memory, exits. Every invocation pays
both the unlock cost (passphrase prompt / YubiKey touch) *and* the full
decrypt-and-resolve cost, since there's no cache to reuse between
invocations — see "Addressing entries" below. Best for scripting, one-off
lookups, and composing with other unix tools. (`Vault.Unlock` and
`Identity.Close`, called once each — see "Library architecture" above.)

**2. Session mode** — running `gage` with no subcommand (or `gage shell`)
drops you into an interactive prompt, *provided stdin is a terminal*.
With stdin piped or redirected, a bare `gage` prints help instead of
starting a session: `--stdin` is already the explicit spelling for "read
session commands from stdin" (see "Non-interactive session mode" below),
and letting a bare `gage` silently mean the same thing would give one
behavior two spellings, with the implicit one being the surprising one
inside a script.

```
$ gage
gage> use personal
[personal] method: yubikey — touch your key...
[personal🔓] gage> show protonmail
correcthorsebatterystaple
[personal🔓] gage> show chase --qr
██████████████
██ ▄▄▄▄▄ █▀▄██
██ █   █ █▀▀██
██ █▄▄▄█ █▀ ▄██
██████████████
[personal🔓] gage> use work
[work] method: passphrase
Enter passphrase: ****
[personal🔓 work🔓] gage> status
  personal   unlocked  (last used 5s ago)
  work       unlocked  (last used 1s ago)
[personal🔓 work🔓] gage> lock personal
personal locked. run `use personal` to unlock again.
[personal🔒 work🔓] gage> exit
$
```

The unlocked identity for each vault is held **only in this process's
memory**, page-locked the moment `Vault.Unlock` produces it so it can't
be paged to swap — `mlock`/`munlock` on Linux and macOS,
`VirtualLock`/`VirtualUnlock` on Windows — and the process itself has
core dumps disabled at startup where the OS supports it
(`setrlimit(RLIMIT_CORE, 0)` on Linux/macOS). Windows has no direct
equivalent to a Unix core dump; `gage` does what a non-elevated process
can (suppressing the Windows Error Reporting crash dialog via
`SetErrorMode`) but can't fully guarantee no crash dump gets written the
way `RLIMIT_CORE` does on POSIX — a documented gap, not a claimed
guarantee it can't back. If page-locking itself fails (common under
restricted `ulimit -l`/container defaults, or a locked-down Windows
policy), `gage` warns once and proceeds without it rather than refusing
to unlock — the same "warn and proceed" posture as an unreachable network
in "Sync model": a weaker guarantee beats an unusable tool. There is
nothing to attach to from another terminal — that's the whole point. When
the process exits (`exit`, `quit`, Ctrl-D, or the process being killed),
the memory is zeroed and reclaimed by the OS; there is no cleanup step
that can be skipped or forgotten. This is the same `Identity`
`Vault.Unlock` returns in one-shot mode — `Session` just holds onto it
across calls instead of closing it after one; see "Library architecture"
above.

**Non-interactive session mode** exists too, for automation that wants the
same "unlock once, do several things, then gone" property without a human
at a TTY:

```
gage --script deploy-secrets.gage      # reads session commands from a file
cat commands.txt | gage --stdin        # same, from stdin
```

Both stop at the first failing command rather than running the rest
against a state the script's author never anticipated, and neither writes
to the history file — that file records what a human typed at a prompt.

**Where the passphrase comes from**, in this fixed order:

1. `GAGE_PASSPHRASE`, if set.
2. Otherwise a prompt — but only under `--script`, which leaves stdin
   free for a human to answer on. `--stdin` has already spent stdin on
   the command stream and can never prompt; a prompt there would race the
   script for the same bytes.
3. Otherwise a refusal naming the variable, with `exitcode.LockedOrAuth`.
   Never a read that cannot be answered, and never an empty passphrase
   reported as a wrong one.

`GAGE_PASSPHRASE` opens an *existing* identity only. It is never used for
`init` or `identity add`, where the answer protects a brand-new key and
so cannot be checked against anything — a typo in the variable would be
unrecoverable, and nobody would find out until the next unlock. It also
gets exactly one attempt, since the variable gives the same answer every
time. There is no per-vault spelling: a script driving two vaults
reassigns the variable between them.

### Session-only commands

```
use <vault>          switch/unlock the active vault for this session
lock [vault]         drop key material for one vault (or all, if omitted)
                     without exiting the process
status / whoami     list vaults touched this session and their lock state
help
exit / quit / ^D
```

Everything else in the command reference works inside a session too —
not just entry commands. The rule is short enough to state once:
**every command is available in a session except `init` and `clone`.**

- **Entry commands** (`show`, `cat`, `ls`, `insert`, `edit`, `rename`,
  `generate`, `search` (aka `grep`), `rm`, `mv`, `cp`, `reindex`) and the
  **sync family** (`sync`, `pull`, `push`, `log`, `history`) operate
  against the current session vault without needing `--use` each time.
- **Management commands** (`vault list/info/remove/set-default`,
  `identity add/list`, `recipient add/remove/list/verify`) work in a
  session as well. Adding a recipient right after you've unlocked is
  exactly when you'd want to, and making you exit the session first
  would be a poor edge for no benefit.
- **`gage git set-remote`** works in a session, but note it takes an
  explicit `<name>` argument rather than acting on the session's current
  vault — it's the one command in this list that isn't
  current-vault-scoped.
- **`init` and `clone` are one-shot only.** Both *create* a vault rather
  than operating on one, which leaves an unanswered question about
  whether the newly created vault should become the session's current
  vault. Invoked inside a session they report that plainly rather than
  failing as unknown commands. This is the conservative side of a
  reversible choice: making them session-available later is additive,
  taking it away would be breaking.

`--use NAME` still works ad hoc on any of the above against any vault
already `use`d this session (it'll prompt to unlock if you haven't
touched it yet).

### Help

There are two help surfaces, and they deliberately differ:

- **`gage --help` and `gage help`** (equivalent spellings — `gage help`
  is what most operators try first) list the commands available in
  one-shot mode. They never list `use`/`lock`/`status`/`exit`/`help`,
  which don't exist outside a session; listing them would point an
  operator at a path that can't work.
- **In-session `help`** lists the session-only commands above plus
  everything else available in a session, and presents vault selection
  as the bare `use <vault>` command rather than the `-u|--use NAME`
  flag. The flag still appears in per-command help (`help show`), since
  it does work ad hoc in a session.

`gage help <command>` and in-session `help <command>` both print one
command's usage. Both surfaces render from a single registry of command
metadata — name, aliases, description, group, and where the command is
available — so a command can't appear in one and go missing from the
other. That's an implementation detail, but a load-bearing one: two
hand-maintained lists would drift the first time a command is added.

**A usage rejection carries its command's usage, not just the bare
complaint.** `gage edit` with no query used to print only Cobra's own
`accepts 1 arg(s), received 0` and leave the operator to go dig up `gage
help edit` themselves. Any error that never opted into an explicit exit
code — a bad argument count, an unknown flag, an unrecognized (sub)command
— gets that command's usage block folded into the same message before
it's printed, one blank line below the complaint; an unrecognized
top-level command gets the full grouped listing instead, since there's no
single command's usage to show. This applies identically in one-shot mode
and inside a session (or a `--script`/`--stdin` run): both dispatch paths
route through the same helper, so they can't drift on when or how much
help is shown. A command's own error — wrong passphrase, entry not found,
a sync conflict — already opted into a specific exit code and message via
the exit-code taxonomy, and is left exactly as that command built it; only
Cobra's own parsing rejections gain the extra text.

### Addressing entries & the metadata index

With opaque UUID filenames, there's no more "path" to type. Instead,
`show`, `cat`, `edit`, `rename`, `rm`, `mv`, and `cp` all take a
**query** that's matched against decrypted `title`s. (`insert` and
`generate` take a plain `<title>` instead — they create an entry rather
than addressing an existing one, so there's nothing to resolve against.)

```
[personal🔓] gage> show protonmail
correcthorsebatterystaple
[personal🔓] gage> show aws
Multiple entries match "aws":
  1. AWS root account          (4b9d7710) — updated 2026-08-20
  2. AWS IAM backup admin       (a03e5f88) — updated 2026-01-14
Which one? [1-2]:
```

Resolution order: exact title match → substring match on title → exact
UUID → substring match on a UUID's canonical string form → ambiguous, so
list candidates and ask (one-shot mode fails with the same list instead
of prompting, since there's no one to ask). A stage that matches nothing
falls through to the next; a stage that matches more than one stops
there and lists *that stage's own* candidates, never spilling into a
later stage — an exact title match always wins over a substring match of
a different entry, even when the substring stage would itself have been
unique.

Title comes before UUID, not the other way around, for a concrete
reason rather than a preference: entries are addressed by title far more
often than by the UUID nobody has memorized, and a hex-spellable title —
`dead`, `beef`, `cafe`, even a single letter — is exactly the kind of
string an entry's own randomly-generated UUID can coincidentally
contain. An earlier version of this resolver tried UUID matches first,
which meant a query that was the *exact, literal title* of one entry
could silently resolve to a completely different entry instead, with no
error and no ambiguity warning, whenever the query happened to also be a
unique UUID substring elsewhere in the vault. Checking title first
closes that hole structurally: by the time UUID matching ever runs, no
entry's title matched the query at all, so a UUID match can never
pre-empt an exact or substring title hit.

At the library level this is one method that returns either a resolved
entry or a candidate list — never printed text. One-shot CLI treats a
non-empty candidate list as failure, session-mode CLI prompts with it, and
a future GUI would render it as a picker; see "Library architecture."

Making this fast is why `ls` used to be a "cheap, no-decrypt" command and
can't be anymore — the moment titles move inside the encrypted payload,
even *browsing* requires the key. The session process resolves this by
building an **in-memory metadata index** the first time `ls`/`show`/`search`
needs one per vault: it decrypts every entry once, pulls out just the
metadata fields (title, description, dates, `updated_by`), and keeps that
index in memory for the rest of the session, updating it incrementally as
you `insert`/`edit`/`rm`. This index dies with the process exactly like the
key does — it's never written to disk, so "nothing plaintext outlives the
process" still holds even though metadata is now something the tool has to
decrypt just to display a list. `gage reindex` forces a rebuild (useful
after a `git pull` run outside `gage`, or if you suspect staleness).

The index is also invalidated automatically by `gage`'s own syncing. The
fast-forward pull that happens on every vault unlock (see "Sync model"
below) can bring in entries written on another device, changing
`entries/` underneath a live session — so a successful pull that actually
moved HEAD rebuilds or invalidates that vault's index before the next
command reads it. A pull that brings in nothing leaves the index alone,
since rebuilding on every unlock would defeat the point of caching it.
Locking a vault (explicitly, or via the idle timeout) discards its index
along with its key: the index is decrypted metadata, so it has no business
outliving the identity that produced it.

One-shot mode gets no such cache — every invocation of `ls`/`search`/`show`
pays the full decrypt-every-entry cost from scratch. That's fine for a
handful of entries, but it's the real cost of hiding names: for a large
vault, prefer session mode for anything beyond a single known lookup.

### Why an idle timeout still matters despite process-scoped keys

Process lifetime is a clean boundary in theory, but terminal multiplexers
break the assumption that "process exits when you're done." A `gage`
session left running in a `tmux` pane over a weekend is functionally an
unmanaged agent again. `idle_timeout` re-locks a vault's key automatically
after inactivity — the process stays alive, but the prompt drops to
`🔒` and the next data-touching command re-prompts, same as if you'd run
`lock` yourself. This is a deliberate belt-and-suspenders addition, not a
contradiction of "process lifetime is the boundary" — it's there because
*wall-clock* idle time is a better proxy for "the user walked away" than
*process* liveness once multiplexers are in the picture.

### Trade-off vs. a shared agent (ssh-agent style)

This design explicitly gives up one convenience: with `ssh-agent`, unlocking
once lets *every* terminal tab share that unlocked key for its lifetime.
Here, each `gage` session is its own island — open a second terminal and
you touch the YubiKey / re-enter the passphrase again. That's a deliberate
trade: blast radius is one terminal's process, not "every shell you've
opened since boot," and there's no socket whose permissions you need to
reason about. If cross-terminal sharing becomes a real pain point later,
it can be added as an *opt-in* agent mode without changing the default.

### Concurrent processes and the vault lock

The trade-off just described has a consequence worth stating outright:
**multiple `gage` processes against the same vault is the normal case,
not an edge case.** A session sitting in a `tmux` pane while you run
`gage show` in another terminal is precisely the workflow this design
encourages, since there's no shared agent to route both through.

That collides with "every write is a commit." A write is a
read-modify-commit sequence against a single git working tree — decrypt,
modify, re-encrypt, stage, commit — and two of those interleaving can
produce a commit containing another process's half-written state, or lose
one of the two writes entirely. The dirty-working-tree reset described
under "Recipient / access management" makes it sharper still: run
concurrently, one process's cleanup would discard another's in-flight
work, which is exactly the "silently discard a just-rotated password"
outcome the sync model refuses to allow.

So `gage` takes a **per-vault advisory lock** — `flock` on Linux/macOS,
`LockFileEx` on Windows — held across each complete read-modify-commit
sequence, and released on every exit path including error paths:

- **Writes take it; reads don't.** Two concurrent `show`s never block
  each other. A write blocks other writes to the same vault, and only
  that vault — a session working in `personal` is unaffected by a write
  to `work`.
- **"The same vault" means the same `id`, not the same local name.** The
  lock file is `$GAGE_STATE/locks/<vault-id>.lock`, keyed like every
  other piece of per-vault local state (see "Local identity storage").
  One repository registered twice under two local names is one vault, so
  the two registrations contend for one lock rather than holding one
  each and both writing the same working tree. That is also why `mv`/`cp`
  refuse a destination whose id matches the source's, however the two
  are labelled.
- **A contended lock waits, with a message**, rather than failing
  immediately. Whoever holds it is nearly always about to finish; a
  one-line "waiting for another gage process" beats a spurious failure.
  It waits with a timeout rather than forever, since a wedged holder
  shouldn't hang a terminal indefinitely.
- **The lock is process-scoped, like everything else here.** It's
  released by the OS if a process is killed, so there's no stale lock
  file to clean up by hand — the same property that makes "process
  lifetime is the boundary" work for key material.
- **A re-encryption pass holds it for its whole run** — `recipient add`,
  which always re-encrypts, and `recipient remove --reencrypt` alike
  (see "Recipient / access management") — which on a large vault can be
  a while. That's the right
  trade: the operation's all-or-nothing guarantee is worth more than
  letting an unrelated write slip in beside it.
- **Cross-vault `mv`/`cp` take two locks**, one per vault, acquired in a
  deterministic order — sorted by vault id, the same thing the lock file
  is named for — so two simultaneous moves in opposite directions
  between the same pair can't deadlock.

This is about correctness between cooperating `gage` processes, not
security. It doesn't defend against anything hostile with write access to
the vault directory — that's the trust boundary discussed below, and a
lock file has nothing to say about it.

---

## Sync model: mostly automatic, deliberately not fully

This section describes how the `git` vault type syncs — the only type
that exists today (see "Vault types" above). A future vault type would
need its own sync policy, but the split below — between "always-local
durability" and "risky network operations get an actual policy" — is one
every type should preserve.

Sync is split into two operations with very different risk profiles, and
they're handled differently on purpose.

**Commit is local, instant, and always happens.** Per the original design
principle — every write is a commit — nothing is ever *only* in memory once
you save an edit. It's durable in the local git object store immediately,
sync or no sync. This part was never in question.

**Push/pull are network-dependent and can conflict**, so they get an actual
policy instead of "always" or "never":

- **On `use <vault>`** — entering a vault in session mode, or unlocking it
  for a one-shot command — gage does a `git fetch` + fast-forward-only pull
  automatically. This is the "catch me up" moment for sitting down at a
  fresh terminal. Fast-forward-only means it never silently merges anything:
  it either cleanly fast-forwards or it doesn't touch local state at all. If
  there's no network, gage doesn't block on it — it warns once and proceeds
  with the local copy, because refusing to show a password because you're
  offline is a worse failure mode than showing a possibly-stale one.
- **After every write** (`insert`, `edit`, `generate`, `rm`, `mv`, `cp`) —
  gage pushes immediately following the commit. When nothing's
  diverged, this is invisible: a normal write propagates within a second or
  two. If the push fails because you're offline, the commit still exists
  locally — nothing is lost, you just have unpushed commits, same as any
  ordinary git repo — and the next successful `use`/push retries it.
- **Real divergence is the one thing that never auto-resolves.** If another
  device has pushed commits your local copy doesn't have, the automatic
  push simply fails and reports it: `origin has diverged — 2 local commits
  pending, run 'gage sync'`. `.age` files are opaque binary blobs, so git
  can't three-way-merge two edits to the same encrypted entry the way it
  can merge text — and a naive auto-resolution (last-write-wins) could
  silently discard a just-rotated password with no indication it happened.
  That's a much worse outcome for a secrets manager than for, say, a notes
  app, so this case is kicked to a human on purpose.

`gage sync` is the explicit tool for that last case: it pulls both sides,
and for any entry that actually conflicts, decrypts and shows *both*
versions so you choose — gage never guesses on your behalf. `pull`, `push`,
and `git` remain as the manual override / scripting / CI control surface,
but day-to-day they're a fallback, not something to remember every session.

### What "diverged" actually means

"The entry conflicts" is only one of several things divergence can mean,
and they want different handling. Given local and remote commits sharing
a merge base:

| Situation | Git's view | What `gage` does |
|---|---|---|
| Both sides changed **different** entries | Merges cleanly — separate files | Nothing to ask. Merge and move on |
| Both sides changed **the same** entry | Conflict — `.age` is binary, no 3-way merge | Decrypt both, ask (below) |
| One side **deleted** an entry the other **edited** | Delete/modify conflict | Decrypt the surviving version, ask |
| Both sides changed the **recipient files** | *Would* merge cleanly — prevented by `.gitattributes` | Forced conflict, resolved through the trust-cache confirmation |
| Both sides inserted entries with the **same title** | Merges cleanly — different UUIDs | Not a conflict. Two real entries; the ambiguous-query resolver handles it |

The last row is deliberate. Detecting duplicate titles during sync would
mean decrypting every entry on both sides, forcing an unlock on every
sync — including the clean fast-forwards that currently need no identity
at all. The resolver already lists candidates and asks (see "Addressing
entries"), which is the same answer arrived at later, for free.

**Sync unlocks lazily.** A fast-forward, and a merge where the two sides
touched different entries, need no identity — nothing has to be
decrypted to complete them. `gage sync` only prompts for an unlock when
it reaches a conflict whose resolution requires showing you plaintext.
This is why the trust-cache check hooks `sync` directly rather than
riding on `Vault.Unlock` (see "Local trust cache"): a clean sync may
never unlock anything.

### Resolving an entry conflict

For each conflicting entry, `gage` decrypts both sides and shows their
metadata — who last wrote each, and when — then asks:

```
[personal🔓] gage> sync
Entry conflicts: "ProtonMail"

  local    updated 2026-08-30 14:22  by laptop-1
  remote   updated 2026-08-29 09:03  by phone

  [l] keep local     [r] keep remote
  [b] keep both      [s] skip this entry     [q] abort sync
Which? [l/r/b/s/q]:
```

**`keep both` is why this menu has three options rather than two.** It
writes the losing version as a *new* entry under a fresh UUID, so
resolving a conflict never destroys a secret — the two versions become
two entries with the same title, which the ambiguous-query resolver
already knows how to present, and which you can reconcile later at
leisure with `rm` or `rename`. Choosing wrong under time pressure is a
recoverable mistake rather than a silent loss, which is the right
default for a tool whose whole premise is not losing secrets.

`skip` leaves that entry conflicted and moves to the next; the sync ends
without pushing, since the tree still has unresolved conflicts. `abort`
restores the pre-sync state entirely.

**The result is a real merge commit**, with both sides as parents, the
same as any other git merge. Local commit granularity survives, both
devices' histories remain walkable by `history --decrypt`, and the vault
stays an ordinary git repository someone can `cd` into and inspect with
real git — which is exactly the property the "no passthrough" decision
elsewhere depends on. Rebasing local work onto the remote would give a
tidier line at the cost of re-prompting for the same entry once per
local commit that touched it; flattening to a single post-sync commit
would discard the per-write history a secrets manager may later want.

### Remote authentication: HTTPS and a token

`gage` speaks to remotes through go-git, never a `git` binary (see
"Git-specific commands"), and that has one consequence worth stating
plainly rather than discovering: **go-git does not read `~/.ssh/config`,
git credential helpers, or `insteadOf` rewrites.** SSH host aliases,
`IdentityFile`, `ProxyCommand` — none of it applies. A remote spelled
`git@internal:secrets/work-vault.git`, relying on a `Host internal` block
to resolve, simply won't connect.

So the supported path is **HTTPS with a token**:

```
gage auth login  [--host HOST]     # store a token for a git host
gage auth status [--host HOST]     # which hosts have a token, and whether it works
gage auth logout [--host HOST]     # forget it
```

This isn't a GitHub decision — it's an SSH one. Every limitation above
belongs to SSH transport; token-over-HTTPS has none of them and works
identically against GitHub, GitLab, Gitea, Bitbucket, and self-hosted
installs. Narrowing to one host would have solved the same problem by
giving up far more than the problem required.

- **Tokens live in `$GAGE_STATE/tokens/<host>`, `0600`.** That root is
  documented as local-only, disposable device state, which is exactly
  what a token is: re-acquirable at any time by logging in again, unlike
  an identity file. Deliberately *not* `$GAGE_CONFIG`, which dotfile
  managers sync by convention — the same reasoning that keeps identity
  files out of it.
- **Scope the token as narrowly as the host allows.** On GitHub that
  means a fine-grained PAT limited to the vault's repository, not a
  classic `repo`-scoped one that reaches every repository you own.
  `gage auth login` says so at the prompt.
- **You bring the token; `gage` never brokers one.** There's no OAuth
  device flow and no client ID shipped in the binary, and that's a
  deliberate choice rather than a missing feature. GitHub's OAuth App
  authorizations are scope-based — `repo` grants full control of *every*
  private repository you own, with no per-repository selection — so a
  device-flow login would acquire strictly broader access than the
  fine-grained PAT recommended just above. For a secrets manager,
  normalizing "grant this tool access to all your private repos" is the
  wrong trade for a smoother first run. It would also help exactly one
  host while every other host still pasted a token, reintroducing the
  host-specialness this whole section exists to avoid.
- **An expired token fails legibly.** PATs expire; when one does, `gage`
  says so and names `gage auth login`, rather than surfacing a bare 403
  from the transport layer.
- **`gage init --remote` solicits a token inline rather than sending you
  to `gage auth login` separately.** If the remote's host is an HTTP(S)
  one with no token stored yet, `init` asks for it at the same masked
  prompt `auth login` uses, before it attempts to publish — otherwise a
  fresh private remote would take three commands instead of one: `init`
  (failing for lack of a token), `auth login`, then a manual `push` to
  finish what `init` was asked to do. Leaving the prompt blank falls back
  to the anonymous attempt exactly as before; not every remote needs a
  token, and `init` has no way to know which case it's in without asking.
  A local path or an SSH remote is never prompted at all — neither one
  authenticates with a stored token in the first place.
- **SSH remotes still work when they're simple.** If a remote is
  SSH-spelled and ssh-agent has a usable key, `gage` will use it. What
  it won't do is interpret `~/.ssh/config` to figure out what the remote
  really means — so an SSH remote that depends on a host alias fails
  with a message saying exactly that, and pointing at the HTTPS
  equivalent. Best-effort, documented as such, in the same spirit as the
  Windows core-dump gap: a narrower guarantee stated honestly beats a
  broad one that doesn't hold.

---

## Trust boundaries

Two different questions get conflated when people ask "can someone read
this," and `gage`'s guarantees are very different for each one.

**1. Can someone decrypt ciphertext that already exists?** No — not without
a private key that unwraps one of the stanzas embedded in that specific
file at encryption time. This is airtight and doesn't depend on git access
at all. age bakes the recipient list into the file itself: a random
per-file content key encrypts the payload, and that content key is wrapped
once per recipient's public key at encryption time. There is no separate
access-control check at read time — the ciphertext bytes *are* the access
list.

**2. Can someone get added as a recipient for *future* writes?** This is a
much weaker guarantee. `.age-recipients` is a plain, unsigned,
git-committed text file. Anyone with git write access to the vault — even
someone holding no decryption key at all — can append a public key to it.
`gage` has no built-in way to distinguish "a device legitimately
registered via `gage identity add`" from "a line someone typed into a text
file," because nothing cryptographically binds the two. The next real
write trusts whatever's currently listed and quietly wraps the new content
key for that recipient too.

Removing per-directory recipients (see "Why no per-directory sharing"
above) already closed the sharper version of this problem — there's no
longer a way to gain access to *part* of a vault by manipulating where a
file sits, since there's only one recipient list and it's not tied to
location at all. What's left is the coarser, unavoidable version: **git
write access to a vault is, transitively, the ability to eventually get
added as a recipient of that vault.** That's not really a `gage`-specific
problem — it's true of any system where "who can commit" and "who can
grant access" aren't cryptographically separated — but it's worth stating
plainly rather than assuming the encryption alone covers it.

### Mitigations

- **Git-hosting access control (the baseline).** Don't grant write access
  to a vault to anyone who shouldn't eventually be able to read everything
  in it. This is external to `gage` — a property of GitHub/self-hosted
  permissions today, or whatever access control a future vault type's
  backing store offers — but since a vault is now the *entire* trust
  boundary, it's also the entire mitigation surface at this layer.
- **Change detection on the recipient list (a practical backstop).** See
  "Local trust cache" below — a concrete mechanism, not just a policy.
- **Signed recipient changes (optional, higher-assurance).** For methods
  that support signing (SSH keys, YubiKey PIV, Secure Enclave), `gage
  recipient add`/`remove` could write a detached signature alongside
  `.age-recipients`, and `gage` would refuse to honor a file whose latest
  change isn't validly signed by a previously trusted recipient. Real
  fix, not just a warning — but real added complexity, so worth reserving
  for genuinely high-stakes shared vaults rather than making it a default.

Nothing here changes guarantee #1: no already-encrypted entry becomes
readable through this vector, ever. What's at stake is only whether an
untrusted git-writer can add themselves to what gets encrypted *next* —
and now that the whole vault is one flat trust boundary, "don't give that
person write access to this vault" is most of the answer by itself.

### Local trust cache

The mechanism behind "change detection" above:

- **What's cached.** The first time a device successfully uses a vault,
  `gage` writes a verbatim copy of `.gage/config.toml` to
  `$GAGE_STATE/<vault-id>/known-config.toml`, plus the content hash of
  `.age-recipients` at that same moment, both local, uncommitted, never
  synced. `config.toml` is the file diffed and shown to the user (device
  names alongside pubkeys make for a legible warning; a bare `age1...`
  list doesn't); the `.age-recipients` hash exists because that's the file
  actually consulted at encryption time, and diffing `config.toml` alone
  would miss anyone who edits `.age-recipients` directly without touching
  `config.toml` to match. Together, "the cache" means: what recipients did
  I last see and approve, as of the last time I encrypted (or explicitly
  reviewed) here.
- **When it's checked.** Before any operation that encrypts
  (`insert`, `edit`, `generate`, `rename`, and `mv`/`cp --to-vault` against
  the *destination* vault's cache), `gage` compares the current committed
  `.gage/config.toml` and `.age-recipients` against the cached copies —
  this check blocks the write pending a `[y/N]` answer (or `--yes`). It
  also checks *opportunistically*, non-blockingly, in two places that
  don't otherwise trigger it: on every vault unlock — session `use` and a
  one-shot command's implicit unlock alike, the same hook "Sync model"'s
  auto fetch+pull uses, so a plain `gage show`/`ls` surfaces the warning
  too, not just whatever command happens to write next — and on `sync`
  explicitly, since a sync that fast-forwards cleanly with no conflicting
  entry never needs to unlock any identity at all and so wouldn't
  otherwise pass through the unlock hook. Either way you see the warning
  when you sit down (or run any command at all), not only at the moment
  you're about to write.
- **What a mismatch looks like — a plaintext diff, always.** `gage` never
  just says "recipients changed"; it shows exactly what changed, since
  the cached copy of `config.toml`:
  ```
  [personal🔓] gage> generate chase-checking
  ⚠ Recipients for this vault changed since you last encrypted here:

  --- known-config.toml (last confirmed 2026-08-14)
  +++ .gage/config.toml (current)
  @@
   [[recipients]]
   device = "yubikey-5c-nfc-1"
   pubkey = "age1yubikey1q..."
  +[[recipients]]
  +device = "unknown-device"
  +pubkey = "age1qz8x2..."

  .age-recipients matches config.toml. ✓ (gage recipient verify passes)

  1 recipient added. Proceed and trust this recipient list? [y/N]
  ```
  At the library level this is a typed value (the diff, plus whether
  `.age-recipients` matches `config.toml`) that `cmd/gage` renders as the
  terminal prompt above — see "Library architecture."
- **Resolving it — two different outcomes depending on `verify`.**
  - **`.age-recipients` matches `config.toml`** (the ordinary case: a
    legitimate `gage recipient add` from another device, correctly
    touching both files). Confirming (`y`, or `--yes` for scripting/CI)
    regenerates *both* halves of the local cache — the `config.toml` copy
    and the `.age-recipients` hash — to the current committed state, and
    proceeds with the operation. From that point on, nothing warns again
    until the committed files change again: one acknowledgment per actual
    change, not per command.
  - **`.age-recipients` does *not* match `config.toml`** (someone edited
    the encryption-time file directly, or the two have otherwise
    diverged). This is shown as its own, more severe line, and confirming
    past it does *not* silently regenerate a clean cache the same way —
    `gage` still records that the mismatch itself was seen, but keeps
    flagging `.age-recipients`/`config.toml` as inconsistent (i.e.
    `gage recipient verify` keeps failing) on every subsequent check
    until someone actually fixes it, typically by re-running `gage
    recipient add`/`remove` properly so both files agree again. The two
    outcomes shouldn't collapse into the same "click yes, quiet forever"
    path, because one is routine and the other is exactly the tampering
    scenario this cache exists to catch.
  - Declining either prompt aborts the write entirely; the cache stays at
    its old value, so the same warning reappears next time.
- **Why local state is trustworthy here.** This cache lives only on a
  device that already holds decrypt access to the vault (or is about to
  gain it) — it's not protecting against a compromised local machine,
  which is a different, more severe threat model where the game is
  already lost. It's protecting against a remote git-writer trying to
  inject themselves quietly, and a local record of "what I last saw and
  approved" is exactly what's needed to catch that.

---

## Command reference

Every command below is `cmd/gage`'s rendering of a `Vault`/`Session`
library method (see "Library architecture") — flags become method
parameters, and prompts become terminal renderings of the structured
results those methods return.

### Vault lifecycle

```
gage init <name> [--dir PATH] [--remote URL] [--type git]
                  [--method passphrase] [--device NAME]
                  [--recipient PUBKEY ...]

    Creates a new vault: for the git type (the only one today), this means
    git init, writes .gage/config.toml with type = "git" plus the chosen
    default method, generates or registers the first identity using that
    same method for this device (writing its wrapped
    identity file for passphrase/age-key methods — see "Local identity
    storage" above), and commits the initial (empty) structure. --recipient
    can be repeated to add extra
    recipients (e.g. a recovery key) at creation time. --method gets the
    same treatment as --type: it defaults to (and, today, can only be)
    passphrase, validated against a single-value allowlist, so the
    additional methods named elsewhere in this document — age-key, ssh,
    yubikey, secure-enclave, plugin:<name> — arrive later as new accepted
    values rather than as a flag introduced for the first time on configs
    and scripts that predate it. Without --dir, the
    vault is created at $GAGE_DATA/vaults/<name> and registered under that
    path in the global config. --type defaults to (and, today, can only be)
    git; it's accepted now, validated against a single-value allowlist, so
    a future second type is additive to the CLI rather than introducing
    the flag for the first time. --remote is git-type-specific — a vault
    can be created local-only and gain a remote later via `gage git
    set-remote` (see "Git-specific commands" below). Given --remote, init
    publishes the vault's first commit immediately; if the remote's host
    needs a token and none is stored yet, init asks for one at the same
    prompt `gage auth login` uses before it tries to publish (see "Remote
    authentication" above) — leaving that prompt blank tries the push
    anonymously, same as if --remote had pointed at a public repository.

gage clone <remote-url> [--name NAME] [--dir PATH]

    Clones an existing vault: for the git type, a git clone, followed by
    reading .gage/config.toml to learn the vault's default method, which
    is what a subsequent `gage identity add` will suggest for this
    device. Without --dir,
    clones to $GAGE_DATA/vaults/<name> — same default as `init` — inferring
    <name> from the remote URL unless --name overrides it. Does NOT grant
    you access — if your device isn't already a recipient, gage tells you
    to run `gage identity add` to generate your public key, then get it
    added via `gage recipient add` from a device that already has access.

gage vault list
gage vault remove <name>             # forgets locally; does not delete the underlying store
gage vault info [<name>]             # type, method, recipient count, and type-specific
                                      # detail (for git: remote, dirty/clean)
gage vault set-default <name>        # changes `current` in global config
```

`use` is the one verb for "operate against this vault," in both modes:

- **Session mode:** `use <name>` as its own line, switching what subsequent
  commands in this `gage>` process target.
- **One-shot mode:** `-u|--use NAME` as a flag on any single command.

Previously these were two different words (`repo use` vs. a `--repo` flag)
for the same underlying action, which was needless vocabulary to learn.
Now it's one concept — select which vault this command or session talks to
— spelled the same way everywhere. `vault set-default` is kept as a
separate, deliberately different-sounding command because it does something
meaningfully different: it doesn't select a vault for the current action, it
changes what "no `--use` given" falls back to, persistently, in the global
config. Conflating "use this now" with "make this the default forever"
under one verb would be confusing in the other direction.

### Identity (how *this device* proves it can decrypt)

```
gage identity add --use NAME [--method passphrase] [--device NAME]
                   [--key-path PATH]

    Registers how *this device* holds its private key, and prints the
    resulting public key so it can be added as a recipient (either by
    you, if you're bootstrapping, or by an existing recipient). --method
    is this device's own choice, not the vault's (see "Decryption methods
    are per-device"); omitted, it takes the vault's [method].default.
    Same single-value allowlist as `gage init` — today, passphrase.
    --device names this device; omitted, it defaults to the normalized
    hostname (see "Local identity storage"). A name already registered as
    a recipient of this vault is rejected rather than silently taken
    over. For
    passphrase/age-key methods this also writes the device's wrapped
    identity file to $GAGE_DATA/identities/<vault>/<device>.age (see "Local
    identity storage"); ssh/yubikey/secure-enclave write nothing here,
    since the private key never leaves external hardware, an agent, or the
    OS keychain.

gage identity list [--use NAME]
```

### Recipient / access management

```
gage recipient add <pubkey-or-name> [--use NAME]
gage recipient remove <pubkey-or-name> [--use NAME] --reencrypt
gage recipient list [--use NAME]
gage recipient verify [--use NAME]

    Adding a recipient ALWAYS decrypts and re-writes every entry in the
    vault, so the new recipient can read everything in it — there is no
    flag, and no way, to add a recipient who can read only part of a
    vault. See "Why adding a recipient always re-encrypts" below.
    Removing a recipient REQUIRES --reencrypt (gage refuses to silently
    leave old ciphertext readable by a removed party) and prints a clear
    warning that this revokes future access only — anything already read
    can't be unread.

    Because adding grants access to the whole vault, it can only be done
    by someone who already has access to the whole vault. An actor who
    cannot decrypt every entry is refused before the vault lock is taken
    and before any confirmation is shown, with an error naming how many
    entries it cannot read — never a bare decryption failure on an
    opaque entry UUID partway through a write. The one exception is a
    dirty working tree, where the check would be reading ciphertext the
    reset below is about to discard: there the refusal is deferred until
    just after that reset, so an interrupted re-encryption cannot be
    misreported as lost access. It still lands before anything is
    written and before any confirmation.

    Re-encryption is all-or-nothing: every entry is re-encrypted in the
    working tree first, and the recipient-list files
    (.age-recipients/config.toml) and every touched entry land in exactly
    one commit together — nothing commits until all of it succeeds. If
    the process is interrupted partway (crash, kill, power loss), HEAD is
    untouched; the vault is exactly as it was before the command ran, and
    re-running --reencrypt picks up cleanly from scratch. gage also
    refuses to start any write against a dirty working tree it didn't just
    create itself — the likeliest thing to leave one is an interrupted
    --reencrypt, so gage warns once (naming the untracked/modified paths
    it found) and resets the whole tree to HEAD, discarding untracked
    files along with modified ones, before the new operation proceeds,
    rather than either silently discarding whatever it found or risking an
    unrelated write folding a stale partial reencrypt into its own commit.
    The reset covers the whole tree rather than only entries/ because a
    crash in the window after --reencrypt writes the recipient files but
    before it commits dirties those two as well, and a reset that skipped
    them would leave exactly the half-migrated state --reencrypt exists to
    rule out. The warning, not the reset itself, is what's load-
    bearing here: the interrupted-reencrypt assumption is strong but not
    provable in general, so a change that's actually a hand-edit gage
    didn't cause still gets surfaced, even though gage doesn't stop to ask
    before proceeding — the same "warn and proceed" posture as an
    unreachable network in "Sync model."

    `verify` checks that .age-recipients and .gage/config.toml's
    [[recipients]] list the same public keys. Needs no unlock — both files
    are plaintext — so it works before any identity is available and is
    safe to run in CI. Exits 0 and prints "in sync" on a match; exits 1
    and lists the specific differences otherwise (present in one file,
    missing from the other). This is the same comparison the local trust
    cache runs automatically before every encrypt (see "Local trust
    cache") — `verify` just exposes it as something you can run any time.
```

#### Why adding a recipient always re-encrypts

An earlier version of this design made re-encryption opt-in on `add`, so
a recipient could be admitted for future writes only. That produced a
recipient who could read entries written after their admission but not
before — and that state is wrong in three compounding ways.

**It contradicts principle 1.** "A vault is the unit of trust. Each
vault has its own set of recipients, and that list — nothing
finer-grained — is who can read it." A partially-readable recipient *is*
the finer-grained tier that principle rules out. The answer this design
gives to "these people should see less" is a second vault and
`mv --to-vault` (see "Why no per-directory sharing"), not a
half-admitted recipient of one vault. Partial access was never a tier
anyone chose; it was `age` baking recipients into each file at
encryption time, leaking into the user-facing model.

**It is undiagnosable from the interface.** The new device runs `ls`,
sees every entry, and gets decryption failures on what looks like an
arbitrary subset. The dividing line — written before or after
admission — is not the title, not the age, not anything `ls` displays.

**It is contagious and unrepairable, which is what settles it.**
Re-encryption decrypts every entry with the acting identity and fails on
the first one it cannot read. So a partially-admitted device cannot
repair its own access *and cannot grant full access to anyone else*: its
re-encryption pass dies partway through, after the trust-cache prompt
and inside the write lock, naming an opaque entry UUID. Partial access
therefore propagates to every recipient admitted by a partial recipient,
each generation harder to diagnose than the last. A default able to
quietly produce that is the wrong default, and an opt-out flag would
keep the vector open.

The cost is real and accepted: every `recipient add` rewrites every
entry, which grows the repository over time and puts a
no-plaintext-change revision into each entry's history for
`history --decrypt` to walk. Vaults are small, the operation is rare,
and the all-or-nothing machinery already exists — whereas the
alternative is an access model users cannot predict.

`remove` keeps its flag, and keeps it mandatory, because it means
something different there: not "grant access to history" but "stop
handing a removed party readable copies," which the next bullet in "A
few decisions worth calling out" explains.

### Entry CRUD

```
gage ls [--use NAME] [-H|--header]          # list entries: title, short id, created,
                                              # updated, updated_by (requires unlock —
                                              # see below). Default: unlabelled,
                                              # positional rows (greppable). -H/--header:
                                              # a labelled, |-delimited table instead.
gage search <pattern> [--use NAME]          # matches title/description/body
                                              # (decrypts in bulk if no index
                                              # cached yet; alias: gage grep)

gage insert <title> [--use NAME] [--description TEXT]
             [-m|--multiline | --value-stdin | -e|--edit] [--field NAME=VALUE]...
             [-f|--force] [--yes]
gage edit <query>   [--use NAME]            # decrypt to a scratch file, $EDITOR,
                                              # re-encrypt, commit (re-stamps
                                              # updated/updated_by)
gage rename <query> <new-title> [--use NAME]  # quick metadata-only edit, no $EDITOR
gage generate <title> [--use NAME] [-l LENGTH] [--no-symbols]
                       [-f|--force] [-c|--clip] [-q|--qr] [--yes]

gage show <query> [--use NAME] [-c|--clip] [-q|--qr] [--field NAME]
gage cat  <query> [--use NAME]              # always full raw plaintext, for scripting/piping —
                                              # display-only: short id after title, labels column-aligned

gage rm <query> [--use NAME]
gage mv <query> --to-vault <name> [--use NAME] [--yes]   # decrypt here, re-encrypt + commit
                                                  # there, remove from here — this
                                                  # is the sharing mechanism
gage cp <query> --to-vault <name> [--use NAME] [--yes]   # same, but keeps the original too
```

`--yes` bypasses the recipient-change confirmation prompt (see "Local
trust cache") for scripting/CI; interactively it's never needed since
`gage` just asks.

Notes on `insert`:
- Exactly one of `-m`, `--value-stdin`, `-e` may be given; with none of
  them, `gage` prompts for `value` once, masked, the same way it prompts
  for a passphrase. `-m` reads multiple lines straight from the terminal
  until EOF, no external process. `--value-stdin` reads `value` verbatim
  from stdin (one trailing newline trimmed) — deliberately not named
  `--stdin`, which is already the *session*-level flag for piping whole
  command lines into non-interactive mode (see "Session model"); the two
  would otherwise fight over the same stream inside a scripted session.
  `-e`/`--edit` opens the full entry as YAML in `$EDITOR`, seeded with
  `title`/`description` already filled in and empty `value`/`fields` —
  the same scratch-file round trip `gage edit` uses (see below), just
  seeded with a stub instead of a decrypted entry. It's the only `insert`
  mode
  that can set `fields` at creation time; every other mode leaves
  `fields` empty until a follow-up `gage edit`.
- `-e`/`--edit` aborts the insert (no entry written, no commit) if the
  file comes back unchanged, or with `value` still empty and `fields`
  still empty — the same "empty message aborts the commit" convention
  `git commit` uses, so quitting the editor without really saving
  anything can't silently create a blank secret.
- The template lets `title` itself be edited, not just `description`/
  `value`/`fields`; the duplicate-title check (and whether `-f` is
  required) runs against whatever title is in the file when it's saved,
  not the `<title>` argument the command was invoked with.
- `--field NAME=VALUE` (repeatable) is the non-interactive way to set
  `fields` at creation time, alongside whichever of `-m`/`--value-stdin`/
  default-prompt sets `value` — the two are independent, since those
  modes only ever touch `value`. Each `NAME=VALUE` splits on the *first*
  `=` only, so a value may itself contain `=`. Every whitespace character
  in `NAME` — leading, trailing, or interior — is removed, so a stray
  space can't create a key that `show --field` can't visibly be asked
  for; `VALUE` is kept verbatim and may be empty. Validated before any
  I/O/unlock, the same posture as the `-m`/`--value-stdin`/`-e`
  exclusivity check above: a missing `=`, a `NAME` that's empty once
  stripped, or the same stripped `NAME` given across two `--field` flags
  is a usage error — no silent last-wins. `Insert`'s duplicate-*title*
  guard is the precedent for erroring rather than overwriting, but
  unlike a duplicate title there's no `-f` override: a map can't hold
  both values. Mutually exclusive with `-e`/`--edit`, since that's the
  other way to set `fields` and combining them is ambiguous about which
  wins.

Notes on `show`:
- Default (no flag): prints the `value` field only — not `title`,
  `description`, `fields`, or the timestamps. Just the one value you
  almost certainly want, so it composes cleanly with pipes/paste. `gage
  cat` is the command that dumps the full decrypted YAML, metadata
  included, when you actually want everything.
- `--field NAME`: prints one entry from `fields` instead of `value`
  (e.g. `--field username`, `--field totp_seed`).
- `-c`: copies to clipboard, auto-clears after a short timeout
  (`[shell].clipboard_timeout`, 45s by default). The clear always happens
  inside the process that wrote the clipboard — never a forked child,
  which would be exactly the surviving background process principle 5
  refuses — so the two modes differ in who waits: a one-shot `gage show
  -c` blocks until the timeout with its notice on stderr (Ctrl-C clears
  early and still exits 0), while in a session the prompt returns
  immediately and the clear runs on a timer, with session exit clearing
  anything still pending. The clear is skipped if the clipboard changed
  after `gage` wrote it — compared by hash, so a pending clear is not a
  second copy of the secret in memory. A process killed outright never
  reaches its clear; that is a documented limit, like the Windows
  core-dump gap above.
- `-q`: renders a terminal QR code **instead of** printing plaintext — this
  is the scan-with-Camera-app workflow, now built into the tool instead of
  being a manual `-q`-then-decode dance.
- `--field NAME` with `-q`: QR-encodes *only* that field, not the whole
  entry — important so a structured note (say, an SSH private key plus
  metadata) doesn't get dumped whole into a QR code when you only wanted
  the TOTP seed.

### Sync (vault-generic, git-implemented today)

Push and fast-forward-only pull happen automatically around `use` and
writes (see "Sync model" above) — these commands are the manual override:
forcing a check, resolving a real divergence, or scripting/CI use. They
stay top-level, undecorated `gage` verbs rather than living under `git`,
because every vault type needs some notion of "catch up," "publish my
changes," and "show history," even though today's one vault type happens
to implement all three via git:

```
gage sync [--use NAME]          # pull, surface + resolve any conflicts, then push
gage pull [--use NAME]
gage push [--use NAME]

gage log [QUERY] [--use NAME]         # commit history for one entry (resolved via query);
                                       # timestamps only — filenames alone reveal nothing
gage history --decrypt <query> [--use NAME]
                                       # walks history for one entry and decrypts each
                                       # revision to show a diff. Explicit subcommand,
                                       # not a flag on `log`, because it's meaningfully
                                       # more dangerous (surfaces old secret values).
```

### Git-specific commands

Everything here only exists because the current (and only) vault type is
`git` — none of it has an obvious equivalent under a different backing
store, which is exactly why it's namespaced under `git` instead of
`vault`: these are the commands that would need reworking, or would simply
disappear, if a second vault type showed up. Today that's these:

```
gage git set-remote <name> <url>     # sets/changes the git remote (origin) — see below

gage auth login  [--host HOST]       # store a token for a git host — see
gage auth status [--host HOST]       # "Remote authentication" above
gage auth logout [--host HOST]
```

`auth` is namespaced here rather than at the top level because a token
for a git host is exactly the kind of thing that has no meaning under a
different backing store — a future type would authenticate its own way,
or not at all. It's also per-*host*, not per-vault: two vaults on the
same host share one token, and `--host` defaults to the host of the
current vault's `origin`.

`--remote` on `init` is optional — a vault can start local-only (no sync
until you're ready) and gain a remote later, e.g. after creating an empty
repo on GitHub. Giving it at `init` time publishes right away and, if the
host needs a token gage doesn't have yet, prompts for one in the same
breath (see "Remote authentication" above) — `set-remote` never pushes
and so never prompts for one either; it only repoints `origin`, and the
next sync is what actually needs a token. `gage git set-remote` is the
command for that: it sets
`origin` on the actual git repo (equivalent to `git remote add/set-url
origin <url>`) *and* updates `vaults.<name>.git.origin` in the global
config in the same step, so the two never drift apart. Doing this through
`gage` rather than a raw `git remote add` matters specifically because it
also has to touch gage's own global config — a plain passthrough would
set git's remote without gage's config ever finding out, leaving `vault
info` reporting a stale or missing remote for a vault that actually has
one.

**There's deliberately no generic `gage git -- <args...>` passthrough.**
`gage` never shells out to a `git` binary for anything — every command
above, `set-remote` included, goes through `go-git`. For git operations
`gage` doesn't model at all (branch/tag management, `gc`, `fsck`, reflog
inspection, and the rest of git's long tail), the vault is just a normal
git repository sitting at a well-known path (`gage vault info` reports
it) — `cd` there and use the real `git` CLI directly, with its actual
help text and actual tab completion, rather than whatever would leak
through a forwarding layer. This also means a second vault type never
has to reckon with "what does an arbitrary-passthrough command even mean
here" — by construction, nothing would replace it.

---

## A few decisions worth calling out

- **The library has no idea it's being driven by a terminal.**
  `Vault`/`Session` methods return values, errors, or structured requests
  for a decision (unlock, confirm, disambiguate) — never printed text or a
  stdin read. `cmd/gage` owns all of that. This is what keeps a future
  GUI/TUI from needing to rework any of the above — it's a second thin
  layer over the same calls, not a fork of the logic. See "Library
  architecture."
- **`vault` replaces `repo` in the vocabulary; a `type` field says how
  it's actually stored.** Every command that used to say `--repo`/`repo
  <verb>` now says `--use`/`vault <verb>`, and each vault's config records
  `type = "git"` alongside its method and recipients. Today `git` is the
  only type, so on its own this looks like renaming for its own sake —
  but paired with the vault-generic/git-specific split in the command
  reference (see "Vault types" and "Git-specific commands"), it's what
  lets a future backing store slot in without touching entry CRUD,
  identity, recipients, or the session model at all.
- **Multiple `gage` processes are expected, so writes take a per-vault
  lock.** Having no shared agent means a session in one pane and a
  one-shot command in another are the normal case, not an edge — and two
  read-modify-commit sequences interleaving against one git working tree
  could lose a write or commit half of one. A per-vault advisory lock
  (`flock`/`LockFileEx`) serializes writers while leaving readers
  unblocked, waits with a message instead of failing when contended, and
  is released by the OS on process death like everything else here. It's
  a correctness mechanism between cooperating processes, not a security
  one. See "Concurrent processes and the vault lock."
- **No daemon means no IPC attack surface.** There's no unix socket whose
  permissions need auditing, no risk of a stale agent process outliving
  the terminal that spawned it and being forgotten about. The trust
  boundary collapses to "is this specific process still alive," which the
  OS already enforces.
- **One verb, `use`, for vault selection everywhere.** Session mode and
  one-shot mode used to have different vocabulary for the same action
  (`use <name>` vs. a `--repo NAME` flag); they now both say `use`, as a
  bare session command or a `-u|--use NAME` flag. `vault set-default`
  stays a distinct command since "pick a vault for this default" is a
  different, persistent action, not the same thing spelled differently.
- **`config.toml` and `.age-recipients` are split by access pattern, not
  just by convention.** `config.toml` (method/device metadata) is read
  rarely and changes almost never. `.age-recipients` is read on every
  encrypt and changes comparatively often, and it's a plain, flat,
  one-key-per-line file — that's what keeps it usable directly by stock
  `age`/`passage`, with no per-vault parsing beyond splitting lines.
- **`identity` vs `recipient` stay separate.** `recipient` is "who can
  decrypt this vault" (public keys, vault-side, committed to git).
  `identity` is "how does *this device* prove it's one of those
  recipients" (private-key handling, device-side, never committed). A
  vault might have a laptop identity via NFC/USB touch and a
  phone identity via the Secure Enclave — same recipient list, different
  local mechanics. This is why the decryption *method* is a per-device
  choice rather than a vault-wide setting: it's an identity concern, so
  it's recorded on the device and never committed. The vault's
  `[method].default` only says what to suggest to the next device that
  joins. See "Decryption methods are per-device."
- **Local identity files live under `$GAGE_DATA`, never inside a vault.**
  `passphrase`/`age-key` methods need `gage` to persist a wrapped private
  key somewhere on this device (`ssh`/`yubikey`/`secure-enclave` don't —
  the key never leaves external hardware, an agent, or a keychain). That
  file is `$GAGE_DATA/identities/<vault>/<device>.age`, a sibling of
  `$GAGE_DATA/vaults/`, not a child of it — putting it inside a vault's
  git tree would let anyone with *read* access to the vault attempt an
  offline brute-force of the passphrase, a strictly worse guarantee than
  entry ciphertext already has. Losing the file is a recovery problem
  (register a fresh identity, get re-added by another recipient), not a
  backup problem — `gage` doesn't sync or back it up itself. See "Local
  identity storage."
- **Memory page-locking is cross-platform, and Windows' crash-dump story
  is honestly weaker than POSIX's.** `mlock`/`munlock` (Linux, macOS) and
  `VirtualLock`/`VirtualUnlock` (Windows) protect the same thing — an
  unlocked `Identity`'s key bytes never getting paged to swap — through
  platform-appropriate syscalls behind one interface. Disabling core
  dumps (`RLIMIT_CORE`) has no real Windows equivalent a non-elevated
  process can rely on; `gage` suppresses the Windows Error Reporting
  crash dialog but documents this as a narrower guarantee rather than
  pretending parity with POSIX. If page-locking itself fails (restricted
  `ulimit -l`, a locked-down container or Windows policy), `gage` warns
  once and proceeds unlocked-but-unprotected rather than refusing to
  work — the same "warn and proceed" posture as an unreachable network in
  "Sync model."
- **The `$EDITOR` scratch file prefers real tmpfs, but doesn't pretend to
  have it everywhere.** Linux gets the strong guarantee — a verified
  tmpfs-backed directory (`$XDG_RUNTIME_DIR`, falling back to
  `/dev/shm`; checked via `statfs`, not assumed from the path), so
  plaintext never touches persistent storage during an edit. macOS has
  no built-in equivalent; a real RAM-backed volume is possible there
  (`hdiutil attach -nomount ram://`) but was deliberately ruled out — it
  shells out to `hdiutil`/`diskutil`, leaves a visible mounted volume
  while active, and needs its own crash-recovery story if `gage` is
  killed mid-edit. Windows has no equivalent at all without a
  third-party driver. Both fall back instead to a tightly-permissioned
  (`0600`, exclusively created) file in the OS's standard temp
  directory, best-effort overwritten before deletion — and `gage` says
  plainly that this is *not* a hard no-disk-touch guarantee there, since
  SSD wear-leveling and copy-on-write filesystems (APFS included) mean
  overwrite-before-delete can't reliably erase the original blocks
  regardless of effort. Same posture as Windows' weaker core-dump
  guarantee above: a documented gap, not a claimed guarantee the
  platform can't back.
- **Session vaults are re-lockable without killing the process.** `lock
  <vault>` (or the idle timeout) drops that vault's key from memory while
  leaving other unlocked vaults and the shell itself intact — useful when
  you're about to step away but want to keep working in a different vault,
  or hand the terminal to someone else without exiting entirely.
- **Command history must never contain plaintext.** The session's
  readline-style history should log `show protonmail`, not the decrypted
  value that was printed — an easy leak vector to overlook once you have a
  REPL with recallable history. Titles typed as *queries* are fine to log
  (you typed them yourself); it's only the decrypted `value`/`fields`
  values that must never land in the history file.
- **`insert`'s value-input modes are mutually exclusive, and the
  per-value stdin flag isn't named `--stdin`.** The global `--stdin` flag
  already means "read whole command lines from stdin for non-interactive
  session mode" (see "Session model"); reusing that name for "read this
  one value from stdin" on `insert` would mean the two senses fight over
  the same stream inside a scripted session. The per-command flag is
  `--value-stdin` instead, and it's mutually exclusive with `-m` and
  `-e`/`--edit` — `gage` rejects more than one being set before doing any
  I/O.
- **Adding a recipient always re-encrypts; there is no flag.** A
  recipient who can read entries written after their admission but not
  before is the finer-grained access tier principle 1 rules out — and
  worse, the state is contagious: a partially-admitted device cannot
  repair itself or grant full access to anyone else, because its own
  re-encryption pass dies on the first entry it cannot read. An opt-out
  flag would keep that vector open, so there isn't one. The cost — every
  add rewrites every entry — is accepted. See "Why adding a recipient
  always re-encrypts."
- **`--reencrypt` is mandatory, not default-on, for recipient removal.**
  It survives on `remove` alone, and means something different there:
  not "grant access to history" but "stop handing a removed party
  readable copies." Silently leaving stale ciphertext readable by a
  removed recipient is a worse failure mode than forcing the user to
  explicitly opt into the (slower) re-encryption pass. It stays explicit
  rather than implicit because removal, unlike addition, has a genuinely
  destructive edge the operator should have to name.
- **`--reencrypt` is all-or-nothing, on purpose, not incrementally
  committed.** Re-encrypting hundreds of entries one commit at a time
  (with a resumable progress marker) would handle huge vaults more
  gracefully, but it means a genuinely inconsistent state — recipient
  list says one thing, some entries' actual ciphertext says another —
  could sit committed, and potentially pushed to a remote and pulled by
  another device, until someone notices and resumes it. Staging every
  entry in the working tree and committing exactly once, alongside the
  recipient-list files, means that window never exists in durable
  history at all: a crash mid-`--reencrypt` leaves HEAD untouched rather
  than half-migrated. This is the same shape of problem the local trust
  cache exists to catch (see "Local trust cache") — declared recipients
  and actual ciphertext recipients disagreeing — just self-inflicted
  instead of caused by a remote git-writer, so it gets the same
  "never let it become committed truth" treatment.
- **The recipient files are marked unmergeable, and that's load-bearing.**
  `.age-recipients` is one key per line, so two devices each adding a
  different recipient would merge cleanly into a union neither of them
  wrote — with no unreviewed *change* for the trust cache to catch,
  because the merge manufactured it. A `.gitattributes` marking that
  file and `config.toml` as `-merge` forces the conflict instead. See
  "On-disk layout."
- **Conflict resolution keeps both versions when asked, and that's the
  interesting option.** `keep both` writes the losing side as a new
  entry under a fresh UUID, turning "I picked wrong under pressure" from
  a lost secret into a duplicate title the resolver already handles. For
  a tool whose premise is not losing secrets, non-destructive should be
  one keystroke away. See "Resolving an entry conflict."
- **Sync is automatic except where auto-resolving would be dangerous.**
  Fast-forward pulls on `use` and pushes after every write happen without
  being asked, so `pull`/`push` aren't something to remember day-to-day.
  The one case that stays manual — a real divergence between two devices —
  does so because `.age` files can't be three-way-merged like text, and
  guessing a winner risks silently discarding a secret with no trace.
- **Filenames are opaque UUIDs; metadata lives inside the ciphertext.**
  This trades away the one thing `pass`/`passage` leave exposed — that
  someone with vault read access (but no key) can see your entry names and
  folder structure even if they can't read the values. Here they see
  nothing but random filenames. The cost is that `ls` and `search` are no
  longer distinct tiers — both now require the key, since even browsing
  titles means decrypting.
- **The metadata index is in-memory and session-scoped, not written to
  disk.** Rather than paying the full decrypt-everything cost on every
  `ls`/`show`, a session builds it once and reuses it for the rest of the
  process — but the index dies with the process exactly like the
  decryption key does, so "nothing plaintext outlives the process" still
  holds even though metadata browsing now requires decryption.
- **`history --decrypt` is still the one meaningfully more dangerous tier.**
  `ls`/`search` reveal titles and descriptions — current-state metadata.
  `history --decrypt` walks git log and decrypts *past* revisions of a
  secret's actual value, which is a strictly bigger exposure, so it stays
  an explicit, separately-named subcommand rather than a flag on `log`.
- **One vault, one recipient list — sharing means making another vault,
  not another subtree.** An earlier version let `.age-recipients` be
  overridden per directory, `passage`-style. Dropping that isn't just
  simpler to build — it removes the entire class of bug where "which
  directory a file sits in" is itself a privilege boundary. Now the vault
  *is* the trust boundary, full stop, and creating a new one is cheap
  enough that there's no real cost to that simplicity. See "Why no
  per-directory sharing."
- **`mv`/`cp` are now cross-vault, and that's the sharing mechanism.** With
  a single flat recipient list, there's nothing left to move an entry
  *between* within one vault — so `mv <query> --to-vault <name>` /
  `cp <query> --to-vault <name>` decrypt from the source and re-encrypt for
  the destination's recipients, which is literally what "share this
  credential with someone" means in this design.
- **`.age-recipients` is a confidentiality boundary for the future, not an
  integrity-protected one.** Anyone with git write access can hand-edit it
  to add themselves as a recipient for *upcoming* writes — the encryption
  can't prevent that, since the file is plain committed text and age has
  no concept of "who's allowed to add a recipient." Removing per-directory
  scoping already shrank this to "git write access to the vault," which
  `gage` backs up with local, uncommitted change detection rather than
  pretending the crypto alone makes the file trustworthy. See "Trust
  boundaries."
- **The trust cache diffs `config.toml`, not `.age-recipients` — but
  checks both.** `config.toml`'s device names make for a warning a human
  can actually parse; a bare list of `age1...` keys doesn't. But
  `.age-recipients` is what encryption actually reads, so the cache
  verifies it's consistent with `config.toml` too — otherwise someone
  could bypass the whole mechanism by editing only the file that isn't
  being watched.
- **Cache regeneration isn't one-size-fits-all.** Approving a recipient
  change only silently clears future warnings when `.age-recipients` and
  `config.toml` agree — the routine case. If they've diverged, that's the
  actual tampering signature the cache exists to catch, so confirming past
  it doesn't quietly reset to a clean slate; `gage recipient verify` keeps
  failing until someone fixes the inconsistency for real. Collapsing both
  cases into the same "click yes, never see it again" path would defeat
  the purpose of checking `.age-recipients` at all.
