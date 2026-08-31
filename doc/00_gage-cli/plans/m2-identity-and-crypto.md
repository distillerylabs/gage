# M2 — Identity & memory protection

[← M1](m1-vault-lifecycle.md) · [plan index](index.md) · next: [M3 — Entry format](m3-entry-format.md)

## Goal

The first milestone where any key material exists. Three things, in
dependency order:

1. **A thin age wrapper** — encrypt bytes to a set of recipient public
   keys, decrypt bytes with an identity. No entry format, no files under
   `entries/` — just bytes in, bytes out. This is the riskiest technical
   piece in the whole build, and it's proven here in isolation before
   anything is layered on it.
2. **Passphrase-method identity generation and unlock** — a fresh X25519
   keypair, private half wrapped via age's scrypt passphrase recipient and
   written to `$GAGE_DATA/identities/<vault>/<device>.age`, with only the
   public key going into `.age-recipients`/`config.toml`.
   `Vault.Unlock(Prompter) (Identity, error)` decrypts it back.
3. **Cross-platform memory protection** — page-locking and core-dump
   suppression on Linux, macOS, and Windows, established here because
   this is the first point a private key exists in process memory.

This milestone also completes `gage init`: the default path now generates
a local identity, so M1's required `--recipient` becomes optional and
purely additive (a recovery key alongside the device's own).

## Depends on

- **M0** — `Prompter`, the `Identity`/`Vault.Unlock` skeleton, the PTY
  test helper.
- **M1** — vault structure, `.age-recipients`/`config.toml` writers, the
  global config `device` field, recipient key validation.

## Design references

- ["Local identity storage"](../tdds/gage-cli-design.md) — why the file
  lives under `$GAGE_DATA` and nowhere else, and why losing it is a
  recovery problem rather than a backup problem
- ["Session model"](../tdds/gage-cli-design.md) — page-locking,
  core-dump suppression, and the honest Windows gap
- ["Library architecture"](../tdds/gage-cli-design.md) — the
  method-agnostic `Prompter.Unlock` exchange and typed unlock errors

## Decisions to make first

- **Retry policy on a wrong passphrase.** The design is explicit that
  the library returns a typed error and `cmd/gage` owns retry policy —
  so decide the CLI's policy here: how many attempts, and does a retry
  re-enter through `Prompter` or loop inside one call?
- **scrypt work factor.** age's default vs. something higher. This is
  the only thing standing between a stolen identity file and an offline
  brute force; worth a deliberate number rather than the default by
  omission.

## Tests (write first)

**age wrapper**

- [ ] Encrypt → decrypt round-trip recovers a byte-identical payload
- [ ] A payload encrypted to N recipients is independently decryptable by
      each recipient's private key
- [ ] Decrypting with a key that is *not* a recipient fails with a clear
      typed error, not a panic or silent garbage output
- [ ] Corrupted/truncated ciphertext fails decryption cleanly
- [ ] Encrypting to zero recipients is refused rather than producing a
      file nobody can read
- [ ] Attempting to combine a scrypt passphrase recipient with any other
      recipient is refused — the invariant the identity file relies on
      (see [A3](open-questions.md))

**Identity generation & unlock**

- [ ] `gage init` (no `--recipient`) now succeeds, generating the
      device's identity and writing its public key to both recipient
      files — replaces M1's "fails without `--recipient`" test
- [ ] `gage init --recipient <extra>` writes both the generated device
      key and the extra key, in that order
- [ ] `gage init` writes the device's wrapped identity file to
      `$GAGE_DATA/identities/<vault>/<device>.age`, directory `0700` and
      file `0600`
- [ ] The public key derived from the generated identity matches exactly
      what's written to both `.age-recipients` and `.gage/config.toml`'s
      `[[recipients]]`
- [ ] `gage init` records this device's `device` and `method` under
      `[vaults.<name>]` in global config, and `Unlock` resolves the
      identity file from the former
- [ ] `gage init --device NAME` uses that name for the identity file
      path, the `[[recipients]].device` label, and the global config
      record — all three agree
- [ ] **No path is constructed from an unvalidated device name**: a
      global config or vault config carrying a traversal-style `device`
      value causes `Unlock` to fail with a typed error rather than
      reading or writing outside `$GAGE_DATA/identities/<vault>/`
      (Q-DEVICE-NAME)
- [ ] `Unlock` dispatches on **this device's** locally-recorded method,
      not on the vault's `[method].default` — the two can differ by
      design (Q-METHOD-SCOPE), and reading the vault's default would be
      the wrong source even though today both are `passphrase` and the
      bug would be invisible
- [ ] `Vault.Unlock` with the correct passphrase returns an `Identity`
      that decrypts a payload encrypted to its public key via the wrapper
      above
- [ ] `Vault.Unlock` with the wrong passphrase returns a distinguishable
      typed error (not a generic error) — no partial or garbage key is
      ever produced
- [ ] `Vault.Unlock` returns distinguishable typed errors for a corrupt
      identity file and for no local identity registered for this device,
      each different from the wrong-passphrase error
- [ ] `Unlock` requests the passphrase through `Prompter` with
      `Kind: "passphrase"` — never a direct stdin read from the library
- [ ] The CLI's real `x/term`-backed prompter reads a passphrase without
      echoing it, driven through a `creack/pty` pseudoterminal (one of
      the few tests that must use a real terminal)
- [ ] `Identity.Close()` releases the page lock and zeroes the key
      material; using the `Identity` after `Close()` fails instead of
      silently succeeding
- [ ] `Close()` is idempotent — calling it twice doesn't panic or
      double-unlock a page

**Memory protection**

- [ ] The `memlock` package's `Lock`/`Unlock` round-trip succeeds on the
      current platform (Linux/macOS/Windows, whichever CI runs on) for a
      representative key-sized byte slice
- [ ] A page-lock failure injected via a fake `Locker` doesn't abort
      `Vault.Unlock` — it proceeds and returns a usable `Identity` after a
      single warning via `Prompter`, rather than refusing to unlock
- [ ] The page-lock-failure warning fires exactly once per unlock, not
      once per key or once per call
- [ ] Core-dump suppression is applied at `cmd/gage` startup and is
      observable on the current platform (`RLIMIT_CORE` readable as 0 on
      Linux/macOS; `SetErrorMode` reflected on Windows)

## Implementation

- [ ] Thin age wrapper in `internal/gage`: encrypt a byte slice to a list
      of parsed recipients, decrypt a byte slice with an `Identity`.
      Typed errors for not-a-recipient and corrupt ciphertext. No file
      handling, no entry format — those are M3's
- [ ] Passphrase-method identity generation: fresh X25519 keypair,
      private half wrapped via age's scrypt passphrase recipient at the
      chosen work factor, written to
      `$GAGE_DATA/identities/<vault>/<device>.age`
- [ ] Device-name and per-device-method resolution and recording, per
      Q-DEVICE-NAME — written to local state, never to the vault's
      committed config. `Vault.Unlock` reads the method from here, which
      is what makes a second method additive later rather than a change
      to how unlocking finds its method at all
- [ ] `Vault.Unlock(Prompter) (Identity, error)`: decrypts the wrapped
      identity file back into a usable private key via the typed unlock
      exchange; returns distinguishable typed errors (wrong passphrase /
      corrupt identity file / no local identity registered for this
      device); the only place a `Vault` ever obtains an identity. The
      returned `Identity`'s key bytes are page-locked immediately; if
      locking fails, `Unlock` warns once via `Prompter` and proceeds
      unlocked-but-unprotected rather than failing the whole operation
- [ ] `cmd/gage`'s wrong-passphrase retry policy, per the decision above —
      in the CLI layer, never in the library
- [ ] `internal/gage/memlock`: `Lock([]byte) error`/`Unlock([]byte) error`,
      build-tag-separated — `mlock(2)`/`munlock(2)` on Linux and macOS
      (`golang.org/x/sys/unix`), `VirtualLock`/`VirtualUnlock` on Windows
      (`golang.org/x/sys/windows`) — behind one signature `Vault` and
      `Session` both call unchanged regardless of OS
- [ ] `Locker` interface between `Vault.Unlock`/`Identity.Close` and the
      `memlock` package — the real `memlock`-backed implementation in
      production and in every realistic test, a fake returning a
      deterministic failure in the one page-lock-failure test; the same
      injectable-seam pattern M8 uses for `RemoteSyncer`
- [ ] `Identity.Close()`: releases the page lock and zeroes the private
      key, cross-platform, via the same `memlock` package; idempotent
- [ ] Process-wide core dump disabling at `cmd/gage` startup:
      `setrlimit(RLIMIT_CORE, 0)` on Linux/macOS; on Windows, suppress the
      Windows Error Reporting crash dialog via `SetErrorMode` (a narrower
      guarantee than POSIX's, documented as such rather than claimed as
      parity)
- [ ] `gage init` completed: default path generates the local identity;
      `--recipient` becomes optional and additive

## Definition of done

Full test list green on all three CI platforms. A vault can be created
with a real identity, and that identity can be unlocked and used to
decrypt something encrypted to it — but there are still no entries.

## Affects later milestones

- The age wrapper is what M3's entry layer, M9's `--reencrypt`, and M11's
  cross-vault encrypt all call. It should stay bytes-in/bytes-out; no
  entry semantics leak into it.
- `Identity.Close()`'s contract (releases the lock, zeroes, idempotent) is
  what M4's one-shot handler and M6's `Lock`/idle-timeout both invoke.
  Nothing after this milestone adds new memory-protection mechanics —
  M6 only changes how long an already-protected `Identity` survives.
- Typed unlock errors are what M8's `clone` uses to say "this device
  isn't a recipient yet" plainly.
