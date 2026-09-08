# Device enrollment — joining a vault without hand-carrying a public key

*Status: **design settled; not implemented.** An enhancement to
[gage-cli-design.md](gage-cli-design.md), not a replacement.
Implementation is planned in
[plans/gage-cli-init-design/](../plans/gage-cli-init-design/index.md).*

*All nine `D-ENROLL-*` decisions below are resolved.* Everything
that document says about vaults, identities, recipients, and the trust
boundary still holds; this adds one new path on top of it and changes no
existing invariant. Sections below reference the base document by its
section titles.*

---

## The problem

Getting a second device onto an existing vault takes five steps today,
and the awkward one is in the middle:

1. `gage clone <remote-url>` on the new device. This grants nothing —
   it's a git clone.
2. `gage identity add --use <vault>` on the new device. Generates a
   keypair, writes the wrapped private half to
   `$GAGE_DATA/identities/<vault-id>/<device>.age`, prints the public half.
3. **Carry that public key to a device that already has access.** By
   hand. A 62-character `age1...` string, moved between two machines
   over whatever channel the user improvises — a chat message, an email
   to themselves, a photo of a terminal, retyping it.
4. `gage recipient add <pubkey> --reencrypt` on the authorized device.
5. `gage sync` on the new device, which can now decrypt.

Steps 1 and 2 are two commands where the user has one intention ("put
this vault on this machine"). Step 3 is the real friction: it's manual,
error-prone, and — because pasting a key from an untrusted channel into
`recipient add` is exactly the moment access is granted — it's also the
step where the user's judgment is doing all of the security work, with
no help from the tool.

This document adds a path where the vault itself carries the public key,
and the thing the human moves out-of-band is a short code that
*authenticates* the request instead of a long key that *is* the request.

---

## Decisions

Settled in this document before any code is written, per the project's
milestone convention: a decision recorded here is durable across
sessions, while one made implicitly in code has to be re-derived by
whoever reads it next.

### `[x]` D-ENROLL-PROMPTER — how the approver's code reaches the library

**Resolved: it doesn't need to. The code is an ordinary input parameter,
not an interactive decision, so no `Prompter` change is needed at all.**

This overturns the draft's own recommendation (a new
`KindEnrollmentCode` on `UnlockRequest`). Writing that up is what showed
it was wrong, in two independent ways:

- **It isn't an unlock.** `Prompter.Unlock` is documented as asking for
  "whatever proves this device holds the private key a vault's chosen
  method expects." An enrollment code proves nothing of the kind — it
  proves the *requester* knew a secret shared out-of-band. Reusing that
  exchange would make `Kind` mean two unrelated things.
- **The additive-ness was illusory, and in the dangerous direction.**
  Adding a `Kind` compiles against every existing `Prompter`, which
  sounds like the "additive, not reworked" bar this project holds itself
  to. But an implementation that renders "Enter passphrase for vault X"
  for whatever it is handed would then render the *wrong prompt* for the
  new `Kind` rather than failing. A silent behavioral break is strictly
  worse than the compile-time break that adding a method would have
  caused — loud failure is the entire reason `UnlockRequest` is typed.

Neither option is needed, because the code is just an argument.
`OpenEnrollment(requests, codes)` is a pure function of what it's handed,
the way `AddRecipient` takes a pubkey. `cmd/gage` collects codes from
`--code` flags or, when none were given, by prompting with the existing
`Prompter.Value` — masked, one line, carrying no vault/device/attempt
context, which is exactly what that method's doc comment already
describes. A wrong code returns `ErrEnrollmentCodeWrong`, and `cmd/gage`
decides whether to ask again.

That is the base design's stated split working as intended: "retry
policy stays a decision `cmd/gage` makes, not one the library bakes in."
The library gains no new interface surface for the code, and neither
does `Prompter`.

**Scope, since one nearby thing does change `Prompter`.** This decision
is about the *enrollment code* — how the approver's secret reaches
`OpenEnrollment` — and the answer there is "it's an argument, so nothing
new is needed." It is not a claim that this feature adds nothing to
`Prompter` anywhere: clone's "enroll now?" offer needs a yes/no whose
default is *yes*, which today's `Confirm` cannot express. That is a
separate addition with its own reasoning, under "Clone's offer needs a
default-yes confirm" in "Library surface". The two were conflated in an
earlier draft, which is how this document came to claim a
`[Y/n]` transcript and an unchanged `Prompter` on the same page.

### `[x]` D-ENROLL-CODE-FORMAT — what the code looks like

**Resolved: Crockford base32, 16 characters, four hyphenated groups,
displayed behind a cosmetic `GAGE-` prefix.**

```
GAGE-7K4M-9QX2-P3RH-8WVN
```

- **16 characters × 5 bits = 80 bits**, drawn from `crypto/rand`. Behind
  age's scrypt work factor that is not brute-forceable, which matters
  because the sealed blob is an offline, unlimited-guess target for
  anyone who can read the vault (see "The reframe"). It stays short
  enough to dictate over a phone call or type on a phone keyboard.
- **Crockford's alphabet excludes `I`, `L`, `O`, and `U`**, so the
  characters most often confused in handwriting or speech never appear.
  Decoding additionally *maps* `I`/`L` → `1` and `O` → `0`, so the
  ambiguity that survives a bad transcription is corrected rather than
  rejected. Excluding `U` also keeps the generator from spelling
  unfortunate words by accident.
- **Input is normalized before use**: uppercased, with hyphens, spaces,
  and any `GAGE-` prefix stripped, then Crockford-decoded. Someone who
  writes it down and types it back lowercase, with spaces, or without
  the prefix gets what they expect.
- **A code that fails length or alphabet validation is rejected before
  any decryption is attempted**, so a typo costs nothing instead of N
  scrypt runs across N pending requests.
- **The library never persists the code.** It is returned once from
  `Enroll` for `cmd/gage` to display, and the *joining* device writes it
  nowhere — not in the vault, not in local state, not in the session
  history file. That is a property of the joining side specifically:
  enroll generates the code and never takes one as input, so there is no
  line for a history file to record it from.

  **The approving side is different, and the difference is not the
  library's to fix.** `gage recipient approve --code GAGE-…` puts the
  code on a command line, which in a session lands in the history file
  verbatim and in a one-shot run lands in the user's own shell history —
  neither of which `internal/gage` can see. Recorded as an accepted risk
  rather than papered over; the exposure is bounded (device-local, mode
  `0600` for gage's own file, dead in 24h, and the request is deleted on
  approval) and the approver who wants none of it omits `--code` and
  answers the masked `Prompter.Value` prompt instead, which is already
  the fallback. See "The enrollment code in command history" under
  "Accepted risks".

The alternative was a diceware-style word list, which is genuinely
easier to read aloud. It was rejected for now because it means shipping
and versioning a word list in the binary for a marginal gain over an
unambiguous alphabet, and because a changed list would invalidate codes
generated by an older build. If read-aloud transcription turns out to be
the dominant channel in practice, this is additive later — the wire
format is "a passphrase string," and nothing else depends on its shape.

### `[x]` D-ENROLL-SEAL-COST — what opening a request is allowed to cost

**Resolved: cap the claimed work factor, seal at a lower one than
identity files use, and bound how many requests a single run will
attempt.** Three parts, because the cost of opening a request has three
independent inputs and two of them are attacker-controlled.

Raised because "the load-bearing invariant: `pending/` is inert" was
overclaiming. Inertness governs authority, not cost — and the cost of
opening a request is the product of three things: how many files sit in
`pending/`, what work factor each one's scrypt stanza *claims*, and what
factor gage writes its own seals at. The first two are supplied by
whoever can write to the repository. Only the third is gage's alone, and
it was inherited from a calculation done for a different threat.

**First: the open path caps the claimed work factor, exactly as the
identity path already does.** `unlock.go` calls
`SetMaxWorkFactor(scryptMaxWorkFactor)` — 22 — before opening an
identity file, and its comment gives the reason in one line: "a damaged
or hostile file claiming 2^30 costs a bounded wait instead of hanging
the process." Enrollment is a *new* decryption path and inherits nothing
automatically. Without the same call, **one** committed file claiming
2^30 hangs `approve` for hours, with no volume of files needed and no
error to show for it. This is not a new decision so much as a
requirement not to forget an existing one in new code.

**The two calibrated choices: what factor gage *writes* a seal at, and
how many requests one run will attempt.**

The identity file's factor of 19 (~2s) is set by what it defends: a
*user-chosen* passphrase, where every doubling doubles an offline
attacker's cost. An enrollment code defends nothing of the kind. It is
16 Crockford characters from `crypto/rand` — ~80 bits — and 2^80 is
infeasible at *any* work factor, including none. The KDF is doing
approximately no work here that the entropy is not already doing.

That matters because the cost is multiplied. `approve` tries each
supplied code against each pending request, so at factor 19 the accepted
risk's own example — 3 codes, 10 requests — is 30 runs, about a minute
of wall clock for an operation the document elsewhere describes as
quick. The same arithmetic is what makes a stuffed `pending/` a
denial of service rather than an annoyance.

**Resolved: both.** They compose rather than compete — the factor
removes the cost problem at its root, the bound is what still holds if
the factor is ever raised again.

#### Seals are written at work factor 14, not 19

```go
// shippedEnrollmentScryptWorkFactor is the log2 cost a sealed enrollment
// request is written at. It is deliberately NOT
// shippedScryptWorkFactor: the two protect different things and are
// calibrated against different attacks. Do not collapse them.
const shippedEnrollmentScryptWorkFactor = 14

// enrollmentScryptWorkFactor is the live copy, lowered by the test
// binary through SetEnrollmentWorkFactorForTests and by nothing else —
// the same shape, and for the same reason, as scryptWorkFactor.
var enrollmentScryptWorkFactor = shippedEnrollmentScryptWorkFactor
```

**Both halves of that pattern are load-bearing, and taking only the first
is a trap worth naming.** "Its own const, not the mutable hook" is two
requirements that sound like one. The requirement that matters for
*calibration* is that enrollment must never **read** `scryptWorkFactor`,
or a suite lowering the identity factor silently lowers the seal's and
hides exactly the decision being made here. The requirement that matters
for *the suite* is that a security parameter deliberately set to two
seconds of work cannot be paid per operation by a test binary — which is
why `scryptWorkFactor` exists at all, and the arithmetic is no kinder
here: E2's own fixtures are 32 seals, and E4 builds pending requests
through `Enroll` across most of its list, on three platforms.

So enrollment gets the whole pattern rather than half of it: two
constants, two variables, two setters. Independence is preserved by there
being two of everything, not by there being no hook — moving either
factor leaves the other alone, which is a property worth a test in both
directions. The "is deliberate" and "reaches age" tests assert the
**shipped** constant, so the number that ships stays pinned however the
live one is moved.

**Why lower is correct here rather than merely convenient.** A work
factor buys cost *per guess*, which is what a user-chosen passphrase
needs and what `shippedScryptWorkFactor`'s comment is entirely about. An
enrollment code is not chosen by anyone: it is 80 bits from
`crypto/rand` (D-ENROLL-CODE-FORMAT). At 80 bits the guess count is the
defence, and it is infeasible at *any* factor including none — so the
extra doublings are being paid for in latency and bought nothing.

**Why not zero, then.** Because the licence above is a property of the
generator, and the factor should still hold up if that property is
weakened by a bug rather than by a decision. Factor 14 is roughly 60ms
against the identity file's ~2s at 19. Even against an implausible
collapse of the code to 40 bits, 2^40 guesses at 60ms is on the order of
centuries; against the real 80 bits it is not a number worth writing
down. So 14 keeps meaningful stretching as defence in depth while being
~32× cheaper than 19.

**The coupling, recorded where someone would break it:** *the low factor
is licensed by the code being generated, uniform, and ~80 bits.* If a
user-chosen code is ever accepted, or the length shortened, this factor
goes back up in the same change. D-ENROLL-CODE-FORMAT's deferred
word-list option is fine as specified, since it keeps the entropy and
changes only the rendering.

**Two work factors now exist in `gage`, and that is the point.** They
must not be merged, and enrollment must not read `scryptWorkFactor` —
the mutable test hook — or a suite that lowers the identity factor would
silently lower the seal's too, hiding exactly the calibration this
decision is making. Follow `shippedScryptWorkFactor`'s existing pattern:
a named const, a comment carrying this reasoning, and a test asserting
the constant is what ships. No migration is needed to change it later:
age records the factor in each blob's own scrypt stanza, so existing
requests keep opening at whatever they were written with — and requests
live 24 hours anyway.

#### One run attempts at most 32 requests

`approve` tries each supplied code against each pending request, so the
work is codes × requests and the second multiplicand is whatever a git
writer put in the directory.

- **The bound is 32 live requests**, counted after pruning, so expired
  and unparseable files never consume it. Thirty-two is far above
  anything legitimate — the transcripts here show two, and requests
  expire in 24h — and it is a bound on *cost*, not a limit on how many
  devices may enrol.
- **Exceeding it is `ErrEnrollmentTooManyPending`**, which refuses
  before any decryption and names `gage recipient approve <ID>` as the
  way through. That is a real escape hatch rather than a shrug:
  `ResolveEnrollment` narrows to one request from the filename alone, so
  an ID-scoped run is one scrypt run per code regardless of how many
  files are in the directory. That is a property of `OpenEnrollment`
  taking the requests to try rather than reading the directory itself:
  the two runs are one code path over different scopes, so the escape
  hatch needs no flag and cannot drift from the bounded path.
- **`deny` and `recipient pending` need no bound.** Neither opens
  anything — deny requires no code, and listing is filename parsing —
  so both keep working normally on a stuffed directory, which is also
  how someone would see and clean up the mess.

**The case this is really calibrated for is a typo, not an attack.** A
wrong code must try every request before it can conclude it opens
nothing, and `cmd/gage` retries on exactly that error. At 19 with ten
pending, one mistyped character costs twenty seconds before the retry
prompt returns. At 14, a wrong code against the full permitted 32 costs
about two seconds, and the realistic handful costs a tenth of that. The
attack case falls out for free once the honest case is fast.

### `[x]` D-ENROLL-TTL — default request lifetime

**Resolved: 24 hours by default, `--ttl` to override, a hard ceiling of
7 days.**

- **24 hours** covers the realistic slow case — a request made in one
  timezone and approved the next morning in another — without leaving
  blobs around for weeks. The common case (one person, two machines,
  minutes apart) is unaffected by any value in this range.
- **The 7-day ceiling is load-bearing, not a round number**, though what
  it does changed once the expiry moved into the filename (see
  D-ENROLL-EXPIRY-IN-NAME). It now does two jobs: it bounds `--ttl` at
  creation, and it bounds the *filename* value gage will honor — an
  epoch more than the ceiling beyond now is treated as already expired
  and pruned, which is the only thing stopping a forged far-future name
  from parking a blob in the tree permanently.
- **`--ttl` is validated at the boundary** — positive and no greater
  than the ceiling — and rejected with a usage error before anything is
  generated or written, the same treatment `--type` and `--method`
  already get.
- **Expiry is always enforced from the sealed copy**, never from the
  filename or the listing. The filename's epoch is a housekeeping hint
  only; see "Expiry, revocation, and pruning".

### `[x]` D-ENROLL-EXPIRY-IN-NAME — the expiry is in the filename

**Resolved: a pending request is named
`<request-id>-<expires-epoch>.age`, with the expiry as UTC seconds since
the epoch in the clear.**

This replaces an earlier draft that kept the filename a bare UUID and
tried to recover timing from git. That draft had two defects, and this
change removes both rather than solving either:

