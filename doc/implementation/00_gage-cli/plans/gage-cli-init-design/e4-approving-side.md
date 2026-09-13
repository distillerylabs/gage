# E4 — Approving a device

[← E3](e3-joining-side.md) · [plan index](index.md)

> **Recommended model: Opus.** Three things at once that are each easy to
> get subtly wrong: an atomic multi-request commit, an unlock that
> happens in the *middle* of the command rather than around it, and a
> re-verification under the lock of something already shown to a human.

## Goal

`gage recipient pending` / `approve` / `deny`: list what is waiting,
authenticate a request with an out-of-band code, and — on confirmation —
add the device as a recipient, re-encrypt the vault, and clear the
request, all in one commit.

This is the milestone that makes enrollment actually grant access.

## Depends on

- **E2** — `PendingEnrollments`, `OpenEnrollment`, `ResolveEnrollment`,
  and pruning are all built and proven there. E4 wires them to commands
  and reimplements none of them. Listed explicitly rather than left
  implicit through E3, because E4 is the milestone that *calls* all four
  and the boundary is otherwise easy to blur.
- **E3** — requests exist to approve, and its `Enroll` builds the test
  fixtures.
- **E1a** — approval always re-encrypts and reuses
  `ErrCannotGrantFullAccess`, **and needs its pre-flight callable on its
  own** rather than only from inside `AddRecipient`: approval runs it at
  a different point in the sequence than `recipient add` does.
- **E1b** — the **N-recipient form** of `AddRecipient`'s body, which this
  milestone's one-commit batch is built on. See "The batch path is not
  `AddRecipient`" below; this is the dependency that turns approval into
  wiring rather than a refactor. Three specific things are consumed from
  it, and each has a test on this milestone's list: the **slice** (one
  commit for a batch), the **extra paths** (the request files cleared in
  that commit), and the **failed-push clause** (approval's local-only
  warning, which the shared commit tail cannot otherwise say). It must
  also leave the `withVaultWrite` wrapper on `AddRecipient` and extract
  the **lock-held** body, or approval's fetch has nowhere to go — see
  "Approval fetches before it commits".
- **E0** — `recipient approve --device` relabels recipients, which is
  precisely what makes E0's `removeOrphanedIdentity` fix necessary. **Do
  not ship E4 without it**, or approval introduces a new route to an
  unrecoverable key deletion.
- **M9** — `AddRecipient` and the all-or-nothing commit.
- **M10** — the trust cache, which approval regenerates and other
  devices' warnings come from.

## Design references

- ["The flows — approving device"](../../tdds/gage-cli-init-design.md)
- ["Approval always re-encrypts"](../../tdds/gage-cli-init-design.md)
- ["The approver unlocks, and the unlock comes late"](../../tdds/gage-cli-init-design.md)
- [`D-ENROLL-COLLISIONS`](../../tdds/gage-cli-init-design.md) — relabel,
  the already-a-recipient no-op, fresh-request-per-enroll
- ["Approval fetches before it commits"](../../tdds/gage-cli-init-design.md)
  — why approval catches up first when `recipient add` does not, and why
  its divergence advice differs from the joining side's
- ["The request names the vault it is for"](../../tdds/gage-cli-init-design.md)
  — `ErrEnrollmentWrongVault`, and why it is neither of its neighbours
- ["Interaction with the local trust cache"](../../tdds/gage-cli-init-design.md)

## Decisions

Settled — but read the second bullet before writing anything. It
corrects an ordering this milestone inherited from E1a that cannot hold
here, and getting it wrong means either an impossible test or the loss
of the late unlock. The rest shape the structure more than they look
like they do:

- **The unlock comes after the confirmation.** Opening a request needs
  no identity, so a wrong code, an expired request, or a declined prompt
  must cost no unlock. This means `approve` **cannot** be a blanket
  `withUnlockedVault` wrapper the way `recipient add` is — it unlocks in
  the middle. That is the one place this feature departs from an
  existing command's shape, and it departs toward `sync`'s lazy unlock.
