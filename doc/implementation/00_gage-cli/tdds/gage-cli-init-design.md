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

## Decisions to make first

Per the project's milestone convention, these want a human answer before
any code is written. Each has a recommendation; the reasoning is in the
sections below.

- **`[ ] D-ENROLL-VERBS` — command naming.** Recommendation: `gage
  enroll` on the joining side, `gage recipient pending/approve/deny` on
  the approving side. Rationale: they are two different actors with two
  different mental models, and approval genuinely *is* a recipient
  mutation — it shares `--reencrypt`, the trust cache, and `recipient
  verify` with `recipient add`. The alternative is one `gage enroll
  request/list/approve/deny` group, which keeps the feature findable
  under one word at the cost of putting a recipient-list write somewhere
  other than `recipient`. See "Command reference".
- **`[ ] D-ENROLL-PROMPTER` — how the approver's code entry reaches the
  library.** Recommendation: reuse `Prompter.Unlock` with a new
  `UnlockKind` (`KindEnrollmentCode`), which is precisely what
  `prompter.go`'s existing comment says a second `Kind` is for, and
  which avoids adding a method to an interface that has three
  implementers. The alternative — a dedicated
  `Prompter.EnrollmentCode(...)` method — is more honest about the fact
  that this proves knowledge of a shared code rather than possession of
  a private key, but it breaks every existing `Prompter` implementation
  the day it lands. See "Library surface".
- **`[ ] D-ENROLL-CODE-FORMAT` — what the code looks like.**
  Recommendation: Crockford base32, 16 characters in four hyphenated
  groups (~80 bits), normalized case-insensitively with separators
  stripped on input. The alternative is a diceware-style word list
  (easier to read aloud, better for a phone call) at the cost of
  shipping a word list in the binary. See "The enrollment code".
- **`[ ] D-ENROLL-TTL` — default request lifetime.** Recommendation: 24
  hours by default, `--ttl` to override, with a hard ceiling of 7 days
  that exists so pruning can be done without opening anything. See
  "Expiry, revocation, and pruning".

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
│       ├── 7c1e4a90-3b52-4f18-9d6a-8e2f10b4c3d7.age
│       └── e4f88b21-0a77-4c39-b512-6d90a1e2f345.age
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

**Filenames are opaque UUIDs, and the device name lives inside the
seal.** This follows the same rule as `entries/`: a filename never
encodes content. Naming the file `andrews-macbook-pro.age` would hand
every reader of the vault a free "Andrew is setting up a new laptop"
signal before anyone has approved anything. The device name becomes
public on approval — it's a recipient label in `config.toml` — but there
is no reason to leak it *before*, and no reason at all if the request is
denied or expires.

**`request_id` is repeated inside the seal, matching the filename.** The
filename is unauthenticated (anyone with git write access can rename a
file); the copy under the seal is not. Approval compares the two and
refuses a mismatch, so a blob cannot be renamed onto a different slot or
replayed into a fresh one.

**`expires` is enforced from inside the seal, never from the filename or
the commit date.** Both of those are attacker-controlled — git commit
timestamps are whatever the committer says they are. The authenticated
copy is what approval checks. Unauthenticated timestamps are used for
housekeeping only, where the worst case of a lie is a blob that gets
pruned early or lingers past its usefulness while still being
un-approvable.

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

Generated by `gage` on the joining device, never chosen by the user:

```
GAGE-7K4M-9QX2-P3RH-8WVN
```

Crockford base32 — no `I`, `L`, `O`, or `U`, so there is no
zero-versus-O or one-versus-l ambiguity when it's read aloud or
retyped. Sixteen characters is ~80 bits, which behind age's scrypt work
factor is not brute-forceable by anyone, and which stays short enough to
dictate over a phone call or type on a phone keyboard.

**Input is normalized before use**: uppercased, with all hyphens,
spaces, and the `GAGE-` prefix stripped. Someone who writes the code
down and types it back with spaces instead of hyphens, or in lowercase,
gets the behavior they expect rather than a wrong-code error. The `GAGE-`
prefix is cosmetic — it makes the string identifiable when it turns up in
a chat log, which is also a reminder that it doesn't belong in one.

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

Generated a new identity for this device:
  device:  andrews-macbook-pro
  pubkey:  age1qz8x2...

