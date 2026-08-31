# Implementation plan

Sequential milestones for building out
[gage-cli-design.md](../tdds/gage-cli-design.md). Each milestone should
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

---

## Milestones

| # | Milestone | Status | Theme |
|---|---|---|---|
| M0 | [Scaffolding](m0-scaffolding.md) | `[ ]` | Toolchain, layout, CI, primitives — no crypto, no vaults |
| M1 | [Vault lifecycle & config](m1-vault-lifecycle.md) | `[ ]` | On-disk vault structure and the vault registry — still no crypto |
| M2 | [Identity & memory protection](m2-identity-and-crypto.md) | `[ ]` | age primitives, identity generation, `Unlock`/`Close`, page-locking |
| M3 | [Entry format](m3-entry-format.md) | `[ ]` | Entry YAML + per-entry encrypt/decrypt round-trip |
| M4 | [CRUD (one-shot)](m4-crud.md) | `[ ]` | `insert`/`cat`/`rm`/`ls`, commit-per-write |
| M5 | [Query resolution](m5-query-resolution.md) | `[ ]` | prefix/exact/substring/ambiguous; `show`/`edit`/`rename`/`generate` |
| M6 | [Session mode](m6-session-mode.md) | `[ ]` | `Session` type, REPL, multi-vault, idle timeout |
| M7 | [Metadata index](m7-metadata-index.md) | `[ ]` | Decrypt-once cache, `search`/`grep`, `reindex` |
| M8 | [Sync (+ `clone`)](m8-sync.md) | `[ ]` | Auto fetch/pull/push, divergence, `gage sync`, `gage clone` |
| M9 | [Identity & recipient management](m9-recipients.md) | `[ ]` | Multi-device, `recipient add/remove`, atomic `--reencrypt` |
| M10 | [Local trust cache](m10-trust-cache.md) | `[ ]` | `known-config.toml`, recipient-change detection |
| M11 | [Cross-vault sharing](m11-cross-vault-sharing.md) | `[ ]` | `mv`/`cp --to-vault` |
| M12 | [Polish / output modes](m12-polish.md) | `[ ]` | `--clip`, `--qr`, `--field`, `log`, `history`, `--script` |

### Dependency graph

```
M0 ─┬─> M1 ──> M2 ──> M3 ──> M4 ─┬─> M5 ──> M6 ──> M7 ──> M8
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
doc's ["Library architecture"](../tdds/gage-cli-design.md). This is what
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
| [go-git/go-git](https://github.com/go-git/go-git) | All git operations — native Go, no shelling out | M1 (`init`), M8 (sync) |
| [golang.org/x/term](https://pkg.go.dev/golang.org/x/term) | Masked terminal input, cross-platform | M2 (passphrase prompt) |
| [creack/pty](https://github.com/creack/pty) | **Test-only.** Drives the real `x/term` prompter through a pseudoterminal in the handful of tests that need it | M2 |
| [FiloSottile/age](https://github.com/FiloSottile/age) | All encryption/decryption; the format `.age-recipients` interop depends on | M2 |
| [google/uuid](https://github.com/google/uuid) | Entry filenames (`entries/<uuid>.age`), RFC 4122 UUIDv4 | M3 |
| [atotto/clipboard](https://github.com/atotto/clipboard) | `-c`/`--clip` | M12 |
| [mdp/qrterminal](https://github.com/mdp/qrterminal) | `-q`/`--qr`, terminal block art | M12 |

**The one runtime binary dependency** is `atotto/clipboard` on
macOS/Linux, which shells out to `pbcopy`/`xclip`/`xsel` (it uses native
syscalls on Windows). The `gage` binary itself still compiles as a single
static executable. Worth flagging as the exception, since the git side
deliberately has none — see the design doc's "Git-specific commands" for
why there's no `git`-binary passthrough anywhere in `gage`.

**Not yet locked: git remote authentication.** go-git does not read
`~/.ssh/config`, git credential helpers, or `insteadOf` rewrites. This is
unresolved and tracked in [open-questions.md](open-questions.md#q-git-auth);
it could still change the choice of backing-store library before M8.
Nothing before M8 depends on the answer.

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
bare repos via the `gittest` helper package (M0, extended in M8), which
exercises go-git's real code paths without a socket. The one exception is
"network unreachable," which is tested by injecting a fake `RemoteSyncer`
(M8) rather than depending on a real timeout.

**Injectable seams.** Where a failure mode can't be produced honestly in
a test, the library calls through a narrow interface and the test injects
a fake: `Locker` for page-lock failure (M2), `RemoteSyncer` for network
unreachability (M8), `Prompter` for every human decision. The real
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
| M0 | `gittest.NewBareRemote` | M1's `set-remote` tests; M8's full sync harness |
| M1 | `.gage/config.toml` schema incl. `format_version`, and rejection of unknown versions | Every later reader of that file |
| M1 | Global config schema, incl. the per-vault `device` and `method` fields | M2 populates both; M4's `updated_by` reads `device`; `Unlock` dispatches on `method` |
| M1 | Device-name normalization **and validation** — names reach the filesystem as path components and arrive from a committed file any git-writer can edit | M2 (identity file paths), M9 (`identity add`) |
| M2 | `Identity.Close()` releases the page lock and zeroes key material | M4's one-shot handler; M6's `Lock` and idle timeout |
| M2 | Thin age encrypt-to-recipients / decrypt-with-identity wrapper (bytes in, bytes out) | M3's entry layer; M9's `--reencrypt`; M11's cross-vault encrypt |
| M4 | Commit-per-write, under the vault lock | M8's auto-push; M9's single-commit `--reencrypt` |
| M5 | Resolver returns a resolved entry *or* a candidate list, as a value | M6 prompts with it; one-shot fails with it |
| M6 | `Session` owns all "how long does this stay unlocked" bookkeeping | M7's index lives there too |
| M7 | Session-scoped metadata index | **M8 must invalidate/rebuild it after a successful pull**; M11 must update both vaults' indexes |
| M9 | Recipient list changes touch `.age-recipients` and `config.toml` together, in one commit | M10 diffs exactly that pair |
| M10 | `RecipientChangeWarning` typed value + cache regeneration rules | M11 runs the check against the *destination* vault |

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
  is implemented and tested against a `gittest` bare remote in M8; an
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