- **And therefore `ErrCannotGrantFullAccess` lands *after* that
  confirmation**, not before it — the one place this milestone's
  ordering differs from E1a's, and the thing to get right before writing
  any of it. The pre-flight decrypts every entry, so it needs the
  unlocked identity; an age file's X25519 stanzas carry an ephemeral
  share rather than a recipient key, so there is no cheaper form of the
  question. In `recipient add` the phrase "before any confirmation"
  means *before M10's trust prompt*, because the unlock has already
  happened. Here it can only mean the same thing. The guarantee that
  survives — and the one that was always the point — is that the refusal
  never arrives mid-write on an opaque entry UUID: it is still before
  the write lock and before M10's prompt.
- **Every supplied `--code` must open something.** One that opens
  nothing fails the whole run before any confirmation and before any
  commit, rather than approving a subset of the batch the human
  confirmed. A mistyped code is likelier than a surplus one.
- **An ID resolves the way an entry query does** — exact, then
  substring, then a candidate list — over the UUID portion only. `deny`
  deletes, so a resolver that guessed would delete a request nobody
  named.
- **A device name is a label; the pubkey is the identity.** Relabeling
  on approval is legitimate and changes nothing about what was
  authenticated.
- **Two `[y/N]` questions can appear**, and must not be collapsed: "let
  this device in" and M10's "do you trust the list you're about to
  encrypt to" are different questions.

### Settled at the start of implementation

Five questions the sections above leave open, resolved here before any
code was written. Four of them exist because the TDD's "Library surface"
block predates the two sections that follow it — the fetch and the batch
path — so its recorded signatures describe an earlier shape of this
milestone. Where they disagree with what follows, what follows wins, and
the TDD's block has been corrected to match rather than left to be
rediscovered.

- **`ApproveEnrollments` takes the codes, and re-opens each seal under
  the lock.** The recorded signature carries no code, and neither
  `Approval` nor `OpenedRequest` carries one or a path — so as written
  there is nothing to re-verify *with*. `OpenEnrollment` also never
  reports which code opened which request, so a `Code` field on
  `Approval` could not be populated by its caller without widening what
  that function returns. The codes therefore travel as their own
  parameter, and the re-verification is a genuine second open through
  `openSealedRequest`: expiry, vault id and the id/filename match are all
  re-checked, and **what gets written to the recipient list comes from
  that second open**, never from the struct the human was shown. A file
  that is gone by then is `ErrEnrollmentNoSuchRequest`. The cost is one
  extra scrypt run per approved request, at the factor D-ENROLL-SEAL-COST
  chose to make exactly this affordable.

  The alternative considered and rejected was a digest of the sealed
  bytes captured at open time and compared under the lock. It detects the
  swap just as well, and costs no KDF run — but it leaves the *first*
  open's values being written, which is a weaker reading of "what gets
  written is what was verified under the lock" than this milestone's test
  list asks for.

- **`ApproveEnrollments` takes a `context.Context`, first.** Its fetch is
  a hard precondition whose failure changes the outcome, which is the
  TDD's own test for which methods take one (`Enroll`, `Pull`, `Push`,
  `Sync`, `SyncResolving` all do). Minting one internally would make
  approval the single blocking network call in `gage` a caller cannot
  cancel. `DenyEnrollment` is deliberately *not* changed: it performs no
  fetch, and its push rides `pushAfterWriteSaying`, which mints its own
  context like every other write.

- **The already-a-recipient no-op is filtered before the shared body, and
  an all-duplicate batch takes a clear-only commit.**
  `addRecipientsLocked`'s duplicate check returns `ErrRecipientExists`,
  and handing it an empty slice would still re-encrypt every entry — so
  neither a pass-through nor an empty call can produce the outcome this
  milestone's test list requires ("request cleared, zero entries
  re-encrypted, no new recipient, `Added: false`"). Approval therefore
  drops requests whose pubkey is already listed, and:

  - a batch with something left to add calls the shared body, with the
    dropped requests' files riding in `deletePaths` — so a mixed batch is
    still one lock, one trust question, one pass, one commit;
  - a batch with nothing left takes a clear-only write under the same
    lock: delete the request files, prune, commit, push. It rewrites no
    entry, asks M10's trust question **not at all** — nothing is being
    encrypted to anything — and does not regenerate the trust cache,
    since no recipient list changed for it to bless.

  The authoritative duplicate check stays where "The batch path is not
  `AddRecipient`" puts it: inside the shared body, under the lock,
  returning `ErrRecipientExists`. What is filtered here is the case that
  is *not* an error.