Enter a passphrase to protect this device's key: ****
Confirm passphrase: ****

Enrollment request published (expires in 24h).

  Enrollment code:  GAGE-7K4M-9QX2-P3RH-8WVN

Give this code to someone who can already read "personal", over a channel
where they can tell it came from you — not through this vault's remote.
They run:  gage recipient approve --code GAGE-7K4M-9QX2-P3RH-8WVN

Then run `gage sync` here.
```

`gage enroll --use personal` does the same thing against a vault that's
already cloned, and is what someone reaches for when they cloned first
and decided to join second, or answered `n` above. It **creates the
identity only if one does not already exist**: a device that has run
`gage identity add`, or that already holds a wrapped identity file for
this vault from an earlier attempt, reuses that key rather than
generating a second one and orphaning the first. This is the one
behavioral difference from `identity add`, which refuses to overwrite.
The distinction is deliberate — `identity add`'s refusal protects "the
only copy of a private key"; `enroll`'s reuse is what makes it safe to
run twice after a failed push.

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
for this vault at all? That yields the rule already shipped, which the
enrollment prompt should adopt unchanged:

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
automatically. Anyone in that state runs `gage enroll` explicitly, which
reuses the existing key rather than generating a second one.

### Approving device

```
$ gage recipient pending --use personal
2 pending enrollment requests for "personal":

  7c1e4a90   requested 2026-09-07 10:12   expires in 22h
  e4f88b21   requested 2026-09-06 18:40   expires in 6h

Nothing here can decrypt anything until it is approved.
Run `gage recipient approve --code <code>` with a code you received
out-of-band.
```

The listing shows what can be known without a code: an ID, an
unauthenticated timestamp, and a derived expiry. It does not show device
names, because they're under the seal. That is a mild UX cost — an
approver with several pending requests can't tell them apart at a glance
— and it is the right trade: the alternative leaks who is joining to
everyone with read access, in exchange for saving one scrypt run.

```
$ gage recipient approve --code GAGE-7K4M-9QX2-P3RH-8WVN --reencrypt
Opened 1 of 2 pending requests with that code.

  device:  andrews-macbook-pro
  pubkey:  age1qz8x2...
  method:  passphrase
  created: 2026-09-07 10:12  (expires in 22h)

Add this device as a recipient of "personal" and re-encrypt 47 entries
so it can read existing history? [y/N] y

Re-encrypting 47 entries... done.
Committed and pushed: +1 recipient, 47 entries re-encrypted, 1 request cleared.
```

`gage` tries the code against every pending request and approves the one
that opens. The approver never has to know or type an ID; the code
identifies the request as a side effect of authenticating it. An
optional positional ID narrows the search when someone wants to be
explicit.

**Approving several devices at once is one `--reencrypt` pass**, which is
the whole reason batch approval exists:

```
$ gage recipient approve --code GAGE-7K4M-... --code GAGE-2NPT-... --reencrypt
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

- **Default lifetime is 24 hours** (`--ttl` on `gage enroll` to change),
  with a hard ceiling of 7 days that `gage` refuses to exceed.
- **Approval checks the sealed `expires` and refuses a stale request**,
  regardless of what any filename, commit date, or listing said. This is
  the only expiry check that is load-bearing.
- **Approval deletes the request file in the same commit** as the
  recipient change. A request is one-shot by construction: there is
  nothing left to replay.
- **Pruning is best-effort and uses the unauthenticated commit date**,
  which is safe precisely because the hard ceiling exists — anything
  committed longer ago than 7 days is definitely expired, whatever its
  sealed `expires` says. `gage` prunes opportunistically when it's
  already writing (on approve, deny, or the next recipient change) rather
  than taking a lock to do housekeeping alone. A forged-future commit
  date keeps a dead blob in the tree; it does not make it approvable.
- **Replay within the TTL is accepted, not defended against.** Someone
  with read and write access could copy a pending blob and re-commit it.
  They still cannot open it, and approval still requires a human with the
  code to say yes — so a replay costs an attacker exactly what the
  original did, which is nothing they didn't already have.

---

## Command reference (additions)

