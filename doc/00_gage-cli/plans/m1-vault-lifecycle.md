# M1 — Vault lifecycle & config

[← M0](m0-scaffolding.md) · [plan index](index.md) · next: [M2 — Identity & memory protection](m2-identity-and-crypto.md)

## Goal

A vault's on-disk structure and the global vault registry — **with no
crypto anywhere in this milestone**. `gage init` creates the directory,
writes `.gage/config.toml` (with `type = "git"` and `format_version`),
writes `.age-recipients` from public keys handed to it as plain strings,
git-inits with one commit, and registers the vault in global config.
Alongside it: `vault list/info/remove/set-default` and
`git set-remote`.

**Why no crypto here.** Writing an `age1...` public key into a file needs
no cryptography, and keeping key material out of this milestone is what
lets M2 genuinely prove crypto correctness in isolation the way the plan
claims. The practical consequence: `gage init` in M1 **requires**
`--recipient PUBKEY` (there's no local identity to generate one from
yet), and M2 is what makes that flag optional. This also forces
`--recipient` to be implemented properly rather than being the
afterthought it was in the original draft — it's the flag the design doc's
whole recovery story (`recovery-paper-key`) depends on.

One vault type only (`git` — the only type that exists) and one method
only (`passphrase`), both validated against single-element allowlists so
a second value is additive later. Defer `clone` to M8, once a
bare-remote test harness exists to clone *from*.

## Depends on

- **M0** — XDG paths, global config read/write, atomic TOML writes,
  Cobra skeleton, exit-code taxonomy, `gittest.NewBareRemote`.

## Design references

- ["Vault types"](../tdds/gage-cli-design.md) — why `type` is a
  first-class field and why `--type` exists with one accepted value
- ["On-disk layout"](../tdds/gage-cli-design.md) — what lives where and
  why `.age-recipients` stays flat while `config.toml` nests
- ["Global config"](../tdds/gage-cli-design.md) — the registry schema and
  `[vaults.<name>.<type>]` namespacing
- ["Git-specific commands"](../tdds/gage-cli-design.md) — why
  `set-remote` is namespaced under `git` and why there's no passthrough

## Decisions to make first

- **[Q-DEVICE-NAME](open-questions.md)** — the global config schema needs
  per-vault `device` *and* `method` fields if that's the answer. Both are
  local-only by Q-METHOD-SCOPE: a device's actual method is an identity
  concern and is never committed to the vault, so it can't live in
  `.gage/config.toml` alongside `[method].default`. M1 defines the
  schema; M2 populates both fields. Decide before writing the schema,
  not after.
- **`gage init` into an already-registered name.** Reject outright, or
  allow with `--force`? Untested and unspecified today.
- **`gage vault info` with no argument** — the design shows `[<name>]` as
  optional, presumably meaning `current`. Confirm.

## Tests (write first)

- [ ] `gage init <name> --recipient <pubkey>` creates `.gage/config.toml`,
      `.age-recipients`, `entries/`, `.gitignore`, and a git repo with
      exactly one commit
- [ ] Without `--dir`, the vault is created at `$GAGE_DATA/vaults/<name>`
      and registered under exactly that path in global config
- [ ] With `--dir PATH`, the vault is created at `PATH` and registered
      under it
- [ ] The generated `.gitignore` contains the standard OS-cruft patterns
      (`.DS_Store`, `Thumbs.db`) and nothing `gage`-state-specific —
      there's nothing else to ignore, since the trust cache and identity
      files live outside the vault by design
- [ ] `gage init` into a non-empty, non-gage directory fails cleanly
      without touching existing files
- [ ] `gage init` with a name already registered in global config behaves
      per the decision above, and either way leaves the existing vault's
      files and registration untouched
- [ ] `.gage/config.toml` round-trip: `[vault]` (including `type = "git"`
      and `format_version`) + `[method].default` + `[[recipients]]` fields
      survive write/parse
- [ ] `[[recipients]]` entries carry only `device` and `pubkey` — no
      per-device `method` field leaks into the committed vault config,
      which would tell anyone with read access which recipient is the
      softest target (Q-METHOD-SCOPE)
- [ ] A `.gage/config.toml` carrying an unrecognized `format_version`
      is refused cleanly with an "upgrade gage" error, before any other
      field is acted on — never best-effort parsed
- [ ] `gage init` with no `--type` flag defaults to `type = "git"`
- [ ] `gage init --type git` succeeds and is equivalent to omitting the flag
- [ ] `gage init --type <anything-else>` fails with a usage error before
      creating any files, and lists `git` as the only accepted value
