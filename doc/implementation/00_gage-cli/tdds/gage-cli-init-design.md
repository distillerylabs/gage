# Device enrollment — joining a vault without hand-carrying a public key

*Status: **draft, for review.** An enhancement to
[gage-cli-design.md](gage-cli-design.md), not a replacement. Everything
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
   `$GAGE_DATA/identities/<vault>/<device>.age`, prints the public half.
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
`OpenEnrollment(codes []string)` is a pure function of what it's handed,
the way `AddRecipient` takes a pubkey. `cmd/gage` collects codes from
`--code` flags or, when none were given, by prompting with the existing
`Prompter.Value` — masked, one line, carrying no vault/device/attempt
context, which is exactly what that method's doc comment already
describes. A wrong code returns `ErrEnrollmentCodeWrong`, and `cmd/gage`
decides whether to ask again.

That is the base design's stated split working as intended: "retry
policy stays a decision `cmd/gage` makes, not one the library bakes in."
The library gains no new interface surface, and neither does `Prompter`.

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
  `Enroll` for `cmd/gage` to display, and it exists nowhere else — not
  in the vault, not in local state, not in the session history file
  (which already refuses to record values).

The alternative was a diceware-style word list, which is genuinely
easier to read aloud. It was rejected for now because it means shipping
and versioning a word list in the binary for a marginal gain over an
unambiguous alphabet, and because a changed list would invalidate codes
generated by an older build. If read-aloud transcription turns out to be
the dominant channel in practice, this is additive later — the wire
format is "a passphrase string," and nothing else depends on its shape.

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

Each file is one sealed enrollment request: an age file with a single
scrypt (passphrase) recipient, whose plaintext is:

```toml
request_id = "7c1e4a90-3b52-4f18-9d6a-8e2f10b4c3d7"
device     = "andrews-macbook-pro"
pubkey     = "age1qz8x2..."
method     = "passphrase"
created    = "2026-09-07T10:12:00Z"
expires    = "2026-09-08T10:12:00Z"
```

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
achieve precisely nothing: they could already append a key to
`.age-recipients` directly, which is strictly more effective. Adding an
inert directory that a human must explicitly promote out of is not a new
attack surface — it's a *narrower* channel offered alongside the wide
one that already exists.

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
   `$GAGE_DATA/identities/<vault>/<device>.age`. The *user* chooses it,
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
`$GAGE_DATA/identities/<vault>/<device>.age`. Hardware-backed methods
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
on a UUID:

```go
var ErrCannotGrantFullAccess = errors.New("gage: this device cannot read every entry in the vault, so it cannot grant full access")
```

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

This is `sync`'s established pattern, not a new one: "**Sync unlocks
lazily.** … `gage sync` only prompts for an unlock when it reaches a
conflict whose resolution requires showing you plaintext." It's also the
same instinct as `clone` refusing to prompt for a passphrase in order to
report that the unlock was pointless. Concretely, `approve` cannot be a
blanket `withUnlockedVault` wrapper the way `recipient add` is; it
unlocks in the middle, after `OpenEnrollment` and the confirmation have
both succeeded.

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
one push, rather than N. This reuses the existing all-or-nothing
`--reencrypt` machinery unchanged: every entry re-encrypted in the
working tree first, then the recipient files, every touched entry, and
the removal of every approved request's file land in exactly one commit.
A crash partway leaves HEAD untouched and every request still pending.

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
  before it was written. That is not an expiry, it is a wrong clock, and
  the error should say so instead of reporting a stale request.
- **At enroll time, warn on the available signal.** A freshly cloned or
  fetched vault carries commit timestamps written by other devices. If
  local time is meaningfully behind HEAD's committer timestamp, this
  machine's clock is probably wrong — one field read, no history walk.
  Warn and proceed, the same posture as an unreachable network or a
  failed page-lock; refusing to enroll over a heuristic would be worse
  than publishing a request that might expire early.
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

    Requires git write access to the remote. A device with read-only
    access gets a message saying so and pointing at the manual path
    (`gage identity add`, then `gage recipient add` elsewhere), which
    remains fully supported and is also the answer for air-gapped
    transfer (see D-ENROLL-PRINT-ONLY).

    Refuses, before generating any key, a device name that already
    labels a recipient of this vault — and says to pass --device.