- **The collision pre-check is a library pass that `cmd/gage` calls**,
  rather than a comparison written in `cmd/gage` itself. The TDD's
  wording ("`cmd/gage`'s pre-check returns `ErrEnrollmentNameTaken`") is
  about *where in the sequence* it runs, not about which package holds
  it: after the open, before the render, before the confirmation, holding
  no identity. Putting the comparison itself in the frontend would leave
  a future GUI to reimplement an access-control check, which the
  library/CLI split treats as a correctness property. Nothing else moves:
  the ordering is unchanged, the error is still
  `ErrEnrollmentNameTaken` at `Conflict`, and the authoritative check
  under the lock still returns `ErrRecipientExists`.

- **The prompted-code retry loop ends on empty input**, with a runaway
  guard rather than a policy. `--code` never retries — a flag that opens
  nothing fails the whole run, which this milestone's test list already
  fixes. The prompted path loops while the answer is non-empty and the
  refusal is `ErrEnrollmentCodeWrong`; a bare Enter aborts with nothing
  done, and the guard mirrors `maxUnlockAttempts`, which `unlock.go`
  describes as "a runaway guard, not a retry policy" for the same reason.
  Every other refusal from the open path — wrong vault, malformed,
  expired, clock skew — exits with its own advice rather than
  re-prompting for a code that is already correct.

Two smaller ones, settled by existing precedent rather than by argument:
pruning hooks into `commitRecipientList`, which is the one site `recipient
add`, `recipient remove` and approval all pass through under the lock,
with `deny` calling it directly since it has its own write; and declining
the approve `[y/N]` follows `confirmSelfRemoval` — `exitcode.Conflict`
with a message saying nothing was written, not a silent exit 0.

### Approval fetches before it commits

Settled after this doc was first written, and it is the one place
approval's shape departs from `recipient add`'s on purpose rather than
by inheritance.

Approval rewrites **every entry**. A commit that touches every entry made
onto a stale tip does not diverge in one place, it diverges in all of
them — every entry another device touched meanwhile becomes an entry
conflict the approver answers `[l/r/b]` to, one at a time, through `gage
sync`. `recipient add` has the same shape and gets away with
it because it is rare; approval is the ordinary way a device joins a
vault, so its stale-tip case is a Tuesday rather than a corner.

So `ApproveEnrollments` fetches and fast-forwards under its own write
lock, before it re-verifies and writes. Three things follow, and the
first is the one that will cost a day if it is missed:

- **It pulls via the unexported `v.pull`, inside its own lock** — the
  same trap E3 documents at length for enroll. `Pull` takes the lock
  itself through `underWriteLock` and `vaultlock` is not re-entrant, so
  calling it here blocks against this very process until the acquisition
  times out and reports contention. Read E3's implementation bullet on
  this before writing it; the failure looks like an unrelated
  concurrency bug.
- **A divergence refuses before anything is written, with the
  *ordinary* advice.** No new error, and minting one would be wrong: an
  approver holds a key and can decrypt, so `gage sync` is exactly the
  command that resolves this for them. `ErrEnrollmentDiverged` exists
  because that advice is wrong for a device with *no* key, which is not
  the situation here. As on the joining side, `v.pull` reports divergence
  with a **nil error** and `SyncReport.Diverged` set, so it is checked
  for rather than caught.
- **An unreachable remote warns and proceeds**, unlike enroll. This is
  the genuine asymmetry: an unpublished enrollment request accomplishes
  nothing, so enroll refuses, while an approval that lands locally is
  real work — the vault is re-encrypted and the recipient is listed. The
  existing failed-push warning is what tells the human the rest.

**The fetch can resolve the request out from under the run**, which is
the consequence to expect rather than rediscover: another device may
have approved or denied it in the window. The re-verification under the
lock finds no file, and the run reports `ErrEnrollmentNoSuchRequest` —
the request is no longer pending, which is what that error already says
— with a message naming the likely cause rather than implying a mistyped
ID. Nothing is committed, and the outcome is right in both directions:
approved elsewhere means the device already has access, denied means it
should not be granted any.

This window pre-dates the fetch; it is the same swap the under-the-lock
re-verification already exists to catch. What the fetch changes is how
often it happens, which is why the outcome gets a name instead of being
left to surface as an internal error.

