# E2 — The sealed request

[← E1b](e1b-shared-recipient-write.md) · [plan index](index.md) · next: [E3 — Joining a vault](e3-joining-side.md)

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
- **E0** — `[vault].id` and `Vault.ID`. New since an earlier draft, which
  called E2 independent of it: the sealed payload now carries `vault_id`
  and the open path refuses a request sealed for a different vault, so
  the primitive cannot be built against a vault that has no id. Only E0's
  *keying* half is needed, which is the half that should land first
  anyway.

E2 is independent of E1a and E1b and can be worked in parallel with
either.

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
- ["The sealed payload is untrusted input"](../../tdds/gage-cli-init-design.md)
  — what validation runs before `OpenedRequest` exists, and the size
  bound that runs before anything is read
- ["The request names the vault it is for"](../../tdds/gage-cli-init-design.md)
  — the replay direction `request_id` does not cover
- ["A sealed expiry beyond the ceiling is expired, not refused"](../../tdds/gage-cli-init-design.md)
  — the clamp applied to the copy approval actually enforces

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
  licensed explicitly by the code being 80 generated bits. It must not
  read the mutable `scryptWorkFactor` test hook, or a suite that lowers
  the identity factor silently lowers this one too — and it follows
  `shippedScryptWorkFactor`'s pattern **in full**: shipped const, live
  copy, and its own setter. An earlier draft of this bullet asked for the
  const alone, which is half the pattern and would have made the suite
  pay two seconds per seal; see the implementation list.
- One code-trying run attempts at most **32 live requests**.

**Four more decisions landed after this doc was first written**, all in
the TDD, and the first of them changes a signature this milestone
publishes:

- **`OpenEnrollment` takes the requests to try, not just the codes.** An
  earlier draft's `OpenEnrollment(codes []string)` had no way to express
  an ID-scoped run — which is the entire escape hatch from
  `ErrEnrollmentTooManyPending`, and which two bullets in the test list
  below require. The signature is
  `OpenEnrollment(requests []PendingRequest, codes []string)`, and the
  bound applies to whatever scope it is handed. A broad run passes
  `PendingEnrollments()`' result and can exceed the bound; an ID-scoped
  run passes the one request `ResolveEnrollment` returned and never can.
  Neither caller opts in or out — the escape hatch works by construction
  rather than by a flag or a second path.
- **The request names the vault it is for.** `vault_id` is sealed
  alongside `request_id` and compared against this vault's `[vault].id`,
  refusing a mismatch with `ErrEnrollmentWrongVault`. The `request_id`
  check stops a blob being replayed onto another *slot*; nothing stopped
  it being replayed into another *vault*, which needs no attacker at all
  — one person with two vaults and two codes on screen reaches it by
  typing the wrong one. See the TDD section of that name.
- **A pending file is bounded in size before it is opened.**
  `D-ENROLL-SEAL-COST` bounds count and work factor; it does not bound
  how large a file a git writer puts in `pending/`. 16 KiB, checked from
  the directory entry, plus the same limit on the decrypted payload.
- **The ceiling clamps the sealed expiry too, not only the filename's.**
  The sealed copy is the one approval enforces, so clamping the filename
  alone left the authoritative value unbounded. An `expires` more than
  the ceiling beyond *now* is `ErrEnrollmentExpired` — the existing
  error, because that is exactly what it is, and compared against now
  rather than `created` since both are attacker-supplied.

**And one clarification that is easy to read past**, because it decides
where two other rules live: `PendingEnrollments` returns **live requests
only** — expired-by-filename and beyond-ceiling ones are *filtered*, not
deleted. Deleting is pruning's job and rides the next write, because
listing is a read that takes no lock. That split is what makes both
"`recipient pending` never shows an expired request as pending" and "the
32-request bound counts live requests" true without a write having to
have come along first.

## Tests (write first)

**Inertness — the invariant that makes the feature safe**

- [x] An entry written while a valid, in-date, correctly-sealed request
      for key K is pending is **not** decryptable by K.
- [x] `recipient list` omits pending requests; `recipient verify`
      reports "in sync" on a vault with ten pending requests.
- [x] Garbage files, non-age files, and files whose names don't match
      `<uuid>-<epoch>.age` are skipped, not fatal — the same posture
      `ListIdentities` takes toward strays.
- [x] **A vault with no `pending/` directory at all reports zero pending
      requests**, not an error. This is the normal state, not an edge
      case: git tracks no empty directories, so every vault has it until
      the first enroll creates the directory lazily.

**Seal and code**

- [x] Round trip: seal, then open with the returned code, yields the
      same device, pubkey, and method.
