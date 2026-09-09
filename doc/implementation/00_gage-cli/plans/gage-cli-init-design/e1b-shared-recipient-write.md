# E1b — The shared recipient write

[← E1a](e1a-unconditional-reencrypt.md) · [plan index](index.md) · next: [E2 — The sealed request](e2-sealed-request.md)

> **Recommended model: Sonnet\* ⚑.** Borderline. The change is additive
> and mechanical — one function grows a slice where it had a scalar — but
> it is M9's central write path, and the property it must not lose is
> crash-safety: a failure partway leaves HEAD untouched. That is the kind
> of "subtly wrong is unrecoverable" this plan reserves Opus for, so
> **the tests are worth an Opus review pass** even where Sonnet writes
> them. Switch outright if `commitRecipientList` turns out to resist the
> extra paths.

## Goal

Give `AddRecipient`'s body an N-recipient form, so that adding several
recipients is **one** lock, **one** trust question, **one** re-encryption
pass and **one** commit — and so that commit can also delete files handed
to it.

`AddRecipient` becomes the one-recipient caller of that form. Nothing
about what it does changes.

**Not enrollment**, and not A19 either. This is the structural half of
the M9 amendment: [E1a](e1a-unconditional-reencrypt.md) changed what
`recipient add` does, this changes the shape of the function it does it
in. E4's batch approval is the reason it exists.

## Depends on

- **E1a** — the `reencrypt bool` is gone by then, so the extraction never
  has to thread a parameter through a body it is about to lose. Doing
  these in the other order means touching the same signature twice.
- **M9** — `AddRecipient`, `reencryptTo`, `commitRecipientList`, and the
  all-or-nothing commit.

## Design references

- ["Approving several devices at once is one re-encryption pass"](../../tdds/gage-cli-init-design.md)
  — the requirement this exists to serve, including why "reuses the
  existing machinery unchanged" was not quite true
- ["Approval always re-encrypts"](../../tdds/gage-cli-init-design.md) —
  why every approval rewrites every entry, which is what makes doing it
  once per batch worth the extraction

## Decisions

### What the shape actually is

`AddRecipient` today is a complete write (`internal/gage/recipient.go`):
validate → `withVaultWrite` (lock + dirty-tree reset) →
`requireRecipientsInSync` → `confirmRecipientTrust` → duplicate check →
append **one** recipient → `commitRecipientList` (one commit) →
`noteRecipientsReviewed`.

Three things change, and only three:

- **The append takes a slice**, and the duplicate check runs over all of
  them — including against each other, so a batch containing the same
  pubkey twice is caught rather than written twice.
- **The commit takes extra paths to delete**, alongside the recipient
  files and the entries it rewrites. E4 passes the approved requests'
  files; nothing else has a use for it yet.
- **The commit takes the failed-push clause its caller wants said.**
  This is the third change, and an earlier draft of this milestone
  omitted it — which would have left E4 unable to pass a test on its own
  list. `commitRecipientList` ends by calling
  `v.pushAfterWrite(ident.warnTo())`, which emits one fixed sentence:
  *"the write is committed locally but not pushed."* That is right for
  `recipient add` and much too thin for approval, whose local-versus-
  remote split is the widest in the tool — locally the vault is
  re-encrypted, the recipient is listed and the request file is gone; on
  the remote none of it happened, the request is still pending, and the
  joining device is still locked out with `gage sync` reporting nothing
  wrong. The TDD spells that message out under "Approval's failed push
  needs the same treatment"; this is the parameter that lets it be said.

  Shape it as a caller-supplied clause with today's text as the default,
  not as a second push path: `pushAfterWrite` keeps its signature and
  gains a sibling taking the clause, the divergence branch is untouched
  (`report.Summary()` is already the right thing in both cases), and
  `recipient add`/`recipient remove` pass nothing and behave exactly as
  they do now.

  With three parameters arriving at once, `commitRecipientList` is past
  the point where positional arguments read well. Bundling them into one
  options struct is fine and probably better; what must not happen is the
  extra paths and the clause reaching the commit through two different
  mechanisms because they were added in two sittings.

Everything else stays where it is. In particular the lock, the reset,
`requireRecipientsInSync`, the trust question and the cache regeneration
happen **once** per call regardless of how many recipients are in the
slice — that is the entire point, and each of them being once is a
separate test below.

### Why this is its own milestone

It was folded into E1a in an earlier draft, on the reasoning that E1a is
already inside `AddRecipient` so the alternative is opening it twice.
That is true and it is still the weaker argument.

- **They are different kinds of change.** E1a is a behavior change with a
  user-visible surface (a flag disappears, a new refusal appears, four
  prose sites stop naming `--reencrypt`). E1b is a pure refactor with
  *no* observable difference — its correctness criterion is that nothing
  changes. Reviewing them together means reading every diff hunk twice to
  ask which kind it is.
- **They fail differently.** E1a's risk is missing a site that still
  teaches the flag. E1b's risk is losing atomicity on M9's central write
  path, which is invisible until a crash. Those want different attention,
  and one milestone can only carry one recommendation about where to
  spend it.
- **Only one of them is on E4's critical path for correctness.** E1a can
  ship and be useful on its own — A19 is a real fix whether or not
  enrollment is ever built. E1b exists solely because E4 needs it.

The cost is that `recipient.go` is opened twice. That is a smaller cost
than it looks: E1a adds a pass *beside* the method and removes a
parameter from it, while E1b restructures the body. They barely touch the
same lines.

### What this is not

**Not a batch `recipient add` command.** No CLI surface changes here. The
N-recipient form is unexported and has exactly two callers when this
lands: `AddRecipient`, and later `ApproveEnrollments`. Adding a
`recipient add` that takes several pubkeys would be a new user-facing
verb nobody asked for, and the project's no-speculative-abstraction rule
applies to *interfaces*, not to giving one existing function the shape
its second caller needs.

