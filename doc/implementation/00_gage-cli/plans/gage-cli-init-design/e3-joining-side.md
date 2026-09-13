# E3 — Joining a vault

[← E2](e2-sealed-request.md) · [plan index](index.md) · next: [E4 — Approving a device](e4-approving-side.md)

> **Recommended model: Sonnet.** Wiring settled primitives to a command,
> plus a prompt on `clone`. The judgment calls — prompt versus flag,
> fail-early versus warn-and-proceed, the two identity paths — are all
> resolved in the TDD, so what is left is faithful execution against a
> written spec.

## Goal

`gage identity enroll`: create or reuse this device's identity, seal a
request to a freshly generated code, publish it, and print the code. Plus
the `clone` integration that offers it without a flag.

After this milestone a device can *ask* to join a vault. Nothing can
approve one yet — that is E4 — so the end state is a request sitting in
`pending/`, inert, and a code on screen.

## Depends on

- **E2** — the seal, the code, the filename scheme.
- **E0** — identity files live at the id-keyed path; the id is available
  from global config; and `[vaults.<name>].pubkey` exists, which the
  already-a-recipient check reads. Without that field the check silently
  degrades to the name comparison it was added to replace. Note that
  `clone` is **not** a writer of `pubkey` — it holds no identity to
  derive one from — so a freshly cloned vault legitimately lands in the
  "pubkey absent" branch until this milestone's `enroll` records one.
- **M8a** — fetch/push and the `RemoteSyncer` seam.
- **M2** — `CreateIdentity` and its create-or-reuse behavior.

## Design references

- ["The flows — joining device"](../../tdds/gage-cli-init-design.md) — the
  transcript this milestone renders
- ["Enrolling with an identity you already have"](../../tdds/gage-cli-init-design.md)
  — the second, differently-prompting path, and why it unlocks
- ["Which secret is which"](../../tdds/gage-cli-init-design.md) — the two
  secrets on one screen and how they are labeled
- [`D-ENROLL-REMOTE`](../../tdds/gage-cli-init-design.md) — ordering, the
  lock, and why an unreachable remote fails the command