gage clone <remote-url> [--name NAME] [--dir PATH]

    Unchanged, except for what happens after a successful clone when this
    device can't read the vault. Interactively, clone offers to enroll
    ([Y/n]) and runs the same path `gage identity enroll` does on yes. With no
    TTY, or on no, it prints the message it prints today and exits 0.

    No flag gates this — see "Why there is no --enroll flag". The
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

    Refuses an expired request, a request whose sealed request_id does
    not match its filename's UUID portion, and a code that opens
    nothing. Two refusals happen before the lock is taken and before
    anything is asked: an approver who cannot itself read every entry,
    and a device name that now collides with an existing recipient.

gage recipient deny <ID> [--use NAME]

    Removes a pending request without granting anything. Needs no code
    and no unlock: refusing to grant access requires no proof, it
    changes no recipient list, and anyone with git write access could
    delete the file directly anyway. It still takes the vault write lock
    and commits, like any other write.
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
type EnrollmentRequest struct {
    ID      string
    Device  string
    Pubkey  string
    Method  string
    Created time.Time
    Expires time.Time
    Code    string
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
// field here is authenticated.
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

func (v *Vault) Enroll(device string, ttl time.Duration, p Prompter) (EnrollmentRequest, error)
func (v *Vault) PendingEnrollments() ([]PendingRequest, error)
func (v *Vault) OpenEnrollment(codes []string) ([]OpenedRequest, error)
func (v *Vault) ApproveEnrollments(approvals []Approval, ident *Identity) (ApprovalResult, error)
func (v *Vault) DenyEnrollment(id string) error
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
var ErrEnrollmentCodeWrong  = errors.New("gage: no pending request opened with that code")
var ErrEnrollmentIDMismatch = errors.New("gage: this request's sealed id does not match its filename")
// (compared against the filename's UUID portion only — the epoch portion
// is an unauthenticated hint and is deliberately not part of the check.)
var ErrEnrollmentNoRemote   = errors.New("gage: publishing an enrollment request needs a writable remote")

// Raised when a request's device name — the one it was sealed with — now
// labels a different recipient. Recoverable by the approver alone, via
// --device; the key was authenticated, the label was not.
var ErrEnrollmentNameTaken = errors.New("gage: another recipient of this vault already uses this request's device name")

// Raised before the write lock and before any confirmation, so a
// partially-admitted approver learns why rather than hitting a
// decryption failure on an opaque entry UUID mid-operation.
var ErrCannotGrantFullAccess = errors.New("gage: this device cannot read every entry in the vault, so it cannot grant full access")
```

`ApproveEnrollments` takes no `reencrypt` parameter — there is nothing
to decide. See "Approval always re-encrypts".

**Collisions are checked twice, on purpose.** `cmd/gage` checks the
recipient list as soon as a request is opened, so a name clash is
reported before the confirmation and before any unlock — the same
fail-early posture as `ErrCannotGrantFullAccess`. `AddRecipient` checks
again under the write lock, which is the authoritative one: the first
check is UX, the second is correctness, and only the second can be
trusted against a concurrent writer.

**`Prompter` is unchanged — no new method, no new `UnlockKind`** (see
D-ENROLL-PROMPTER). The approver's code is an argument to
`OpenEnrollment`, not something the library asks for mid-flight;
`cmd/gage` gathers it from `--code` or from the existing
`Prompter.Value`, and owns the retry loop around
`ErrEnrollmentCodeWrong`. The joining side needs no code prompt at all,
since `gage` generates the code — it does still use the existing
`PurposeCreate` unlock exchange to protect the new identity file, which
is unchanged behavior.

**Clone's "enroll now?" prompt needs no new interface surface.** It is
`Prompter.Confirm`, which already exists for exactly this kind of
yes/no. Note where the decision lives: `cmd/gage`'s clone handler asks
and then calls `Vault.Enroll`, rather than `Enroll` itself asking. The
library never learns whether a terminal was involved, and the "is there
a TTY" test that decides whether to prompt at all is a `cmd/gage`
concern — the same split that keeps `--script`/`--stdin` out of the
library today.

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

## Test list (definition of done)

Not exhaustive, but these are the ones a milestone is not done without.
Following the project convention: written before the implementation they
cover, driven in-process through `rootCmd.Execute()` with an injected
`Prompter`, against ephemeral bare repos via `gittest`.

**Inertness — the invariant that makes the feature safe:**
- An entry written while a valid, in-date, correctly-sealed request for
  key K is pending is **not** decryptable by K.
- `recipient list` omits pending requests; `recipient verify` reports "in
  sync" on a vault with pending requests.
- Garbage files, non-age files, and files whose names don't match
  `<uuid>-<epoch>.age` in `.gage/pending/` are skipped, not fatal — the
  same posture `ListIdentities` takes toward strays.

**Seal and code:**
- Round trip: `Enroll` then `OpenEnrollment` with the returned code
  yields the same device, pubkey, and method.
- A wrong code opens nothing and returns `ErrEnrollmentCodeWrong`.
- Codes normalize: lowercase, spaces-for-hyphens, and a missing `GAGE-`
  prefix all open the same request.
- Crockford substitutions open the same request: `I` and `L` typed for
  `1`, `O` typed for `0`.
- Generated codes never contain `I`, `L`, `O`, or `U` — asserted over
  many generations, not one.
- Two successive `Enroll` calls produce different codes (the generator
  is actually seeded from `crypto/rand`, not a fixed or time-derived
  source).
- A code failing length or alphabet validation is rejected **before any
  decryption is attempted** — assert via decrypt call-count
  instrumentation, the same technique M7 uses for the index, so a typo
  can't cost N scrypt runs.
- The generated code appears in `Enroll`'s return value and nowhere
  else: not in the vault, not in local state, not in the session history
  file.
- A request whose sealed `request_id` disagrees with its filename is
  refused with `ErrEnrollmentIDMismatch` (write the file under a
  different UUID to produce this). Changing only the *epoch* portion is
  **not** a mismatch — it is an unauthenticated hint by design, and the
  sealed `expires` is what approval checks.
- A tampered sealed blob fails to open rather than opening with altered
  contents.

**Expiry and the filename:**
- `Enroll` names the file `<request-id>-<expires-epoch>.age`, and the
  epoch parses back to the same instant as the sealed `expires`.
- A request past its sealed `expires` is refused with
  `ErrEnrollmentExpired` **even when its filename claims a far-future
  epoch** — the test that proves expiry is read from the authenticated
  copy, not the name.
- The converse is safe too: a filename claiming an already-past epoch on
  a request whose seal is still valid gets pruned rather than approved,
  and nothing about that grants access.
- `PendingEnrollments` performs **no git history traversal** — assert by
  running it against a vault with a long history and a bare-remote
  harness, or by instrumenting the git layer. This is the property the
  whole filename scheme exists to buy.
- `recipient pending` renders the expiry in the local timezone, and the
  same request renders differently under two different `TZ` values while
  naming the same instant.
- `--ttl` beyond the 7-day ceiling, zero, and negative are each rejected
  at the boundary with a usage error, before anything is generated,
  written, or committed.
- **A filename whose epoch is more than the ceiling beyond now is pruned
  as already expired**, so a forged `<uuid>-99999999999.age` cannot
  linger — the clamp that keeps the ceiling load-bearing.
- A file in `pending/` whose name does not parse (bad UUID, non-numeric
  epoch, missing epoch) is **skipped, not deleted** — gage never removes
  a file it cannot account for.
- **An ID matches the UUID portion only.** Construct a vault where one
  request's expiry epoch contains another request's short id as a
  substring, then assert `deny <that id>` removes the request whose
  *UUID* matches and never the one whose *epoch* does.
- An id that appears only inside an epoch, and in no UUID, matches
  nothing rather than matching by accident.
- A request sealed with an `expires` already in the past at `created`
  (a slow-clock device) is refused with a message naming clock skew,
  distinguishable from an ordinary expired request that simply sat too
  long.
- `identity enroll` warns, and proceeds, when local time is behind the vault's
  HEAD committer timestamp.
- `--device` with a run that resolves to more than one request is a
  usage error naming the ID form; with exactly one request it applies.

**Approval:**
- Approving adds exactly one recipient to both `.age-recipients` and
  `config.toml`, and `verify` still passes afterward.
- **Approval makes every pre-existing entry readable by the new key** —
  the newly approved device can read entries written long before it
  existed. There is no flag, and no code path, that produces a
  partially-readable recipient.
- Approval, the re-encryption, and removal of the pending request's file
  land in **one** commit.
- Batch approval of N requests performs **one** re-encryption pass and
  produces **one** commit.
- An injected failure partway through the re-encryption leaves HEAD
  untouched and every request still pending.
- **An approver who cannot read every entry is refused with
  `ErrCannotGrantFullAccess` before the write lock is taken and before
  any confirmation is shown** — the error names the count of unreadable
  entries, never a bare decryption failure on a UUID. Construct the
  state by adding a recipient without re-encryption (or by hand-editing
  `.age-recipients`) and then approving from that device.
- `deny` removes the file, grants nothing, and needs no code.

**Collisions and duplicates (D-ENROLL-COLLISIONS):**
- `identity enroll` whose device name already labels a recipient fails with
  `ErrDeviceNameTaken`, **writes no identity file**, and the message
  names `--device`.
- A request whose device name became taken between enroll and approve is
  refused with `ErrEnrollmentNameTaken` **before the confirmation and
  before any unlock** — passphrase prompt count zero.
- `approve --device <other-name>` applies that same request, recording
  the approver's label rather than the sealed one. The recipient's
  pubkey is the sealed one, unchanged: relabeling changes the name and
  nothing else.
- Approving a request whose **pubkey is already a recipient** succeeds
  with no work — request cleared, zero entries re-encrypted, no new
  recipient, and the outcome reports `Added: false`.
- Re-running `identity enroll` produces a **different request id and a different
  code** from the first run, and both requests remain independently
  openable by their own codes.
- Approving one of two duplicate requests for the same device, then the
  other, leaves exactly one recipient — the second approval is the
  no-op above rather than an error.

**The approver's unlock:**
- Approval prompts for the approver's passphrase — a git-writer holding
  no key of this vault cannot approve an enrollment.
- **A wrong code costs no unlock**: the passphrase prompt count is zero
  when `--code` opens nothing. Same for an expired request.
- **Declining the `[y/N]` costs no unlock**, and leaves the request
  pending and nothing committed.
- In a session with the vault already unlocked, approval prompts for no
  passphrase at all and reuses the cached `Identity`.
- When another device changed the recipient list first, the approver
  answers *two* distinct prompts — the approve confirmation and M10's
  recipient-change warning — and declining the second aborts with
  nothing committed and the request still pending.
- A sealed request swapped between `OpenEnrollment` and the commit is
  caught: what gets written is what was verified under the lock, not
  what was displayed.

**Enroll-side behavior:**
- `identity enroll` on a device with no identity creates one, via a
  `PurposeCreate` exchange (asked twice).
- `identity enroll` on a device that already holds an identity **reuses it and
  does not write a second identity file** — and prompts **once**, with
  `Purpose: PurposeUnlock`. Assert the purpose and the prompt count, not
  just the end state: the whole point is that these are two different
  exchanges and the fake `Prompter` can tell them apart.
- A **wrong passphrase on the reuse path** surfaces `ErrWrongPassphrase`
  at `exitcode.LockedOrAuth`, writes nothing to `.gage/pending/`, and
  pushes nothing — a failure mode the create path cannot produce at all.
- Re-running `identity enroll` after an injected push failure succeeds on the
  second attempt (the documented "safe to run twice" property), reusing
  the identity written by the first attempt rather than generating a
  second one.
- The public key sealed into the request equals the public key derived
  from the identity file this device actually holds — the invariant that
  rules out caching the pubkey in a sidecar that could drift.
- `identity enroll` against a vault with no writable remote fails with
  `ErrEnrollmentNoRemote` and names the manual path, via an injected fake
  `RemoteSyncer` rather than a real timeout.
- An interactive `clone` of a vault this device can't read offers to
  enroll, and answering yes produces the same end state as `clone`
  followed by `identity enroll`.
- Answering `n` leaves the vault cloned, no identity written, and
  nothing pushed — and a later `gage identity enroll` still works.
- A **non-interactive** `clone` (no TTY) never prompts, never writes an
  identity, never pushes, exits 0, and prints the run-`gage identity enroll`
  message — the behavior a scripted clone has today.
- A `clone` whose device already holds an identity for that vault name
  is **not** prompted at all, and no unlock is attempted during the
  clone — the passphrase prompt count for that run is zero.
- `gage identity enroll` run explicitly in that state reuses the existing
  identity rather than generating a second one.
- **The two secrets never cross.** The identity passphrase appears in no
  output stream and in nothing committed; the enrollment code appears in
  no identity file and in no committed plaintext. One test asserting
  both, since the whole risk is that they get confused for each other.
- The passphrase prompt and the printed code each carry their
  disambiguating label ("stays on this device" / "safe to send"), so a
  reader of the transcript alone can tell them apart.

**Trust cache:**
- A second already-authorized device gets the ordinary
  recipient-change warning after someone else approves an enrollment.
- The approving device does not warn itself about its own approval.
- A pending request alone triggers no warning on any device.
