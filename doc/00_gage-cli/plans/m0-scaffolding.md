# M0 — Scaffolding

[← plan index](index.md) · next: [M1 — Vault lifecycle & config](m1-vault-lifecycle.md)

## Goal

Project setup, build tooling, CI, and the CLI/config skeleton — no crypto,
no vaults yet, just prove the plumbing. Language/toolchain: **Go** (single
static binary, mature `age` library, easy cross-compilation for the
Windows/macOS/Linux XDG story).

M0 also decides several shapes before there's any real logic to misplace:
the library/CLI split, the `Prompter` and `Identity` interfaces, the
advisory lock primitive, atomic config writes, and the exit-code
taxonomy. All of these are cheap to decide now and expensive to retrofit.

## Depends on

Nothing.

## Design references

- ["Library architecture"](../tdds/gage-cli-design.md) — the
  library/frontend split and why interactive decisions are data
- ["Global config"](../tdds/gage-cli-design.md) — XDG roles and the
  Windows mapping
- ["Local identity storage"](../tdds/gage-cli-design.md) — path shape
  only; nothing is written here yet

## Decisions to make first

- **[Q-HELP-SURFACES](open-questions.md)** — `gage --help`/`gage help`
  and in-session `help` are two different surfaces that share the entry
  commands. Decide how they stay in sync (recommendation: one command
  registry tagged by availability, so both derive from it) before writing
  either, since retrofitting a registry after two hand-maintained lists
  exist is the expensive order.
- **Exit-code taxonomy.** The design specifies codes ad hoc (`recipient
  verify` exits 0/1; an ambiguous query fails nonzero). Define the full
  set centrally now — success, usage error, not-found, ambiguous,
  locked/auth failure, conflict, internal — or they'll be inconsistent by
  M12.
- **Contended-lock behavior.** When `vaultlock` is held by another
  process: block with a message naming the holder, or fail fast? Block
  with a message is the recommendation, with a timeout.

## Tests (write first)

- [ ] `make build` produces a `gage` binary
- [ ] `make test` runs with no network access and no pre-existing keys
- [ ] `make lint` and `make fmt --check` exit 0 on a clean tree
- [ ] a lint check fails if `internal/gage` (the library layer) references
      `os.Stdin`/`os.Stdout` or calls `fmt.Print*`/`os.Exit` outside
      `_test.go` files — keeps CLI-only I/O out of the library from day one
- [ ] `gage --help` exits 0 and lists the registered top-level subcommands
- [ ] `gage help` exits 0 and produces output identical to `gage --help` —
      the spelling every operator tries first
- [ ] `gage help <subcommand>` prints that subcommand's usage and exits 0;
      `gage help <unknown>` fails with the usage exit code
- [ ] Neither help spelling lists the session-only commands
      (`use`/`lock`/`status`/`exit`) as top-level subcommands — they don't
      exist outside a session, and listing them would send an operator
      down a path that can't work
- [ ] Every registered top-level subcommand appears in `gage --help` with
      a non-empty short description — no command ships undocumented
- [ ] `gage --version` prints a version string including the build's
      commit and, on a tagged build, the tag
- [ ] Bare `gage` with a TTY on stdin enters session mode rather than
      printing help (Q-ROOT-CMD) — asserted at the dispatch level in M0;
      the session itself is M6's
- [ ] Bare `gage` with stdin piped or redirected prints help instead of
      entering a session, leaving `--stdin` as the one spelling for
      "read session commands from stdin"
- [ ] XDG path resolution: correct config/data/state paths on Linux/macOS
      with `XDG_*` env vars set, and with them unset (defaults)
- [ ] XDG path resolution: correct `%APPDATA%`/`%LOCALAPPDATA%`-based paths
      on Windows
- [ ] Global `config.toml` round-trip: write a `vaults.*`/`current` struct,
      read it back, fields match exactly
- [ ] An interrupted global-config write (simulated failure between temp
      write and rename) leaves the previous `config.toml` intact and
      parseable — never a truncated file
- [ ] Local identity path resolves to `$GAGE_DATA/identities/<vault>/<device>.age`
      — a sibling of `$GAGE_DATA/vaults/`, never nested inside a vault's own
      directory
- [ ] `vaultlock`: two acquisitions of the same lock path from
      independent processes are serialized — the second observes the
      first's hold rather than proceeding concurrently
- [ ] `vaultlock`: releasing lets the waiter proceed; a process exiting
      while holding the lock releases it (no stale lock survives a kill)
- [ ] `vaultlock` round-trips on the current platform, Linux/macOS
      (`flock`) and Windows (`LockFileEx`) alike
