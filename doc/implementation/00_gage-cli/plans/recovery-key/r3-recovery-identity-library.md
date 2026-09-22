# R3 - Recovery identity: library

[<- R2](r2-verify-and-docs.md) - [plan index](index.md) - next: [R4 - `gage recovery enroll`](r4-recovery-enroll-cli.md)

> **Recommended model: Opus.** A new, security-critical path: a full-read credential is turned into an identity that must exist only briefly and only inside one function, and the change it makes must be atomic. The tests only prove what you thought to simulate, so the crash and failure cases need the stronger model's reasoning.

## Goal

The library half of "the recovery key enrolls a fresh device, then is
retired": a recovery-scoped identity that cannot escape `internal/gage`,
and one atomic operation that adds the new device, retires the used
recovery recipient, and optionally mints a replacement and removes the
lost device. Nothing here prints or prompts beyond the existing `Prompter`.

## Depends on

- **R2** - `RecoveryDeviceLabel`, `NewRecoveryKey`, `VerifyRecoveryKey`.
- Existing `newIdentity`, `parseIdentityFile`, `commitRecipientList`,
  `requireRecipientsInSync`, `confirmRecipientTrust`, `RequireFullAccess`,
  `withVaultWrite`, `syncOnUnlock`, `memlock.Alloc`.

## Design references

- [index.md](index.md) decisions D8-D12.
- ["Recovery key at `init`"](../../tdds/gage-cli-design.md) - the limit this closes.
- The enrollment approval path (`internal/gage/enrollapprove.go`) - the
  precedent for a bespoke operation built on `commitRecipientList`.

## Decisions to make first

None open. Two details to settle in the milestone rather than leave to the
code:

- **Commit message.** One message naming the device added and the recovery
  key retired, in the style of the existing recipient commits, which name
  devices because a recipient commit's own diff is plaintext anyway.
- **Order inside the lock.** Validate against the *final* recipient list
  before writing anything, so a refusal leaves the tree untouched.

## Tasks

- [ ] `(*Vault).recoveryIdentity(secret, p)`, unexported: parse the secret
      into a `memlock.Alloc`'d buffer, require its public key to be the
      recipient labelled `RecoveryDeviceLabel`, build an `Identity` with
      `device = RecoveryDeviceLabel` via `newIdentity`. Only the functions
      below call it, and each `Close`s it on return.
- [ ] Shared unexported `swapRecipients`: under `withVaultWrite`,
      `requireRecipientsInSync`, `confirmRecipientTrust` (kept, D12),
      validate the final list (duplicate key, duplicate label, at least one
      recipient remains), `RequireFullAccess`, then `commitRecipientList`
      with a computed list and one commit message. It does not go through
      `RemoveRecipient`, so `confirmSelfRemoval` never fires.
- [ ] `(*Vault).RecoverDevice(RecoverSpec, secret, p) (RecoverResult, error)`.
      `RecoverSpec{Device, Pubkey, Replaces, NewRecoveryPubkey}`. One
      commit: add `Device`; remove the used recovery recipient; add
      `NewRecoveryPubkey` as the new `recovery-paper-key` when non-empty;
      remove `Replaces` when set. Calls `syncOnUnlock` first, because this
      path skips `Unlock` and would otherwise re-encrypt on a stale tip.
- [ ] Typed errors, each with a pinned exit code and a test:
      `ErrNotRecoveryKey` (a valid key that is not the recovery recipient;
      `LockedOrAuth`), `ErrLocalIdentityExists` (`Usage`); reuse
      `ErrRecipientExists`, `ErrNotARecipient`, `ErrMalformedRecoveryKey`.
- [ ] Error text never contains the pasted key (age's parse errors can
      quote it; do not wrap them).

## Tests (write first)

- [ ] `recoveryIdentity` accepts the recovery-labelled key and refuses a
      device key, a valid stranger, and garbage, with the pinned errors and
      exit codes.
- [ ] No error, from any path, contains the input secret.
- [ ] `RecoverDevice` yields exactly the expected recipient list, in
      `config.toml` and `.age-recipients`, in **one** new commit.
- [ ] Every entry decrypts with the new device key and with the new
      recovery key; the old recovery key does not open ciphertext written
      after the swap.
- [ ] `Replaces` removes the lost device's recipient in the same commit.
- [ ] Refusals write nothing and leave HEAD untouched: duplicate key,
      duplicate label, `Replaces` naming an absent device or the recovery
      label, and a swap that would leave zero recipients.
- [ ] A failure partway through leaves HEAD untouched (reuse the crash
      simulation pattern in `enrollapproveatomic_test.go`).
- [ ] M10's confirmation still fires, and a declining prompter aborts with
      nothing written.
- [ ] `updated_by` on every entry is unchanged.
- [ ] The secret buffer is zeroed after the call returns, on success and on
      every failure.
- [ ] `forbidigo` stays clean: nothing in `internal/gage` prints.

## Definition of done

Every item green on all three CI platforms, `make lint` and `make test`
clean, and no exported way to obtain a recovery-scoped `Identity`.
