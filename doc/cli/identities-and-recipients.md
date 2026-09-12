# Identities and recipients

`gage` separates two concerns that are easy to conflate: **who can read a
vault** (recipients — a vault-wide list of public keys) and **how a
particular device proves it holds the matching private key** (identity —
decided independently by each device, never dictated by the vault).

## Identity: how this device proves it can decrypt

```
gage identity add    [--use NAME] [--method passphrase] [--device NAME]
gage identity enroll [--use NAME] [--device NAME] [--ttl DURATION]
gage identity list   [--use NAME]
```

`identity add` registers how *this device* holds its private key and
prints the resulting public key so it can be added as a recipient — either
by you, if you're bootstrapping a new vault, or by an existing recipient
adding you to theirs with `gage recipient add <pubkey> --device NAME`.

`identity enroll` is the same thing plus publishing: it registers the
identity *and* commits a sealed request to join into the vault itself,
printing a short code to hand to an existing recipient. That's the path
most second devices should take — see [Adding a device](enrollment.md).
`identity add` remains the answer when this device has read-only access to
the remote, or none at all.

- **`--method`** is this device's own choice, not the vault's. Omitted, it
  takes the vault's configured default method. `passphrase` is the only
  method today.
- **`--device`** names this device as a recipient label. Omitted, it
  defaults to your normalized hostname (lowercased, truncated at the first
  dot, non-`[a-z0-9._-]` characters replaced). A name already registered as
  a recipient of this vault is rejected rather than silently taken over —
  pass `--device` explicitly to get past that, which is what two machines
  with the same normalized hostname need.
- Both verbs **reuse an existing identity file** rather than generating a
  second key and orphaning the first, so re-running either after a failure
  is safe. `identity add` prompts for a new passphrase twice when it
  creates a key; `identity enroll` on the reuse path instead asks once for
  the existing passphrase, since the public key its request names can only
  be derived by opening the private key.
- A hostname-derived name is convenient but not private — it's stored in a
  plaintext, committed file every recipient can read. If that's a concern
  for a shared vault, pass `--device` explicitly.

For the passphrase method, this also writes a wrapped identity file to
local, device-only storage (never inside the vault's own git tree — see
[Configuration](configuration.md) for exactly where). **Losing that file is
a recovery problem, not a backup problem:** the fix is the same as "this
device is gone" — run `gage identity enroll` again to generate a fresh
identity and publish a request for it (or `gage identity add`, and have any
other recipient add the printed key via `gage recipient add`). Note that
neither verb will overwrite an identity file that's still there; recovery
means registering a *new* key, never restoring the old one from a backup.
This is why it's worth adding a recovery key as an extra recipient when you
create a vault (`gage init --recipient ...`) — a vault that depends on
exactly one identity file surviving forever has no real recovery story.

## Recipients: who can decrypt a vault

```
gage recipient add <pubkey> --device NAME [--use NAME]
gage recipient remove <pubkey-or-name> --reencrypt [--use NAME]
gage recipient list [--use NAME]
gage recipient verify [--use NAME] [--repair]

gage recipient pending [--use NAME]                       # enrollment requests
gage recipient approve [ID...] [--code CODE ...] [--device NAME] [--use NAME]
gage recipient deny <ID> [--use NAME]
```

The last three belong to the enrollment flow and have their own page:
[Adding a device](enrollment.md). The rest are the manual, hand-carried-key
path.

- **`recipient add`** takes a bare public key and **requires `--device`**:
  a public key says nothing about whose it is, and the label is what every
  later `recipient remove`/`list` addresses it by, so there's no sensible
  default to invent.
- **`recipient add` authorizes that key and re-encrypts every existing
  entry** to include it, so the new recipient can read the vault's whole
  history. There is no `--reencrypt` flag and no way to skip it — passing
  the flag is rejected as a usage error rather than accepted and ignored,
  so a script that asked for the old optional behavior finds out. The
  reason: a recipient who can read only part of a vault isn't an access
  tier anyone chose, and the state spreads — a partially admitted device
  can neither repair its own access nor grant full access to anyone else.
  The other side of that rule is that adding a recipient requires being
  able to read every entry yourself; run it from a device that can. If you
  can't, `gage` says so — naming how many entries are unreadable — before
  it changes anything.
- **`recipient remove`** *requires* `--reencrypt` — `gage` refuses to
  silently leave old ciphertext readable by someone you just revoked. It
  prints a clear warning either way: **removing a recipient revokes future
  access only.** Anything they already read can't be unread. Removing this
  device's own key is allowed but asks first (and `--yes` does *not* answer
  that question); removing the last recipient is refused outright.
- **Re-encryption is all-or-nothing.** Every entry is re-encrypted in the
  working tree, and the recipient list plus every touched entry land in
  exactly one commit — nothing commits until the whole operation succeeds.
  If it's interrupted (crash, kill, power loss), the vault is left exactly
  as it was before the command ran; re-running the command starts cleanly
  from scratch.
- **`recipient verify`** checks that `.age-recipients` and
  `.gage/config.toml` agree on who's a recipient. It needs no unlock (both
  files are plaintext), so it works before any identity is available and is
  safe to run in CI. It exits `0` and prints "in sync" on a match, or exits
  `1` and lists the specific differences otherwise.
- **`recipient verify --repair`** is the way out of that disagreement. It
  rewrites `.age-recipients` from `.gage/config.toml`'s list and commits
  it, after naming every key it drops and every key it adds — `gage` says
  what it dropped rather than only that it succeeded, since those keys
  could read everything written while they were listed. `recipient
  add`/`remove` refuse to run over a disagreement at all (they rebuild
  `.age-recipients` from `config.toml`, so running one would erase the
  stray key and commit that as an ordinary recipient change), which is why
  the repair is its own explicit step. It takes no `--reencrypt` — rewriting
  a plaintext key list says nothing about whether existing ciphertext
  should be rewritten — and it does not clear the trust cache, so the next
  write still shows you the recipient list for review.

## The recipient-change confirmation

Before encrypting to a vault's current recipient list, `gage` compares it
against a locally cached copy from the last time you used this vault (its
"local trust cache"). If the list has changed since then — a new recipient
was added, on this device or another — you get a warning and a
confirmation prompt before the write proceeds, so a compromised or
carelessly merged recipient list can't silently start encrypting your
secrets to someone new. Pass `--yes` on the write command to skip this
prompt in scripts/CI, where there's nobody to confirm anyway.

`--yes` answers **only** this question. It is deliberately not a general
"assume yes": approving an enrollment request, confirming the removal of
this device's own key, and resolving a sync conflict are all different
decisions, and a flag that meant "yes to whatever you were going to ask"
would answer them too. It's also refused in an *interactive* session — a
human is sitting right there, and `gage --yes` entering a session would
silently approve every recipient change for as long as that session lasted.
`gage --script deploy.gage --yes` is fine, since that's exactly the run
with nobody to show a diff to.
