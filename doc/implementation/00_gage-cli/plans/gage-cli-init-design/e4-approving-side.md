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

- **E3** — requests exist to approve, and its `Enroll` builds the test
  fixtures.
- **E1** — approval always re-encrypts and reuses
  `ErrCannotGrantFullAccess`.
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

Settled. Three that shape the structure more than they look like they
do:

- **The unlock comes after the confirmation.** Opening a request needs
  no identity, so a wrong code, an expired request, or a declined prompt
  must cost no unlock. This means `approve` **cannot** be a blanket
  `withUnlockedVault` wrapper the way `recipient add` is — it unlocks in
  the middle. That is the one place this feature departs from an
  existing command's shape, and it departs toward `sync`'s lazy unlock.
- **A device name is a label; the pubkey is the identity.** Relabeling
  on approval is legitimate and changes nothing about what was
  authenticated.
- **Two `[y/N]` questions can appear**, and must not be collapsed: "let
  this device in" and M10's "do you trust the list you're about to
  encrypt to" are different questions.

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
      `ErrCannotGrantFullAccess` before the lock and before any
      confirmation** — the error names the count of unreadable entries,
      never a bare decryption failure on a UUID.
- [ ] `deny` removes the file, grants nothing, and needs no code.
- [ ] **`deny` warns when its push fails** rather than reporting plain
      success — injected fake `RemoteSyncer`, and assert the warning
      reached the `Prompter`. A silent failed deny leaves the request
      live for every other device.

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

**Collisions and duplicates**

- [ ] A request whose device name became taken between enroll and
      approve is refused with `ErrEnrollmentNameTaken` **before the
      confirmation and before any unlock**.
- [ ] `approve --device <other-name>` applies that same request,
      recording the approver's label. The recipient's pubkey is the
      sealed one, unchanged — relabeling changes the name and nothing
      else.
- [ ] `--device` with a run resolving to more than one request is a
      usage error naming the ID form; with exactly one request it
      applies.
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

- [ ] `PendingEnrollments()` and `OpenEnrollment(codes)` taking **no
      `Identity`** — that is what makes the late unlock possible, and it
      is a contract, not an accident.
- [ ] `ApproveEnrollments(approvals []Approval, ident *Identity)
      (ApprovalResult, error)` with `Approval{Request, Label}` and
      per-request `ApprovalOutcome`. `RecipientChange` is deliberately
      *not* reused — it names one device, and a batch resolves N.
- [ ] `DenyEnrollment(id string, p Prompter) error` — takes a
      `Prompter` despite holding no `Identity`; the TDD records why that
      is a deliberate exception rather than an erosion of the
      interaction-rides-the-Identity rule.
- [ ] `cmd/gage` orders it: open → collision check → render → confirm →
      **unlock** → lock → re-verify → add + re-encrypt + clear → commit
      → push.
- [ ] Three commands registered in the `Recipients` group, both-mode
      availability. `recipient list`'s `Short` reworded to "List the
      keys this vault is encrypted to", against `identity list`'s "List
      the keys this machine holds for a vault" — they answer different
      questions and previously scanned as synonyms.
- [ ] Codes gathered from `--code` or, when absent, `Prompter.Value`,
      with the retry loop around `ErrEnrollmentCodeWrong` owned by
      `cmd/gage`. **No `Prompter` change**, and no new `UnlockKind`.

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
