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
  degrades to the name comparison it was added to replace.
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
- **"Already a recipient" is a success, not a name collision.** It is
  checked by public key, before the name check, using E0's
  `[vaults.<name>].pubkey`. The old behavior told such a device to pass
  `--device`, which mints a second identity for no reason.

## Tests (write first)

**The two identity paths**

- [ ] `identity enroll` on a device with **no** identity creates one via
      a `PurposeCreate` exchange (asked twice).
- [ ] `identity enroll` on a device that **already holds** one reuses it,
      writes no second file, and prompts **once** with
      `Purpose: PurposeUnlock`. Assert the purpose and prompt count, not
      just the end state — the point is that these are two different
      exchanges the fake `Prompter` can tell apart.
- [ ] A wrong passphrase on the reuse path surfaces `ErrWrongPassphrase`
      at `exitcode.LockedOrAuth`, writes nothing to `pending/`, and
      pushes nothing — a failure the create path cannot produce.
- [ ] The public key sealed into the request equals the one derived from
      the identity file this device actually holds.
- [ ] After a successful enroll, global config's `[vaults.<name>].pubkey`
      holds that same key — without it, E0's orphan check falls back to
      "keep and say why" on every enrolled device.

**Remote behavior (`D-ENROLL-REMOTE`)**

- [ ] `identity enroll` **fetches before it commits** — against a bare
      remote that moved ahead, assert the resulting push is a
      fast-forward rather than a divergence.
- [ ] An **unreachable remote fails before any identity file is written
      and before any prompt**: `ErrEnrollmentRemoteUnreachable`,
      passphrase prompt count zero, nothing under
      `$GAGE_DATA/identities/`, nothing committed. Injected fake
      `RemoteSyncer`, not a real timeout.
- [ ] A vault with **no remote configured** fails with the *different*
      error `ErrEnrollmentNoRemote`; the two are distinguishable by the
      caller.
- [ ] **The write lock is held across the pull, not taken after it** —
      assert a second process cannot move HEAD between the catch-up and
      the commit, in the style of M4's concurrency tests.
