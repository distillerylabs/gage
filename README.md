# gage

[![Test](https://github.com/distillerylabs/gage/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/distillerylabs/gage/actions/workflows/test.yml)
[![Lint](https://github.com/distillerylabs/gage/actions/workflows/lint.yml/badge.svg?branch=main)](https://github.com/distillerylabs/gage/actions/workflows/lint.yml)
[![Vuln](https://github.com/distillerylabs/gage/actions/workflows/vuln.yml/badge.svg?branch=main)](https://github.com/distillerylabs/gage/actions/workflows/vuln.yml)

`gage` (`git` + `age`) is a git-backed, age-encrypted secret and notes manager for the command line, written in Go.

Secrets and notes are stored as individual [age](https://age-encryption.org/)-encrypted files in a git repository. Git handles history, sync, and multi-device distribution; `gage` handles encryption, recipient management, and giving you a fast CLI (and REPL) for storing and retrieving entries. There is no server, no daemon, and no proprietary storage format — a vault is just a git repo you can clone, back up, and inspect like any other.

## Why

- **No server to trust or run.** Vaults sync over plain git remotes (self-hosted, a private GitHub repo, whatever you already use).
- **You already know how to inspect it.** A vault is a normal git repo on disk — `git log`, `git clone`, `git remote` all work as expected.
- **Opaque on disk.** Entries are stored as one `.age` file per entry, named by random UUID. Someone with read access to the repo but not the decryption key sees ciphertext and meaningless filenames — not even titles or categories leak.
- **No long-lived agent process.** Unlocking a vault happens in-process; the decrypted key lives only as long as that `gage` process runs. There's no background daemon or socket holding your key after you've walked away.
- **Pluggable decryption, per device.** Passphrase, a raw age key file, or other identity types can be added later — each device chooses its own method independently of what a vault's other devices use.

## How it works

A **vault** is a git repository plus:

- `.age-recipients` — the public keys of everyone (every device) allowed to decrypt this vault. Recipients are vault-wide; there's no finer-grained per-entry sharing. If you need a different set of readers for a subset of secrets, that's a different vault.
- `.gage/config.toml` — vault-level config (vault type, default decryption method, device metadata).
- `entries/*.age` — one encrypted file per entry, named with a random UUID. Everything a human would recognize — title, description, the secret value itself, arbitrary structured fields — lives *inside* the encrypted payload as YAML.

Every write to a vault is a git commit; `gage sync` pulls, merges what can be cleanly merged, and pushes. Because entries are opaque encrypted blobs, git can't three-way-merge a genuine conflict (both sides changed the same entry) — that case is always surfaced to you rather than silently resolved.

## Getting started

```
gage init myvault              # create a new vault
gage insert github-token       # create an entry (prompts for its value)
gage show github-token         # print the entry's value
gage ls                        # list entries in the current vault
```

`gage` can also be driven interactively as a session/REPL — unlock a vault once and run multiple commands (`use`, `show`, `insert`, `search`, ...) against it without re-authenticating each time:

```
gage
gage> use myvault
gage> ls
gage> exit
```

Run `gage help` for the full command reference, or see
[doc/cli/](doc/cli/) for complete usage documentation.

### Command overview

| Group | Commands |
|---|---|
| Vault lifecycle | `init`, `clone`, `vault list`/`info`/`remove`/`set-default` |
| Identity | `identity add`, `identity enroll`, `identity list` |
| Recipients | `recipient add`/`remove`/`list`/`verify`, `recipient pending`/`approve`/`deny` |
| Entry CRUD | `insert`, `show`, `cat`, `edit`, `rename`, `generate`, `rm`, `mv`, `cp`, `ls`, `search`, `reindex` |
| Sync | `sync`, `pull`, `push`, `log`, `history` |
| Git-specific | `git set-remote`, `auth login`/`status`/`logout` |
| Session (REPL only) | `use`, `lock`, `status`, `exit`, `help` |

`mv`/`cp` move or copy an entry between vaults with different recipient lists — that's `gage`'s mechanism for sharing a secret with a subset of people rather than a whole vault.

`identity enroll` and `recipient pending`/`approve`/`deny` are how a second device joins a vault without hand-carrying a public key: the joining device publishes a sealed request and prints a short code, and an existing recipient approves it with `gage recipient approve --code ...`. See [doc/cli/enrollment.md](doc/cli/enrollment.md).

## Building and testing

```
make build       # builds ./cmd/gage -> ./gage
make test        # go test ./... (no network access, no pre-existing keys required)
make lint        # gofmt check + golangci-lint
make fmt         # gofmt -w .
make vet         # go vet only, no full lint
```

Requires Go 1.26+.

## Project layout

- `internal/gage` — the library: all real behavior (vault lifecycle, entry CRUD, identity/recipient management, sync, session handling) lives here.
- `cmd/gage` — a thin [Cobra](https://github.com/spf13/cobra) CLI frontend over that library.

The library/CLI split is intentional and enforced by lint rules: `internal/gage` never touches stdin/stdout or prints directly, so the same library can back other frontends (a future GUI/TUI) without rework.

## Status and documentation

`gage` is under active milestone-based development. Full design rationale lives in [doc/implementation/00_gage-cli/tdds/gage-cli-design.md](doc/implementation/00_gage-cli/tdds/gage-cli-design.md); the build plan (milestones and cross-milestone contracts) lives in [doc/implementation/00_gage-cli/plans/gage-cli-design/index.md](doc/implementation/00_gage-cli/plans/gage-cli-design/index.md).

## License

`gage` is licensed under the [Apache License 2.0](LICENSE).