- [x] A wrong code opens nothing and returns `ErrEnrollmentCodeWrong`.
- [x] Codes normalize: lowercase, spaces-for-hyphens, and a missing
      `GAGE-` prefix all open the same request.
- [x] Crockford substitutions open the same request: `I` and `L` typed
      for `1`, `O` typed for `0`.
- [x] Generated codes never contain `I`, `L`, `O`, or `U` — asserted
      over many generations, not one.
- [x] Two successive generations produce different codes (the source is
      actually `crypto/rand`, not fixed or time-derived).
- [x] A code failing length or alphabet validation is rejected **before
      any decryption is attempted** — assert via decrypt call-count
      instrumentation, the technique M7 uses for the index.
- [x] A tampered sealed blob fails to open rather than opening with
      altered contents. This is the AEAD property the authentication
      claim rests on.
- [x] **A request whose scrypt stanza claims an absurd work factor costs
      a bounded wait, not a hang.** Hand-build a blob claiming 2^30,
      commit it, and assert the open path refuses it promptly. The open
      path must call `SetMaxWorkFactor(scryptMaxWorkFactor)` the way
      `unlock.go` already does for identity files — enrollment is a new
      decryption path and inherits that guard from nothing. This is the
      first part of `D-ENROLL-SEAL-COST`, and it is worth writing early:
      **one** hostile file is enough, no volume required, and the symptom
      is a process that appears to have stopped rather than an error.
- [x] **Seals are written at factor 14**, asserted against the *shipped*
      constant the way `TestScryptWorkFactorIsDeliberate` asserts the
      identity file's — a number this deliberate should fail a test when
      someone changes it, not drift silently.
- [x] **The two work factors are independent.** Move the identity factor
      with `SetScryptWorkFactorForTests` and assert a sealed request's is
      unchanged; then move the enrollment factor with its own setter and
      assert the identity file's is unchanged. Both directions, because
      the failure being ruled out is the two being collapsed into one
      const later — which would recalibrate a security parameter through
      a test-only hook.
- [x] **The shipped enrollment factor is what a fresh seal actually
      reaches age with**, in the spirit of
      `TestScryptWorkFactorReachesAge`: restore the shipped value, seal
      one request, and read the factor back out of the blob's own scrypt
      stanza. The const being right is not the same claim as the const
      being wired up.
- [x] **Only the setter writes the live enrollment factor.** M2 pins this
      for the identity factor with an AST walk over the package
      (`TestOnlySetScryptWorkFactorForTestsAssignsIt`); the enrollment
      factor is a security parameter of the same kind and gets the same
      guard, extended rather than duplicated.
- [x] **A wrong code against a full directory is fast.** Not a
      benchmark — assert the honest-typo path finishes well inside a
      generous ceiling with 32 requests pending, **at the shipped factor,
      restored for the duration of this one test**. This is the case the
      factor was chosen for, and it regresses invisibly if someone later
      "hardens" the seal back to 19.

      **This is the one wall-clock assertion in the plan, so give it
      room.** At factor 14 the fixtures alone are 32 seals (~2s), and the
      Windows runner is the slow one. The ceiling wants to be generous
      enough that only a return to 19 — a ~32× regression — trips it,
      which is what the test is actually for. A tight bound here buys
      nothing and costs a flaky suite, which this project's convention of
      injected seams over timing exists to avoid.
- [x] **More than 32 requests in the scope is refused** with
      `ErrEnrollmentTooManyPending` at `exitcode.Conflict`, **before any
      decryption**, naming the ID form.
- [x] **The bound counts live requests only**: a directory of 40 where
      most have expired by filename yields a `PendingEnrollments` result
      below the bound, and a broad open over it proceeds normally.
      Expired files must never consume the budget, or ordinary neglect
      starts to look like an attack. Assert this **without a write having
      run** — the filtering is what makes it true, not the pruning.
- [x] **An ID-scoped run passes the bound by construction.** Build a
      directory well past 32, resolve one request by ID, hand
      `OpenEnrollment` that one-element slice, and assert it opens with
      one decryption attempt per code. This is what makes the refusal a
      detour rather than a wall — and note there is no flag involved: the
      two runs differ only in the scope they were handed.
- [x] **The scope is honoured, not treated as a hint.** With two valid
      requests in `pending/` and a code that opens one of them, a run
      scoped to the *other* request opens nothing and returns
      `ErrEnrollmentCodeWrong`. A scope parameter that were quietly
      widened back to "everything pending" would still pass every bullet
      above, and would silently reintroduce the unbounded path.
