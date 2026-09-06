# Git remotes and authentication

`gage` never shells out to a `git` binary — every git operation goes
through [go-git](https://github.com/go-git/go-git). That has one consequence
worth knowing up front rather than discovering by surprise: **go-git does
not read `~/.ssh/config`, git credential helpers, or `insteadOf` rewrites.**
SSH host aliases, `IdentityFile`, `ProxyCommand` — none of it applies. A
remote spelled `git@internal:secrets/work-vault.git`, relying on a `Host
internal` block to resolve, simply won't connect.

## Setting a remote

```
gage git set-remote <name> <url>
```

Sets (or changes) `origin` on the vault's git repository *and* updates
`gage`'s own record of that remote in your global config, in one step —
using this instead of a raw `git remote set-url` matters because a plain
passthrough would leave `gage vault info` reporting a stale remote.

A vault can start local-only (`gage init` with no `--remote`) and gain a
remote later this way — e.g. after creating an empty repository on your git
host.

## The supported path: HTTPS with a token

```
gage auth login  [--host HOST]     # store a token for a git host
gage auth status [--host HOST]     # which hosts have a token, and whether it works
gage auth logout [--host HOST]     # forget it
```

This works identically against GitHub, GitLab, Gitea, Bitbucket, or a
self-hosted install — it's not a GitHub-specific feature, it's the
consequence of SSH config not being honored (above). A token is stored per
*host*, not per vault: two vaults on the same host share one token, and
`--host` defaults to the host of the current vault's `origin`.

- **Scope the token as narrowly as your host allows.** On GitHub, use a
  fine-grained personal access token limited to the vault's repository —
  not a classic `repo`-scoped token, which reaches every private repository
  you own. `gage auth login` reminds you of this at the prompt.
- **You bring the token; `gage` never brokers one.** There's no OAuth
  device flow. This is deliberate, not a missing feature — GitHub's OAuth
  App scopes are all-or-nothing per repository type, so a device-flow login
  would necessarily grant broader access than a properly scoped PAT.
- **An expired token fails legibly**, naming `gage auth login` as the fix,
  rather than surfacing a bare transport-layer 403.
- **Tokens live in local, disposable device state** (never inside your
  synced config — see [Configuration](configuration.md)), since a token is
  always re-acquirable by logging in again.

## SSH remotes

A simple SSH remote (no host-alias trickery) works if `ssh-agent` already
has a usable key loaded. What won't work is anything that depends on
`~/.ssh/config` to resolve — if it does, `gage` fails with a message saying
so and pointing you at the HTTPS equivalent, rather than silently trying
and failing in a confusing way.

## Things `gage` doesn't model

For git operations `gage` has no command for — branch/tag management,
`gc`, `fsck`, reflog inspection, and the rest of git's long tail — remember
that a vault is just an ordinary git repository sitting at a well-known
path (`gage vault info` reports it). `cd` there and use the real `git` CLI
directly, with its actual help text and tab completion. There is
deliberately no generic `gage git -- <args...>` passthrough — every command
`gage` does support goes through go-git directly.
