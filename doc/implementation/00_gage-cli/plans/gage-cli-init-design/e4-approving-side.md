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
  wiring rather than a refactor.
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

- [ ] Approving adds exactly one recipient to both `.age-recipients` and
      `config.toml`, and `verify` still passes afterward.
- [ ] **Approval makes every pre-existing entry readable by the new
      key** — the device can read entries written long before it
      existed. No flag and no code path produces a partially-readable
      recipient.
- [ ] Approval, the re-encryption, and removal of the request's file
      land in **one** commit.
- [ ] Batch approval of N requests performs **one** re-encryption pass
      and produces **one** commit.
- [ ] An injected failure partway through the re-encryption leaves HEAD
      untouched and every request still pending.
- [ ] **An approver who cannot read every entry is refused with
      `ErrCannotGrantFullAccess` before the write lock and before M10's
      recipient-change prompt** — the error names the count of unreadable
      entries, never a bare decryption failure on a UUID. Assert the
      position precisely: nothing committed, no entry rewritten, and
      M10's prompt never shown.
- [ ] That refusal comes **after** the approve confirmation and the
      unlock, which is the documented cost of the late unlock: assert the
      passphrase prompt count is one, not zero. A test asserting zero
      here is asserting the impossible — see this milestone's Decisions.
- [ ] `deny` removes the file, grants nothing, and needs no code.
- [ ] **Approve and deny each prune expired requests they encounter**,
      since both are already writing and holding the lock. A request
      expired by its filename epoch, and one whose epoch is beyond the
      ceiling, are both gone from `pending/` after either command — in
      the same commit, not a second one.
- [ ] **`recipient add` and `recipient remove` prune too.** The TDD says
      pruning rides "the next recipient change"; E1a is the milestone that
      touches `AddRecipient`, but pruning does not exist until E2, so the
      wiring lands here. Assert an expired request is cleared by an
      ordinary `recipient add` with no enrollment involved.
- [ ] **`deny` warns when its push fails** rather than reporting plain
      success — injected fake `RemoteSyncer`, and assert the warning
      reached the `Prompter`. A silent failed deny leaves the request
      live for every other device.
- [ ] **`approve` warns when its push fails**, and the local commit
      stands. Injected fake `RemoteSyncer`. Assert all of it, because
      approval's local/remote split is the widest in the tool: locally
      the recipient is listed, every entry is re-encrypted, and the
      request file is gone; on the remote none of that happened and the
      request is still pending. The warning must say the approval is
      local-only and name the push, not just "not pushed" — the joining
      device is still locked out and `gage sync` will tell it nothing is
      wrong.
- [ ] **A re-approval after a failed push, once the push lands, is the
      already-a-recipient no-op** — not a second grant and not an error.
      This is what makes the failed-push state self-healing, so it is
      worth pinning rather than reasoning about.

**The approver's unlock**

- [ ] Approval prompts for the approver's passphrase — a git-writer
      holding no key of this vault cannot approve an enrollment.
- [ ] **A wrong code costs no unlock**: passphrase prompt count zero
      when `--code` opens nothing. Same for an expired request.
- [ ] **Declining the `[y/N]` costs no unlock**, and leaves the request
      pending with nothing committed.
- [ ] In a session with the vault already unlocked, approval prompts for
      no passphrase and reuses the cached `Identity`.
- [ ] When another device changed the recipient list first, the approver
      answers **two** distinct prompts, and declining the second aborts
      with nothing committed and the request still pending.
- [ ] A sealed request swapped between the open and the commit is
      caught: what gets written is what was verified **under the lock**,
      not what was displayed.

**Codes and IDs**

- [ ] `--code A --code B` where B opens nothing fails the whole run with
      `ErrEnrollmentCodeWrong` at `exitcode.LockedOrAuth` — nothing
      confirmed, nothing approved, A's request still pending.
- [ ] With no `--code`, the code is read through `Prompter.Value`, and
      `cmd/gage` — not the library — owns the retry loop around a wrong
      one.
- [ ] A positional ID narrows an `approve` run to that request even when
      the code would have opened several.
- [ ] `deny <ambiguous-substring>` returns the matches as
      `*AmbiguousRequestError`, deletes nothing, and exits `Ambiguous`;
      `cmd/gage` renders the list. `approve` resolves through the same
      function, so the behavior is identical there.
- [ ] `deny <unmatched>` is `ErrEnrollmentNoSuchRequest` at
      `exitcode.NotFound`, and deletes nothing.
- [ ] `deny <8-char-prefix>` works — the form the TDD's own transcript
      uses.
- [ ] **`approve` with no ID against a stuffed `pending/` refuses**
      with `ErrEnrollmentTooManyPending` at `exitcode.Conflict`, prompts
      for nothing, commits nothing, and the message names
      `approve <ID>`. E2 proves the bound; this proves the command
      surfaces it as advice rather than as a bare error.
- [ ] **`approve <ID>` against that same directory works**, and `deny`
      and `recipient pending` are unaffected by it — neither opens
      anything, so a stuffed directory stays inspectable and cleanable.
      This is the pair that makes the refusal a detour rather than a
      dead end.
