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
- **No host-specific code paths.** (Initially this resolution added
  go-github for GitHub conveniences; Q-OAUTH-APP below then removed both
  the device flow and the dependency. The final position is
  host-neutral: a user-supplied token, everywhere.)
- **SSH remotes stay best-effort**: used when ssh-agent has a usable key,
  but `~/.ssh/config` is never interpreted, so an alias-dependent remote
  fails with a message saying so and pointing at the HTTPS spelling.
  Same posture as the Windows core-dump gap — a narrow guarantee stated
  honestly. If this proves more trouble than it's worth in M8a, dropping
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
Covered by an M8a test.

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
instant, and always happens"](../../tdds/gage-cli-design.md) durability
guarantee assumes a local object store that an API-backed vault wouldn't
have. It's also a *vault type* question as much as an auth question, so
it may belong under the design doc's "Vault types" section rather than
replacing the `git` type outright. Worth deciding whether it's
`type = "github"` alongside `type = "git"`, or a replacement.

Nothing before M8a depended on the answer, so it stayed open through M7
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
- **["`identity` vs `recipient` stay separate"](../../tdds/gage-cli-design.md)**
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

### `[x]` Q-SYNC-CONFLICT — What does conflict resolution actually do?

**Resolved 2026-08-30.** Applied as A16. M8 split into
[M8a](m8a-sync-transport.md) (transport + detection) and
[M8b](m8b-sync-conflicts.md) (resolution).

**The main finding was that "the entry conflicts" is one of five
situations, not the whole problem:**

| Situation | Git's view | Handling |
|---|---|---|
| Different entries changed | Merges cleanly | Nothing to ask |
| Same entry changed | Conflict (binary) | Decrypt both, ask |
| Delete vs. modify | Conflict | Decrypt survivor, ask |
| **Recipient files changed** | **Would merge cleanly** | **Forced to conflict via `.gitattributes`** |
| Same title, different UUIDs | Merges cleanly | Not a conflict — resolver handles it |

**The recipient-file row is a security hole that existed in the design.**
`.age-recipients` is one key per line, so two devices each adding a
*different* recipient add two different lines and git merges them without
any conflict. The result is a recipient list neither device wrote,
assembled by a clean-looking merge, with every subsequent write encrypted
to all of it — and **no unreviewed change for the trust cache to catch**,
because the merge manufactured it rather than a person doing so. Fixed
with a `.gitattributes` marking `.age-recipients` and `.gage/config.toml`
`-merge`. Written by `init` in M1, its effect tested in M8a.

**The answers:**

1. **Resolution menu: keep local / keep remote / keep both**, plus skip
   and abort. `keep both` writes the losing version as a new entry under
   a fresh UUID, so resolving never destroys a secret — the two versions
   become two same-titled entries that M5's ambiguous-query resolver
   already handles. Choosing wrong under pressure becomes recoverable
   rather than a silent loss, which is the right default here.
2. **History: a true merge commit**, two parents. Preserves both devices'
   history and local commit granularity, and keeps the vault an ordinary
   git repo. Rebasing would re-prompt for the same entry once per local
   commit that touched it; flattening to one commit would discard
   per-write audit trail.
3. **Unlock is lazy.** Fast-forwards and merges touching only different
   entries need no identity at all — nothing has to be decrypted. `sync`
   prompts for unlock only on reaching a conflict that needs plaintext
   shown. This preserves the property M10 depends on: a clean sync may
   never call `Vault.Unlock`, which is why the trust-cache check hooks
   `sync` directly.
4. **Same-title-different-UUID is not a conflict.** Detecting it would
   mean decrypting every entry on both sides, forcing an unlock on every
   sync including clean fast-forwards. The ambiguous-query resolver
   reaches the same outcome later for free.

**M8 split.** Detection and resolution are separable risks — the same
isolation the plan uses for crypto (M2) and the trust cache (M10). M8a
leaves a fully usable tool where divergence is detected, classified, and
reported; M8b adds acting on it. Kept as `M8a`/`M8b` rather than
renumbering, so M9–M12 are unaffected.

Sub-decisions left to M8b itself (recorded there, not here): whether
`keep both` suffixes the duplicate title, whether it preserves the losing
side's `updated_by`, non-interactive behavior, and lock-holding during a
human-paced resolution.

---

### `[ ]` Q-KEEPBOTH-DELMOD — Should `keep both` degenerate to `keep remote`/`keep local` on a delete/modify conflict? {#q-keepboth-delmod}

**Blocks:** nothing; M8b shipped with the behavior below and a test
pinning it, so this is a refinement question, not something stuck.

On a delete/modify conflict — one side removed the entry, the other
edited it — there is only one surviving version, yet `keep both` still
allocates it a **fresh UUID** rather than restoring it at the entry's
original id the way `keep remote` (or `keep local`, if the deletion was
remote) would. The two options end up applying the same content and
differing only in which UUID it lands under, because there is nothing
for the losing side to contribute when it deleted rather than edited.

