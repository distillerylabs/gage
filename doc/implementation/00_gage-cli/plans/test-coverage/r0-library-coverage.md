# R0 — Library coverage (`internal/gage` and leaf packages)

[plan index](index.md) · next: [R1 — CLI coverage](r1-cli-coverage.md)

> **Recommended model: Sonnet.** Additive, well specified, pattern-following. Every test here is a direct library call against an existing helper; nothing needs new abstractions or security reasoning.

## Goal

Close the honest coverage gaps in `internal/gage` and its leaf packages:
the entry-format marshalling internals, search's body matching, the
`gitrepo` states that ordinary vault use never reaches, the trust cache's
diff, and the validation errors that every constructor rejects but no
test checks. No production code changes — every file this milestone
touches ends in `_test.go`.

## Depends on

- Nothing. Cut off `27-test-coverage`.
- Existing helpers, reused rather than reinvented: `isolateXDG`,
  `registerVault`, `vaultIDForTest`, `fakePrompter`, `countingLocker`
  (`internal/gage/testsupport_test.go`); `gittest.BareRemote` and
  `gittest.Device` for git work.

## Design references

- [Core plan, "Test conventions"](../gage-cli-design/index.md) — in-process
  tests, no network, no new seams.
- [Design doc, "Entry format"](../../tdds/gage-cli-design.md) — what
  `MarshalEntry` and `verifyFaithful` are protecting.
- [index.md](index.md) decisions D1, D2, D6, and "What is deliberately
  not tested".

## Decisions to make first

None open. Two details worth settling before writing code, so they are
not re-derived per test:

- **Assert the exit code, not just the message.** Every validation test
  in task 5 pins `exitcode.CodeOf(err)` as well as the text. The
  taxonomy is the part a frontend depends on; the wording is not.
- **Prefer a real round trip over an internal assertion.** For the
  `entry.go` work, marshal → unmarshal → compare is the test, not an
  assertion about the emitted YAML's style. The style is an
  implementation detail `verifyFaithful` already guards; the property is
  that the value survives.

## Tasks

Tests first, at task granularity: write a task's tests, get them green,
move on. Do not author the whole list up front.

### 1. Entry YAML internals — `internal/gage/entry.go`

The single largest honest gap (47 uncovered blocks).

- [ ] `Timestamp.MarshalYAML` emits the underlying `time.Time`: marshal a
      struct holding one and assert an unquoted RFC 3339 scalar, matching
      what `timestampNode` produces.
- [ ] `Timestamp.UnmarshalYAML` error paths, with distinct messages: a
      node that is not a string, and a string that is not RFC 3339.
- [ ] `unknownValueNode` over `Entry.Extra`, one case each: `nil`,
      `bool`, `int64`, `uint64`, `float64`, a nested `map[string]any`,
      and a type outside the closed set (the default `n.Encode` branch).
      Marshal, unmarshal, assert the value survives.
- [ ] `verifyFaithful` called directly with mismatched bytes: each of the
      four named fields (`title`, `description`, `updated_by`, `value`),
      the field-count mismatch, and a per-field value mismatch.
- [ ] `MarshalEntry`'s `styleAlwaysQuoted` fallback: an entry whose value
      defeats `styleReadable` so the second `marshalEntryNodes` pass
      runs and the result is still faithful.
- [ ] `ListEntries` skips a subdirectory and a stray non-`.age` file
      under `entries/` without failing the whole listing.

### 2. Search — `internal/gage/search.go`

- [ ] `Search` matches a **field value**, not just the key, and reports
      `MatchedBody: true`. Currently untested in either package.
- [ ] `sortSearchResults` tie-break: two entries sharing a title sort by
      ID.

### 3. `internal/gage/gitrepo`

- [ ] `RemoteState.String()` over all five states plus an out-of-range
      value → `state(N)`.
- [ ] `defaultBranchOf`, against bare repos built with `gittest`: no
      branches → the "empty repository, not a vault" error; `main` plus
      another → picks `main`; `master` plus another → picks `master`;
      two branches, neither named → the "several branches" error.
