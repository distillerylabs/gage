# `gage` — a git-backed, age-encrypted secret & notes manager

*(working name: `gage` = "git" + "age"; rename freely)*

## Design principles

1. **A repository is the unit of trust.** Each repo has exactly one decryption
   method chosen at `init` time, and its own set of recipients. You can have
   as many repos as you want (`work`, `personal`, `shared-family`), each with
   a different security posture.
2. **Entries are just files, named opaquely.** A password, a structured note
   (API keys, recovery codes), or an unstructured note (a paragraph of text)
   are all the same thing on disk: one `.age`-encrypted file per entry,
   git-tracked, named with a random UUID rather than a human-readable path.
   Everything a human would recognize — title, description, the actual
   secret — lives *inside* the encrypted payload as structured YAML. Someone
   with read access to the repo but not the decryption key sees only opaque
   filenames and ciphertext; they learn nothing about what you have stored,
   not even categories or counts-per-topic.
3. **Git is sync, not a feature to hide.** Every write is a commit. Standard
   git remotes (self-hosted, GitHub private repo, etc.) do the syncing;
   `gage` never invents its own transport.
4. **Decryption method is pluggable, not hardcoded.** Passphrase, raw age
   key file, SSH key, YubiKey (`age-plugin-yubikey`), Secure Enclave — the
   repo just stores *which* method and *whose* public keys; each device
   registers its own way of proving it holds the matching private key.
5. **Key material lives exactly as long as the process that holds it.**
   No background daemon, no IPC socket, no separate agent lifecycle to
   manage or forget about. Unlocking happens in-process; exiting the
   process is what destroys the key. See "Session model" below.
6. **Plaintext should be surprising to produce.** Default `show` prints to
   the terminal (you asked for it), but copy-to-clipboard and QR-to-camera
   are first-class so plaintext never has to sit in scrollback or a file.

---

## On-disk layout

```
myvault/                          # git repo root
├── .gage/
│   └── config.toml               # repo-level config: method + device metadata (committed, plaintext)
├── .age-recipients               # recipients for the whole repo — the only one
├── vault/
│   ├── 4b9d7710-8e2a-4a1f-9c3d-1a2b3c4d5e6f.age
│   ├── a03e5f88-1c44-4e9a-8b77-2d3e4f5a6b7c.age
│   └── 8f3a1c2e-5566-4a11-9d22-33aa44bb55cc.age
└── .gitignore
```

Flat *inside* `vault/`, deliberately — the UUID files themselves have no
further structure. Keeping them under `vault/` rather than scattered at
the repo root is purely cosmetic (a `git status`/`ls` at the root shows
`.gage/`, `.age-recipients`, `vault/` — three things, not a growing wall
of random UUIDs) since there's no more per-directory recipient scoping to
motivate any particular placement. Filenames carry no meaning — they're
generated (UUIDv4) at `insert` time and never chosen or seen by the user
in normal use. Categorization lives entirely in each entry's metadata (see
"Entry format" below), searchable only by someone holding the key.

If you want a subset of entries to have a *different* set of people who
can read them, that's a different repo, not a subtree of this one — see
"Why no per-directory sharing" below.

Two things live at the repo root instead of nested, and one thing doesn't
— worth being explicit about why, since it's not arbitrary:

- **`.age-recipients` stays flat at the root.** It needs a predictable,
  un-nested path so the stock `age`/`passage` CLI can use it directly
  (`age -R .age-recipients -e file`) without knowing anything about
  `gage`'s internal layout — that external-interop requirement is the
  whole reason it's not tucked away somewhere.
- **`config.toml` lives under `.gage/`, not at the root.** Nothing outside
  `gage` itself ever reads it, so there's no interop reason it needs a
  flat path. Nesting it — mirroring `.git/`'s own convention — makes clear
  it's `gage`'s control-plane data rather than repo content, and leaves
  room for more `gage`-owned files later (schema version markers,
  migration state) without cluttering the root or claiming a generic name
  like `config.toml` at the top level.

