# M5 — Query resolution

[← M4](m4-crud.md) · [plan index](index.md) · next: [M6 — Session mode](m6-session-mode.md)

> **Recommended model: Sonnet.** Resolver plus the `$EDITOR` scratch-file helper. Mechanical, though the scratch-file cleanup-on-every-exit-path bullet deserves care.

## Goal

Upgrade addressing from "exact UUID/title" to the full resolution order
(exact title → substring title → exact UUID → substring UUID → ambiguous
prompt/fail), and move every query-taking command onto it — `cat` and
`rm` (both stuck on M4's exact-match-only behavior) alongside the
commands new to this milestone: `show`, `edit`, `rename`. **No command
should be left on the old exact-only matching once this milestone is
done.**

`gage generate` also lands here, and `-e|--edit` is added to `gage insert`
(deferred from M4), since it shares `gage edit`'s
`$EDITOR`-on-scratch-file round trip — one CLI-layer helper, two call
sites.

## Depends on

- **M3** — entry decryption (resolution matches against decrypted titles).
- **M4** — the CRUD verbs being re-wired, and commit-per-write.

## Design references

- ["Addressing entries & the metadata index"](../tdds/gage-cli-design.md)
  — the resolution order and the library-returns-a-candidate-list rule
- ["Entry CRUD"](../tdds/gage-cli-design.md) — `show` vs `cat` (value
  only vs. full YAML), the `insert -e` abort conditions
- ["A few decisions worth calling out"](../tdds/gage-cli-design.md) — the
  `$EDITOR` scratch file's tmpfs-where-available posture

## Decisions made

- **`generate` takes `<title>`, not a query.** Confirms A2: it creates an
  entry, so it addresses nothing existing. "Addressing entries" already
  listed only `show`/`cat`/`edit`/`rename`/`rm`/`mv`/`cp` as query-taking;
  the stray mention of `generate` there was the leftover A2 flags, not a
  second, correct statement.
- **Substring matching is case-insensitive.** Title substring matching
  runs before any UUID matching (see below); `description` and body stay
  `search`'s job (alias `grep`), not addressing's, at either stage.
- **Title is checked before UUID, and UUID matching is substring, not
  just prefix — reversed from this milestone's original
  implementation, which tried UUID (prefix, no minimum length) before
  title.** That order shipped a real bug, caught after the fact rather
  than in review: a query that was the *exact, literal title* of one
  entry could silently resolve to a *different* entry instead, whenever
  the query happened to also be a unique substring of that other
  entry's UUID — no error, no ambiguity warning, just the wrong secret.
  `"22"` as a title, next to some unrelated entry whose random UUID
  happens to start `22ed1bfd...`, is enough to trigger it; short
  hex-spellable words (`dead`, `beef`, `cafe`) are exactly as exposed as
  bare hex digits. The final order is **exact title → substring title →
  exact UUID → substring UUID → ambiguous**: by the time UUID matching
  ever runs, no entry's title has matched at all, so a UUID hit can never
  pre-empt a title hit. Accepting UUID queries as *substring* rather than
  prefix-only (the original scope) is intentional now that they run
  last — matching `ls`'s short-id convention no longer needs prefix
  specifically once title can never be shadowed by it.
  See ["Addressing entries & the metadata index"](../tdds/gage-cli-design.md)
  for the full reasoning, now recorded there rather than only here.
- **`rename` enforces the same duplicate-title check as `insert`,
  with the same `-f` override.** Even though M5's resolver now handles an
  ambiguous title gracefully instead of failing unrecoverably, silently
  letting `rename` produce a collision would still be a worse default
  than `insert`'s — consistency between the two creating/retitling
  operations means one rule to remember rather than two.

## Tests (write first)

**Resolution**

- [ ] An exact title match resolves uniquely even when it's also a
      substring of another entry's title
- [ ] A unique (non-exact) substring title match resolves correctly
- [ ] An exact UUID resolves to the correct entry
- [ ] A unique UUID substring (not just a prefix) resolves to the correct
      entry
- [ ] **An exact title match wins over a UUID match, even when the query
      is also a unique substring of a *different* entry's UUID** — the
      regression test for the bug this milestone's original
      implementation shipped: title stages run, and stop, before UUID
      matching is ever attempted
- [ ] An ambiguous substring match (title or UUID) lists all candidates
      and, in one-shot mode, fails with nonzero exit instead of prompting
- [ ] The resolver returns a candidate list as a *value*; no library code
      path prints it
- [ ] A query matching nothing is distinguishable from a query matching
      several — different typed results, different exit codes
- [ ] `gage cat` and `gage rm` resolve every stage (exact/substring
      title, exact/substring UUID) exactly like `show` — no longer
      limited to M4's UUID-or-exact-title-only matching
- [ ] An ambiguous query given to `cat` or `rm` lists candidates and fails
      in one-shot mode, exactly like `show` — neither command silently
      acts on the first match nor falls back to M4's stricter matching

**Commands**

- [ ] `gage show` prints only the `value` field — not title, description,
      fields, or timestamps
- [ ] `gage cat` prints the full decrypted YAML including metadata,
      distinguishing it from `show`
- [ ] `gage edit` re-stamps `updated`/`updated_by` on save, leaves other
      fields untouched if unedited, and produces a new commit
- [ ] `gage edit` that comes back with unparseable YAML aborts with no
      commit and a clear error, and the original entry is unchanged
- [ ] `gage rename` changes only the title (not `value`/`fields`) and
      bumps `updated`
- [ ] `gage rename` to an existing title behaves per the decision above
- [ ] `gage generate` inserts a new entry with a randomly generated
      `value` of a sane default length — `-l`/`--no-symbols` customization
      is deferred to M12, tested there
- [ ] `gage generate` draws from a cryptographically secure source
      (`crypto/rand`), asserted structurally rather than statistically

**`insert -e` and the shared editor helper**

- [ ] `gage insert -e` opens a template (`title`/`description`
      pre-filled, empty `value`/`fields`) in `$EDITOR`; saving it inserts
      an entry with the edited `value`/`fields`, including `fields` set
      directly at creation — the one `insert` mode that can do that
- [ ] `gage insert -e` aborts with no entry written and no commit if the
      file comes back unchanged, or with `value` still empty and `fields`
      still empty
- [ ] `gage insert -e` changing `title` inside the editor uses the
      edited title, not the original `<title>` argument, for both the
      saved entry and the duplicate-title/`-f` check
- [ ] `-e` is rejected alongside `-m` or `--value-stdin`, before any I/O
      (extends M4's mutual-exclusion test to three flags)
- [ ] `editYAML`'s scratch file lands in a verified tmpfs directory on
      Linux and the OS's standard temp directory elsewhere, created
      `0600`
- [ ] The scratch file is removed (contents overwritten first) on every
      exit path — `$EDITOR` exits zero, `$EDITOR` exits non-zero, and the
      edited content fails to parse back into an `Entry`
- [ ] An unset or unusable `$EDITOR` fails with a clear error before a
      scratch file containing plaintext is written

## Implementation

- [ ] Query resolver (exact title/substring title/exact UUID/substring
      UUID/ambiguous, in that order — see "Decisions made"), returning a
      resolved entry or a candidate list as a value — never printed text;
      the CLI layer decides whether to prompt (session) or fail (one-shot)
- [ ] `gage cat`/`gage rm` re-wired onto the shared resolver, replacing
      M4's exact-UUID/title-only matching
- [ ] `gage show` (value only, `--field` deferred to M12)
- [ ] Cross-platform scratch-file location for `editYAML`: on Linux,
      verify (via `statfs`) and prefer a tmpfs-backed directory
      (`$XDG_RUNTIME_DIR`, falling back to `/dev/shm`); on macOS/Windows,
      fall back to the OS's standard secure temp directory (`0600`,
      exclusively created)
- [ ] Shared `editYAML` CLI helper: write the scratch file, launch
      `$EDITOR`, read back, detect unchanged, then best-effort overwrite
      before deleting on every exit path (success, non-zero `$EDITOR`
      exit, parse failure) — used by both `gage edit` and
      `gage insert -e`
- [ ] `gage edit` (`editYAML` seeded with the decrypted entry, re-stamps
      `updated`/`updated_by`)
- [ ] `gage insert -e` (`editYAML` seeded with a stub entry instead of a
      decrypted one)
- [ ] `gage rename`
- [ ] `gage generate` (default-length/character-set value only, from
      `crypto/rand`; `-l`/`--no-symbols` land in M12)

## Definition of done

Full test list green on all three CI platforms. Every query-taking
command resolves identically, and no command is still on M4's exact-match
path.

## Affects later milestones

- The resolver's candidate-list-as-value contract is what M6 prompts
  with. If it ever returns rendered text, session mode can't do its job.
- `editYAML` is CLI-only by design and explicitly has no GUI equivalent —
  see the design doc's "What's deliberately CLI-only."
- M10's trust cache hooks the encrypting methods, which by the end of
  this milestone are `insert`/`edit`/`generate`/`rename`.