```
gage enroll [--use NAME] [--device NAME] [--ttl DURATION] [--print-only]

    Publishes an enrollment request for this device: creates a local
    identity if one does not already exist for this vault (reusing it if
    it does), seals device name and public key to a freshly generated
    code, commits it to .gage/pending/, pushes, and prints the code.

    Requires git write access to the remote. A device with read-only
    access gets a message saying so and pointing at the manual path
    (`gage identity add`, then `gage recipient add` elsewhere), which
    remains fully supported.

    --print-only skips the commit and push and just prints the sealed
    request, for a remote this device cannot write to and for air-gapped
    transfer. The approving device reads it back with
    `gage recipient approve --request-file PATH --code CODE`.

gage clone <remote-url> [--name NAME] [--dir PATH]

    Unchanged, except for what happens after a successful clone when this
    device can't read the vault. Interactively, clone offers to enroll
    ([Y/n]) and runs the same path `gage enroll` does on yes. With no
    TTY, or on no, it prints the message it prints today and exits 0.

    No flag gates this — see "Why there is no --enroll flag". The
    condition is one clone already detects and already reports.

gage recipient pending [--use NAME]

    Lists pending enrollment requests: ID, unauthenticated request time,
    derived expiry. Needs no unlock and no code — everything shown is
    outside the seal. Exits 0 with "no pending requests" when there are
    none.

gage recipient approve [ID...] --code CODE [--code CODE ...]
                       [--use NAME] [--reencrypt] [--request-file PATH]

    Opens each pending request with the given code(s), shows what each
    one claims, and — on confirmation — adds them to the recipient list
    in a single atomic commit alongside a single --reencrypt pass and
    the removal of every approved request file.

    Refuses an expired request, a request whose sealed request_id does
    not match its filename, and a code that opens nothing.

gage recipient deny <ID> [--use NAME]

    Removes a pending request without granting anything. Needs no code:
    refusing to grant access requires no proof.
```

`--reencrypt` carries exactly the meaning it already has on `recipient
add` — without it the new device reads only entries written after it was
approved. For a device joining an existing vault that is almost never
what the user wants, so `approve` says so explicitly at the confirmation
prompt rather than letting someone discover it later.

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