`.gage/config.toml` (plaintext, committed — it names the method and public
keys, never secret material):

```toml
[repo]
name = "myvault"
format_version = 1
created = "2026-08-29"

[method]
kind = "yubikey"          # passphrase | age-key | ssh | yubikey | secure-enclave | plugin:<name>
plugin = "age-plugin-yubikey"

[[recipients]]
device = "yubikey-5c-nfc-1"
pubkey = "age1yubikey1q..."
[[recipients]]
device = "recovery-paper-key"
pubkey = "age1..."
```

Recipients apply to the whole repo, full stop — no per-directory overrides.
`gage init` is cheap enough that "who needs access to this?" is a repo-level
question answered once at creation, not something that needs finer-grained
plumbing inside a single repo.

### Why no per-directory sharing

An earlier version of this design let `.age-recipients` be overridden per
subtree, `passage`-style, so one repo could mix a household `shared/` tree
with a private `personal/` tree. It works, but it buys a real amount of
complexity for a use case that has a much simpler answer: **make a second
repo.**

```
gage init shared-family --method passphrase --recipient <family-member-pubkey>
gage mv family-wifi --to-repo shared-family
```

`mv --to-repo` decrypts the entry from the source repo (which has to be
unlocked) and re-encrypts it for the destination repo's recipients — and
notably, that destination doesn't need to be unlocked at all, since
encrypting *to* a set of public keys never requires holding any of the
matching private keys. `cp --to-repo` does the same but leaves the
original in place.

This isn't just simpler to implement — it removes an entire class of
problem rather than mitigating it. Per-directory recipients meant "which
directory an entry sits in" was itself a privilege boundary, which is
exactly the shape of bug your original question was probing: someone with
git write access moving a file into a directory they happen to have keys
for. With one recipient list per repo, there's no finer-grained boundary
to attack — "does this person have write access to this specific git
repo" is now the entire question, and it's a question git hosting already
answers.

What you give up: if your `work` repo needs three different partial-access
groups, that's three repos instead of one repo with three subtrees. In
practice this tends to be the right shape anyway — separate repos get
separate remotes, separate access lists on the git host, and separate
audit trails, which is arguably what you wanted for genuinely different
trust groups regardless of what `gage` did internally.

---

## Entry format

Once decrypted, an entry is YAML with a fixed set of metadata fields plus
the actual content:

```yaml
title: ProtonMail
description: Personal email, 2FA via authenticator app
created: 2026-01-14T10:32:00Z
updated: 2026-08-20T09:03:00Z
updated_by: yubikey-5c-nfc-1
secret: correcthorsebatterystaple
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
- **`secret`** / **`fields`** — the actual payload. `secret` holds the
  primary value (a password, or the body of an unstructured note); `fields`
  holds structured key/value metadata (`--field NAME` on `show`/generate
  extracts from here). This replaces the old positional "line 1 = secret,
  rest = key:value" convention from `pass`/`passage` with explicit keys,
  since the file is already structured YAML for the metadata anyway.

`gage insert`/`gage generate` populate `title`/`created`/`updated_by`
automatically; `gage edit` opens the full YAML in `$EDITOR` and re-stamps
`updated`/`updated_by` on save.

---

## Global config

`~/.gage/config.toml` tracks known repos and shell preferences:

```toml
current = "personal"

[repos.personal]
path = "~/.gage/vaults/personal"
remote = "git@github.com:you/personal-vault.git"

[repos.work]
path = "~/.gage/vaults/work"
remote = "git@internal:secrets/work-vault.git"

