# Sync and conflicts

Every write to a vault is a git commit, made immediately to your local
repository — that part never depends on the network and never fails due to
connectivity. Getting those commits to and from a remote is a separate
concern with its own policy.

## What happens automatically

- **On `use <vault>`** (entering a vault in a session, or the implicit
  unlock behind a one-shot command), `gage` does a fetch + fast-forward-only
  pull. This is the "catch me up" moment for a fresh terminal. Fast-forward
  only means it never merges anything on your behalf — it either cleanly
  fast-forwards, or leaves your local state completely untouched. If
  there's no network, `gage` doesn't block: it warns once and proceeds with
  your local copy, because refusing to show a password because you're
  offline is worse than showing a possibly-stale one.
- **After every write** (`insert`, `edit`, `generate`, `rm`, `mv`, `cp`,
  `recipient add`/`remove`/`approve`/`deny`), `gage` pushes immediately
  following the commit. When nothing has diverged, this is invisible — the
  write propagates within a second or two. If the push fails because you're
  offline, nothing is lost: the commit already exists locally, same as any
  ordinary git repo with unpushed commits, and the next successful
  `use`/push retries it. The warning says what specifically went
  unpublished; for `recipient approve` that matters a lot, since locally
  the vault is re-encrypted and the device is a recipient while on the
  remote the request is still pending and the joining device is still
  locked out.

### The two exceptions

Two operations depend on the network rather than treating it as best-effort:

- **`gage identity enroll` fails if it can't reach the remote**, or if the
  histories have diverged — before generating a key, prompting, or writing
  anything. An unpublished enrollment request accomplishes nothing.
- **`gage recipient approve` fetches and fast-forwards under its own write
  lock** before it re-verifies and writes, because an approval rewrites
  every entry and one made onto a stale tip conflicts on every one of them.
  A divergence refuses before anything is written (with the ordinary
  `gage sync` advice); an unreachable remote warns and proceeds, since an
  approval that lands locally is real work.

Note that `gage recipient pending` does **not** fetch — it reads your local
copy, so a request published a minute ago shows up only after `gage pull`
or `gage sync`. See [Adding a device](enrollment.md).

## What never auto-resolves

If another device has pushed commits your local copy doesn't have, the
automatic push after a write simply fails and tells you:

```
origin has diverged — 2 local commits pending, run 'gage sync'
```

`.age` files are opaque binary blobs, so git can't three-way-merge two
edits to the same encrypted entry the way it can merge text. A
last-write-wins auto-resolution could silently discard a just-rotated
password with no record that it happened — a much worse failure for a
secrets manager than for, say, a notes app. So a real divergence is always
kicked to a human.

## Resolving a divergence: `gage sync`

```
gage sync [--use NAME]
gage pull [--use NAME]
gage push [--use NAME]
```

`sync` pulls both sides and, for anything that actually conflicts, decrypts
and shows both versions so you choose — `gage` never guesses. `pull` and
`push` are the manual, single-direction versions, useful for scripting or
CI. Day to day, the automatic behavior above means you rarely need to run
any of these directly; they exist for the divergence case and for explicit
control.

**Sync unlocks lazily.** A clean fast-forward, and a merge where both sides
touched different entries, need no identity at all — nothing has to be
decrypted to complete them. `gage sync` only prompts you to unlock when it
actually reaches a conflict it needs to show you plaintext to resolve.

### What counts as a conflict

| Situation | What happens |
|---|---|
| Both sides changed **different** entries | Merges cleanly, nothing to ask |
| Both sides changed the **same** entry | Real conflict — decrypts both, asks you |
| One side **deleted** an entry the other **edited** | Decrypts the surviving version, asks you |
| Both sides changed the **recipient files** | Forced conflict, resolved through the [recipient-change confirmation](identities-and-recipients.md#the-recipient-change-confirmation) |
| Both sides **inserted** entries with the same title | Not a conflict — two real entries with different IDs; resolve the ambiguity later via [query resolution](addressing-entries.md) |
| Both sides added **enrollment requests** | Not a conflict — `.gage/pending/` is deliberately left mergeable, the filenames are unique, and the files are inert until someone approves one |

### Resolving an entry conflict

```
gage> sync
Entry conflicts: "ProtonMail"

  local    updated 2026-08-30 14:22  by laptop-1
  remote   updated 2026-08-29 09:03  by phone

  [l] keep local     [r] keep remote
  [b] keep both      [s] skip this entry     [q] abort sync
Which? [l/r/b/s/q]:
```

- **`l`/`r`** keeps one side and discards the other.
- **`b`** (keep both) writes the losing version as a brand-new entry under a
  fresh ID, so resolving a conflict never destroys a secret — you end up
  with two entries sharing a title, which you can reconcile later with
  `rm`/`rename` whenever you have time to think about it. Choosing wrong
  under time pressure is recoverable, not a silent loss.
- **`s`** (skip) leaves that entry conflicted and moves to the next one;
  the sync ends without pushing, since the tree still has unresolved
  conflicts.
- **`q`** (abort) restores the state from before `sync` ran.

The result is a real git merge commit with both sides as parents. Both
devices' full history remains walkable with `gage history --decrypt`, and
the vault stays an ordinary git repository you can inspect directly with
`git log`/`git show` if you ever need to.

## Concurrent `gage` processes are normal, not an edge case

Because there's no shared agent (see [Session mode](session-mode.md)), it's
completely normal to have a session open in one terminal while running a
one-shot `gage show` in another, against the same vault. `gage` takes a
per-vault advisory lock across each full read-modify-commit sequence to
keep that safe:

- Reads (`show`, `ls`, `cat`, ...) never take the lock — two concurrent
  reads never block each other.
- Writes take the lock and block other writes to the *same* vault only —
  work in one vault never blocks another.
- A contended lock waits (with a message), rather than failing outright,
  since whoever holds it is nearly always about to finish. It times out
  rather than waiting forever, so a wedged holder can't hang your terminal
  indefinitely.
- The lock is released automatically if a process is killed — there's no
  stale lock file to clean up by hand.
