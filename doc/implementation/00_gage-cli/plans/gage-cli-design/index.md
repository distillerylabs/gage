# Implementation plan

Sequential milestones for building out
[gage-cli-design.md](../../tdds/gage-cli-design.md). Each milestone should
leave the tool in a runnable, testable state — no milestone should require
reworking a prior one, though later milestones may extend earlier
abstractions.

This file is the shared ground every milestone stands on: the dependency
graph, the locked-in library choices, the layering and testing rules, and
the contracts milestones publish to each other. **It is not a summary of
the phase docs** — the per-milestone detail lives in its own file, and
this one holds only what more than one milestone needs to agree on.

Open decisions, skipped issues, and pending design-doc amendments are
tracked in [open-questions.md](open-questions.md). That register is the
single place a deferred problem is allowed to live; nothing gets dropped
by being "handled later" without an entry there.

Status legend: `[ ]` not started, `[~]` in progress, `[x]` done.

**All twelve milestones were built and shipped** — M0 through M12 are
implemented, and `make lint`/`make test` pass on the Linux/macOS/Windows
matrix. Two things qualify that, and both are recorded below rather than
left to be discovered: behavior that shipped *after* the plan was
finished is under "Post-plan changes," and three milestones (M1, M2, M9)
have since had their contracts amended, so they carry work that is
decided but not yet built — see "Accepted, not yet implemented." The aim
is for this file to describe the tool as it actually is, including where
it currently disagrees with the design doc.

---

## Milestones

| # | Milestone | Status | Model | Theme |
|---|---|---|---|---|
| M0 | [Scaffolding](m0-scaffolding.md) | `[x]` | Sonnet ⚑ | Toolchain, layout, CI, primitives — no crypto, no vaults |
| M1 | [Vault lifecycle & config](m1-vault-lifecycle.md) | `[x]`† | Sonnet | On-disk vault structure and the vault registry — still no crypto |
| M2 | [Identity & memory protection](m2-identity-and-crypto.md) | `[x]`† | **Opus** ⚑ | age primitives, identity generation, `Unlock`/`Close`, page-locking |
| M3 | [Entry format](m3-entry-format.md) | `[x]` | Sonnet | Entry YAML + per-entry encrypt/decrypt round-trip |
| M4 | [CRUD (one-shot)](m4-crud.md) | `[x]` | Sonnet | `insert`/`cat`/`rm`/`ls`, commit-per-write |
| M5 | [Query resolution](m5-query-resolution.md) | `[x]` | Sonnet | title-then-UUID, exact/substring/ambiguous; `show`/`edit`/`rename`/`generate` |
| M6 | [Session mode](m6-session-mode.md) | `[x]` | Sonnet* | `Session` type, REPL, multi-vault, idle timeout |
| M7 | [Metadata index](m7-metadata-index.md) | `[x]` | Sonnet | Decrypt-once cache, `search`/`grep`, `reindex` |
| M8a | [Sync: transport & detection](m8a-sync-transport.md) | `[x]` | **Opus** | Auth, auto fetch/pull/push, `clone`, divergence detection |
| M8b | [Sync: conflict resolution](m8b-sync-conflicts.md) | `[x]` | **Opus** | `gage sync` — keep local/remote/both, merge commit |
| M9 | [Identity & recipient management](m9-recipients.md) | `[x]`† | **Opus** | Multi-device, `recipient add/remove`, atomic `--reencrypt` |
| M10 | [Local trust cache](m10-trust-cache.md) | `[x]` | Sonnet* | `known-config.toml`, recipient-change detection |
| M11 | [Cross-vault sharing](m11-cross-vault-sharing.md) | `[x]` | Sonnet | `mv`/`cp --to-vault` |
| M12 | [Polish / output modes](m12-polish.md) | `[x]` | Sonnet | `--clip`, `--qr`, `--field`, `log`, `history`, `--script` |

