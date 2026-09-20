# Adding a device: enrollment

Getting a second device into a vault used to mean hand-carrying a public
key: run `gage identity add` on the new machine, copy the key it printed
into a chat window, and have someone run `gage recipient add` with it.
That path still exists and is still supported (see
[Identities and recipients](identities-and-recipients.md)), but it asks
the two people involved to verify a 62-character string by eye, which
nobody actually does.

**Enrollment inverts it.** The joining device publishes a *sealed request*
into the vault's own git repository and prints a short code. The code —
not the public key — is what travels out-of-band. An existing recipient
runs `gage recipient approve --code ...`, and the code both identifies the
request and proves it came from whoever they got the code from.

```
gage identity enroll [--use NAME] [--device NAME] [--ttl DURATION]
gage recipient pending [--use NAME]
gage recipient approve [ID...] [--code CODE ...] [--use NAME] [--device NAME]
gage recipient deny <ID> [--use NAME]
```

## The joining device

`gage clone` detects that this machine holds no identity for the vault it
just cloned and, on a terminal, offers to enroll:

```
$ gage clone https://github.com/you/personal-vault.git
gage: cloned "personal" to ~/.local/share/gage/vaults/personal
This device holds no identity for "personal", so it can't read anything here yet.
Set up an enrollment request now? [Y/n] y
```

This is the one yes/no question in `gage` that defaults to **yes** — every
other confirmation defaults to no. It's the answer to a problem `clone`
just reported, not an action you might not want.

There is deliberately **no `--enroll` flag**. `clone` already detects and
reports this exact condition, so a flag opting into acting on a fact the
tool just printed would carry no information. And enrollment commits and
pushes: silently turning "fetch a copy" into "fetch a copy and publish a
request naming this machine" would widen what `clone` does in a way its
name doesn't suggest.

Answering `n`, or cloning without a terminal, prints the manual path
instead and exits `0`:

```
gage: this device ("desktop") has no identity for "demo", so it cannot decrypt anything in it yet.
gage: run `gage identity enroll` here to generate a key and publish a request to join,
gage: then give the code it prints to someone who can already read the vault.
gage: or, if this device cannot write to the remote: run `gage identity add` here and
gage: have someone with access add the printed key with `gage recipient add`.
```

`gage identity enroll` is the same thing against a vault that's already
cloned — what you reach for when you cloned first and decided to join
second, or answered `n` above:

```
$ gage identity enroll --use personal
gage: creating identity "laptop-1" for vault "personal".
gage: this passphrase protects this device's private key. It cannot be recovered or reset.
gage: it stays on this device and is never sent to anyone.
Choose a passphrase:
Confirm passphrase:
gage: this device is "laptop-1", public key age1hs5xpch6r8vxwvn7v2zn7va5td2her8lzethz68edtr5zqlnsy9qjz5a9r

gage: enrollment request published (expires in 24h).

  Enrollment code:  GAGE-EQ7M-ZX5R-42MJ-E5FV

gage: this code is safe to send and is not your passphrase. Give it to someone who
gage: can already read "personal", over a channel where they can tell it came from
gage: you — not through this vault's remote. They run:
gage:   gage recipient approve --code GAGE-EQ7M-ZX5R-42MJ-E5FV

gage: then run `gage sync` here.
```

`identity enroll` **is** `identity add` plus publishing — the same
create-or-reuse behavior, differing only in what happens once the key
exists. A device that already holds a wrapped identity file for this vault
reuses that key rather than generating a second one and orphaning the
first, which is what makes the command safe to re-run after a failed push.
The two paths prompt differently: creating a key asks for a new passphrase
twice, reusing one asks for that key's existing passphrase once (the public
key the request has to name can only be derived by opening the private
key).

### Which secret is which

Two different secrets appear a few lines apart on one screen, and
conflating them is the expensive mistake this flow is designed against:

