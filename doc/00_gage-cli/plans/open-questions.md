# Open questions & deferred issues

The single register for anything unresolved. A problem is allowed to be
deferred; it is not allowed to be deferred *silently*. If it isn't fixed
and isn't here, it's lost.

Three sections:

- **Open decisions** — needs a human answer before the named milestone.
- **Design-doc amendments** — changes the TDD needs; `[x]` once applied,
  kept as a record of what changed and why.
- **Accepted risks** — knowingly not fixing; recorded so nobody
  "discovers" them later and assumes they were missed.

Status legend: `[ ]` open, `[~]` under discussion, `[x]` resolved (keep
the entry, record the answer).

---

## Open decisions

### `[x]` Q-GIT-AUTH — How does `gage` authenticate to remotes? {#q-git-auth}

**Resolved 2026-08-30 — HTTPS with a token, any host.** go-git stays;
nothing in M0 is invalidated. Applied as A6.

**The reframe that decided it: this is an SSH problem, not a GitHub
problem.** Every limitation driving the question — `~/.ssh/config` host
aliases, `IdentityFile`, `ProxyCommand`, credential helpers,
`insteadOf` — belongs to SSH transport. Token-over-HTTPS has none of
them and works identically against GitHub, GitLab, Gitea, Bitbucket, and
self-hosted. Restricting to GitHub would have fixed the same problem by
giving up much more than the problem required, including the self-hosted
remotes the design doc explicitly names.

**What this means concretely:**

- New `gage auth login/status/logout [--host HOST]`, namespaced under
  `git` because a token for a git host has no meaning under a different
  backing store. Per-*host*, not per-vault — two vaults on one host
  share a token.
- Tokens at `$GAGE_STATE/tokens/<host>`, `0600`. That root is documented
  as disposable local state, which is exactly right for something
  re-acquirable by logging in again. Not `$GAGE_CONFIG` — dotfile
  managers sync it, same reasoning that keeps identity files out.
- `gage auth login` steers toward narrowly-scoped tokens (on GitHub, a
  fine-grained PAT limited to the one repository rather than a classic
  `repo`-scoped token reaching everything you own).
- **go-github is added, but only for conveniences** on recognized hosts:
  OAuth device-flow login instead of a pasted token, and creating the
  private repo during `gage init --remote`. The transport is go-git
  either way; elsewhere the same commands ask for a token. Mechanism
  identical, acquisition friendlier.
- **SSH remotes stay best-effort**: used when ssh-agent has a usable key,
  but `~/.ssh/config` is never interpreted, so an alias-dependent remote
  fails with a message saying so and pointing at the HTTPS spelling.
  Same posture as the Windows core-dump gap — a narrow guarantee stated
  honestly. If this proves more trouble than it's worth in M8, dropping
  SSH entirely is a clean narrowing.
- The design doc's example remotes were SSH-spelled (`git@github.com:...`,
  `git@internal:...`) — the second one specifically depended on a
  `~/.ssh/config` alias. Both rewritten as HTTPS.

**Reading B — GitHub as the storage layer** (Contents API instead of git
transport) was considered and rejected. It breaks "commit is local,
instant, and always happens," breaks offline reads, makes
`--reencrypt`'s single-commit atomicity impossible without dropping to
the Git Data API, makes rate limits a correctness concern rather than a
speed one, and forfeits the "it's just a git repo, `cd` there and use
real git" escape hatch. Recorded so it isn't re-proposed without those
costs attached.

Leaves Q-OAUTH-APP below genuinely open.

---

### `[x]` Q-OAUTH-APP — Does the project register a GitHub OAuth App?

**Resolved 2026-08-30 — no, and not later either.** Users generate their
own fine-grained PAT and `gage auth login` stores it. No OAuth App, no
device flow, no shipped client ID.