- [x] A request whose sealed `request_id` disagrees with its filename's
      UUID portion is refused with `ErrEnrollmentIDMismatch`. Changing
      only the **epoch** portion is not a mismatch.

**The payload is untrusted input**

Hand-build each of these: seal a payload with a known code and a bad
field, the way both a git writer and a code-holder can.

- [x] A sealed `device` outside `devicename.Valid`'s allowlist is refused
      with `ErrEnrollmentMalformedRequest`, **not**
      `ErrEnrollmentCodeWrong` — the code worked. The distinction is what
      keeps E4's retry loop from re-prompting for a correct code against
      a file no code can validate.
- [x] Specifically, a `device` carrying **ANSI escapes or a newline** is
      refused. This is the case the check exists for: E4 prints the
      device name directly above the prompt that grants vault-wide
      access, so a name that can move the cursor can rewrite the question
      the human is answering.
- [x] A sealed `pubkey` that is not a valid age recipient is refused the
      same way, by `agekey.ValidateRecipient` — before display, not at
      `AddRecipient` two milestones and one confirmation later.
- [x] A `method` outside the allowlist (`passphrase` today) and a
      `request_id` that is not a UUID are each refused.
- [x] `ErrEnrollmentMalformedRequest` maps to `exitcode.Conflict` — vault
      state a human looks at, never a retryable code.
- [x] **A malformed request does not poison the run.** With one bad file
      and one good one in `pending/`, opening still yields the good
      request. A refusal that took the whole directory down would hand
      any git writer a denial of service on approval, which is the thing
      the bound in `D-ENROLL-SEAL-COST` exists to prevent by a different
      route.

**The request names the vault it is for**

- [x] **A request sealed for vault A does not open in vault B.** Seal one
      against A's id, drop the file into B's `pending/`, and open it in B
      with the correct code: `ErrEnrollmentWrongVault` at
      `exitcode.Conflict`. Construct it as a file copy, which is exactly
      how it happens — the blob is committed and readable to everyone who
      can read A.
- [x] It is **not** `ErrEnrollmentCodeWrong`, asserted explicitly. A
      retry loop keyed on wrong-code would otherwise sit re-prompting for
      a code that is already correct, against a file no code can make
      right in this vault.
- [x] It is **not** `ErrEnrollmentMalformedRequest` either. The payload
      is well-formed and gage wrote it; reporting corruption would send a
      human looking for damage that does not exist. Three errors, three
      different next actions.
- [x] A sealed `vault_id` that is **absent or not a UUID** is
      `ErrEnrollmentMalformedRequest` — that one genuinely is a payload
      gage could not have produced.
- [x] The error **names the vault the request is for**, not just the
      mismatch. The id is opaque; the user has the other vault registered
      under a name gage can look up and print, and "this request is for
      \"work\", not \"personal\"" is the whole content of the mistake.
- [x] **Round-tripping within one vault is unaffected**, which is the
      regression this could cause: every seal-then-open test above still
      passes with the field present.

**A pending file is bounded before it is opened**

- [x] **A file over 16 KiB in `pending/` is skipped**, from its size
      alone — asserted with decrypt call-count instrumentation, so the
      claim is "never opened" rather than "opened and rejected". Skipped,
      not deleted: an oversized file is not something gage wrote, and the
      filename parser's posture toward strays is the one to match.
- [x] **An oversized file does not poison the run**: one 20 MiB file and
      one good request in `pending/`, and the good one still opens.
- [x] **The decrypted payload is limited too.** Seal a multi-megabyte
      payload with a known code and assert opening it refuses on the
      limit rather than allocating it. The party who can produce this is
      a code-holder — trusted enough to be granted access, not trusted
      enough to be handed an unbounded allocation — so the ciphertext
      bound alone does not cover it.

**Expiry and the filename**

- [x] The file is named `<request-id>-<expires-epoch>.age`, and the
      epoch parses back to the same instant as the sealed `expires`.
- [x] A request past its sealed `expires` is refused **even when its
      filename claims a far-future epoch** — the test proving expiry
      comes from the authenticated copy.
- [x] The converse is safe: a filename claiming an already-past epoch on
      a still-valid seal gets pruned rather than approved, and grants
      nothing either way.
- [x] Listing performs **no git history traversal** — assert against a
      vault with a long history, or by instrumenting the git layer. This
      is the property the whole filename scheme exists to buy.
- [x] A TTL beyond the 7-day ceiling, zero, and negative are each
      rejected **by the library**, before anything is generated or
      written, with an error carrying `exitcode.Usage`. (E3 proves the
      `--ttl` flag itself; there is no command here to produce a usage
      rejection from.)