That may be harmless — the entry is still exactly one entry either way,
just addressed by a new id — but it's also a UUID change as a side
effect of a choice that reads, from the `[l/r/b/s/q]` prompt, as "keep
both versions," when there is only one. Worth deciding whether `keep
both` should special-case an absent side and behave like the
corresponding `keep remote`/`keep local` (preserving the original id),
or whether relocating the id is an acceptable — maybe even irrelevant —
cost of not special-casing it. Current behavior (fresh id) is pinned by
`TestKeepBothOnADeleteModifyConflictKeepsTheSurvivor` in
`internal/gage/syncresolve_test.go`, which would need updating if this
is answered the other way.

---

### `[x]` Q-ENROLL-VERBS — What are the device-enrollment commands called? {#q-enroll-verbs}

**Resolved: `gage identity enroll` joining, `gage recipient
pending`/`approve`/`deny` approving.** Full reasoning and the rendered
help listing are in D-ENROLL-VERBS in
[gage-cli-init-design.md](../../tdds/gage-cli-init-design.md); the short
version is below. No new top-level noun, no new help group, and
`groupOrder` untouched.

**What decided it, and it wasn't taxonomy.** `enroll` turned out to be
`identity add` *plus publishing* — literally the same `CreateIdentity`
call with the same create-or-reuse behavior, differing only in what
happens once the key exists. A command belongs next to the command it is
a superset of. Neither option originally on the table could express that,
because both put the two verbs in different namespaces.

An earlier draft of the enrollment doc asserted a behavioral difference
between the two (that `identity add` refused to overwrite where `enroll`
reused). That was wrong — both reuse — and finding it is what produced
the answer.

The feature has two actors with two different mental models — a device
asking to join, and a device that already has access deciding whether to
let it — and the naming question is whether that split should be visible
in the command surface.

- **Split, top-level `gage enroll`** (the draft's provisional
  spelling). Rejected: puts a superset command in a different namespace
  from its own base command, and adds a fourth top-level noun beside
  `vault`/`identity`/`recipient` whose only member is this feature.
- **Unified `gage enroll request/list/approve/deny`.** Rejected despite
  reading best in isolation: it moves a recipient-list write out of
  `recipient`, and needs a new `groupOrder` entry or the group sorts
  after Git-specific.
- **Chosen: `identity enroll` + `recipient pending/approve/deny`.**
  Joining lives with the other key-creating verb; approval lives where
  every one of its effects lands.

**Accepted cost:** the feature spans two help groups and never renders
as one story. Judged minor, because the approving side is discovered
from the joining device's own output — which prints the exact command to
run — rather than by scanning `gage help`.

**Follow-on for whoever implements it.** All six commands register with a
`Short`, a `Group`, and an availability, or M0's completeness test fails;
all are session-available, since none creates a vault the way
`init`/`clone` do. `recipient add`'s description also needs rewording for
A19 ("Authorize a public key and re-encrypt the vault to include it"),
which is A19's work rather than this decision's.

---

### `[x]` Q-IDENTITY-VAULT-NAME — an identity file is keyed by vault *name*, which is not unique {#q-identity-vault-name}

**Resolved: key the identities directory by a vault id minted at `init`
and committed to `.gage/config.toml`.** Applied to the design doc as
[A20](#a20) and implemented in E0. The full reasoning is below, after the
problem statement it answers.

**Blocks:** nothing formally, but it should be treated as a live bug
rather than a design tidiness question — one branch **silently destroys
the only copy of a private key**, and reaching it needs no new feature.
Found while tracing `identity enroll`'s reuse path for
[gage-cli-init-design.md](../../tdds/gage-cli-init-design.md); the
data-loss branch was found on a second pass and is not caused by
enrollment at all.

**The severity was understated when this entry was first filed.** It was
originally recorded as "two vaults share a keypair," which is real but
recoverable. The second harm below is not.

Identity files live at `$GAGE_DATA/identities/<vault>/<device>.age`,
where `<vault>` is the *local registration name* — which is chosen at
clone time (inferred from the URL, or `--name`) and is not tied to the
remote in any way. Two unrelated vaults can therefore share a directory
of identities on one machine.

The sequence that goes wrong:

1. `gage vault remove personal` — which, per #39, **keeps** the identity
   file when this device is still a listed recipient.
2. `gage clone <a-different-remote> --name personal`.
3. `gage enroll` — `CreateIdentity` finds the surviving file and reuses
   it, so the request published to the *new* vault names a keypair that
   belongs to the *old* one.

Nothing is leaked and nothing is stolen — it's the user's own key — but
it silently binds two unrelated vaults to a single keypair, which is
exactly what the per-device, per-vault identity model exists to avoid.
Revoking access to one vault then can't be done without affecting the
other, and the design doc's "Local identity storage" section reads as
though that can't happen.

**The second harm is worse, and needs no enrollment.** `vault remove`'s
orphan cleanup can delete a key that belongs to a *different* vault:

1. Vault A is registered as `personal`; `laptop-1` is a recipient; the
   key is at `identities/personal/laptop-1.age`.
2. `gage vault remove personal` — the key is **kept**, because
   `laptop-1` is still a recipient of A. #39 working as intended.
3. `gage clone <remote-B> --name personal` — allowed, the name is free.
   `clone` records `Device: laptop-1` in global config from the hostname
   default (`cmd/gage/clone.go`), even though no identity was created
   for B.
4. `gage vault remove personal` — now removing **B**.
   `removeOrphanedIdentity` finds `identities/personal/laptop-1.age`,
   reads **B's** recipient list, does not find `laptop-1`, and deletes
   the file.

That file was vault A's only key. A's repository still exists and
nothing can read it. No confirmation, no warning, unrecoverable. Steps
3–4 are just "cloned the wrong repo, then removed it."

**Root cause is one the enrollment work already named.**
`removeOrphanedIdentity` decides whether to delete a private key by
comparing `r.Device == entry.Device` — a *name* comparison. That is the
same mistake `D-ENROLL-COLLISIONS` resolves for enrollment: the device
name is a label, not an identity. Here it is load-bearing for a
destructive, irreversible operation.

Options, roughly in order of cost:

- **A marker file in `identities/<vault>/` recording the origin URL.**
  Plaintext, non-secret, entirely under `$GAGE_DATA`, so no vault-format
  change and no `format_version` bump. `clone` consults it before
  reusing anything; `vault remove` consults it before deleting. It goes
  stale if `git set-remote` repoints a vault, so it should warn rather
  than refuse — unlike the public-key sidecar rejected in the enrollment
  doc, a stale answer here causes a needless prompt rather than a wrong
  key being published.
- **Key the identities directory by something stable** (a vault id
  minted at `init` and committed to `.gage/config.toml`) rather than by
  the local name. Correct, but touches M1's on-disk contract.
- **Have `vault remove` never delete an identity**, reverting #39's
  cleanup. Closes the data-loss branch outright at the cost of the
  clutter #39 set out to fix — a strictly safe direction, and the right
  stopgap if the real fix is going to take a while.
- **Refuse `clone --name X` when `identities/X/` is non-empty.** Closes
  it at the entry point, but breaks the legitimate re-clone-the-same-
  vault flow (`TestReinitAfterVaultRemoveReusesTheKeptIdentity`) unless
  it can distinguish same from different — which needs one of the first
  two options anyway.

**"Accept and document it" is not on this list**, which it would have
been for the key-coupling harm alone. Silently destroying the only copy
of a private key is not something to write down and live with.

## The answer: an assigned id, not a derived one

**`$GAGE_DATA/identities/<vault-id>/<device>.age`**, where `<vault-id>`
is a UUIDv4 minted once by `gage init` and written to `.gage/config.toml`
as `[vault].id`. Because it lives in the committed config, it clones with
the vault: every clone of the same vault agrees on it, and two different
vaults can never collide however they are named locally.

**Hashing the origin URL was considered and rejected.** It is the same
instinct — key by something belonging to the vault rather than to a local
nickname — applied to a value that cannot carry it:

- **A remote URL is not canonical.** `https://host/you/v.git`,
  `https://host/you/v`, a trailing slash, an scp-style `git@host:you/v`,
  an embedded `user@`, host casing — one remote, many spellings, each
  hashing differently. Two clones of the *same* vault spelled two ways
  would get separate identity directories, and the second would generate
  a fresh keypair instead of reusing the good one. Avoiding that means
  canonicalizing URLs, which is never complete — the same open-ended
  parsing trap this project already refused when it declined to
  interpret `~/.ssh/config` (Q-GIT-AUTH).
