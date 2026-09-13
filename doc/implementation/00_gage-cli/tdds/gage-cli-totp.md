# TOTP — storing and generating one-time authenticator codes

*Status: **design settled; not implemented.** An enhancement to
[gage-cli-design.md](gage-cli-design.md), not a replacement. Everything
that document says about vaults, entries, and the library/CLI split still
holds; this adds one reserved field convention and two commands on top of
it. No implementation plan exists yet — this gets scheduled into a
milestone (or its own `plans/gage-cli-totp/`) when work begins.*

---

## The problem

`gage` already stores exactly the kind of secret a TOTP shared secret is:
short, sensitive, associated with one login. Today a user who wants both
a password and its 2FA code in one place still needs a separate
authenticator app — `gage` has no way to compute a code from a stored
secret. This document adds that, as a small extension of the existing
`Entry` model rather than a new kind of object.

---

## Decisions

### `[x]` D-TOTP-STORAGE — the secret lives in `Entry.Fields`, not a new type

**Resolved: a reserved field key, `fields["totp"]`, on the existing
`Entry`. No new struct, no schema change.**

`Entry.Fields` (`internal/gage/entry.go`) is already a flat
`map[string]string`, encrypted exactly like `Value` and every other
field — same trust boundary, same at-rest guarantee, no new crypto
surface. Nothing in the codebase currently treats any field name
specially (`internal/gage/field.go`'s `Field(name)` is a generic
untyped lookup), so this is the first reserved field-name convention —
a small, additive precedent rather than a rework, and one later features
(e.g. a `notes` or `expiry` convention) could follow the same shape.

The alternative — a new `TOTP *TOTPParams` field on `Entry` — was
rejected because it changes the wire format (`marshalEntryNodes` and
`verifyFaithful` both hand-maintain field order and round-trip checks;
see `entry.go`) for a feature that doesn't need a new type, only a new
string convention.

### `[x]` D-TOTP-INPUT-FORMAT — accept either a bare secret or an `otpauth://` URI

**Resolved: `fields["totp"]` holds either a bare base32 secret (assumed
SHA1 / 30s period / 6 digits — the near-universal default) or a full
`otpauth://totp/...` URI (for the minority of services that vary
digits, period, or algorithm, e.g. Steam-style or SHA256 enterprise
setups). Detected by prefix at parse time.**