- [x] **A filename epoch more than the ceiling beyond now is pruned as
      already expired** — the clamp that stops a forged
      `<uuid>-99999999999.age` lingering forever.
- [x] **A *sealed* `expires` more than the ceiling beyond now is treated
      as expired too**, with `ErrEnrollmentExpired` and not a malformed-
      payload error. The sealed copy is the one approval enforces, so a
      clamp applied only to the filename leaves the authoritative value
      unbounded. Hand-seal a payload claiming a ten-year expiry under a
      filename with an in-range epoch, so the filename cannot be what
      catches it.
- [x] The comparison is **against now, not against `created`** — assert a
      payload whose `created` is also forged far into the past still
      expires. Both fields are attacker-supplied, so checking one against
      the other checks nothing.
- [x] A name that does not parse is **skipped, not deleted** — gage
      never removes a file it cannot account for.
- [x] An ID matches the **UUID portion only**. Construct a vault where
      one request's epoch contains another's short id as a substring,
      and assert resolution picks the UUID match. An id appearing only
      inside an epoch matches nothing.
- [x] **Resolution follows the base design's order**: exact UUID beats a
      substring match; a substring matching several requests returns the
      candidates rather than picking one; an id matching nothing returns
      `ErrEnrollmentNoSuchRequest`. Assert the ambiguous case returns
      *all* matches — a resolver that guesses here deletes a request the
      human did not name.
- [x] A request sealed with `expires` already in the past at `created`
      (a slow-clock device) returns `ErrEnrollmentClockSkew`, and one
      that merely sat too long returns `ErrEnrollmentExpired`. Two
      errors, because the fixes are unrelated — fix that machine's clock,
      versus ask for a fresh request.

**Code hygiene**

- [x] The generated code appears in the return value and nowhere else on
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
| `Vault.OpenEnrollment(requests, codes)` | **E2** — every wrong-code, normalization, tamper, and expiry test calls it |
| `Vault.ResolveEnrollment(id)` | **E2** |
| `Vault.Enroll(...)` | E3 |
| `Vault.ApproveEnrollments(...)` | E4 |
| `Vault.DenyEnrollment(...)` | E4 |

E4 *wires* the first three into commands; it does not build them. Its
implementation list names them only to pin the contract that
`PendingEnrollments` and `OpenEnrollment` take no `Identity`, which is
what makes the late unlock possible.

- [x] **A sealing entry point that validates the TTL**, since the test
      list rejects a zero, negative, or beyond-ceiling TTL "by the
      library" and there is no command here to reject it from. Sealing is
      the only E2 function that takes a duration, so the validation lives
      at its boundary and `Vault.Enroll` inherits it in E3 rather than
      re-checking. Unexported is fine — E3 is its only caller — but it
      has to exist, or that bullet has nothing to test.
- [x] Code generation: 16 Crockford base32 characters from
      `crypto/rand`, rendered in four hyphenated groups behind a
      cosmetic `GAGE-` prefix.
- [x] Normalization and validation as one function, run before any
      decryption is attempted.
- [x] The sealed payload as TOML — `request_id`, **`vault_id`**,
      `device`, `pubkey`, `method`, `created`, `expires` — encrypted to a
      single scrypt recipient at `enrollmentScryptWorkFactor`.
- [x] **Payload validation on the open path**, before `OpenedRequest` is
      returned: `devicename.Valid` on `device`,
      `agekey.ValidateRecipient` on `pubkey`, the single-value allowlist
      on `method`, UUID on `request_id` and on `vault_id`, RFC 3339 on
      both timestamps. Failures are `ErrEnrollmentMalformedRequest` at
      `Conflict`, and a malformed file is skipped rather than failing the
      whole open — the same posture the filename parser already takes
      toward strays.
- [x] **The `vault_id` comparison**, after that validation and before
      `OpenedRequest` is returned: sealed value against `v.ID` (E0),
      mismatch is `ErrEnrollmentWrongVault` at `Conflict` with a message
      naming the vault the request belongs to. Kept distinct from both
      neighbours — the code worked, and the payload is not corrupt. There
      is deliberately **no `VaultID` field on `OpenedRequest`**: every one
      that exists is for this vault, so a field would be a constant the
      caller could only re-check.
- [x] **The sealed-expiry clamp**: an `expires` more than the TTL ceiling
      beyond now is `ErrEnrollmentExpired`, the same treatment the
      filename epoch already gets and the same error a genuinely stale
      request gets. Not a new error and not a malformed payload — the
      claim is simply outside what gage honours, which is what expired
      means. Compared against now rather than `created`, since both are
      attacker-supplied.
