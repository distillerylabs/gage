# Addressing entries

Entry filenames are random UUIDs with no meaning — there's no "path" to
type the way there is with `pass`. Instead, `show`, `cat`, `edit`,
`rename`, `rm`, `mv`, and `cp` all take a **query**, matched against
decrypted titles. (`insert` and `generate` take a plain `<title>` instead —
they create an entry rather than address an existing one.)

## Resolution order

A query is resolved in this fixed order, stopping at the first stage that
matches anything:

1. Exact title match
2. Substring match on title
3. Exact UUID match
4. Substring match on a UUID's canonical string form
5. No match at any stage → not found

If a stage matches **more than one** entry, resolution stops there and
lists that stage's own candidates — it never falls through to a later
stage:

```
gage> show aws
Multiple entries match "aws":
  1. AWS root account          (4b9d7710) — updated 2026-08-20
  2. AWS IAM backup admin       (a03e5f88) — updated 2026-01-14
Which one? [1-2]:
```

Title is checked before UUID deliberately: entries are addressed by title
far more often than by a UUID nobody has memorized, and a short,
hex-spellable title (`dead`, `beef`, a single letter) can coincidentally
appear inside some other entry's randomly generated UUID. Checking title
first means an exact title match always wins, and a UUID substring can
never silently pre-empt it.

## One-shot vs. session mode

- **Session mode** prompts you with the candidate list shown above and lets
  you pick.
- **One-shot mode** has nobody to prompt, so it fails with the same
  candidate list printed to stderr and a non-zero exit code
  (`exitcode.Ambiguous` — see [Exit codes](exit-codes.md)) instead of
  hanging waiting for input.

## Why `ls`/`search`/`show` need an unlock

Because titles live inside encrypted payloads, even *listing* entries
requires decrypting them. In session mode, `gage` builds an in-memory
metadata index the first time `ls`/`show`/`search` needs one per vault —
decrypting every entry once and caching just the metadata (title,
description, dates, `updated_by`) for the rest of the session, updating it
incrementally as you insert/edit/remove entries. The index is never written
to disk; it disappears when the vault is locked or the process exits.

`gage reindex` forces a rebuild — useful if you suspect staleness, or after
a `git pull` run outside `gage` directly against the vault's repository.

One-shot mode gets no such cache: every invocation of `ls`/`search`/`show`
pays the full decrypt-every-entry cost from scratch. That's fine for a
small vault, but for a large one, prefer session mode for anything beyond a
single known lookup.