- [ ] `gage init` with no `--method` flag defaults to
      `[method].default = "passphrase"` in `.gage/config.toml` — a
      default for devices joining, not a vault-wide constraint
      (Q-METHOD-SCOPE)
- [ ] `gage init --method passphrase` succeeds and is equivalent to
      omitting the flag
- [ ] `gage init --method <anything-else>` fails with a usage error before
      creating any files, and lists `passphrase` as the only accepted
      value — including for the method names the design doc mentions as
      future work (`ssh`, `yubikey`, `age-key`, `secure-enclave`,
      `plugin:*`), which must fail like any other unknown value rather
      than being silently accepted and written to config
- [ ] `gage init` with no `--recipient` fails with a clear error naming
      the flag — in M1 there's no identity generation to supply a key
      (M2 changes this test)
- [ ] `gage init --recipient` repeated N times writes all N keys
- [ ] `gage init --recipient <malformed>` fails with a usage error before
      creating any files
- [ ] `.age-recipients` round-trip: one public key per line, no gage-only
      framing, so a stock `age`/`passage` CLI could use it as-is
- [ ] The keys written to `.age-recipients` match `.gage/config.toml`'s
      `[[recipients]]` exactly
- [ ] `gage vault list` includes a freshly-`init`'d vault
- [ ] `gage vault info <name>` reports the correct type, method, recipient
      count, and (for `git`) remote + clean/dirty state
- [ ] `gage vault info` with no argument reports on `current`
- [ ] `gage vault remove <name>` drops it from global config but leaves
      the underlying git repo and its files on disk untouched
- [ ] `gage vault set-default <name>` updates `current` in global config
- [ ] `gage init` without `--remote` succeeds and leaves the vault
      remote-less (no `origin`, no `[vaults.<name>.git]` table in global
      config)
- [ ] `gage git set-remote <name> <url>` sets `origin` on the actual git
      repo (verified by reading git config back via go-git) *and* updates
      `vaults.<name>.git.origin` in global config in the same call
- [ ] `gage git set-remote <name> <new-url>` on a vault that already has
      an `origin` changes it (equivalent to `set-url`), and `vault info`
      reflects the new URL afterward
- [ ] `gage git set-remote` invoked without both `<name>` and `<url>`
      fails with a usage error and makes no change to the repo or global
      config

## Implementation

- [ ] `Vault.Create(spec)` in the library: writes the full on-disk
      skeleton given a name, type, method, path, and a list of recipient
      public keys as strings; go-git `PlainInit` + initial commit. Takes
      no identity and performs no encryption
- [ ] `gage init <name> [--dir PATH] [--type git] [--method passphrase]
      [--recipient PUBKEY ...] [--remote URL]` as a thin wiring layer
      over `Vault.Create`. `--type` and `--method` get identical
      treatment: optional, defaulting to the single value that exists
      today (`git` / `passphrase`), validated against a single-element
      allowlist before any file is touched, so a second value later is
      additive to the CLI surface rather than a breaking change
      (Q-METHOD-FLAG)
- [ ] `.gage/config.toml` read/write (`[vault]` incl. `type` and
      `format_version`, `[method]`, `[[recipients]]`), through M0's
      atomic-write helper
- [ ] `format_version` written on create and strictly validated on every
      read — an unrecognized version is a typed refusal, not a
      best-effort parse
- [ ] `.age-recipients` read/write — one key per line, no framing
- [ ] Recipient public key parsing/validation (well-formed `age1...`
      bech32), so a malformed `--recipient` fails at the CLI boundary
      rather than at first encrypt in M3
- [ ] Global config registry: register on `init`, deregister on `vault
      remove`, `current` on `set-default`; schema includes the per-vault
      `device` field (populated in M2)
- [ ] `gage vault list/info/remove/set-default`
- [ ] `gage git set-remote` (go-git set/update `origin`, synced into
      `vaults.<name>.git.origin` in global config in the same call)

## Definition of done

Full test list green on all three CI platforms. You can create, list,
inspect, re-point, and forget vaults. Nothing in them is encrypted yet,
and there are no entries.

## Affects later milestones

- **M2** completes `gage init`: the default path generates a local
  identity and supplies its public key, making `--recipient` optional and
  additive rather than required. The `gage init` with no `--recipient`
  test above is rewritten there.
- The `.gage/config.toml` schema and its `format_version` enforcement are
  fixed here; every later reader depends on both.
- The global config schema, including `device`, is fixed here.
- M9's `recipient add/remove` writes through the same `.age-recipients`
  and `config.toml` writers built here.