- **It displayed an expiry it could not compute.** `recipient pending`
  showed "expires in 22h" while `PendingRequest.Expires` was specified as
  the *ceiling*-derived bound — so a 24h request would have rendered
  "expires in 6d 22h". The real expiry is sealed. Outside the seal there
  was simply no honest value to print.
- **It needed a git history walk that gage has nowhere else.** Deriving
  "when was this requested" from the introducing commit means a
  first-appearance search through history, per request, in go-git. With
  the epoch in the name, the question disappears: `PendingRequest` drops
  `Requested` entirely, and pruning becomes a parse and a comparison.

The cost is a small, deliberate exception to the "filenames encode
nothing" rule — justified under "The filename" above, along with the
disagreement cases and why none of them are unsafe. The short version:
the existence of pending requests already leaks, the commit already
reveals roughly when, and what's added is the TTL, which is the 24h
default nearly always.

### `[x]` D-ENROLL-COLLISIONS — device names collide; the pubkey is the identity

**Resolved: a device name is a *label*, not an identity. The public key
is the identity.** Every collision case follows from that one sentence.

Two machines called `macbook-pro` is not exotic — default OS names,
corporate imaging, `ubuntu` on every VM. The codebase already treats it
as real: `TestIdentityAddRejectsAHostnameCollisionWithoutAnyoneTypingTheName`
exists precisely because the collision happens *without anyone typing
the name*. Three cases, three answers:

- **At enroll time**, `AddIdentity` already checks the vault's recipient
  list before generating anything and returns `ErrDeviceNameTaken`. That
  behavior is correct and unchanged; what this document adds is the
  requirement that the message name the fix (`--device NAME`) rather
  than only stating the conflict.
- **At enroll time, when the recipient it collides with is *this device
  itself*** — a case the bullet above gets wrong, and gets wrong in the
  one direction that hurts. A device that ran `gage identity add`, had
  its key added manually, and then runs `gage identity enroll` hits the
  name check and is told to pass `--device NAME`. Following that advice
  mints a *second* identity under a *second* label and publishes a
  request to grant access this device already has — the exact
  label-versus-identity confusion this decision exists to prevent, handed
  to the user as a suggestion. Enroll must therefore ask the pubkey
  question before the name question, and answer it the way approval
  answers its own duplicate: **already a recipient is a success that does
  no work.** Nothing is generated, nothing is sealed, nothing is
  published, and `gage` says this device can already read the vault.

  **It is answerable without an unlock, which is the only reason it can
  come first.** `[vaults.<name>].pubkey` in global config records this
  device's public key for this vault, written by `init`, `identity add`,
  and `enroll` itself (A20 / `Q-ORPHAN-BY-NAME`) — the three paths that
  produce or open a key and therefore *have* one to record. **`clone` is
  not among them**, and an earlier draft listing it there was wrong in a
  way worth stating rather than quietly deleting: a clone holds no
  identity and deliberately refuses to unlock in order to derive a public
  key, which is the entire subject of "What 'not a recipient yet'
  actually means". A clone that goes on to accept the enroll offer
  records the field, but it records it *as enroll*, at the point a key
  exists. The
  check is that key against the vault's recipient list — **keys, not
  names**, so it is also correct for a device an approver relabeled via
  `approve --device`, where no name collision fires at all and today's
  check would sail past and publish a pointless request.

  **When `pubkey` is absent** — an older global-config entry, or a
  hand-edited one — gage cannot answer the question and falls back to
  the name check above, `ErrDeviceNameTaken` and its `--device` advice
  included. That is the same "say why and do the conservative thing"
  posture `removeOrphanedIdentity` takes when the field is missing, and
  it is the pre-existing behavior rather than a new failure.
- **Between enroll and approve** — the request was created with no
  collision, and one appeared before it was approved — `approve` takes an
  optional `--device NAME` that relabels the request on the way in. The
  approver owns the recipient list, so choosing a non-colliding label is
  squarely their call, and it costs no new round trip with a joining
  device that is sitting there waiting. Without this the request would be
  **permanently unapprovable**: a valid code and a valid authenticated
  request that `AddRecipient` refuses on a name clash, failing late, with
  nothing the approver can do about it.
- **When the request's pubkey is already a recipient**, approval is a
  **success that does no work**: clear the request, report "already a
  recipient," re-encrypt nothing, commit nothing new. This is the stale
  duplicate — enroll, failed push, enroll again, one of the two approved
  — and erroring on it would be answering a question nobody asked.

**Relabeling does not weaken the authentication**, and it's worth being
precise about why. The code authenticates the *key*; the device name was
only ever advisory metadata that the vault happens to store beside it.
`OpenedRequest` stays immutable — every field on it is authenticated —
and the chosen label travels separately, as data the approver supplied
rather than data the seal vouched for.

**Auto-suffixing to `macbook-pro-2` was rejected.** Silent renaming
contradicts the posture of every other decision here.

**`--device` relabels exactly one request per run.** One flag cannot say
which of several requests it applies to, so it is accepted only when the
run resolves to a single request — narrow a batch with an ID if two
requests collide at once. That costs a second re-encryption pass in the
rare double-collision case, which is the right trade against inventing a
per-request flag syntax for something that needs two devices to collide
simultaneously.

**Re-running `identity enroll` mints a fresh request and a fresh code**, never
reusing the pending one. Reuse would force gage either to persist the
code — which D-ENROLL-CODE-FORMAT forbids — or to re-seal under a new
code and leave two live ids for one device. Duplicates are harmless
because `pending/` is inert, expiry prunes them, and the already-a-
recipient rule above turns a late approval into a no-op.

### `[x]` D-ENROLL-PRINT-ONLY — cut from v1

**Resolved: `--print-only` and `--request-file` are not in the first
version.** They are moved to "Deliberately out of scope."

The idea was an offline path: print the sealed request instead of
committing it, and let the approver read it back from a file — for a
remote this device cannot write to, and for air-gapped transfer. It was
the only part of this document that was sketched rather than designed,
and it forks nearly every rule in it:

- **No wire format.** "Prints" implies text; an age file is binary, so it
  would need ASCII armor specified.
- **No filename**, so `<request-id>-<expires-epoch>` doesn't exist —
  `ErrEnrollmentIDMismatch` has nothing to compare against and
  D-ENROLL-EXPIRY-IN-NAME simply doesn't apply.
- **No commit**, so nothing ever prunes it.
- **No file to delete on approval**, so the one-shot property quietly
  stops holding and the same blob can be approved repeatedly.

None of those are *unsafe* — approval reads only the sealed copy — but
four unstated exceptions is not a specification.

**What it would have bought, and why that isn't enough yet.** Both use
cases already have a supported answer this document points at twice: the
manual path (`gage identity add`, carry the public key, `gage recipient
add` elsewhere). What `--print-only` adds over it is *authentication* —
genuinely the point of this whole feature. But it buys that by having a
human hand-carry a ~500-byte armored blob **plus** a code out-of-band,
versus a 62-character key. On a manual channel that is a worse trade
than it first appears, and it is a second control flow for exactly the
reason `--wait` and approver-initiated invites are already deferred.

It stays cleanly additive: the sealed blob is unchanged, so accepting one
from a file later is a new input source, not a redesign.

**The cost is real and is not being hidden.** The trust-boundary section
used to name `--print-only` as the answer for a vault where the mere
*existence* of enrollment activity is sensitive. With it cut, that
answer becomes the manual path — which means giving up the
authentication this feature exists to provide. That narrowing is stated
where the leak is described rather than dropped.

### `[x]` D-ENROLL-REMOTE — when enroll touches the network, and how it fails

**Resolved: fetch first and fail early if the remote is unreachable;
push last and fail legibly if it is unwritable.** Enroll is the one write
in `gage` that is worthless unless it reaches the remote, and both halves
of this follow from that.

**It pulls before it commits, and it has to do so explicitly.** The base
design's automatic fetch + fast-forward pull rides on *unlocking a
vault* — session `use`, or a one-shot command's implicit unlock. A
joining device never unlocks the vault, because it cannot: it is not a
recipient. So enroll passes through none of the existing hooks and must
fetch on its own.

Skipping it would mean committing onto a stale tip and failing the push
as a non-fast-forward, with the standard "origin has diverged — run
`gage sync`" advice. That advice is wrong here specifically: `gage sync`
resolves entry conflicts by decrypting both sides, and this device
cannot decrypt anything. Sending someone to a command that cannot work
for them is worse than the divergence.

**An unreachable remote fails the command, which departs from
"warn and proceed."** That posture exists because refusing to show a
password when offline is worse than showing a possibly-stale one — a
read still delivers value offline. Enroll delivers none: an enrollment
request that was never published is a private key, a local commit, and a
code nobody can act on. Failing before any of that is created is kinder
than producing all three and reporting that the useful part didn't
happen.

The two failures stay **separate typed errors**, because they have
different fixes and the caller decides what to say about each:
`ErrEnrollmentNoRemote` means this vault has no remote configured (fix:
`gage git set-remote`), while `ErrEnrollmentRemoteUnreachable` means it
has one that can't be reached right now (fix: get on the network, or
check the token). Folding them together would have been a small
violation of the rule that library boundaries return distinguishable
errors so `cmd/gage` — not the library — picks the advice.

Order of operations, with the reasons that fix each position:

1. **Remote configured?** Else `ErrEnrollmentNoRemote` — before anything,
   and before the lock, since it needs no vault state.
2. **Take the vault write lock**, and hold it through step 8.
3. **Discard an unexpectedly dirty working tree**, warning once — the
   ordinary preamble every write already has, not a step invented here.
   It is `withVaultWrite`'s reset and it must run *before* the pull; see
   "The reset is not optional here" below.
4. **Fetch + fast-forward pull.** Fails with
   `ErrEnrollmentRemoteUnreachable` if the remote can't be reached, and
   with `ErrEnrollmentDiverged` if the fetch leaves the two sides
   diverged rather than fast-forwardable. Also the first point at which a
   bad or missing token is discovered, which is as early as it can
   honestly be discovered (below).
5. **Already-a-recipient check, then device-name collision check** —
   both against the recipient list as of the pull just performed rather
   than a stale one. The pubkey question comes first, because its answer
   makes the name question moot and its "no" is the one that must not be
   reported as a name conflict (D-ENROLL-COLLISIONS). Both run before
   step 6, so neither costs a prompt.
6. **Create or reuse the identity** — the only step that prompts.
7. **Seal, write to `pending/`, commit.**
8. **Push**, then release the lock — on every exit path, error paths
   included.

**The reset is not optional here, and it is the one step this list used
to omit.** Every mutating method in `gage` runs `resetDirtyWorkTree`
under the lock before it writes anything — that is what `withVaultWrite`
*is* (`internal/gage/vault.go`), and it is why "no write folds a previous
write's leftovers into its own commit" is structural rather than a rule
each verb remembers. Enroll is a write and gets the same preamble, so it
takes `withVaultWrite`, not the bare `withWriteLock`.

Skipping it fails in two ways at once, and the second one is the reason
this is spelled out rather than left to inheritance:

- **A fast-forward refuses over a dirty tree.** `gitrepo.FastForward`
  returns `ErrDirtyWorkTree`, which `syncStateError` maps to `Conflict`.
  So a stray file in `pending/` — precisely what a killed enroll leaves —
  makes the *next* enroll fail at step 4 with a dirty-working-tree error.
- **A joining device has no other write to clear it with.** Every other
  path that would run the reset (`insert`, `recipient add`, anything that
  commits) needs an unlock this device cannot perform. It is not a
  recipient; that is the whole reason it is enrolling. Without the reset
  in enroll's own preamble, one interrupted enroll wedges the device
  permanently, recoverable only by deleting a file by hand — and the file
  is under `.gage/`, which nothing in the tool's output ever mentions.

This is also what makes "Clock skew"'s crash-safety story true. That
section says a stray is discarded by the next write on that machine; on a
joining device, the next write *is* the next enroll, and only because the
reset runs here.

**A divergence is refused, not merged, and gets its own error.** Step 4
can find three states, and only two of them were previously named. A
clean fast-forward proceeds; an unreachable remote fails with
`ErrEnrollmentRemoteUnreachable`; and a *diverged* branch fails with
`ErrEnrollmentDiverged`, which is new here.

Divergence is reachable rather than exotic, and by the path this document
already accepts: a read-scoped token fails the push at step 8, leaving a
local commit behind (see below); the remote then moves; the re-run finds
two sides that have both advanced. Note that `v.pull` reports this
condition with a **nil error** and `SyncReport.Diverged` set, so an
implementation that only checks the error proceeds straight past it,
commits onto the diverged branch, and fails at the push — landing on the
standard "origin has diverged — run `gage sync`" advice that the top of
this decision spends three paragraphs establishing is exactly wrong for a
device that cannot decrypt anything.

Failing at step 4 instead keeps the property the rest of this ordering
exists for: nothing is generated, prompted for, or written that cannot be
published. What the error says is what a joining device can actually act
on — the unpublished local commit is inert (it is a `pending/` file and
nothing else), so the fix is to discard it and enroll again, not to
resolve a merge. Merging was considered and rejected: two devices'
pending files are disjoint and would merge cleanly, but that is a
property of the *common* divergence rather than of divergence, and a
device that cannot decrypt has no business in a code path whose failure
mode is an entry conflict.

**The lock wraps the pull, not just the commit**, and an earlier draft of
this section had that wrong. A fast-forward pull mutates the working
tree, so it already runs under the write lock today —
`tryPull` wraps `pull` in `underWriteLock` (`internal/gage/remotesync.go`).
Pulling outside the lock and taking it afterwards would leave a window
for another `gage` process to move HEAD between the catch-up and the
commit, which is exactly the interleaving the lock exists to prevent.
Enroll's pull-then-commit is one read-modify-commit sequence and gets one
lock, per "every write holds the per-vault advisory lock across the full
sequence."

**Which means enroll cannot reach the network through the existing public
sync entry points, and this is a trap rather than a detail.** `Pull`,
`Push`, and `tryPull` each take the write lock *themselves*, via
`underWriteLock` (`internal/gage/remotesync.go`) — that is the very
precedent cited above. But `vaultlock` is not re-entrant: every
`acquireVaultLock` opens a fresh file descriptor
(`internal/gage/vault.go`), and both `flock` and `LockFileEx` conflict
across open file descriptions even within one process. So an `Enroll`
that holds the lock and then calls `v.Pull(ctx)` does not recurse
harmlessly — it blocks against itself until the acquisition times out and
fails as a contended lock, reporting that some other process holds
something this process is holding.