| | Chosen by | Leaves the machine? | Lifetime |
|---|---|---|---|
| **Identity passphrase** | you | never | forever — it's what you type at every unlock |
| **Enrollment code** | `gage` | yes, that's its job | 24 hours by default |

So each is labelled at its point of use. The passphrase prompt says the
answer stays on this device; the code is printed with an explicit "safe to
send and is not your passphrase." The failure mode being designed against
is someone pasting their identity passphrase into a chat window because
they thought it was the code — or typing the code at their next unlock
prompt and concluding `gage` is broken.

A third secret lives on the *other* device: the approver's own identity
passphrase, which approval prompts for because re-encrypting the vault
needs their key. It's unrelated to either of the above.

### The code

`GAGE-XXXX-XXXX-XXXX-XXXX` — 16 characters of [Crockford
base32](https://www.crockford.com/base32.html), which is 80 bits from
`crypto/rand`. The alphabet excludes `I`, `L`, `O` and `U`, and typing
`I`/`L`/`O` anyway maps them to `1`/`1`/`0` rather than failing, because a
code containing one is a transcription of a code `gage` generated. Case,
spaces instead of hyphens, and a dropped `GAGE-` prefix are all absorbed —
retype it however it arrived.

The entropy is the security parameter, not the key-derivation behind it:
the sealed request sits in a git repository every reader of the vault can
fetch, which makes it an offline, unlimited-guess target. That's also why
the code is *generated* rather than chosen by you.

### `--ttl`

A request lives 24 hours by default, and `--ttl` changes that up to a hard
ceiling of 7 days (`--ttl 2h`, `--ttl 168h`). The ceiling does double duty:
it bounds what you can ask for, and it bounds what `gage` will *honor* on
the way back in — a request claiming an expiry further than 7 days out is
treated as already expired, so nobody can park a permanent blob in the
tree.

### Enrollment needs the network, and fails if it can't reach it

This is the one place `gage` departs from its usual "warn and proceed" over
a network failure. `identity enroll` fetches and fast-forwards before it
commits, and **fails outright** if the remote is unreachable or if the two
histories have diverged — before generating any key, prompting for
anything, or writing anything. A read still delivers value offline; an
enrollment request that was never published is a private key, a local
commit, and a code nobody can act on.

It also needs git **write** access. A token that can read but not write is
only discovered at the push, which reports that the identity was created
and the request committed locally but not published, and names
`gage auth login`. A read-only device uses the manual path
(`identity add` here, `recipient add` there), which is also the answer for
air-gapped transfer.

### When enrollment does nothing

- **This device's key is already a recipient.** Reported as a success, with
  nothing generated and nothing published. Checked by public key rather
  than by device name, so it's also correct for a device an approver
  relabeled.
- **The device name already labels a *different* recipient.** Refused
  before any key is generated, naming `--device` as the way out. This is
  the case two machines whose hostnames normalize identically land in.

## The approving device

`recipient pending` lists what's waiting. It reads the request's
*filename*, so it needs no unlock, no code, and no history walk:

```
$ gage recipient pending --use personal
gage: 1 pending enrollment request for "personal":

  931b0148   expires 2026-09-12 22:25 PDT  (in 24h)

gage: nothing here can decrypt anything until it is approved.
gage: run `gage recipient approve --code <code>` with a code you received out-of-band.
```

Device names are deliberately absent: they're under the seal, and listing
them would tell every reader of the vault who is setting up a new machine
before anyone approved anything. That's a mild UX cost — an approver with
several pending requests can't tell them apart at a glance — and it's the
right trade.

It also does **not** fetch. `pending` and `approve` read your local copy of
the vault, so a request published a minute ago shows up only after a `gage
pull` or `gage sync`. (`approve` fetches under its own write lock once
you've confirmed, but that's too late to have listed anything.)

Then approve:

```
$ gage recipient approve --code GAGE-EQ7M-ZX5R-42MJ-E5FV
gage: opened 1 pending request.

  device:  laptop-1
  pubkey:  age1hs5xpch6r8vxwvn7v2zn7va5td2her8lzethz68edtr5zqlnsy9qjz5a9r
  method:  passphrase
  created: 2026-09-11 22:25  (expires in 24h)

Add "laptop-1" as a recipient of "demo"? It will be able to read all 1 entry, including everything already in the vault. [y/N] y
Enter passphrase for vault "demo" (device laptop):
gage: approved "laptop-1" (age1hs5xpch6r8vxwvn7v2zn7va5td2her8lzethz68edtr5zqlnsy9qjz5a9r)
gage: 1 recipient added, 1 entry re-encrypted, 1 request cleared; committed locally as 2e3e08e28bd9
```

The code identifies the request as a side effect of authenticating it, so
the approver never has to know or type an ID. A positional `ID` narrows the
search when you want to be explicit, and `--code` is repeatable to approve
several devices in one commit.

With no `--code` at all, `gage` prompts for one and retries a wrong one; a
bare Enter cancels.

### The order things happen in

Open the request → check for a device-name collision → show what it claims
→ take the `[y/N]` → *then* unlock → then write. **The unlock is late on
purpose**: a wrong code or a declined prompt costs no passphrase. In a
session where the vault is already unlocked there's no prompt at all.

The write itself adds the recipients, re-encrypts every entry, and deletes
the approved request files — all in **one commit**, or none of it. It
fetches and fast-forwards under its own write lock first, since an approval
rewrites every entry and one made onto a stale tip would conflict on every
one of them. A divergence refuses before anything is written; an
unreachable remote warns and proceeds, since an approval that lands locally
is real work.

`--yes` does **not** answer the "add this device?" question. That flag
answers the [recipient-change
confirmation](identities-and-recipients.md#the-recipient-change-confirmation)
and nothing else — letting a device into a vault is exactly the decision a
run nobody watched must not make by omission. With nobody to ask, approval
fails with `locked-or-auth` (`5`) and the request stays pending.

### Approval always re-encrypts

There is no `--reencrypt` flag on `approve`. Approval re-encrypts every
entry, unconditionally, for the same reason
[`recipient add`](identities-and-recipients.md#recipients-who-can-decrypt-a-vault)
does: a recipient who can read only part of a vault isn't an access tier
anyone chose, it's invisible in the interface (the dividing line is
"written before or after you were approved"), and the state spreads —
a partially admitted device can neither repair its own access nor grant
full access to anyone else.

The other side of that rule: **approving requires being able to read every
entry yourself.** An approver who can't is refused with a count of how many
entries are unreadable, rather than dying mid-write on an opaque UUID.

### `--device`

```
gage recipient approve <ID> --code CODE --device other-laptop
```

Records the approved key under a name of the approver's choosing instead of
the one the request claims. It exists for the case where the claimed name
now collides with an existing recipient — without it, such a request would
be permanently unapprovable. The call is the approver's to make: the code
authenticated the *key*, not the label.

It's only accepted when the run resolves to exactly one request, because
one flag can't name which of several it relabels. Pass an ID to narrow a
multi-request run; otherwise `gage` says so rather than guessing.

### Denying

```
$ gage recipient deny 931b0148
gage: removed pending request 931b0148 from "personal". Nothing was granted; nothing to re-encrypt.
```

Needs no code and no unlock: refusing to grant access requires proving
nothing, it changes no recipient list, and anyone with git write access
could delete the file directly anyway. It still takes the vault write lock
and commits like any other write, and warns if the push fails — a deny that
never reached the remote leaves the request live for every other device.

### Addressing a request by ID

`approve <ID>` and `deny <ID>` resolve against the **UUID portion of the
filename only**, never the expiry, in the same order [entry
queries](addressing-entries.md) use:

1. Exact UUID (all 36 characters).
2. Substring of the UUID — the eight-character prefix the listing shows is
   this case, and it's what people actually type.
3. More than one match: `gage` lists the candidates and exits `ambiguous`
   (`4`) rather than guessing. This matters more here than for a read,
   because `deny` deletes.
4. No match: `not-found` (`3`).

There is no matching on device name — the name is sealed, which is the
whole point of the filename scheme.

## What a pending request is, on disk

```
myvault/
└── .gage/
    └── pending/
        └── <request-uuid>-<expires-epoch>.age
```

The directory is created by the first enroll and is absent in a vault
that's never had one. Each file is an age blob encrypted **to the code** —
not to any recipient — carrying the device name, public key, method,
request id, vault id, and expiry.

The load-bearing property is that **`pending/` is inert**. Nothing in it
can decrypt anything; a request grants access only once an approver
commits the recipient change. A hostile git writer can add files there and
achieve nothing but noise, which is why:

- The expiry is in the filename, so listing and pruning need no unlock, no
  code, and no history walk.
- `.gitattributes` deliberately does **not** mark `pending/` unmergeable
  (unlike `.age-recipients` and `.gage/config.toml`). Two devices enrolling
  at once write two differently-named files that merge cleanly, and there
  is nothing dangerous about a union of inert blobs.
- Commit messages name no device (`gage: enrollment request`), so a reader
  of the repository learns nothing about who is joining — or who was turned
  away.
- A file whose name doesn't parse is left alone, never deleted: it's by
  definition not something `gage` wrote.

## Expiry and pruning

- Default lifetime 24 hours, hard ceiling 7 days.
- **Approval checks the sealed `expires`** and refuses a stale request
  regardless of what the filename or the listing said. That's the only
  expiry check that's load-bearing.
- **Approval deletes the request file in the same commit** as the recipient
  change — a request is one-shot by construction, with nothing left to
  replay.
- Pruning reads the filename's epoch and rides a write that was happening
  anyway (approve, deny, or the next recipient change) rather than taking
  the lock to do housekeeping alone.
- A request whose sealed expiry was already in the past when it was created
  is reported as **clock skew** on the requesting device, not as an expired
  request — the fixes share nothing.

## Limits

A run that supplies a code without naming an ID tries it against at most
**32** live requests, then refuses and tells you to name an ID (which
resolves from the filename and costs one attempt per code however full the
directory is). The bound is on *work*, not on how many devices may enroll:
the count comes from a directory any git writer can fill, and each attempt
is a key-derivation run. Expired requests are pruned before the bound is
applied, so they never count toward it.

Every supplied `--code` must open something. One that opens nothing fails
the whole run before any confirmation and before anything is committed — a
mistyped code is far likelier than a deliberately surplus one, and
partially approving a batch you confirmed as a batch is the wrong way to be
helpful.

## What can go wrong, and what it exits with

| Situation | Exit code |
|---|---|
| Code opens nothing (wrong, mistyped, or for another vault's request) | `5` locked-or-auth |
| Nobody to answer the `[y/N]` (no terminal); request stays pending | `5` locked-or-auth |
| Declined at the `[y/N]`; request stays pending | `1` conflict |
| Request expired, or sealed id doesn't match its filename, or sealed for a different vault | `1` conflict |
| Requesting device's clock is wrong (expiry predates creation) | `1` conflict |
| Device name already labels a different recipient (either side) | `1` conflict |
| Approver can't read every entry | `1` conflict |
| More than 32 live requests to try a code against | `1` conflict |
| ID matches several pending requests | `4` ambiguous |
| ID matches none | `3` not-found |
| `identity enroll` with no remote configured, or a bad `--ttl` | `2` usage |
| `identity enroll` can't reach the remote | `7` unreachable |
| `identity enroll` on a diverged history | `1` conflict |

See [Exit codes](exit-codes.md) for the full taxonomy.