- [ ] Every defined exit code is produced by at least one code path, and
      no command returns a bare `1` that isn't in the taxonomy
- [ ] `gittest.NewBareRemote` (or similar) creates an ephemeral local
      bare repo under `t.TempDir()` and returns its path; two calls
      produce independent, non-colliding repos
- [ ] CI runs `make build`/`make test`/`make lint` on Linux, macOS, *and*
      Windows runners — actual native execution, not cross-compilation,
      since later milestones' Windows-specific claims (M0's `vaultlock`,
      M2's `mlock`/`VirtualLock`, M6's session/idle-timeout parity) rely on
      syscalls that only run correctly on their native platform

## Implementation

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
- [ ] Cobra root command + subcommand dispatch skeleton; `--version` with
      build metadata injected at link time. Bare `gage` dispatches to
      session mode on a TTY and to help otherwise (Q-ROOT-CMD) — the
      session handler itself is a stub until M6
- [ ] Command registry: one place recording each command's name, short
      description, and where it's available (one-shot, session, or both).
      `gage --help`/`gage help` and M6's in-session `help` both render
      from it, so a command added later can't appear in one surface and
      not the other (Q-HELP-SURFACES)
- [ ] Exit-code taxonomy as a single library-side enum, rendered to
      process exit codes by `cmd/gage` (the library itself never calls
      `os.Exit`)
- [ ] skeleton `Prompter` (or similar) callback interface in
      `internal/gage` for interactive decisions (confirm, disambiguate,
      and unlock) — implemented against stdin/stdout by `cmd/gage`,
      satisfied by a fake in library tests. The unlock exchange is typed
      and method-agnostic (`UnlockRequest`/`UnlockResponse` carrying a
      `Kind`, e.g. `"passphrase"`), not a passphrase-specific method, so
      a second identity method later is additive rather than a breaking
      interface change
- [ ] skeleton `Identity` type (wraps unlocked private key material) with
      a `Close()` method, and the `Vault.Unlock(Prompter) (Identity,
      error)` method signature — no real crypto behind either yet (that's
      M2), but deciding the shape now is what lets every later CRUD method
      take `Identity` as an explicit parameter instead of unlocking
      internally, so M6's `Session` can cache one across calls without
      reworking M4/M5
- [ ] `internal/gage/vaultlock`: cross-platform advisory file lock —
      `flock(2)` on Linux/macOS (`golang.org/x/sys/unix`),
      `LockFileEx` on Windows (`golang.org/x/sys/windows`) — behind one
      signature, with a timeout and a typed contended error carrying
      whatever the holder recorded. Not used by anything yet; M4 is the
      first caller
- [ ] Atomic TOML write helper (write temp in the same directory, fsync,
      rename) used by every config writer in the codebase — global
      config here, `.gage/config.toml` in M1, `known-config.toml` in M10.
      Cheap now; prevents a truncated global config from losing every
      registered vault
- [ ] XDG path resolution (config/data/state, with Windows mapping)
- [ ] Local identity path helper: `$GAGE_DATA/identities/<vault>/<device>.age`
      (directory `0700`, file `0600`)
- [ ] Global `$GAGE_CONFIG/config.toml` read/write
- [ ] Test harness: in-process `rootCmd.Execute()` runner with injected
      IO and a fake `Prompter`, plus a `creack/pty` helper for the small
      number of tests that must drive a real terminal (first used in M2)
- [ ] `internal/gage/gittest` test-helper package: `NewBareRemote(t)
      string` wraps `t.TempDir()` + `git.PlainInit(path, true)` to create
      an ephemeral local bare repo for use as a fake remote in later
      milestones' tests (first real use: M1's `git set-remote` tests; the
      full sync-testing harness built on top of it lands in M8)

## Definition of done

Full test list green on all three CI platforms. `gage --help`,
`gage help`, and `gage --version` work, and bare `gage` dispatches
correctly by TTY; nothing else does yet, and that's correct.

## Affects later milestones

- `Prompter`, `Identity`, and `Vault.Unlock`'s signature are fixed here —
  M2 fills them in, M4/M5 consume them, M6 caches across them.
- `vaultlock` is unused until M4 but its semantics (blocking, timeout,
  typed contention error) are settled here.
- The command registry is what M6's in-session `help` renders from, and
  what every milestone adding a command must register into. A command
  that isn't in the registry is invisible to one help surface or both.
- The atomic-write helper and exit-code taxonomy are expected by every
  later milestone; using anything else is a review failure.
- `gittest.NewBareRemote` is extended, not replaced, in M8.
