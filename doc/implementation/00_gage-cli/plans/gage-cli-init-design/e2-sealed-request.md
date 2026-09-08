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

All settled. Two to keep in front of you while writing tests:

**The seal provides authentication, not confidentiality.** The plaintext
is a public key. A test that only proves round-tripping has not tested
the property the feature depends on — tampering must fail, and a wrong
code must open nothing.

**Authenticated is not well-formed, and the payload is untrusted input.**
Settled after this doc was first written; the TDD's "The sealed payload
is untrusted input" carries it. The seal proves the bytes are unaltered
since sealing, not that the fields mean anything — and two people can
supply a malformed one without any attack: a git writer can drop a file
into `pending/`, and anyone legitimately holding a code can seal whatever
they like. Every field is therefore validated before `OpenedRequest` is
returned, which is *before* E4 renders any of it to the approver. That
ordering is the point: the device name is displayed above the `[y/N]`
that decides whether to grant vault-wide access, and `devicename.Valid`'s
allowlist is what keeps escape sequences out of it.

**`D-ENROLL-SEAL-COST` was added after this doc was first written**, and
lands squarely in this milestone. Three things, and the reasoning for
each is in the TDD rather than restated here:

- The open path caps a request's **claimed** work factor, as
  `unlock.go` already does for identity files. Not doing it is a hang,
  not a slowdown.
- Seals are **written** at factor 14, not the identity file's 19,
  licensed explicitly by the code being 80 generated bits. It gets its
  own const and its own "is deliberate" test, following
  `shippedScryptWorkFactor`'s pattern — and must not read the mutable
  `scryptWorkFactor` test hook, or a suite that lowers the identity
  factor silently lowers this one too.
- One code-trying run attempts at most **32 live requests**.

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
- [ ] **A request whose scrypt stanza claims an absurd work factor costs
      a bounded wait, not a hang.** Hand-build a blob claiming 2^30,
      commit it, and assert the open path refuses it promptly. The open
      path must call `SetMaxWorkFactor(scryptMaxWorkFactor)` the way
      `unlock.go` already does for identity files — enrollment is a new
      decryption path and inherits that guard from nothing. This is the
      first part of `D-ENROLL-SEAL-COST`, and it is worth writing early:
      **one** hostile file is enough, no volume required, and the symptom
      is a process that appears to have stopped rather than an error.
- [ ] **Seals are written at factor 14**, asserted against the constant
      the way `TestScryptWorkFactorIsDeliberate` asserts the identity
      file's — a number this deliberate should fail a test when someone
      changes it, not drift silently.
- [ ] **The two work factors are independent.** Move the identity factor
      with `SetScryptWorkFactorForTests` and assert a sealed request's is
      unchanged. This is the test that stops the two being collapsed into
      one const later, which would recalibrate a security parameter
      through a test-only hook.
- [ ] **A wrong code against a full directory is fast.** Not a
      benchmark — assert the honest-typo path finishes well inside a
      generous ceiling with 32 requests pending, at the real factor. This
      is the case the factor was chosen for, and it regresses invisibly
      if someone later "hardens" the seal back to 19.

      **This is the one wall-clock assertion in the plan, so give it
      room.** At factor 14 the fixtures alone are 32 seals (~2s), and the
      Windows runner is the slow one. The ceiling wants to be generous
      enough that only a return to 19 — a ~32× regression — trips it,
      which is what the test is actually for. A tight bound here buys
      nothing and costs a flaky suite, which this project's convention of
      injected seams over timing exists to avoid.
- [ ] **More than 32 live requests is refused** with
      `ErrEnrollmentTooManyPending` at `exitcode.Conflict`, **before any
      decryption**, naming the ID form.
- [ ] **The bound counts live requests only**: a directory of 40 where
      most have expired prunes below the bound and proceeds normally.
      Expired files must never consume the budget, or ordinary neglect
      starts to look like an attack.
- [ ] **An ID-scoped run ignores the bound** and costs one attempt per
      code. Build a directory well past 32 and assert resolution by ID
      still works — this is what makes the refusal recoverable rather
      than a wall.
- [ ] A request whose sealed `request_id` disagrees with its filename's
      UUID portion is refused with `ErrEnrollmentIDMismatch`. Changing
      only the **epoch** portion is not a mismatch.

**The payload is untrusted input**

Hand-build each of these: seal a payload with a known code and a bad
field, the way both a git writer and a code-holder can.

- [ ] A sealed `device` outside `devicename.Valid`'s allowlist is refused
      with `ErrEnrollmentMalformedRequest`, **not**
      `ErrEnrollmentCodeWrong` — the code worked. The distinction is what
      keeps E4's retry loop from re-prompting for a correct code against
      a file no code can validate.
- [ ] Specifically, a `device` carrying **ANSI escapes or a newline** is
      refused. This is the case the check exists for: E4 prints the
      device name directly above the prompt that grants vault-wide
      access, so a name that can move the cursor can rewrite the question
      the human is answering.
- [ ] A sealed `pubkey` that is not a valid age recipient is refused the
      same way, by `agekey.ValidateRecipient` — before display, not at
      `AddRecipient` two milestones and one confirmation later.
- [ ] A `method` outside the allowlist (`passphrase` today) and a
      `request_id` that is not a UUID are each refused.
- [ ] `ErrEnrollmentMalformedRequest` maps to `exitcode.Conflict` — vault
      state a human looks at, never a retryable code.