[shell]
prompt = "[{repo}{lock}] gage> "     # {repo}, {lock} (🔓/🔒), {dirty} tokens available
idle_timeout = "10m"                 # auto re-lock a session repo after inactivity
history_file = "~/.gage/state/history"   # command names/paths only, never values
```

Everything `gage` owns lives under one root, `~/.gage/`, rather than
spread across the XDG-standard split (`~/.config`, `~/.local/share`,
`~/.local/state`). For a single-user tool, "back up or move one folder"
is worth more than XDG purity here — and since `~/.gage/` is its own
top-level directory rather than nested inside `~/.config`, it won't get
swept into a dotfiles-sync of `~/.config` by accident. It still keeps a
light substructure rather than dumping everything flat: `vaults/` holds
actual git repos, `state/` holds local-only, never-synced device state
(the history file and, per-repo, `known-config.toml` — the trust cache
used to detect unreviewed recipient changes, see "Local trust cache").
That last distinction matters even within one root: state reflects this
device's own history and prior human review, so it's not something you'd
want a dotfiles manager or backup tool copying to a new machine the same
way `config.toml` or a vault itself would be.

---

## Session model: `gage` as its own agent

Instead of a background daemon with a socket, `gage` has **two invocation
modes**, and they're deliberately independent — nothing bridges them, so
there's no persistent listener to secure or forget about:

**1. One-shot mode** — `gage show protonmail`. Unlocks the identity,
decrypts every entry once to resolve the title against what you typed,
shows the match, drops the key from memory, exits. Every invocation pays
both the unlock cost (passphrase prompt / YubiKey touch) *and* the full
decrypt-and-resolve cost, since there's no cache to reuse between
invocations — see "Addressing entries" below. Best for scripting, one-off
lookups, and composing with other unix tools.

**2. Session mode** — running `gage` with no subcommand (or `gage shell`)
drops you into an interactive prompt:

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

The unlocked identity for each repo is held **only in this process's
memory**, `mlock`'d so it can't be paged to swap, with core dumps disabled
for the process. There is nothing to attach to from another terminal —
that's the whole point. When the process exits (`exit`, `quit`, Ctrl-D, or
the process being killed), the memory is zeroed and reclaimed by the OS;
there is no cleanup step that can be skipped or forgotten.

**Non-interactive session mode** exists too, for automation that wants the
same "unlock once, do several things, then gone" property without a human
at a TTY:

```
gage --script deploy-secrets.gage      # reads session commands from a file
cat commands.txt | gage --stdin        # same, from stdin
```

### Session-only commands

```
use <repo>          switch/unlock the active repo for this session
lock [repo]         drop key material for one repo (or all, if omitted)
                     without exiting the process
status / whoami     list repos touched this session and their lock state
help
exit / quit / ^D
```

All entry commands (`show`, `ls`, `insert`, `edit`, `generate`,
`search` (aka `grep`), `rm`, `mv`, `cp`, `sync`, `git`, `log`, `history`) work
against the current session repo without needing `--use` each time, but
`--use NAME` still works ad hoc against any repo already `use`d this
session (it'll prompt to unlock if you haven't touched it yet).

### Addressing entries & the metadata index

With opaque UUID filenames, there's no more "path" to type. Instead, `show`,
`edit`, `rm`, `mv`, `cp`, and `generate` all take a **query** that's matched
against decrypted `title`s:

```
[personal🔓] gage> show protonmail
correcthorsebatterystaple
[personal🔓] gage> show aws
Multiple entries match "aws":
  1. AWS root account          (4b9d7710) — updated 2026-08-20
  2. AWS IAM backup admin       (a03e5f88) — updated 2026-01-14
