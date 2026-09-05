# M8a — Sync: transport & divergence detection

[← M7](m7-metadata-index.md) · [plan index](index.md) · next: [M8b — Sync: conflict resolution](m8b-sync-conflicts.md)

> **Recommended model: Opus.** go-git's fetch/push/merge surface is thin on documentation, and divergence classification is real problem-solving rather than transcription. The `.gitattributes` test is security-relevant.

## Goal

Auto fetch + fast-forward-only pull on every vault unlock, auto push after
writes, remote authentication, `gage clone`, and divergence **detection**.
Resolving a conflict is M8b's job; this milestone's contract is that
divergence is noticed, classified, and reported — never silently merged.
These commands stay vault-generic in the CLI
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

- **Push failure vs. divergence.** Resolved: `RemoteSyncer` classifies
  every `Fetch`/`Push` error into exactly three buckets before it ever
  reaches `Vault`'s sync logic, so neither `Vault` nor `cmd/gage` parses
  error text to tell them apart — a sentinel error per bucket (e.g.
  `ErrRemoteUnreachable`), returned identically by the real go-git-backed
  implementation and by the fake the offline test injects.
  - **Unreachable** — DNS failure, connection refused, timeout,
    `context.DeadlineExceeded`; detected via `net.Error`/`*net.OpError`/
    `*net.DNSError` in the error chain. This is never the human's fault,
    so it's warn-and-proceed, not a failure: one warning, the triggering
    operation (the write, the pull-on-unlock) still succeeds on its own
    terms, `exitcode.Success`, and the next successful sync retries with
    nothing to remember in between — same shape as `newIdentity`'s
    mlock-failure warn-and-continue in `unlock.go`.
  - **Auth** — `transport.ErrAuthenticationRequired`,
    `ErrAuthorizationFailed`, and `ErrRepositoryNotFound` (go-git's HTTP
    transport wraps all three with `%w`, so `errors.Is` is reliable
    here). Fold `ErrRepositoryNotFound` in here rather than treating it
    as a distinct "vanished repo" case: for a private remote, most hosts
    return the same "not found" for "doesn't exist" and "you can't see
    it," so at push/fetch time (unlike at `init`, where the repo just
    plain doesn't exist yet) it reads as an access problem. Reported with
    the Authentication section's host-naming message, `exitcode.LockedOrAuth`
    (already documented on that code as covering "an expired auth
    token").
  - **Diverged** — everything else, by elimination. Don't pattern-match
    go-git's non-fast-forward rejection string to detect this directly:
    `Remote.Push`'s real client-side fast-forward check
    (`checkFastForwardUpdate` in go-git's `remote.go`) returns a bare
    `fmt.Errorf("non-fast-forward update: %s", ...)` that does **not**
    wrap the exported `git.ErrNonFastForwardUpdate` sentinel — that
    sentinel is only ever returned by `Worktree.Pull`'s ff-only path, not
    by `Push`. Code written as `errors.Is(err, git.ErrNonFastForwardUpdate)`
    on a push result will silently never match a real divergence, and the
    raw string isn't a stable API to match on either. Classifying by
    elimination sidesteps both problems, and it's sound here specifically
    because gage's remotes are dedicated, hook-free, LFS-free repos — a
    personal vault's git remote has no server-side rejection reason other
    than divergence. Maps to `exitcode.Conflict` (already documented on
    that code as covering "a sync divergence"); the specific wording
    (entry-level conflict pointing at `gage sync` vs. the recipient/config
    "more severe" case) comes from the merge classification that follows
    fetch, not from this bucket by itself.

  Both leave the local commit intact regardless of bucket — divergence
  and unreachable differ in whether a human needs to act, not in whether
  data was lost.

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

- [x] `use` performs a fetch + fast-forward-only pull when the remote has
      commits the local vault lacks
- [x] A one-shot command (e.g. `gage show`) against a vault with unpulled
      remote commits performs the same fetch + fast-forward-only pull on
      its implicit unlock, with no session `use` step involved — the
      design ties this to every vault unlock, not to the `use` verb
      specifically
- [x] `use` with no network reachable warns once and proceeds with the
      local copy instead of blocking or failing
- [x] The fake `RemoteSyncer` injected in the offline test returns
      exactly the error `Vault`'s sync logic treats as "unreachable" —
      proving the warn-and-proceed path is reachable without depending on
      a real network failure
- [x] **A successful fast-forward pull invalidates or rebuilds M7's
      metadata index**: entries arriving via the pull appear in the very
      next `ls` in the same session, with no manual `reindex`
- [x] A pull that brings in nothing leaves the index intact (no
      gratuitous full rebuild on every unlock)

**Push on write**

- [x] A write command triggers a push; when nothing has diverged, it
      completes without user-visible friction
- [x] A simulated offline push leaves the local commit intact (nothing
      lost); the next successful `use`/push retries and succeeds
- [x] A push that fails because the remote diverged is reported
      differently from one that fails because the network is down

**Divergence**

- [x] Real divergence (local and remote both have commits the other
      lacks) is detected and reported, never auto-merged or silently
      resolved
- [x] `gittest`'s second-clone helper pushes an independent commit to a
      shared bare remote, producing real divergence when the vault under
      test also has an unpushed local commit
- [x] A divergence with no *entry-level* conflict (both sides touched
      different entries) merges cleanly and pushes, with no prompt and no
      unlock — git handles separate files on its own
- [x] A divergence that *does* conflict on an entry is detected, reported,
      and left unresolved with a pointer to `gage sync` — M8a never
      resolves, it only detects
- [x] **`.age-recipients` diverging on both sides produces a conflict
      rather than a silent union.** Two devices each adding a different
      recipient add two different lines, which git would merge cleanly
      without `.gitattributes` — and the merged list is one neither
      device wrote, with no unreviewed change for M10's trust cache to
      catch. This is the security-relevant test of the milestone
- [x] The same holds for `.gage/config.toml`
- [x] `gage init` writes the `.gitattributes` marking both files `-merge`
      (M1 wrote the file; this asserts it actually prevents the merge)

**Authentication**

- [x] `gage auth login` stores a user-supplied token at
      `$GAGE_STATE/tokens/<host>`, created `0600`, and `auth status`
      reports the host as configured
- [x] An expired or revoked token fails with a message naming the host
      and `gage auth login` — not a bare 403 or transport error
      (Q-OAUTH-APP: this is the one real cost of user-supplied tokens,
      so it's the one that gets a test)
- [x] `gage auth logout` removes it, and a subsequent push fails with a
      not-authenticated error naming `gage auth login` rather than an
      opaque transport error
- [x] A fetch/push against an HTTPS remote uses the stored token for that
      host; two vaults on the same host share one token
- [x] `--host` defaults to the host of the current vault's `origin`
- [x] A token file with permissions looser than `0600` is refused rather
      than used
- [x] An SSH-spelled remote that depends on a `~/.ssh/config` host alias
      fails with a message saying `~/.ssh/config` is not consulted and
      pointing at the HTTPS spelling — not a generic connection error
- [x] An SSH-spelled remote that ssh-agent can satisfy directly works,
      confirming best-effort SSH is genuinely best-effort and not absent
      — asserted as far as the no-network constraint allows: a
      fully-qualified SSH host is *not* refused as an alias and goes on
      to ask ssh-agent, rather than being silently unsupported. Whether
      an agent answers depends on the machine, so the test pins which
      path was taken, not the outcome
- [x] No token value appears in any error message, log line, or the
      session history file

**`clone`**

- [x] `gage clone` against a `gittest.NewBareRemote` produces a working
      vault — `.gage/config.toml`, `.age-recipients`, and `entries/`
      matching the remote's committed state — registered in global config
      the same way `init` registers a new one
- [x] `gage clone` infers the vault name from the remote URL, and
      `--name` overrides it
- [x] `gage clone` without `--dir` lands at `$GAGE_DATA/vaults/<name>`,
      the same default as `init`
- [x] `gage clone` against a vault where the local device isn't yet a
      recipient reports that plainly and points at `gage identity add`,
      rather than leaving a vault directory that silently can't decrypt
      anything
- [x] `gage clone` of a vault whose `format_version` is unrecognized
      refuses cleanly rather than registering an unusable vault

## Implementation

- [x] `RemoteSyncer` interface (`Fetch(ctx) error`/`Push(ctx) error`)
      between `Vault`'s sync logic and go-git — real go-git-backed
      implementation in production and in the realistic bare-repo tests,
      a fake in the one offline-handling test where a deterministic
      injected error matters more than a real network failure. Both
      implementations return one of the three classified sentinel errors
      from "Push failure vs. divergence" above (unreachable / auth /
      diverged) — `Vault` and `cmd/gage` never inspect a raw go-git or
      transport error directly
- [x] The real implementation's divergence-by-elimination branch (the
      code that decides "not unreachable, not auth, therefore diverged")
      carries a comment citing go-git's `Remote.Push` not wrapping
      `ErrNonFastForwardUpdate` the way `Worktree.Pull` does — see
      [open-questions.md](open-questions.md#go-gits-push-doesnt-wrap-errnonfastforwardupdate-so-m8a-classifies-divergence-by-elimination-instead)
      — so a future reader doesn't mistake the elimination logic for the
      intended design and "simplify" it back into a direct (and silently
      broken) `errors.Is(err, git.ErrNonFastForwardUpdate)` check. Worth
      an upstream go-git issue/PR once M8a ships
- [x] Remote authentication (Q-GIT-AUTH): HTTPS + token via go-git's
      `BasicAuth`, tokens stored per-host at `$GAGE_STATE/tokens/<host>`
      (`0600`) through M0's atomic-write helper; best-effort ssh-agent
      for SSH remotes, with a specific error when a remote depends on
      `~/.ssh/config`
- [x] `gage auth login/status/logout [--host HOST]`, registered in M0's
      command registry under the git group, available in both modes.
      `login` prompts for a token the user issued themselves and points
      at fine-grained, single-repository scoping; it never brokers one
      (Q-OAUTH-APP)
- [x] No host-specific code paths and no host-specific dependencies —
      no `go-github`, no device flow, no shipped client ID. `gage init
      --remote URL` against a repository that doesn't exist fails with a
      message saying to create it, rather than creating it for you
- [x] `gittest` package extended with a second-clone helper: clone a
      `NewBareRemote` repo into a second temp dir, commit there, push
      back — a throwaway stand-in for "another device," used to produce
      real divergence in tests
- [x] Auto fetch + fast-forward pull on every vault unlock — session
      `use` and a one-shot command's implicit unlock alike (go-git
      `Fetch`/`Pull`, ff-only), so the logic lives where both paths call
      through it rather than being wired into the `use` REPL command only
- [x] Index invalidation on successful pull, hooked where the pull
      happens rather than in each caller
- [x] Auto push after writes (go-git `Push`), under the same vault lock
      the write holds
- [x] Divergence detection, with distinct reporting from offline failure
      (the three-way `RemoteSyncer` classification above). Once a push
      is classified as diverged, fetch and classify what diverged —
      disjoint entries (merge and continue), conflicting entry (report,
      point at `gage sync`), recipient files (report as the more severe
      case) — since M8b's resolution consumes that classification
- [x] Manual `pull`/`push` via go-git (both already implemented purely in
      go-git — no passthrough, no `git` binary dependency, anywhere in
      `gage`)
- [x] `gage clone` (go-git `PlainClone`, reusing this milestone's
      bare-remote test harness); reads the cloned `.gage/config.toml` to
      learn the vault's default method — what a subsequent `identity add`
      will suggest for this device, not a constraint on it
      (Q-METHOD-SCOPE) — and reports plainly if the local device
      isn't yet a recipient

## Definition of done

Full test list green on all three CI platforms. A vault syncs between two
simulated devices; a divergence touching different entries merges and
pushes without a prompt; a divergence that genuinely conflicts is
detected, classified, and reported without being resolved; a recipient-file
divergence conflicts rather than silently unioning; and a clone of a vault
you can't yet read tells you so plainly.

## Affects later milestones

- **M8b consumes this milestone's divergence classification.** Detection
  decides *what kind* of conflict exists; resolution only has to present
  and apply a choice.
- M9's `--reencrypt` produces one large commit that then gets pushed;
  its atomicity guarantee is about HEAD, not about the push succeeding.
- M10's opportunistic trust-cache warning must also fire on `sync`
  specifically, because a clean fast-forward sync can complete without
  ever calling `Unlock` — so it can't ride the unlock hook alone.