// PendingRequest is one unopened request, built entirely from what is
// visible outside the seal.
type PendingRequest struct {
    ID        string
    Path      string
    Requested time.Time // unauthenticated; from the commit
    Expires   time.Time // derived from Requested + the TTL ceiling
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

func (v *Vault) Enroll(device string, ttl time.Duration, p Prompter) (EnrollmentRequest, error)
func (v *Vault) PendingEnrollments() ([]PendingRequest, error)
func (v *Vault) OpenEnrollment(codes []string) ([]OpenedRequest, error)
func (v *Vault) ApproveEnrollments(reqs []OpenedRequest, reencrypt bool, ident *Identity) (RecipientChange, error)
func (v *Vault) DenyEnrollment(id string) error
```

`ApproveEnrollments` returns the existing `RecipientChange` rather than a
new type — approval *is* a recipient change, and the caller wants the
same "what moved, how many entries re-encrypted, which commit" answer
`AddRecipient` already gives it.

Typed errors at the boundary, so `cmd/gage` owns retry and exit-code
policy exactly as it does for `Unlock`:

```go
var ErrEnrollmentExpired    = errors.New("gage: this enrollment request has expired")
var ErrEnrollmentCodeWrong  = errors.New("gage: no pending request opened with that code")
var ErrEnrollmentIDMismatch = errors.New("gage: this request's sealed id does not match its filename")
var ErrEnrollmentNoRemote   = errors.New("gage: publishing an enrollment request needs a writable remote")
```

**The approver's code entry** reaches the library through
`Prompter.Unlock` with a new `UnlockKind` — `KindEnrollmentCode`, with
`Purpose: PurposeUnlock` — pending decision `D-ENROLL-PROMPTER` above.
The joining side needs no prompt at all for the code, since `gage`
generates it; it does need the existing `PurposeCreate` unlock exchange
to protect the new identity file, which is unchanged behavior.

**Clone's "enroll now?" prompt needs no new interface surface.** It is
`Prompter.Confirm`, which already exists for exactly this kind of
yes/no. Note where the decision lives: `cmd/gage`'s clone handler asks
and then calls `Vault.Enroll`, rather than `Enroll` itself asking. The
library never learns whether a terminal was involved, and the "is there
a TTY" test that decides whether to prompt at all is a `cmd/gage`
concern — the same split that keeps `--script`/`--stdin` out of the
library today.

**`enroll` takes the per-vault write lock** for its commit, like every
other write. **`approve` holds it across the whole `--reencrypt`
sequence**, exactly as `recipient add --reencrypt` does today.

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
  becomes readable through any of this. A pending request is inert; an
  approval only affects what gets encrypted next, or — with
  `--reencrypt` — what the approver deliberately chose to rewrite.

**One genuinely new piece of information leaks**, and it should be
recorded rather than discovered: a reader of the vault can now see *that*
someone is enrolling, and how many requests are outstanding, from the
existence of files in `.gage/pending/`. Device names and public keys stay
sealed, so what leaks is "activity is happening," not who. The commit
history already leaked comparable timing information, so this is a small
increment on an existing exposure — but it is not zero, and a vault where
that matters should use the `--print-only` path instead.

---

## Deliberately out of scope

- **`gage enroll --wait`**, polling until approval lands. Pleasant, but
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
- **Auto-approval of any kind.** No policy, no allowlist, no "trust
  requests from this device name." The human decision is the feature.
- **Enrollment for methods other than `passphrase`.** The payload carries
  a `method` field so that a future `yubikey` or `ssh` request is a new
  value rather than a new field, but only `passphrase` is generated or
  accepted today — the same single-value-allowlist treatment `--type` and
  `--method` already get.

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
- Garbage files, non-age files, and files with invalid UUID names in
  `.gage/pending/` are skipped, not fatal — the same posture
  `ListIdentities` takes toward strays.

**Seal and code:**
- Round trip: `Enroll` then `OpenEnrollment` with the returned code
  yields the same device, pubkey, and method.
- A wrong code opens nothing and returns `ErrEnrollmentCodeWrong`.
- Codes normalize: lowercase, spaces-for-hyphens, and a missing `GAGE-`
  prefix all open the same request.
- A request whose sealed `request_id` disagrees with its filename is
  refused with `ErrEnrollmentIDMismatch` (write the file under a
  different name to produce this).
- A tampered sealed blob fails to open rather than opening with altered
  contents.

**Expiry:**
- A request past its sealed `expires` is refused with
  `ErrEnrollmentExpired`, **even when its commit date is recent** — this
  is the test that proves expiry is read from the authenticated copy.
- `--ttl` beyond the 7-day ceiling is rejected at the boundary.

**Approval:**
- Approving adds exactly one recipient to both `.age-recipients` and
  `config.toml`, and `verify` still passes afterward.
- Approving with `--reencrypt` makes pre-existing entries readable by the
  new key; without it, only entries written after approval are.
- Approval, the re-encryption, and the request-file deletion land in
  **one** commit.
- Batch approval of N requests performs **one** re-encryption pass and
  produces **one** commit.
- An injected failure partway through `--reencrypt` leaves HEAD
  untouched and every request still pending.
- `deny` removes the file, grants nothing, and needs no code.

**Enroll-side behavior:**
- `enroll` on a device with no identity creates one; `enroll` on a device
  that already holds one reuses it and does not write a second identity
  file.
- `enroll` against a vault with no writable remote fails with
  `ErrEnrollmentNoRemote` and names the manual path, via an injected fake
  `RemoteSyncer` rather than a real timeout.
- An interactive `clone` of a vault this device can't read offers to
  enroll, and answering yes produces the same end state as `clone`
  followed by `enroll`.
- Answering `n` leaves the vault cloned, no identity written, and
  nothing pushed — and a later `gage enroll` still works.
- A **non-interactive** `clone` (no TTY) never prompts, never writes an
  identity, never pushes, exits 0, and prints the run-`gage enroll`
  message — the behavior a scripted clone has today.
- A `clone` whose device already holds an identity for that vault name
  is **not** prompted at all, and no unlock is attempted during the
  clone — the passphrase prompt count for that run is zero.
- `gage enroll` run explicitly in that state reuses the existing
  identity rather than generating a second one.

**Trust cache:**
- A second already-authorized device gets the ordinary
  recipient-change warning after someone else approves an enrollment.
- The approving device does not warn itself about its own approval.
- A pending request alone triggers no warning on any device.
