# Getting started

## Building/installing

```
go install github.com/distillerylabs/gage/cmd/gage@latest
```

Or build from a checkout:

```
make build       # builds ./cmd/gage -> ./gage
```

See the top-level [README](../../README.md) for build requirements. This
guide assumes a `gage` binary on your `PATH`.

## Create your first vault

```
$ gage init personal
```

This creates a new vault named `personal`, generates a decryption identity
for the current device (defaulting to the passphrase method — you'll be
prompted to set one), and registers the vault in your global config as the
current default. By default the vault's files live under `$GAGE_DATA/vaults/personal`
(see [Configuration](configuration.md) for exactly where that is on your OS) —
pass `--dir PATH` to put it somewhere else.

`init` also generates an **offline recovery key** and shows it once. Write it
down and keep it somewhere safe and offline. If this device's identity file or
passphrase is ever lost, `gage recovery enroll` uses it to get you back in,
and `gage` keeps no copy. See
[Storing your recovery key](identities-and-recipients.md#storing-your-recovery-key)
for what to do (and not do) with it.

The vault starts with no remote. If you want it to sync to a git host later,
see [Git remotes and authentication](git-and-auth.md) — or create it with a
remote up front:

```
$ gage init personal --remote https://github.com/you/personal-vault.git
```

## Store a secret

```
$ gage insert github-token
Enter value: ****************
```

`insert` prompts for the entry's value, masked like a passphrase. You can
also pipe a value in or open an editor instead — see [Entries](entries.md).

## Read it back

```
$ gage show github-token
ghp_xxxxxxxxxxxxxxxxxxxx
```

`show` with no flags prints just the entry's `value` — nothing else — so it
composes cleanly with pipes and paste. Add `-c` to copy it to the clipboard
instead (auto-clearing after a short timeout) or `-q` to render it as a
scannable terminal QR code.

## List what's in the vault

```
$ gage ls
TITLE          ID        CREATED      UPDATED      UPDATED BY
github-token   4b9d7710  2026-09-01   2026-09-01   laptop-1
```

## A faster way to work: session mode

Every command above unlocked the vault, did one thing, and dropped the key
from memory — fine for one-off lookups, but wasteful if you're doing several
things in a row. Run `gage` with no arguments to start an interactive session
instead:

```
$ gage
gage> use personal
Enter passphrase: ****
[personal🔓] gage> insert wifi-password
Enter value: ****
[personal🔓] gage> show wifi-password
correcthorsebatterystaple
[personal🔓] gage> exit
$
```

Inside a session you unlock a vault once and run as many commands against it
as you like, with no unlock cost. See [Session mode and scripting](session-mode.md)
for the full picture, including non-interactive automation.

## Add a second device

On the new machine, clone the vault and let `gage` set up an enrollment
request:

```
$ gage clone https://github.com/you/personal-vault.git
gage: cloned "personal" to ~/.local/share/gage/vaults/personal
This device holds no identity for "personal", so it can't read anything here yet.
Set up an enrollment request now? [Y/n] y
...
  Enrollment code:  GAGE-7K4M-9QX2-P3RH-8WVN
```

Send that code to the machine that already has access — over chat, in
person, however you'd normally reach yourself — and approve it there:

```
$ gage sync
$ gage recipient approve --code GAGE-7K4M-9QX2-P3RH-8WVN
```

Back on the new device, `gage sync` and you can read the vault. The full
story, including denying requests and what the code does and doesn't
prove, is in [Adding a device](enrollment.md).

## Next steps

- Give another device (or another person) access: [Adding a device](enrollment.md) and [Identities and recipients](identities-and-recipients.md).
- Set up a remote so your vault syncs between machines: [Git remotes and authentication](git-and-auth.md).
- Learn the full entry format (descriptions, structured fields, generated passwords): [Entries](entries.md).