**Where it sits relative to E1b's extraction** is what makes it
implementable: E1b extracts the **lock-held body**, leaving the
`withVaultWrite` wrapper on `AddRecipient`. `ApproveEnrollments`
therefore takes the lock itself, pulls, re-verifies, and *then* calls
the shared body. Approval's fetch never leaks into `recipient add`, and
it never has to happen outside the lock.

### The batch path is not `AddRecipient`

Settled after this doc was first written, and it changes what "wires
E2's primitives to commands" means in one place.

`AddRecipient` is a complete write: lock, dirty-tree reset,
`requireRecipientsInSync`, M10's trust question, duplicate check, append
**one** recipient, one commit, regenerate the cache. Calling it once per
approved request would take N locks, ask N trust questions, run N
re-encryption passes and produce N commits — the exact thing batch
approval exists to avoid, and incompatible with "removal of every
approved request's file lands in the same commit."

`ApproveEnrollments` therefore calls **[E1b](e1b-shared-recipient-write.md)'s
N-recipient form**: the same sequence, with a slice of recipients and the
pending files to delete passed to the commit. E1b owns that extraction;
this milestone consumes it. If E1b was skipped, stop and do it there
rather than growing a second implementation here — two functions that
both mean "add recipients and re-encrypt" is how they drift.

**What this does not change** is where the authoritative duplicate check
lives. It is still inside that shared body, still under the lock, and it
still returns `ErrRecipientExists`.

## Tests (write first)

**Approval**

- [x] Approving adds exactly one recipient to both `.age-recipients` and
      `config.toml`, and `verify` still passes afterward.
- [x] **Approval makes every pre-existing entry readable by the new
      key** — the device can read entries written long before it
      existed. No flag and no code path produces a partially-readable
      recipient.
- [x] Approval, the re-encryption, and removal of the request's file
      land in **one** commit.
- [x] Batch approval of N requests performs **one** re-encryption pass
      and produces **one** commit.
- [x] An injected failure partway through the re-encryption leaves HEAD
      untouched and every request still pending.
- [x] **An approver who cannot read every entry is refused with
      `ErrCannotGrantFullAccess` before the write lock and before M10's
      recipient-change prompt** — the error names the count of unreadable
      entries, never a bare decryption failure on a UUID. Assert the
      position precisely: nothing committed, no entry rewritten, and
      M10's prompt never shown.
- [x] That refusal comes **after** the approve confirmation and the
      unlock, which is the documented cost of the late unlock: assert the
      passphrase prompt count is one, not zero. A test asserting zero
      here is asserting the impossible — see this milestone's Decisions.
- [x] `deny` removes the file, grants nothing, and needs no code.
- [x] **Approve and deny each prune expired requests they encounter**,
      since both are already writing and holding the lock. A request
      expired by its filename epoch, and one whose epoch is beyond the
      ceiling, are both gone from `pending/` after either command — in
      the same commit, not a second one.
- [x] **`recipient add` and `recipient remove` prune too.** The TDD says
      pruning rides "the next recipient change"; E1a is the milestone that
      touches `AddRecipient`, but pruning does not exist until E2, so the
      wiring lands here. Assert an expired request is cleared by an
      ordinary `recipient add` with no enrollment involved.
- [x] **`deny` warns when its push fails** rather than reporting plain
      success — injected fake `RemoteSyncer`, and assert the warning
      reached the `Prompter`. A silent failed deny leaves the request
      live for every other device.
- [x] **`approve` warns when its push fails**, and the local commit
      stands. Injected fake `RemoteSyncer`. Assert all of it, because
      approval's local/remote split is the widest in the tool: locally
      the recipient is listed, every entry is re-encrypted, and the
      request file is gone; on the remote none of that happened and the
      request is still pending. The warning must say the approval is
      local-only and name the push, not just "not pushed" — the joining
      device is still locked out and `gage sync` will tell it nothing is
      wrong. This is the message **E1b's failed-push clause** exists to
      carry; a run producing the generic "the write is committed locally
      but not pushed" is the signal that the clause never got threaded
      through, not that the wording needs adjusting here.
- [x] **A re-approval after a failed push, once the push lands, is the
      already-a-recipient no-op** — not a second grant and not an error.
      This is what makes the failed-push state self-healing, so it is
      worth pinning rather than reasoning about.

**Approval fetches before it commits**

