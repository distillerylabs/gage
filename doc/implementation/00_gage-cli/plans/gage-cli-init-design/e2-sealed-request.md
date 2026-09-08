# E2 — The sealed request

[← E1](e1-unconditional-reencrypt.md) · [plan index](index.md) · next: [E3 — Joining a vault](e3-joining-side.md)

> **Recommended model: Opus.** The code is an offline-attackable secret
> and the seal is what makes the whole feature mean anything — entropy,
> generation source, and AEAD semantics are the parts where being subtly
> wrong is invisible to a passing test. **Worth an Opus review pass over
> the tests**, in the same spirit as M2.

## Goal

The enrollment primitives, at the library level, with **no command wired
up**: generate a code, seal a request to it, name the file, open it
again, decide whether it has expired, and prune. Bytes and files in,
values out.

Isolated exactly the way M2 isolates crypto and M3 isolates the entry
format: the risky part is proven before anything is layered on it.

## Depends on

- **M2** — age encrypt/decrypt, and the scrypt passphrase recipient the
  seal uses.
- **M0** — atomic writes, the exit-code taxonomy.

E2 is independent of E0 and E1 and can be worked in parallel with either.

## Design references

- ["The reframe: the seal is authentication, not confidentiality"](../../tdds/gage-cli-init-design.md)
  — why the code's entropy is a security parameter and the payload's
  secrecy is not
- [`D-ENROLL-CODE-FORMAT`](../../tdds/gage-cli-init-design.md) — Crockford
  base32, 80 bits, normalization, validation-before-decrypt
- [`D-ENROLL-EXPIRY-IN-NAME`](../../tdds/gage-cli-init-design.md) — why the
  expiry is in the filename and what that removed
- [`D-ENROLL-TTL`](../../tdds/gage-cli-init-design.md) — the ceiling and
  the two jobs it does
- ["The load-bearing invariant: `pending/` is inert"](../../tdds/gage-cli-init-design.md)

## Decisions

All settled. The one to keep in front of you while writing tests: **the
seal provides authentication, not confidentiality.** The plaintext is a
public key. A test that only proves round-tripping has not tested the
property the feature depends on — tampering must fail, and a wrong code
must open nothing.

## Tests (write first)

**Inertness — the invariant that makes the feature safe**

- [ ] An entry written while a valid, in-date, correctly-sealed request
      for key K is pending is **not** decryptable by K.
- [ ] `recipient list` omits pending requests; `recipient verify`
      reports "in sync" on a vault with ten pending requests.
- [ ] Garbage files, non-age files, and files whose names don't match
      `<uuid>-<epoch>.age` are skipped, not fatal — the same posture
      `ListIdentities` takes toward strays.
- [ ] **A vault with no `pending/` directory at all reports zero pending
      requests**, not an error. This is the normal state, not an edge
      case: git tracks no empty directories, so every vault has it until
      the first enroll creates the directory lazily.

**Seal and code**

- [ ] Round trip: seal, then open with the returned code, yields the
      same device, pubkey, and method.
- [ ] A wrong code opens nothing and returns `ErrEnrollmentCodeWrong`.
- [ ] Codes normalize: lowercase, spaces-for-hyphens, and a missing
      `GAGE-` prefix all open the same request.
- [ ] Crockford substitutions open the same request: `I` and `L` typed
      for `1`, `O` typed for `0`.
- [ ] Generated codes never contain `I`, `L`, `O`, or `U` — asserted
      over many generations, not one.
- [ ] Two successive generations produce different codes (the source is
      actually `crypto/rand`, not fixed or time-derived).
- [ ] A code failing length or alphabet validation is rejected **before
      any decryption is attempted** — assert via decrypt call-count
      instrumentation, the technique M7 uses for the index.
- [ ] A tampered sealed blob fails to open rather than opening with
      altered contents. This is the AEAD property the authentication
      claim rests on.
