# R1 — Recovery key: `gage init` integration

[← R0](r0-library.md) · [plan index](index.md) · next: [R2 — `recovery verify` and docs](r2-verify-and-docs.md)

> **Recommended model: Opus.** This is the security-sensitive milestone: ordering (never show before the vault is committed, never leak on a failure path), secret hygiene across stdout, stderr, disk and git objects, and non-terminal edge cases. Mistakes here are quiet, so the stronger model is worth it. Follow with a focused secret-handling review before merging.

## Goal

`gage init` generates the recovery key by default, registers it as
`recovery-paper-key`, and shows the private key exactly once after the
vault exists, behind a confirmation, with plain warnings about custody.
The key is never stored.

## Depends on

- **R0** — `NewRecoveryKey`, `ExtraRecipients`, `RecoveryDeviceLabel`.
- `renderQR`, `Prompter.Value`, `atomicfile`, `zero`.

## Design references

- [index.md](index.md) decisions D1, D2, D3, D5, D7.
- [Design doc, `gage init`](../../tdds/gage-cli-design.md) — the flag surface
  this extends.

## Decisions to make first

None open. The wording of the warning text and the value of N for the
re-type confirmation are implementation details; pin them in tests.

## Tasks

- [ ] `runInit`: generate the key after the device identity, pass it via
      `ExtraRecipients`, call `Create`, roll back only the identity on
      failure (the recovery key needs no rollback and has not been shown).
- [ ] `--no-recovery-key` opts out; no key generated, nothing shown.
- [ ] `--recovery-key-out FILE`: write the secret to a `0600` file via
      `atomicfile` instead of the terminal; nothing secret on stdout.
- [ ] Fail-closed rule (D2): no terminal and neither flag given is a
      Usage error raised before anything is created.
- [ ] `showRecoveryKey` in new `cmd/gage/recoverykey.go`, called only
      after `Create` succeeds: key as text plus QR (`renderQR`) on stderr;
      warning that the key is unencrypted, grants full vault access to
      anyone holding it, cannot be shown again, and must be stored
      offline (paper or offline encrypted drive; never cloud-synced
      notes, password managers, email or photos; trusted printer or
      handwrite; clear scrollback afterwards).
- [ ] Confirmation (D3): re-type the last N characters via masked
      `Prompter.Value`; a wrong or declined answer is handled per the
      test below.
- [ ] Zero the secret bytes on every exit path (`defer`).
- [ ] Output gains one line naming the recovery recipient's public key.
- [ ] Reject `--device recovery-paper-key` before any file is written.
- [ ] Update `--recipient` help text so it no longer presents itself as
      the recovery mechanism; update Long help for the new default.

## Tests (write first, in-process via `runCLIWithPrompter` and `isolateXDG`)

- [ ] Default `init` yields two recipients, `<device>` and
      `recovery-paper-key`, in `config.toml` and `.age-recipients`.
- [ ] An entry encrypted at init time decrypts with the recovery secret
      alone.
- [ ] Hygiene: after `init`, walk `XDG_DATA_HOME`, the vault directory and
      `.git` objects; the secret string appears nowhere. It appears
      exactly once across stdout+stderr.
- [ ] Warning text (unencrypted, full access, offline storage) appears
      alongside the key.
- [ ] Failing `Create` shows no key and leaves no identity or vault
      (extends `TestInitLeavesNoVaultBehindWhenCreateFails`).
- [ ] `--no-recovery-key`: one recipient, no key output.
- [ ] `--device recovery-paper-key` rejected, exit code pinned, no files
      written.
- [ ] No terminal and no flags: fails closed before creating anything,
      exit code pinned.
- [ ] No terminal with `--recovery-key-out`: file is `0600`, contains the
      key, and stdout contains no secret.
- [ ] Confirmation: correct re-type completes init; wrong re-type is
      handled as specified (the vault already exists at that point, so
      the behavior and its message are pinned in the test) and the key is
      never re-displayed.
- [ ] Existing `init` tests unchanged apart from stating their recovery
      flag or terminal fixture.

## Definition of done

Every item green on all three CI platforms, `make lint` and `make test`
clean, plus a focused review of secret handling (stdout/stderr/disk/git,
failure paths, zeroing).
