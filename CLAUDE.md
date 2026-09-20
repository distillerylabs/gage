# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`gage` (`git` + `age`) is a git-backed, age-encrypted secret & notes manager, written in Go. Full design rationale lives in [doc/implementation/00_gage-cli/tdds/gage-cli-design.md](doc/implementation/00_gage-cli/tdds/gage-cli-design.md); the build plan (milestones, dependency graph, cross-milestone contracts) lives in [doc/implementation/00_gage-cli/plans/gage-cli-design/index.md](doc/implementation/00_gage-cli/plans/gage-cli-design/index.md) with one file per milestone alongside it. Read the relevant section of the design doc before changing behavior it documents — most non-obvious decisions in this codebase (why a field lives where it does, why a check exists) are explained there, not in code comments.

## Commands

```
make build       # builds ./cmd/gage -> ./gage
make test        # go test ./... with GOPROXY=off (no network access, no pre-existing keys)
make lint        # gofmt check + golangci-lint (pinned version, installed to ./bin)
make fmt         # gofmt -w .
make vet         # go vet only, no full lint
```

Run a single test: `go test ./internal/gage/ -run TestName -v` (or the relevant package path, e.g. `./cmd/gage/`).

`golangci-lint` is pinned (see `GOLANGCI_VERSION` in the Makefile) and installed into `./bin`, not resolved from `PATH` — `make lint` handles installing it.

CI (`.github/workflows/test.yml` for build/test/coverage, `lint.yml` for lint) runs natively on Linux, macOS, and Windows — not cross-compiled. `vuln.yml` runs `govulncheck` once on Linux. Several invariants (page-locking, vault file locking, core-dump suppression) only hold if real platform syscalls run, so cross-platform breakage can't be caught on one OS alone.

## Development workflow

**Never commit or push on your own initiative.** Leave all changes staged/unstaged in the working tree for the user to review locally. Only run `git commit` or `git push` when the user explicitly asks for that specific action in the current request — a prior commit/push approval does not carry forward to later changes.

**Plan mode always ends in a GitHub issue and a branch, before any implementation.** When a session starts in plan mode, the outcome of that planning is not code — it is:
1. A GitHub issue (`gh issue create`) documenting the feature/fix: the problem, the agreed approach from the plan, and the acceptance criteria (for milestone work, the test list from the plan doc).
2. A branch created for that issue (e.g. `gh issue develop <N> --checkout`, or `git checkout -b <N>-short-slug`) off `main`, which is where all implementation work happens.

Only after both exist may implementation begin. Never start implementing straight out of plan mode without first documenting the work in a GitHub issue, and never implement on `main`. Reference the issue number in the branch name, and leave the branch uncommitted/unpushed per the rule above unless asked. Creating the issue and branch is the one step here that is expected as part of plan-mode output; if the plan is not yet settled or the user hasn't approved it, ask before filing.

**Work that needs more than one milestone uses a parent issue, sub-issues, and a stack of branches.** When a plan is too big for one reviewable change, it is split into milestones and set up as follows. This is the standing process; don't wait to be re-told it. (Single-milestone work keeps the simple issue + branch flow above.)

*Splitting the work.*
- Aim for milestones that are each independently reviewable and mergeable, ending green on `make lint` and `make test`, with a strictly linear dependency order. Typically 2-4; more usually means the scope should be cut instead.
- Split by risk and layer: library first (`internal/gage`, additive and easy to test), then CLI integration (`cmd/gage`), then any secondary command plus docs last so docs describe shipped behavior. Isolate security-sensitive or crash-safety work in its own milestone.
- Recommend a model per milestone in the plan, following the core plan's model-selection guidance: Opus for security-sensitive ordering, secret handling, or crash-safety reasoning; Sonnet for well-specified, additive, pattern-following work and docs.
- Any decision the plan makes is recorded as resolved in the plan docs, with a `Q-<NAME>` entry in [doc/implementation/00_gage-cli/plans/gage-cli-design/open-questions.md](doc/implementation/00_gage-cli/plans/gage-cli-design/open-questions.md).

*When the plan is approved (still no implementation):*
1. File one **parent issue** (`gh issue create`): problem, agreed approach, resolved decisions, the milestone table, overall acceptance criteria, and the plan-doc paths.
2. Create the parent's branch off `main` with `gh issue develop <P> --checkout --name <P>-<slug>`. Note this also creates the branch on the remote, which is expected.
3. On that branch, write **all** the plan docs under `doc/implementation/00_gage-cli/plans/<feature-slug>/`: `index.md` (why, resolved decisions, milestone table with model/dependency/status columns, process notes) plus one `r<N>-<slug>.md` per milestone in the existing milestone-doc layout (nav line, "Recommended model" blockquote, Goal, Depends on, Design references, Decisions to make first, tests-first task checklist, Definition of done).
4. File one **sub-issue per milestone**, each attached to the parent through the sub-issue API (`gh api -X POST repos/<owner>/<repo>/issues/<P>/sub_issues -F sub_issue_id=<numeric id from .id>`, not the issue number). Each body links its plan doc (a blob URL on the parent branch), states the recommended model and dependency, and says the plan doc's test list is the definition of done.
5. Stop and wait for the user to review, commit, and open the PR for the parent branch. No sub-issue branches yet.

