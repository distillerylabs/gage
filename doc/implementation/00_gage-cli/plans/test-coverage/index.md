# Raising test coverage toward 90% — implementation plan

Tracked by GitHub issue [#27](https://github.com/distillerylabs/gage/issues/27); one sub-issue per milestone below.

Two sequential milestones that close the honest gaps in the test suite
without touching production code. Same rules as the
[core plan](../gage-cli-design/index.md): each milestone leaves the tool
runnable and testable, none reworks a prior one, and the test list is the
definition of done.

**This plan does not restate the shared ground.** Layering rules
(library returns data, `cmd/gage` prints), library choices, test
conventions and model-selection guidance live in the
[core plan index](../gage-cli-design/index.md) and apply unchanged. The
decision register is the core plan's
[open-questions.md](../gage-cli-design/open-questions.md), entry
`Q-COVERAGE`.

## Why

The gate in [.testcoverage.yml](../../../../../.testcoverage.yml) is a
total of 80, and the figure visible in a `make cover` run looked like it
was "just above 80%" — one bad merge from a red gate.

It is not. Measured on `main` at 3c548c7:

| Figure | Value |
|---|---|
| Total excluding `scripts/` — what `cover-check` gates on | **86.55%** (5625/6499 statements) |
| Total including `scripts/` — what `go tool cover -func` prints | 86.09% |
| The `cmd/gage` test binary's slice of all packages | **80.4%** |

The 80.4% is the per-package line `make cover` prints
(`ok .../cmd/gage ... coverage: 80.4% of statements in ./...`): one test
binary's view of the whole module under `-coverpkg=./...`, not the merged
total. Read as a gate figure it is wrong by 6 points, which is worth
knowing before anyone reacts to it again.

The real reason to do this work is the other half. Of the 874 uncovered
statements, a meaningful subset is untested *behaviour* rather than
plumbing:

- `Vault.Search` never matching on a **field value** — the `matchesBody`
  field loop has no test at all, despite being half of what `search` is for.
- The recovery-key destination checks under `$GAGE_DATA` — a security
  boundary deciding where an unencrypted private key is allowed to land.
- The prompter's retry ceilings (`maxChooseAttempts`,
  `maxConflictAttempts`) and its refusal to guess at an unrecognized
  conflict answer.
- The whole `unknownValueNode` type switch in `entry.go`, which is what
  keeps a future gage's fields intact through a round trip.
- `RemoteState.String()`, `defaultBranchOf`'s branch selection, and the
  no-commits-yet paths in `gitrepo`.

Those are worth closing regardless of what the gate says.

## Resolved decisions

| ID | Decision |
|---|---|
| D1 | Target ~90% total, reached only through tests that assert real behaviour. No coverage-driven tests that assert nothing. |
| D2 | No new fault-injection seams. Existing seams (`Prompter`, `Locker`, `RemoteSyncer`) only, per the core plan's test conventions. |
| D3 | `threshold.total` stays at **80**. The new coverage is a cushion, not a new floor — raising the gate to just under the new figure re-creates the same brittleness one milestone later. |
| D4 | `internal/gage/gittest` is excluded from the gate and from Codecov, alongside the existing `^scripts/`. |
| D5 | No new pty tests. Everything in-process. |
| D6 | No production code changes. The only non-`_test.go` files this plan touches are `.testcoverage.yml` and `codecov.yml`, in R1's last task. |

## What is deliberately not tested

Roughly 600 of the 874 uncovered statements are the same shape:

```go
if err != nil {
    return fmt.Errorf("gitrepo: opening %s: %w", dir, err)
}
```

— wrappers around `os.Stat`, `os.MkdirAll`, `os.Chmod`,
`git.PlainOpen`, `atomicfile.WriteFile` and `go-git` calls that fail only
on a filesystem or repository fault. Covering them needs fault injection
that does not exist in this codebase and should not be added: the core
plan's rule is that the real implementation runs in production *and in
every realistic test*, with a fake going through an existing seam only
for the specific failure being proven. A mock filesystem layered under
`internal/gage` to reach an `os.Chmod` error branch would invert that.

This is recorded here so it is not re-litigated. If a future milestone
genuinely needs one of these paths tested, the right move is to argue for
the seam on its own merits, not to reach for it in pursuit of a number.

Also not covered, and not worth it:

- `cmd/gage/main.go` — 6 statements, reachable only from a real process.
- The `t.Fatalf` branches in `internal/gage/gittest` — test scaffolding,
  excluded from the gate by D4 rather than tested.

## Design facts the milestones rely on

- `-coverpkg=./...` in `make cover` means every test binary emits a
  profile block for every package, so `coverage.out` contains the same
  block many times over. `go tool cover` merges them; a naive per-file
  script does not, and will report a wildly wrong figure (8.8% instead of
  86.5%). Use `go tool cover -func`.
- `make cover-check` installs `go-test-coverage` from the network, so it
  cannot run under the `GOPROXY=off` the rest of the toolchain uses.
  `go tool cover -func=coverage.out | tail -1` is the offline equivalent;
  its total includes `scripts/`, so it reads ~0.5pp below the gated figure.
- `internal/gage`'s test binary lowers both scrypt work factors
  (`SetScryptWorkFactorForTests`), so an added test that unlocks a vault
  costs milliseconds, not a second. The same holds in `cmd/gage`.
- Both packages already have the harness these tests need:
  `isolateXDG`, `registerVault`, `fakePrompter`, `countingLocker`
  ([internal/gage/testsupport_test.go](../../../../../internal/gage/testsupport_test.go));
  `runCLI`, `runCLIWithPrompter`, `runCLIWithApp`, `runSessionScript`,
  `script`, `initEntryTestVault`
  ([cmd/gage/testutil_test.go](../../../../../cmd/gage/testutil_test.go)).
  Nothing here should need a new helper.
- `internal/gage/gitrepo`'s tests build ephemeral bare repos through
  `gittest`, never a real socket. The `defaultBranchOf` tests in R0 need
  a bare remote with hand-placed branch refs, which `gittest` supports.

## Milestones

Legend: `[ ]` not started, `[~]` in progress, `[x]` done. Branches are
stacked: `main` <- `27-test-coverage` (these docs) <- R0 <- R1.

| # | Milestone | Model | Depends on | Status |
|---|---|---|---|---|
| R0 | [Library coverage](r0-library-coverage.md) | Sonnet | none | `[ ]` |
| R1 | [CLI coverage and the gate config](r1-cli-coverage.md) | Opus | R0 | `[ ]` |

R0 is additive, well-specified, pattern-following library work — Sonnet's
case exactly. R1 is Opus because its first tasks are a security boundary
(where an unencrypted recovery private key is allowed to land) and its
prompter, clipboard and secret-output tasks touch secret handling; the
rest of R1 is rendering and would not on its own justify it.

## Process notes

- Each milestone ends green on `make lint` and `make test`, and reports
  its measured coverage total in the sub-issue before review.
- A milestone is not done because most of its list is. The failure mode
  to watch for here is the usual one — quiet scope narrowing — and it is
  easier than usual to hide behind a coverage percentage that went up.
  Check the boxes against the list, not the number.
- If a listed test turns out not to be worth writing (it asserts nothing
  a reader would miss), strike it from the list *in the doc* with a
  one-line reason rather than silently skipping it.