- [ ] A request whose sealed `request_id` disagrees with its filename's
      UUID portion is refused with `ErrEnrollmentIDMismatch`. Changing
      only the **epoch** portion is not a mismatch.

**Expiry and the filename**

- [ ] The file is named `<request-id>-<expires-epoch>.age`, and the
      epoch parses back to the same instant as the sealed `expires`.
- [ ] A request past its sealed `expires` is refused **even when its
      filename claims a far-future epoch** — the test proving expiry
      comes from the authenticated copy.
- [ ] The converse is safe: a filename claiming an already-past epoch on
      a still-valid seal gets pruned rather than approved, and grants
      nothing either way.
- [ ] Listing performs **no git history traversal** — assert against a
      vault with a long history, or by instrumenting the git layer. This
      is the property the whole filename scheme exists to buy.
- [ ] A TTL beyond the 7-day ceiling, zero, and negative are each
      rejected **by the library**, before anything is generated or
      written, with an error carrying `exitcode.Usage`. (E3 proves the
      `--ttl` flag itself; there is no command here to produce a usage
      rejection from.)
- [ ] **A filename epoch more than the ceiling beyond now is pruned as
      already expired** — the clamp that stops a forged
      `<uuid>-99999999999.age` lingering forever.
- [ ] A name that does not parse is **skipped, not deleted** — gage
      never removes a file it cannot account for.
- [ ] An ID matches the **UUID portion only**. Construct a vault where
      one request's epoch contains another's short id as a substring,
      and assert resolution picks the UUID match. An id appearing only
      inside an epoch matches nothing.
- [ ] **Resolution follows the base design's order**: exact UUID beats a
      substring match; a substring matching several requests returns the
      candidates rather than picking one; an id matching nothing returns
      `ErrEnrollmentNoSuchRequest`. Assert the ambiguous case returns
      *all* matches — a resolver that guesses here deletes a request the
      human did not name.
- [ ] A request sealed with `expires` already in the past at `created`
      (a slow-clock device) returns `ErrEnrollmentClockSkew`, and one
      that merely sat too long returns `ErrEnrollmentExpired`. Two
      errors, because the fixes are unrelated — fix that machine's clock,
      versus ask for a fresh request.

**Code hygiene**

- [ ] The generated code appears in the return value and nowhere else —
      not in the vault, not in local state, not in the session history
      file.

## Implementation

- [ ] Code generation: 16 Crockford base32 characters from
      `crypto/rand`, rendered in four hyphenated groups behind a
      cosmetic `GAGE-` prefix.
- [ ] Normalization and validation as one function, run before any
      decryption is attempted.
- [ ] The sealed payload as TOML — `request_id`, `device`, `pubkey`,
      `method`, `created`, `expires` — encrypted to a single scrypt
      recipient.
- [ ] Filename construction and parsing, with the UUID and epoch halves
      validated independently.
- [ ] `PendingRequest`, `OpenedRequest`, `EnrollmentRequest` types, and
      the typed errors: expired, wrong-code, id-mismatch, clock-skew, and
      no-such-request. Each carries the exit code the TDD's "Library
      surface" table assigns it — the taxonomy is an M0 contract, not a
      per-command choice.
- [ ] `Vault.ResolveEnrollment(id)` as the single resolution path both
      `approve` and `deny` use, returning `*AmbiguousRequestError` with
      the matches on ambiguity — a value `cmd/gage` renders, mirroring
      M5's `CandidateList` rather than inventing a second shape for the
      same question.
- [ ] Pruning: expired-by-filename, plus the beyond-ceiling clamp,
      leaving unparseable names alone.

## Definition of done

Every test above green on all three platforms. No command exists yet and
none should — E2 is finished when the primitives are proven, not when
they are reachable.

## Affects later milestones

- **E3** produces requests through these primitives and displays the
  code they generate.
- **E4** consumes them: opens by code, checks expiry against the sealed
  copy, and prunes.
- The **inertness** tests here are the ones that must never be weakened.
  Everything in E3 and E4 rests on `pending/` granting nothing on its
  own.