- [ ] `RemoteURL` and `HasRemote` on a repo with no remote → `("", nil)`,
      and on one whose remote is configured with zero URLs.
- [ ] `RemoteStateOf` with no remote → `RemoteNone`.
- [ ] `localAndRemoteHead` with no remote-tracking ref → `(local, nil, nil)`.
- [ ] `DirtyPaths` over more than `maxNamedDirtyPaths` files → the
      "and N more" suffix, with exactly `maxNamedDirtyPaths` named.
- [ ] `ResetHard` removes an untracked file and resets a modified tracked
      one.
- [ ] `LastCommitTime` and `History` on a repo with no commits → zero
      value and nil, no error.

### 4. Trust cache — `internal/gage/trustcache.go`

- [ ] `longestCommonSubsequence` table including a pure-insertion case
      (the uncovered `default` op), and `splitDiffLines("")` → nil.
- [ ] `recipient verify --repair` on an already-in-sync vault → no
      commit, and not an error.
- [ ] A recipient-change confirmation with a nil prompter → the
      `Conflict` refusal rather than a nil dereference; `--repair` with a
      nil prompter likewise.
- [ ] The `OnlyInConfig` reporting branch (a key in `.gage/config.toml`
      that is missing from `.age-recipients`).

### 5. Validation errors

Direct library calls; each pins the `exitcode` as well as the message.

- [ ] `enrollseal.go` `Seal`: invalid device name, invalid pubkey,
      unknown method, and a vault with no valid ID. `Open` on sealed
      contents that decrypt but are not TOML.
- [ ] `recipient.go` `AddRecipient` and the batch form: invalid device
      name, invalid pubkey.
- [ ] `recovery_enroll.go`: invalid device name and invalid pubkey in the
      recipient list; the "this change would leave nothing to encrypt to"
      refusal.
- [ ] `identity_add.go`: invalid device name; the identity listing skips
      directories and invalidly-named files rather than failing.
- [ ] `vault_create.go` `Create`: empty `Name`, empty `Path`, and a
      target path that already exists as a regular file.
- [ ] `generate.go`: `length <= 0` and an empty alphabet.
- [ ] `crypt.go`: `Recipient.String()`; a zero-value `Recipient` →
      `ErrInvalidRecipient`; `ParseRecipient` on garbage.

### 6. Session — `internal/gage/session.go`

- [ ] `Use("")` and an entry operation with no current vault →
      `ErrNoCurrentVault` at `Usage`, with the "run `use <vault>` first"
      guidance.
- [ ] An ambiguous resolve with a nil prompter returns the candidate list
      — the same outcome one-shot mode has — rather than dereferencing nil.

### 7. Leaf packages

Small, pure, high statement-per-test ratio.

- [ ] `exitcode`: `Code.String()` on an unregistered code → `code(N)`.
- [ ] `xdgpaths`: all three `panic("xdgpaths: unknown role")` sites
      (`xdgVar`, `unixDefault`, `windowsDefault`), via an out-of-range
      `Role` and `recover()`.
- [ ] `agekey`: a bech32 string whose human-readable part holds a
      non-printable byte → the "non-printable characters" message;
      `isPrintableASCII` at its boundaries (32, 33, 126, 127).
- [ ] `memlock`: `Lock` and `Unlock` on an empty slice → nil, with no
      syscall attempted.
- [ ] `syncerr`: `IsUnreachable(ErrUnreachable)`, and a wrapped
      `*net.OpError`.
- [ ] `vaultlock`: `(*Lock)(nil).Release()` → nil.
- [ ] `atomicfile`: `WriteFile` into a directory that does not exist →
      an error *and* no temp file left behind in the parent.
- [ ] `recipients`, `config`, `vaultconfig`: the parse-error paths on
      malformed input files.

## Definition of done

Every box above ticked, green on Linux, macOS and Windows CI. `make lint`
and `make test` clean. `make cover` total excluding `scripts/` at
**>= 88.5%** — report the measured figure in the sub-issue.

No file outside `_test.go` changed. If a test appears to require a
production change, stop and raise it rather than making the change:
that is a finding about the code, not a step in this milestone.