Which one? [1-2]:
```

Resolution order: exact UUID (or its short prefix) → exact title match →
unique substring match on title → ambiguous, so list candidates and ask
(one-shot mode fails with the same list instead of prompting, since there's
no one to ask).

Making this fast is why `ls` used to be a "cheap, no-decrypt" command and
can't be anymore — the moment titles move inside the encrypted payload,
even *browsing* requires the key. The session process resolves this by
building an **in-memory metadata index** the first time `ls`/`show`/`search`
needs one per repo: it decrypts every entry once, pulls out just the
metadata fields (title, description, dates, `updated_by`), and keeps that
index in memory for the rest of the session, updating it incrementally as
you `insert`/`edit`/`rm`. This index dies with the process exactly like the
key does — it's never written to disk, so "nothing plaintext outlives the
process" still holds even though metadata is now something the tool has to
decrypt just to display a list. `gage reindex` forces a rebuild (useful
after a `git pull` run outside `gage`, or if you suspect staleness).

One-shot mode gets no such cache — every invocation of `ls`/`search`/`show`
pays the full decrypt-every-entry cost from scratch. That's fine for a
handful of entries, but it's the real cost of hiding names: for a large
vault, prefer session mode for anything beyond a single known lookup.

### Why an idle timeout still matters despite process-scoped keys

Process lifetime is a clean boundary in theory, but terminal multiplexers
break the assumption that "process exits when you're done." A `gage`
session left running in a `tmux` pane over a weekend is functionally an
unmanaged agent again. `idle_timeout` re-locks a repo's key automatically
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

---

## Sync model: mostly automatic, deliberately not fully

Sync is split into two operations with very different risk profiles, and
they're handled differently on purpose.

**Commit is local, instant, and always happens.** Per the original design
principle — every write is a commit — nothing is ever *only* in memory once
you save an edit. It's durable in the local git object store immediately,
sync or no sync. This part was never in question.

**Push/pull are network-dependent and can conflict**, so they get an actual
policy instead of "always" or "never":

- **On `use <repo>`** — entering a repo in session mode, or unlocking it for
  a one-shot command — gage does a `git fetch` + fast-forward-only pull
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
  device has pushed commits your local repo doesn't have, the automatic
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
git-committed text file. Anyone with git write access to the repo — even
someone holding no decryption key at all — can append a public key to it.
`gage` has no built-in way to distinguish "a device legitimately
registered via `gage identity add`" from "a line someone typed into a text
file," because nothing cryptographically binds the two. The next real
write trusts whatever's currently listed and quietly wraps the new content
key for that recipient too.

Removing per-directory recipients (see "Why no per-directory sharing"
above) already closed the sharper version of this problem — there's no
longer a way to gain access to *part* of a repo by manipulating where a
file sits, since there's only one recipient list and it's not tied to
location at all. What's left is the coarser, unavoidable version: **git
write access to a repo is, transitively, the ability to eventually get
added as a recipient of that repo.** That's not really a `gage`-specific
problem — it's true of any system where "who can commit" and "who can
grant access" aren't cryptographically separated — but it's worth stating
plainly rather than assuming the encryption alone covers it.

### Mitigations

- **Git-hosting access control (the baseline).** Don't grant write access
  to a repo to anyone who shouldn't eventually be able to read everything
  in it. This is external to `gage` — a property of GitHub/self-hosted
  permissions — but since a repo is now the *entire* trust boundary, it's
  also the entire mitigation surface at this layer.
- **Change detection on the recipient list (a practical backstop).** See
  "Local trust cache" below — a concrete mechanism, not just a policy.
- **Signed recipient changes (optional, higher-assurance).** For methods
  that support signing (SSH keys, YubiKey PIV, Secure Enclave), `gage
  recipient add`/`remove` could write a detached signature alongside
  `.age-recipients`, and `gage` would refuse to honor a file whose latest
  change isn't validly signed by a previously trusted recipient. Real
  fix, not just a warning — but real added complexity, so worth reserving
  for genuinely high-stakes shared repos rather than making it a default.

Nothing here changes guarantee #1: no already-encrypted entry becomes
readable through this vector, ever. What's at stake is only whether an
untrusted git-writer can add themselves to what gets encrypted *next* —
and now that the whole repo is one flat trust boundary, "don't give that
person write access to this repo" is most of the answer by itself.

### Local trust cache

The mechanism behind "change detection" above:

- **What's cached.** The first time a device successfully uses a repo,
  `gage` writes a verbatim copy of `.gage/config.toml` to
  `~/.gage/state/<repo>/known-config.toml`, plus the content hash of
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
  (`insert`, `edit`, `generate`, `rename`, and `mv`/`cp --to-repo` against
  the *destination* repo's cache), `gage` compares the current committed
  `.gage/config.toml` and `.age-recipients` against the cached copies.
  It also checks opportunistically on `use`/`sync`, so you see a warning
  when you sit down, not only at the moment you're about to write.
- **What a mismatch looks like — a plaintext diff, always.** `gage` never
  just says "recipients changed"; it shows exactly what changed, since
  the cached copy of `config.toml`:
  ```
  [personal🔓] gage> generate chase-checking
  ⚠ Recipients for this repo changed since you last encrypted here:

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
  device that already holds decrypt access to the repo (or is about to
  gain it) — it's not protecting against a compromised local machine,
  which is a different, more severe threat model where the game is
  already lost. It's protecting against a remote git-writer trying to
  inject themselves quietly, and a local record of "what I last saw and
  approved" is exactly what's needed to catch that.