- [ ] A request expired by its **sealed** copy is refused with
      `ErrEnrollmentExpired`; one that was expired before it was created
      is refused with `ErrEnrollmentClockSkew`, and the message names the
      requesting device's clock rather than a stale request.

**Non-interactive**

- [ ] `recipient approve` under `--script` fails cleanly rather than
      approving: `--yes` answers only `ConfirmRecipientChange`, so
      approve's own `[y/N]` stays a real question and defaults to no.
      Assert nothing is committed and the message says why.
- [ ] `recipient pending` and `recipient deny` work unchanged under
      `--script` — neither asks anything a script cannot answer.

**Collisions and duplicates**

- [ ] A request whose device name became taken between enroll and
      approve is refused with `ErrEnrollmentNameTaken` **before the
      confirmation and before any unlock** — this one genuinely can come
      first, since it reads only the plaintext recipient list.
- [ ] The authoritative check **in the shared recipient-write body**,
      under the lock, still fires when another writer takes the name in
      the window — and returns the pre-existing `ErrRecipientExists`, not
      `ErrEnrollmentNameTaken`. Two errors for one condition is
      deliberate; see the TDD's "Library surface". (Phrased against the
      shared body rather than `AddRecipient` because approval does not
      call `AddRecipient` — see "The batch path is not `AddRecipient`".
      The check is the same code; the entry point is not.)
- [ ] `approve --device <other-name>` applies that same request,
      recording the approver's label. The recipient's pubkey is the
      sealed one, unchanged — relabeling changes the name and nothing
      else.
- [ ] `--device` with a run resolving to more than one request is a
      usage error naming the ID form; with exactly one request it
      applies.
- [ ] **`--device` with an invalid name is rejected at the command line**,
      at `exitcode.Usage`, before the code is tried and before any
      confirmation. Today that validation lives inside the recipient
      write, which would surface it after the approver has already
      answered `[y/N]` and typed their passphrase — a rejection of the
      command line should cost neither.
- [ ] **A request whose sealed payload is malformed is refused with
      `ErrEnrollmentMalformedRequest`, and its contents are never
      rendered.** E2 proves the validation; this proves the command never
      prints an unvalidated device name. Use a payload carrying ANSI
      escapes: the approve confirmation is the one screen in `gage` whose
      correctness depends on a human reading it, and it sits directly
      below that field.
- [ ] Approving a request whose **pubkey is already a recipient**
      succeeds with no work — request cleared, zero entries
      re-encrypted, no new recipient, outcome reports `Added: false`.
- [ ] Approving one of two duplicate requests for the same device, then
      the other, leaves exactly one recipient — the second approval is
      the no-op above, not an error.

**Listing**

- [ ] `recipient pending` needs no unlock and no code, and shows id and
      expiry only — never device names, which are sealed.
- [ ] Expiry renders in the local timezone; the same request renders
      differently under two `TZ` values while naming the same instant.
- [ ] `recipient pending` exits 0 with a "no pending requests" message
      when there are none.

**Trust cache**

- [ ] A second already-authorized device gets the ordinary
      recipient-change warning after someone else approves an
      enrollment.
- [ ] The approving device does not warn itself about its own approval.
- [ ] A pending request alone triggers no warning on any device.

## Implementation

- [ ] Wire **E2's** `PendingEnrollments()` and `OpenEnrollment(codes)`
      into `recipient pending` and `approve`. Both are built and proven
      in E2; nothing here reimplements them. What E4 depends on is their
      signature — neither takes an `Identity` — since that is what makes
      the late unlock possible, and it is a contract, not an accident.
- [ ] `ApproveEnrollments(approvals []Approval, ident *Identity)
      (ApprovalResult, error)` with `Approval{Request, Label}` and
      per-request `ApprovalOutcome`. `RecipientChange` is deliberately
      *not* reused — it names one device, and a batch resolves N.
      Implemented over **E1b's N-recipient form**, handing it the labels
      to add and the pending files to delete, so the whole batch is one
      lock, one trust question, one re-encryption pass and one commit.
      It does **not** call `AddRecipient` — see this milestone's
      Decisions.
- [ ] `DenyEnrollment(id string, p Prompter) error` — takes a
      `Prompter` despite holding no `Identity`; the TDD records why that
      is a deliberate exception rather than an erosion of the
      interaction-rides-the-Identity rule.
- [ ] `cmd/gage` orders it: open → collision check → render → confirm →
      **unlock** → full-access pre-flight → lock → re-verify → add +
      re-encrypt + clear → commit → push. The pre-flight sits between the
      unlock and the lock, which is why E1a must expose it as its own pass
      rather than burying it inside `AddRecipient`.
- [ ] Three commands registered in the `Recipients` group, both-mode
      availability. `recipient list`'s `Short` reworded to "List the
      keys this vault is encrypted to", against `identity list`'s "List
      the keys this machine holds for a vault" (reworded in E3) — they
      answer different questions and previously scanned as synonyms.
- [ ] Pruning wired into `AddRecipient`/`RemoveRecipient` as well as
      approve and deny — the "next recipient change" half of the TDD's
      pruning rule, which has no other home.
- [ ] Every error mapped to the exit code the TDD's "Library surface"
      table assigns it.
- [ ] Codes gathered from `--code` or, when absent, `Prompter.Value`,
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
