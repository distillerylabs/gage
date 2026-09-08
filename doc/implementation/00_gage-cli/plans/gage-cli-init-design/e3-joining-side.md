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
- **E0** — identity files live at the id-keyed path, and the id is
  available from global config.
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
- [ ] A **push rejected for lack of write access** leaves the identity
      and the local commit in place, and the error names both what
      happened and `gage auth login` — never a bare transport error.
- [ ] Re-running after that failure reuses the existing identity, mints
      a fresh request and code, and succeeds once the push is allowed.
- [ ] `identity enroll` warns, and proceeds, when local time is behind
      the vault's HEAD committer timestamp.

**Collisions**

- [ ] `identity enroll` whose device name already labels a recipient
      fails with `ErrDeviceNameTaken`, **writes no identity file**, and
      the message names `--device`.

**Clone integration**

- [ ] An interactive `clone` of a vault this device can't read offers to
      enroll; answering yes produces the same end state as `clone`
      followed by `identity enroll`.
- [ ] Answering `n` leaves the vault cloned, no identity written,
      nothing pushed — and a later `identity enroll` still works.
- [ ] A **non-interactive** `clone` never prompts, never writes an
      identity, never pushes, exits 0, and prints the run-`identity
      enroll` message — the behavior a scripted clone has today.
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

**Crash-safety of a partial enroll**

- [ ] A file written into `pending/` by an enroll that died before
      committing is **inert** — an entry written afterwards is not
      decryptable by the key it names.
- [ ] That stray is pruned on its own epoch like any other expired
      request.
- [ ] **No commit ever stages the `pending/` directory wholesale.**
      Construct a stray, run an unrelated write, and assert the stray is
      absent from the resulting commit. This is what keeps a local
      artifact from becoming something other devices see.
- [ ] `recipient pending` on the machine that failed lists the stray; on
      any other machine it does not exist.

## Implementation

- [ ] `Vault.Enroll(device, ttl, Prompter) (EnrollmentRequest, error)`,
      ordered per `D-ENROLL-REMOTE`: remote check → lock → fetch+pull →
      collision check → identity → seal/write/commit → push → release.
- [ ] `cmd/gage`'s `identity enroll`, registered with a `Short`, the
      `Identity` group, and both-mode availability.
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
