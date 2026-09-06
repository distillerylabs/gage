# gage CLI documentation

`gage` is a command-line secret and notes manager. Every vault is a git
repository; every secret is an age-encrypted entry inside it. This is the
user-facing documentation for the `gage` command — how to install it, create
and use vaults, manage who can read them, and keep multiple devices in sync.

For the project's internal design rationale and build history, see
[doc/implementation/](../implementation/00_gage-cli/) instead — this
directory is written for people *using* `gage`, not developing it.

## Contents

1. [Getting started](getting-started.md) — install, create your first vault, store and read a secret.
2. [Vaults](vaults.md) — creating, cloning, listing, and switching between vaults.
3. [Entries](entries.md) — the entry format, and every command that creates, reads, or edits one.
4. [Addressing entries](addressing-entries.md) — how a typed query resolves to an entry, and what happens when it's ambiguous.
5. [Session mode and scripting](session-mode.md) — the interactive REPL, one-shot invocation, and non-interactive automation.
6. [Identities and recipients](identities-and-recipients.md) — how a device proves it can decrypt a vault, and who else is allowed to.
7. [Sharing entries between vaults](sharing.md) — `mv`/`cp`, and why gage has no per-directory sharing.
8. [Sync and conflicts](sync.md) — how push/pull happen automatically, and how to resolve a real conflict.
9. [Git remotes and authentication](git-and-auth.md) — `git set-remote`, `auth login`, and the HTTPS/token model.
10. [Configuration and file locations](configuration.md) — the global config file, XDG paths, and environment variables.
11. [Command reference](command-reference.md) — every command and flag in one place.
12. [Exit codes](exit-codes.md) — the taxonomy scripts and CI can rely on.

## Conventions used in these docs

- A `gage>` prompt means the command is shown as typed inside an interactive
  session. A bare `$ gage ...` means it's a one-shot invocation from your
  regular shell. Almost every command works both ways — see
  [Session mode and scripting](session-mode.md).
- `<query>` means an argument that resolves to an existing entry (by title or
  ID) — see [Addressing entries](addressing-entries.md). `<title>` means an
  argument that *names a new entry* (`insert`, `generate`).
- Flags shown as `[--use NAME]` are optional everywhere except where a page
  says otherwise.
