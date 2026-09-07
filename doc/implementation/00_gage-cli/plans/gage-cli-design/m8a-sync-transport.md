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

- ["Sync model"](../../tdds/gage-cli-design.md) — the local-durability vs.
  network-risk split, and why divergence is kicked to a human
- ["Sync (vault-generic, git-implemented today)"](../../tdds/gage-cli-design.md)
  — the command surface
- ["Vault lifecycle"](../../tdds/gage-cli-design.md) — `clone`'s "does NOT
  grant you access" behavior

## Decisions to make first

- **Push failure vs. divergence.** Resolved: `RemoteSyncer` classifies
  every `Fetch`/`Push` error into exactly three buckets before it ever
  reaches `Vault`'s sync logic, so neither `Vault` nor `cmd/gage` parses
  error text to tell them apart — a sentinel error per bucket (e.g.
  `ErrRemoteUnreachable`), returned identically by the real go-git-backed
  implementation and by the fake the offline test injects.
  - **Unreachable** — DNS failure, connection refused, timeout,
    `context.DeadlineExceeded` *and* `context.Canceled`; detected via
    `net.Error`/`*net.OpError`/`*net.DNSError` in the error chain. This is
    never the human's fault, so it's warn-and-proceed, not a failure: one
    warning, the triggering operation (the write, the pull-on-unlock)
    still succeeds on its own terms, `exitcode.Success`, and the next
    successful sync retries with nothing to remember in between — same
    shape as `newIdentity`'s mlock-failure warn-and-continue in
    `unlock.go`.

    An *explicitly invoked* sync verb does surface it, and it gets its own
    exit code — **`exitcode.Unreachable` (7), added by this milestone**,
    not `Conflict`. The whole point of the classification is that "retry
    this unchanged when the network is back" and "a human has to reconcile
    two histories" are different situations; sharing exit code 1 would
    have made that difference visible only to someone reading the message,
    not to a script. `Conflict`'s own definition — "a state a human must
    resolve" — doesn't describe a network outage either.

    `context.Canceled` is in the list for the same reason the bucket
    exists: classification is by elimination, so leaving it out would have
    turned a cancelled sync into "origin has diverged".
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

There is a second, narrower seam for the same reason. A local bare repo
needs no credentials, so nothing in the harness above can observe whether
resolved auth actually reaches go-git — the tests pass identically with
the `Auth:` field deleted. `gittest.RecordingTransport` registers a
`gagetest://` protocol with go-git's client registry that records the
`AuthMethod` and endpoint it was handed and then fails, so a test can
assert on the credentials of a fetch or push that never opens a socket.
It is also how the auth-*failure* messages are tested, since a real 401
needs a server.

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
      differently from one that fails because the network is down — in
      wording *and* in exit code (`Conflict` vs. `Unreachable`), so the
      distinction survives being read by something other than a human

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
      — asserted as *content*, deliberately. gage never invokes git's
      merge machinery, so `.gitattributes` is not what enforces this
      property: `gitrepo.MergeRemote` is gage's own **file-level** merge,
      and a file both sides changed is a conflict there whatever any
      attribute says. The committed file still matters — it binds the real
      `git` a human may run inside the vault — but the security guarantee
      is proven by the recipient-file test above, not by this one

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
      opaque transport error — the push half asserted through the
      recording transport, since a real refusal needs a server
- [x] A fetch/push against an HTTPS remote uses the stored token for that
      host; two vaults on the same host share one token. Asserted at the
      transport boundary, not just at `remoteauth.Method`: every realistic
      sync test syncs against a local path where auth is legitimately
      `nil`, so that harness structurally cannot notice credentials never
      reaching go-git — deleting `Auth:` from `FetchOptions`/`PushOptions`
      left the whole suite green. `gittest.RecordingTransport` closes it
      by registering a `gagetest://` scheme that records what go-git was
      handed and fails instead of connecting
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
- [x] `gage init --remote` against an HTTP(S) host with no token stored
      solicits one at the same prompt `gage auth login` uses, before
      attempting the publish, and stores what it's given — asserted
      through the recording transport, checking both the prompt text and
      that the token presented to the push is the one just typed
