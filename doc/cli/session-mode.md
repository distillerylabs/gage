# Session mode and scripting

`gage` has two invocation modes. There's no background daemon or shared
agent bridging them — each is a separate process holding its own unlocked
keys in its own memory for as long as it runs.

## One-shot mode

```
$ gage show protonmail
```

Unlocks the identity, decrypts entries as needed to resolve your query,
does the one thing you asked, drops the key from memory, and exits. Every
invocation pays the full unlock cost (passphrase prompt, or a future
YubiKey touch) and the full decrypt-and-resolve cost, since nothing is
cached between invocations. Best for scripting, one-off lookups, and
piping into other Unix tools.

## Session mode (the REPL)

Running `gage` with no subcommand, with stdin attached to a terminal, drops
you into an interactive prompt:

```
$ gage
gage> use personal
Enter passphrase: ****
[personal🔓] gage> show protonmail
correcthorsebatterystaple
[personal🔓] gage> use work
Enter passphrase: ****
[personal🔓 work🔓] gage> status
  personal   unlocked  (last used 5s ago)
  work       unlocked  (last used 1s ago)
[personal🔓 work🔓] gage> lock personal
personal locked. run `use personal` to unlock again.
[personal🔒 work🔓] gage> exit
$
```

You unlock a vault once with `use <vault>` and run as many commands against
it as you like — no repeated prompts. The unlocked key exists only in this
process's memory (page-locked where the OS allows it) and is zeroed the
moment the process exits, whether by `exit`, `quit`, Ctrl-D, or being
killed. There's nothing to attach to from another terminal, and no
persistent listener to secure.

If stdin is *not* a terminal (piped or redirected), a bare `gage` prints
help instead of silently starting a session — use `--stdin` (below) if you
actually want to feed it commands non-interactively.

### Session-only commands

These only exist inside a session — they have no meaning as a one-shot
command:

```
use <vault>          switch/unlock the active vault for this session
lock [vault]         drop key material for one vault (or all, if omitted),
                      without exiting the process
status / whoami       list vaults touched this session and their lock state
help
exit / quit / ^D
```

### Everything else works in a session too

Every other command — entry commands (`show`, `cat`, `ls`, `insert`,
`edit`, `rename`, `generate`, `search`/`grep`, `rm`, `mv`, `cp`,
`reindex`), the sync family (`sync`, `pull`, `push`, `log`, `history`), and
management commands (`vault list/info/remove/set-default`,
`identity add/enroll/list`,
`recipient add/remove/list/verify/pending/approve/deny`, `git set-remote`,
`auth login/status/logout`) — works inside a session, operating against the
session's current vault without needing `--use` each time. `--use NAME`
still works ad hoc against any other vault already `use`d this session (it
prompts to unlock if you haven't touched it yet).

The few commands that don't need an unlocked vault — `identity enroll`,
`recipient pending`, `recipient approve`, `recipient deny`,
`recipient verify` — work with `--use NAME` against a vault this session
could never have `use`d, which is the point: a device enrolling in a vault
can't unlock it yet. `use` on such a vault fails as it always has, now
pointing at the command that fixes it:

```
gage> use personal
gage: no local identity is registered for this device: "personal" is not registered on this machine; run `gage identity enroll` to publish a request to join "personal"
```

**`init` and `clone` are one-shot only.** Both create a vault rather than
operate on one, which leaves "should the new vault become this session's
current vault?" unanswered — invoked in a session, they say so plainly
rather than failing as an unknown command.

### Idle timeout

A session left open in a terminal multiplexer over a weekend is
functionally an unmanaged agent. `[shell].idle_timeout` (10 minutes by
default) automatically re-locks a vault's key after inactivity — the
process stays alive, but the prompt drops to `🔒` and the next
data-touching command re-prompts for unlock, same as running `lock`
yourself.

### Trade-off vs. a shared agent

Unlike `ssh-agent`, unlocking a vault in one terminal does **not** unlock it
for other terminals — each `gage` session is its own island. Open a second
terminal and you unlock again. This trades convenience for a smaller blast
radius (one terminal's process, not every shell you've opened since boot)
and no socket to reason about the permissions of.

## Non-interactive session mode (scripting)

For automation that wants the "unlock once, do several things, then gone"
property without a human at a keyboard:

```
gage --script deploy-secrets.gage      # reads session commands from a file
cat commands.txt | gage --stdin        # same, from stdin
```

Both stop at the first failing command rather than continuing against
state the script's author never anticipated, and neither writes to the
shell history file (that file records what a human typed at a prompt).

`--value-stdin` on `insert` and `--stdin` here are deliberately different
flags: `--stdin` has already spent stdin on the command stream, so
`insert --value-stdin` inside a `--stdin`-driven script would race the
script for the same bytes — don't combine them.

### Where the passphrase comes from

In this fixed order:

1. **`GAGE_PASSPHRASE`**, if set in the environment.
2. Otherwise, a prompt — but only under `--script`, which leaves stdin free
   for a human to answer. `--stdin` has already spent stdin on the command
   stream and can never prompt.
3. Otherwise, a refusal naming the missing variable (exit code
   `LockedOrAuth`) — never a read that can't be answered, and never an
   empty passphrase reported as a wrong one.

This applies to `--script`/`--stdin` runs and only to them. An ordinary
one-shot `gage show ...` ignores `GAGE_PASSPHRASE` and prompts.

`GAGE_PASSPHRASE` only opens an *existing* identity — it's never used for
`init` or `identity add`, where a typo would silently create an unrecoverable
key with nothing to check it against. It's also single-use per process: a
script driving two vaults with different passphrases reassigns the variable
between them.

### What enrollment does non-interactively

`gage identity enroll` has two paths and they behave differently here, both
correctly:

- **Creating a key refuses.** A new identity's passphrase can't come from
  `GAGE_PASSPHRASE` for the reason above. Under `--script FILE` at a
  terminal the prompt passes through to the human and enroll proceeds
  normally; under `--stdin`, or with no terminal, it fails with
  `locked-or-auth` (`5`) before any key is generated.
- **Reusing an existing key succeeds.** That's an ordinary unlock, so
  `GAGE_PASSPHRASE` answers it and a fully scripted enroll works end to
  end. This is the path a re-run after a failed push takes, and it's the
  reason enroll is scriptable at all.

A refusal costs nothing that matters: no identity is written, and nothing
is sealed, committed, or pushed. The fast-forward enroll performs first
survives, which just means the vault is more up to date than it was.

`gage recipient approve` is **not** scriptable, by design. Its confirmation
is a decision about letting a device into a vault, and a run nobody watched
must not make it by omission — so with nobody to ask it fails
`locked-or-auth` (`5`) and leaves the request pending. `--yes` does not
answer it; that flag answers the recipient-change confirmation and nothing
else. `recipient pending` and `recipient deny` need no unlock and no
confirmation, so both script fine.
