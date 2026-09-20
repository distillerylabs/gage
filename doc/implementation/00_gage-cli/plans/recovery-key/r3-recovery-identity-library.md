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

None open on entry. Both planned details were settled as written — the
commit message is `gage: recovery enroll <device>, retire
recovery-paper-key (reencrypt)`, and validation runs against the final
list. Four more were settled while implementing:

- **`RequireFullAccess` always runs inside the lock**, unlike
  `AddRecipient`, which runs it before the lock when the tree is clean and
  inside when it is dirty. `withVaultWrite` has just reset the tree, so one
  in-lock call reads exactly the state the re-encryption will read and
  there is no clean/dirty case to get wrong. The ordering property
  `AddRecipient`'s comment cares about is kept: it is still ahead of the
  first byte written and of every question asked.
- **Collisions are checked against the final list, not the current one.**
  Rebuilding a machine under the name it always had
  (`--replaces laptop-1` plus `--device laptop-1`) is the ordinary case,
  and a check against the current list would refuse it. `applySwap` is
  pure so every refusal happens before a byte is written.
- **A `--replaces` label that matches nobody is an error**, not a no-op, so
  a typo cannot silently leave the lost device a recipient.
- **`RecoverDevice` zeroes the caller's buffer; `VerifyRecoveryKey` does
  not.** The asymmetry is deliberate and now documented on both: verify
  returns immediately having compared a public key, while `RecoverDevice`
  holds the secret across a lock, a full re-encryption and a push. Callers
  that need the key afterwards must pass a copy — the interrupted-swap test
  does exactly that.

## Tasks

- [x] `(*Vault).recoveryIdentity(secret, p)`, unexported: parse the secret
      into a `memlock.Alloc`'d buffer, require its public key to be the
      recipient labelled `RecoveryDeviceLabel`, build an `Identity` with
      `device = RecoveryDeviceLabel` via `newIdentity`. Only the functions
      below call it, and each `Close`s it on return.
- [x] Shared unexported `swapRecipients`: under `withVaultWrite`,
      `requireRecipientsInSync`, `confirmRecipientTrust` (kept, D12),
      validate the final list (duplicate key, duplicate label, at least one
      recipient remains), `RequireFullAccess`, then `commitRecipientList`
      with a computed list and one commit message. It does not go through
      `RemoveRecipient`, so `confirmSelfRemoval` never fires.
- [x] `(*Vault).RecoverDevice(RecoverSpec, secret, p) (RecoverResult, error)`.
      `RecoverSpec{Device, Pubkey, Replaces, NewRecoveryPubkey}`. One
      commit: add `Device`; remove the used recovery recipient; add
      `NewRecoveryPubkey` as the new `recovery-paper-key` when non-empty;
      remove `Replaces` when set. Calls `syncOnUnlock` first, because this
      path skips `Unlock` and would otherwise re-encrypt on a stale tip.
- [x] Typed errors, each with a pinned exit code and a test:
      `ErrNotRecoveryKey` (a valid key that is not the recovery recipient;
      `LockedOrAuth`), `ErrLocalIdentityExists` (`Usage`); reuse
      `ErrRecipientExists`, `ErrNotARecipient`, `ErrMalformedRecoveryKey`.
- [x] Error text never contains the pasted key (age's parse errors can
      quote it; do not wrap them).

## Tests (write first)

- [x] `recoveryIdentity` accepts the recovery-labelled key and refuses a
      device key, a valid stranger, and garbage, with the pinned errors and
      exit codes.
- [x] No error, from any path, contains the input secret.
- [x] `RecoverDevice` yields exactly the expected recipient list, in
      `config.toml` and `.age-recipients`, in **one** new commit.
- [x] Every entry decrypts with the new device key and with the new
      recovery key; the old recovery key does not open ciphertext written
      after the swap.
- [x] `Replaces` removes the lost device's recipient in the same commit.
- [x] Refusals write nothing and leave HEAD untouched: duplicate key,
      duplicate label, `Replaces` naming an absent device or the recovery
      label, and a swap that would leave zero recipients.
- [x] A failure partway through leaves HEAD untouched (reuse the crash
      simulation pattern in `enrollapproveatomic_test.go`).
- [x] M10's confirmation still fires, and a declining prompter aborts with
      nothing written.
- [x] `updated_by` on every entry is unchanged.
- [x] The secret buffer is zeroed after the call returns, on success and on
      every failure.
- [x] `forbidigo` stays clean: nothing in `internal/gage` prints.

## Definition of done

Every item green on all three CI platforms, `make lint` and `make test`
clean, and no exported way to obtain a recovery-scoped `Identity`.

Met. 31 tests, written first. The enforcement claim holds structurally:
`recoveryIdentity` is unexported and `Identity`'s fields are unexported, so
outside `internal/gage` there is no way to obtain one.
