# M12 — Polish / output modes

[← M11](m11-cross-vault-sharing.md) · [plan index](index.md)

## Goal

The remaining output modes and conveniences. These items are independent
of each other and of the trust model — good candidates to parallelize or
reorder freely, and safe to defer individually without blocking anything
else.

Two of them are more than polish despite living here: `--clip` and
`--qr` are the design's answer to principle 6 ("plaintext should be
surprising to produce"), and `history --decrypt` is explicitly the most
dangerous command in the tool.

## Depends on

- **M5** — `show`/`generate` exist and are on the resolver.
- **M6** — `Session`, which `--script`/`--stdin` drives non-interactively.
- **M8** — commit history for `log`/`history`.

## Design references

- ["Notes on `show`"](../tdds/gage-cli-design.md) — `--field`, `-c`, `-q`
  semantics and why `--field NAME` with `-q` scopes the QR
- ["Sync (vault-generic, git-implemented today)"](../tdds/gage-cli-design.md)
  — `log` vs `history --decrypt`
- ["`history --decrypt` is still the one meaningfully more dangerous tier"](../tdds/gage-cli-design.md)
- ["Non-interactive session mode"](../tdds/gage-cli-design.md)

## Decisions to make first

- **Clipboard clear semantics.** The design says "auto-clears after a
  short timeout." A one-shot `gage show -c` exits immediately — so
  either the process lingers until the timeout (surprising in a script)
  or it forks something (a process outliving the one holding the key,
  which cuts against principle 5). Also: clearing unconditionally would
  wipe whatever the user copied in the meantime; clear-only-if-unchanged
  is the safer behavior. Needs an explicit answer.
- **Clipboard timeout value** and whether it's configurable in
  `[shell]`.
- **`--script`/`--stdin` and unlock.** The design says "unlocking each
  vault at most once." Where does the passphrase come from in a
  non-interactive run — a prompt on the controlling TTY, an env var, or
  is the mode simply unusable without a TTY? This is the flag CI would
  use, so it matters.
- **Does `history --decrypt` require confirmation?** It's named as the
  most dangerous tier in the design; it currently has no guard beyond
  being explicitly invoked.

## Tests (write first)

- [ ] `--clip` copies plaintext to the clipboard and clears it after the
      configured timeout (clipboard/timer mocked in tests)
- [ ] `--clip`'s clear does not wipe clipboard contents that changed
      after `gage` wrote them (per the decision above)
- [ ] `--qr` renders a QR code that decodes back to exactly the requested
      value; `--field NAME --qr` encodes only that field, not the whole
      entry
- [ ] `--qr` prints no plaintext alongside the code — it renders
      *instead of* printing the value
- [ ] `--field NAME` on `show` extracts the correct value from `fields`
      and errors clearly on an unknown field name
- [ ] `generate` respects `-l LENGTH` and `--no-symbols` in the produced
      secret
- [ ] `generate -l` with an unreasonably small length is rejected rather
      than producing a trivially weak secret
- [ ] `gage log` shows commit timestamps/history only — asserts no
      decrypted content appears in its output
- [ ] `gage log` on a vault whose commit messages contain only UUIDs (per
      M4's decision) reveals no titles
- [ ] `gage history --decrypt` walks revisions and produces a diff of
      decrypted content across commits
- [ ] `--script`/`--stdin` runs a sequence of commands non-interactively,
      unlocking each vault at most once
- [ ] `--script` aborts on the first failing command rather than
      continuing through the rest of the file
- [ ] `--script`/`--stdin` writes nothing to the session history file

## Implementation

- [ ] `--clip` (clipboard, auto-clear per the decided semantics)
- [ ] `--qr` (terminal QR, `--field` scoped)
- [ ] `--field NAME` extraction
- [ ] `generate`'s `-l LENGTH`/`--no-symbols` password-generation logic
      (command itself already exists from M5)
- [ ] `gage log`
- [ ] `gage history --decrypt`
- [ ] Non-interactive session (`--script`, `--stdin`)

## Definition of done

Full test list green on all three CI platforms. Every command in the
design doc's command reference now exists.

## Affects later milestones

None — this is the last planned milestone. Remaining scope lives in the
[deferred list](index.md) and [open-questions.md](open-questions.md),
notably [Q-RELEASE](open-questions.md#q-release), which is what stands
between this and something a user can actually install.