- **`git set-remote` legitimately changes it**, orphaning every identity
  under the old hash. And "start local-only, gain a remote later" is a
  documented lifecycle, so that migration would be the normal path
  rather than an edge case.
- **Local-only vaults have no origin at all.** They are not exempt from
  the bug — they can be the vault whose key gets deleted, and two of
  them collide with each other through `init`/`remove`/`init` — so a
  URL-derived scheme leaves them with no key to derive from.

An id has none of these properties: it is assigned rather than derived,
so it is stable by construction and independent of transport.

**The cost is a `.gage/config.toml` schema change, and this is the
cheapest it will ever be.** Nothing has shipped — Q-RELEASE is still
open and there is no distribution — so there is no installed base to
migrate. `format_version` goes to 2, and a v1 vault is refused by the
check that already exists, with "re-create this vault" as the migration.
That is only acceptable because the population of affected vaults is
developer machines, and `make reset-local-state` already exists to clear
them. Deferring this decision makes it strictly more expensive.

**Details that are part of the decision, not follow-up:**

- **The id is untrusted input.** It arrives from a committed file any
  git-writer can edit and is used as a filesystem path component —
  exactly the condition Q-DEVICE-NAME imposed validation for. It is
  validated as a UUID before any path is built from it, on the way in
  *and* on the way out, and a config whose `id` does not parse is
  refused rather than helpfully coerced.