A bare secret is what most services show you (or what's in the
provisioning QR's `secret=` parameter) — supporting it directly means no
URI construction for the common case. The URI form is accepted so the
rare non-default case isn't unsupported outright; a user who scans or
copies a full `otpauth://` URI can paste it as-is rather than having to
extract just the secret and lose the non-default parameters.

Input is normalized before storage: whitespace stripped, case-folded to
upper for a bare secret (base32 is case-insensitive). Validated by
`ParseTOTP` at write time (see D-TOTP-CLI) so a malformed secret is
rejected before it's ever committed, not discovered later at `gage totp`
time.

**HOTP (counter-based, non-time codes) is out of scope.** It needs a
persisted, mutating counter — `gage` entries have no field of that kind
today, and HOTP is a small minority of real-world 2FA. Deferred rather
than designed around; nothing here forecloses adding it as a second
`fields["hotp"]` convention later.

### `[x]` D-TOTP-LIBRARY — pure stdlib, no new dependency, no clock seam

**Resolved: a new file, `internal/gage/totp.go`, using only
`crypto/hmac`, `crypto/sha1`/`sha256`/`sha512`, and `encoding/base32` —
the same posture the codebase already takes in `enrollcode.go`, which
hand-rolls Crockford base32 rather than importing an encoding library.**

```go
type TOTPParams struct {
    Secret    []byte
    Digits    int
    Period    time.Duration
    Algorithm func() hash.Hash
}

func ParseTOTP(raw string) (TOTPParams, error)
func (p TOTPParams) Code(at time.Time) (string, error)
```

`Code` takes `time.Time` explicitly rather than calling `time.Now()`
internally. This is deliberate and buys two things: it can be tested
directly against RFC 6238's published test vectors with no seam,
fake, or mock needed (unlike `Prompter`/`Locker`/`RemoteSyncer`, which
exist because their failure modes are otherwise unreachable in a test —
here the "failure mode" is just a fixed point in time, which a plain
argument already gives for free); and `cmd/gage` — not the library —
decides what "now" means, which is the same split the rest of the
codebase draws between library logic and CLI-level decisions.

A `pquerna/otp`-style third-party dependency was considered and
rejected: RFC 6238 is roughly thirty lines of HMAC-and-truncate over
stdlib primitives, and the project already prefers hand-rolling small,
auditable encodings over adding a dependency for them.

### `[x]` D-TOTP-CLI — `insert --totp`, and a new `gage totp <query>` command

**Resolved: a `--totp <secret-or-uri>` flag on `insert`, validated via
`ParseTOTP` before the entry is written; a new `gage totp <query>`
command that resolves an entry, reads its `totp` field, computes the
code for `time.Now()`, and either prints it or copies it with `-c`.**

`insert --totp` mirrors the existing `--description` flag rather than
requiring the `-e/--edit` YAML-editor path (today the *only* way to set
an arbitrary field, per `runInsertEdit` in `cmd/gage/entry.go`) — a
dedicated flag is worth adding here because TOTP is a named, validated
concept, not an arbitrary field a user is expected to hand-edit.

`gage totp <query>` follows the same resolve → decrypt → emit shape as
`gage show` (`cmd/gage/entry.go`): same query resolution
(title-first, per the design doc's "Addressing entries"), same
`withUnlockedVault` path, same `-c/--clip` flag routed through the
existing `clipboardKeeper` (`cmd/gage/clipboard.go`). Reusing the
clipboard path is a good fit specifically for this feature: a TOTP code
is meaningfully short-lived (30s by default), and `clipboardKeeper`
already auto-clears on a timer and verifies its own digest before
wiping — behavior built for exactly this kind of secret, not added for
it.

An entry with no `totp` field returns a typed error (`ErrNoTOTPField`)
from the library, distinguishable from a resolution failure, so
`cmd/gage` can say "this entry has no TOTP secret" rather than "no such
entry."

**Left out of `gage totp` for now:** a live-updating countdown display
(re-rendering the code as it approaches expiry, the way some
authenticator apps do). It's pure `cmd/gage`-side polish with no library
implications, and can be added later without touching anything decided
here.

### `[x]` D-TOTP-REGISTRY — no new group, no REPL-specific wiring

**Resolved: `totp` is registered in `cmd/gage/registry.go` under
`Group: GroupEntry`, `Availability: AvailBoth`, next to `show`/`cat`.**

It needs nothing beyond the registry entry. `runSessionCommand`
(`cmd/gage/repl.go`) only hand-wires the small set of meta-verbs that
have no Cobra command at all (`use`, `lock`, `status`, `help`); every
real command — `totp` included — reaches the session through the same
shared `NewRootCmd(app)` tree that one-shot mode uses.

---

## Testing

Per the project's convention (tests before implementation, at task
granularity):

1. **`ParseTOTP` / `Code` against RFC 6238's official test vectors** —
   SHA1, SHA256, and SHA512, at the RFC's published timestamps. No
   fakes needed, per D-TOTP-LIBRARY.
2. **`ParseTOTP` rejecting malformed input** — bad base32, an
   `otpauth://` URI missing a secret, an unsupported algorithm name —
   each as its own case, since these are the errors `insert --totp`
   surfaces at write time.
3. **`insert --totp` end-to-end**, in-process via `rootCmd.Execute()`:
   a valid secret is stored and round-trips through `fields["totp"]`;
   an invalid one is rejected and nothing is written.
4. **`gage totp <query>` end-to-end**: insert an entry with a known
   secret, run `totp`, and compare its output against `Code()` computed
   independently in the test at the time the command actually ran —
   not a hardcoded expected string, since the point of this test is
   proving the command's wiring (resolution, decryption, field lookup),
   not re-proving TOTP correctness already covered by (1).
5. **`gage totp` on an entry with no `totp` field** returns
   `ErrNoTOTPField`, surfaced as a distinct CLI message from "no such
   entry."

---

## Deliberately out of scope

- **HOTP** (counter-based codes) — see D-TOTP-INPUT-FORMAT.
- **QR-code provisioning** (scanning a service's enrollment QR directly
  into `gage`) — `rsc.io/qr` is already a dependency and used for
  *displaying* QR codes (`show -q`), but *reading* one would need a
  camera/image-decode path this CLI has no use for elsewhere. A user
  provisions by pasting the secret or `otpauth://` URI shown alongside
  the QR, which every service already provides as text.
- **A live-updating countdown** in `gage totp`'s output — see
  D-TOTP-CLI.
- **Bulk/entry-list TOTP status** (e.g. flagging which entries have a
  TOTP field in `gage ls`) — no current command surfaces per-entry
  field presence, and adding one is unrelated to storing and generating
  codes.
