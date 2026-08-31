# Open questions & deferred issues

The single register for anything unresolved. A problem is allowed to be
deferred; it is not allowed to be deferred *silently*. If it isn't fixed
and isn't here, it's lost.

Three sections:

- **Open decisions** — needs a human answer before the named milestone.
- **Design-doc amendments pending review** — changes the TDD needs, not
  yet applied.
- **Accepted risks** — knowingly not fixing; recorded so nobody
  "discovers" them later and assumes they were missed.

Status legend: `[ ]` open, `[~]` under discussion, `[x]` resolved (keep
the entry, record the answer).

---

## Open decisions

### `[~]` Q-GIT-AUTH — How does `gage` authenticate to remotes? {#q-git-auth}

**Blocks:** M8. **Could invalidate:** the go-git choice in M0.

go-git does not read `~/.ssh/config` (host aliases, `IdentityFile`,
`ProxyCommand`, `Port`), does not use git credential helpers, and does
not honor `insteadOf` rewrites. The design doc's own example remote —
`git@internal:secrets/work-vault.git` — is a host alias that only
resolves through `~/.ssh/config`. For HTTPS remotes there is no
credential storage story at all.

Options on the table:

1. **ssh-agent + explicit configured key path + prompted token for
   HTTPS**, documenting plainly that `~/.ssh/config` is not honored.
   Keeps the no-git-binary rule intact; costs some users a config change.
2. **Parse a subset of `~/.ssh/config`** (`Host`/`HostName`/`User`/
   `IdentityFile`/`Port`) and feed it to go-git. Ongoing maintenance,
   never quite complete.
3. **Relax the no-git-binary rule for network operations only** — shell
   out to real `git` for fetch/push/clone, keep go-git for local work.
   All existing user auth config just works; contradicts a stated design
   principle and adds a runtime binary dependency.
4. **`go-github` + a GitHub-only backing store** — *proposed 2026-08-30,
   discussion deferred by request.*