- **Global config records the id too**, under `[vaults.<name>]`. Without
  it, `vault remove` could not identify which identities directory a
  vault owns once that vault's own files are gone or moved — which is
  precisely the situation the destructive branch above arises in.
- **The directory carries a plaintext marker naming the vault.** An
  opaque UUID directory would otherwise break the "a user is free to
  back up a device's wrapped identity file themselves" story the design
  doc explicitly permits. The marker is advisory — nothing reads it to
  make a decision.

**This does not close the whole bug, and the remainder should not be
folded into it.** Correct keying removes the cross-vault collision,
which is the destructive half. But `removeOrphanedIdentity` still
decides whether to delete a private key by comparing device *names*
(`r.Device == entry.Device`) — the same label-versus-identity confusion
`D-ENROLL-COLLISIONS` settled for enrollment. That comparison deserves
fixing on its own terms rather than being declared safe because
collisions got rarer.

Related: the enrollment doc's "Enrolling with an identity you already
have" explains why the reuse path exists and why it cannot cheaply
verify what it is reusing.

---

### `[x]` Q-ORPHAN-BY-NAME — `vault remove` deletes a private key based on a name comparison {#q-orphan-by-name}

**Resolved: compare public keys, and never delete without asking.**
Implemented in [E0](../gage-cli-init-design/e0-vault-id-keying.md); the
`pubkey` field it needs is folded into [A20](#a20), which was already
changing the same config table. Full reasoning after the problem
statement.

**Blocks:** nothing, but it is the unfixed remainder of
[Q-IDENTITY-VAULT-NAME](#q-identity-vault-name), and it is filed
separately on purpose. That entry is marked resolved, and an unresolved
remainder living inside a resolved entry is exactly the silent deferral
this register exists to prevent.

`removeOrphanedIdentity` (`cmd/gage/vault.go`) decides whether to delete
this device's wrapped private key by comparing device **names**:

```go
for _, r := range recipients {
    if r.Device == entry.Device { /* keep */ }
}
// ...otherwise delete the key file
```

That is the same label-versus-identity confusion `D-ENROLL-COLLISIONS`
settled for enrollment — the device name is a label the vault happens to
store, not proof of which key it refers to. Here it gates an
irreversible destructive operation on the only copy of a private key.

**A20 makes this much harder to trigger and does not fix it.** Keying the
identities directory by vault id removes the cross-vault collision, which
is what made the wrong comparison catastrophic. What remains is a
within-vault version: a recipient entry relabeled, or a device name
reused after a `recipient remove`, can make the comparison answer
"not a recipient" about a key that is one.

The honest fix is to compare public keys rather than names — which needs
the local key's public half, which needs an unlock (see the enrollment
doc's "Enrolling with an identity you already have"). Options: prompt
before deleting (turning a silent side effect into a deliberate act),
never delete and accept the clutter, or delete only when the vault's
recipient list is empty of *any* plausible match and say what it did.

**Do not close this by declaring it rare.** It was already rare; the
severity comes from being unrecoverable, not from being frequent.

**It is wrong in both directions**, which is the clearest evidence the
comparison isn't measuring what it claims:

- **False delete, unrecoverable.** The vault lists this device's key
  under a label other than the one local config records — global config
  says `laptop-1`, the vault says `laptop-1-work`, same public key. No
  name match, so `vault remove` deletes a key that is an *active
  recipient*. Nothing warns beforehand: the device works normally until
  then, because `Unlock` locates the identity file by the *local* name,
  which is still correct.
- **False keep, harmless.** `recipient remove laptop-1 --reencrypt`,
  then some other device is added under that label with a different key.
  The stale local file now matches by name and is kept. Clutter only.

**`recipient approve --device` made the dangerous direction newly
reachable.** D-ENROLL-COLLISIONS lets an approver relabel an incoming
request — correctly, since the code authenticates the key and not the
label — but that produces precisely the false-delete state: the joining
device recorded one name at enroll time, the approver stored the key
under another. So this fix belongs with device enrollment rather than
after it.

## The answer

**The obvious repair is blocked.** Comparing public keys means deriving
this device's public key, which lives *inside* the encrypted identity
file, which needs an unlock — and prompting for a passphrase to decide a
cleanup side effect on a vault being removed is the same "exactly
backwards" pattern `clone.go` already refuses.

So the public key is recorded where it can be read without one:

1. **Record `pubkey` in global config**, beside `device` under
   `[vaults.<name>]`, written when the identity is created (`init`,
   `identity add`, `identity enroll`) — the moment gage holds the key
   anyway. It is public, and already sits in the vault's committed
   config, so there is no secrecy cost to a local plaintext copy. The
   comparison becomes `r.Pubkey == entry.Pubkey`: an identity
   comparison, no unlock. Folded into A20, which is already editing this
   table.
2. **Never delete without a `Confirm`, defaulting to keep.** This is the
   load-bearing half. Step 1 makes the *suggestion* accurate, but a
   stale record — someone swaps the `.age` file by hand — would make it
   confidently wrong, which is worse than uncertain. A prompt turns any
   wrong answer into a wrong suggestion a human reads, with the path in
   front of them. `app.Prompter` is already on the struct, so there is
   no plumbing, and `Confirm` defaults to no, which means a scripted
   `vault remove` keeps the file by construction.
3. **When the pubkey is unknown, say so and keep.** Missing field,
   hand-edited config, older install — do not guess.

**A20 also removes most of what this cleanup was for.** #39 deleted
orphans largely so that `vault remove` followed by `init` under the same
name wouldn't silently reuse the old key. Keying by vault id makes that
structurally impossible, so the deletion is doing less work than it was
designed for — which further favors asking over assuming.

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

Changes [gage-cli-design.md](../../tdds/gage-cli-design.md) needs. `[x]`
means applied to the design doc; entries are kept after application as a
record of what changed and why.

**Applied:** A1–A18 — A1, A2, A3, A5, A7, A8, A9, A10, A11, A12, A13,
A14 on 2026-08-30; A4 and A6 once Q-DEVICE-NAME and Q-GIT-AUTH were
answered; A15–A18 alongside the milestones that needed them.

A19 and A20 are the only amendments here that change shipped behavior
rather than describing it. `[x]` in this section has always meant "the
TDD says this," never "the code does"; that distinction matters for
exactly those two entries. **A20 is now implemented** (E0), together with
Q-ORPHAN-BY-NAME, which it carried the `pubkey` field for. **A19 is
not** — it is sequenced as E1a in
[plans/gage-cli-init-design/](../gage-cli-init-design/index.md), and the
outstanding work is listed under "Accepted, not yet implemented" in
[index.md](index.md).

A21 runs the other way: the code was right and the doc was wrong, so
applying it changed nothing but prose. A22 is the one entry deliberately
**not** applied yet — the layout it describes does not exist until E3
ships, and a design doc that describes a directory no build creates is
the failure mode this register exists to prevent.

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

["Addressing entries"](../../tdds/gage-cli-design.md) lists `generate`
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

["Session-only commands"](../../tdds/gage-cli-design.md) says "all entry
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

### `[x]` A16 — Conflict resolution, and the recipient-file merge hole

Per Q-SYNC-CONFLICT. Four changes:

1. **`.gitattributes` added to the on-disk layout**, with the reasoning:
   two devices each adding a different recipient would otherwise merge
   into a union neither wrote, invisibly to the trust cache. This was a
   real hole in the design, not a documentation gap.
2. **New "What \"diverged\" actually means"** — the five-situation table,
   why same-title-different-UUID isn't a conflict, and the lazy-unlock
   rule.
3. **New "Resolving an entry conflict"** — the `[l/r/b/s/q]` prompt,
   why `keep both` exists, and why the result is a merge commit rather
   than a rebase or a flattened one.
4. **Two bullets in "A few decisions worth calling out"** for the
   unmergeable recipient files and for `keep both`.

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

### `[x]` A17 — Document `--clip`'s clear semantics {#a17}

M12's resolution. The design doc says only that `-c` "copies to
clipboard, auto-clears after a short timeout," which leaves the part that
actually matters unstated: *which process* does the clearing. Needs, in
"Notes on `show`": the clear happens inside the process that wrote the
clipboard and never in a forked child (principle 5); one-shot `-c`
therefore blocks until the timeout with its notice on stderr, and
Ctrl-C clears early and still exits 0; in-session `-c` is a timer and
session exit clears a pending copy; the clear is skipped if the
clipboard changed after gage wrote it, compared by hash. Plus
`clipboard_timeout` (default `45s`) in the `[shell]` config example.

### `[x]` A18 — Say where a non-interactive session's passphrase comes from {#a18}

M12's resolution. "Non-interactive session mode" shows `--script` and
`--stdin` but never says how a run with no human at a TTY unlocks
anything — and `--stdin` structurally cannot prompt, since stdin is the
command stream. Needs: `GAGE_PASSPHRASE`, tried first, then a prompt if
stdin is a terminal, then a hard failure with `exitcode.LockedOrAuth`
naming the variable; that it is honored for unlocking an existing
identity only, never for choosing a new one's passphrase; and that a
script driving two vaults reassigns the variable between them, there
being no per-vault spelling.

---

### `[x]` A19 — Make re-encryption unconditional on `recipient add` {#a19}

**Accepted and applied to the design doc.** Per this section's
convention, `[x]` means the TDD now says this — it does **not** mean the
code does. This is the one amendment here that changes shipped behavior
rather than documenting it, so it carries an implementation gap until
the work below lands; that gap is tracked under "Accepted, not yet
implemented" in [index.md](index.md).

**Applied to the design doc as:** `--reencrypt` removed from
`gage recipient add` in "Recipient / access management"; a new
"Why adding a recipient always re-encrypts" subsection; and the
`--reencrypt` bullets under "A few decisions worth calling out" split
into one for `add` (no flag) and one for `remove` (flag stays
mandatory).

Raised while designing device enrollment
([gage-cli-init-design.md](../../tdds/gage-cli-init-design.md)), which
had already settled the same question the same way for `recipient
approve`: approval always re-encrypts and has no flag.

**The proposal:** `gage recipient add` re-encrypts every entry always.
`--reencrypt` is removed from `add` (it stays mandatory on `remove`,
where it means something different and is already required).

**Why.** Adding a recipient without re-encrypting produces a recipient
who can read entries written after their admission and not before, and
that state is a problem in three compounding ways:

1. **It contradicts design principle 1.** "A vault is the unit of trust.
   Each vault has its own set of recipients, and that list — nothing
   finer-grained — is who can read it." A partially-readable recipient
   *is* the finer-grained tier the principle rules out. The design's own
   answer to "these people should see less" is a second vault plus
   `mv --to-vault`, not a half-admitted recipient.
2. **It's undiagnosable from the interface.** The new device runs `ls`,
   sees every entry, and gets decryption failures on an arbitrary-looking
   subset. The dividing line — written before or after admission — is not
   the title, the age, or anything `ls` shows.
3. **It's contagious and unrepairable.** `reencryptTo`
   (`internal/gage/recipient.go:409`) decrypts every entry with the
   acting identity and hard-fails on the first one it can't read. So a
   partially-admitted device cannot repair itself *and cannot grant full
   access to anyone else* — its re-encryption pass dies partway, after
   the trust-cache prompt and inside the write lock, with an error naming
   an opaque entry UUID. Each generation is harder to diagnose than the
   last.

**What it costs.** Every `recipient add` rewrites every entry: a larger
repo over time, and a no-op-plaintext revision in each entry's history
that `history --decrypt` walks. Judged worth it — vaults are small, the
operation is rare, and the all-or-nothing machinery already exists.

**What it requires.** A typed refusal for the case that can no longer be
worked around: an actor who can't read every entry can no longer add a
recipient at all. That must fail before the lock and before any
confirmation, naming the count of unreadable entries rather than
surfacing a decryption error — `ErrCannotGrantFullAccess` in the
enrollment doc's spelling.

**If this is declined**, the enrollment doc should be revisited too:
`approve` having no flag while `add` has one is defensible (the new door
picks the better default) but leaves `add` as the vector that keeps
creating partial recipients, so most of the benefit is lost.

**Affected:** design doc "Recipient / access management" and the
`--reencrypt` bullets under "A few decisions worth calling out"; M9's
test list, which currently pins the opposite behavior.

---

### `[x]` A20 — Key the identities directory by a vault id {#a20}

**Applied to the design doc; implemented in
[E0](../gage-cli-init-design/e0-vault-id-keying.md).** Resolves
[Q-IDENTITY-VAULT-NAME](#q-identity-vault-name), whose second harm is a
live path that silently deletes the only copy of a private key.

**Applied as:** `[vault].id` added to `.gage/config.toml` (a UUIDv4
minted by `init`); `format_version` raised to 2; the identity path in
"Local identity storage" becomes
`$GAGE_DATA/identities/<vault-id>/<device>.age`; the **trust cache** path
becomes `$GAGE_STATE/<vault-id>/known-config.toml`; the id added to
global config's `[vaults.<name>]`; and the id folded into the
untrusted-input validation rule that already covers device names.

**`pubkey` is added to `[vaults.<name>]` in the same change**, per
[Q-ORPHAN-BY-NAME](#q-orphan-by-name): this device's public key for that
vault, recorded when the identity is created, so `vault remove` can tell
whether a local key is genuinely orphaned by comparing keys instead of
names. Bundled here because both fields land in the same table and both
exist for the same reason — a name was being used where an identity was
meant.

**The trust cache had the same flaw and is fixed in the same change.**
`TrustCacheDir(vault string)` keyed `$GAGE_STATE/<vault>/` by the local
name, so two vaults registered under one name in sequence shared a
cache. It is far less serious than the identity case — it fails safe (an
inherited cache produces a *spurious* recipient-change warning rather
than suppressing a real one) and the design already calls the cache
disposable — but there is no reason to leave one keying scheme correct
and its neighbour wrong when the id exists anyway.

**Affects:** M1 (config schema, `format_version`, global config), M2
(identity file paths), and the enrollment doc's references to the
identity path. Migration is "re-create the vault", which is only
acceptable because nothing has shipped — see the resolution for why
deferring makes this strictly more expensive.

**Does not close the whole bug on its own.** Correct keying removes the
cross-vault collision but not the label-versus-identity confusion
underneath it, which is [Q-ORPHAN-BY-NAME](#q-orphan-by-name) — filed
separately and, in the event, fixed in the same milestone, since both
land in the same config table.

**One thing E0 added that this entry did not name.** `LockFilePath` was
keyed by the local name too, and E0 makes "one repository registered
twice under two local names" a supported state — so two registrations
would have taken two different lock files and both written one working
tree. The lock protects a repository, so it is keyed by the id like the
other two. After E0 nothing under `$GAGE_DATA` or `$GAGE_STATE` is
addressed by a vault's local name.

---

### `[x]` A21 — the dirty-tree reset covers the whole working tree, not `entries/` {#a21}

**Applied to the design doc. No code change — this is the doc catching up
to what shipped.**

The design doc said in two places that gage "refuses to start any write
against a dirty `entries/` working tree" and "resets `entries/` to HEAD".
The shipped implementation resets the **whole** working tree and
**deletes untracked files** while doing it — `resetDirtyWorkTree`
(`internal/gage/vault.go`) calls `gitrepo.ResetHard`, whose own doc
comment states the reason: a crash in the window after `--reencrypt`
writes the recipient files but before it commits dirties those two as
well, and a reset scoped to `entries/` would leave exactly the
half-migrated state `--reencrypt` exists to rule out.

**Found while reviewing the enrollment plan**, which inherited the stale
claim and built a crash-safety story on it — a stray `.gage/pending/`
file was described as surviving to expire on its own epoch, when in fact
the next write on that machine discards it. Recorded here rather than
fixed silently because two documents and one milestone's test list were
reasoning from it, and because "what does a write do to a tree it didn't
expect" is a property worth being able to look up.

**Applied as:** both passages in
["Recipient / access management"](../../tdds/gage-cli-design.md) and the
concurrency section now say "dirty working tree", name the untracked-file
deletion, and carry the reason the scope is what it is. The enrollment
doc's "Clock skew" section and E3's test list are corrected to match.

---

### `[x]` A22 — the vault layout gains `.gage/pending/` {#a22}

**Accepted; applies to the design doc when E3 lands**, since that is the
first release in which a vault can actually grow the directory.

The base design's on-disk layout enumerates `.gage/`, `.age-recipients`,
`entries/`, `.gitattributes`, `.gitignore`. Device enrollment adds
`.gage/pending/`, holding one sealed request per file. The layout diagram
gains it with a one-line pointer to
[gage-cli-init-design.md](../../tdds/gage-cli-init-design.md) rather than
a restatement of the scheme, which lives there.

**Two properties belong in the base doc rather than only the enhancement
doc**, because they constrain readers of the layout who never read the
enrollment feature:

- **The directory is created lazily**, on the first enroll. Git does not
  track empty directories, so its absence is the normal state and means
  "no pending requests", never an error.
- **`pending/` is inert.** Nothing in it is read at encryption time. This
  is the invariant that keeps the directory from widening the trust
  boundary, and it is stated where someone auditing the layout will see
  it.

`.gitattributes` deliberately does **not** cover `pending/` — a union of
two devices' pending requests is the correct merge outcome, unlike a
union of two recipient lists. The reasoning is in the enrollment doc's
"`.gitattributes` deliberately does *not* cover `pending/`".

---

## Accepted risks

### Batch enrollment approval costs one scrypt run per code per pending request

`gage recipient approve` tries each supplied code against each pending
request, and each attempt is a deliberately slow scrypt KDF. Approving
3 devices with 10 requests outstanding is 30 runs.

**Knowingly not optimized for the benign case.** Pending requests expire
in 24h by default and are deleted on approval, so more than a handful
outstanding at once means something unusual is already happening.
Malformed codes are rejected on length and alphabet before any
decryption, so the common typo costs nothing.

**Two things this entry originally got wrong**, both found reviewing the
enrollment plan and both now resolved in the TDD as
`D-ENROLL-SEAL-COST`:

- **The benign case was slower than "30 runs" makes it sound**, because
  the entry assumed the identity file's work factor. Seals are now
  written at 14 rather than 19 — roughly 60ms a run instead of 2s —
  licensed by the code being 80 bits from `crypto/rand`, where the
  entropy is doing all the work the KDF was being paid for. That example
  is now a couple of seconds rather than a minute.
- **Neither multiplicand was bounded by anything gage controls.** The
  count comes from a directory any git-writer can fill, and the
  per-attempt cost comes from a work factor each blob *claims*. Both are
  now bounded: the open path caps a claimed factor the way `unlock.go`
  already does for identity files, and a code-trying run attempts at most
  32 live requests.

So the accepted risk is narrower than it was. A stuffed `pending/` is a
bounded refusal naming `approve <ID>`, which resolves from the filename
and is O(1) in directory size. That it is *possible* to make approval
briefly inconvenient is accepted, and is the same class as the filename
section's "anyone who can rename the file can equally delete it."

What is still ruled out is the shortcut this entry originally described:
adding unsealed hints about which code opens which request leaks who is
enrolling, which is the thing the filename scheme exists to prevent.

Recorded so a future reader doesn't mistake it for an oversight.


### The enrollment code reaches command history on the approving device {#enrollment-code-history}

`gage recipient approve --code GAGE-…` puts the code on a command line.
In a session that line is recorded verbatim — `history.add` writes the
typed line and nothing filters arguments (`cmd/gage/history.go`) — and in
a one-shot run it lands in the user's own shell history, which `gage`
cannot see at all.

**The enrollment doc originally claimed the code "exists nowhere else…
not in the session history file (which already refuses to record
values)".** That claim is true of the *joining* device, which generates
the code and never takes one as input, and it was read across to the
approving device, which does. The history file's guarantee is narrower
than the parenthetical suggested: it never contains a decrypted value
because no command ever puts one on a line, not because it filters
anything.

**Accepted rather than fixed**, and the exposure is genuinely small:

- The file is `0600` and device-local; `gage` tightens the mode on every
  open rather than trusting an existing file.
- The code is dead in 24 hours by default and the request it opens is
  deleted on approval, so what a recovered code can do is nothing.
- Cracking the seal was never the interesting attack anyway — what a code
  buys is the ability to forge a *request*, which still has to be
  approved by a human.

**And there is an escape hatch that costs nothing.** Omitting `--code`
makes `cmd/gage` ask through `Prompter.Value`, which is masked and never
recorded. An approver who cares reaches for the prompt, which is already
the specified fallback rather than something added for this.

Filtering `--code` out of `history.add` was considered and not taken: it
would make the history writer argument-aware for the first time, for a
partial fix that leaves the shell-history half — the larger half —
untouched. Recorded here so the narrowed claim in
[gage-cli-init-design.md](../../tdds/gage-cli-init-design.md)'s
D-ENROLL-CODE-FORMAT has somewhere to point.


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

### go-git's `Push` doesn't wrap `ErrNonFastForwardUpdate`, so M8a classifies divergence by elimination instead

Confirmed against `go-git/go-git/v5@v5.19.2`: `Worktree.Pull`'s ff-only
check returns the exported sentinel `git.ErrNonFastForwardUpdate`, but
`Remote.Push`'s own client-side fast-forward check
(`checkFastForwardUpdate` in `remote.go`) returns a bare
`fmt.Errorf("non-fast-forward update: %s", ...)` that does **not** wrap
that sentinel. Code written as `errors.Is(err, git.ErrNonFastForwardUpdate)`
on a `Push` result compiles cleanly and passes review, but silently never
matches a real push-time divergence — it would misreport every genuine
conflict as an unclassified/internal error instead of `exitcode.Conflict`.

Accepted (as a workaround, not a fix): M8a's `RemoteSyncer` classifies
`Push` failures by elimination — rule out unreachable (network-level) and
auth (`transport.Err*`, properly `%w`-wrapped) explicitly, then treat
anything left over as divergence. Sound for gage specifically because its
remotes are dedicated, hook-free repos with no other server-side push
rejection reason. See ["Push failure vs.
divergence"](m8a-sync-transport.md#decisions-to-make-first) for the full
three-way classification this produces.

**Revisit:** this is a go-git bug/gap, not a gage design constraint —
`Remote.Push`'s non-fast-forward path should wrap
`ErrNonFastForwardUpdate` the same way `Worktree.Pull`'s does. Worth
filing an upstream issue/PR against go-git once M8a ships; if it lands,
gage's elimination-based classifier can be simplified to a direct
`errors.Is` check. The classifier function gage ships in M8a must carry
a code comment citing this entry so the workaround isn't mistaken for
the intended long-term design.

### Entries carry no format version of their own

`format_version` lives on the vault config, not on each entry's YAML.
Adding an entry-level schema version later would be a breaking change to
existing ciphertext. Accepted on the grounds that the vault-level version
can gate an entry-schema migration — a vault at `format_version = 2` can
be defined to contain v2 entries. Revisit if entry schema churn turns out
to be more likely than expected.

### A SIGKILL leaves a copied secret on the clipboard

Per M12, `--clip`'s auto-clear runs inside the process that wrote the
clipboard — no forked clearer, because a process outliving the one that
unlocked is the daemon principle 5 refuses. The consequence is that a
`gage` killed outright (SIGKILL, a closed terminal, a crash) never
reaches its clear, and the secret stays on the clipboard until something
overwrites it.

Accepted as the correct side of the trade: the alternative is a surviving
background process holding a fingerprint of a secret gage no longer has
any key for, which is a worse property than a clipboard entry the user
can overwrite. It is a documented limit rather than a claimed guarantee,
the same posture as the Windows core-dump gap in "Session model". Ctrl-C
during the wait is a clean early clear, so the common interactive
interruption is handled.