- [x] **Size bounds**: `maxPendingRequestBytes` (16 KiB), applied twice —
      once from the directory entry at listing time, so an oversized file
      is skipped without being read, and once as a limit on the decrypted
      payload, since age's plaintext is not bounded by its ciphertext and
      the only party who can produce a large one is a code-holder.
      Skipped, never deleted.
- [x] **A scrypt recipient at an explicit work factor**, which does not
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
- [x] `shippedEnrollmentScryptWorkFactor = 14` as its own const, carrying
      D-ENROLL-SEAL-COST's reasoning in its comment — including the
      coupling that licenses it (the code is generated, uniform, ~80
      bits) and the instruction not to merge it with
      `shippedScryptWorkFactor`.
- [x] **A live copy and its own test setter**, mirroring
      `scryptWorkFactor` / `SetScryptWorkFactorForTests` exactly:
      `enrollmentScryptWorkFactor = shippedEnrollmentScryptWorkFactor`
      and `SetEnrollmentWorkFactorForTests`. An earlier draft of this
      milestone asked for the const alone, which is the *first* half of
      M2's pattern mistaken for the whole of it — and the reason M2 has
      the second half is arithmetic that applies here too: this package's
      `TestMain` lowers the identity factor for the whole test binary
      because a suite that pays the shipped cost per operation is
      unusable. E2 fixtures alone are 32 seals; E4 builds pending requests
      through `Enroll` in roughly twenty tests, on three platforms, with
      Windows the slow one.

      **This does not weaken the independence the previous bullet is
      about**, which is the thing to check before "simplifying" it back:
      the two factors have two constants, two variables and two setters,
      so moving either leaves the other alone — which is exactly what the
      "independent" test above asserts in both directions. What is
      forbidden is enrollment *reading* `scryptWorkFactor`, and a separate
      hook is how you avoid that while still being able to run the suite.
      The "is deliberate" and "reaches age" tests assert the **shipped**
      const, so the shipped number stays pinned however the live one is
      moved.
- [x] `SetMaxWorkFactor(scryptMaxWorkFactor)` on the open path, before
      any request is decrypted.
- [x] The 32-request bound, applied to the scope handed in and before
      any decryption, returning `ErrEnrollmentTooManyPending`. Expired
      requests never reach it because `PendingEnrollments` already
      filtered them. It belongs on the code-trying path only — `deny` and `PendingEnrollments` open
      nothing and stay unbounded, which is also how someone inspects and
      cleans up a stuffed directory.
- [x] Filename construction and parsing, with the UUID and epoch halves
      validated independently.
- [x] `Vault.PendingEnrollments()` — builds `PendingRequest`s from
      filenames alone, returns zero for a missing `pending/`, skips
      strays and oversized files, reads no git history, and returns
      **live requests only**: expired-by-filename and beyond-ceiling ones
      are filtered out rather than deleted, because a listing takes no
      lock and writes nothing. Deleting them is pruning's job.
- [x] `Vault.OpenEnrollment(requests, codes)` — normalize and validate
      each code, apply the bound to the scope it was handed, then try the
      surviving codes against the requests it was given and nothing else.
      Takes **no `Identity`**: opening is keyed by the code, and that
      signature is the contract E4's late unlock rests on, so it is fixed
      here rather than arrived at there.

      **The scope parameter is what makes the bound recoverable**, so
      resist the shortcut of reading the directory inside this function
      and treating the argument as advisory. A broad run and an ID-scoped
      run are the same code path over different inputs; that is the whole
      design, and it is why neither caller needs a flag.
- [x] `PendingRequest`, `OpenedRequest`, `EnrollmentRequest` types, and
      the typed errors: expired, wrong-code, id-mismatch, wrong-vault,
      malformed-request, clock-skew, and no-such-request. Each carries the exit code the TDD's "Library
      surface" table assigns it — the taxonomy is an M0 contract, not a
      per-command choice.
- [x] A malformed code returns `ErrEnrollmentCodeWrong` at
      `LockedOrAuth` — the *same* error as a well-formed code that opens
      nothing, now settled in the TDD's error list and exit-code table.
      Validation still runs before any decryption; that buys cost, not a
      different answer. Both spellings must be indistinguishable to the
      caller, since E4's retry loop keys on this value and a typo is what
      the loop is for.
- [x] `Vault.ResolveEnrollment(id)` as the single resolution path both
      `approve` and `deny` use, returning `*AmbiguousRequestError` with
      the matches on ambiguity — a value `cmd/gage` renders, mirroring
      M5's `CandidateList` rather than inventing a second shape for the
      same question.
- [x] Pruning: expired-by-filename, plus the beyond-ceiling clamp,
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