*Implementing a milestone (only when the user says to start it):*
1. Branches are a **stack**, everything ultimately merging down into `main`: `main` <- parent branch (plan docs) <- R0 <- R1 <- R2. Cut each milestone's branch off the *previous* milestone's branch (R0 off the parent branch), linked to its sub-issue: `gh issue develop <sub-N> --checkout --base <previous-branch> --name <sub-N>-<slug>`. If `gh` insists the base exist on the remote, fall back to `git checkout -b <sub-N>-<slug>` from the previous local branch.
2. Check `git status` first and stop on a dirty tree: uncommitted changes follow a checkout, so the previous phase must already be committed by the user.
3. Follow the milestone doc's tests-first task list, keep its checkboxes and the index's status column current, and finish with `make lint` and `make test` clean.
4. Stop at the end of the milestone and wait for review. Never start the next milestone unprompted. Merge order for the stack is the user's call.

The no-commit/no-push rule above applies throughout: the exception is only what the user explicitly asks for in the current request (for example "commit these and create a PR"). When writing a `gh` body with backticks, use a quoted heredoc or `--body-file` and don't escape the backticks, which otherwise appear literally.

**Before considering any change done:** `make lint` (gofmt check + golangci-lint) and `make test` must both be clean, `go vet ./...` is a fast intermediate check while iterating. Don't leave this for CI to catch — CI's only job is cross-platform confirmation (Linux/macOS/Windows), not first-pass discovery.

**Write tests first, at task granularity.** Write a task's tests before its implementation, then implement to green, then move to the next task — don't write the whole implementation and backfill tests after, and don't try to author a whole milestone's test suite upfront against APIs that don't exist yet. This is the project's actual convention (see "Test conventions" in [doc/implementation/00_gage-cli/plans/gage-cli-design/index.md](doc/implementation/00_gage-cli/plans/gage-cli-design/index.md)), not a generic suggestion.

**If you're working from a milestone plan doc** (`doc/implementation/00_gage-cli/plans/gage-cli-design/m*.md`):
- Resolve any open "Decisions to make first" in the doc itself — get them reviewed — before writing any code for that milestone. A decision settled in the doc is durable across sessions; one made implicitly in code has to be re-derived by whoever reads it next.
- The doc's test list is the definition of done, not a nice-to-have checked loosely at the end. A milestone isn't done until every item in that list is green (on all three CI platforms, unless the doc says otherwise). When judging whether a milestone is complete, check the actual list in the doc — the failure mode to watch for is quiet scope narrowing: most of the list implemented and reported as done.
- If context runs low mid-milestone, write a `*-sessionhandoff.md` in the same plans directory for a reader with zero conversation history, rather than pushing through with degraded context.