**Note for the option-4 discussion when it happens** (recorded so the
framing is right, not to pre-judge it): `go-github` is a client for
GitHub's REST API, not a git transport. Backing a vault with it would
most likely mean storing entries via the Contents API rather than
`clone`/`fetch`/`push`. That interacts with two things already committed
to in the design doc — principle 3 ("sync happens via the backing
store") is fine with it, but the sync model's ["commit is local,
instant, and always happens"](../tdds/gage-cli-design.md) durability
guarantee assumes a local object store that an API-backed vault wouldn't
have. It's also a *vault type* question as much as an auth question, so
it may belong under the design doc's "Vault types" section rather than
replacing the `git` type outright. Worth deciding whether it's
`type = "github"` alongside `type = "git"`, or a replacement.

Nothing before M8 depends on the answer, so this can stay open through
M7 without blocking work.

---

### `[ ]` Q-ROOT-CMD — What does bare `gage` (no subcommand) do?

**Blocks:** M0 (skeleton), M6 (REPL).

The design doc says running `gage` with no subcommand drops into the
interactive session prompt. Cobra's default for a root command with no
args is to print help. Needs an explicit decision and an M0 test either
way, because M6 inherits whatever M0 wires up. Recommendation: root
command with no args and a TTY on stdin enters session mode; no TTY
prints help.

---

### `[ ]` Q-METHOD-FLAG — Is `--method` required, and how is it validated?

**Blocks:** M1.

`--type` got careful treatment: accepted now, validated against a
single-element allowlist, so a second type is purely additive later.
`--method` has exactly the same future (additional methods are explicitly
deferred) and no equivalent treatment anywhere in the design doc or plan.
The command reference shows it with no default, implying required.
Decide: required-and-allowlisted (mirrors `--type`'s explicitness), or
defaulted to `passphrase`.

---

### `[ ]` Q-DEVICE-NAME — Where is this device's identity name recorded?

**Blocks:** M2 (`Unlock` must find the file), M4 (`updated_by`).

`Vault.Unlock` has to locate `$GAGE_DATA/identities/<vault>/<device>.age`
and every write stamps `updated_by` with the device name. Neither
document says where "which device am I, for this vault" is stored.
Globbing the directory only works under an unstated one-file-per-vault
assumption.

Recommendation, and what the plan is currently written against: a
`device` field under `[vaults.<name>]` in global config, written by
`init`/`identity add`/`clone`. Needs confirmation because it changes the
global config schema tested in M0/M1.

Secondary question it raises: what's the default device name when the
user doesn't pass one? Hostname is the obvious candidate; it's also
PII-ish and lands in a committed, plaintext `config.toml` readable by
everyone with vault read access.

---

### `[ ]` Q-SYNC-CONFLICT — What does conflict resolution actually do?

**Blocks:** M8.

"`gage sync` surfaces both versions and requires an explicit choice" is
the whole specification today, and this is the second-riskiest piece in
the build after crypto. Unanswered:

- What are the choices — keep-local, keep-remote, keep-both-as-two-entries
  (new UUID for one)? Field-level merge is presumably out of scope.
- What does the resulting git history look like — a merge commit, or
  reset-and-recommit?
- Resolution needs to decrypt both sides, so it needs an unlocked
  identity — but the trust cache (M10) also wants to warn on a clean
  fast-forward sync that never unlocks anything. Which paths unlock?
- Same-UUID-modified-on-both-sides is the obvious conflict. Is
  same-title-different-UUID (two devices independently inserting
  "AWS root") a conflict, or two entries?

This likely wants its own subsection in the design doc, and may justify
splitting M8 into happy-path sync and conflict resolution.

---

### `[ ]` Q-DEVICE-DEFAULT-NAME — see Q-DEVICE-NAME above

Folded into Q-DEVICE-NAME; kept as an anchor so it isn't answered by
accident.

---

### `[ ]` Q-RELEASE — Release engineering and distribution {#q-release}

**Blocks:** nothing; needed before a first public release.

Reproducible builds, checksums, artifact signing, package manager
manifests, install documentation. Absent from both the milestones and the
original deferred list. For a secrets manager, "how do I verify the
binary I downloaded is the one you built" deserves an explicit answer
rather than silent omission. Decide whether this becomes M13 or stays
out of scope for a first cut.

---

## Design-doc amendments pending review

Changes [gage-cli-design.md](../tdds/gage-cli-design.md) needs. **None of
these have been applied** — listed here for review first.

### `[ ]` A1 — Add a concurrency section

The design's "Trade-off vs. a shared agent" actively makes multi-process
the normal case (a session in one pane, one-shot commands elsewhere).
Every write is a commit against one working tree, and M9 adds a "reset
`entries/` to HEAD if dirty" precondition on every write — which, run
concurrently, will discard another process's in-flight state. The design
doc needs a short section stating the advisory-lock model, what's held
across what, and the contended-lock behavior. Decision already taken
(primitive in M0, first used at M4); the doc just doesn't reflect it.

### `[ ]` A2 — Resolve the `generate` title-vs-query contradiction

["Addressing entries"](../tdds/gage-cli-design.md) lists `generate`
among the query-taking commands; the command reference says
`gage generate <title>`. `generate` creates a new entry, so it takes a
title — the query framing is most likely a leftover. Confirm and remove
`generate` from the query-taking list.

### `[ ]` A3 — State the scrypt sole-recipient invariant

age refuses to encrypt to a scrypt passphrase recipient combined with
any other recipient. That's fine for the identity file as designed
(scrypt-only), but the invariant should be written into "Local identity
storage" so nobody later tries to add a second recipient to
`<device>.age`.

### `[ ]` A4 — Say where the device name is recorded

Follows whatever Q-DEVICE-NAME resolves to.

### `[ ]` A5 — Specify `format_version` enforcement

The field appears in the example `.gage/config.toml` and nowhere else. A
forward-compat marker that isn't enforced from v1 is worse than none —
an old binary will half-parse a v2 config. State that an unrecognized
`format_version` is a clean refusal, not a best-effort parse.

### `[ ]` A6 — Document git remote auth limitations

Follows whatever Q-GIT-AUTH resolves to.

### `[ ]` A7 — Note that the metadata index is invalidated by sync

The index is described as session-scoped and incrementally updated on
insert/edit/rm. It doesn't mention that an auto fetch + fast-forward pull
on unlock changes `entries/` underneath a live session. One sentence in
"Addressing entries & the metadata index."

### `[ ]` A8 — Fix the stale cross-reference

Not a design-doc issue — was in the plan, now fixed: the plan pointed at
`../designs/gage-cli-design.md`, which moved to `../tdds/` in `c0ee608`.
Recorded here only so the move is on the record.

---

## Accepted risks

### Entries carry no format version of their own

`format_version` lives on the vault config, not on each entry's YAML.
Adding an entry-level schema version later would be a breaking change to
existing ciphertext. Accepted on the grounds that the vault-level version
can gate an entry-schema migration — a vault at `format_version = 2` can
be defined to contain v2 entries. Revisit if entry schema churn turns out
to be more likely than expected.