- [x] **Approval fast-forwards before it writes.** Against a bare remote
      that moved ahead, assert the approval commit lands on top of the
      remote's tip and the push is a fast-forward — not a divergence that
      leaves the approver resolving one conflict per entry.
- [x] **A diverged remote refuses before anything is written**: no
      recipient added, no entry rewritten, nothing committed, request
      still pending. The message says `gage sync`, which is **correct
      here** and is the one place this milestone's advice deliberately
      differs from E3's — assert the text, since the whole point of
      `ErrEnrollmentDiverged` existing separately is that it is *not*
      used on this side.
- [x] **The nil-error trap is pinned on this side too.** `v.pull` reports
      divergence with a nil error and `SyncReport.Diverged` set, so
      assert the refusal rather than an error propagating — the same test
      shape E3 uses, for the same reason, against a second call site.
- [x] **An unreachable remote warns and the approval proceeds** — the
      asymmetry with enroll, which refuses. Injected fake `RemoteSyncer`.
      Assert the local work actually happened: recipient listed, entries
      re-encrypted, request cleared, and the failed-push warning shown.
- [x] **An uncontended approval never reports contention**, the same
      plain-success assertion E3 makes: a `Pull` called from inside
      approval's own lock fails as `*vaultlock.ContendedError` after the
      full timeout, so assert the success path completes well inside
      `vaultLockTimeout`.
- [x] **A request resolved by another device between the open and the
      lock is reported, not crashed into.** Construct it: open the
      request, then move the bare remote ahead with a commit that deletes
      that pending file, then let the run reach its fetch.
      `ErrEnrollmentNoSuchRequest` at `exitcode.NotFound`, nothing
      committed, and a message naming the likely cause rather than
      implying the ID was mistyped.

**The approver's unlock**

- [x] Approval prompts for the approver's passphrase — a git-writer
      holding no key of this vault cannot approve an enrollment.
- [x] **A wrong code costs no unlock**: passphrase prompt count zero
      when `--code` opens nothing. Same for an expired request.
- [x] **Declining the `[y/N]` costs no unlock**, and leaves the request
      pending with nothing committed.
- [x] In a session with the vault already unlocked, approval prompts for
      no passphrase and reuses the cached `Identity`.
- [x] When another device changed the recipient list first, the approver
      answers **two** distinct prompts, and declining the second aborts
      with nothing committed and the request still pending.
- [x] A sealed request swapped between the open and the commit is
      caught: what gets written is what was verified **under the lock**,
      not what was displayed.

**Codes and IDs**

- [x] `--code A --code B` where B opens nothing fails the whole run with
      `ErrEnrollmentCodeWrong` at `exitcode.LockedOrAuth` — nothing
      confirmed, nothing approved, A's request still pending.
- [x] With no `--code`, the code is read through `Prompter.Value`, and
      `cmd/gage` — not the library — owns the retry loop around a wrong
      one.
- [x] A positional ID narrows an `approve` run to that request even when
      the code would have opened several.
- [x] `deny <ambiguous-substring>` returns the matches as
      `*AmbiguousRequestError`, deletes nothing, and exits `Ambiguous`;
      `cmd/gage` renders the list. `approve` resolves through the same
      function, so the behavior is identical there.
- [x] `deny <unmatched>` is `ErrEnrollmentNoSuchRequest` at
      `exitcode.NotFound`, and deletes nothing.
- [x] `deny <8-char-prefix>` works — the form the TDD's own transcript
      uses.
- [x] **`approve` with no ID against a stuffed `pending/` refuses**
      with `ErrEnrollmentTooManyPending` at `exitcode.Conflict`, prompts
      for nothing, commits nothing, and the message names
      `approve <ID>`. E2 proves the bound; this proves the command
      surfaces it as advice rather than as a bare error.
- [x] **`approve <ID>` against that same directory works**, and `deny`
      and `recipient pending` are unaffected by it — neither opens
      anything, so a stuffed directory stays inspectable and cleanable.
      This is the pair that makes the refusal a detour rather than a
      dead end.
- [x] **The two runs differ only in the scope they pass.** Assert at the
      command level what E2 asserts at the library level: a broad run
      hands `OpenEnrollment` the `PendingEnrollments` result, an
      ID-scoped run hands it the single `ResolveEnrollment` match, and
      neither passes a flag that turns the bound on or off. A `cmd/gage`
      that reached for a "skip the bound" parameter instead would pass
      the two bullets above and lose the property that makes the bound
      safe to have in the first place.