**The reason is scope, not effort.** This design already steers users
toward "a fine-grained PAT limited to the vault's repository, not a
classic `repo`-scoped one that reaches every repository you own."
**An OAuth App cannot honor that.** OAuth App authorizations are
scope-based — `repo` means full control of *all* private repositories,
with no per-repository selection. Device flow would therefore hand
`gage` broader access than the path it replaces, and asking a secrets
manager's users to grant access to every private repo they own is a bad
thing to normalize.

A **GitHub App** could match PAT scoping — user-to-server device flow
needs only a public client ID, and the token is limited to repos where
the app is installed — but it adds an app-installation step for the user
*and* the same permanent registration commitment, to reach the scoping a
fine-grained PAT already provides with neither. So it doesn't rescue the
idea.

Two supporting reasons:

- **It only helps one host.** Every other host still pastes a token, so
  device flow reintroduces exactly the GitHub-specialness that resolving
  Q-GIT-AUTH removed on the grounds that the problem was never
  GitHub-shaped.
- **A shipped client ID is a permanent liability for a convenience.** It
  can lapse, be revoked, or be rate-limited, breaking login for every
  installed copy — including old versions no longer under anyone's
  control.

**The one real cost, and it's an error-message problem:** PATs expire.
An expired token must fail legibly ("your token for github.com expired,
run `gage auth login`") rather than as an opaque 403 from the transport.
Covered by an M8 test.

**Consequence: go-github is dropped entirely.** With device flow gone,
its only remaining job was creating the private repo during `gage init
--remote`, and the design doc already assumes manual repo creation ("a
vault can start local-only and gain a remote later, e.g. after creating
an empty repo on GitHub"). Dropping it removes the last host-specific
code path from an otherwise host-neutral design.

*Recorded so this isn't revisited as a "nice UX improvement": the
objection is that device flow is a **downgrade in scope**, not that it's
extra work.*

---

### `[x]` Q-GIT-AUTH-ORIGINAL — the original framing, for the record

**Blocked:** M8. **Could have invalidated:** the go-git choice in M0.

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

Nothing before M8 depended on the answer, so it stayed open through M7
without blocking work.

---

### `[x]` Q-ROOT-CMD — What does bare `gage` (no subcommand) do?

**Resolved 2026-08-30.** Bare `gage` drops into the interactive session
prompt, per the design doc — overriding Cobra's default of printing help
for an argument-less root command.

With one carve-out: **only when stdin is a TTY.** With stdin piped or
redirected, bare `gage` prints help instead of entering a session. That's
not a hedge on the decision — `--stdin` is already the design's explicit
flag for "read session commands from stdin," and letting bare `gage`
silently do the same thing would give one behavior two spellings, with
the implicit one being the surprising one inside a script.

Wired in M0, consumed by M6. See Q-HELP-SURFACES below for what falls out
of it.

---

### `[x]` Q-HELP-SURFACES — Are `gage help` and in-session `help` the same thing?

**Resolved 2026-08-30.** Two surfaces, one source of truth.

They cannot be the same text: `gage --help` must not list
`use`/`lock`/`status`/`exit` (they don't exist outside a session, and
listing them sends an operator down a path that can't work), while
in-session `help` must list exactly those. The entry commands overlap,
and that overlap is where two hand-maintained lists would silently drift
as commands land across M4, M5, M7, M8, and M12.

**The answers:**

1. **`gage help` is supported and equivalent to `gage --help`.** It's
   what every operator tries first. Both are tested, including
   `gage help <subcommand>`.
2. **In-session `help <command>` mirrors `gage help <subcommand>`.** The
   registry makes it nearly free, and asymmetry there is its own small
   surprise.
3. **Both surfaces render from one command registry** — name, aliases,
   short description, group, and availability. Adding a command updates
   both help surfaces or neither. A test compares the rendered sets
   rather than eyeballing two lists.
4. **`-u|--use` appears in per-command help** (`help show` lists it),
   **not in top-level session help**, which leads with bare
   `use <vault>`. The flag genuinely works ad hoc inside a session — the
   design doc says so — so hiding it entirely would hide something
   functional; leading with it would bury the primary spelling.

**Registry placement: `cmd/gage`, not `internal/gage`.** Command names
are CLI vocabulary — a GUI/TUI calls library methods directly and never
needs a command table. Putting it in the library would also collide with
M0's no-terminal-I/O lint rule. Worth stating because it's a reasonable
thing to get backwards.

**Registry shape:** name, aliases (`search`/`grep`, `status`/`whoami`,
`exit`/`quit`), short description, group (mirroring the design doc's own
command-reference sections rather than a flat 25-item list), and
availability.

**Availability tagging** (see Q-CMD-AVAILABILITY below):

| Availability | Commands |
|---|---|
| session-only | `use`, `lock`, `status`/`whoami`, `exit`/`quit`, `help` |
| both | `show`, `cat`, `ls`, `insert`, `edit`, `rename`, `generate`, `rm`, `mv`, `cp`, `search`/`grep`, `reindex`, `sync`, `pull`, `push`, `log`, `history`, `vault list/info/remove/set-default`, `identity add/list`, `recipient add/remove/list/verify`, `git set-remote` |
| one-shot only | `init`, `clone` |

---

### `[x]` Q-CMD-AVAILABILITY — Which management commands work in-session?

**Resolved 2026-08-30.** Everything except `init` and `clone`.

The design doc lists "all entry commands" plus sync/git/log/history as
session-available and is silent on the management commands, but the
registry needs every command tagged. Management commands (`vault`,
`identity`, `recipient`, `git set-remote`) work in-session: `recipient
add` right after being unlocked is exactly when you'd want it, and
forcing an exit for it would be a poor edge.

`init` and `clone` stay one-shot because they create a vault rather than
operating on the current one, which leaves an unanswered question about
whether the new vault becomes the session's current vault. Widening this
later is additive; narrowing it would be breaking — so the conservative
side is the right one to start on.

Needs a design-doc amendment (A11 below), since the doc's session command
list is currently entry-commands-only.

---

### `[x]` Q-METHOD-FLAG — Is `--method` required, and how is it validated?

**Resolved 2026-08-30.** `--method` gets exactly the same treatment as
`--type`: **optional, defaulting to `passphrase`, validated against a
single-value allowlist.** `ssh`, `yubikey`, `age-key`, `secure-enclave`,
and `plugin:<name>` arrive later as additional accepted values — additive
to the CLI surface rather than a flag introduced for the first time on
configs and scripts that predate it.

Applied to the design doc as A13. Note this changed the command reference
from `--method <required>` to `[--method passphrase]`, and dropped the
not-yet-real values from the usage line — they're described in the prose
instead, so the usage line documents what the tool actually accepts.

Q-METHOD-SCOPE below — a separate question that happens to touch the
same word — was surfaced by this one and is now resolved too.

---

### `[x]` Q-METHOD-SCOPE — Is a vault's method one-per-vault, or per-device?

**Resolved 2026-08-30 — option 2: per-device.** A vault's `[method]` is a
*default* for devices joining it, not a constraint. `gage identity add
--method` is meaningful and keeps its flag. Applied as A14.

**What follows from it:**

- `.gage/config.toml`'s `[method] kind = ...` becomes
  `[method] default = ...` — the old name read as the constraint
  interpretation being removed. No compatibility concern; nothing is
  built yet.
- **Each device's actual method is recorded in local state, not in the
  vault.** A method is an identity concern, and the design's own
  `identity`/`recipient` split says identities are device-side and never
  committed. Nothing else needs it: unlock consults only your own
  method, and `recipient add` only ever needs a public key.
- That placement has a security benefit worth keeping deliberately: a
  committed `device = "laptop-1", method = "passphrase"` line would tell
  anyone with vault read access which recipient is the softest target,
  while enabling nothing in return.
- `gage init --method` sets both the vault default and the first
  device's own method; `gage identity add --method` sets only that
  device's, defaulting to the vault's default.
- Principle 1 reworded — it no longer claims one method per vault.

**Couples to Q-DEVICE-NAME below:** the local per-vault record now
has to hold both this device's identity *name* and its *method*. Whatever
Q-DEVICE-NAME settles on as the local record is where both live, so the
two should be answered together.

**The reasoning, retained:** nothing cryptographic forced one method per
vault. age recipients are just public keys, and a single `.age` file can
be encrypted to a mix of X25519 and plugin recipients — a YubiKey
recipient is `age1yubikey1...` sitting in `.age-recipients` beside any
other. A vault-wide method would have been policy dressed up as a
constraint.

The original contradiction, for the record — resolving Q-METHOD-FLAG made
it visible rather than causing it:

- **Principle 1** says "each vault has exactly one decryption method
  chosen at `init` time," and `.gage/config.toml` carries a single
  `[method] kind = ...` to match.
- **["`identity` vs `recipient` stay separate"](../tdds/gage-cli-design.md)**
  says the opposite: "A YubiKey-based vault might have a laptop identity
  via NFC/USB touch and a phone identity via the Secure Enclave — same
  recipient list, different local mechanics." Secure Enclave and YubiKey
  are two different `kind` values in the config enum, not two flavors of
  one.
- **`gage identity add --method ssh|yubikey|passphrase|secure-enclave`**
  takes a method flag at all, which only makes sense if a device can
  choose one — but its own description says it "registers this device's
  way of satisfying *the vault's* method," which implies it can't.



---

### `[x]` Q-DEVICE-NAME — Where is this device's identity name *and method* recorded?

**Resolved 2026-08-30.** `device` and `method` fields under
`[vaults.<name>]` in global config, written by `init`/`identity
add`/`clone`. Both are per-vault, because both can legitimately differ
between vaults on one machine. Applied as A15.

**Default device name: the normalized hostname.** Lowercased, truncated
at the first dot (`Andrews-MacBook-Pro.local` → `andrews-macbook-pro`),
anything outside `[a-z0-9._-]` replaced with `-`, runs collapsed, length
capped. If normalization leaves nothing usable, `gage` prompts rather
than inventing a name.

**`--device NAME` added to `init` and `identity add`.** This turned out
to be a gap rather than a choice: neither command had *any* way to name
a device, so "hostname is the default" was unimplementable — a default
needs something to be a default *for*. The flag is also the escape hatch
for the disclosure noted under Accepted risks below.

**Security consequence, and the reason this resolution grew:** a device
name is a filename component in
`$GAGE_DATA/identities/<vault>/<device>.age`, and it arrives from
`.gage/config.toml` — a committed file that, per the design's own
"Trust boundaries," anyone with git write access can edit. A
`[[recipients]]` entry reading `device = "../../../../etc/cron.d/x"`
must be rejected, not resolved. Device names are therefore validated
against the character allowlist on read *and* on write, and no path is
ever constructed from an unvalidated one. This is the only place a
vault's plaintext metadata reaches the local filesystem, so it's treated
as untrusted input. Tested in M1 (config read) and M2 (path
construction).

Collision handling was already covered: M9 rejects an `identity add`
whose device name is already a recipient of that vault.

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

### `[x]` Q-DEVICE-DEFAULT-NAME — see Q-DEVICE-NAME above

Folded into Q-DEVICE-NAME and resolved there: normalized hostname by
default, `--device NAME` to override.

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

## Design-doc amendments

Changes [gage-cli-design.md](../tdds/gage-cli-design.md) needs. `[x]`
means applied to the design doc; entries are kept after application as a
record of what changed and why.

**Applied 2026-08-30:** A1, A2, A3, A5, A7, A8, A9, A10, A11, A12, A13,
A14. **Still open:** A4 and A6 — both are blocked on unresolved questions
(Q-DEVICE-NAME and Q-GIT-AUTH respectively) and can't be written until
those are answered.

### `[x]` A1 — Add a concurrency section

The design's "Trade-off vs. a shared agent" actively makes multi-process
the normal case (a session in one pane, one-shot commands elsewhere).
Every write is a commit against one working tree, and M9 adds a "reset
`entries/` to HEAD if dirty" precondition on every write — which, run
concurrently, will discard another process's in-flight state. The design
doc needs a short section stating the advisory-lock model, what's held
across what, and the contended-lock behavior. Decision already taken
(primitive in M0, first used at M4); the doc just doesn't reflect it.

### `[x]` A2 — Resolve the `generate` title-vs-query contradiction

["Addressing entries"](../tdds/gage-cli-design.md) lists `generate`
among the query-taking commands; the command reference says
`gage generate <title>`. `generate` creates a new entry, so it takes a
title — the query framing is most likely a leftover. Confirm and remove
`generate` from the query-taking list.

### `[x]` A3 — State the scrypt sole-recipient invariant

age refuses to encrypt to a scrypt passphrase recipient combined with
any other recipient. That's fine for the identity file as designed
(scrypt-only), but the invariant should be written into "Local identity
storage" so nobody later tries to add a second recipient to
`<device>.age`.

### `[x]` A4 — Say where the device name is recorded

Superseded by A15, which covers this and the naming/validation rules
Q-DEVICE-NAME turned out to need.

### `[x]` A5 — Specify `format_version` enforcement

The field appears in the example `.gage/config.toml` and nowhere else. A
forward-compat marker that isn't enforced from v1 is worse than none —
an old binary will half-parse a v2 config. State that an unrecognized
`format_version` is a clean refusal, not a best-effort parse.

### `[x]` A6 — Document git remote auth

Per Q-GIT-AUTH. New "Remote authentication: HTTPS and a token"
subsection under the sync model, stating go-git's limitations outright
(no `~/.ssh/config`, no credential helpers, no `insteadOf`), the token
storage location and scoping guidance, GitHub's friendlier acquisition
step, and SSH's best-effort status. `gage auth login/status/logout`
added to the git-specific command list, with a note on why it's
namespaced there and why it's per-host rather than per-vault.

Also rewrote both example remotes from SSH to HTTPS — the `work` one
(`git@internal:secrets/work-vault.git`) was itself an instance of the
problem, depending on a `~/.ssh/config` host alias that go-git would
never resolve.

### `[x]` A7 — Note that the metadata index is invalidated by sync

The index is described as session-scoped and incrementally updated on
insert/edit/rm. It doesn't mention that an auto fetch + fast-forward pull
on unlock changes `entries/` underneath a live session. One sentence in
"Addressing entries & the metadata index."

### `[x]` A8 — Fix the stale cross-reference

Not a design-doc issue — was in the plan, now fixed: the plan pointed at
`../designs/gage-cli-design.md`, which moved to `../tdds/` in `c0ee608`.
Recorded here only so the move is on the record.

### `[x]` A9 — State the bare-`gage` TTY carve-out

Q-ROOT-CMD is resolved as "session on a TTY, help when stdin is piped."
The design doc currently says only that running `gage` with no subcommand
drops you into the prompt, which read literally would make a piped bare
`gage` a second, implicit spelling of `--stdin`. One sentence in "Session
model."

### `[x]` A10 — Document the two help surfaces

The design doc mentions `help` exactly once, in the session-only command
list, and never mentions `gage help`/`gage --help` at all. Given
Q-ROOT-CMD makes bare `gage` a session rather than a help dump, the
command reference should state plainly that there are two help surfaces,
what each covers, and that session-only commands never appear in the
one-shot surface. Per Q-HELP-SURFACES: `gage help` ≡ `gage --help`,
`help <command>` works in both, and both render from one registry.

### `[x]` A11 — Widen the session command list to the management commands

["Session-only commands"](../tdds/gage-cli-design.md) says "all entry
commands (`show`, `ls`, `insert`, ...) work against the current session
vault" and doesn't mention `vault`/`identity`/`recipient` at all. Per
Q-CMD-AVAILABILITY those are session-available too, and `init`/`clone`
are the only one-shot-only commands. The doc should say so, since as
written it reads like an exhaustive list.

### `[x]` A12 — `gage git` in the session command list is imprecise

The same list includes bare `git` among the commands that "work against
the current session vault," but the only git-specific command is
`gage git set-remote <name> <url>`, which takes an explicit vault name
and so isn't scoped to the session's current vault at all. Minor, but
it's the kind of thing that produces a wrong help entry — worth
correcting when A11 is applied.

### `[x]` A13 — `--method` is optional and defaults to `passphrase`

Per Q-METHOD-FLAG. The `gage init` usage line showed `--method` as
required and listed six values, only one of which exists. Now
`[--method passphrase]`, with the future values described in the
following prose rather than advertised in a usage line as though they
were accepted today.

Deliberately did **not** touch `gage identity add --method` in this
amendment; that followed in A14 once Q-METHOD-SCOPE was answered.

### `[x]` A14 — Decryption methods are per-device, not per-vault

Per Q-METHOD-SCOPE (option 2). Five changes:

1. **Principle 1 reworded** — no longer claims "exactly one decryption
   method chosen at `init` time." Now: recipients are the trust
   boundary, and the vault records a *default* method while the method
   itself is a per-device choice.
2. **New section, "Decryption methods are per-device"** — why nothing
   cryptographic required otherwise, why a device's actual method is
   local rather than committed, and what `[method].default` is for.
3. **`[method] kind` → `[method] default`** in the vault config, with
   the comment relabeled as a default rather than a constraint.
4. **`gage identity add`** — usage becomes `[--method passphrase]`,
   described as this device's own choice, defaulting to the vault's
   default, on the same allowlist as `init`.
5. **Residual "the vault's method" claims corrected** in `gage init`
   ("chosen default method… using that same method for this device") and
   `gage clone` ("learn the vault's default method, which is what a
   subsequent `gage identity add` will suggest"), plus the
   `identity`/`recipient` bullet, which now states the per-device rule
   explicitly instead of merely implying it.

### `[x]` A15 — Device naming, storage, and validation

Per Q-DEVICE-NAME. Four changes:

1. **Global config gains `device` and `method`** under
   `[vaults.<name>]`, with prose on why both are per-vault and why both
   are local rather than committed.
2. **"Where the device name comes from"** in "Local identity storage" —
   the hostname default and its normalization rules, and `--device` as
   the override.
3. **`--device NAME` added** to `gage init` and `gage identity add`
   usage, plus the already-registered-name rejection on `identity add`.
4. **"Device names are validated before they're ever used as a path"** —
   the path-traversal rule, framed against the design's own trust
   boundary (a committed file that any git-writer can edit).

---

## Accepted risks

### A hostname device name discloses whose machine it is

`device` defaults to the normalized hostname and is written into the
vault's committed, plaintext `.gage/config.toml`, where every recipient
can read it — `andrews-macbook-pro` names a person as much as a machine.

Accepted because the people who can read a vault generally already know
whose devices are on it, and because a recognizable device name is the
whole point of the trust-cache diff being legible ("a bare `age1...`
list doesn't" — see "Local trust cache"). `--device NAME` is the escape
hatch, and is worth reaching for on vaults shared beyond people who
should know your machine names.

Note the disclosure is bounded to the *name*: per Q-METHOD-SCOPE, a
device's unlock method stays local, so the committed config never
reveals which recipient is the softest target.

### Entries carry no format version of their own

`format_version` lives on the vault config, not on each entry's YAML.
Adding an entry-level schema version later would be a breaking change to
existing ciphertext. Accepted on the grounds that the vault-level version
can gate an entry-schema migration — a vault at `format_version = 2` can
be defined to contain v2 entries. Revisit if entry schema churn turns out
to be more likely than expected.