---

## Command reference

### Repository lifecycle

```
gage init <name> [--dir PATH] [--remote URL]
                  --method passphrase|age-key|ssh|yubikey|secure-enclave|plugin:<name>
                  [--recipient PUBKEY ...]

    Creates a new repo: git init, writes .gage/config.toml with the chosen
    method, generates or registers the first identity, commits the initial
    (empty) structure. --recipient can be repeated to add extra recipients
    (e.g. a recovery key) at creation time. Without --dir, the repo is
    created at ~/.gage/vaults/<name> and registered under that path in
    the global config.

gage clone <remote-url> [--name NAME] [--dir PATH]

    git clones the repo and reads .gage/config.toml to learn the required
    method. Without --dir, clones to ~/.gage/vaults/<name> — same default
    as `init` — inferring <name> from the remote URL unless --name overrides
    it. Does NOT grant you access — if your device isn't already a
    recipient, gage tells you to run `gage identity add` to generate your
    public key, then get it added via `gage recipient add` from a device
    that already has access.

gage repo list
gage repo remove <name>              # forgets locally; does not delete the git repo
gage repo info [<name>]              # method, recipient count, remote, dirty/clean
gage repo set-default <name>         # changes `current` in global config
```

`use` is the one verb for "operate against this repo," in both modes:

- **Session mode:** `use <name>` as its own line, switching what subsequent
  commands in this `gage>` process target.
- **One-shot mode:** `-u|--use NAME` as a flag on any single command.

Previously these were two different words (`repo use` vs. a `--repo` flag)
for the same underlying action, which was needless vocabulary to learn.
Now it's one concept — select which repo this command or session talks to
— spelled the same way everywhere. `repo set-default` is kept as a
separate, deliberately different-sounding command because it does something
meaningfully different: it doesn't select a repo for the current action, it
changes what "no `--use` given" falls back to, persistently, in the global
config. Conflating "use this now" with "make this the default forever"
under one verb would be confusing in the other direction.

### Identity (how *this device* proves it can decrypt)

```
gage identity add --use NAME --method ssh|yubikey|passphrase|secure-enclave
                   [--key-path PATH]

    Registers this device's way of satisfying the repo's method, and prints
    the resulting public key so it can be added as a recipient (either by
    you, if you're bootstrapping, or by an existing recipient).

gage identity list [--use NAME]
```

### Recipient / access management

