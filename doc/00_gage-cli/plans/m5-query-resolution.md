# M5 — Query resolution

[← M4](m4-crud.md) · [plan index](index.md) · next: [M6 — Session mode](m6-session-mode.md)

> **Recommended model: Sonnet.** Resolver plus the `$EDITOR` scratch-file helper. Mechanical, though the scratch-file cleanup-on-every-exit-path bullet deserves care.

## Goal

Upgrade addressing from "exact UUID/title" to the full resolution order
(prefix → exact → unique substring → ambiguous prompt/fail), and move
every query-taking command onto it — `cat` and `rm` (both stuck on M4's
exact-match-only behavior) alongside the commands new to this milestone:
`show`, `edit`, `rename`. **No command should be left on the old
exact-only matching once this milestone is done.**

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

## Decisions to make first

- **[A2 / `generate`'s argument](open-questions.md)** — the design doc
  contradicts itself: "Addressing entries" lists `generate` among the
  query-taking commands, the command reference says
  `gage generate <title>`. `generate` creates an entry, so it takes a
  title. Confirm before writing the tests, and amend the design doc.
- **Substring matching semantics** — case sensitivity (recommend
  case-insensitive), and whether `description` is matched or only
  `title`. The design says titles; `search` is the command that spans
  description and body.
- **UUID prefix minimum length.** A one-character prefix will collide
  constantly. Decide a floor (or accept any length and let ambiguity
  handle it).
- **Does `rename` enforce the duplicate-title check?** `insert` does,
  with `-f` to override. `rename` can produce a duplicate just as easily
  and the design doesn't say.

## Tests (write first)

**Resolution**

- [ ] A UUID prefix resolves to the correct entry
- [ ] An exact title match resolves uniquely even when it's also a
      substring of another entry's title
- [ ] A unique (non-exact) substring match resolves correctly
- [ ] An ambiguous substring match lists all candidates and, in one-shot
      mode, fails with nonzero exit instead of prompting
- [ ] The resolver returns a candidate list as a *value*; no library code
      path prints it
- [ ] A query matching nothing is distinguishable from a query matching
      several — different typed results, different exit codes
- [ ] `gage cat` and `gage rm` resolve prefix/exact/substring matches
      exactly like `show` — no longer limited to M4's UUID-or-exact-title-
      only matching
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

- [ ] Query resolver (prefix/exact/substring/ambiguous), returning a
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