**† These milestones shipped complete, then had their contracts
amended.** [A19](open-questions.md#a19) makes re-encryption
unconditional on `gage recipient add` (M9) — applied to the design doc,
not yet in the code; see "Accepted, not yet implemented" below.
[A20](open-questions.md#a20) keys the identities directory by a vault id
rather than by the local vault name (M1's config schema and
`format_version`, M2's identity paths) — **implemented** in
[E0](../gage-cli-init-design/e0-vault-id-keying.md), together with
[Q-ORPHAN-BY-NAME](open-questions.md#q-orphan-by-name), so those two
milestones' amended contracts are now what the code does.

**Model column.** `⚑` marks a milestone worth an Opus review pass over
its *tests* before moving on, even where Sonnet wrote them. `*` marks a
borderline call — start on Sonnet, switch if it turns awkward. Each phase
doc carries the same recommendation with its reasoning, so you don't have
to come back here.

The split works because most of this plan is now specification rather
than design: the decisions are made and written down, which is exactly
what lets a cheaper model execute faithfully. Opus is reserved for the
four milestones where the work is genuine problem-solving (go-git's thin
merge surface) or where being subtly wrong is unrecoverable (crypto,
crash-safety).

**Switch at milestone boundaries, not within them.** The plan's test
convention is task-level TDD — write a task's tests, implement it, move
on — so there's no clean tests-then-implementation seam to switch models
at mid-milestone. Pick one model per milestone and let it work.

**The failure mode to watch** isn't wrong code, it's quiet scope
narrowing: 18 of 24 test bullets implemented and the milestone reported
done. Check the list off against the doc rather than against the summary,
whichever model wrote it.

### Dependency graph

```
M0 ─┬─> M1 ──> M2 ──> M3 ──> M4 ─┬─> M5 ──> M6 ──> M7 ──> M8a ──> M8b
    │                            │                        │
    │                            └────────────────────────┴──> M9 ──> M10 ──> M11
    │                                                                          │
    └──────────────────────────────────────────────────────────────────────────┴──> M12
```

M12's items are mutually independent and individually deferrable; the rest
of the chain is genuinely sequential.

**Why M1 has no crypto and M2 does.** The original draft of this plan had
M1 generate the first device's identity, and simultaneously claimed M2
would prove crypto correctness "in isolation before anything else depends
on it." That was contradictory: wrapping an identity file *is* an age
operation, and M1's own unlock test needed M2's encrypt/decrypt to exist.
The boundary now sits where the dependency actually falls — M1 builds the
vault's on-disk structure and registry with recipient public keys handed
to it as plain strings (writing an `age1...` string to a file needs no
crypto), and M2 is the first milestone where any key material exists.
The practical consequence is that M1's `gage init` requires
`--recipient PUBKEY`, and M2 is what makes that flag optional by
generating a local identity to supply the key itself.

---

## Layering rules

Every milestone builds its logic into the `internal/gage` library first
and wires `cmd/gage` (Cobra) on top as a thin frontend — see the design
doc's ["Library architecture"](../../tdds/gage-cli-design.md). This is what
keeps a future GUI/TUI addable later without reworking any milestone
here: it's a second frontend over the same methods, not a refactor of
them.

Concretely, and enforced by an M0 lint check:

- `internal/gage` never references `os.Stdin`/`os.Stdout`, never calls
  `fmt.Print*` or `os.Exit`, outside `_test.go` files.
- Anything needing a human decision returns a **typed value** — a
  candidate list, a recipient-change warning, an `UnlockRequest` — and
  `cmd/gage` decides how to render it. Never printed text, never a
  direct stdin read.
- `Identity` is a parameter, never internal state. `Vault.Unlock` is the
  only thing that produces one; every CRUD method takes it explicitly.
  This is what lets M6's `Session` cache an identity across calls
  without changing a single M4/M5 method signature.
- The in-session metadata index lives on `Session`, never on `Vault`.
  `Vault` stays a stateless, identity-agnostic operator over ciphertext
  in both invocation modes.

---

## Locked-in dependencies

Decided up front so later milestones don't reshuffle. "First used" names
the milestone that introduces it.

| Library | Purpose | First used |
|---|---|---|
| [spf13/cobra](https://github.com/spf13/cobra) | Command structure/dispatch, both invocation modes | M0 |
| [pelletier/go-toml](https://github.com/pelletier/go-toml) v2 | All TOML read/write — global and per-vault config. TOML 1.0 compliant, struct-tag driven, no CGo | M0 |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) (`unix`, `windows`) | The `memlock` and `vaultlock` packages — `mlock`/`flock` vs `VirtualLock`/`LockFileEx` behind one signature | M0 (`vaultlock`), M2 (`memlock`) |
| [go-git/go-git](https://github.com/go-git/go-git) | All git operations — native Go, no shelling out | M1 (`init`), M8a (sync) |
| [golang.org/x/term](https://pkg.go.dev/golang.org/x/term) | Masked terminal input, cross-platform | M2 (passphrase prompt) |
| [creack/pty](https://github.com/creack/pty) | **Test-only.** Drives the real `x/term` prompter through a pseudoterminal in the handful of tests that need it | M2 |
| [FiloSottile/age](https://github.com/FiloSottile/age) | All encryption/decryption; the format `.age-recipients` interop depends on | M2 |
| [google/uuid](https://github.com/google/uuid) | Entry filenames (`entries/<uuid>.age`), RFC 4122 UUIDv4 | M3 |
| [atotto/clipboard](https://github.com/atotto/clipboard) | `-c`/`--clip` | M12 |
| [rsc.io/qr](https://pkg.go.dev/rsc.io/qr) | `-q`/`--qr` — QR *encoding* only; the half-block terminal art is gage's own (`cmd/gage/qrcode.go`) | M12 |

**The one runtime binary dependency** is `atotto/clipboard` on
macOS/Linux, which shells out to `pbcopy`/`xclip`/`xsel` (it uses native
syscalls on Windows). The `gage` binary itself still compiles as a single
static executable. Worth flagging as the exception, since the git side
deliberately has none — see the design doc's "Git-specific commands" for
why there's no `git`-binary passthrough anywhere in `gage`.

**Git remote authentication is HTTPS with a user-supplied token** —
go-git reads none of `~/.ssh/config`, credential helpers, or
`insteadOf`, and every one of those limitations is specific to SSH
transport rather than to any particular host. Tokens live at
`$GAGE_STATE/tokens/<host>` (`0600`) and are managed by `gage auth
login/status/logout`. SSH remotes are best-effort via ssh-agent and fail
with a clear message when they depend on a host alias. `gage init
--remote` solicits a token inline, at the same prompt, when the remote's
host needs one and none is stored yet — so a fresh private HTTPS remote
doesn't need a separate `gage auth login` before its first push succeeds.

**There are no host-specific code paths and no host-specific
dependencies.** No OAuth device flow, no shipped client ID, no
`go-github` — `gage` never brokers a credential, because a brokered
OAuth token would be *broader* than the fine-grained, single-repository
PAT a user can issue themselves. See
[open-questions.md](open-questions.md#q-git-auth) and Q-OAUTH-APP.

---

## Test conventions

**Workflow per milestone:** the milestone's test list is the definition of
done, not a nice-to-have added afterward. Work through it task by task,
writing each task's tests before its implementation — but don't try to
author the whole milestone's suite upfront against APIs that don't exist
yet. The list is the acceptance checklist; task-level TDD is how you
walk it.

**A milestone is not done until its full test list is green**, on all
three platforms where the list doesn't say otherwise.

**Driving the CLI in tests — hybrid.** Most CLI tests call
`rootCmd.Execute()` in-process with injected IO and a fake `Prompter`:
fast, deterministic, and identical on Windows CI. A small number of tests
— the masked passphrase read and `insert -m`'s multiline-until-EOF
capture — drive the real `x/term`-backed prompter through a
`creack/pty` pseudoterminal, so those paths don't ship untested. Any test
that can be written in-process should be.

**No network.** `make test` runs with no network access and no
pre-existing keys. Remote git behavior is tested against ephemeral local
bare repos via the `gittest` helper package (M0, extended in M8a), which
exercises go-git's real code paths without a socket. The one exception is
"network unreachable," which is tested by injecting a fake `RemoteSyncer`
(M8a) rather than depending on a real timeout.

**Injectable seams.** Where a failure mode can't be produced honestly in
a test, the library calls through a narrow interface and the test injects
a fake: `Locker` for page-lock failure (M2), `RemoteSyncer` for network
unreachability (M8a), `Prompter` for every human decision. The real
implementation is used in production *and in every realistic test*; fakes
are for the specific failure being proven, not a general substitute.

**Cross-platform CI from day one.** The matrix runs `make build`/`make
test`/`make lint` natively on Linux, macOS, and Windows — actual
execution, not cross-compilation. Several milestones make claims that
only hold if the platform's own syscalls run: M0's `vaultlock`, M2's
`memlock` and core-dump suppression, M6's session/idle-timeout parity.

---

## Cross-milestone contracts

Things one milestone publishes that a later one silently depends on. This
list exists because splitting the plan into per-phase files makes exactly
this class of coupling easy to lose.

| Established in | Contract | Consumed by |
|---|---|---|
| M0 | `Prompter` interface; unlock exchange is typed and method-agnostic (`UnlockRequest`/`UnlockResponse` carrying a `Kind`) | M2 (passphrase), every interactive decision after |
| M0 | `Identity` type + `Vault.Unlock(Prompter) (Identity, error)` signature, decided before any real crypto exists to shape it | M2 implements it; M4/M5 take `Identity` as a parameter; M6 caches it |
| M0 | `vaultlock` advisory lock primitive | M4 onward: held across every read-modify-commit |
| M0 | Atomic TOML write (temp + rename) | Global config, `.gage/config.toml`, `known-config.toml` (M10) |
| M0 | Exit-code taxonomy | Every command; M9's `verify` 0/1 contract |
| M0 | Command registry in `cmd/gage` — name, aliases, description, group, and one-shot/session/both availability | `gage --help`/`gage help` render the one-shot set; M6's in-session `help` renders the session set. **Every milestone that adds a command must register it**, enforced by M0's registry-completeness test rather than by convention |
| M0 | `gittest.NewBareRemote` | M1's `set-remote` tests; M8a's full sync harness |
| M1 | `.gage/config.toml` schema incl. `format_version`, and rejection of unknown versions | Every later reader of that file |
| M1 | Global config schema, incl. the per-vault `device` and `method` fields | M2 populates both; M4's `updated_by` reads `device`; `Unlock` dispatches on `method` |
| M1 | `.gitattributes` marking `.age-recipients`/`config.toml` `-merge` | M8a — without it, two devices each adding a recipient merge into a union neither wrote, invisibly to M10's trust cache |
| M1 | Device-name normalization **and validation** — names reach the filesystem as path components and arrive from a committed file any git-writer can edit | M2 (identity file paths), M9 (`identity add`) |
| M2 | `Identity.Close()` releases the page lock and zeroes key material | M4's one-shot handler; M6's `Lock` and idle timeout |
| M2 | Thin age encrypt-to-recipients / decrypt-with-identity wrapper (bytes in, bytes out) | M3's entry layer; M9's `--reencrypt`; M11's cross-vault encrypt |
| M4 | Commit-per-write, under the vault lock | M8a's auto-push; M9's single-commit `--reencrypt` |
| M5 | Resolver returns a resolved entry *or* a candidate list, as a value | M6 prompts with it; one-shot fails with it |
| M6 | `Session` owns all "how long does this stay unlocked" bookkeeping | M7's index lives there too |
| M7 | Session-scoped metadata index | **M8a must invalidate/rebuild it after a successful pull**; M8b after each resolution; M11 must update both vaults' indexes |
| M9 | Recipient list changes touch `.age-recipients` and `config.toml` together, in one commit | M10 diffs exactly that pair |
| M10 | `RecipientChangeWarning` typed value + cache regeneration rules | M11 runs the check against the *destination* vault |

---

## Post-plan changes (shipped after M12)

Work that landed after the milestone plan was finished. It's recorded
here because it changes behavior the milestone docs specify, and would
otherwise live only in git history. Each item's tests sit with the
milestone whose surface it touches rather than in a suite of their own.

- **Context-relevant help on a usage rejection** (#27). A Cobra-native
  parsing rejection — wrong argument count, unknown flag, unrecognized
  (sub)command — now carries that command's own usage block alongside
  the bare complaint, in one-shot mode and inside a session alike, while
  a command's own coded error is left exactly as it built it. Folded
  into **M0**'s test list (`cmd/gage/usageerror_test.go`) rather than
  tracked separately, since it's the help/registry surface M0 owns.
- **`gage init --remote` solicits a token inline** (#38). Specified in
  **M8a** all along; the implementation simply landed after the rest of
  that milestone.
- **`gage vault remove` offers to clean up an orphaned identity file**
  (#39, as revised by E0). This *extends* M1's "drops registration,
  leaves the underlying files untouched" bullet, and the narrowness is
  the whole point: deletion is offered only when the vault's recipient
  list is readable **and** no longer contains this device's *public
  key*, and it never happens without a `Confirm` that names the path.
  If the key is still listed (under any label), or the list can't be
  read at all (moved store, remote-only, filesystem trouble), or global
  config records no public key for this device, `gage` keeps the file
  and says why — guessing wrong would permanently strand access to that
  vault's ciphertext, and `vault remove` never touches the store.
  `Confirm` defaults to no, so a non-interactive run keeps the file by
  construction.
  **What E0 changed:** the test used to compare device *names*, which
  was wrong in both directions (Q-ORPHAN-BY-NAME) — it would delete an
  active recipient's key whenever the vault labelled it differently, and
  keep a stale key whenever another device took its old label. E0 also
  removed most of this cleanup's original motivation: keying identities
  by vault id makes the silent-reuse case it guarded against
  structurally impossible, which is why the remaining deletion asks
  rather than assumes.
- **`make reset-local-state`** (`scripts/resetlocalstate`) — a
  development helper that deletes this machine's
  `$GAGE_CONFIG`/`$GAGE_DATA`/`$GAGE_STATE` so the fresh-install path can
  be exercised by hand. It prints the paths it resolved and requires an
  interactive `yes` before deleting anything. Deliberately a separate
  `main` under `scripts/` rather than a `gage` subcommand: destroying a
  user's vaults and identities has no place in the CLI's own command
  surface, and keeping it out means it can't be reached by accident.

---

## Accepted, not yet implemented

Decided and written into the design doc, but the code does not do it
yet. This section exists so that gap is visible rather than inferred
from a mismatch between the TDD and the binary — and it should be empty
most of the time.

**One item remains.** A19 is `E1a` in
[plans/gage-cli-init-design/](../gage-cli-init-design/index.md), which
also covers the device-enrollment feature that raised it. The summary
here is the standing record; the milestone doc carries the test list.

A20 was the other item and is **done** — `E0`, landed with
Q-ORPHAN-BY-NAME. Its former entry is kept below as a record of what
changed, the way the amendment register keeps applied entries.

### A19 — `gage recipient add` always re-encrypts

Full reasoning in [open-questions.md](open-questions.md#a19) and in the
design doc's "Why adding a recipient always re-encrypts". Short version:
a recipient who can read entries written after their admission but not
before is the finer-grained access tier principle 1 rules out, it's
undiagnosable from the interface, and — because `reencryptTo` fails on
the first entry the acting identity can't read — it's contagious and
unrepairable. A partially-admitted device can't fix itself or grant full
access to anyone else.

Work remaining, all in M9's surface:

- `Vault.AddRecipient` drops its `reencrypt bool` parameter and always
  re-encrypts. `RemoveRecipient` is untouched — its flag stays mandatory
  and means something different.
- A pre-flight check that the acting identity can decrypt every entry,
  failing with a new `ErrCannotGrantFullAccess` **before** the vault lock
  and **before** the trust-cache confirmation, naming the count of
  unreadable entries.
- `cmd/gage`'s `recipient add` loses `--reencrypt`; passing it becomes a
  usage error rather than being silently accepted.
- M9's three A19 test bullets go green, and the existing tests pinning
  the old behavior (`add` without re-encryption leaving entries
  unreadable) are replaced rather than deleted — the replacement asserts
  no invocation of `add` can produce a partial recipient.

Device enrollment ([gage-cli-init-design.md](../../tdds/gage-cli-init-design.md))
already specifies `recipient approve` this way, so implementing A19 first
keeps the two doors consistent rather than letting `add` remain the
vector that creates partial recipients.

### A20 — the identities directory is keyed by a vault id — **done (E0)**

Full reasoning in [open-questions.md](open-questions.md#q-identity-vault-name).
Short version: identity files are keyed by the *local* vault name, which
is chosen at clone time and tied to the remote by nothing — so two
vaults registered under one name in sequence share a directory. Two
failures follow, and the second is unrecoverable: a vault silently
reusing another's keypair, and `vault remove` deleting a private key
belonging to a vault it is not removing.

What landed, spanning M1 and M2:

- `[vault].id` (UUIDv4) minted by `init` and written to
  `.gage/config.toml`; `format_version` to 2, with a v1 vault refused by
  the check that already exists.
- The identity path becomes `$GAGE_DATA/identities/<vault-id>/<device>.age`,
  with a plaintext marker in the directory naming the vault so a
  hand-managed backup is still findable.
- The id copied into global config's `[vaults.<name>]`, so `vault remove`
  can identify a vault's identities directory without reading the vault.
- The id validated as untrusted input before any path is built from it,
  the same rule device names already get (Q-DEVICE-NAME).
- The trust cache moved to `$GAGE_STATE/<vault-id>/known-config.toml` —
  the same keying flaw, far milder (it fails safe, producing a spurious
  recipient-change warning rather than suppressing a real one), fixed
  here because the id exists anyway.
- `pubkey` added to `[vaults.<name>]`, and `removeOrphanedIdentity`
  changed to compare public keys rather than device names **and to
  `Confirm` before deleting, defaulting to keep**
  ([Q-ORPHAN-BY-NAME](open-questions.md#q-orphan-by-name)). Today that
  function decides whether to delete the only copy of a private key by
  comparing labels, which is wrong in both directions; the false-delete
  direction is unrecoverable, and `recipient approve --device` makes it
  newly reachable.
- **The vault lock too**, which this summary did not originally name:
  `$GAGE_STATE/locks/<vault-id>.lock`. E0 makes "one repository
  registered twice under two local names" a supported state, and a
  name-keyed lock would have given those two registrations one lock file
  each — so both could write the same working tree at once. After E0
  nothing under `$GAGE_DATA` or `$GAGE_STATE` is addressed by a vault's
  local name.

**Migration is "re-create the vault,"** which was only tolerable because
there is no installed base; `make reset-local-state` exists for exactly
this. A v1 vault is refused by the `format_version` check with a
directional message — older than this build says re-create, newer still
says upgrade gage.

---

## Deferred / not on the critical path

- **Additional identity methods** (ssh, yubikey, secure-enclave, plugin)
  — passphrase alone proves the `identity`/`recipient` abstraction; add
  real methods once that interface is stable, otherwise you're debugging
  plugin behavior and architecture at the same time.
- **Additional vault types** — `git` alone proves the vault/backing-store
  split; a second type is only worth adding once the vault-generic vs.
  type-specific command boundary has been exercised by real
  implementation, not just designed on paper. (Note that a
  GitHub-API-backed type is one of the live options under
  [open-questions.md](open-questions.md#q-git-auth).)
- **Signed recipient changes** — design doc calls this out as "reserve
  for high-stakes repos," not a default.
- **`gage clone` against a real, external remote** — the command itself
  is implemented and tested against a `gittest` bare remote in M8a; an
  actual GitHub/self-hosted URL is only really exercisable manually.
- **A GUI and/or TUI frontend.** The library/CLI split is *not* deferred
  — it's built in from M0 — but actually writing a second frontend is.
  Once the library's interactive-decision interface (`Prompter`,
  structured warnings/candidate lists) is proven by the CLI through M10,
  a GUI/TUI is a new consumer of existing methods, not new core logic.
- **Identity file backup/export tooling.** The recovery story is
  multi-recipient (see M9's lost-identity-file test), not device-key
  backup — `gage` doesn't sync or back up `$GAGE_DATA/identities/`
  itself. A user can copy a device's wrapped identity file manually (it's
  already passphrase-protected ciphertext), but that stays a manual,
  user-owned choice outside `gage`.
- **Release engineering and distribution** — reproducible builds,
  checksums, signing, package manager manifests, install docs. Not
  scheduled; tracked in [open-questions.md](open-questions.md#q-release)
  because for a secrets tool "how do I know the binary I downloaded is
  the one you built" deserves an explicit answer rather than silent
  omission.