- [ ] **A malformed request does not poison the run.** With one bad file
      and one good one in `pending/`, opening still yields the good
      request. A refusal that took the whole directory down would hand
      any git writer a denial of service on approval, which is the thing
      the bound in `D-ENROLL-SEAL-COST` exists to prevent by a different
      route.

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

- [ ] The generated code appears in the return value and nowhere else on
      the **generating** side: not in the vault, not in any file this
      milestone writes, not in local state.

      Scoped deliberately. An earlier version of this bullet also claimed
      the session history file, which E2 cannot test — it builds no
      commands — and which turned out to be false on the *approving*
      side, where `--code` is a typed command line that `history.add`
      records verbatim. That is E4's surface and an accepted risk rather
      than a test; see
      [the register entry](../gage-cli-design/open-questions.md#enrollment-code-history).

## Implementation

**Which of the TDD's exported functions land here.** Stated explicitly
because the test list above exercises listing and opening, and a reader
skimming only the bullets would take those for E3/E4 work and ship E2
without them. E2 delivers three of the six:

| Function | Milestone |
|---|---|
| `Vault.PendingEnrollments()` | **E2** — every listing test above calls it |
| `Vault.OpenEnrollment(codes)` | **E2** — every wrong-code, normalization, tamper, and expiry test calls it |
| `Vault.ResolveEnrollment(id)` | **E2** |
| `Vault.Enroll(...)` | E3 |
| `Vault.ApproveEnrollments(...)` | E4 |
| `Vault.DenyEnrollment(...)` | E4 |

E4 *wires* the first three into commands; it does not build them. Its
implementation list names them only to pin the contract that
`PendingEnrollments` and `OpenEnrollment` take no `Identity`, which is
what makes the late unlock possible.

- [ ] **A sealing entry point that validates the TTL**, since the test
      list rejects a zero, negative, or beyond-ceiling TTL "by the
      library" and there is no command here to reject it from. Sealing is
      the only E2 function that takes a duration, so the validation lives
      at its boundary and `Vault.Enroll` inherits it in E3 rather than
      re-checking. Unexported is fine — E3 is its only caller — but it
      has to exist, or that bullet has nothing to test.
- [ ] Code generation: 16 Crockford base32 characters from
      `crypto/rand`, rendered in four hyphenated groups behind a
      cosmetic `GAGE-` prefix.
- [ ] Normalization and validation as one function, run before any
      decryption is attempted.
- [ ] The sealed payload as TOML — `request_id`, `device`, `pubkey`,
      `method`, `created`, `expires` — encrypted to a single scrypt
      recipient at `enrollmentScryptWorkFactor`.
- [ ] **Payload validation on the open path**, before `OpenedRequest` is
      returned: `devicename.Valid` on `device`,
      `agekey.ValidateRecipient` on `pubkey`, the single-value allowlist
      on `method`, UUID on `request_id`. Failures are
      `ErrEnrollmentMalformedRequest` at `Conflict`, and a malformed file
      is skipped rather than failing the whole open — the same posture
      the filename parser already takes toward strays.
- [ ] **A scrypt recipient at an explicit work factor**, which does not
      exist yet. `PassphraseRecipient` hardcodes `scryptWorkFactor` —
      the mutable test hook (`internal/gage/crypt.go`) — so sealing
      through it is exactly the collapse `D-ENROLL-SEAL-COST` forbids: a
      suite that lowers the identity factor would silently lower the
      seal's. Give it an explicit-factor form and let the existing
      function be the caller that passes `scryptWorkFactor`. This is a
      change to a shared crypto constructor on `CreateIdentity`'s path,
      so it is called out rather than left to be discovered mid-task; the
      "two work factors are independent" test above is what proves it
      landed.
- [ ] `enrollmentScryptWorkFactor = 14` as its own const, carrying
      D-ENROLL-SEAL-COST's reasoning in its comment — including the
      coupling that licenses it (the code is generated, uniform, ~80
      bits) and the instruction not to merge it with
      `shippedScryptWorkFactor`.
- [ ] `SetMaxWorkFactor(scryptMaxWorkFactor)` on the open path, before
      any request is decrypted.
- [ ] The 32-request bound, applied after pruning and before any
      decryption, returning `ErrEnrollmentTooManyPending`. It belongs on
      the code-trying path only — `deny` and `PendingEnrollments` open
      nothing and stay unbounded, which is also how someone inspects and
      cleans up a stuffed directory.
- [ ] Filename construction and parsing, with the UUID and epoch halves
      validated independently.
- [ ] `Vault.PendingEnrollments()` — builds `PendingRequest`s from
      filenames alone, returns zero for a missing `pending/`, skips
      strays, and reads no git history.
- [ ] `Vault.OpenEnrollment(codes)` — normalize and validate each code,
      then try the surviving ones against each request. Takes **no
      `Identity`**: opening is keyed by the code, and that signature is
      the contract E4's late unlock rests on, so it is fixed here rather
      than arrived at there.
- [ ] `PendingRequest`, `OpenedRequest`, `EnrollmentRequest` types, and
      the typed errors: expired, wrong-code, id-mismatch, clock-skew, and
      no-such-request. Each carries the exit code the TDD's "Library
      surface" table assigns it — the taxonomy is an M0 contract, not a
      per-command choice.
- [ ] A malformed code returns `ErrEnrollmentCodeWrong` at
      `LockedOrAuth` — the *same* error as a well-formed code that opens
      nothing, now settled in the TDD's error list and exit-code table.
      Validation still runs before any decryption; that buys cost, not a
      different answer. Both spellings must be indistinguishable to the
      caller, since E4's retry loop keys on this value and a typo is what
      the loop is for.
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