```
gage recipient add <pubkey-or-name> [--use NAME] [--reencrypt]
gage recipient remove <pubkey-or-name> [--use NAME] --reencrypt
gage recipient list [--use NAME]
gage recipient verify [--use NAME]

    Adding a recipient only affects future encryptions unless --reencrypt
    is passed, which decrypts and re-writes every entry in the repo so the
    new recipient can read history too. Removing a recipient REQUIRES
    --reencrypt (gage refuses to silently leave old ciphertext readable by
    a removed party) and prints a clear warning that this revokes future
    access only — anything already read can't be unread.

    `verify` checks that .age-recipients and .gage/config.toml's
    [[recipients]] list the same public keys. Needs no unlock — both files
    are plaintext — so it works before any identity is available and is
    safe to run in CI. Exits 0 and prints "in sync" on a match; exits 1
    and lists the specific differences otherwise (present in one file,
    missing from the other). This is the same comparison the local trust
    cache runs automatically before every encrypt (see "Local trust
    cache") — `verify` just exposes it as something you can run any time.
```

### Entry CRUD

```
gage ls [--use NAME]                        # list titles (requires unlock — see below)
gage search <pattern> [--use NAME]          # matches title/description/body
                                              # (decrypts in bulk if no index
                                              # cached yet; alias: gage grep)

gage insert <title> [--use NAME] [--description TEXT] [-m|--multiline] [-f|--force] [--yes]
gage edit <query>   [--use NAME]            # decrypt to tmpfs, $EDITOR, re-encrypt, commit
                                              # (re-stamps updated/updated_by)
gage rename <query> <new-title> [--use NAME]  # quick metadata-only edit, no $EDITOR
gage generate <title> [--use NAME] [-l LENGTH] [--no-symbols]
                       [-f|--force] [-c|--clip] [-q|--qr] [--yes]

gage show <query> [--use NAME] [-c|--clip] [-q|--qr] [--field NAME]
gage cat  <query> [--use NAME]              # always full raw plaintext, for scripting/piping

gage rm <query> [--use NAME]
gage mv <query> --to-repo <name> [--use NAME] [--yes]   # decrypt here, re-encrypt + commit
                                                  # there, remove from here — this
                                                  # is the sharing mechanism
gage cp <query> --to-repo <name> [--use NAME] [--yes]   # same, but keeps the original too
```

