# Implementation plan

Sequential milestones for building out
[gage-cli-design.md](../designs/gage-cli-design.md). Each milestone should
leave the tool in a runnable, testable state — no milestone should require
reworking a prior one, though later milestones may extend earlier
abstractions.

Status legend: `[ ]` not started, `[~]` in progress, `[x]` done.

---

## M0 — Scaffolding

CLI framework (subcommand dispatch, one-shot mode only for now), XDG path
resolution (`$GAGE_CONFIG`/`$GAGE_DATA`/`$GAGE_STATE`), global
`config.toml` read/write (`repos.*`, `current`). No crypto yet — just prove
the plumbing.

- [ ] CLI arg parsing / subcommand dispatch skeleton
- [ ] XDG path resolution (config/data/state, with Windows mapping)
- [ ] Global `$GAGE_CONFIG/config.toml` read/write

## M1 — Repo lifecycle, single method

`gage init` with **one** method only (passphrase — simplest, no external
plugin dependency). Writes `.gage/config.toml`, `.age-recipients`,
git-inits, registers in global config. Defer `clone` until there's
something worth cloning.

- [ ] `gage init` (passphrase method only)
- [ ] `.gage/config.toml` read/write
- [ ] `.age-recipients` read/write
- [ ] `gage repo list/info/remove/set-default`

## M2 — Entry format + crypto round-trip

The core loop, at the library level, no CLI verb wired up yet: generate
UUID, YAML-encode an entry, encrypt to `vault/<uuid>.age` via the `age`
library against `.age-recipients`, decrypt it back. This is the riskiest
technical piece (crypto correctness) — prove it in isolation before
anything else depends on it.

- [ ] Entry struct + YAML (de)serialization
- [ ] Encrypt entry to `vault/<uuid>.age`
- [ ] Decrypt entry from `vault/<uuid>.age`
- [ ] Round-trip test coverage

## M3 — Dumbest possible CRUD (one-shot mode)

`insert`, `cat` (by UUID or exact title match only — no fuzzy/ambiguous
resolution yet), `rm`, `ls`. Every write is a git commit (no push yet).
First end-to-end usable slice: store and retrieve a secret from the
command line, decrypting everything every time (no cache).

- [ ] `gage insert`
- [ ] `gage cat` (exact UUID/title only)
- [ ] `gage rm`
- [ ] `gage ls`
- [ ] Commit-per-write

## M4 — Query resolution

Upgrade addressing from "exact UUID/title" to the full resolution order
(prefix → exact → unique substring → ambiguous prompt/fail). Wire `show`,
`edit`, `rename`, `generate` on top of it.

- [ ] Query resolver (prefix/exact/substring/ambiguous)
- [ ] `gage show`
- [ ] `gage edit` (`$EDITOR` + tmpfs, re-stamps `updated`/`updated_by`)
- [ ] `gage rename`
- [ ] `gage generate`

## M5 — Session mode

The REPL: `use`, `lock`, `status`, `exit`. In-memory key holding with
`mlock`, all M3/M4 commands ported to work against "current session
repo." No metadata index yet — still decrypt-on-demand per command.

- [ ] REPL loop + session commands (`use`/`lock`/`status`/`exit`/`help`)
- [ ] In-memory key holding (`mlock`, no core dumps)
- [ ] Entry commands ported to session mode
- [ ] Idle timeout re-lock

## M6 — Metadata index

In-session decrypt-once cache for `ls`/`search`/`show` resolution,
incremental updates on insert/edit/rm. Pure performance/UX layer on top of
M5 — correctness doesn't change.

- [ ] Build index on first `ls`/`show`/`search` per repo
- [ ] Incremental update on insert/edit/rm
- [ ] `gage reindex`
- [ ] `gage search` / `gage grep`

## M7 — Sync

Needs M5's session lifecycle to hook `use` into, and M3's
commit-per-write already true.

- [ ] Auto fetch + fast-forward pull on `use`
- [ ] Auto push after writes
- [ ] Divergence detection
- [ ] `gage sync` (conflict surfacing, both versions shown)
- [ ] Manual `pull`/`push`/`git` passthrough

## M8 — Identity/recipient management + trust cache

First place multi-device/multi-recipient scenarios become testable —
naturally after single-user CRUD+sync are solid.

- [ ] `gage identity add/list`
- [ ] `gage recipient add/remove --reencrypt`
- [ ] `gage recipient verify`
- [ ] Local trust cache (`known-config.toml`, diff + warning on `use`/encrypt)

## M9 — Cross-repo sharing

Just M2's encrypt primitive pointed at a second repo's recipients —
trivial once M1–M2 exist for two repos, but distinct enough to be its own
milestone since it's the sharing story.

- [ ] `gage mv --to-repo`
- [ ] `gage cp --to-repo`
- [ ] `gage clone`

## M10 — Polish / output modes

Independent of each other and of the trust model — good candidates to
parallelize or reorder freely, safe to defer individually.

- [ ] `--clip` (clipboard, auto-clear)
- [ ] `--qr` (terminal QR, `--field` scoped)
- [ ] `--field NAME` extraction
- [ ] `generate` password-generation logic
- [ ] `gage log`
- [ ] `gage history --decrypt`
- [ ] Non-interactive session (`--script`, `--stdin`)

---

## Deferred / not on the critical path

- **Additional identity methods** (ssh, yubikey, secure-enclave,
  plugin) — passphrase alone proves the `identity`/`recipient`
  abstraction; add real methods once that interface is stable, otherwise
  you're debugging plugin behavior and architecture at the same time.
- **Signed recipient changes** — design doc calls this out as "reserve
  for high-stakes repos," not a default.
- **`gage clone` against a real remote** — folded into M9, but only
  really testable once there's an actual remote to clone from.