**Testing conventions used throughout this codebase:**
- Prefer in-process tests: drive the CLI via `rootCmd.Execute()` with injected IO and a fake `Prompter`. Reserve real-terminal tests (`creack/pty`) for the few paths that genuinely need one (masked passphrase entry, `insert -m`'s multiline capture) — if a test *can* be written in-process, it should be.
- Never rely on real network access or pre-existing keys (`make test` runs with `GOPROXY=off`). Exercise git behavior against ephemeral local bare repos (`gittest`) instead of mocking `go-git`; exercise "network unreachable" via an injected fake `RemoteSyncer` instead of a real timeout.
- When a failure mode can't be produced honestly in a test, inject a fake through an existing seam (`Prompter`, `Locker`, `RemoteSyncer`) rather than adding a new bespoke one — the real implementation should run in production and in every realistic test; fakes exist only for the specific failure being proven.

**General practices, project-specific and otherwise:**
- Additive over reworked. The whole milestone plan is structured so a later milestone extends an earlier abstraction rather than rewriting it — prefer adding a new case/`Kind`/allowlist entry to an existing typed interface over introducing a parallel path.
- Typed, distinguishable errors at library boundaries (e.g. `Unlock` returns wrong-passphrase vs. corrupt-file vs. no-identity-registered as different errors), so the caller — not the library — decides retry/UX policy.
- No speculative abstraction: build what the current milestone/task needs, not scaffolding for a later one that hasn't started (e.g. don't build multi-vault-type plumbing while there's only `git`).
- Respect the library/CLI purity rule (below) as a correctness property, not a style nit — it's what keeps a future GUI/TUI addable without reworking existing commands.

## Architecture: the library/CLI split is load-bearing

All real behavior lives in `internal/gage` (the library); `cmd/gage` is a thin Cobra frontend. This isn't a style preference — a GUI/TUI is planned as a second in-process frontend over the same library later, and the split is enforced by a custom `golangci-lint` `forbidigo` rule (see `.golangci.yml`), not just convention:

- `internal/gage` **must never** call `fmt.Print*`/`fmt.Fprint*`, reference `os.Stdin`/`os.Stdout`, or call `os.Exit`, outside `_test.go` files. Violations fail lint, with a message pointing back at this rule.
- Anything needing a human decision (ambiguous query match, recipient-change warning, unlock prompt) is a **typed return value** — a candidate list, a warning struct, an `UnlockRequest` — never printed text or a direct stdin read. `cmd/gage` decides how to render it (terminal prompt today; a dialog/picker for a future GUI).
- `Identity` is a parameter, never internal state: `Vault.Unlock(Prompter) (Identity, error)` is the only thing that produces one, and every `Vault` CRUD method takes it explicitly. `Vault` itself is stateless and identity-agnostic in both invocation modes.
- The in-session metadata index (decrypt-once cache of title/description/dates) lives on `Session`, never on `Vault`.

When adding behavior, put it in `internal/gage` and add a thin `cmd/gage` command on top — not the other way around.

### Core library types (`internal/gage`)

- **`Vault`** — one vault's on-disk state (config, recipients, entries). Vault-generic CRUD (`init`, `ls`, `insert`, `recipient add`, ...), independent of whether anything is unlocked.
- **`Session`** — process-scoped: one or more unlocked `Vault`s, their `Identity`s, and each vault's metadata index. Backs both the interactive REPL and non-interactive `--script`/`--stdin` mode.
- **`Entry`** — a single decrypted record (YAML: `title`, `description`, `created`/`updated`/`updated_by`, `value`, `fields`).

Supporting packages under `internal/gage/`: `agekey`, `atomicfile` (temp+rename writes), `config`, `devicename` (validates/normalizes device names — these become filesystem path components, so validation is a security boundary, not cosmetic), `exitcode` (the taxonomy every command maps errors to), `gitrepo`/`gittest` (go-git wrapper + ephemeral bare-repo test harness — no real network in tests), `memlock` (`mlock`/`VirtualLock` behind one signature), `recipients`, `remoteauth`, `syncerr`, `vaultconfig`, `vaultlock` (the per-vault advisory `flock`/`LockFileEx`), `xdgpaths`.

### Key invariants worth knowing before touching related code

- **Entries are opaque.** One `.age` file per entry under `entries/`, named by random UUIDv4 — no human-readable path or directory structure ever encodes vault contents. All metadata (title, etc.) is inside the encrypted payload.
- **A vault is the unit of trust.** Recipients (who can decrypt) are vault-wide, never per-directory — see "Why no per-directory sharing" in the design doc if you're tempted to add finer-grained sharing.
- **Decryption method is per-device, not per-vault.** `[method].default` in `.gage/config.toml` is a suggestion for new devices, not a constraint; each device's actual method is local-only config, never committed.
- **Query resolution order is title-first**: exact title → substring title → exact UUID → substring UUID → ambiguous (list candidates). This order is deliberate (see design doc "Addressing entries") — don't reorder it without reading why.
- **Writes are commit + push; divergence is never auto-resolved.** A real conflict (both sides changed the same entry) is always kicked to a human via `gage sync`, since `.age` files are opaque binary blobs git can't three-way-merge.
- **Every write holds the per-vault advisory lock** (`vaultlock`) across the full read-modify-commit sequence. Reads don't take it. Don't add a write path that skips this.
- **No shelling out to `git`** — all git operations go through `go-git`. Consequence: `~/.ssh/config`, credential helpers, and `insteadOf` rewrites are not honored; the supported remote auth path is HTTPS + token (`gage auth login`).
- **Tests run with no network** (`GOPROXY=off`) and no pre-existing keys. Remote/git behavior is tested against ephemeral local bare repos (`gittest`), not real sockets. Network-unreachable behavior is tested via an injected fake `RemoteSyncer`, not a real timeout.
- **Injectable seams for otherwise-untestable failures:** `Prompter` (every human decision), `Locker` (page-lock failure), `RemoteSyncer` (network unreachable). Use the real implementation everywhere except the specific test proving that failure mode.

### Command registry

`cmd/gage` commands are registered in one place with name, aliases, description, group, and one-shot/session availability, so `gage help` and in-session `help` render from a single source rather than two hand-maintained lists. Adding a command means registering it there, not just wiring a Cobra `Run` func.