- [x] A request expired by its **sealed** copy is refused with
      `ErrEnrollmentExpired`; one that was expired before it was created
      is refused with `ErrEnrollmentClockSkew`, and the message names the
      requesting device's clock rather than a stale request.

**Non-interactive**

- [x] `recipient approve` under `--script` fails cleanly rather than
      approving: `--yes` answers only `ConfirmRecipientChange`, so
      approve's own `[y/N]` stays a real question and defaults to no.
      Assert nothing is committed and the message says why.
- [x] `recipient pending` and `recipient deny` work unchanged under
      `--script` — neither asks anything a script cannot answer.

**Collisions and duplicates**

- [x] A request whose device name became taken between enroll and
      approve is refused with `ErrEnrollmentNameTaken` **before the
      confirmation and before any unlock** — this one genuinely can come
      first, since it reads only the plaintext recipient list.
- [x] The authoritative check **in the shared recipient-write body**,
      under the lock, still fires when another writer takes the name in
      the window — and returns the pre-existing `ErrRecipientExists`, not
      `ErrEnrollmentNameTaken`. Two errors for one condition is
      deliberate; see the TDD's "Library surface". (Phrased against the
      shared body rather than `AddRecipient` because approval does not
      call `AddRecipient` — see "The batch path is not `AddRecipient`".
      The check is the same code; the entry point is not.)
- [x] `approve --device <other-name>` applies that same request,
      recording the approver's label. The recipient's pubkey is the
      sealed one, unchanged — relabeling changes the name and nothing
      else.
- [x] `--device` with a run resolving to more than one request is a
      usage error naming the ID form; with exactly one request it
      applies.
- [x] **`--device` with an invalid name is rejected at the command line**,
      at `exitcode.Usage`, before the code is tried and before any
      confirmation. Today that validation lives inside the recipient
      write, which would surface it after the approver has already
      answered `[y/N]` and typed their passphrase — a rejection of the
      command line should cost neither.
- [x] **A request sealed for a different vault is refused with
      `ErrEnrollmentWrongVault`**, before the confirmation and before any
      unlock, and the message names the vault the request is actually
      for. E2 proves the comparison; this proves the command surfaces it
      as an actionable sentence rather than a bare mismatch, and that it
      does not enter the wrong-code retry loop — assert the prompt count,
      since a retry here re-prompts for a code that is already correct.
- [x] **A request whose sealed payload is malformed is refused with
      `ErrEnrollmentMalformedRequest`, and its contents are never
      rendered.** E2 proves the validation; this proves the command never
      prints an unvalidated device name. Use a payload carrying ANSI
      escapes: the approve confirmation is the one screen in `gage` whose
      correctness depends on a human reading it, and it sits directly
      below that field.
- [x] Approving a request whose **pubkey is already a recipient**
      succeeds with no work — request cleared, zero entries
      re-encrypted, no new recipient, outcome reports `Added: false`.
- [x] Approving one of two duplicate requests for the same device, then
      the other, leaves exactly one recipient — the second approval is
      the no-op above, not an error.

**Listing**

- [x] `recipient pending` needs no unlock and no code, and shows id and
      expiry only — never device names, which are sealed.
- [x] Expiry renders in the local timezone; the same request renders
      differently under two `TZ` values while naming the same instant.
- [x] `recipient pending` exits 0 with a "no pending requests" message
      when there are none.

**Trust cache**

- [x] A second already-authorized device gets the ordinary
      recipient-change warning after someone else approves an
      enrollment.
- [x] The approving device does not warn itself about its own approval.
- [x] A pending request alone triggers no warning on any device.

## Implementation

- [x] Wire **E2's** `PendingEnrollments()` and
      `OpenEnrollment(requests, codes)` into `recipient pending` and
      `approve`. Both are built and proven in E2; nothing here
      reimplements them. What E4 depends on is their signature — neither
      takes an `Identity` — since that is what makes the late unlock
      possible, and it is a contract, not an accident.

      **`OpenEnrollment` takes the scope, and `cmd/gage` chooses it.**
      With no ID, hand it `PendingEnrollments()`' result, which is where
      the 32-request bound can bite. With an ID, hand it the single
      `ResolveEnrollment` match, which never can. That is the whole
      difference between the two runs — there is no flag, no second code
      path, and nothing that switches the bound off. The escape hatch
      named in `ErrEnrollmentTooManyPending`'s message works because the
      human narrowed the question, not because the tool relaxed a rule.