## Tests (write first)

**The N-recipient form**

- [x] **Several recipients land in one commit**, and every one of them
      can read every pre-existing entry. Exercise the form directly at
      the library level — E4 is what puts a command on it, and a test
      that waits for E4 leaves this milestone shipping an untested
      function.
- [x] **One re-encryption pass for the batch**, not N. Assert on the
      count of entry rewrites, not on wall-clock time: N passes over the
      same entries produce the same end state and a different commit
      count, which is what makes this worth pinning rather than assuming.
- [x] **One trust question for the batch.** M10's
      `ConfirmRecipientChange` is asked at most once however many
      recipients are added, and declining it aborts the whole batch with
      nothing committed.
- [x] **One lock acquisition and one dirty-tree reset**, so the warning
      about discarded leftovers cannot appear N times for one operation.
- [x] **The extra paths are deleted in that same commit** as the
      recipient files and the re-encrypted entries. Assert the mechanism
      with an ordinary file rather than a pending request, so this
      milestone's tests do not depend on a feature two milestones away.
- [x] **A caller-supplied failed-push clause reaches the warning**, and
      the default is today's text. Two assertions: a call passing a
      clause warns with it, and `AddRecipient` — passing none — produces
      the byte-for-byte message M9 already produces. Injected fake
      `RemoteSyncer`, not a real failure. E4 is what needs the first
      half; the second half is what keeps this milestone's "nothing
      observable changed" claim true.
- [x] **The divergence branch is untouched.** A push that fails as a
      divergence still warns with `report.Summary()` whatever clause was
      supplied — the clause describes what did not get published, not
      what went wrong, and a diverged push has its own established
      sentence.
- [x] **A duplicate *within* the batch is refused** — the same pubkey
      twice, or two entries claiming one device name — before anything is
      written. The existing check only ever compared against the recipient
      list, because a batch of one has nothing to collide with.
- [x] A recipient in the batch that **already exists in the vault** is
      refused with the pre-existing `ErrRecipientExists`, unchanged. (E4
      turns that case into a no-op *before* it reaches here, by dropping
      the request from the batch; this level still refuses it, which is
      what makes the check authoritative.)

**Atomicity — the property that must not be lost**

- [x] **An injected failure partway leaves HEAD untouched**: no
      recipients added, no entries rewritten, no paths deleted. This is
      M9's existing guarantee re-asserted on the new shape, and it is the
      reason this milestone is flagged for a review pass.
- [x] The same holds for a failure **in the deletion step specifically** —
      a path that cannot be removed does not leave a commit behind with
      the recipients added and the file still present.
- [x] A **declined** trust question leaves the same nothing: no commit,
      no partial re-encryption, no cache regeneration.

**`AddRecipient` is unchanged**

- [x] **Every M9 test for `recipient add` still passes**, untouched. This
      milestone's correctness criterion is that nothing observable
      changed, so the existing suite is the primary assertion and this
      bullet is a reminder not to "update" a test to match a regression.
- [x] `AddRecipient`'s duplicate check under the lock still returns
      `ErrRecipientExists`, and its trust question, cache regeneration and
      commit message are what they were.
- [x] `RemoveRecipient` is untouched — it is not a caller of the new form
      and gains nothing here.

## Implementation

- [x] **Extract the N-recipient, lock-held body** of `AddRecipient` per
      "What the shape actually is": the append takes a slice, the
      duplicate check covers the slice and the existing list, and
      `commitRecipientList` gains both the extra paths to delete in the
      same commit and the failed-push clause.

      **Lock-held is the word that matters**, and E4 depends on it.
      `AddRecipient` keeps its own `withVaultWrite` wrapper and the
      extracted body assumes the lock is already held — which is what
      lets `ApproveEnrollments` take the lock itself, fetch inside it,
      re-verify the seals, and only then call this. An extraction that
      swallowed the wrapper would force approval's fetch either outside
      the lock or into `recipient add`, and both are wrong.
- [x] `AddRecipient` becomes the one-recipient caller. Its signature and
      behavior are unchanged; it wraps the value in a one-element slice,
      passes no extra paths, and passes no push clause so the default
      stands.
- [x] **Unexported.** E4's `ApproveEnrollments` is its only other caller
      and lives in the same package, so there is no reason to widen the
      library's surface for it.
- [x] Give `pushAfterWrite` a sibling taking the caller's failed-push
      clause, with the existing function delegating to it and keeping its
      current text. The divergence branch stays as it is.
- [x] Keep `commitRecipientList`'s all-or-nothing property intact — the
      working tree is written first, then everything lands in exactly one
      commit, and a failure anywhere before that leaves HEAD alone. If
      the extra-paths parameter makes that awkward, that is the signal to
      switch models rather than to weaken the guarantee.

## Definition of done

Every test above green on all three platforms, **plus M9's existing
`recipient add` suite green and unmodified**. `make lint` and `make test`
clean. No user-visible behavior differs from before this milestone —
that is the whole claim.

## Affects later milestones

- **E4**'s `ApproveEnrollments` is built directly on this. Without it,
  approval either re-implements M9's central write path or falls back to
  N `AddRecipient` calls — N locks, N trust prompts, N re-encryption
  passes, N commits — which is neither the single atomic commit approval
  is specified to produce nor a place to delete the approved requests'
  files from.

  Three specific things E4 consumes, each of which fails a named E4 test
  if it is missing here: the **slice** (one commit for a batch), the
  **extra paths** (the request files cleared in that commit), and the
  **push clause** (approval's local-only warning). The third is the one
  an earlier draft dropped, and its absence is invisible until E4's
  failed-push test runs.