- [x] Leaving that prompt blank does not store anything and falls back to
      the pre-existing anonymous push, rather than being treated as a
      usage error (unlike `gage auth login`'s own blank-token refusal —
      `init` doesn't know whether this remote needs a token at all)
- [x] `gage init --remote` against a host with a token already stored
      does not prompt a second time
- [x] `gage init --remote` against a local path or an SSH remote never
      prompts for a token — neither authenticates with one

**Local state a sync has to respect**

- [x] A working tree with uncommitted changes refuses both a
      fast-forward *and* a merge, with the same `ErrDirtyWorkTree`
      sentinel and the offending paths named. gage commits its own writes
      immediately, so uncommitted changes mean something outside gage is
      mid-edit: a fast-forward's hard reset would discard that work and a
      merge's staging would publish it to every other device. `Vault`
      reports either as `exitcode.Conflict` — a state a human resolves,
      not an internal fault
- [x] A merge that fails partway through applying itself rolls the
      working tree back, so a failed merge never leaves a tree that is
      neither the old state nor the new one (which, now that every sync
      operation refuses over a dirty tree, would wedge the vault)
- [x] "N local commit(s) pending" is the truth once a merge commit exists
      between the local head and the remote-tracking ref — the count stops
      at everything the remote can reach, not at the merge-base commit
      alone. Stopping at the single hash let the walk descend through a
      merge's other parent into the shared history behind the base, and
      report a vault's *entire* commit count as pending
- [x] A merge whose follow-up push then fails still reports what is
      pending (the local write plus the merge commit), rather than the
      zero that would say the work is published

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
- [x] `gage init --remote` solicits a token inline instead of requiring a
      separate `gage auth login` round trip: `remoteauth.TokenHost`
      (additive alongside `Host`/`Method`) tells `cmd/gage` whether a
      remote is a token candidate at all — "" for a local path or an SSH
      remote, the same protocol dispatch `Method` already makes — and
      `init`'s new `ensureRemoteToken` uses it to check `remoteauth.Load`
      before publishing, prompting with `Prompter.Value` (the same call
      `auth login` makes) only when the host has no token yet. A blank
      answer is not an error here, unlike in `auth login` itself — it
      falls back to the pre-existing anonymous push, since `init` has no
      way to know in advance whether the remote actually needs one
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
- [x] The *automatic* sync on unlock takes the write lock only if it is
      free right now, and skips silently if it isn't. It needs the lock at
      all because a fast-forward resets the working tree — but nobody
      asked for it, and before this milestone a read took no lock at all.
      Waiting would put `gage show` behind an unrelated `gage insert` for
      the full `vaultLockTimeout` and then warn, in exchange for nothing:
      whoever holds the lock is mid-write and will push when it finishes.
      Manual `sync`/`pull`/`push` still wait — there the human asked
- [x] `RemoteOpTimeout` (30s) bounds every network operation, automatic or
      manual, so neither an unlock nor a terminal hangs on a blackholed
      host. Exported so a frontend driving `Pull`/`Push`/`Sync` bounds them
      the same way instead of inventing its own number
- [x] Divergence detection, with distinct reporting from offline failure
      (the three-way `RemoteSyncer` classification above). Once a push
      is classified as diverged, fetch and classify what diverged —
      disjoint entries (merge and continue), conflicting entry (report,
      point at `gage sync`), recipient files (report as the more severe
      case) — since M8b's resolution consumes that classification
- [x] gage's own file-level three-way merge (`gitrepo.MergeRemote`), since
      go-git has none. File-level is not a shortcut: entries are age
      ciphertext, opaque blobs with no line structure, so a text merge of
      two edits to one entry could only produce a secret neither device
      wrote. It is also what makes the recipient-file guarantee
      independent of `.gitattributes` — see the test note above
- [x] Both operations that move the working tree refuse over uncommitted
      changes, sharing one `gitrepo.ErrDirtyWorkTree` sentinel so `Vault`
      can report either as `exitcode.Conflict` with one `errors.Is`. A
      partially applied merge rolls itself back, which is safe precisely
      because the tree was verified clean before the first write —
      everything discarded is the merge's own work
- [x] Manual `pull`/`push` via go-git (both already implemented purely in
      go-git — no passthrough, no `git` binary dependency, anywhere in
      `gage`)
- [x] Sync verbs resolve their vault *without unlocking it*
      (`vaultForSync` / `Session.VaultWithoutUnlocking`) — the one command
      family that deliberately skips `withUnlockedVault`. A fast-forward,
      and a merge whose two sides touched different entries, decrypt
      nothing, so prompting for a passphrase up front would ask for a key
      most syncs never use
- [x] `gage clone` picks the branch to clone explicitly rather than
      following the remote's HEAD. A vault's remote routinely has a HEAD
      pointing at a branch that doesn't exist: modern `git init --bare`
      sets HEAD to `refs/heads/main` before any commit, while go-git — what
      gage commits with — creates `refs/heads/master`. One extra
      round trip on a once-per-vault operation beats a bare "reference not
      found"
- [x] `gage clone` (go-git `PlainClone`, reusing this milestone's
      bare-remote test harness); reads the cloned `.gage/config.toml` to
      learn the vault's default method — what a subsequent `identity add`
      will suggest for this device, not a constraint on it
      (Q-METHOD-SCOPE) — and reports plainly if the local device
      isn't yet a recipient

## Corrections made during review

Recorded because each was a checkbox that was ticked before the behaviour
underneath it was right, and the shape of the mistake is worth keeping:

- **`AheadCount` reported a vault's whole history as pending.** Walking
  back from the local head and stopping at the merge-*base commit* is only
  equivalent to "commits the remote lacks" while no merge sits in between.
  Once one does, the walk descends into the merge's other parent and
  re-enters the shared history behind the base without ever passing
  through it. Reproduced at 7 where 2 was truthful; on a real vault it
  would have been the entire commit count.
- **Nothing tested that a stored token reaches go-git.** Every sync test
  syncs against a local path, where `nil` auth is correct — so the whole
  suite stayed green with `Auth:` deleted from both option structs. Two
  plan checkboxes were ticked against tests that only exercised
  `remoteauth.Method` in isolation.
- **Offline and diverged shared exit code 1**, which is the one
  distinction this milestone exists to draw.
- **`MergeRemote` swept a human's uncommitted edits into an automatic
  merge commit and pushed them**, while `FastForward` refused over the
  same state.

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
- **M9's `--reencrypt` holds the write lock for a long time.** The
  automatic sync on unlock skips a contended lock rather than waiting, so
  reads stay responsive while it runs; that is a property to keep, not an
  accident to tidy away.
- **`exitcode.Unreachable` (7) is new here.** Anything later that
  enumerates exit codes — docs, shell completions, a man page — has one
  more to list.