Enroll therefore calls the unexported `v.pull` / `v.push` inside its own
`withVaultWrite`, which is the same shape `underWriteLock` itself has,
plus the dirty-tree reset every write takes (see "The reset is not
optional here"). The
requirement is recorded here rather than left to be discovered because
the failure is a timeout rather than a deadlock: it looks like
contention, it takes `vaultLockTimeout` to appear, and the obvious fix
someone reaches for — dropping the lock around the pull — is precisely
the interleaving the preceding paragraph rules out.

**Holding it across the passphrase prompt is deliberate**, and has
precedent in both directions: `--reencrypt` holds the lock for its whole
run, and M8b resolved the same question for interactive conflict
resolution by holding it. The concurrency model is
same-user-multiple-terminals, so "my other pane waits while I type a
passphrase" is expected rather than surprising.

**A read-scoped token still fails at step 8, and that is accepted rather
than solved.** A token with read access clones and fetches fine and
cannot push, so someone with one gets through the passphrase prompt,
gets a key written to disk and a local commit, and only then learns they
cannot publish.

Predicting it is not available: telling read scope from write scope
means asking the host about the token, and `gage` is deliberately
host-neutral with no host-specific code paths (Q-GIT-AUTH). The project
has already taken this exact position — `gage auth status`
"deliberately makes no network call," because "does this token still
work" is answered by the next fetch or push, "which says so in terms of
the operation the human actually wanted." Adding a credential probe here
would contradict a decision already made.

So the requirement is legibility and cheap recovery, not prediction:

- The push failure names the likely cause and the fix — a token without
  write access for this host, and `gage auth login` — rather than
  surfacing a bare 403 from the transport.
- It says plainly what *did* happen: the identity was created and the
  request committed locally but not published.
- Re-running after `gage auth login` is cheap, because the reuse path
  picks up the existing identity rather than generating a second one. It
  does mint a fresh request and code (D-ENROLL-COLLISIONS); the
  unpublished one is inert and expires.

Step 4 narrows the window: an unusable token is usually caught at the
fetch, before the passphrase prompt. What survives to step 8 is the
narrower case of a token that can read but not write.

### `[x]` D-ENROLL-VERBS — command naming

**Resolved: `gage identity enroll` on the joining side, `gage recipient
pending` / `approve` / `deny` on the approving side.** No new top-level
noun, no new help group.

```
Identity:
  identity add              Register this device's identity and print its public key
  identity enroll           Register this device's identity and publish a request to join
  identity list             List the keys this machine holds for a vault

Recipients:
  recipient add             Authorize a public key and re-encrypt the vault to include it
  recipient remove          Revoke a recipient and re-encrypt the vault without its key
  recipient list            List the keys this vault is encrypted to
  recipient pending         List enrollment requests waiting for approval
  recipient approve         Authorize a device from its enrollment request
  recipient deny            Discard an enrollment request without authorizing it
  recipient verify          Check .age-recipients and config.toml still agree
```

**What decided it: `enroll` *is* `identity add` plus publishing.** Not
approximately — `AddIdentity` calls `CreateIdentity`, the same function,
with the same create-or-reuse behavior
(`TestCreateIdentityReusesAnExistingFile`). The only difference is what
happens once the key exists. A command should live next to the command it
is a superset of, and putting it anywhere else hides a relationship the
implementation makes literal.

The two descriptions are deliberately identical up to their second
clause, so the listing shows that relationship without prose:
"Register this device's identity and **print its public key**" against
"Register this device's identity and **publish a request to join**."

**A bare top-level `gage enroll` was rejected**, though it reads better
in isolation. It would put a superset command in a different namespace
from its own base command, and introduce a fourth top-level noun beside
`vault`/`identity`/`recipient` whose only member is this one feature. A
unified `gage enroll request/list/approve/deny` group has the further
cost of moving a recipient-list write out of `recipient`, and needs a new
entry in the registry's `groupOrder` or it sorts after Git-specific.

**Approval stays under `recipient` because that is what it does**: it
writes `.age-recipients` and `config.toml` together, runs the
re-encryption, regenerates the trust cache, and is what `recipient
verify` checks afterward. Every effect it has is a recipient-list effect.

**The known cost, stated rather than glossed:** the feature spans two
help groups and never appears as one story. That matters less than it
first seems, because nobody discovers the approving side by reading
`gage help` — they discover it because the joining device printed the
exact command to run. The help listing is where you go once you already
know roughly what you want.

**Two list commands sit close enough to be confused, and their
descriptions do the disambiguating.** `identity list` answers "what can
this machine open," `recipient list` answers "who does this vault
trust." They diverge constantly — empty versus populated right after a
clone, and on an N-device vault every machine sees N recipients but one
identity. The wording puts the contrast in the grammatical subject
("this machine holds" / "this vault is encrypted to") rather than
leaving it to be inferred.

There is a sharper hazard underneath that wording, recorded here because
it is not fixable by naming: **the two lists are joined on the device
name, which `D-ENROLL-COLLISIONS` establishes is a label rather than an
identity** — and `identity list` cannot show public keys without an
unlock. So when both show `laptop-1`, nothing tells you whether they are
the same key. See `Q-IDENTITY-VAULT-NAME`.

**Registry consequences** (M0 enforces these, so they are part of the
decision, not follow-up): every command above is registered with a
`Short`, a `Group`, and an availability; all of them are available in a
session as well as one-shot, since none of them creates a vault the way
`init`/`clone` do; `groupOrder` is untouched. `recipient add`'s
description changes as part of A19 rather than this decision.

**Three existing `Short`s change, and they are not cosmetic** — each one
is a contrast this decision leans on, so shipping the new commands beside
the old wording would leave the listing arguing with itself:

| Command | Today | Becomes |
|---|---|---|
| `identity add` | "Register a new identity for this device and print its public key" | "Register this device's identity and print its public key" |
| `identity list` | "List the identities this device holds for a vault" | "List the keys this machine holds for a vault" |
| `recipient list` | "List every device this vault is encrypted to" | "List the keys this vault is encrypted to" |

`identity add`'s rewording is what makes it and `identity enroll`
identical up to their second clause, which is how the listing shows that
one is a superset of the other without prose. `identity list` and
`recipient list` are the pair this decision calls "close enough to be
confused": the new wording puts the contrast in the grammatical subject —
"this machine holds" against "this vault is encrypted to" — where the
current wording buries it in "identities" versus "devices," which reads
as a synonym.

---

## The reframe: the seal is authentication, not confidentiality

The obvious way to describe this feature is "encrypt the public key with
a passphrase and commit it." That description is misleading in a way
that produces the wrong design, so it's worth correcting up front.

**A public key needs no confidentiality.** Publishing it grants nothing.
Anyone with read access to the vault could already enumerate every
recipient's public key from `.age-recipients` and `.gage/config.toml`,
both plaintext and committed. Hiding one more public key buys nothing.

What the seal actually provides is **authentication**: successful
decryption proves that whoever produced the sealed blob knew a secret
that was agreed out-of-band. age's scrypt recipient is an AEAD, so a
blob that opens is a blob that was written by someone holding the code
and has not been altered since. That is the whole security value, and
it's a genuine one — it's the ingredient the base design explicitly says
is missing:

> `gage` has no built-in way to distinguish "a device legitimately
> registered via `gage identity add`" from "a line someone typed into a
> text file," because nothing cryptographically binds the two.
> — "Trust boundaries"

An enrollment request *does* bind the two, as strongly as the
out-of-band channel that carried the code.

Three design consequences fall directly out of this reframing, and none
of them follow from the "encrypt a secret" framing:

- **The code's entropy is a security parameter, so `gage` generates it
  rather than asking the user for one.** The sealed blob sits in a git
  repository that every reader of the vault can fetch, which makes it an
  offline, unlimited-guess target — exactly the exposure "Local identity
  storage" refuses to accept for wrapped private keys. The difference is
  what a successful crack yields: not a private key, but the ability to
  *forge an enrollment request* that a human would then be asked to
  approve. That's still worth defending, and a user-chosen passphrase
  would not defend it. A generated ~80-bit code, behind scrypt, does.
- **The blob's short life is a security parameter too.** Since the only
  thing a crack buys is the ability to forge a request while one is
  pending, a request that expires and is deleted on approval shrinks the
  window to hours.
- **Putting the blob in the vault is fine, and does not contradict "not
  inside `$GAGE_DATA/vaults/<name>/`."** That rule exists because a
  passphrase-wrapped *private key* in a synced repo weakens guarantee #1
  ("can someone decrypt ciphertext that already exists") down to the
  strength of a passphrase. No private key is involved here. The
  plaintext under the seal is a public key and some metadata, so a
  successful offline attack reveals nothing that wasn't already public.

---

## On-disk layout

One new directory in the vault, alongside the existing structure:

```
myvault/
├── .gage/
│   ├── config.toml
│   └── pending/                                        # new
│       ├── 7c1e4a90-3b52-4f18-9d6a-8e2f10b4c3d7-1788862320.age
│       └── e4f88b21-0a77-4c39-b512-6d90a1e2f345-1788816000.age
├── .age-recipients
├── entries/
├── .gitattributes
└── .gitignore
```

**`pending/` is created lazily, by the first enroll**, and its absence is
the normal state rather than an error. Git does not track empty
directories, so a vault that has never had a request simply does not have
the directory — including every vault that predates this feature.
`PendingEnrollments` reports zero requests for a missing directory, the
same answer it gives for an empty one, and no command creates it
speculatively. (Recorded as
[A22](../plans/gage-cli-design/open-questions.md#a22), which folds the
directory into the base design's layout section when E3 ships.)

Each file is one sealed enrollment request: an age file with a single
scrypt (passphrase) recipient, whose plaintext is:

```toml
request_id = "7c1e4a90-3b52-4f18-9d6a-8e2f10b4c3d7"
vault_id   = "b0d4e7f2-91a3-4c60-8e15-3f7a26c8d904"
device     = "andrews-macbook-pro"
pubkey     = "age1qz8x2..."
method     = "passphrase"
created    = "2026-09-07T10:12:00Z"
expires    = "2026-09-08T10:12:00Z"
```

**`vault_id` binds the request to one vault**, and it is the reason the
seal cannot be replayed sideways. See "The request names the vault it is
for" below.

### The sealed payload is untrusted input

**Authenticated is not the same as well-formed**, and conflating the two
is the one way this feature's confirmation prompt could be turned against
the person reading it. What the seal proves is that the blob has not been
altered since it was written by someone holding the code — nothing about
what the fields *contain*. Two suppliers of a malformed one need no
attack at all: any git writer can drop a file into `pending/`, and anyone
who legitimately holds a code can seal whatever they like under it.

So every field is validated on the way out of `OpenEnrollment`, before
`OpenedRequest` is returned and therefore before anything is rendered:

- **`device`** against `devicename.Valid` — the same allowlist
  `vaultconfig.Read` already applies to a committed, git-writable
  recipient label, and for the same reason (Q-DEVICE-NAME). It is a
  filesystem path component and a `config.toml` value eventually, but
  the check has to happen *earlier* than either of those uses, because
  the name is displayed to the approver first. The allowlist is
  `a-z0-9._-`, which excludes control characters and escape sequences —
  so validating here is what stops a sealed device name from redrawing
  the `[y/N]` prompt it is printed above.
- **`pubkey`** against `agekey.ValidateRecipient`, the check
  `AddRecipient` already performs. Same argument: it is shown before it
  is used.
- **`method`** against the single-value allowlist (`passphrase` today),
  the treatment `--type` and `--method` already get.
- **`request_id`** as a UUID, before it is compared against the
  filename's UUID portion.
- **`vault_id`** as a UUID, and then against this vault's own
  `[vault].id`. A mismatch is `ErrEnrollmentWrongVault` rather than a
  malformed payload — the file is well-formed, it is simply not for this
  vault. See "The request names the vault it is for".
- **`expires` and `created`** as RFC 3339 timestamps. An `expires` more
  than the TTL ceiling beyond *now* is not rejected as malformed: it is
  **treated as already expired**, exactly as the same claim in a filename
  is. See "A sealed expiry beyond the ceiling is expired, not refused".

A payload that fails any of these is `ErrEnrollmentMalformedRequest`,
deliberately **not** `ErrEnrollmentCodeWrong`: the code worked. It opened
a request that is not one `gage` could have produced, which is a fact
about the vault a human should look at rather than something to retype a
code over. The distinction matters to `cmd/gage`'s retry loop, which
keys on the wrong-code error and would otherwise sit re-prompting for a
correct code against a file no code can ever make valid.

This is the same posture as everything else in this document that reads
from the repository: `.gage/config.toml`'s device names are validated on
read, filenames are parsed and skipped rather than trusted, and the
sealed `expires` is preferred over the filename's. The payload is one
more committed, git-writable surface, and it gets the same treatment.

**A pending file is bounded in size, before it is opened at all.** A
legitimate sealed request is a few hundred bytes; the payload is six
short fields. Nothing in `D-ENROLL-SEAL-COST`'s three cost inputs bounds
how *large* a file in `pending/` is, and a git writer chooses that
freely. So there is a fourth bound, and it is the cheapest of the four:

- **A file larger than `maxPendingRequestBytes` (16 KiB) is skipped at
  listing time**, from the directory entry's size alone — before any
  read, any KDF run, and any allocation. Skipped, not deleted: an
  oversized file is by definition not something gage wrote, and the
  conservative direction is the one the filename parser already takes
  toward strays.
- **The decrypted payload is read through a limit too**, at the same
  ceiling, because age's plaintext is not bounded by the ciphertext's
  size and the only party who can produce one is a code-holder — trusted
  enough to be granted access, not trusted enough to be handed an
  unbounded allocation.

16 KiB is roughly fifty times the largest request gage can produce, which
is the right shape for a bound whose only job is to keep a hostile file
from being interesting.

### The request names the vault it is for

**`vault_id` is sealed alongside `request_id`, and it closes the one
replay direction the filename scheme does not.** The `request_id`
comparison stops a blob being renamed onto a different slot or replayed
into a fresh one *within a vault*. It says nothing about a blob moved
*between* vaults, and moving one costs an attacker nothing: the file is
committed, world-readable to every reader of the vault, and inert
wherever it lands.

The scenario is not exotic, and it does not need an attacker at all:

- **With one.** Someone with read access to vault X and git write access
  to vault Y copies a pending request out of X and commits it into Y.
  They still cannot open it — the code is not theirs — but the approver
  who *does* hold the code now has a request for that device sitting in
  two vaults.
- **Without one.** One person, two vaults, two devices being set up in
  the same afternoon: two codes on screen, and
  `gage recipient approve --code …` run against whichever vault is
  current. Nothing in the request stops the wrong code opening in the
  wrong vault, and what is granted is vault-wide access to a vault
  nobody asked for.

The confirmation names the vault, so a careful human catches both. That
is exactly the kind of load the rest of this document declines to put on
a human when a field can carry it instead — the same argument that put
the device name under the seal rather than in the filename.

So `OpenEnrollment` compares the sealed `vault_id` against the opening
vault's `[vault].id` (A20 / `Q-IDENTITY-VAULT-NAME`) and refuses a
mismatch with `ErrEnrollmentWrongVault`, which is deliberately three
things at once:

- **Not `ErrEnrollmentCodeWrong`** — the code worked, and `cmd/gage`'s
  retry loop must not re-prompt for a code that can never be right here.
- **Not `ErrEnrollmentMalformedRequest`** — the payload is well-formed
  and was produced by `gage`. Reporting it as corruption would send a
  human to look for damage that isn't there.
- **Its own message**, naming the vault the request *is* for, since that
  is the whole content of the mistake and the user has the other vault
  registered under a name gage can print.

**This is the second consumer of A20's vault id**, after the identity and
trust-cache paths, and it is the reason E2 depends on E0 rather than
being independent of it: a request cannot name a vault that has no id.

**A device relabeled by `approve --device` is unaffected**, as are every
other case `D-ENROLL-COLLISIONS` covers. The label was never
authenticated; the vault id is, and it is not a label — it is the one
thing about a vault that does not change.

### A sealed expiry beyond the ceiling is expired, not refused

`D-ENROLL-TTL`'s ceiling does two jobs, and until now it did the second
of them in one place only. It bounds `--ttl` at creation, and it bounds
the *filename* epoch gage will honor — "an epoch more than the ceiling
beyond now is treated as already expired and pruned," which is what stops
a forged `<uuid>-99999999999.age` parking in the tree forever.

The sealed `expires` is the copy approval actually enforces, and the same
clamp applies to it for the same reason: a hand-sealed payload can claim
any expiry at all, and the ceiling is the statement of what gage could
have produced. A sealed `expires` more than the ceiling beyond now is
therefore **treated as already expired** — `ErrEnrollmentExpired`, the
error that already exists, because that is precisely what it is.

Two things follow, and both are what makes this a one-line rule rather
than a new category:

- **It needs no trust in `created`.** The comparison is against *now*,
  the same reference the filename clamp uses. `created` is
  attacker-supplied too, so hanging the check on it would be checking one
  unauthenticated claim against another.
- **It is not a malformed payload.** Nothing about the file is
  ill-formed; the claim is simply outside what gage honors, which is the
  definition of expired. Minting a separate error would split one
  outcome — "ask for a fresh request" — across two names.

`ErrEnrollmentClockSkew` is unaffected and still covers the opposite
direction: an expiry already in the past at `created`, which is a wrong
clock rather than a stale request.

### The filename: `<request-id>-<expires-epoch>.age`

```
7c1e4a90-3b52-4f18-9d6a-8e2f10b4c3d7-1788862320.age
└──────────── UUIDv4 (36 chars) ────┘ └─ epoch ─┘
```

**The device name lives inside the seal, and the filename carries no
identity.** Naming the file `andrews-macbook-pro.age` would hand every
reader of the vault a free "Andrew is setting up a new laptop" signal
before anyone approved anything. The device name becomes public on
approval — it's a recipient label in `config.toml` — but there is no
reason to leak it *before*, and no reason at all if the request is denied
or expires.

**The expiry, however, is deliberately in the clear, and that is an
exception to `entries/`'s "a filename never encodes content" rule.** The
exception is narrow and worth justifying rather than slipping past.
Under `entries/`, the *set of entries* is itself the secret, so a
filename must reveal nothing at all. Under `pending/`, the existence and
count of requests is already unavoidably visible to any reader, and what
the epoch encodes is not *what* or *who* but *when this stops mattering*
— operational housekeeping, not vault content.

What it buys is disproportionate to what it costs:

- **`recipient pending` can show a real expiry**, in the approver's own
  local timezone, without opening anything. Epoch seconds are
  timezone-free by construction, so there is nothing to normalize.
- **Pruning needs no git history walk.** The alternative was deriving a
  request's age from the commit that introduced it, which means a
  first-appearance search (`--diff-filter=A`-equivalent) through history
  per request — O(history), and machinery gage has nowhere else. With the
  epoch in the name, pruning is a string parse and a comparison, and the
  concept of "when was this requested" disappears from the outside of the
  seal entirely.

What it costs: a reader learns each request's TTL. In practice that is
the 24h default, and the commit that added the file already reveals
roughly when it appeared, so the genuinely new signal is only a
*non-default* TTL — and a distinctive one used repeatedly is a weak
correlator across requests. Judged acceptable against removing an
O(history) lookup and a display gage otherwise could not honestly
produce.

**`request_id` is repeated inside the seal, matching the filename's UUID
portion.** The filename is unauthenticated — anyone with git write access
can rename a file — while the copy under the seal is not. Approval
compares the two and refuses a mismatch, so a blob cannot be renamed onto
a different slot or replayed into a fresh one. The epoch portion is
deliberately *not* part of that comparison; see below.

**`expires` is enforced from inside the seal, never from the filename.**
The filename's epoch is a hint for display and pruning only. The two can
disagree, and both directions are safe:

- **Filename says expired, seal says valid** — gage prunes a request that
  was still live. A denial of service, but not a new one: anyone who can
  rename the file can equally delete it.
- **Filename says valid, seal says expired** — the listing shows it, and
  approval refuses it with `ErrEnrollmentExpired`. Mildly confusing,
  never unsafe.

Because approval reads only the authenticated copy, no lie in a filename
can extend a request's real life by a second.

### `.gitattributes` deliberately does *not* cover `pending/`

The base design marks `.age-recipients` and `.gage/config.toml` as
`-merge` because a line-based union of two recipient lists is a
recipient list nobody wrote and nobody reviewed — a silent grant. That
reasoning does not apply here, and reflexively copying it would be
wrong.

Two devices enrolling simultaneously produce two *different files*,
which git merges by taking both. A union of pending requests is exactly
the correct outcome: both people are in fact asking to join, and the
merge grants neither of them anything. `pending/` is inert (below), so
there is no such thing as a dangerous merge of it.

---

## The load-bearing invariant: `pending/` is inert

**Nothing in `pending/` is ever consulted at encryption time.** Encryption
reads `.age-recipients`, exactly as it does today, and nothing about
this feature changes which file that is or when it's read. A pending
request is a *proposal*; only `gage recipient approve` — a human
decision, with a code, on a device that already holds access — turns a
proposal into a grant, and it does so by writing the two recipient files
that already exist.

This is what keeps the feature from widening the trust boundary. Someone
with git write access can drop arbitrary files into `.gage/pending/` and
gain no access by it: they could already append a key to
`.age-recipients` directly, which is strictly more effective. Adding an
inert directory that a human must explicitly promote out of is not a new
attack surface — it's a *narrower* channel offered alongside the wide
one that already exists.

**"Grants nothing" is not "costs nothing", and an earlier draft of this
section overclaimed by saying the attacker achieves "precisely
nothing".** Inertness is about *authority*, and it holds: no file in
`pending/` moves the trust boundary a millimetre. What such a file can
do is make `approve` expensive, because opening a request is a
deliberately slow KDF run and the number of requests to try is whatever
the directory happens to hold. That is a denial-of-service concern
rather than a disclosure one, and it needs bounding rather than
dismissing — see `D-ENROLL-SEAL-COST`.

It is worth being precise that this is the same *class* of nuisance the
filename section already accepts, not a new kind of exposure: "anyone
who can rename the file can equally delete it." Someone with git write
access can already make a vault unpleasant to use in a dozen ways. The
difference is that an unbounded KDF loop degrades badly and confusingly
— a hang, not an error — so it gets a bound.

Two rules enforce inertness in code, and both belong in the test list:

- Encrypt paths never read `.gage/pending/`. A vault whose `pending/`
  contains a valid, in-date, correctly-sealed request for an attacker's
  key must produce ciphertext that the attacker cannot open, until and
  unless someone approves it.
- `gage recipient list` and `gage recipient verify` never count pending
  requests as recipients. `verify` compares `.age-recipients` against
  `config.toml` and nothing else, so a vault with ten pending requests
  still reports "in sync".

---

## The enrollment code

Generated by `gage` on the joining device, never chosen by the user —
`GAGE-7K4M-9QX2-P3RH-8WVN`. The format, entropy, alphabet, and
normalization rules are settled under D-ENROLL-CODE-FORMAT above; what
follows is what the code *means* rather than what it looks like.

The `GAGE-` prefix is cosmetic, and it earns its place by making the
string recognizable when it turns up somewhere it shouldn't — a chat
log, a screenshot, a terminal recording. That recognizability is a
reminder that it doesn't belong there.

**`gage` says what the channel requirements are, at the moment it prints
the code**, rather than assuming the user has read this document: the
code must reach the approving person over a channel where they can tell
it came from you, and it should not be sent through the same git remote
the request itself travels over. That second clause is the whole point —
a code sitting in the vault beside the blob it seals authenticates
nothing.

### What the code does *not* prove

The sealed payload claims a public key, and nothing in it proves the
requester holds the matching private key. Someone who knows the code
could enroll a public key they do not control.

This is a real gap, stated plainly rather than papered over — but it is
close to self-neutralizing, for a structural reason worth recording. The
outcome of approval is that the vault is re-encrypted *to that public
key*. An attacker who enrolls a key they don't hold has arranged for
the vault to be readable by someone who isn't them. The residual harm is
a junk recipient in the list, which is noise rather than exposure, and
which every other device's trust cache will flag on their next
operation. Removing it costs a `recipient remove --reencrypt`.

Closing the gap properly would need a signature over the request, and
X25519 identity keys can't produce one — it would mean a second key of a
different type, per device, purely for enrollment. That is not worth it
for the harm above. If a future method that *can* sign (SSH keys,
YubiKey PIV, Secure Enclave) becomes a real identity method, the request
payload gains an optional signature field and approval starts preferring
it. That is additive, in the same shape as the "Signed recipient changes"
mitigation the base document already sketches.

---

## The flows

### Joining device

```
$ gage clone https://github.com/you/personal-vault.git
Cloning into ~/.local/share/gage/vaults/personal... done.
This device holds no identity for "personal", so it can't read anything here yet.

Set up an enrollment request now? [Y/n] y

This device needs a key of its own for "personal". Choose a passphrase to
protect it: it stays on this device, it is not the enrollment code, and
you will never send it to anyone.
Enter passphrase: ****
Confirm passphrase: ****

gage: this device is "andrews-macbook-pro", public key age1qz8x2...

Enrollment request published (expires in 24h).

  Enrollment code:  GAGE-7K4M-9QX2-P3RH-8WVN

This code is safe to send and is not your passphrase. Give it to someone
who can already read "personal", over a channel where they can tell it
came from you — not through this vault's remote.
They run:  gage recipient approve --code GAGE-7K4M-9QX2-P3RH-8WVN

Then run `gage sync` here.
```

### Which secret is which

Three distinct secrets appear in or around this flow. Conflating any two
would be a genuine error, so they're worth naming outright rather than
leaving to inference:

1. **The identity passphrase** — the one prompted for above. It protects
   this device's newly generated private key at
   `$GAGE_DATA/identities/<vault-id>/<device>.age`. The *user* chooses it,
   it never leaves this machine, it is never transmitted to anyone, and
   it is what this device will be asked for on every future unlock. It's
   confirmed twice because this is the existing `PurposeCreate`
   exchange: there is nothing to check the answer against, so a typo
   would be unrecoverable and nobody would find out until the next
   unlock. It is also why `GAGE_PASSPHRASE` is not honored here — the
   base design already refuses it wherever the answer protects a
   brand-new key (see "Session model").
2. **The enrollment code** — *generated* by `gage`, displayed once,
   carried out-of-band, dead in 24 hours. It seals and authenticates the
   pending request. The user never chooses it and never has to remember
   it.
3. **The approver's own identity passphrase** — prompted on the *other*
   device, because approval needs their identity both to run the
   re-encryption and to ask M10's recipient-change question (see "The
   approver unlocks, and the unlock comes late"). Unrelated to either of
   the above, and notably it is *their* secret, not the joining
   device's.

**(1) and (2) are opposites that appear four lines apart on one screen**,
which is exactly the setup for an expensive mistake: one is chosen by the
user and stays on one machine forever, the other is produced by the tool
and is meant to be sent. The failure mode to design against is someone
pasting their identity passphrase into a chat window because they thought
it was the code — or typing the code at their next unlock prompt and
concluding gage is broken.

So each is labeled at its point of use, in the output above: the
passphrase prompt says the answer stays on this device and is not the
code, and the code is printed with an explicit "safe to send, not your
passphrase." This is cheap, and it's the only place in `gage` where two
different secrets are on screen at once.

`gage identity enroll --use personal` does the same thing against a vault that's
already cloned, and is what someone reaches for when they cloned first
and decided to join second, or answered `n` above. It **creates the
identity only if one does not already exist**: a device that has run
`gage identity add`, or that already holds a wrapped identity file for
this vault from an earlier attempt, reuses that key rather than
generating a second one and orphaning the first.

**This is not a difference from `identity add` — it is the same code.**
An earlier draft of this document claimed `identity enroll` reused where
`identity add` refused to overwrite. That was simply wrong:
`AddIdentity` calls `CreateIdentity`, which reuses an existing file
(`TestCreateIdentityReusesAnExistingFile`), so both verbs behave
identically here. Never overwriting is a property of the private key
being the only copy there is, and it belongs to both. The reuse is what
makes `identity enroll` safe to run twice after a failed push, but it is inherited
rather than special.

### Enrolling with an identity you already have

The transcript above shows the *create* path. There is a second one, and
it prompts differently — worth showing rather than leaving a reader to
discover that the "reuses that key" convenience above is not free:

```
$ gage identity enroll --use personal
gage: reusing the existing local identity file for "personal" (device andrews-macbook-pro)

Unlock it so gage can read this device's public key — the request has to
name it. This is the passphrase you chose when the key was created.
Enter passphrase: ****

gage: this device is "andrews-macbook-pro", public key age1qz8x2...

Enrollment request published (expires in 24h).

  Enrollment code:  GAGE-2NPT-6J8K-QR40-XZ71
...
```

**Why it unlocks at all.** `formatIdentityFile` writes `# public key:
…` into the plaintext buffer, and that whole buffer is then encrypted —
there is no plaintext copy of the public key anywhere on disk. The
vault's own `.age-recipients` and `config.toml` can't supply it either:
if this device's key were listed there, it would already be a recipient
and would have nothing to enroll. So the rule is unavoidable and worth
stating in one line: **you cannot say "here is my public key" without
first opening the file that holds it.** Deriving a public key needs the
private key, and the private key is at rest under a passphrase.

Mechanically this is `CreateIdentity` finding the file and handing off to
`reuseIdentity`, which runs `decryptIdentityFile` — the ordinary
`PurposeUnlock` exchange, retry loop and all. It differs from the create
path in every way that matters to a caller:

| | create path | reuse path |
|---|---|---|
| Purpose | `PurposeCreate` | `PurposeUnlock` |
| Prompts | enter + confirm | enter once |
| A wrong answer | impossible — nothing to check against | retries, then `ErrWrongPassphrase` |
| Exit code | — | `exitcode.LockedOrAuth` is reachable |

**This is not a corner case.** It is the path behind this document's own
retry story — "safe to run twice after a failed push" means the second
run unlocks. It is also what a device hits after cloning, declining the
prompt, running `gage identity add`, and then enrolling; and after the
`vault remove` case that deliberately keeps an identity file (#39),
which is the most surprising of the three because the user may not
remember the file survived.

**The clone flow is unaffected.** A `clone` that finds an existing
identity doesn't prompt at all (see "What 'not a recipient yet' actually
means"), so this exchange is only ever reached from an explicit `gage
enroll`. The joining transcript above stays accurate for the path it
draws.

**Do not optimize this away with a cached public key.** The obvious
improvement is a plaintext `<device>.pub` sidecar next to the wrapped
file, which would remove this unlock, let `clone` answer "am I already a
recipient?" precisely instead of conservatively, and give hardware
methods — which write no `.age` file at all — a home for the same fact.
Three problems, one change. It is still wrong, for a reason that isn't
obvious until you look for it: a sidecar can drift from the file it
describes (an identity restored from backup, one of the two files
copied and not the other), and a drifted sidecar makes `identity enroll` publish
a public key whose private half this device no longer holds. Approval
then succeeds, the vault is re-encrypted to a key nobody has, and the
damage surfaces at first decrypt. Verifying the sidecar against the real
file to prevent that requires — an unlock.

So the unlock is load-bearing rather than incidental: it is what
guarantees the public key a request names matches the private key the
requester holds. It is also why gage's own enrollment path is immune to
the proof-of-possession gap described under "What the code does not
prove" — that gap is real, but only for a hand-crafted blob, never for a
request gage produced.

### What changes when a method other than `passphrase` exists

Yes, the flow adjusts — but only in one place, and it's the place that's
already abstracted for it. Worth tracing precisely, because the parts
that *don't* change are the interesting ones.

- **The identity-creation exchange changes, and nothing else in the
  flow does.** "Enter passphrase / Confirm passphrase" is a rendering of
  the existing `Prompter.Unlock` call with `Purpose: PurposeCreate`. The
  base design already says a YubiKey-method request "would carry `Kind:
  "yubikey"` and expect nothing but a touch signal." So those two lines
  become "Touch your YubiKey to generate a key…" and the rest of the
  output — the pubkey, the published request, the code, the instructions
  — is byte-for-byte the same. No new branch in the enrollment logic;
  the method dispatch happens one layer down, where it already lives.
- **The enrollment code is completely unaffected**, and that
  orthogonality is worth stating because it isn't obvious. The sealed
  request always uses a scrypt (passphrase) recipient — the *code* —
  regardless of what method the joining device uses to store its own
  key. The code authenticates the request; the method describes key
  storage. They never interact, so "YubiKey device joining a vault"
  needs no new sealing story.
- **`method` in the payload is already provisioned.** It's a new value
  in an existing field, not a new field, which is the whole reason it's
  carried today when only one value can appear.
- **The approver's side is entirely unchanged.** They unlock with
  *their* method, the re-encryption runs, a public key goes into
  `.age-recipients`. age takes mixed recipient types in one file — a
  YubiKey recipient is `age1yubikey1...` and sits beside any other — so
  nothing downstream distinguishes them.

**The one thing that needs care, and it is a trap this document set for
itself.** The clone prompt above is driven by `HasIdentity(vault,
device)`, which is a pure file-existence check on
`$GAGE_DATA/identities/<vault-id>/<device>.age`. Hardware-backed methods
**never write that file** — "ssh/yubikey/secure-enclave write nothing
here, since the private key never leaves external hardware, an agent, or
the OS keychain." So a fully working YubiKey-backed device would report
`false` forever, and `clone` would offer to enroll it on every single
clone, permanently, no matter how many times it had already been
approved.

That is a rework waiting to happen, so the requirement gets recorded now
even though nothing implements it yet: **the predicate behind the prompt
is "is this device registered for this vault," not "does a wrapped file
exist."** For `passphrase`/`age-key` those coincide, which is why
today's check is correct today. For a hardware method the answer must
come from this device's local registration — `[vaults.<name>].device`
and `.method` in global config — rather than from the filesystem.

Two constraints on whatever that becomes, both inherited rather than
new: it must stay answerable **without an unlock** (prompting during a
clone to report that the unlock was pointless is, per `clone.go`,
"exactly backwards"), and it must treat "the external holder isn't
plugged in right now" as an unlock-time problem, not a
not-registered signal — otherwise leaving your YubiKey at home would
offer to enroll you a second time. The exact spelling is settled when a
second method actually lands; what's settled *here* is that the
predicate is about registration, so that milestone extends this rather
than rewriting it.

### Why there is no `--enroll` flag

An earlier draft of this document put the behavior behind `gage clone
--enroll`. That was wrong, for a reason worth recording because this
codebase has already made and fixed the same mistake once: it asked the
user to answer a question `gage` already knows the answer to.

`clone` has always detected this exact condition and already says so —
"does not grant you access… gage tells you to run `gage identity add`"
in the base design, `TestCloneSaysSoWhenThisDeviceCannotDecryptAnything`
in the suite. A flag that opts into acting on a fact the tool just
printed carries no information: the only people who would ever pass
`--enroll` are the people who have already read this document, and they
are the ones who least need it.

The precedent is `gage init --remote` (#38). It used to fail when the
remote's host needed a token and none was stored, leaving the user to
run three commands to accomplish one thing; it now solicits the token
inline at the same prompt `gage auth login` uses. Same shape, same
answer: when the tool detects a missing prerequisite, asking beats
requiring a flag or a second command.

**Why it's a prompt rather than fully automatic.** Enrollment commits
and pushes. Plain `clone` is otherwise a read-only operation, and there
are real cases where someone clones a vault without meaning to announce
themselves to it — inspecting one, mirroring one, a CI checkout, a
shared or throwaway machine. Silently turning "fetch a copy" into "fetch
a copy and publish a request naming this machine" would widen what
`clone` does in a way its name doesn't suggest. Asking costs one
keystroke and keeps the property that `gage clone` writes nothing to the
remote unless a human said so. It is also simply the house style: the
trust cache asks, conflict resolution asks, `init --remote` asks.

**Non-interactive `clone` never enrolls and never blocks.** With no TTY
it prints the same "this device can't read anything here yet, run `gage
enroll`" message it prints today, and exits 0. This falls out of two
existing rules rather than adding a third: enrollment generates a
brand-new key, and `GAGE_PASSPHRASE` is explicitly never used where the
answer protects a brand-new key (see "Session model"), so there is
nothing a script could supply even if it wanted to. A scripted `gage
clone` behaves exactly as it does today.

**And no `--no-enroll` either.** An interactive user who doesn't want it
answers `n`; a script is never asked. A flag would exist only to skip a
prompt that already has a one-keystroke default.

**The offer is made exactly when the passphrase behind it can be
answered**, which is one rule rather than a second TTY test of its own.
Enrolling on a device with no identity requires a `PurposeCreate`
exchange, and `scriptPrompter` already decides where those can be
answered: a human when `--script FILE` runs at a terminal, and nowhere
at all under `--stdin` or with no terminal, where `noCreatePrompter`
refuses. Offering to enroll in a context that must then refuse the
passphrase would be asking a question whose only outcome is a failure —
so `clone` asks precisely where a create can be answered, and prints
today's message everywhere else.

### `gage identity enroll` under `--script` and `--stdin`

Worth stating rather than leaving to be derived, because the same command
has two different non-interactive outcomes and both are correct:

- **The create path refuses.** A new identity's passphrase is a
  `PurposeCreate` answer, and `GAGE_PASSPHRASE` deliberately never
  answers one — there is nothing to check it against, so a typo would be
  unrecoverable and undiscovered until the next unlock (see "Session
  model" and A18). Under `--script FILE` at a terminal the request
  passes through to the human and enroll proceeds normally. Under
  `--stdin`, or with no terminal, it fails at `exitcode.LockedOrAuth`
  before any key is generated.
- **The reuse path succeeds.** Opening an identity that already exists is
  an ordinary `PurposeUnlock`, so `GAGE_PASSPHRASE` answers it and a
  fully scripted enroll works end to end — key reused, request sealed,
  committed, and pushed. This is the path a CI-ish re-run after a failed
  push takes, and it is the reason enroll is scriptable at all.

**A refusal costs nothing that matters.** It lands at step 6 of
D-ENROLL-REMOTE's order, so the lock is released, no identity is
written, and nothing is sealed, committed, or pushed. What does survive
is the fast-forward from step 4 — the vault is simply more up to date
than it was, which is not a side effect anyone needs to undo.

### What "not a recipient yet" actually means

This is subtler than it looks, and the existing implementation already
picked the right answer for a reason worth preserving.

The obvious test — "is this device's public key in the vault's recipient
list?" — **cannot be run during a clone.** The local identity file is
passphrase-wrapped, so deriving this device's public key from it
requires an unlock. Prompting for a passphrase during a clone in order
to tell someone their unlock was pointless is, as `clone.go` puts it,
"exactly backwards."

So the condition `gage` tests is the one that costs nothing:
`HasIdentity(vault, device)` — does this machine hold a wrapped identity
for this vault at all? (Correct while `passphrase` is the only method;
see "What changes when a method other than `passphrase` exists" for why
this predicate has to become registration-based before a hardware method
lands.) That yields the rule already shipped, which the enrollment
prompt should adopt unchanged:

- **No local identity for this vault.** This device certainly isn't a
  recipient. Offer to enroll, generating a key.
- **A local identity exists.** Leave it alone — no prompt. It was set up
  deliberately, and `gage` can't cheaply tell whether it's already a
  recipient.

That second case is deliberately conservative rather than clever, and it
is not hypothetical: it's the state `vault remove` leaves behind when it
keeps an identity file (#39), and the state a re-clone after
`identity add` lands in. A device-name match against `[[recipients]]`
*would* be checkable without an unlock, but it proves less than it looks
like it does — the listed key under that name may be a different key
than the one this machine holds — so it isn't worth acting on
automatically. Anyone in that state runs `gage identity enroll` explicitly, which
reuses the existing key rather than generating a second one.

### Approving device

```
$ gage recipient pending --use personal
2 pending enrollment requests for "personal":

  7c1e4a90   expires 2026-09-08 06:12 EDT  (in 22h)
  e4f88b21   expires 2026-09-07 14:40 EDT  (in 6h)

Nothing here can decrypt anything until it is approved.
Run `gage recipient approve --code <code>` with a code you received
out-of-band.
```

The listing shows exactly what is knowable without a code: an ID and the
expiry from the filename, rendered in the approver's own local timezone.
It does not show device names, because those are under the seal. That is
a mild UX cost — an approver with several pending requests can't tell
them apart at a glance — and it is the right trade: the alternative
leaks who is joining to everyone with read access, in exchange for
saving one scrypt run.

It deliberately does *not* show a "requested at" time. That would have
to come from the introducing commit, which is both an O(history) lookup
and no more trustworthy than the filename; the expiry is the only
timestamp an approver acts on, and `created` is right there in the seal
once a code opens it.

```
$ gage recipient approve --code GAGE-7K4M-9QX2-P3RH-8WVN
Opened 1 of 2 pending requests with that code.

  device:  andrews-macbook-pro
  pubkey:  age1qz8x2...
  method:  passphrase
  created: 2026-09-07 10:12  (expires in 22h)

Add this device as a recipient of "personal"? It will be able to read all
47 entries, including everything already in the vault. [y/N] y

Unlocking "personal" (your own passphrase for this vault, not the code):
Enter passphrase: ****

Re-encrypting 47 entries... done.
Committed and pushed: +1 recipient, 47 entries re-encrypted, 1 request cleared.
```

`gage` tries the code against every pending request and approves the one
that opens. The approver never has to know or type an ID; the code
identifies the request as a side effect of authenticating it. An
optional positional ID narrows the search when someone wants to be
explicit.

### Approval always re-encrypts, and there is no `--reencrypt` flag

Approval re-encrypts every entry, unconditionally. The flag that exists
on `recipient add` is deliberately not carried over, because partial
access is not a state this design has any coherent use for — and,
worse, it's a state that spreads.

**It contradicts the first design principle.** "A vault is the unit of
trust. Each vault has its own set of recipients, and that list — nothing
finer-grained — is who can read it." A recipient who can read some
entries but not others is precisely the finer-grained tier that
principle rules out. When two groups should see different things, the
answer this design already gives is a second vault and `mv --to-vault`,
not a half-admitted recipient of one vault. So "added but can't read
history" isn't an access tier anyone chose; it's an artifact of age
baking recipients into each file at encryption time, leaking into the
user-facing model.

**The confusion is exactly as bad as it sounds.** A newly approved
device runs `ls`, sees 47 entries, opens three of them, and gets a
decryption failure on the fourth — with nothing in the interface
explaining why those three and not that one. The dividing line is
"written before or after you were approved," which is invisible: it
isn't the title, the age, or anything `ls` displays.

**And the state is contagious, which is the part that turns a wart into
a trap.** `reencryptTo` decrypts *every* entry with the acting
identity and fails on the first one it can't read
(`internal/gage/recipient.go:409`). So a device that was itself added
without `--reencrypt`:

- cannot repair its own access, and
- **cannot grant full access to anyone else** — its own
  re-encryption pass dies on the first entry predating its admission,
  after the trust-cache prompt and inside the write lock, with an error
  naming an opaque entry UUID.

Partial access therefore propagates to every recipient admitted by a
partial recipient, and each generation is harder to diagnose than the
last. A default that can quietly produce that is the wrong default.

**What it costs, stated honestly.** Every approval rewrites every entry,
so each one is a commit touching the whole vault. That inflates repo
size over time and puts a no-op-plaintext revision into every entry's
history, which `history --decrypt` will walk. Both are real, and both
are worth it: vaults are small (hundreds of entries of a few KB), the
operation is rare, the all-or-nothing machinery already exists, and the
alternative is an access model users can't predict.

**When the approver can't read everything.** With no flag to skip
re-encryption, an approver who is themselves partially admitted can no
longer approve at all — which is correct, since they were never in a
position to grant what they were being asked to grant. That must fail
*before* the write lock and *before* the confirmation, with an error
that names the real problem rather than surfacing a decryption failure
on a UUID — `ErrCannotGrantFullAccess`, declared under "Library
surface".

The message should say how many entries are unreadable and that someone
who can read the whole vault has to perform the approval. This is the
one place the feature surfaces pre-existing partial-access damage, and
it should do so legibly.

**`recipient add` is being changed to match**, since leaving the flag on
the older door would keep the vector that creates partial recipients in
the first place. That change is
[A19](../plans/gage-cli-design/open-questions.md#a19): accepted, applied
to the base design doc, and reflected in M9's test list — but **not yet
implemented in code**, which is tracked under "Accepted, not yet
implemented" in [index.md](../plans/gage-cli-design/index.md).

Implementing A19 before this feature is the tidier order: it makes
`ErrCannotGrantFullAccess` and the "always re-encrypt" behavior existing
machinery that enrollment reuses, rather than two doors arriving at the
same rule independently.

### The approver unlocks, and the unlock comes late

**Approval always needs the approver's own identity**, and in a session
where the vault is already unlocked there is no prompt at all — the
cached `Identity` is reused, exactly as for any other command.

The re-encryption pass is reason enough on its own. But the identity
would be required even if nothing were decrypted, for two further
reasons inherited rather than introduced here — worth recording so the
requirement doesn't look like a side effect of re-encryption that could
be optimized away:

- **`recipient add` already unlocks unconditionally.** `cmd/gage` wraps
  it in `withUnlockedVault` regardless of the flag, and approval is a
  recipient add — diverging would make the sanctioned path weaker than
  the manual one.
- **The `Prompter` rides on the `Identity`.** `AddRecipient` runs
  M10's blocking trust-cache check via `ident.frontend()`, which is how
  this codebase deliberately avoids "growing a `Prompter` parameter on
  every mutating method." A nil `Identity` would mean no way to ask the
  recipient-change question at all.

There's a security dividend worth naming: it means **a git-writer
holding no key of this vault cannot approve an enrollment.** They can
still hand-edit `.age-recipients` — that hole is unchanged and unclosed
— but the enrollment channel demands more than the direct-edit channel
does, rather than offering a cheaper way around it.

**The unlock happens after the confirmation, not before it**, and that
ordering is the point. Opening the sealed request needs no identity —
the seal is scrypt, keyed by the code — so `gage` can validate the code,
check expiry, show what the request claims, and take the `[y/N]` while
still holding nothing. Only once the answer is yes does it ask for a
passphrase. A wrong code, an expired request, or a declined prompt
therefore costs the approver no unlock at all.

**Which forces `ErrCannotGrantFullAccess` to sit after that
confirmation, not before it** — and an earlier draft of this document had
that wrong, because it copied a sentence from `recipient add` where the
same phrase means something different. The pre-flight is "decrypt every
entry with the acting identity," so it *needs* an unlocked `Identity`;
there is no cheaper way to ask the question, since an age file's X25519
stanzas carry an ephemeral share rather than the recipient's public key
and cannot be matched against a key you do not hold. In `recipient add`,
`withUnlockedVault` has already run by the time the pre-flight happens,
so "before any confirmation" there means *before M10's trust-cache
prompt*. `approve` has an extra, earlier confirmation that `add` does not
have, and running the pre-flight ahead of it would mean unlocking first —
giving up exactly the property this section exists to preserve.

So the order is:

1. `OpenEnrollment(requests, codes)` — no identity held, over the scope
   `PendingEnrollments` or `ResolveEnrollment` produced.
2. Device-name collision pre-check (`ErrEnrollmentNameTaken`) — still no
   identity; it reads the recipient list, which is plaintext.
3. Render what each request claims.
4. **The approve `[y/N]`.** Declining here costs no unlock, as does a
   wrong code or an expired request at step 1.
5. **Unlock.**
6. Full-access pre-flight — `ErrCannotGrantFullAccess`, still **before
   the write lock** and **before M10's recipient-change prompt**, which
   is what that guarantee was always actually about: the refusal never
   arrives mid-write, on an opaque entry UUID, after a human has answered
   the question about trusting the list.
7. Take the lock, **fetch and fast-forward**, re-verify each seal under
   it, then write. See "Approval fetches before it commits" below.

The cost of this ordering, stated so it isn't rediscovered as a bug: a
partially-admitted approver types their passphrase before learning they
cannot grant what they were asked to grant. That is one wasted unlock in
a state that is itself already damage, against a wrong code costing none
in the common case — and the common case is the one worth optimizing.

This is `sync`'s established pattern, not a new one: "**Sync unlocks
lazily.** … `gage sync` only prompts for an unlock when it reaches a
conflict whose resolution requires showing you plaintext." It's also the
same instinct as `clone` refusing to prompt for a passphrase in order to
report that the unlock was pointless. Concretely, `approve` cannot be a
blanket `withUnlockedVault` wrapper the way `recipient add` is; it
unlocks in the middle, after `OpenEnrollment` and the confirmation have
both succeeded.

### Approval fetches before it commits

**Approval pulls under its own write lock, which `recipient add` does
not do**, and the reason is a property of approval rather than a general
improvement to recipient writes.

Every approval rewrites **every entry** ("Approval always re-encrypts").
A commit that touches every entry, made onto a stale tip, does not
diverge in one place — it diverges in *all* of them, and every entry
another device touched meanwhile becomes an entry conflict the approver
resolves one `[l/r/b]` answer at a time through `gage sync`. That is a
disproportionate outcome for a command whose whole job is to let one more
device in, and it is entirely avoidable: catching up first costs one
fetch.

`recipient add --reencrypt` has the same shape and does not do this. The
difference is frequency, not mechanism. `--reencrypt` is a rare
deliberate act; approval is the ordinary way a device joins a vault, so
its stale-tip case stops being a corner and starts being a Tuesday.

Three constraints, all inherited rather than invented:

- **It pulls inside the lock, via the unexported `v.pull`.** Same trap as
  enroll, same reason: `Pull` takes the lock itself via `underWriteLock`
  and `vaultlock` is not re-entrant, so calling it from inside approval's
  lock blocks against itself until the acquisition times out and reports
  contention against this very process. See D-ENROLL-REMOTE's "Which
  means enroll cannot reach the network through the existing public sync
  entry points" — the paragraph is about enroll and the mechanism is not.
- **A divergence refuses before anything is written, with the *ordinary*
  advice.** `v.pull` reports it with a nil error and `SyncReport.Diverged`
  set, so it is checked for rather than caught. Unlike enroll, no new
  error is needed and none should be minted: an approver can decrypt, so
  `gage sync` is exactly the command that resolves this for them.
  `ErrEnrollmentDiverged` exists because that advice is *wrong* for a
  device with no key, which is not the situation here.
- **An unreachable remote does not fail the command.** This is where
  approval and enroll genuinely differ. An unpublished enrollment request
  accomplishes nothing, so enroll refuses; an approval that lands locally
  is real work — the vault is re-encrypted and the recipient is listed —
  and refusing it because the network is down would be the "warn and
  proceed" rule broken in the direction it exists to prevent. So a failed
  fetch warns and the approval proceeds, and the existing failed-push
  warning below covers what the human then has to do.

**One consequence to expect rather than rediscover: the fetch can resolve
the request out from under the run.** Another device may have approved or
denied it in the window between `OpenEnrollment` and the lock. The
re-verification under the lock finds no file and the run reports
`ErrEnrollmentNoSuchRequest` — the request is genuinely no longer
pending, which is what that error already says — with a message naming
the likely cause rather than implying the ID was mistyped. Nothing is
committed, and the state is correct in both directions: if it was
approved elsewhere, the device already has access; if it was denied, it
should not be granted any.

Note this window pre-dates the fetch rather than being created by it —
the same swap the re-verification under the lock already exists to catch
("Otherwise a request could be swapped between the moment it was shown to
the human and the moment its key was written"). What the fetch changes is
how often it happens, which is the argument for naming the outcome
instead of leaving it to be discovered as an internal error.

One consequence to expect rather than be surprised by: the approver may
answer **two** different `[y/N]` questions. The first is "approve this
device," from this feature. The second is M10's recipient-change
warning, which fires inside `AddRecipient` if someone *else* changed the
recipient list since this device last encrypted. They're genuinely
different questions — "should this device be let in" versus "do you
trust the list you're about to encrypt to" — and collapsing them would
hide the second one behind the first.

Finally, because `OpenEnrollment` reads outside the vault write lock and
`ApproveEnrollments` writes under it, the sealed request is re-read and
re-verified under the lock before anything is committed. Otherwise a
request could be swapped between the moment it was shown to the human
and the moment its key was written into the recipient list.

**Approving several devices at once is one re-encryption pass**, which is
the whole reason batch approval exists:

```
$ gage recipient approve --code GAGE-7K4M-... --code GAGE-2NPT-...
```

Each device generated its own code, so approving N devices means N codes
— but it means *one* decrypt-and-rewrite of every entry, one commit, and
one push, rather than N: every entry re-encrypted in the working tree
first, then the recipient files, every touched entry, and the removal of
every approved request's file land in exactly one commit. A crash partway
leaves HEAD untouched and every request still pending.

**That is the existing all-or-nothing machinery, but it is not
`AddRecipient` as it stands today**, and an earlier draft's "reuses it
unchanged" was wrong in a way worth correcting here rather than
discovering in E4. `AddRecipient` is a complete write: it takes the lock,
runs the reset, asks M10's question, appends **one** recipient, commits,
and regenerates the cache (`internal/gage/recipient.go`). Calling it N
times would take N locks, ask N trust questions, run N re-encryption
passes and produce N commits — the exact shape batch approval exists to
avoid.

What is needed is an N-recipient form of that body: the same sequence,
with the append taking a slice and the commit taking the pending files to
delete alongside the entries it rewrites. `AddRecipient` becomes its
one-recipient caller, which keeps a single implementation of "add
recipients and re-encrypt" rather than two that must be kept honest
against each other. The extraction belongs with A19's work on
`recipient add` rather than with approval — see the plan's E1b — so that
approval remains what this document describes it as: a wiring of existing
machinery.

Denying is the same shape without the code, since refusing something
requires no proof of anything:

```
$ gage recipient deny e4f88b21
Removed pending request e4f88b21. Nothing was granted; nothing to re-encrypt.
```

---

## Expiry, revocation, and pruning

- **Default lifetime is 24 hours** (`--ttl` on `gage identity enroll` to change),
  with a hard ceiling of 7 days that `gage` refuses to exceed.
- **Approval checks the sealed `expires` and refuses a stale request**,
  regardless of what the filename or the listing said. This is the only
  expiry check that is load-bearing.
- **Approval deletes the request file in the same commit** as the
  recipient change. A request is one-shot by construction: there is
  nothing left to replay.
- **Pruning reads the filename's epoch** — no history walk, no unlock, no
  code. A file whose epoch is in the past is removed. `gage` prunes
  opportunistically when it is already writing (on approve, deny, or the
  next recipient change) rather than taking the lock to do housekeeping
  alone.
- **A filename claiming an expiry more than the ceiling beyond *now* is
  treated as already expired**, and pruned. This is what keeps the
  7-day ceiling load-bearing now that pruning no longer depends on
  commit dates: a legitimate request is created with an expiry at most
  `now + 7d`, so at any later moment its epoch is at most 7d in the
  future. Anything claiming more was forged or written by a badly-skewed
  clock, and either way is not something to keep forever. Without this
  clamp a hostile git-writer could park `<uuid>-99999999999.age` in the
  tree permanently. Only clock skew beyond roughly six days trips it,
  which is well outside what a working git remote tolerates anyway.
- **A file whose name doesn't parse is left alone, not deleted.** An
  unparseable name is by definition not something gage wrote, so gage
  skips it — the same posture `ListIdentities` takes toward strays, and
  the conservative direction: refusing to delete a file it doesn't
  understand.

### An ID always means the UUID, never the whole filename

Every place a human types an ID — `approve <ID>`, `deny <ID>`, and the
listing that shows them — resolves **against the UUID portion of the
filename only**. Never the epoch, never the filename as a whole.

This is not fussiness; it is the same hazard the base design's
resolution order exists to prevent. "A hex-spellable title — `dead`,
`beef`, `cafe`, even a single letter — is exactly the kind of string an
entry's own randomly-generated UUID can coincidentally contain." Now
that a pending filename carries ten digits of epoch, a short id like
`1788` could match the *expiry* of one request and the *id* of another,
and a naive substring search over whole filenames would resolve to
whichever it hit first. `gage deny 1788` deleting the wrong request
because the digits happened to land in a timestamp is precisely the
class of bug that ordering was written to close.

So the epoch is parsed off before any matching happens, and it is not
part of the searchable text.

**What an ID resolves *to* is the base design's rule, reused rather than
reinvented.** "Addressing entries" already settled how a human-typed
identifier becomes one thing: exact match first, then substring, then —
when more than one survives — a candidate list, never a guess. Pending
requests get the same order, over the UUID portion only:

1. **Exact UUID.** The whole 36 characters.
2. **Substring of the UUID.** `e4f88b21` in the transcripts above is
   this case, and it is the one people actually type.
3. **More than one match: ambiguous.** The matching `PendingRequest`s
   come back as a *value* for `cmd/gage` to render, exactly as an
   ambiguous entry query returns a candidate list rather than printed
   text. `exitcode.Ambiguous`.
4. **No match: `ErrEnrollmentNoSuchRequest`**, at `exitcode.NotFound`.

Reusing the order matters more here than it would for a read, because
`deny` deletes. A resolution rule that guessed — first match wins, or
silently picking the soonest to expire — would delete a request the human
did not name, and the thing they would have to notice is a UUID prefix.
Two pending UUIDs sharing an eight-character prefix is vanishingly
unlikely; a human typing three characters is not, and that is the case
the candidate list is for.

Note this is the *only* resolution `gage` performs on a pending request.
There is no title to match, no metadata outside the seal, and
deliberately no matching on device name — the name is sealed, which is
the whole point of the filename scheme.

### Clock skew, in the direction the ceiling doesn't cover

The far-future clamp above handles a *fast* clock. A **slow** one fails
differently and more confusingly: a device two days behind seals
`expires` at a moment already in the past, so the request is dead on
arrival. The joining device reports success and prints a code; the
approver sees a request that is already expired, or nothing at all
because it was pruned.

`gage` cannot reliably detect this locally — by its own clock the expiry
is always in the future, which is the whole problem. Two cheap measures
make it legible rather than baffling:

- **At approve time, diagnose it.** Both `created` and `expires` are in
  the seal. A request whose `expires` is in the past *and* whose
  `created` is also in the past by less than its own TTL was expired
  before it was written. That is not an expiry, it is a wrong clock, so
  it gets its own error — `ErrEnrollmentClockSkew`, declared under
  "Library surface" — rather than being reported as a stale request. The
  two are separated because their fixes share nothing: one says fix that
  machine's clock and enroll again, the other says ask for a fresh
  request.
- **At enroll time, warn on the available signal.** A freshly cloned or
  fetched vault carries commit timestamps written by other devices. If
  local time is meaningfully behind HEAD's committer timestamp, this
  machine's clock is probably wrong — one field read, no history walk.
  Warn and proceed, the same posture as an unreachable network or a
  failed page-lock; refusing to enroll over a heuristic would be worse
  than publishing a request that might expire early.
- **An interrupted enroll can strand an uncommitted file**, and the
  machinery that already exists handles it — no new path, and none
  wanted. Enroll seals the request, writes it into `pending/`, then
  commits; process death between those two steps leaves an untracked file
  behind. The next write on that machine discards it: every mutating
  method runs `resetDirtyWorkTree` under the vault lock, which warns once
  naming what it found and resets to HEAD, **deleting untracked files
  along with modified ones, across the whole working tree**.

  An earlier draft of this section said that reset was "scoped to
  `entries/`" and concluded a stray would survive to expire on its own
  epoch. That was wrong about the shipped code, and the base design doc
  was wrong in the same way — corrected as
  [A21](../plans/gage-cli-design/open-questions.md#a21). The scope is
  deliberate: a crash after `--reencrypt` writes the recipient files but
  before it commits dirties those two as well, so a reset scoped to
  `entries/` would leave the half-migrated state `--reencrypt` exists to
  prevent.

  What that means here, precisely, since it is the opposite of what the
  draft claimed:

  - A stray pending file lives until the next write on that machine,
    which may be seconds away or never come. `recipient pending` — a
    read, which takes no lock and resets nothing — lists it meanwhile.
  - It is inert for as long as it exists, and visible only on the machine
    that failed to publish it. Either way it grants nothing, which is why
    tolerating it was always the right call even though the reason given
    was wrong.
  - **No commit can sweep it up**, and that holds structurally rather
    than by anyone remembering to stage carefully: the reset runs
    *before* each write's own changes, so by the time anything is staged
    the stray is gone. An earlier draft made "stage explicit paths, never
    the `pending/` directory wholesale" a hard requirement on every commit
    this feature makes. That would have meant replacing
    `gitrepo.CommitAll` — `git add -A`, which every write path in the
    codebase uses — with a path-scoped variant, to buy a guarantee the
    reset already provides. The requirement is dropped; why it is safe to
    drop is recorded here rather than left as a silent omission.

  `D-ENROLL-REMOTE` already narrowed the window: enroll holds the write
  lock across the whole sequence, so another process cannot interleave
  with it. What remains is genuine process death.

- **Replay within the TTL is accepted, not defended against.** Someone
  with read and write access could copy a pending blob and re-commit it.
  They still cannot open it, and approval still requires a human with the
  code to say yes — so a replay costs an attacker exactly what the
  original did, which is nothing they didn't already have.

---

## Command reference (additions)

```
gage identity enroll [--use NAME] [--device NAME] [--ttl DURATION]

    Publishes an enrollment request for this device: creates a local
    identity if one does not already exist for this vault (reusing it if
    it does), seals device name and public key to a freshly generated
    code, commits it to .gage/pending/, pushes, and prints the code.

    The two paths prompt differently. Creating a key asks for a new
    passphrase twice; reusing an existing one asks for that key's
    existing passphrase once, because the public key the request has to
    name can only be derived by opening the private key. A wrong
    passphrase on the reuse path fails with ErrWrongPassphrase and
    publishes nothing. See "Enrolling with an identity you already have".

    Fetches and fast-forwards before committing, and fails outright if
    the remote is unreachable — unlike a read, an unpublished enrollment
    request accomplishes nothing, so producing a key and a local commit
    it cannot publish would be worse than stopping. It fails the same way,
    with its own error, if the fetch leaves the two sides diverged rather
    than fast-forwardable: `gage sync` is not an answer available to a
    device that cannot decrypt, and the local commit in the way is an
    unpublished pending/ file that is inert and safe to discard. See
    D-ENROLL-REMOTE.

    Requires git *write* access. A token that can read but not write is
    only discovered at the push, which reports that the identity was
    created and the request committed locally but not published, and
    names `gage auth login`. A device with read-only access has the
    manual path (`gage identity add`, then `gage recipient add`
    elsewhere), which remains fully supported and is also the answer for
    air-gapped transfer (see D-ENROLL-PRINT-ONLY).

    Reports success and publishes nothing, before generating any key,
    when this device's recorded public key is already a recipient of the
    vault — there is nothing to ask for. Checked by key rather than by
    name, so it is also correct for a device an approver relabeled.

    Refuses, before generating any key, a device name that already
    labels a *different* recipient of this vault — and says to pass
    --device. When global config records no pubkey for this vault, the
    key check cannot run and this is the only check left; see
    D-ENROLL-COLLISIONS.

gage clone <remote-url> [--name NAME] [--dir PATH] [--device NAME]

    Unchanged, except for what happens after a successful clone when this
    device can't read the vault. Interactively, clone offers to enroll
    ([Y/n]) and runs the same path `gage identity enroll` does on yes —
    under the device name clone already resolved, so an explicit --device
    carries through to the request rather than being re-derived. With no
    TTY, or on no, it prints its can't-read-anything message and exits 0.

    That message is reworded. It names `gage identity add` today, which
    is still correct and still supported, but it is no longer the whole
    answer: `gage identity enroll` is the one that also publishes, and it
    is what someone who declined the offer or ran without a terminal
    should be pointed at. The manual path stays in the message for the
    read-only-remote and air-gapped cases (D-ENROLL-PRINT-ONLY).

    No flag gates the offer — see "Why there is no --enroll flag". The
    condition is one clone already detects and already reports.

gage recipient pending [--use NAME]

    Lists pending enrollment requests: ID and expiry, the latter parsed
    from the filename and rendered in the local timezone. Needs no
    unlock, no code, and no history walk — everything shown comes from
    the filename. Exits 0 with "no pending requests" when there are none.

gage recipient approve [ID...] --code CODE [--code CODE ...]
                       [--use NAME] [--device NAME]

    Opens each pending request with the given code(s), shows what each
    one claims, and — on confirmation — adds them to the recipient list,
    re-encrypts every entry so they can read the whole vault, and
    removes every approved request file, all in one atomic commit.

    There is no --reencrypt flag: approval always re-encrypts, because a
    recipient who can read only part of a vault is a state this design
    has no use for and cannot cleanly recover from. See "Approval always
    re-encrypts".

    Needs the approver's own identity, and unlocks for it only after the
    code has opened a request and the confirmation has been answered —
    so a wrong code or a declined prompt costs no unlock.
    Already-unlocked session vaults reuse the cached Identity and prompt
    for nothing.

    --device relabels a request whose claimed device name now collides
    with an existing recipient, which is the approver's call to make: the
    code authenticated the key, not the label. Without it such a request
    would be permanently unapprovable.

    --device is only accepted when the run resolves to exactly one
    request — because one flag cannot name which of several requests it
    relabels. Pass an ID to narrow a multi-request run down to one.
    Otherwise it is a usage error that says so, rather than guessing.

    A request whose pubkey is already a recipient is not an error: the
    request is cleared, nothing is re-encrypted, and gage reports that
    the device already had access.

    Fetches and fast-forwards under its own write lock before it
    re-verifies and writes, because an approval rewrites every entry and
    one made onto a stale tip conflicts on every entry. A divergence
    refuses before anything is written, with the ordinary `gage sync`
    advice — correct here, unlike on the joining side. An unreachable
    remote warns and proceeds, since an approval that lands locally is
    real work. See "Approval fetches before it commits".

    Refuses an expired request — including one whose sealed expires
    claims more than the TTL ceiling beyond now, which is treated as
    expired rather than as corruption — a request whose sealed request_id
    does not match its filename's UUID portion, a request sealed for a
    different vault (ErrEnrollmentWrongVault, naming the vault it is
    actually for), and a code that opens
    nothing. A device-name collision is refused before the lock, before
    any unlock, and before the confirmation. An approver who cannot
    itself read every entry is refused before the lock and before M10's
    recipient-change prompt, but necessarily after the unlock — the check
    is a decryption pass and has no identity-free form. See "The approver
    unlocks, and the unlock comes late".

    Every supplied --code must open a pending request. One that opens
    nothing fails the whole run with ErrEnrollmentCodeWrong, before any
    confirmation and before anything is committed: a mistyped code is far
    likelier than a deliberately surplus one, and partially approving a
    batch the human confirmed as a batch is the wrong way to be helpful.

    Tries a code against at most 32 live requests. Beyond that it
    refuses with ErrEnrollmentTooManyPending and says to name an ID,
    which resolves from the filename and costs one attempt per code
    however full the directory is. The bound is on work, not on how many
    devices may enrol; expired requests are pruned before it is applied,
    so they never count toward it. See D-ENROLL-SEAL-COST.

gage recipient deny <ID> [--use NAME]

    Removes a pending request without granting anything. Needs no code
    and no unlock: refusing to grant access requires no proof, it
    changes no recipient list, and anyone with git write access could
    delete the file directly anyway. It still takes the vault write lock
    and commits, like any other write — and warns if the push fails,
    since a deny that never reached the remote leaves the request live
    for every other device.
```

The confirmation prompt states the consequence in the terms the approver
actually cares about — "it will be able to read all 47 entries,
including everything already in the vault" — rather than naming a
mechanism. Re-encryption is how that happens, not a thing the approver
should have to reason about.

---

## Library surface

Everything here obeys the library/CLI split: `internal/gage` returns
typed values and never renders or reads a terminal.

```go
// EnrollmentRequest is what a joining device produced. Code is the
// generated out-of-band secret — the one field cmd/gage must display
// and the library must never persist.
//
// Published is false in exactly one case: this device's recorded pubkey
// is already a recipient of the vault, so there was nothing to ask for.
// Every other field is then zero, Code included — no key was generated,
// nothing was sealed, and nothing was committed or pushed. It is a
// success, not an error, and it mirrors ApprovalOutcome.Added on the
// other side of the same duplicate (see D-ENROLL-COLLISIONS).
type EnrollmentRequest struct {
    ID        string
    Device    string
    Pubkey    string
    Method    string
    Created   time.Time
    Expires   time.Time
    Code      string
    Published bool
}

// PendingRequest is one unopened request, built entirely from its
// filename — no git history walk, no unlock, no code.
type PendingRequest struct {
    ID      string
    Path    string
    Expires time.Time // unauthenticated; parsed from the filename's epoch.
                      // Display and pruning only — approval reads the
                      // sealed copy. See D-ENROLL-EXPIRY-IN-NAME.
}

// OpenedRequest is a PendingRequest whose seal a code has opened. Every
// field here is authenticated — and validated: authenticated says the
// bytes are unaltered, not that they are well-formed, so device, pubkey,
// method, request_id, vault_id and the two timestamps are all checked
// before one of these is returned.
//
// There is deliberately no VaultID field. The sealed value is compared
// against this vault's own id and the request refused on a mismatch, so
// every OpenedRequest that exists is one for this vault — a field would
// be a constant the caller could only re-check. See "The sealed payload
// is untrusted input" and "The request names the vault it is for".
type OpenedRequest struct {
    ID      string
    Device  string
    Pubkey  string
    Method  string
    Created time.Time
    Expires time.Time
}

// Approval pairs an opened request with the label the approver chose for
// it. An empty Label means "use what the request claims"; a non-empty
// one overrides it, which is how a device-name collision gets resolved
// without a new round trip (D-ENROLL-COLLISIONS).
//
// The label rides here rather than being written onto OpenedRequest,
// because every field on that type is authenticated and a label the
// approver typed is not.
type Approval struct {
    Request OpenedRequest
    Label   string
}

// ApprovalOutcome is what happened to one request.
type ApprovalOutcome struct {
    Request OpenedRequest
    Label   string // the name actually recorded in the recipient list
    Added   bool   // false when this pubkey was already a recipient
}

// ApprovalResult covers the whole batch, which shares one re-encryption
// pass and one commit.
type ApprovalResult struct {
    Outcomes    []ApprovalOutcome
    Reencrypted int
    Commit      string
}

// AmbiguousRequestError carries the requests an ID matched when it
// matched more than one. It is a value cmd/gage renders, not printed
// text — the same shape as M5's CandidateList, for the same reason.
type AmbiguousRequestError struct {
    ID      string
    Matches []PendingRequest
}

// Enroll takes a context because it is the one write in gage that
// blocks on the network and *fails* when it can't reach it. Every other
// exported method that touches a remote takes one (Pull, Push, Sync,
// SyncResolving); the writes that push opportunistically do not, because
// pushAfterWrite warns and proceeds and so has nothing a caller would
// want to cancel. Enroll's fetch is a hard precondition, which puts it
// on the first list.
func (v *Vault) Enroll(ctx context.Context, device string, ttl time.Duration, p Prompter) (EnrollmentRequest, error)
//
// PendingEnrollments returns the *live* requests: one whose filename
// epoch is in the past, or more than the ceiling beyond now, is filtered
// out rather than returned. Filtered, not deleted — listing is a read,
// takes no lock and writes nothing, so removing the file is pruning's
// job and rides the next write (see "Expiry, revocation, and pruning").
//
// That split is load-bearing in two places. `recipient pending` must not
// present an expired request as pending, and `D-ENROLL-SEAL-COST`'s
// 32-request bound counts what this returns — so ordinary neglect can
// never look like an attack, whether or not a write has come along to
// prune yet.
func (v *Vault) PendingEnrollments() ([]PendingRequest, error)

// ResolveEnrollment applies the resolution order in "An ID always means
// the UUID, never the whole filename": exact, then substring, over the
// UUID portion only. Ambiguity is *AmbiguousRequestError; no match is
// ErrEnrollmentNoSuchRequest. Both approve and deny go through it, so
// there is one resolution rule rather than one per verb.
func (v *Vault) ResolveEnrollment(id string) (PendingRequest, error)

// OpenEnrollment tries each code against each request in the scope it is
// handed, and returns the ones that opened.
//
// The scope is a parameter rather than "every pending request" because
// that is what makes `ErrEnrollmentTooManyPending`'s escape hatch work
// *by construction* rather than by a second code path. A broad run
// passes PendingEnrollments()' result and is refused when that exceeds
// the bound; an ID-scoped run passes the one PendingRequest that
// ResolveEnrollment returned, and one request never exceeds it. Neither
// caller opts into or out of the bound — they differ only in what they
// ask about, which is the difference the human expressed by typing an ID.
//
// It takes no Identity: opening is keyed by the code. That signature is
// what makes the late unlock possible, so it is fixed here rather than
// arrived at in E4.
func (v *Vault) OpenEnrollment(requests []PendingRequest, codes []string) ([]OpenedRequest, error)
func (v *Vault) ApproveEnrollments(approvals []Approval, ident *Identity) (ApprovalResult, error)
func (v *Vault) DenyEnrollment(id string, p Prompter) error
```

`ApproveEnrollments` returns `ApprovalResult` rather than the existing
`RecipientChange`. An earlier draft reused `RecipientChange` on the
grounds that approval *is* a recipient change — true, but that type
describes exactly one device (`Device`, `Pubkey`), and batch approval
resolves N requests under one commit, each of which may have been
relabeled or turned out to be a no-op. Reusing it would have forced the
caller to reconstruct per-request outcomes it no longer had. The shared
facts — how many entries were re-encrypted, which commit — stay on the
result once, where they belong, since they are properties of the batch
rather than of any one device.

Typed errors at the boundary, so `cmd/gage` owns retry and exit-code
policy exactly as it does for `Unlock`:

```go
var ErrEnrollmentExpired    = errors.New("gage: this enrollment request has expired")

// ErrEnrollmentCodeWrong covers both ways a code fails to open anything:
// one that is well-formed but opens nothing, and one rejected up front
// on length or alphabet (D-ENROLL-CODE-FORMAT) without a decryption
// being attempted at all. They are deliberately the *same* error despite
// being distinguishable, because the caller does the same thing with
// both — cmd/gage's retry loop is keyed on this value, and a typo is
// precisely the case that loop exists for. Splitting them would exit the
// command on the likeliest mistake while retrying the rarer one.
//
// The validation still happens before any decryption; what that buys is
// cost, not a different answer. See "Batch enrollment approval costs one
// scrypt run per code per pending request".
var ErrEnrollmentCodeWrong  = errors.New("gage: no pending request opened with that code")
var ErrEnrollmentIDMismatch = errors.New("gage: this request's sealed id does not match its filename")
// (compared against the filename's UUID portion only — the epoch portion
// is an unauthenticated hint and is deliberately not part of the check.)
var ErrEnrollmentNoRemote   = errors.New("gage: this vault has no remote to publish an enrollment request to")

// Distinct from NoRemote: a remote exists but could not be reached. Kept
// separate because the fixes differ — configure one, versus get on the
// network — and cmd/gage decides which to say. See D-ENROLL-REMOTE.
var ErrEnrollmentRemoteUnreachable = errors.New("gage: could not reach this vault's remote, so the enrollment request was not published")

// Distinct again from both: the remote was reached, but the two sides
// have diverged and the pull was not a fast-forward. Separate because
// the advice a joining device can act on is unique to it — the standard
// "run gage sync" is wrong for a device that cannot decrypt, and the
// local commit in the way is an unpublished pending/ file, which is
// inert and safe to discard. Note that v.pull reports this with a nil
// error and SyncReport.Diverged set, so it has to be checked for rather
// than caught. See D-ENROLL-REMOTE.
var ErrEnrollmentDiverged = errors.New("gage: this vault's local and remote histories have diverged, so the enrollment request was not published")

// Raised when a code opens a request whose sealed payload is not
// something gage could have written — a device name outside the
// filesystem-safe allowlist, a pubkey that is not an age recipient, an
// unknown method, a request_id that is not a UUID.
//
// Deliberately *not* ErrEnrollmentCodeWrong: the code worked. Reporting
// it as a wrong code would put cmd/gage's retry loop into a re-prompt no
// correct code can ever satisfy. See "The sealed payload is untrusted
// input".
var ErrEnrollmentMalformedRequest = errors.New("gage: this enrollment request's sealed contents are malformed")

// Raised when a code opens a well-formed request sealed for a *different*
// vault — the sealed vault_id does not match this vault's [vault].id.
//
// Distinct from both its neighbours on purpose. Not ErrEnrollmentCodeWrong:
// the code worked, and cmd/gage's retry loop would otherwise re-prompt for
// a code that cannot be right here however carefully it is typed. Not
// ErrEnrollmentMalformedRequest either: the payload is well-formed and gage
// produced it, so reporting corruption would send a human looking for
// damage that isn't there. The message names the vault the request is for.
// See "The request names the vault it is for".
var ErrEnrollmentWrongVault = errors.New("gage: this enrollment request is for a different vault")

// Raised when a request's device name — the one it was sealed with — now
// labels a different recipient. Recoverable by the approver alone, via
// --device; the key was authenticated, the label was not.
var ErrEnrollmentNameTaken = errors.New("gage: another recipient of this vault already uses this request's device name")

// Raised before the write lock and before M10's recipient-change prompt,
// so a partially-admitted approver learns why rather than hitting a
// decryption failure on an opaque entry UUID mid-operation. It follows
// the approve confirmation and the unlock, because the check is a
// decryption pass — see "The approver unlocks, and the unlock comes late".
var ErrCannotGrantFullAccess = errors.New("gage: this device cannot read every entry in the vault, so it cannot grant full access")

// Raised when an ID names no pending request. Its sibling — an ID
// matching several — is not an error but a candidate list, per
// "An ID always means the UUID, never the whole filename".
var ErrEnrollmentNoSuchRequest = errors.New("gage: no pending enrollment request with that id")

// Raised when a run with no ID would have to try more than 32 live
// requests, which is a bound on work rather than on how many devices may
// enrol: the count comes from a directory any git writer can fill, and
// each attempt is a KDF run. Refused before any decryption, and
// recoverable in place by naming an ID — which resolves from the
// filename alone and costs one run per code however full the directory
// is. See D-ENROLL-SEAL-COST.
var ErrEnrollmentTooManyPending = errors.New("gage: too many pending enrollment requests to try a code against them all; approve or deny by id")

// Raised when a request's sealed expires is in the past *and* its
// created is in the past by less than its own TTL: it was expired before
// it was written, which is a wrong clock rather than a stale request.
// Distinct from ErrEnrollmentExpired because the fix is completely
// different — fix the joining device's clock and enroll again, versus
// ask for a fresh request. See "Clock skew, in the direction the ceiling
// doesn't cover".
var ErrEnrollmentClockSkew = errors.New("gage: this enrollment request expired before it was created, so the requesting device's clock is wrong")
```

**Exit codes**, mapped here rather than left to each command, since the
taxonomy is a contract every command answers to and three of these have
a non-obvious home:

| Error | Code | Why |
|---|---|---|
| `ErrEnrollmentCodeWrong` | `LockedOrAuth` | A secret that didn't open what it was meant to open — the same shape as a wrong passphrase, and what a retry loop keys on |
| A malformed code (length, alphabet) | `LockedOrAuth` | Same error, same code, on purpose: it is a typo, which is what the retry loop is for. `Usage` would exit on the likeliest mistake |
| `ErrEnrollmentExpired` | `Conflict` | A state a human resolves, by asking for a fresh request |
| `ErrEnrollmentClockSkew` | `Conflict` | Same, with a different fix |
| `ErrEnrollmentIDMismatch` | `Conflict` | Vault state a human must look at; never a usage mistake |
| `ErrEnrollmentNameTaken` | `Conflict` | Resolvable in place, with `--device` |
| `ErrCannotGrantFullAccess` | `Conflict` | Pre-existing damage surfacing; needs a different human |
| `ErrEnrollmentNoRemote` | `Usage` | The vault was never configured for this; `gage git set-remote` |
| `ErrEnrollmentRemoteUnreachable` | `Unreachable` | The code that already exists for exactly this |
| `ErrEnrollmentDiverged` | `Conflict` | Vault state a human resolves — the same answer the base design gives divergence everywhere else |
| `ErrEnrollmentMalformedRequest` | `Conflict` | A file in the vault a human must look at; never a usage mistake, and never a retryable code |
| `ErrEnrollmentWrongVault` | `Conflict` | Resolvable in place, by approving against the vault the request names — the same shape as `ErrEnrollmentNameTaken`'s `--device`, and never a retryable code |
| `ErrDeviceNameTaken` (pre-existing) | `Conflict` | Enroll reuses `AddIdentity`'s error rather than minting a second one; listed so this feature's codes are all in one place |
| `ErrEnrollmentNoSuchRequest` | `NotFound` | Matches the entry resolver's answer to the same question |
| `ErrEnrollmentTooManyPending` | `Conflict` | Vault state a human resolves, in place, by naming an ID — the same shape as `ErrEnrollmentNameTaken`'s `--device`, and unlike the `Usage` rows, which reject a command line before anything is read |
| An ambiguous ID | `Ambiguous` | Likewise — a candidate list, not a failure |
| `--ttl` out of range, `--device` with a multi-request run | `Usage` | Rejections of the command line itself |

**Two different errors describe one device-name collision, deliberately.**
`cmd/gage`'s pre-check returns `ErrEnrollmentNameTaken`, which names
`--device` as the fix. `AddRecipient`'s own check, under the write lock,
returns the pre-existing `ErrRecipientExists`. Both are correct for where
they sit: the first is the UX path, reached in every ordinary run; the
second is the race, reached only when another writer took the name in the
window between them, and it belongs to `recipient add`'s vocabulary
rather than enrollment's. Unifying them would mean either teaching
`AddRecipient` about enrollment or losing the `--device` advice in the
common case.

`ApproveEnrollments` takes no `reencrypt` parameter — there is nothing
to decide. See "Approval always re-encrypts".

**Collisions are checked twice, on purpose.** `cmd/gage` checks the
recipient list as soon as a request is opened, so a name clash is
reported before the confirmation and before any unlock — the same
fail-early posture as `ErrCannotGrantFullAccess`. `AddRecipient` checks
again under the write lock, which is the authoritative one: the first
check is UX, the second is correctness, and only the second can be
trusted against a concurrent writer.

**No part of the code exchange touches `Prompter` — no new method for
it, and no new `UnlockKind`** (see D-ENROLL-PROMPTER). The one method
this feature does add, `ConfirmDefaultYes`, belongs to clone's offer and
is described immediately below; it has nothing to do with codes.
The approver's code is an argument to
`OpenEnrollment`, not something the library asks for mid-flight;
`cmd/gage` gathers it from `--code` or from the existing
`Prompter.Value`, and owns the retry loop around
`ErrEnrollmentCodeWrong`. The joining side needs no code prompt at all,
since `gage` generates the code — it does still use the existing
`PurposeCreate` unlock exchange to protect the new identity file, which
is unchanged behavior.

**Clone's offer needs a default-yes confirm, which `Prompter` does not
have.** An earlier draft of this document claimed the prompt was just
`Prompter.Confirm` and needed no new interface surface, while drawing it
as `[Y/n]` four sections earlier. Both cannot be true: `Confirm` is
specified to treat a bare Enter as *no*, and its doc comment says why —
"every caller of Confirm is about to do something a user might not want,
so the safe answer is the default."

```go
// ConfirmDefaultYes asks a yes/no question whose safe answer is yes, so
// a bare Enter accepts. It is deliberately a second method rather than a
// bool parameter on Confirm: the default is a property of the question,
// not of the call site, and a bool at every existing call site would be
// `false` — noise on nine callers to serve one.
ConfirmDefaultYes(prompt string) (bool, error)
```

**Why this one prompt earns the exception.** Every other `Confirm` in
`gage` guards an action the user might not want: deleting an entry,
removing their own key, trusting a changed recipient list. Clone's offer
guards the action the user has already demonstrated they want — they
typed `gage clone`, and this device holding no identity means the clone
is useless to them until they enroll. Defaulting to no there makes the
overwhelmingly common path cost an extra keystroke to reach the only
outcome that leaves the vault readable, and makes the useful answer the
one you have to know to give.

**What it does not do is widen what `clone` writes without being told
to.** The prompt is still a prompt; "Why there is no `--enroll` flag"
turns on `clone` never publishing unless a human said so, and a human
pressing Enter at a question that names the consequence has said so. The
non-interactive path is untouched and still enrolls nothing — a script
is never asked, so the default never applies to it.

**Every `Prompter` implementation gains the method**, which is the cost
being accepted: the terminal prompter, the test fake, `assumeYesPrompter`
(which delegates it like `Confirm`, since `--yes` answers only
`ConfirmRecipientChange`), and any future GUI. That is a compile-time
break on each of them, which is the right kind — the alternative
considered was reusing `Confirm` and rendering `[Y/n]` while returning
no on Enter, which would have been a silent behavioral lie of exactly
the kind D-ENROLL-PROMPTER rejects.

Note where the decision lives: `cmd/gage`'s clone handler asks and then
calls `Vault.Enroll`, rather than `Enroll` itself asking. The library
never learns whether a terminal was involved, and the "is there a TTY"
test that decides whether to prompt at all is a `cmd/gage` concern — the
same split that keeps `--script`/`--stdin` out of the library today.

**The signatures are what make the late unlock possible, and that's
deliberate.** `PendingEnrollments` and `OpenEnrollment` take no
`Identity` — listing and opening are keyed by the code, not by any
vault key — while `ApproveEnrollments` takes one because it is a
recipient write. So `cmd/gage` can call the first two, render, confirm,
and only then call `Vault.Unlock`. `approve` is therefore *not* wrapped
in `withUnlockedVault` the way `recipient add` is; it unlocks in the
middle. That is the one place this feature departs from the shape of an
existing command, and it departs toward `sync`'s lazy-unlock behavior
rather than inventing anything.

**`DenyEnrollment` takes a `Prompter`, and that is a deliberate
exception to a rule rather than an oversight.** This codebase routes
human interaction through the `Identity` — `warnTo()` returns nil when
there is none, and `identity.go` says why: "human interaction rides the
Identity rather than growing a `Prompter` parameter on every mutating
method." `deny` is the first mutating method that legitimately holds no
`Identity`, because refusing to grant access requires proving nothing.
So the rule has nothing to attach to here.

Without a `Prompter`, a `deny` whose push fails — offline, expired
token — reports success, leaves the request live on the remote, and lets
it reappear on every other device. A silent failed refusal is
indistinguishable from a successful one, which is the worst of the
available outcomes. The alternatives are worse still: requiring an
unlock in order to *refuse* something contradicts the reason `deny`
needs no code, and leaving it silent is the status quo being fixed.

Recorded as an exception so it is not later "corrected" back: the rule
exists so that methods which *already* take an `Identity` do not
redundantly take a `Prompter` too. It was never a rule against a
`Prompter` reaching a method with no identity to carry one.

**Approval's failed push needs the same treatment, and gets it for
free.** `ApproveEnrollments` holds an `Identity`, so it warns through
`ident.warnTo()` and inherits `pushAfterWrite`'s
"committed locally but not pushed" behavior from every other recipient
write — nothing new to build. What it does need is a message that names
*this* consequence rather than the generic one, because approval's
local-versus-remote split is the widest in the tool:

- Locally, the vault is re-encrypted, the recipient is listed, and the
  request file is gone.
- On the remote, none of that happened. The request is still pending,
  so another holder of the code can still approve it, and — this is the
  part worth saying out loud — **the joining device is still locked
  out and has no way to tell.** It was told to run `gage sync`, and
  `gage sync` will report nothing wrong.

So the warning says the approval is local-only and names `gage push` (or
`gage sync`) as the thing that finishes it. Re-approving from another
device after that is harmless rather than a second grant: once the push
lands, the duplicate is the already-a-recipient no-op above. The state
is self-healing in every direction; what it is not is self-explaining,
which is what the message is for.

**`identity enroll` takes the per-vault write lock** for its commit, like every
other write. **`approve` holds it across the whole re-encryption
sequence**, exactly as `recipient add --reencrypt` does today — and
re-reads each sealed request under that lock before trusting what it
showed the human a moment earlier.

---

## Interaction with the local trust cache

Nothing changes in the trust cache's mechanism, and one consequence is
worth stating because it's the desired behavior rather than a side
effect.

- **The approving device** made an intentional recipient change, so
  approval regenerates its cache the same way `recipient add` does. It
  does not warn itself about a change it just made.
- **Every other already-authorized device** sees a recipient it did not
  approve, on its next encrypt or unlock, and gets the ordinary
  recipient-change diff and `[y/N]`. That is correct: enrollment is a
  grant made by *one* holder of access, and the others should find out.
  In a multi-person vault this is the mechanism by which "who let this
  device in?" becomes a question someone actually asks.
- **The joining device** has no cache for this vault yet and writes one
  on first successful use, which is already how a freshly cloned vault
  behaves.
- **A pending request never triggers the cache**, because it never
  changes `.age-recipients` or `config.toml`. This is the inertness
  invariant showing up from the other side: a vault with pending requests
  is, as far as every existing mechanism is concerned, an unchanged
  vault.

---

## What this does and does not change about the trust boundary

**It strictly improves the status quo, and it is important to be precise
about how much.**

Today, git write access to a vault is transitively the ability to become
a recipient: append a line to `.age-recipients`, wait for someone's next
write, read everything written after. The trust cache turns that from
silent into noisy — a human sees a diff and answers `[y/N]` — but the
human is answering it about a public key with no provenance whatsoever.
The only question they can really ask is "does this look like it might
be Andrew's laptop?"

With enrollment, the approver is answering a materially better question:
"did this request come from someone who holds a code I received
out-of-band from the person I mean to authorize?" That's a real binding
between an identity and a human decision, and it's the first one in the
system.

**What it does not do:**

- **It does not close the direct-edit path.** Anyone with git write
  access can still hand-edit `.age-recipients` and skip enrollment
  entirely. Enrollment is an *offered* channel, not an enforced one;
  making it mandatory would require refusing to honor unsigned recipient
  changes, which is the "Signed recipient changes" mitigation the base
  design deliberately reserves for high-assurance vaults. That remains
  the real fix, and this feature is compatible with it rather than a
  substitute for it.
- **It does not make the out-of-band channel `gage`'s problem.** A code
  sent through a compromised channel authenticates an attacker. `gage`
  states the requirement at the moment it prints the code and cannot do
  more than that.
- **It changes nothing about guarantee #1.** No already-encrypted entry
  becomes readable through any of this. A pending request is inert. An
  approval does rewrite every entry to include the new recipient — but
  that is a deliberate act by a human who already holds the key,
  producing *new* ciphertext, not a way to open ciphertext that already
  exists. Anyone holding a copy of the old ciphertext gains nothing from
  it, which is the same thing "Recipient / access management" already
  says about removal being future-facing only.

**One genuinely new piece of information leaks**, and it should be
recorded rather than discovered: a reader of the vault can now see *that*
someone is enrolling, how many requests are outstanding, and when each
one expires, from the files in `.gage/pending/`. Device names and public
keys stay sealed, so what leaks is "activity is happening," not who. The
commit history already leaked comparable timing information, so this is a
small increment on an existing exposure — but it is not zero.

**There is no way to opt out of that leak in v1, and that is a real
narrowing.** An earlier draft offered `--print-only` here — publish
nothing, hand-carry the sealed request — which is now cut
(D-ENROLL-PRINT-ONLY). A vault where the existence of enrollment
activity is itself sensitive therefore falls back to the manual path:
`gage identity add`, carry the public key, `gage recipient add`
elsewhere. That path leaks nothing until the moment access is granted,
and it gives up precisely the authentication this feature exists to
provide. Stating the trade plainly is better than implying a private
option exists.

---

## Deliberately out of scope

- **`gage identity enroll --wait`**, polling until approval lands. Pleasant, but
  it's a second control flow to get right (and to test without a network)
  for something `gage sync` already answers. Additive later.
- **Approver-initiated invites** — the existing device generates a code
  *first*, the joining device consumes it. This is a legitimate
  alternative shape and slightly stronger, since the code would then
  originate with the party granting access. It was not chosen because it
  costs an extra round trip in the common case (old machine → new machine
  → old machine, versus new machine → old machine) and the common case is
  one person setting up their own second device. Worth revisiting if
  multi-person vaults turn out to dominate.
- **`--print-only` / `--request-file`** — publishing nothing and
  hand-carrying the sealed request, for a read-only remote or an
  air-gapped transfer. Cut from v1 (D-ENROLL-PRINT-ONLY): it forks four
  separate rules in this document — armor format, the filename scheme,
  pruning, and one-shot deletion — while its use cases already have the
  supported manual path. Additive later without redesign, since the
  sealed blob is unchanged and a file is just a second input source.
- **Auto-approval of any kind.** No policy, no allowlist, no "trust
  requests from this device name." The human decision is the feature.
- **Enrollment for methods other than `passphrase`.** The payload carries
  a `method` field so that a future `yubikey` or `ssh` request is a new
  value rather than a new field, but only `passphrase` is generated or
  accepted today — the same single-value-allowlist treatment `--type` and
  `--method` already get. What such a method *would* change, and the one
  predicate that has to be fixed before it can land, is traced under
  "What changes when a method other than `passphrase` exists" — the
  analysis is in scope even though the implementation isn't, because
  getting the predicate wrong now would force a rework later.

---
## Implementation plan

The milestone breakdown, the per-milestone test lists that used to live
here, and the sequencing live in
[plans/gage-cli-init-design/](../plans/gage-cli-init-design/index.md) —
the same split the core design uses, where the TDD holds the *why* and
the plan holds the *how* and the definition of done.

Six milestones: **E0**, **E1a** and **E1b** are prerequisites this design
raised against already-shipped work — `A20`, and `A19` plus the
extraction its consumer needs. **E2** proves the sealed request in
isolation, and **E3**/**E4** are the joining and approving sides.

**E0 should be done first and soon.** Its migration is free only while
there is no installed base, which makes it the one piece of work in
either plan that gets more expensive with time.
