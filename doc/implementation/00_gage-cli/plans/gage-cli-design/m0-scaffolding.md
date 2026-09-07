# M0 — Scaffolding

[← plan index](index.md) · next: [M1 — Vault lifecycle & config](m1-vault-lifecycle.md)

> **Recommended model: Sonnet.** Mechanical scaffolding — no design judgment left in it. **Run an Opus review pass over this milestone's tests before moving on:** the `vaultlock` serialization bullet is the archetypal vacuous test (goroutines instead of processes passes, and proves nothing, because `flock` semantics are per-file-descriptor).

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

- ["Library architecture"](../../tdds/gage-cli-design.md) — the
  library/frontend split and why interactive decisions are data
- ["Global config"](../../tdds/gage-cli-design.md) — XDG roles and the
  Windows mapping
- ["Local identity storage"](../../tdds/gage-cli-design.md) — path shape
  only; nothing is written here yet

## Decisions to make first

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
- [ ] A Cobra-native usage rejection (wrong argument count, unknown flag)
      on a real subcommand prints both the original complaint (e.g.
      `accepts 1 arg(s), received 0`) and that subcommand's own usage
      block, not the bare complaint alone; an unrecognized top-level
      command prints the same grouped listing `gage --help` renders. Both
      one-shot mode and a session/script line (`edit` with no query typed
      at the prompt, or read via `--stdin`) go through this
- [ ] A command's own coded error (exit code already set via the
      exit-code taxonomy — wrong passphrase, entry not found, a sync
      conflict) is never expanded with an appended usage block; only
      Cobra's own parsing rejections are
- [ ] Neither help spelling lists the session-only commands
      (`use`/`lock`/`status`/`exit`/`help`) as top-level subcommands —
      they don't exist outside a session, and listing them would send an
      operator down a path that can't work
- [ ] Every command in the registry marked available in one-shot mode
      appears in `gage --help`, and nothing else does — asserted by
      comparing the rendered set against the registry, not against a
      hardcoded list
- [ ] Every registered command has a non-empty short description and a
      group — no command ships undocumented or ungrouped
- [ ] Help output is grouped (vault lifecycle, identity, recipients,
      entry CRUD, sync, git-specific), mirroring the design doc's own
      command-reference sections rather than one flat list
- [ ] A command's aliases (`search`/`grep`, `status`/`whoami`,
      `exit`/`quit`) resolve to the same command and are shown alongside
      the canonical name rather than as separate entries
- [ ] The registry lives in `cmd/gage`: the M0 library-purity lint
      passes, i.e. no command-name metadata leaked into `internal/gage`
- [ ] **Registry completeness is enforced structurally**: a test walks
      the Cobra command tree and fails if any registered Cobra command is
      absent from the registry, or any registry entry has no
      corresponding command. This is what keeps every later milestone
      honest without needing a "remember to register" reminder in eight
      other files — a command added in M9 that skips the registry fails
      M0's test, immediately
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
- [ ] **Command registry** in `cmd/gage` (not `internal/gage` — command
      names are CLI vocabulary; a GUI calls library methods directly and
      never needs a command table). Each entry records:
      - name and aliases (`search`/`grep`, `status`/`whoami`, `exit`/`quit`)
      - short description
      - group, mirroring the design doc's command-reference sections
      - **availability**: session-only, both, or one-shot-only

      `gage --help`/`gage help` render the one-shot-visible set; M6's
      in-session `help` renders the session-visible set. A command added
      in any later milestone registers here or it's invisible to one
      surface or both (Q-HELP-SURFACES, Q-CMD-AVAILABILITY)
- [ ] `gage help` and `gage help <subcommand>` wired as equivalents to
      `gage --help` / `<subcommand> --help`
- [ ] Availability tagging for the commands that exist by the end of M0
      (`help`, `version`); later milestones tag their own as they land.
      The settled full table is in
      [open-questions.md](open-questions.md) — `init` and `clone` are
      the only one-shot-only commands; `use`/`lock`/`status`/`exit`/`help`
      the only session-only ones; everything else is available in both
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
      full sync-testing harness built on top of it lands in M8a)

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
- `gittest.NewBareRemote` is extended, not replaced, in M8a.