- [x] `ApproveEnrollments(approvals []Approval, ident *Identity)
      (ApprovalResult, error)` with `Approval{Request, Label}` and
      per-request `ApprovalOutcome`. `RecipientChange` is deliberately
      *not* reused — it names one device, and a batch resolves N.
      Implemented over **E1b's N-recipient form**, handing it the labels
      to add, the pending files to delete, and the failed-push clause, so
      the whole batch is one lock, one trust question, one re-encryption
      pass and one commit. It does **not** call `AddRecipient` — see this
      milestone's Decisions.

      **It takes the lock itself, fetches inside it, and re-verifies
      before calling the shared body.** E1b extracts the *lock-held*
      body and leaves `AddRecipient` its `withVaultWrite` wrapper, which
      is exactly what makes this possible: approval's fetch lives in
      approval's own preamble and never leaks into `recipient add`.
      Reach the network through the unexported `v.pull` — `Pull` takes
      the lock itself and `vaultlock` is not re-entrant, so calling it
      here blocks against this process for `vaultLockTimeout` and then
      reports contention against its own lock. Check
      `SyncReport.Diverged` explicitly; `v.pull` returns nil in that
      case. See "Approval fetches before it commits".
- [x] `DenyEnrollment(id string, p Prompter) error` — takes a
      `Prompter` despite holding no `Identity`; the TDD records why that
      is a deliberate exception rather than an erosion of the
      interaction-rides-the-Identity rule.
- [x] `cmd/gage` orders it: open → collision check → render → confirm →
      **unlock** → full-access pre-flight → lock → re-verify → add +
      re-encrypt + clear → commit → push. The pre-flight sits between the
      unlock and the lock, which is why E1a must expose it as its own pass
      rather than burying it inside `AddRecipient`.
- [x] Three commands registered in the `Recipients` group, both-mode
      availability. `recipient list`'s `Short` reworded to "List the
      keys this vault is encrypted to", against `identity list`'s "List
      the keys this machine holds for a vault" (reworded in E3) — they
      answer different questions and previously scanned as synonyms.
- [x] Pruning wired into `AddRecipient`/`RemoveRecipient` as well as
      approve and deny — the "next recipient change" half of the TDD's
      pruning rule, which has no other home.
- [x] Every error mapped to the exit code the TDD's "Library surface"
      table assigns it, **`ErrEnrollmentWrongVault` at `Conflict`
      included** — and kept out of the wrong-code retry loop, alongside
      `ErrEnrollmentMalformedRequest`. The loop keys on
      `ErrEnrollmentCodeWrong` and only on it; every other refusal from
      the open path exits with its own advice. Its message names the
      vault the request belongs to, which `cmd/gage` can look up from
      global config's registrations and the user recognises, unlike the
      id itself.
- [x] Codes gathered from `--code` or, when absent, `Prompter.Value`,
      with the retry loop around `ErrEnrollmentCodeWrong` owned by
      `cmd/gage`. **No `Prompter` change**, and no new `UnlockKind`.

      **A code passed as `--code` lands in command history** — verbatim
      in the session history file, and in the user's own shell history for
      a one-shot run. That is an accepted risk rather than something this
      milestone fixes
      ([register entry](../gage-cli-design/open-questions.md#enrollment-code-history));
      what makes it acceptable is that omitting `--code` falls through to
      the masked `Prompter.Value` prompt, which records nothing. Do not
      add a bullet asserting the code never reaches the history file —
      that claim is true of E3's generating side and false here.

## Definition of done

Every test above green on all three platforms, plus the full E2 and E3
lists still green. `make lint` and `make test` clean. A second device can
be enrolled and approved end to end, and can then read entries written
before it existed.

## Affects later milestones

None — E4 completes the feature. The two follow-ons that remain are
outside this plan: the hardware-method predicate (nothing to build until
a second identity method exists) and the pre-existing register entries
`Q-KEEPBOTH-DELMOD` and `Q-RELEASE`.
