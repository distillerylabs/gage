# Contributing to gage

Thanks for your interest! Bug reports, fixes, docs, and features are all
welcome. For anything non-trivial, please open an issue first so we can agree
on the approach before you spend time on it. Security problems should go
through [SECURITY.md](SECURITY.md), not a public issue.

By participating you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
Contributions are licensed under the project's [Apache-2.0 license](LICENSE).

## Setup

Requires the Go version in `go.mod`.

```
make build       # builds ./cmd/gage -> ./gage
make test        # go test ./... with GOPROXY=off (no network, no pre-existing keys)
make lint        # gofmt check + golangci-lint (pinned version, installed to ./bin)
make fmt         # gofmt -w .
make vet         # go vet only
```

Run a single test with `go test ./internal/gage/ -run TestName -v` (or the
relevant package, e.g. `./cmd/gage/`).

## Before opening a PR

`make lint` and `make test` must both be clean. CI runs them natively on
Linux, macOS, and Windows, but it should confirm your change, not discover
problems. Several invariants (page-locking, file locking, core-dump
suppression) depend on real platform syscalls, so please don't assume a change
that passes on one OS passes on all three.

## Conventions

The project's conventions are documented in [CLAUDE.md](CLAUDE.md) (written
for AI coding assistants, but they apply to everyone). The ones that matter
most:

- **Library/CLI split.** All real behavior lives in `internal/gage`;
  `cmd/gage` is a thin Cobra frontend. `internal/gage` must never call
  `fmt.Print*`/`fmt.Fprint*`, use `os.Stdin`/`os.Stdout`, or call `os.Exit`
  outside `_test.go` files. This is enforced by a `forbidigo` lint rule.
  Anything needing a human decision is a typed return value that the CLI
  renders.
- **Tests first, at task granularity.** Write the tests for a change, then the
  implementation. Prefer in-process tests (drive `rootCmd.Execute()` with
  injected IO and a fake `Prompter`); use a real terminal (`creack/pty`) only
  where genuinely needed.
- **No network, no real keys in tests.** Git behavior is tested against
  ephemeral local bare repos (`internal/gage/gittest`), not mocked `go-git`.
  Use the existing seams (`Prompter`, `Locker`, `RemoteSyncer`) for failures
  that can't be produced honestly.
- **Typed, distinguishable errors** at library boundaries so callers decide
  retry and UX policy.
- **Every vault write holds the per-vault lock** (`vaultlock`) across the full
  read-modify-commit sequence.
- **No shelling out to `git`.** All git operations go through `go-git`.
- **Additive over reworked, and no speculative abstraction.** Extend an
  existing typed interface rather than adding a parallel path, and build what
  the change needs.
- **New commands** are registered in the single command registry
  (`cmd/gage/registry.go`), not just wired to a Cobra `Run` func.

Design rationale for most non-obvious decisions is in
[doc/implementation/00_gage-cli/tdds/gage-cli-design.md](doc/implementation/00_gage-cli/tdds/gage-cli-design.md);
please read the relevant section before changing behavior it documents.
User-facing behavior is documented under [doc/cli/](doc/cli/); update it when
you change behavior.

## Dependencies and tooling

Go modules and GitHub Actions are updated by Dependabot. The pinned tool
versions in the `Makefile` (golangci-lint, go-test-coverage, govulncheck) are
not covered by it, so bump those by hand when needed.
