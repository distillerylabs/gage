# Implementation plan

Sequential milestones for building out
[gage-cli-design.md](../designs/gage-cli-design.md). Each milestone should
leave the tool in a runnable, testable state — no milestone should require
reworking a prior one, though later milestones may extend earlier
abstractions.

**Workflow per milestone:** write the milestone's test suite first (it
should fail against the not-yet-built code), then implement until it
passes. A milestone isn't done until its full test list is green — the
test list *is* the definition of done, not a nice-to-have added after the
fact.

Status legend: `[ ]` not started, `[~]` in progress, `[x]` done.

---

## M0 — Scaffolding

Language/toolchain: **Go** (single static binary, mature `age` library,
easy cross-compilation for the Windows/macOS/Linux XDG story). Project
setup, build tooling, and CLI/config skeleton — no crypto yet, just prove
the plumbing.

Key dependencies, decided up front so later milestones don't reshuffle:
- **[spf13/cobra](https://github.com/spf13/cobra)** for command
  structure/dispatch (used from M0 on, in both one-shot and session mode).
- **[go-git/go-git](https://github.com/go-git/go-git)** for all git
  operations (init, commit, fetch, pull, push) — native Go, no shelling
  out to a `git` binary. First used in M1 (`init`), exercised further in
  M3 (commit-per-write) and M7 (sync).
- **[FiloSottile/age](https://github.com/FiloSottile/age)** for all
  encryption/decryption — the reference Go implementation, and the same
  format the design relies on for `.age-recipients` interop with the
  stock `age`/`passage` CLIs. First used in M2 (entry encrypt/decrypt
  round-trip), underpins every command that touches ciphertext after
  that.

### Tests (write first)

- [ ] `make build` produces a `gage` binary
- [ ] `make test` runs with no network access and no pre-existing keys
- [ ] `make lint` and `make fmt --check` exit 0 on a clean tree
- [ ] `gage --help` exits 0 and lists the registered top-level subcommands
- [ ] XDG path resolution: correct config/data/state paths on Linux/macOS
      with `XDG_*` env vars set, and with them unset (defaults)
- [ ] XDG path resolution: correct `%APPDATA%`/`%LOCALAPPDATA%`-based paths
      on Windows
- [ ] Global `config.toml` round-trip: write a `repos.*`/`current` struct,
      read it back, fields match exactly

### Implementation

- [ ] `go.mod` + directory layout (`cmd/gage`, `internal/...`)
- [ ] `Makefile` with `build`, `test`, `lint`, `fmt`, `clean` targets
- [ ] `.gitignore` (binaries, `dist/`, etc.)
- [ ] Cobra root command + subcommand dispatch skeleton
- [ ] XDG path resolution (config/data/state, with Windows mapping)
- [ ] Global `$GAGE_CONFIG/config.toml` read/write

## M1 — Repo lifecycle, single method

`gage init` with **one** method only (passphrase — simplest, no external
plugin dependency). Writes `.gage/config.toml`, `.age-recipients`,
git-inits, registers in global config. Defer `clone` until there's
something worth cloning.

### Tests (write first)

- [ ] `gage init` on an empty temp dir creates `.gage/config.toml`,
      `.age-recipients`, `vault/`, `.gitignore`, and a git repo with
      exactly one commit
- [ ] `gage init` into a non-empty, non-gage directory fails cleanly
      without touching existing files
- [ ] `.gage/config.toml` round-trip: method + `[[recipients]]` fields
      survive write/parse
- [ ] `.age-recipients` round-trip: one public key per line, no gage-only
      framing, so a stock `age`/`passage` CLI could use it as-is
- [ ] `gage repo list` includes a freshly-`init`'d repo
- [ ] `gage repo info <name>` reports the correct method, recipient count,
      remote, and clean/dirty state
- [ ] `gage repo remove <name>` drops it from global config but leaves the
      git repo and its files on disk untouched
- [ ] `gage repo set-default <name>` updates `current` in global config

### Implementation

- [ ] `gage init` (passphrase method only, go-git `git.PlainInit` + initial commit)
- [ ] `.gage/config.toml` read/write
- [ ] `.age-recipients` read/write
- [ ] `gage repo list/info/remove/set-default`

## M2 — Entry format + crypto round-trip

The core loop, at the library level, no CLI verb wired up yet: generate
UUID, YAML-encode an entry, encrypt to `vault/<uuid>.age` via the `age`
library against `.age-recipients`, decrypt it back. This is the riskiest
technical piece (crypto correctness) — prove it in isolation before
anything else depends on it.

### Tests (write first)

- [ ] Entry struct marshals to the exact expected YAML shape
      (`title`/`description`/`created`/`updated`/`updated_by`/`secret`/`fields`)
      and unmarshals back to an identical struct
- [ ] Encrypt → decrypt round-trip recovers a byte-identical entry
- [ ] An entry encrypted to N recipients is independently decryptable by
      each recipient's private key
- [ ] Decrypting with a key that is *not* a recipient fails with a clear
      error, not a panic or silent garbage output
- [ ] Corrupted/truncated ciphertext fails decryption cleanly
- [ ] Generated filenames are valid UUIDv4 and unique across repeated
      calls (no collisions in a large-N generation test)

### Implementation

- [ ] Entry struct + YAML (de)serialization
- [ ] Encrypt entry to `vault/<uuid>.age`
- [ ] Decrypt entry from `vault/<uuid>.age`

## M3 — Dumbest possible CRUD (one-shot mode)

`insert`, `cat` (by UUID or exact title match only — no fuzzy/ambiguous
resolution yet), `rm`, `ls`. Every write is a git commit (no push yet).
First end-to-end usable slice: store and retrieve a secret from the
command line, decrypting everything every time (no cache).

### Tests (write first)

- [ ] `gage insert` followed by `gage cat` round-trips the secret through
      the actual CLI (not just the library)
- [ ] `gage insert` produces exactly one new git commit
- [ ] `gage ls` lists the inserted entry's title
- [ ] `gage rm` deletes the vault file and commits the deletion; a
      subsequent `cat`/`ls` no longer shows the entry
- [ ] `gage cat` on an unknown title/UUID fails with a clear error and
      nonzero exit code
- [ ] Inserting a duplicate title without `-f|--force` is rejected;
      `-f|--force` allows it

### Implementation

- [ ] `gage insert`
- [ ] `gage cat` (exact UUID/title only)
- [ ] `gage rm`
- [ ] `gage ls`
- [ ] Commit-per-write (go-git worktree add + commit)

## M4 — Query resolution

Upgrade addressing from "exact UUID/title" to the full resolution order
(prefix → exact → unique substring → ambiguous prompt/fail). Wire `show`,
`edit`, `rename`, `generate` on top of it.

### Tests (write first)

- [ ] A UUID prefix resolves to the correct entry
- [ ] An exact title match resolves uniquely even when it's also a
      substring of another entry's title
- [ ] A unique (non-exact) substring match resolves correctly
- [ ] An ambiguous substring match lists all candidates and, in one-shot
      mode, fails with nonzero exit instead of prompting
- [ ] `gage edit` re-stamps `updated`/`updated_by` on save, leaves other
      fields untouched if unedited, and produces a new commit
- [ ] `gage rename` changes only the title (not `secret`/`fields`) and
      bumps `updated`
- [ ] `gage generate` inserts a new entry whose `secret` matches the
      requested length/character-set constraints

### Implementation

- [ ] Query resolver (prefix/exact/substring/ambiguous)
- [ ] `gage show`
- [ ] `gage edit` (`$EDITOR` + tmpfs, re-stamps `updated`/`updated_by`)
- [ ] `gage rename`
- [ ] `gage generate`

## M5 — Session mode

The REPL: `use`, `lock`, `status`, `exit`. In-memory key holding with
`mlock`, all M3/M4 commands ported to work against "current session
repo." No metadata index yet — still decrypt-on-demand per command.

### Tests (write first)

- [ ] `use <repo>` unlocks once; subsequent entry commands in the same
      session don't re-prompt for the passphrase
- [ ] `lock <repo>` drops that repo's key; the next entry command against
      it re-prompts, while other unlocked repos in the same session are
      unaffected
- [ ] `status` reports correct lock state for multiple repos touched in
      one session
- [ ] `exit`/EOF terminates cleanly
- [ ] Idle timeout: with simulated/injected elapsed time past
      `idle_timeout`, the next command against that repo re-prompts as if
      `lock` had been run
- [ ] Session command history contains typed command lines (e.g. `show
      protonmail`) but never a decrypted `secret`/`fields` value

### Implementation

- [ ] REPL loop + session commands (`use`/`lock`/`status`/`exit`/`help`)
- [ ] In-memory key holding (`mlock`, no core dumps)
- [ ] Entry commands ported to session mode
- [ ] Idle timeout re-lock

## M6 — Metadata index

In-session decrypt-once cache for `ls`/`search`/`show` resolution,
incremental updates on insert/edit/rm. Pure performance/UX layer on top of
M5 — correctness doesn't change.

### Tests (write first)

- [ ] The first `ls`/`show`/`search` after `use` triggers exactly one
      full-decrypt pass over the repo (assert via decrypt call-count
      instrumentation)
- [ ] Subsequent `ls`/`show`/`search` calls in the same session reuse the
      index without re-decrypting unchanged entries
- [ ] `insert`/`edit`/`rm` update the in-memory index incrementally,
      without triggering a full rebuild
- [ ] `gage reindex` forces a full rebuild and picks up changes made
      outside `gage` (e.g. a manual `git pull`)
- [ ] `search`/`grep` matches against title, description, and body text

### Implementation

- [ ] Build index on first `ls`/`show`/`search` per repo
- [ ] Incremental update on insert/edit/rm
- [ ] `gage reindex`
- [ ] `gage search` / `gage grep`

## M7 — Sync

Needs M5's session lifecycle to hook `use` into, and M3's
commit-per-write already true.

### Tests (write first)

- [ ] `use` performs a fetch + fast-forward-only pull when the remote has
      commits the local repo lacks
- [ ] `use` with no network reachable warns once and proceeds with the
      local copy instead of blocking or failing
- [ ] A write command triggers a push; when nothing has diverged, it
      completes without user-visible friction
- [ ] A simulated offline push leaves the local commit intact (nothing
      lost); the next successful `use`/push retries and succeeds
- [ ] Real divergence (local and remote both have commits the other
      lacks) is detected and reported, never auto-merged or silently
      resolved
- [ ] `gage sync` surfaces both versions of a conflicting entry and
      requires an explicit choice before proceeding
- [ ] `gage git -- <args...>` passthrough executes an arbitrary git
      subcommand against the repo

### Implementation

- [ ] Auto fetch + fast-forward pull on `use` (go-git `Fetch`/`Pull`, ff-only)
- [ ] Auto push after writes (go-git `Push`)
- [ ] Divergence detection
- [ ] `gage sync` (conflict surfacing, both versions shown)
- [ ] Manual `pull`/`push` via go-git; `gage git -- <args...>` passthrough
      shells out to the real `git` binary (go-git doesn't cover the long
      tail of arbitrary git subcommands the design promises here)

## M8 — Identity/recipient management + trust cache

First place multi-device/multi-recipient scenarios become testable —
naturally after single-user CRUD+sync are solid.

### Tests (write first)

- [ ] `gage identity add` registers a device and prints a public key
- [ ] `gage recipient add` (no `--reencrypt`) affects only future writes —
      entries that existed before the add remain undecryptable by the new
      recipient's key
- [ ] `gage recipient add --reencrypt` makes all pre-existing entries
      decryptable by the new recipient
- [ ] `gage recipient remove` without `--reencrypt` is rejected outright;
      with `--reencrypt` it re-encrypts every entry, excluding the
      removed key
- [ ] `gage recipient verify` exits 0 and reports "in sync" when
      `.age-recipients` and `config.toml` agree; exits 1 and lists the
      specific differences when they don't
- [ ] After a device's first successful use of a repo, `known-config.toml`
      is written to local state; a subsequent unreviewed recipient change
      triggers a diff warning before the next encrypt
- [ ] Confirming a *routine* recipient change (files agree) regenerates
      the cache and stops warning; confirming a *mismatched* change
      (`.age-recipients` and `config.toml` disagree) does not silently
      clear the cache — `verify` keeps failing until the inconsistency is
      actually fixed

### Implementation

- [ ] `gage identity add/list`
- [ ] `gage recipient add/remove --reencrypt`
- [ ] `gage recipient verify`
- [ ] Local trust cache (`known-config.toml`, diff + warning on `use`/encrypt)

## M9 — Cross-repo sharing

Just M2's encrypt primitive pointed at a second repo's recipients —
trivial once M1–M2 exist for two repos, but distinct enough to be its own
milestone since it's the sharing story.

### Tests (write first)

- [ ] `gage mv --to-repo` removes the entry from the source repo and it
      becomes decryptable in the destination repo under the destination's
      recipients
- [ ] `gage cp --to-repo` leaves the original in the source repo and adds
      a decryptable copy in the destination
- [ ] `mv`/`cp --to-repo` succeeds even when the destination repo is not
      currently unlocked (encrypting to public keys needs no private key)
- [ ] `gage clone` against a repo where the local device isn't yet a
      recipient reports that plainly and points at `gage identity add`

### Implementation

- [ ] `gage mv --to-repo`
- [ ] `gage cp --to-repo`
- [ ] `gage clone` (go-git `PlainClone`)

## M10 — Polish / output modes

Independent of each other and of the trust model — good candidates to
parallelize or reorder freely, safe to defer individually.

### Tests (write first)

- [ ] `--clip` copies plaintext to the clipboard and clears it after the
      configured timeout (clipboard/timer mocked in tests)
- [ ] `--qr` renders a QR code that decodes back to exactly the requested
      value; `--field NAME --qr` encodes only that field, not the whole
      entry
- [ ] `--field NAME` on `show` extracts the correct value from `fields`
      and errors clearly on an unknown field name
- [ ] `generate` respects `-l LENGTH` and `--no-symbols` in the produced
      secret
- [ ] `gage log` shows commit timestamps/history only — asserts no
      decrypted content appears in its output
- [ ] `gage history --decrypt` walks revisions and produces a diff of
      decrypted content across commits
- [ ] `--script`/`--stdin` runs a sequence of commands non-interactively,
      unlocking each repo at most once

### Implementation

- [ ] `--clip` (clipboard, auto-clear)
- [ ] `--qr` (terminal QR, `--field` scoped)
- [ ] `--field NAME` extraction
- [ ] `generate` password-generation logic
- [ ] `gage log`
- [ ] `gage history --decrypt`
- [ ] Non-interactive session (`--script`, `--stdin`)

---

## Deferred / not on the critical path

- **Additional identity methods** (ssh, yubikey, secure-enclave,
  plugin) — passphrase alone proves the `identity`/`recipient`
  abstraction; add real methods once that interface is stable, otherwise
  you're debugging plugin behavior and architecture at the same time.
- **Signed recipient changes** — design doc calls this out as "reserve
  for high-stakes repos," not a default.
- **`gage clone` against a real remote** — folded into M9, but only
  really testable once there's an actual remote to clone from.
