# Entries

An entry is a single decrypted record — a password, a structured note (API
keys, recovery codes), or a paragraph of unstructured text are all the same
underlying thing. On disk each entry is one `.age`-encrypted file under
`entries/`, named with a random UUID that carries no meaning. Everything a
human recognizes — title, description, value, structured fields — lives
*inside* the encrypted payload.

## The entry format

Once decrypted, an entry is YAML:

```yaml
title: ProtonMail
description: Personal email, 2FA via authenticator app
created: 2026-01-14T10:32:00Z
updated: 2026-08-20T09:03:00Z
updated_by: yubikey-5c-nfc-1
value: correcthorsebatterystaple
fields:
  username: me@proton.me
  totp_seed: JBSWY3DPEHPK3PXP
```

- **`title`** — required. What `ls`/`search` match and display against.
- **`description`** — optional free text, also searchable.
- **`created`** — set once, at `insert` time.
- **`updated`** / **`updated_by`** — rewritten on every edit. `updated_by` is
  the identity name of whichever device made the change (the same name
  registered via `gage identity add`).
- **`value`** — the entry's primary payload: a password, or the body of an
  unstructured note.
- **`fields`** — structured key/value metadata (e.g. `username`,
  `totp_seed`). Use `--field NAME` on `show`/`generate` to read one directly.

## Creating an entry

```
gage insert <title> [--use NAME] [--description TEXT]
             [-m|--multiline | --value-stdin | -e|--edit] [-f|--force] [--yes]
```

Exactly one of `-m`, `--value-stdin`, `-e` may be given. With none of them,
`gage` prompts for the value once, masked:

```
$ gage insert github-token
Enter value: ****************
```

- **`-m`/`--multiline`** reads multiple lines straight from the terminal
  until EOF (Ctrl-D) — useful for a multi-line note or a PEM key.
- **`--value-stdin`** reads the value verbatim from stdin (one trailing
  newline trimmed), for scripting: `pass show old-entry | gage insert new --value-stdin`.
  It's deliberately not called `--stdin`, which is the session-level flag
  for piping whole commands (see [Session mode and scripting](session-mode.md)).
- **`-e`/`--edit`** opens the full entry as YAML in `$EDITOR`, pre-filled
  with `title`/`description` and empty `value`/`fields` — the only `insert`
  mode that can set structured `fields` at creation time. Saving with the
  file unchanged, or with both `value` and `fields` still empty, aborts the
  insert entirely (no entry written) — the same "empty message aborts the
  commit" convention as `git commit`.
- **`-f`/`--force`** allows creating an entry whose title duplicates an
  existing one (by default, `gage` warns instead — two entries can share a
  title, but it's usually a mistake).
- **`--yes`** skips the recipient-change confirmation prompt, for scripts.

## Generating a random value

```
gage generate <title> [--use NAME] [-l LENGTH] [--no-symbols]
                       [-f|--force] [-c|--clip] [-q|--qr] [--yes]
```

Creates a new entry with a randomly generated value instead of one you
type. `-l` sets the length (default 24, minimum 8); `--no-symbols` restricts
the alphabet to letters and digits. `-c`/`-q` behave like the same flags on
`show` (below) — useful for getting a freshly generated password onto
another device without ever printing it to a terminal you don't trust.

## Reading an entry

```
gage show <query> [--use NAME] [-c|--clip] [-q|--qr] [--field NAME]
gage cat  <query> [--use NAME]
```

- **`show`** with no flags prints just `value` — nothing else — so it
  composes cleanly with pipes and paste.
- **`--field NAME`** prints one entry from `fields` instead of `value` (e.g.
  `--field username`, `--field totp_seed`).
- **`-c`** copies the result to the clipboard instead of printing it, and
  auto-clears it after a short timeout (`[shell].clipboard_timeout`, 45s by
  default — see [Configuration](configuration.md)). The clear is skipped if
  you've since overwritten the clipboard with something else.
- **`-q`** renders a terminal QR code instead of printing plaintext — scan it
  with a phone camera instead of typing or pasting a secret. Combined with
  `--field NAME`, it QR-encodes only that field, so a structured note isn't
  dumped whole into a code when you only wanted one piece of it.
- **`cat`** always prints the entry's full decrypted YAML — metadata
  included — for scripting or when you actually want everything, not just
  the value.

## Editing an entry

```
gage edit <query> [--use NAME]
```

Decrypts the entry to a scratch file, opens it in `$EDITOR`, re-encrypts and
commits on save, and re-stamps `updated`/`updated_by`. The template lets you
edit `title` itself, not just `description`/`value`/`fields`.

```
gage rename <query> <new-title> [--use NAME]
```

A quicker, `$EDITOR`-free way to change just the title.

## Listing and searching

```
gage ls [--use NAME]
gage search <pattern> [--use NAME]      # alias: gage grep
```

`ls` lists every entry's title, short ID, created/updated dates, and last
writer. `search` matches against title, description, and body text. Both
require the vault to be unlocked, since titles live inside encrypted
payloads — see [Addressing entries](addressing-entries.md) for how `gage`
keeps this fast in session mode.

## Deleting an entry

```
gage rm <query> [--use NAME]
```

## Viewing history

```
gage log [QUERY] [--use NAME]
gage history --decrypt <query> [--use NAME]
```

`log` shows an entry's commit history — timestamps only, since filenames
alone reveal nothing about content. `history --decrypt` walks that history
and decrypts each past revision to show a diff — a separate, explicit
subcommand (not a flag on `log`) because it surfaces old secret values,
which is meaningfully more dangerous than just seeing when something
changed.