- [ ] **An uncontended `identity enroll` never reports contention.** The
      plain-success test is the one that catches enroll calling a
      self-locking sync entry point: with no other process running, a
      `Pull` inside `Enroll`'s own lock fails as `*vaultlock.
      ContendedError` after the full timeout. Assert the success path
      completes well inside `vaultLockTimeout` so the bug surfaces as a
      failure rather than as a slow suite.
- [ ] A **push rejected for lack of write access** leaves the identity
      and the local commit in place, and the error names both what
      happened and `gage auth login` — never a bare transport error.
- [ ] Re-running after that failure reuses the existing identity, mints
      a fresh request and code, and succeeds once the push is allowed.
- [ ] `identity enroll` warns, and proceeds, when local time is behind
      the vault's HEAD committer timestamp.
- [ ] Each remote error carries its exit code: `ErrEnrollmentNoRemote` →
      `Usage`, `ErrEnrollmentRemoteUnreachable` → `Unreachable`.

**Flags**

- [ ] `--ttl` is honored: the sealed `expires` and the filename epoch
      both reflect it, and the default with no flag is 24h.
- [ ] `--ttl` beyond the 7-day ceiling, zero, or negative is a usage
      error at `exitcode.Usage` that publishes nothing and writes no
      identity — E2 proves the library's rejection, this proves the flag
      reaches it before anything happens.
- [ ] `--use NAME` selects the vault, like every other vault-scoped
      command.

**Collisions**

- [ ] `identity enroll` whose device name already labels a **different**
      recipient fails with `ErrDeviceNameTaken`, **writes no identity
      file**, and the message names `--device`.
- [ ] **`identity enroll` on a device that is already a recipient is a
      success that does no work**: `Published: false`, no key generated,
      nothing sealed, nothing committed, nothing pushed, prompt count
      zero — and specifically **not** `ErrDeviceNameTaken`. Construct it
      the way it actually happens: `identity add`, then `recipient add`
      that key from another device, then `identity enroll`. Without this
      the tool advises `--device`, and following that advice mints a
      second identity and publishes a request for access this device
      already has.
- [ ] **The check is by key, not by name**: a device whose key is a
      recipient under a *different* label — the state
      `approve --device` produces — also gets the no-work success, even
      though no name collides. This is the case a name comparison misses
      entirely.
- [ ] **With no `pubkey` in global config**, the key check is skipped and
      the name check behaves exactly as it does today. Assert the
      fallback rather than assuming it, since it is the branch every
      pre-E0 vault entry lands in.
- [ ] `identity enroll --device NAME` seals that name into the request
      and publishes successfully where the default hostname would have
      collided — the recovery the message above points at.

**Clone integration**

- [ ] An interactive `clone` of a vault this device can't read offers to
      enroll; answering yes produces the same end state as `clone`
      followed by `identity enroll`.
- [ ] **The offer defaults to yes**: a bare Enter enrolls, and the
      rendered prompt reads `[Y/n]`. This is the one `Prompter` question
      in `gage` that does, so pin both the default and the rendering —
      a `ConfirmDefaultYes` accidentally wired to `Confirm` still
      compiles, still passes a yes-answering test, and silently makes
      Enter mean no.
- [ ] **`Confirm`'s default is unchanged everywhere else.** One test over
      an existing default-no question — `recipient remove` of this
      device's own key — asserting a bare Enter still declines, so the
      new method cannot have been added by changing the old one.
- [ ] Answering `n` leaves the vault cloned, no identity written,
      nothing pushed — and a later `identity enroll` still works.
- [ ] A **non-interactive** `clone` never prompts, never writes an
      identity, never pushes, exits 0, and prints the run-`identity
      enroll` message — the behavior a scripted clone has today.
- [ ] **The offer tracks where a `PurposeCreate` can be answered**, not a
      TTY test of its own: `--stdin` gets no offer, `--script FILE` at a
      terminal does. The failure this rules out is offering to enroll and
      then refusing the passphrase the offer requires, which is a
      question whose only outcome is an error.
- [ ] A `clone` whose device already holds an identity for that vault is
      **not prompted at all**, and no unlock is attempted during the
      clone — passphrase prompt count zero.

**The two secrets**

- [ ] **They never cross.** The identity passphrase appears in no output
      stream and in nothing committed; the enrollment code appears in no
      identity file and in no committed plaintext. One test asserting
      both, since the whole risk is confusing them.
- [ ] The passphrase prompt and the printed code each carry their
      disambiguating label ("stays on this device" / "safe to send"), so
      a reader of the transcript alone can tell them apart.

**Non-interactive**

Both outcomes are inherited from `scriptPrompter` rather than invented
here, but nothing currently pins either, and "the same command sometimes
works and sometimes refuses" is the kind of thing that gets "fixed" into
a bug later.

- [ ] **The create path refuses under `--stdin`**: `exitcode.LockedOrAuth`,
      no identity written, nothing sealed, nothing committed, nothing
      pushed. `GAGE_PASSPHRASE` set makes no difference — assert that
      too, since it is the thing someone will reach for.
- [ ] **The reuse path succeeds under `--stdin` with `GAGE_PASSPHRASE`**
      set: existing key reused, request sealed and pushed, code returned.
      This is the scripted retry-after-failed-push path.
- [ ] **A refused scripted enroll still leaves the fast-forward in
      place** and the lock released — the pull from step 3 is not rolled
      back, and nothing about the failure needs undoing.
- [ ] `--script FILE` at a terminal takes the create path normally, the
      `PurposeCreate` request passing through to the human.

**Crash-safety of a partial enroll**

Rewritten against what the code actually does: `resetDirtyWorkTree`
covers the **whole** working tree and deletes untracked files, not just
`entries/`. The TDD said otherwise and was wrong; see
[A21](../gage-cli-design/open-questions.md#a21). The tests below assert
the real behavior, which is stronger than what the doc had claimed.

- [ ] A file written into `pending/` by an enroll that died before
      committing is **inert** — an entry written afterwards is not
      decryptable by the key it names.
- [ ] **The next write on that machine discards the stray**, warning once
      and naming it, exactly as it would for any other unexpected dirt.
      Construct a stray, run an unrelated write, and assert both the
      warning and that the stray is gone and absent from the commit.
- [ ] `recipient pending` on the machine that failed lists the stray
      while it survives — a read takes no lock and resets nothing — and
      on any other machine it does not exist.
- [ ] No test asserts a stray surviving to expire on its own epoch. That
      was the old, incorrect story; a stray outlives only the interval
      before the next local write.

## Implementation

- [ ] `Vault.Enroll(device, ttl, Prompter) (EnrollmentRequest, error)`,
      ordered per `D-ENROLL-REMOTE`: remote check → lock → fetch+pull →
      **already-a-recipient check → name-collision check** → identity →
      seal/write/commit → push → release.
- [ ] **Reach the network through the unexported `v.pull` / `v.push`, not
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
- [ ] **The already-a-recipient check, by key and before the name
      check.** Compare `[vaults.<name>].pubkey` (E0) against the vault's
      recipient list. A match returns `EnrollmentRequest{Published:
      false}` — a success that generates no key, seals nothing, commits
      nothing, and pushes nothing. No match falls through to the existing
      name check. An absent `pubkey` skips the key check entirely and
      leaves today's `ErrDeviceNameTaken` behavior as-is. See
      D-ENROLL-COLLISIONS.
- [ ] `Prompter.ConfirmDefaultYes(prompt string) (bool, error)` added to
      the interface, and implemented on the terminal prompter (rendering
      `[Y/n]`, bare Enter accepting), the test fake, and
      `assumeYesPrompter` (delegating, exactly as it delegates `Confirm`
      — `--yes` still answers only `ConfirmRecipientChange`). Clone's
      offer is its one caller; every other yes/no in `gage` stays on
      `Confirm`. Reasoning in the TDD's "Clone's offer needs a
      default-yes confirm".
- [ ] `cmd/gage`'s `identity enroll`, registered with a `Short`, the
      `Identity` group, and both-mode availability, and carrying
      `--use`, `--device`, and `--ttl`.
- [ ] **`identity add`'s and `identity list`'s existing `Short`s are
      reworded** per D-ENROLL-VERBS' table. `identity add`'s is what
      makes it and `enroll` identical up to their second clause — the
      listing's only signal that one is a superset of the other — so it
      lands with `enroll`, not later. (`recipient list`'s is E4's.)
- [ ] Enroll records this device's `pubkey` (and `device`/`method`) into
      global config's `[vaults.<name>]`, the same as `identity add` —
      see E0's note on the fourth writer.
- [ ] Clone's post-success branch: `Prompter.Confirm` when interactive
      and this device holds no identity; today's message otherwise. The
      TTY test lives in `cmd/gage`, never in the library.
- [ ] Output labels for both secrets, per "Which secret is which".

## Definition of done

Every test above green on all three platforms. A device can publish a
request and print a code; nothing approves it yet.

## Affects later milestones

- **E4**'s tests construct pending requests by calling this milestone's
  `Enroll`, so its create-or-reuse and code-generation behavior is
  load-bearing for E4's fixtures as well as for users.
