# M8 — Sync (+ `clone`)

[← M7](m7-metadata-index.md) · [plan index](index.md) · next: [M9 — Identity & recipient management](m9-recipients.md)

## Goal

Auto fetch + fast-forward-only pull on every vault unlock, auto push after
writes, divergence detection, and `gage sync` for the one case that never
auto-resolves. These commands stay vault-generic in the CLI
(`sync`/`pull`/`push`), even though they're entirely git-implemented
today. (`gage git set-remote` — the one git-*specific* command — was
already implemented back in M1; there's no generic `gage git --
<args...>` passthrough.)

`gage clone` lands here too, not with M11's cross-vault sharing — it has
nothing to do with sharing between two local vaults. It's the last
generic sync-family verb (`init`/`push`/`pull`/`sync`/`clone` all wrap
go-git operations against a remote), and it needs exactly the bare-remote
test harness this milestone already builds for divergence testing.

## Depends on

- **M1** — `git set-remote` and the `[vaults.<name>.git]` config table.
- **M4** — commit-per-write, already true.
- **M6** — `Vault.Unlock` as the hook point both invocation modes share.
- **M7** — the index this milestone has to invalidate.

## Design references

- ["Sync model"](../tdds/gage-cli-design.md) — the local-durability vs.
  network-risk split, and why divergence is kicked to a human
- ["Sync (vault-generic, git-implemented today)"](../tdds/gage-cli-design.md)
  — the command surface
- ["Vault lifecycle"](../tdds/gage-cli-design.md) — `clone`'s "does NOT
  grant you access" behavior

## Decisions to make first

- **[Q-GIT-AUTH](open-questions.md) — blocks this milestone.** go-git
  doesn't read `~/.ssh/config`, credential helpers, or `insteadOf`. Four
  options are on the table including a GitHub-API-backed store; this must
  be resolved before real remote work starts, and it could still change
  the backing-store library choice.
- **[Q-SYNC-CONFLICT](open-questions.md) — blocks this milestone.**
  "Surfaces both versions and requires an explicit choice" is the entire
  specification today. What the choices are, what history results, which
  paths unlock, and whether same-title-different-UUID counts as a
  conflict are all unanswered. This is the second-riskiest piece in the
  build after crypto and likely wants its own design-doc subsection.
- **Whether this milestone splits.** If Q-SYNC-CONFLICT resolves into
  something substantial, split into M8a (fetch/pull/push/clone happy
  path, divergence *detection*) and M8b (conflict *resolution*) — the
  same isolation logic that separates crypto and the trust cache into
  their own milestones.
- **Push failure vs. divergence.** A failed push because the remote
  diverged is a different user-facing situation from a failed push
  because the network is down. Both leave the local commit intact;
  they need different messages and probably different exit codes.

## Test harness

Real fetch/push/pull/divergence behavior is tested against ephemeral local
bare repos, extending M0's `gittest.NewBareRemote` with a second-clone
helper that simulates another device pushing independent commits — no
sockets, no real network, satisfying the "no network access" test
constraint while still exercising go-git's actual code paths.

The one exception is "network unreachable": `Vault`'s sync methods call
through a small `RemoteSyncer` interface (`Fetch`/`Push` against go-git in
production) so that one test can inject a fake returning a deterministic
unreachable-style error, rather than depending on a real timeout or a
missing-path error that wouldn't exercise the same code path gage's real
offline-handling logic runs.

## Tests (write first)

**Pull on unlock**

- [ ] `use` performs a fetch + fast-forward-only pull when the remote has
      commits the local vault lacks
- [ ] A one-shot command (e.g. `gage show`) against a vault with unpulled
      remote commits performs the same fetch + fast-forward-only pull on
      its implicit unlock, with no session `use` step involved — the
      design ties this to every vault unlock, not to the `use` verb
      specifically
- [ ] `use` with no network reachable warns once and proceeds with the
      local copy instead of blocking or failing
- [ ] The fake `RemoteSyncer` injected in the offline test returns
      exactly the error `Vault`'s sync logic treats as "unreachable" —
      proving the warn-and-proceed path is reachable without depending on
      a real network failure
- [ ] **A successful fast-forward pull invalidates or rebuilds M7's
      metadata index**: entries arriving via the pull appear in the very
      next `ls` in the same session, with no manual `reindex`
- [ ] A pull that brings in nothing leaves the index intact (no
      gratuitous full rebuild on every unlock)

**Push on write**

- [ ] A write command triggers a push; when nothing has diverged, it
      completes without user-visible friction
- [ ] A simulated offline push leaves the local commit intact (nothing
      lost); the next successful `use`/push retries and succeeds
- [ ] A push that fails because the remote diverged is reported
      differently from one that fails because the network is down

**Divergence**

- [ ] Real divergence (local and remote both have commits the other
      lacks) is detected and reported, never auto-merged or silently
      resolved
- [ ] `gittest`'s second-clone helper pushes an independent commit to a
      shared bare remote, producing real divergence when the vault under
      test also has an unpushed local commit
- [ ] `gage sync` surfaces both versions of a conflicting entry and
      requires an explicit choice before proceeding
- [ ] Conflict resolution behaves per Q-SYNC-CONFLICT (tests written once
      that resolves — placeholder until then)
- [ ] A divergence with no *entry-level* conflict (both sides touched
      different entries) resolves without prompting

**`clone`**

- [ ] `gage clone` against a `gittest.NewBareRemote` produces a working
      vault — `.gage/config.toml`, `.age-recipients`, and `entries/`
      matching the remote's committed state — registered in global config
      the same way `init` registers a new one
- [ ] `gage clone` infers the vault name from the remote URL, and
      `--name` overrides it
- [ ] `gage clone` without `--dir` lands at `$GAGE_DATA/vaults/<name>`,
      the same default as `init`
- [ ] `gage clone` against a vault where the local device isn't yet a
      recipient reports that plainly and points at `gage identity add`,
      rather than leaving a vault directory that silently can't decrypt
      anything
- [ ] `gage clone` of a vault whose `format_version` is unrecognized
      refuses cleanly rather than registering an unusable vault

## Implementation

- [ ] `RemoteSyncer` interface (`Fetch(ctx) error`/`Push(ctx) error`)
      between `Vault`'s sync logic and go-git — real go-git-backed
      implementation in production and in the realistic bare-repo tests,
      a fake in the one offline-handling test where a deterministic
      injected error matters more than a real network failure
- [ ] Remote authentication, per Q-GIT-AUTH
- [ ] `gittest` package extended with a second-clone helper: clone a
      `NewBareRemote` repo into a second temp dir, commit there, push
      back — a throwaway stand-in for "another device," used to produce
      real divergence in tests
- [ ] Auto fetch + fast-forward pull on every vault unlock — session
      `use` and a one-shot command's implicit unlock alike (go-git
      `Fetch`/`Pull`, ff-only), so the logic lives where both paths call
      through it rather than being wired into the `use` REPL command only
- [ ] Index invalidation on successful pull, hooked where the pull
      happens rather than in each caller
- [ ] Auto push after writes (go-git `Push`), under the same vault lock
      the write holds
- [ ] Divergence detection, with distinct reporting from offline failure
- [ ] `gage sync` (conflict surfacing, both versions shown), per
      Q-SYNC-CONFLICT
- [ ] Manual `pull`/`push` via go-git (both already implemented purely in
      go-git — no passthrough, no `git` binary dependency, anywhere in
      `gage`)
- [ ] `gage clone` (go-git `PlainClone`, reusing this milestone's
      bare-remote test harness); reads the cloned `.gage/config.toml` to
      learn the vault's default method — what a subsequent `identity add`
      will suggest for this device, not a constraint on it
      (Q-METHOD-SCOPE) — and reports plainly if the local device
      isn't yet a recipient

## Definition of done

Full test list green on all three CI platforms. A vault syncs between two
simulated devices, divergence is detected and never silently resolved,
and a clone of a vault you can't yet read tells you so plainly.

## Affects later milestones

- M9's `--reencrypt` produces one large commit that then gets pushed;
  its atomicity guarantee is about HEAD, not about the push succeeding.
- M10's opportunistic trust-cache warning must also fire on `sync`
  specifically, because a clean fast-forward sync can complete without
  ever calling `Unlock` — so it can't ride the unlock hook alone.
