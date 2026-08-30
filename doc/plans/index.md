# Implementation plan

Sequential milestones for building out
[gage-cli-design.md](../designs/gage-cli-design.md). Each milestone should
leave the tool in a runnable, testable state — no milestone should require
reworking a prior one, though later milestones may extend earlier
abstractions.

Every milestone builds its logic into the `internal/gage` library first
and wires `cmd/gage` (Cobra) on top as a thin frontend — see the design
doc's "Library architecture." This is what keeps a future GUI/TUI addable
later without reworking any milestone here: it's a second frontend over
the same methods, not a refactor of them.

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

The `internal/gage` (library) / `cmd/gage` (CLI) split is decided here
too, before any real logic exists to misplace: every later milestone
builds core behavior into the library and wires a thin CLI layer on top,
so a future GUI/TUI is a new frontend on stable ground, not a refactor.

Key dependencies, decided up front so later milestones don't reshuffle:
- **[spf13/cobra](https://github.com/spf13/cobra)** for command
  structure/dispatch (used from M0 on, in both one-shot and session mode).
- **[pelletier/go-toml](https://github.com/pelletier/go-toml)** (v2) for
  all TOML read/write — global `$GAGE_CONFIG/config.toml` and each
  vault's `.gage/config.toml` — TOML 1.0 compliant, struct-tag driven, no
  CGo. First used in M0 itself (the global config round-trip test),
  underpins M1's vault config from there.
- **[go-git/go-git](https://github.com/go-git/go-git)** for all git
  operations (init, commit, fetch, pull, push) — native Go, no shelling
  out to a `git` binary. First used in M1 (`init`), exercised further in
  M3 (commit-per-write) and M7 (sync).
- **[golang.org/x/term](https://pkg.go.dev/golang.org/x/term)** for
  masked terminal input — the standard extended-stdlib package for
  reading input without local echo, cross-platform (Linux/macOS/Windows).
  First used in M1 (`Vault.Unlock`'s passphrase prompt via `Prompter`),
  reused by M3's default `insert` prompt.
- **[golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys)** (`unix` and
  `windows` subpackages) for the cross-platform `memlock` package —
  `mlock`/`munlock` on Linux/macOS, `VirtualLock`/`VirtualUnlock` on
  Windows, behind one signature (see M1's "Local identity storage" and
  memory-protection work). First used in M1.
- **[google/uuid](https://github.com/google/uuid)** for entry filenames
  (`entries/<uuid>.age`) — RFC 4122 UUIDv4 generation. First used in M2
  (entry encrypt/decrypt round-trip).
- **[FiloSottile/age](https://github.com/FiloSottile/age)** for all
  encryption/decryption — the reference Go implementation, and the same
  format the design relies on for `.age-recipients` interop with the
  stock `age`/`passage` CLIs. First used in M2 (entry encrypt/decrypt
  round-trip), underpins every command that touches ciphertext after
  that.
- **[atotto/clipboard](https://github.com/atotto/clipboard)** for
  `-c`/`--clip` — native syscalls on Windows, shells out to
  `pbcopy`/`xclip`/`xsel` on macOS/Linux. This is the one place `gage`
  has a runtime dependency on an external binary being present — not a
  build-time one, the `gage` binary itself still compiles as a single
  static executable — worth flagging as the exception now that the git
  side has none (see the design doc's "Git-specific commands": there's
  deliberately no `git`-binary passthrough anywhere else in `gage`).
  First used in M11.
- **[mdp/qrterminal](https://github.com/mdp/qrterminal)** for `-q`/`--qr`
  — renders a QR code directly as terminal block art, the
  scan-with-camera workflow the design calls for, with no separate
  image-to-terminal conversion step. First used in M11.

### Tests (write first)

- [ ] `make build` produces a `gage` binary
- [ ] `make test` runs with no network access and no pre-existing keys
- [ ] `make lint` and `make fmt --check` exit 0 on a clean tree
- [ ] a lint check fails if `internal/gage` (the library layer) references
      `os.Stdin`/`os.Stdout` or calls `fmt.Print*`/`os.Exit` outside
      `_test.go` files — keeps CLI-only I/O out of the library from day one
- [ ] `gage --help` exits 0 and lists the registered top-level subcommands
- [ ] XDG path resolution: correct config/data/state paths on Linux/macOS
      with `XDG_*` env vars set, and with them unset (defaults)
- [ ] XDG path resolution: correct `%APPDATA%`/`%LOCALAPPDATA%`-based paths
      on Windows
- [ ] Global `config.toml` round-trip: write a `vaults.*`/`current` struct,
      read it back, fields match exactly
- [ ] Local identity path resolves to `$GAGE_DATA/identities/<vault>/<device>.age`
      — a sibling of `$GAGE_DATA/vaults/`, never nested inside a vault's own
      directory
- [ ] `gittest.NewBareRemote` (or similar) creates an ephemeral local
      bare repo under `t.TempDir()` and returns its path; two calls
      produce independent, non-colliding repos
- [ ] CI runs `make build`/`make test`/`make lint` on Linux, macOS, *and*
      Windows runners — actual native execution, not cross-compilation,
      since later milestones' Windows-specific claims (M1's `mlock`/
      `VirtualLock`, M5's session/idle-timeout parity) rely on syscalls
      that only run correctly on their native platform

### Implementation

- [ ] `go.mod` + directory layout: `cmd/gage` (thin Cobra frontend) and
      `internal/gage` (library — vault/session/entry logic, no terminal
      I/O), split from the start so later milestones build into the
      right layer
- [ ] `Makefile` with `build`, `test`, `lint`, `fmt`, `clean` targets
- [ ] `.gitignore` (binaries, `dist/`, etc.)
- [ ] CI workflow with a Linux/macOS/Windows runner matrix invoking
      `make build`/`make test`/`make lint` on each — set up now so every
      later milestone's cross-platform claims are actually verified,
      rather than asserted on whatever OS the plan happens to be written on
- [ ] Cobra root command + subcommand dispatch skeleton
- [ ] skeleton `Prompter` (or similar) callback interface in
      `internal/gage` for interactive decisions (confirm, disambiguate,
      and unlock) — implemented against stdin/stdout by `cmd/gage`,
      satisfied by a fake in library tests. The unlock exchange is typed
      and method-agnostic (`UnlockRequest`/`UnlockResponse` carrying a
      `Kind`, e.g. `"passphrase"`), not a passphrase-specific method, so
      a second identity method later (deferred — see bottom of this doc)
      is additive rather than a breaking interface change
- [ ] skeleton `Identity` type (wraps unlocked private key material) with
      a `Close()` method that releases its page lock and zeroes it, and the
      `Vault.Unlock(Prompter) (Identity, error)` method signature — no
      real crypto behind either yet (that's M1/M2), but deciding the
      shape now is what lets every later CRUD method take `Identity` as
      an explicit parameter instead of unlocking internally, so M5's
      `Session` can cache one across calls without reworking M3/M4
- [ ] XDG path resolution (config/data/state, with Windows mapping)
- [ ] Local identity path helper: `$GAGE_DATA/identities/<vault>/<device>.age`
      (directory `0700`, file `0600`)
- [ ] Global `$GAGE_CONFIG/config.toml` read/write
- [ ] `internal/gage/gittest` test-helper package: `NewBareRemote(t)
      string` wraps `t.TempDir()` + `git.PlainInit(path, true)` to create
      an ephemeral local bare repo for use as a fake remote in later
      milestones' tests (first real use: M1's `git set-remote` tests; the
      full sync-testing harness built on top of it lands in M7)

## M1 — Vault lifecycle, single method

`gage init` with **one** method only (passphrase — simplest, no external
plugin dependency) and **one** vault type only (`git` — the only type
that exists). Writes `.gage/config.toml` (with `type = "git"`),
`.age-recipients`, git-inits, registers in global config. Also generates
the first device's identity — a fresh X25519 keypair, private half
wrapped via age's scrypt passphrase recipient and written to
`$GAGE_DATA/identities/<vault>/<device>.age` (see design doc's "Local
identity storage") — with only the resulting public key going into
`.age-recipients`/`config.toml`. Defer `clone` until there's something
worth cloning. This milestone also establishes the cross-platform memory
protection every later identity relies on — page-locking and core-dump
disabling on Linux, macOS, and Windows — since it's the first point any
private key material exists in process memory.

### Tests (write first)

- [ ] `gage init` on an empty temp dir creates `.gage/config.toml`,
      `.age-recipients`, `entries/`, `.gitignore`, and a git repo with
      exactly one commit
- [ ] `gage init` into a non-empty, non-gage directory fails cleanly
      without touching existing files
- [ ] `.gage/config.toml` round-trip: `[vault]` (including `type = "git"`)
      + method + `[[recipients]]` fields survive write/parse
- [ ] `gage init` with no `--type` flag defaults to `type = "git"`
- [ ] `gage init --type git` succeeds and is equivalent to omitting the flag
- [ ] `gage init --type <anything-else>` fails with a usage error before
      creating any files, and lists `git` as the only accepted value
- [ ] `.age-recipients` round-trip: one public key per line, no gage-only
      framing, so a stock `age`/`passage` CLI could use it as-is
- [ ] `gage init` writes the device's wrapped identity file to
      `$GAGE_DATA/identities/<vault>/<device>.age`, directory `0700` and
      file `0600`
- [ ] `Vault.Unlock` with the correct passphrase returns an `Identity`
      that decrypts an entry encrypted to its public key
- [ ] `Vault.Unlock` with the wrong passphrase returns a distinguishable
      typed error (not a generic error) — no partial or garbage key is
      ever produced
- [ ] `Identity.Close()` releases the page lock and zeroes the key
      material; using the `Identity` after `Close()` fails instead of
      silently succeeding
- [ ] The `memlock` package's `Lock`/`Unlock` round-trip succeeds on the
      current platform (Linux/macOS/Windows, whichever CI runs on) for a
      representative key-sized byte slice
- [ ] A page-lock failure injected via a fake `Locker` doesn't abort
      `Vault.Unlock` — it proceeds and returns a usable `Identity` after a
      single warning via `Prompter`, rather than refusing to unlock
- [ ] The public key derived from the generated identity matches exactly
      what's written to both `.age-recipients` and `.gage/config.toml`'s
      `[[recipients]]`
- [ ] `gage vault list` includes a freshly-`init`'d vault
- [ ] `gage vault info <name>` reports the correct type, method, recipient
      count, and (for `git`) remote + clean/dirty state
- [ ] `gage vault remove <name>` drops it from global config but leaves
      the underlying git repo and its files on disk untouched
- [ ] `gage vault set-default <name>` updates `current` in global config
- [ ] `gage init` without `--remote` succeeds and leaves the vault
      remote-less (no `origin`, no `[vaults.<name>.git]` table in global
      config)
- [ ] `gage git set-remote <name> <url>` sets `origin` on the actual git
      repo (verified by reading git config back via go-git) *and* updates
      `vaults.<name>.git.origin` in global config in the same call
- [ ] `gage git set-remote <name> <new-url>` on a vault that already has
      an `origin` changes it (equivalent to `set-url`), and `vault info`
      reflects the new URL afterward
- [ ] `gage git set-remote` invoked without both `<name>` and `<url>`
      fails with a usage error and makes no change to the repo or global
      config

### Implementation

- [ ] `gage init` (passphrase method only; `--type` flag, default/only
      value `git`, validated against a single-element allowlist; go-git
      `git.PlainInit` + initial commit); passphrase entry goes through the
      library's `Prompter` interface, not a direct stdin read in library
      code
- [ ] Passphrase-method identity generation: fresh X25519 keypair, private
      half wrapped via age's scrypt passphrase recipient, written to
      `$GAGE_DATA/identities/<vault>/<device>.age`
- [ ] `Vault.Unlock(Prompter) (Identity, error)`: decrypts the wrapped
      identity file back into a usable private key via the typed unlock
      exchange; returns distinguishable typed errors (wrong passphrase /
      corrupt identity file / no local identity registered for this
      device); the only place a `Vault` ever obtains an identity. The
      returned `Identity`'s key bytes are page-locked immediately
      (Linux/macOS/Windows — see below); if locking fails, `Unlock` warns
      once via `Prompter` and proceeds unlocked-but-unprotected rather
      than failing the whole operation
- [ ] Cross-platform page-lock package (e.g. `internal/gage/memlock`):
      `Lock([]byte) error`/`Unlock([]byte) error`, implemented with
      build-tag-separated files — `mlock(2)`/`munlock(2)` on Linux and
      macOS (`golang.org/x/sys/unix`), `VirtualLock`/`VirtualUnlock` on
      Windows (`golang.org/x/sys/windows`) — behind one signature `Vault`
      and `Session` both call unchanged regardless of OS
- [ ] `Locker` interface (`Lock([]byte) error`/`Unlock([]byte) error`)
      between `Vault.Unlock`/`Identity.Close` and the `memlock` package —
      the real `memlock`-backed implementation in production and in every
      realistic test, a fake returning a deterministic failure in the one
      page-lock-failure test; the same injectable-seam pattern M7 uses for
      `RemoteSyncer`
- [ ] `Identity.Close()`: releases the page lock and zeroes the private
      key, cross-platform, via the same `memlock` package
- [ ] Process-wide core dump disabling at `cmd/gage` startup:
      `setrlimit(RLIMIT_CORE, 0)` on Linux/macOS; on Windows, suppress the
      Windows Error Reporting crash dialog via `SetErrorMode` (a narrower
      guarantee than POSIX's — see design doc's "Session model")
- [ ] `.gage/config.toml` read/write (`[vault]` section, incl. `type`)
- [ ] `.age-recipients` read/write
- [ ] `gage vault list/info/remove/set-default`
- [ ] `gage git set-remote` (go-git set/update `origin`, sync into
      `vaults.<name>.git.origin` in global config)

## M2 — Entry format + crypto round-trip

The core loop, at the library level, no CLI verb wired up yet: generate
UUID, YAML-encode an entry, encrypt to `entries/<uuid>.age` via the `age`
library against `.age-recipients`, decrypt it back. This is the riskiest
technical piece (crypto correctness) — prove it in isolation before
anything else depends on it.

### Tests (write first)

- [ ] Entry struct marshals to the exact expected YAML shape
      (`title`/`description`/`created`/`updated`/`updated_by`/`value`/`fields`)
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
- [ ] Encrypt entry to `entries/<uuid>.age`
- [ ] Decrypt entry from `entries/<uuid>.age`

## M3 — Dumbest possible CRUD (one-shot mode)

`insert`, `cat`, `rm`, `ls` — `cat` and `rm` addressed by UUID or exact
title match only for now (no fuzzy/ambiguous resolution yet; M4 upgrades
both onto the shared resolver). Every write is a git commit (no push yet).
First end-to-end usable slice: store and retrieve an entry from the
command line, decrypting everything every time (no cache). Each of these
`Vault` methods takes the `Identity` from M1's `Vault.Unlock` as an
explicit parameter and never unlocks internally — `cmd/gage`'s one-shot
handler is what calls `Unlock` → the CRUD method → `Identity.Close()` for
each invocation. M5 reuses these same methods unchanged by caching the
`Identity` across calls instead of closing it after one. `gage insert`'s
value comes from a masked prompt (default), `--value-stdin`, or
`-m|--multiline`; `-e|--edit` (a full `$EDITOR` template) is deferred to
M4, once `gage edit`'s `$EDITOR`-on-scratch-file round trip exists for it
to share.

### Tests (write first)

- [ ] `gage insert` followed by `gage cat` round-trips the value through
      the actual CLI (not just the library)
- [ ] `gage insert --value-stdin` reads the value from stdin (one
      trailing newline trimmed) and round-trips through `cat` identically
      to the default prompt path
- [ ] `gage insert -m` captures multiple lines from the terminal until
      EOF and round-trips through `cat` byte-for-byte
- [ ] With none of `-m`/`--value-stdin` given, `gage insert` prompts once
      for `value` via the library's `Prompter` (a fake in tests) rather
      than reading stdin directly
- [ ] Passing more than one of `-m`/`--value-stdin` is rejected with a
      usage error before any prompt or read happens
- [ ] `gage insert` produces exactly one new git commit
- [ ] `gage ls` lists the inserted entry's title
- [ ] `gage rm` deletes the file under `entries/` and commits the deletion; a
      subsequent `cat`/`ls` no longer shows the entry
- [ ] `gage cat` on an unknown title/UUID fails with a clear error and
      nonzero exit code
- [ ] Inserting a duplicate title without `-f|--force` is rejected;
      `-f|--force` allows it

### Implementation

- [ ] `gage insert`: masked `Prompter` prompt (default), `--value-stdin`
      (read + trim), or `-m|--multiline` (terminal capture until EOF) for
      the value; mutually exclusive, validated before any I/O happens
- [ ] `gage cat` (exact UUID/title only)
- [ ] `gage rm`
- [ ] `gage ls`
- [ ] Commit-per-write (go-git worktree add + commit)

## M4 — Query resolution

Upgrade addressing from "exact UUID/title" to the full resolution order
(prefix → exact → unique substring → ambiguous prompt/fail), and move
every query-taking command onto it — `cat` and `rm` (both stuck on M3's
exact-match-only behavior) alongside the three commands new to this
milestone: `show`, `edit`, `rename`, `generate`. No command should be left
on the old exact-only matching once this milestone is done. Also adds
`-e|--edit` to `gage insert` (deferred from M3), since it shares `gage
edit`'s `$EDITOR`-on-scratch-file round trip — one CLI-layer helper, two
call sites.

### Tests (write first)

- [ ] A UUID prefix resolves to the correct entry
- [ ] An exact title match resolves uniquely even when it's also a
      substring of another entry's title
- [ ] A unique (non-exact) substring match resolves correctly
- [ ] An ambiguous substring match lists all candidates and, in one-shot
      mode, fails with nonzero exit instead of prompting
- [ ] `gage cat` and `gage rm` resolve prefix/exact/substring matches
      exactly like `show` — no longer limited to M3's UUID-or-exact-title-
      only matching
- [ ] An ambiguous query given to `cat` or `rm` lists candidates and fails
      in one-shot mode, exactly like `show` — neither command silently
      acts on the first match or falls back to M3's stricter matching
- [ ] `gage edit` re-stamps `updated`/`updated_by` on save, leaves other
      fields untouched if unedited, and produces a new commit
- [ ] `gage insert -e` opens a template (`title`/`description`
      pre-filled, empty `value`/`fields`) in `$EDITOR`; saving it inserts
      an entry with the edited `value`/`fields`, including `fields` set
      directly at creation — the one `insert` mode that can do that
- [ ] `gage insert -e` aborts with no entry written and no commit if the
      file comes back unchanged, or with `value` still empty and `fields`
      still empty
- [ ] `gage insert -e` changing `title` inside the editor uses the
      edited title, not the original `<title>` argument, for both the
      saved entry and the duplicate-title/`-f` check
- [ ] `editYAML`'s scratch file lands in a verified tmpfs directory on
      Linux and the OS's standard temp directory elsewhere, created
      `0600`
- [ ] The scratch file is removed (contents overwritten first) on every
      exit path — `$EDITOR` exits zero, `$EDITOR` exits non-zero, and the
      edited content fails to parse back into an `Entry`
- [ ] `gage rename` changes only the title (not `value`/`fields`) and
      bumps `updated`
- [ ] `gage generate` inserts a new entry with a randomly generated
      `value` of a sane default length — `-l`/`--no-symbols` customization
      is deferred to M11, tested there

### Implementation

- [ ] Query resolver (prefix/exact/substring/ambiguous), returning a
      resolved entry or a candidate list as a value — never printed text;
      the CLI layer decides whether to prompt (session) or fail (one-shot)
- [ ] `gage cat`/`gage rm` re-wired onto the shared resolver, replacing
      M3's exact-UUID/title-only matching
- [ ] `gage show`
- [ ] Cross-platform scratch-file location for `editYAML`: on Linux,
      verify (via `statfs`) and prefer a tmpfs-backed directory
      (`$XDG_RUNTIME_DIR`, falling back to `/dev/shm`); on macOS/Windows,
      fall back to the OS's standard secure temp directory (`0600`,
      exclusively created)
- [ ] Shared `editYAML` CLI helper: write the scratch file, launch
      `$EDITOR`, read back, detect unchanged, then best-effort overwrite
      before deleting on every exit path (success, non-zero `$EDITOR`
      exit, parse failure) — used by both `gage edit` and
      `gage insert -e`
- [ ] `gage edit` (`editYAML` seeded with the decrypted entry, re-stamps
      `updated`/`updated_by`)
- [ ] `gage insert -e` (`editYAML` seeded with a stub entry instead of a
      decrypted one)
- [ ] `gage rename`
- [ ] `gage generate` (default-length/character-set value only; `-l`/
      `--no-symbols` land in M11)

## M5 — Session mode

The `Session` library type (see design doc's "Library architecture"):
holds one or more vaults' `Identity` values in memory for longer than a
single command. The cross-platform page-locking and zeroing itself
(`mlock`/`VirtualLock`, `Identity.Close()`) is M1's, established the
moment any `Identity` exists — `Session` doesn't add new
memory-protection mechanics here, it just changes how long an
already-protected `Identity` survives before `Close()` runs. Multi-vault
`use`/`lock`/`status`, all M3/M4 commands working against a "current"
vault. `Session.Use` calls the same `Vault.Unlock` from M1 and caches the
resulting `Identity`; `Session.Lock`/idle-timeout call the same
`Identity.Close()` from M1 — no M3/M4 `Vault` method signature changes,
only how many times `Unlock`/`Close` run around them. Windows is an
explicit target here like every other platform: the REPL, idle timeout,
and locking all need to work identically on Windows, Linux, and macOS.
The REPL (`use`, `lock`, `status`, `exit`) is `cmd/gage`'s terminal
rendering of that type — tests below cover `Session` directly wherever
possible, with a thinner REPL-wiring test on top. No metadata index yet —
still decrypt-on-demand per command.

### Tests (write first)

- [ ] `Session.Use(vault)` unlocks once; subsequent entry calls against
      the same session don't re-prompt for the passphrase
- [ ] Entry commands routed through `Session` (`show`, `insert`, ...)
      call the exact same `Vault` methods as one-shot mode — no method
      gained a session-only signature or a duplicate implementation
- [ ] `Session.Lock(vault)` drops that vault's key; the next entry call
      against it re-prompts, while other unlocked vaults in the same
      session are unaffected
- [ ] `Session.Status()` reports correct lock state for multiple vaults
      touched in one session
- [ ] `exit`/EOF terminates the REPL cleanly
- [ ] Idle timeout: with simulated/injected elapsed time past
      `idle_timeout`, the next `Session` call against that vault re-prompts
      as if `Lock` had been called
- [ ] Session command history contains typed command lines (e.g. `show
      protonmail`) but never a decrypted `value`/`fields` value
- [ ] the REPL is a thin wiring layer: it parses a typed line into the
      corresponding `Session` call and renders the result — one wiring
      test suffices here, not a re-test of `Session` behavior
- [ ] The full M5 test suite passes unmodified on Windows, not just
      Linux/macOS — `Session`'s locking/idle-timeout behavior doesn't
      depend on any POSIX-only mechanism

### Implementation

- [ ] `Session` library type: `Use`/`Lock`/`Status`, holding each vault's
      `Identity` (already page-locked and core-dump-protected per M1)
      across multiple calls; idle timeout re-lock calls `Identity.Close()`
      the same as an explicit `Lock`
- [ ] REPL loop in `cmd/gage`: thin terminal wiring over `Session`
      (`use`/`lock`/`status`/`exit`/`help`)
- [ ] Entry commands ported to work against `Session`'s current vault
- [ ] CI/test coverage on Windows in addition to Linux/macOS for this
      milestone specifically — it's the first point session state (idle
      timeout, multi-vault `Identity` caching) needs to be proven to
      behave identically across all three, not just compile

## M6 — Metadata index

In-session decrypt-once cache for `ls`/`search`/`show` resolution,
incremental updates on insert/edit/rm. Pure performance/UX layer on top of
M5 — correctness doesn't change. The index lives on `Session` alongside
each vault's cached `Identity`, never on `Vault` itself — `Vault` stays a
stateless, identity-agnostic operator over ciphertext in both one-shot and
session mode (see design doc's "Library architecture").

### Tests (write first)

- [ ] The first `ls`/`show`/`search` after `use` triggers exactly one
      full-decrypt pass over the vault (assert via decrypt call-count
      instrumentation)
- [ ] Subsequent `ls`/`show`/`search` calls in the same session reuse the
      index without re-decrypting unchanged entries
- [ ] `insert`/`edit`/`rm` update the in-memory index incrementally,
      without triggering a full rebuild
- [ ] `gage reindex` forces a full rebuild and picks up changes made
      outside `gage` (e.g. a manual `git pull`)
- [ ] `search`/`grep` matches against title, description, and body text

### Implementation

- [ ] Build index on first `ls`/`show`/`search` per vault
- [ ] Incremental update on insert/edit/rm
- [ ] `gage reindex`
- [ ] `gage search` / `gage grep`

## M7 — Sync

Needs M5's session lifecycle to hook `use` into, and M3's
commit-per-write already true. These commands stay vault-generic in the
CLI (`sync`/`pull`/`push`), even though, per the design doc's "Vault
types," they're entirely git-implemented today. (`gage git set-remote` —
the one git-*specific* command — was already implemented back in M1;
there's no generic `gage git -- <args...>` passthrough — see the design
doc's "Git-specific commands" for why.)

Test harness for this milestone: real fetch/push/pull/divergence
behavior is tested against ephemeral local bare repos, extending M0's
`gittest.NewBareRemote` with a second-clone helper that simulates
another device pushing independent commits (for the divergence tests) —
no sockets, no real network, satisfying M0's "no network access" test
constraint while still exercising go-git's actual code paths. The one
exception is "network unreachable": `Vault`'s sync methods call through a
small `RemoteSyncer` interface (`Fetch`/`Push` against go-git in
production) so that one test can inject a fake returning a deterministic
unreachable-style error, rather than depending on a real timeout or a
missing-path error that wouldn't exercise the same code path gage's real
offline-handling logic runs.

### Tests (write first)

- [ ] `use` performs a fetch + fast-forward-only pull when the remote has
      commits the local vault lacks
- [ ] A one-shot command (e.g. `gage show`) against a vault with unpulled
      remote commits performs the same fetch + fast-forward-only pull on
      its implicit unlock, with no session `use` step involved — the
      design ties this to every vault unlock, not to the `use` verb
      specifically
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
- [ ] `gittest`'s second-clone helper pushes an independent commit to a
      shared bare remote, producing real divergence when the vault under
      test also has an unpushed local commit
- [ ] The fake `RemoteSyncer` injected in the offline test returns
      exactly the error `Vault`'s sync logic treats as "unreachable" —
      proving the warn-and-proceed path is reachable without depending on
      a real network failure

### Implementation

- [ ] `RemoteSyncer` interface (`Fetch(ctx) error`/`Push(ctx) error`)
      between `Vault`'s sync logic and go-git — real go-git-backed
      implementation in production and in the realistic bare-repo tests,
      a fake in the one offline-handling test where a deterministic
      injected error matters more than a real network failure
- [ ] `gittest` package extended with a second-clone helper: clone a
      `NewBareRemote` repo into a second temp dir, commit there, push
      back — a throwaway stand-in for "another device," used to produce
      real divergence in tests
- [ ] Auto fetch + fast-forward pull on every vault unlock — session
      `use` and a one-shot command's implicit unlock alike (go-git
      `Fetch`/`Pull`, ff-only), so the logic lives where both paths call
      through it rather than being wired into the `use` REPL command only
- [ ] Auto push after writes (go-git `Push`)
- [ ] Divergence detection
- [ ] `gage sync` (conflict surfacing, both versions shown)
- [ ] Manual `pull`/`push` via go-git (both already implemented purely in
      go-git — no passthrough, no `git` binary dependency, anywhere in
      `gage`)

## M8 — Identity/recipient management + atomic reencrypt

First place multi-device/multi-recipient scenarios become testable —
naturally after single-user CRUD+sync are solid.

### Tests (write first)

- [ ] `gage identity add` registers a device and prints a public key
- [ ] Each device registered via `gage identity add` gets its own wrapped
      identity file at `$GAGE_DATA/identities/<vault>/<device>.age`;
      deleting one device's file doesn't affect another device's ability
      to decrypt
- [ ] Recovering from a lost identity file needs no file restore: a fresh
      `gage identity add` on the affected device plus `gage recipient add`
      from any surviving recipient restores access under a new identity
- [ ] `gage recipient add` (no `--reencrypt`) affects only future writes —
      entries that existed before the add remain undecryptable by the new
      recipient's key
- [ ] `gage recipient add --reencrypt` makes all pre-existing entries
      decryptable by the new recipient
- [ ] `gage recipient remove` without `--reencrypt` is rejected outright;
      with `--reencrypt` it re-encrypts every entry, excluding the
      removed key
- [ ] A simulated crash/interruption partway through `--reencrypt` leaves
      HEAD byte-identical to before the command ran — no partial commit,
      no leftover dirty `entries/` once `gage` next starts
- [ ] Retrying `--reencrypt` after such an interruption produces the same
      correct end state as an uninterrupted run
- [ ] A single entry failing during `--reencrypt` (simulated decrypt/
      encrypt error) aborts the whole operation before any commit and
      reports which entry failed; no entries are left partially migrated
- [ ] `--reencrypt` lands the recipient-list files
      (`.age-recipients`/`config.toml`) and every re-encrypted entry in
      exactly one commit — never a commit containing only one side of
      that pair
- [ ] A dirty `entries/` working tree left by a simulated crash is reset
      to HEAD before any subsequent write (`insert`/`edit`/`generate`/
      `--reencrypt`) proceeds, rather than being folded into that write's
      commit
- [ ] `gage recipient verify` exits 0 and reports "in sync" when
      `.age-recipients` and `config.toml` agree; exits 1 and lists the
      specific differences when they don't

### Implementation

- [ ] `gage identity add/list` (reuses M1's passphrase identity-generation
      path for additional devices — each gets its own
      `$GAGE_DATA/identities/<vault>/<device>.age`)
- [ ] `gage recipient add/remove --reencrypt`: stage every re-encrypted
      entry in the working tree first; commit only after all entries
      succeed, with the recipient-list files and every touched entry in
      that same single commit
- [ ] Precondition check on every write (`insert`/`edit`/`generate`/
      `--reencrypt`): if `entries/` is unexpectedly dirty at start, reset
      it to HEAD before proceeding — the only source of unexpected
      dirtiness is an interrupted `--reencrypt`
- [ ] `gage recipient verify`

## M9 — Local trust cache

Builds on M8's recipient add/remove — needs recipient-list changes to
exist to detect changes against — but only needs those files to differ
from a locally-cached copy, not `--reencrypt`'s crash-safety machinery
specifically. Kept as its own milestone so that atomicity work stays
isolated from this one's detection/UX logic, the same way M2 isolates
crypto risk and M7 isolates sync risk.

### Tests (write first)

- [ ] After a device's first successful use of a vault, `known-config.toml`
      is written to local state; a subsequent unreviewed recipient change
      triggers a diff warning before the next encrypt
- [ ] The warning fires the same way for a one-shot `insert`/`edit`/
      `generate` as for a session-mode one — the cache check hooks the
      `Vault` methods that encrypt, not `Session.Use`, since one-shot mode
      never calls `Session.Use` at all
- [ ] Confirming a *routine* recipient change (files agree) regenerates
      the cache and stops warning; confirming a *mismatched* change
      (`.age-recipients` and `config.toml` disagree) does not silently
      clear the cache — `verify` keeps failing until the inconsistency is
      actually fixed
- [ ] the recipient-change warning is a typed value returned by the
      library (diff content, routine-vs-mismatched flag), not printed
      text — `cmd/gage` renders it as the `[y/N]` prompt shown in the
      design doc

### Implementation

- [ ] Local trust cache (`known-config.toml`, diff + warning): the check
      lives on the `Vault` methods that encrypt (`insert`/`edit`/
      `generate`/`rename`/`mv`/`cp`) plus an opportunistic check on
      `use`/`sync`, so it fires identically whether the caller is a
      one-shot command or a session
- [ ] `RecipientChangeWarning` (or similar) structured type returned by
      the library on a trust-cache mismatch; `cmd/gage` renders it as the
      terminal diff + `[y/N]` prompt

## M10 — Cross-vault sharing

Just M2's encrypt primitive pointed at a second vault's recipients —
trivial once M1–M2 exist for two vaults, but distinct enough to be its own
milestone since it's the sharing story.

### Tests (write first)

- [ ] `gage mv --to-vault` removes the entry from the source vault and it
      becomes decryptable in the destination vault under the destination's
      recipients
- [ ] `gage cp --to-vault` leaves the original in the source vault and
      adds a decryptable copy in the destination
- [ ] `mv`/`cp --to-vault` succeeds even when the destination vault is not
      currently unlocked (encrypting to public keys needs no private key)
- [ ] `mv`/`cp --to-vault` runs M9's trust-cache check against the
      *destination* vault's cache (not the source's) before encrypting
      there, and an unreviewed recipient change on the destination
      triggers the same diff warning a same-vault write would
- [ ] `gage clone` against a vault where the local device isn't yet a
      recipient reports that plainly and points at `gage identity add`

### Implementation

- [ ] `gage mv --to-vault`
- [ ] `gage cp --to-vault`
- [ ] `gage clone` (go-git `PlainClone`)

## M11 — Polish / output modes

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
      unlocking each vault at most once

### Implementation

- [ ] `--clip` (clipboard, auto-clear)
- [ ] `--qr` (terminal QR, `--field` scoped)
- [ ] `--field NAME` extraction
- [ ] `generate`'s `-l LENGTH`/`--no-symbols` password-generation logic
      (command itself already exists from M4)
- [ ] `gage log`
- [ ] `gage history --decrypt`
- [ ] Non-interactive session (`--script`, `--stdin`)

---

## Deferred / not on the critical path

- **Additional identity methods** (ssh, yubikey, secure-enclave,
  plugin) — passphrase alone proves the `identity`/`recipient`
  abstraction; add real methods once that interface is stable, otherwise
  you're debugging plugin behavior and architecture at the same time.
- **Additional vault types** — `git` alone proves the vault/backing-store
  split; a second type (e.g. object-storage-backed) is only worth adding
  once the vault-generic vs. git-specific command boundary has been
  exercised by real implementation, not just designed on paper.
- **Signed recipient changes** — design doc calls this out as "reserve
  for high-stakes repos," not a default.
- **`gage clone` against a real remote** — folded into M10, but only
  really testable once there's an actual remote to clone from.
- **A GUI and/or TUI frontend.** The library/CLI split (see design doc's
  "Library architecture") is *not* deferred — it's built in from M0 — but
  actually writing a second frontend is. Once the library's
  interactive-decision interface (`Prompter`, structured warnings/candidate
  lists) is proven by the CLI through M9, a GUI/TUI is a new consumer of
  existing methods, not new core logic.
- **Identity file backup/export tooling.** The recovery story is
  multi-recipient (see M8's lost-identity-file test), not device-key
  backup — `gage` doesn't sync or back up `$GAGE_DATA/identities/` itself.
  A user can copy a device's wrapped identity file manually for extra
  insurance (it's already passphrase-protected ciphertext, so copying it
  isn't unsafe), but that stays a manual, user-owned choice outside
  `gage`, not a command to build.