- ["Why there is no `--enroll` flag"](../../tdds/gage-cli-init-design.md)
- [`Q-ENROLL-VERBS`](../gage-cli-design/open-questions.md#q-enroll-verbs)
  — the command name and its registry entry

## Decisions

Settled. Two worth holding in mind while implementing, because both are
easy to "simplify" back into bugs:

- **`identity enroll` is `identity add` plus publishing** — literally the
  same `CreateIdentity` call. Do not fork it.
- **The reuse path unlocks, and that is load-bearing.** It is what
  guarantees the published public key matches the private key this
  device holds. Do not optimize it away with a cached public key; the
  TDD explains the drift failure that makes a sidecar wrong here.

Three more were settled during review of this plan, after the milestone
docs were first written. They are in the TDD now, but they are the ones a
reader of an earlier draft would not expect:

- **Enroll must not use `Pull`/`Push`/`tryPull`.** They self-lock, and
  the lock is not re-entrant. See the implementation list; this is the
  single most likely way to lose a day on this milestone.
- **Clone's offer defaults to yes, and `Prompter` grows
  `ConfirmDefaultYes` for it.** An earlier draft claimed a `[Y/n]`
  transcript *and* an unchanged `Prompter`, which `Confirm`'s
  documented default-no rule makes impossible. This milestone owns the
  new method and its implementations.

  **What the non-interactive prompters do with it is part of the
  decision**, and an earlier draft left it unstated while naming only
  three implementations to write. The count is the smaller error: the
  script-mode prompters — `envPrompter`, `refusingPrompter`,
  `noCreatePrompter` (`cmd/gage/script.go`) and `assumeYesPrompter` —
  all *embed* `gage.Prompter`, so they inherit the new method silently
  rather than breaking the build. "Every implementation gains it as a
  compile-time break" is therefore true of `terminalPrompter` and the
  bare test fakes, and false of exactly the wrappers whose answer matters
  most.

  **Resolved: a default-yes question is never answered yes without a
  human.** The wrappers delegate, as they do for `Confirm`, and what they
  delegate *to* under `--stdin` or with no terminal is a prompter that
  cannot ask — so the offer is never reached. That is not an accident to
  rely on, though, so it is stated as a rule with a test under it: the
  default belongs to the *rendering* of the question, not to the absence
  of someone to answer it. A wrapper that returned `true` because the
  question's default is yes would turn "a script is never asked" into "a
  script always says yes", which is the one outcome "Why there is no
  `--enroll` flag" spends a section ruling out.

  `assumeYesPrompter` delegates for the same reason it delegates
  `Confirm`: `--yes` answers `ConfirmRecipientChange` and nothing else.
- **"Already a recipient" is a success, not a name collision.** It is
  checked by public key, before the name check, using E0's
  `[vaults.<name>].pubkey`. The old behavior told such a device to pass
  `--device`, which mints a second identity for no reason.

A second review pass settled three more, all in the TDD now:

- **`Enroll` runs under `withVaultWrite`, not `withWriteLock`** — it
  takes the ordinary dirty-tree reset every write takes, *before* the
  pull. This is not tidiness: a fast-forward refuses over a dirty tree,
  and a joining device has no other write to clear one with, so an
  interrupted enroll would wedge the device permanently. See the
  implementation list.
- **A diverged pull fails with `ErrEnrollmentDiverged`**, before anything
  is created. `v.pull` reports divergence with a **nil error** and
  `SyncReport.Diverged` set, so it has to be checked for rather than
  caught.
- **`Enroll` takes a `context.Context`**, matching every other exported
  method that blocks on the network and can fail because of it.

## Tests (write first)

**The two identity paths**

- [x] `identity enroll` on a device with **no** identity creates one via
      a `PurposeCreate` exchange (asked twice).
- [x] `identity enroll` on a device that **already holds** one reuses it,
      writes no second file, and prompts **once** with
      `Purpose: PurposeUnlock`. Assert the purpose and prompt count, not
      just the end state — the point is that these are two different
      exchanges the fake `Prompter` can tell apart.
- [x] A wrong passphrase on the reuse path surfaces `ErrWrongPassphrase`
      at `exitcode.LockedOrAuth`, writes nothing to `pending/`, and
      pushes nothing — a failure the create path cannot produce.
- [x] The public key sealed into the request equals the one derived from
      the identity file this device actually holds.
- [x] After a successful enroll, global config's `[vaults.<name>].pubkey`
      holds that same key — without it, E0's orphan check falls back to
      "keep and say why" on every enrolled device.

**Remote behavior (`D-ENROLL-REMOTE`)**

- [x] `identity enroll` **fetches before it commits** — against a bare
      remote that moved ahead, assert the resulting push is a
      fast-forward rather than a divergence.
- [x] An **unreachable remote fails before any identity file is written
      and before any prompt**: `ErrEnrollmentRemoteUnreachable`,
      passphrase prompt count zero, nothing under
      `$GAGE_DATA/identities/`, nothing committed. Injected fake
      `RemoteSyncer`, not a real timeout.
- [x] A vault with **no remote configured** fails with the *different*
      error `ErrEnrollmentNoRemote`; the two are distinguishable by the
      caller.
- [x] **The write lock is held across the pull, not taken after it** —
      assert a second process cannot move HEAD between the catch-up and
      the commit, in the style of M4's concurrency tests.
- [x] **An uncontended `identity enroll` never reports contention.** The
      plain-success test is the one that catches enroll calling a
      self-locking sync entry point: with no other process running, a
      `Pull` inside `Enroll`'s own lock fails as `*vaultlock.
      ContendedError` after the full timeout. Assert the success path
      completes well inside `vaultLockTimeout` so the bug surfaces as a
      failure rather than as a slow suite.
- [x] A **push rejected for lack of write access** leaves the identity
      and the local commit in place, and the error names both what
      happened and `gage auth login` — never a bare transport error.
- [x] Re-running after that failure reuses the existing identity, mints
      a fresh request and code, and succeeds once the push is allowed.
- [x] `identity enroll` warns, and proceeds, when local time is behind
      the vault's HEAD committer timestamp.
- [x] **A diverged pull fails with `ErrEnrollmentDiverged`** before any
      identity is written, anything is sealed, or anything is committed —
      and the message does **not** say "run `gage sync`", which this
      device cannot usefully do. Construct it the way it actually
      happens: enroll with a push that fails, move the bare remote ahead,
      enroll again.
- [x] **The nil-error trap is pinned.** `v.pull` reports divergence with
      a nil error and `SyncReport.Diverged` set, so assert the *refusal*
      rather than an error propagating — a test that only checks "some
      error came back" passes against an implementation that ignores the
      field entirely and fails later at the push.
- [x] Each remote error carries its exit code: `ErrEnrollmentNoRemote` →
      `Usage`, `ErrEnrollmentRemoteUnreachable` → `Unreachable`,
      `ErrEnrollmentDiverged` → `Conflict`.
- [x] **Enroll takes the dirty-tree reset before it pulls.** Leave an
      unrelated dirty file in the working tree, run `identity enroll`,
      and assert it succeeds with the ordinary discard warning — not a
      dirty-working-tree `Conflict` from the fast-forward. This is the
      bullet that fails if `Enroll` is wired to `withWriteLock` instead
      of `withVaultWrite`, and it fails for a reason that looks like a
      remote problem, which is why it is worth stating rather than
      trusting to inheritance.

**Flags**

- [x] `--ttl` is honored: the sealed `expires` and the filename epoch
      both reflect it, and the default with no flag is 24h.
- [x] `--ttl` beyond the 7-day ceiling, zero, or negative is a usage
      error at `exitcode.Usage` that publishes nothing and writes no
      identity — E2 proves the library's rejection, this proves the flag
      reaches it before anything happens.
- [x] `--use NAME` selects the vault, like every other vault-scoped
      command.

**Collisions**

- [x] `identity enroll` whose device name already labels a **different**
      recipient fails with `ErrDeviceNameTaken`, **writes no identity
      file**, and the message names `--device`.
- [x] **`identity enroll` on a device that is already a recipient is a
      success that does no work**: `Published: false`, no key generated,
      nothing sealed, nothing committed, nothing pushed, prompt count
      zero — and specifically **not** `ErrDeviceNameTaken`. Construct it
      the way it actually happens: `identity add`, then `recipient add`
      that key from another device, then `identity enroll`. Without this
      the tool advises `--device`, and following that advice mints a
      second identity and publishes a request for access this device
      already has.
- [x] **The check is by key, not by name**: a device whose key is a
      recipient under a *different* label — the state
      `approve --device` produces — also gets the no-work success, even
      though no name collides. This is the case a name comparison misses
      entirely.
- [x] **With no `pubkey` in global config**, the key check is skipped and
      the name check behaves exactly as it does today. Assert the
      fallback rather than assuming it, since it is the branch every
      pre-E0 vault entry lands in.
- [x] `identity enroll --device NAME` seals that name into the request
      and publishes successfully where the default hostname would have
      collided — the recovery the message above points at.
- [x] `--device` with a name outside `devicename.Valid`'s allowlist is a
      usage error that writes nothing, matching `AddIdentity`'s existing
      treatment rather than discovering it later.
- [x] `ErrDeviceNameTaken` carries `exitcode.Conflict` on this path.
      Pre-existing, reused rather than redefined — asserted so enroll's
      codes are all pinned in one place.

**Clone integration**

- [x] An interactive `clone` of a vault this device can't read offers to
      enroll; answering yes produces the same end state as `clone`
      followed by `identity enroll`.
- [x] **The offer defaults to yes**: a bare Enter enrolls, and the
      rendered prompt reads `[Y/n]`. This is the one `Prompter` question
      in `gage` that does, so pin both the default and the rendering —
      a `ConfirmDefaultYes` accidentally wired to `Confirm` still
      compiles, still passes a yes-answering test, and silently makes
      Enter mean no.
- [x] **`Confirm`'s default is unchanged everywhere else.** One test over
      an existing default-no question — `recipient remove` of this
      device's own key — asserting a bare Enter still declines, so the
      new method cannot have been added by changing the old one.
- [x] **No non-interactive prompter answers `ConfirmDefaultYes` with
      yes.** Cover the wrappers directly — `assumeYesPrompter` (so
      `--yes` cannot reach a question it was never meant to answer),
      `noCreatePrompter` and `refusingPrompter` — rather than only
      covering `clone`, since these embed `gage.Prompter` and therefore
      gain the method *without* a compile error to prompt anyone to think
      about it. The failure being ruled out is a wrapper that returns
      `true` because the question defaults to yes, which silently turns
      "a script is never asked" into "a script always agrees".
- [x] Answering `n` leaves the vault cloned, no identity written,
      nothing pushed — and a later `identity enroll` still works.
- [x] A **non-interactive** `clone` never prompts, never writes an
      identity, never pushes, and exits 0 — the behavior a scripted clone
      has today, with one deliberate difference: the message it prints is
      the reworded `accessLines`, which now names `identity enroll`
      alongside the manual path. An earlier draft of this bullet said
      "the message it prints today", which was wrong — today's names
      `identity add` only.
- [x] **The offer tracks where a `PurposeCreate` can be answered**, not a
      TTY test of its own: `--stdin` gets no offer, `--script FILE` at a
      terminal does. The failure this rules out is offering to enroll and
      then refusing the passphrase the offer requires, which is a
      question whose only outcome is an error.
- [x] A `clone` whose device already holds an identity for that vault is
      **not prompted at all**, and no unlock is attempted during the
      clone — passphrase prompt count zero.
- [x] **`clone --device NAME` followed by `y` publishes a request naming
      NAME**, not the hostname. `clone` resolves the device name already;
      the offer has to use the one it resolved rather than deriving a
      second one.
- [x] **The reworded `accessLines` names `identity enroll`** and still
      names the manual path. Assert the text on both the declined and the
      no-TTY routes — it is the message a user copies, and the E1a bullet
      about `identity add`'s printed next-command exists because this
      class of instruction goes stale silently.

**Session mode**

`identity enroll` is registered `AvailBoth`, and the session case has a
wrinkle one-shot does not: a joining device **cannot** `use` the vault.
`Session.Use` unlocks, and this device holds no key the vault accepts —
which is the entire reason it is enrolling.

- [x] **`identity enroll -u NAME` works in a session against a vault that
      was never unlocked**, routing through `Session.VaultWithoutUnlocking`
      rather than `Session.Vault`. Without this the command is registered
      for a mode it cannot actually run in.
- [x] **`use <vault>` on a vault this device cannot decrypt still fails**
      as it does today, and the failure names `identity enroll`. Enroll is
      not a way to unlock; it is what you run instead.

**The two secrets**

- [x] **They never cross.** The identity passphrase appears in no output
      stream and in nothing committed; the enrollment code appears in no
      identity file and in no committed plaintext. One test asserting
      both, since the whole risk is confusing them.
- [x] The passphrase prompt and the printed code each carry their
      disambiguating label ("stays on this device" / "safe to send"), so
      a reader of the transcript alone can tell them apart.

**Non-interactive**

Both outcomes are inherited from `scriptPrompter` rather than invented
here, but nothing currently pins either, and "the same command sometimes
works and sometimes refuses" is the kind of thing that gets "fixed" into
a bug later.

- [x] **The create path refuses under `--stdin`**: `exitcode.LockedOrAuth`,
      no identity written, nothing sealed, nothing committed, nothing
      pushed. `GAGE_PASSPHRASE` set makes no difference — assert that
      too, since it is the thing someone will reach for.
- [x] **The reuse path succeeds under `--stdin` with `GAGE_PASSPHRASE`**
      set: existing key reused, request sealed and pushed, code returned.
      This is the scripted retry-after-failed-push path.
- [x] **A refused scripted enroll still leaves the fast-forward in
      place** and the lock released — the pull from step 3 is not rolled
      back, and nothing about the failure needs undoing.
- [x] `--script FILE` at a terminal takes the create path normally, the
      `PurposeCreate` request passing through to the human.

**Crash-safety of a partial enroll**

Rewritten against what the code actually does: `resetDirtyWorkTree`
covers the **whole** working tree and deletes untracked files, not just
`entries/`. The TDD said otherwise and was wrong; see
[A21](../gage-cli-design/open-questions.md#a21). The tests below assert
the real behavior, which is stronger than what the doc had claimed.

- [x] A file written into `pending/` by an enroll that died before
      committing is **inert** — an entry written afterwards is not
      decryptable by the key it names.
- [x] **The next write on that machine discards the stray**, warning once
      and naming it, exactly as it would for any other unexpected dirt.
      Construct a stray, run an unrelated write, and assert both the
      warning and that the stray is gone and absent from the commit.
- [x] `recipient pending` on the machine that failed lists the stray
      while it survives — a read takes no lock and resets nothing — and
      on any other machine it does not exist.
- [x] No test asserts a stray surviving to expire on its own epoch. That
      was the old, incorrect story; a stray outlives only the interval
      before the next local write.
- [x] **A stray does not wedge the device.** Construct one, then run
      `identity enroll` again on that same machine and assert it
      succeeds. On a joining device this is the only write available, so
      this is the test that proves the reset is in enroll's own preamble
      rather than in some other path that will never run here.

**Two devices enrolling at once**

The TDD argues at length that `.gitattributes` deliberately does *not*
cover `pending/`, because a union of two devices' requests is the correct
merge outcome — unlike a union of two recipient lists. Nothing tested it.

- [x] **Two devices enroll against the same remote and both requests
      survive.** Ephemeral bare repo, two working copies, both push;
      assert both files are present after the merge and neither is
      truncated or conflicted. This is the whole justification for the
      `.gitattributes` omission, and it is currently an argument with no
      assertion under it.
- [x] **Neither merged request grants anything** — inertness holds across
      a merge, which is what makes the union safe rather than merely
      convenient.
- [x] **A divergence that involves `pending/` is reported honestly.**
      The union argument covers the *merge*; nothing covered what
      `SyncReport` says on the way there. `ConflictKind()` returns
      `ConflictEntries` for any conflicting path that is not one of the
      two recipient files (`internal/gage/remotesync.go`), and
      `resolveConflicts` then calls `entryIDFromPath` on every conflict
      and fails outright on anything that is not an entry
      (`internal/gage/syncresolve.go`) — so a conflict under `pending/`
      would be classified as an entry conflict and then refuse to
      resolve, leaving `gage sync` with no way through.

      Assert the reachable case rather than a contrived one: two devices
      that both resolve the same request (one approves, one denies)
      delete the same path, which git merges without a conflict. If that
      is genuinely the only way `pending/` can appear in a divergence,
      the test says so and pins it; if a conflicting path can be
      constructed, this milestone is where it stops being a surprise
      inside M8b's resolver.

**The request names the vault it is for**

- [x] **The published request carries this vault's `[vault].id`**, and
      opening it in that vault succeeds. E2 proves the refusal; this
      proves the field is actually populated from the vault being
      enrolled into rather than left zero — a request sealed with an
      empty `vault_id` fails E2's validation, so this is the bullet that
      catches enroll never setting it.

## Implementation

- [x] `Vault.Enroll(ctx, device, ttl, Prompter) (EnrollmentRequest,
      error)`, ordered per `D-ENROLL-REMOTE`: remote check → lock →
      **dirty-tree reset** → fetch+pull → **already-a-recipient check →
      name-collision check** → identity → seal/write/commit → push →
      release.

      The `ctx` is new since the first draft of this doc. Every other
      exported method that blocks on a remote and *fails* when it can't
      reach one takes one; the opportunistic pushes don't, because
      `pushAfterWrite` warns and proceeds and has nothing worth
      cancelling. Enroll's fetch is a hard precondition, so it takes one.

- [x] **Run the whole sequence under `withVaultWrite`, not the bare
      `withWriteLock`.** `withVaultWrite` is lock **plus**
      `resetDirtyWorkTree` (`internal/gage/vault.go`), and enroll needs
      the reset for a reason specific to the device running it:

      - `gitrepo.FastForward` returns `ErrDirtyWorkTree` over a dirty
        tree, mapped to `Conflict` — so a stray `pending/` file from an
        interrupted enroll makes the *next* enroll fail at the pull.
      - A joining device has no other write that would clear it. Every
        path that runs the reset needs an unlock this device cannot
        perform; it is not a recipient, which is the whole reason it is
        enrolling.

      Without the reset in enroll's own preamble, one interrupted enroll
      wedges the device permanently, recoverable only by hand-deleting a
      file under `.gage/` that nothing in the tool's output ever names.
      This is also what makes the crash-safety tests below true: on a
      joining device, "the next write on that machine" *is* the next
      enroll.

- [x] **Check `SyncReport.Diverged` after the pull** and fail with
      `ErrEnrollmentDiverged` before anything is created. `v.pull`
      returns a **nil error** in that case, so an implementation that
      only inspects the error walks straight past it, commits onto the
      diverged branch, and fails at the push with the standard "run `gage
      sync`" advice — which `D-ENROLL-REMOTE` establishes at length is
      exactly wrong for a device that cannot decrypt. The message says
      what a joining device can act on: the unpublished local commit is
      an inert `pending/` file, so discard it and enroll again.
- [x] **Reach the network through the unexported `v.pull` / `v.push`, not
      `Pull` / `Push` / `tryPull`.** Those three take the write lock
      themselves via `underWriteLock`, and `vaultlock` is not re-entrant —
      each `acquireVaultLock` opens a fresh descriptor, and flock and
      `LockFileEx` both conflict across descriptions inside one process.
      Calling them from inside `Enroll`'s lock blocks against itself for
      `vaultLockTimeout` and then fails as *contended*, naming this
      process's own lock. Read `D-ENROLL-REMOTE`'s "Which means enroll
      cannot reach the network through the existing public sync entry
      points" before writing this; the failure looks like an unrelated
      concurrency bug and the obvious fix for it is the one that
      paragraph rules out.

      **`pushAfterWrite` is the in-repo model to copy.** It is the
      automatic push after every write, it already runs inside the
      caller's lock, and it calls `v.push` for exactly this reason — its
      comment says so ("the unlocked form: this already runs inside the
      write's own lock"). Enroll's push is the same shape; its pull is
      the mirror image.
- [x] **The already-a-recipient check, by key and before the name
      check.** Compare `[vaults.<name>].pubkey` (E0) against the vault's
      recipient list. A match returns `EnrollmentRequest{Published:
      false}` — a success that generates no key, seals nothing, commits
      nothing, and pushes nothing. No match falls through to the existing
      name check. An absent `pubkey` skips the key check entirely and
      leaves today's `ErrDeviceNameTaken` behavior as-is. See
      D-ENROLL-COLLISIONS.
- [x] `Prompter.ConfirmDefaultYes(prompt string) (bool, error)` added to
      the interface, and implemented on the terminal prompter (rendering
      `[Y/n]`, bare Enter accepting) and on the bare test fakes. Clone's
      offer is its one caller; every other yes/no in `gage` stays on
      `Confirm`. Reasoning in the TDD's "Clone's offer needs a
      default-yes confirm".

      **The wrapper prompters need attention precisely because the
      compiler will not ask for it.** `assumeYesPrompter`,
      `envPrompter`, `refusingPrompter` and `noCreatePrompter` all embed
      `gage.Prompter` and so acquire the method by promotion the moment
      it is added. Delegation is the right answer for each — the same
      answer they give `Confirm` — but it has to be a decision that was
      made, not one that happened. Grep for `gage.Prompter` embedded as a
      field before calling this done; the count is larger than the three
      an earlier draft named.
- [x] `cmd/gage`'s `identity enroll`, registered with a `Short`, the
      `Identity` group, and both-mode availability, and carrying
      `--use`, `--device`, and `--ttl`.
- [x] **`identity add`'s and `identity list`'s existing `Short`s are
      reworded** per D-ENROLL-VERBS' table. `identity add`'s is what
      makes it and `enroll` identical up to their second clause — the
      listing's only signal that one is a superset of the other — so it
      lands with `enroll`, not later. (`recipient list`'s is E4's.)
- [x] **`cmd/gage`'s `identity enroll` handler** records this device's
      `pubkey` (and `device`/`method`) into global config's
      `[vaults.<name>]` — the same split `identity add` already uses, and
      not something `Vault.Enroll` does itself. `AddIdentity`'s own
      comment states the rule: "The local half — recording device/method
      in global config — stays in `cmd/gage`, exactly the split `gage
      init` already uses" (`internal/gage/identity_add.go`). An earlier
      draft of this bullet put the write in the library, which would have
      made enroll the one identity verb that reaches across that line.
      `EnrollmentRequest.Pubkey` is returned precisely so the handler has
      it. See E0's note on the third writer.

      Reading global config from the library is a different matter and is
      already precedented (`unlock.go` does it), which is what lets
      `Vault.Enroll`'s already-a-recipient check read `pubkey` without
      breaking the same rule.
- [x] Clone's post-success branch: `Prompter.ConfirmDefaultYes` when
      interactive and this device holds no identity; the reworded message
      otherwise. **Not `Confirm`** — that is the default-no method, and
      wiring the offer to it is the silent failure the `[Y/n]` test below
      exists to catch. The TTY test lives in `cmd/gage`, never in the
      library.
- [x] **Enroll under the device name `clone` already resolved.** `clone`
      carries a `--device` flag today (`cmd/gage/clone.go`), and the
      offer has to thread that name through rather than re-deriving it
      from the hostname — otherwise `clone --device X` followed by a
      `y` publishes a request naming something else.
- [x] **Reword `accessLines`** (`cmd/gage/clone.go`), which today says
      "run `gage identity add` here… then have someone who already has
      access add it with `gage recipient add`". That is still correct and
      still supported, but it is no longer the whole answer: `identity
      enroll` is the one that also publishes, and it is what someone who
      declined the offer or ran without a terminal should be pointed at.
      Keep the manual path in the message for the read-only-remote and
      air-gapped cases. The test bullets below assert this text, so it is
      a change with a definition of done rather than a judgment call.
- [x] Output labels for both secrets, per "Which secret is which".

## Definition of done

Every test above green on all three platforms. A device can publish a
request and print a code; nothing approves it yet.

**Plus one documentation change that has no other home:**
[A22](../gage-cli-design/open-questions.md#a22) folds `.gage/pending/`
into the *base* design doc's on-disk layout, and is written to apply
"when E3 lands" — this is that milestone. The layout diagram gains the
directory, along with the two properties A22 says belong in the base doc
rather than only here: it is created lazily and its absence means zero
requests, and it is inert. Left unowned this is exactly the stale-prose
failure E1a's `--reencrypt` grep exists to prevent, one document over.

## Affects later milestones

- **E4**'s tests construct pending requests by calling this milestone's
  `Enroll`, so its create-or-reuse and code-generation behavior is
  load-bearing for E4's fixtures as well as for users.