`--yes` bypasses the recipient-change confirmation prompt (see "Local
trust cache") for scripting/CI; interactively it's never needed since
`gage` just asks.

Notes on `show`:
- Default (no flag): prints the `secret` field only — not `title`,
  `description`, `fields`, or the timestamps. Just the one value you
  almost certainly want, so it composes cleanly with pipes/paste. `gage
  cat` is the command that dumps the full decrypted YAML, metadata
  included, when you actually want everything.
- `--field NAME`: prints one entry from `fields` instead of `secret`
  (e.g. `--field username`, `--field totp_seed`).
- `-c`: copies to clipboard, auto-clears after a short timeout.
- `-q`: renders a terminal QR code **instead of** printing plaintext — this
  is the scan-with-Camera-app workflow, now built into the tool instead of
  being a manual `-q`-then-decode dance.
- `--field NAME` with `-q`: QR-encodes *only* that field, not the whole
  entry — important so a structured note (say, an SSH private key plus
  metadata) doesn't get dumped whole into a QR code when you only wanted
  the TOTP seed.

### Git / sync

Push and fast-forward-only pull happen automatically around `use` and
writes (see "Sync model" above) — these commands are the manual override:
forcing a check, resolving a real divergence, or scripting/CI use.

```
gage sync [--use NAME]          # pull, surface + resolve any conflicts, then push
gage pull [--use NAME]
gage push [--use NAME]
gage git [--use NAME] -- <args...>   # passthrough for anything else (branches, tags, etc.)

gage log [QUERY] [--use NAME]         # commit history for one entry (resolved via query);
                                       # timestamps only — filenames alone reveal nothing
gage history --decrypt <query> [--use NAME]
                                       # walks git log for one entry and decrypts each
                                       # revision to show a diff. Explicit subcommand,
                                       # not a flag on `log`, because it's meaningfully
                                       # more dangerous (surfaces old secret values).
```

---

## A few decisions worth calling out

- **No daemon means no IPC attack surface.** There's no unix socket whose
  permissions need auditing, no risk of a stale agent process outliving
  the terminal that spawned it and being forgotten about. The trust
  boundary collapses to "is this specific process still alive," which the
  OS already enforces.
- **One verb, `use`, for repo selection everywhere.** Session mode and
  one-shot mode used to have different vocabulary for the same action
  (`use <name>` vs. `--repo NAME`); they now both say `use`, as a bare
  session command or a `-u|--use NAME` flag. `repo set-default` stays a
  distinct command since "pick a repo for this default" is a different,
  persistent action, not the same thing spelled differently.
- **`config.toml` and `.age-recipients` are split by access pattern, not
  just by convention.** `config.toml` (method/device metadata) is read
  rarely and changes almost never. `.age-recipients` is read on every
  encrypt and changes comparatively often, and it's a plain, flat,
  one-key-per-line file — that's what keeps it usable directly by stock
  `age`/`passage`, with no per-repo parsing beyond splitting lines.
- **`identity` vs `recipient` stay separate.** `recipient` is "who can
  decrypt this repo" (public keys, repo-side, committed to git).
  `identity` is "how does *this device* prove it's one of those
  recipients" (private-key handling, device-side, never committed). A
  YubiKey-based repo might have a laptop identity via NFC/USB touch and a
  phone identity via the Secure Enclave — same recipient list, different
  local mechanics.
- **Session repos are re-lockable without killing the process.** `lock
  <repo>` (or the idle timeout) drops that repo's key from memory while
  leaving other unlocked repos and the shell itself intact — useful when
  you're about to step away but want to keep working in a different repo,
  or hand the terminal to someone else without exiting entirely.
- **Command history must never contain plaintext.** The session's
  readline-style history should log `show protonmail`, not the decrypted
  value that was printed — an easy leak vector to overlook once you have a
  REPL with recallable history. Titles typed as *queries* are fine to log
  (you typed them yourself); it's only the decrypted `secret`/`fields`
  values that must never land in the history file.
- **`--reencrypt` is mandatory, not default-on, for recipient removal.**
  Silently leaving stale ciphertext readable by a removed recipient is a
  worse failure mode than forcing the user to explicitly opt into the
  (slower) re-encryption pass.
- **Sync is automatic except where auto-resolving would be dangerous.**
  Fast-forward pulls on `use` and pushes after every write happen without
  being asked, so `pull`/`push` aren't something to remember day-to-day.
  The one case that stays manual — a real divergence between two devices —
  does so because `.age` files can't be three-way-merged like text, and
  guessing a winner risks silently discarding a secret with no trace.
- **Filenames are opaque UUIDs; metadata lives inside the ciphertext.**
  This trades away the one thing `pass`/`passage` leave exposed — that
  someone with repo read access (but no key) can see your entry names and
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
- **One repo, one recipient list — sharing means making another repo, not
  another subtree.** An earlier version let `.age-recipients` be overridden
  per directory, `passage`-style. Dropping that isn't just simpler to
  build — it removes the entire class of bug where "which directory a file
  sits in" is itself a privilege boundary. Now the repo *is* the trust
  boundary, full stop, and creating a new one is cheap enough that there's
  no real cost to that simplicity. See "Why no per-directory sharing."
- **`mv`/`cp` are now cross-repo, and that's the sharing mechanism.** With
  a single flat recipient list, there's nothing left to move an entry
  *between* within one repo — so `mv <query> --to-repo <name>` /
  `cp <query> --to-repo <name>` decrypt from the source and re-encrypt for
  the destination's recipients, which is literally what "share this
  credential with someone" means in this design.
- **`.age-recipients` is a confidentiality boundary for the future, not an
  integrity-protected one.** Anyone with git write access can hand-edit it
  to add themselves as a recipient for *upcoming* writes — the encryption
  can't prevent that, since the file is plain committed text and age has
  no concept of "who's allowed to add a recipient." Removing per-directory
  scoping already shrank this to "git write access to the repo," which
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
