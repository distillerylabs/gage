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

- [x] `Timestamp.MarshalYAML` emits the underlying `time.Time`: marshal a
      struct holding one and assert an unquoted RFC 3339 scalar, matching
      what `timestampNode` produces.
- [x] `Timestamp.UnmarshalYAML` error paths, with distinct messages: a
      node that is not a string, and a string that is not RFC 3339.
- [x] `unknownValueNode` over `Entry.Extra`, one case each: `nil`,
      `bool`, `int64`, `uint64`, `float64`, a nested `map[string]any`,
      and a type outside the closed set (the default `n.Encode` branch).
      Marshal, unmarshal, assert the value survives.
- [x] `verifyFaithful` called directly with mismatched bytes: each of the
      four named fields (`title`, `description`, `updated_by`, `value`),
      the field-count mismatch, and a per-field value mismatch.
- [x] ~~`MarshalEntry`'s `styleAlwaysQuoted` fallback~~ — struck.
      Tried every string in `hostileStrings`, singly and combined across
      every field, plus Unicode line separators (U+2028/U+0085/U+2029),
      C0/C1 controls, YAML indicator characters, and long-line folding:
      none defeats `styleReadable` under the current yaml.v3. The branch
      appears unreachable with real `Entry` content on this library
      version, and forcing it needs a seam (e.g. an injectable
      `marshalEntryNodes`) this milestone rules out (D2). The style
      itself — `marshalEntryNodes(e, styleAlwaysQuoted)` — is already
      covered directly by `TestEntryQuotedFallbackRoundTripsEverything`.
- [x] `ListEntries` skips a subdirectory and a stray non-`.age` file
      under `entries/` without failing the whole listing.

### 2. Search — `internal/gage/search.go`

- [x] `Search` matches a **field value**, not just the key, and reports
      `MatchedBody: true`. Currently untested in either package.
- [x] `sortSearchResults` tie-break: two entries sharing a title sort by
      ID.

### 3. `internal/gage/gitrepo`

Names corrected against the actual package while implementing (the
first-pass scan that produced this list inferred a few from surrounding
comments rather than reading the function signatures):
`defaultBranchOf` is `remoteBranch`, `RemoteStateOf` is `Compare`,
`localAndRemoteHead` is `headAndTracking`, `LastCommitTime`/`History` are
`HeadCommitterTime`/`Log`.

- [x] `RemoteState.String()` over all five states plus an out-of-range
      value → `state(N)`.
- [x] `remoteBranch` (Clone's branch selection), against bare repos with
      hand-pushed branches: no branch refs at all (a tag-only remote —
      truly empty fails inside go-git's own transport before reaching
      this function) → the "empty repository, not a vault" error; `main`
      plus an unrelated branch → picks `main`; `master` plus an unrelated
      branch → picks `master`; two unrelated branches, neither default →
      the "several branches" error.
- [x] `RemoteURL` and `HasRemote` on a repo with no remote configured at
      all → `("", nil)` / `false`; and on one whose `.git/config` has an
      `[remote "origin"]` section with no `url=` line — go-git's own
      `CreateRemote` refuses to construct that in memory, so this is
      built via a raw config edit, which is what a hand-edited or
      partially-written config actually looks like on disk.
- [x] `Compare` with no remote-tracking ref at all → `RemoteNone` (same
      code path, `headAndTracking`'s `ErrReferenceNotFound` branch,
      whether or not "origin" itself is configured — neither state has
      ever fetched). Also filled `RemoteBehind`, the one of the four
      states the existing four-states test doesn't reach.
- [x] `requireCleanWorkTree` (`DirtyPaths`' consumer) over more than
      `maxNamedDirtyPaths` dirty files → the "and N more" suffix, with
      exactly `maxNamedDirtyPaths` named.
- [x] ~~`ResetHard` removes an untracked file and resets a modified
      tracked one~~ — already covered by the existing
      `TestResetHardDiscardsModifiedAndUntrackedFiles`; no new test
      needed.
- [x] `Log` and `HeadCommitterTime` on a repo with no commits → nil/zero
      value, no error (distinct from the existing
      `TestLogOnAPathGitHasNeverSeenIsEmpty`, which has commits but an
      unknown path).

### 4. Trust cache — `internal/gage/trustcache.go`

- [x] `longestCommonSubsequence` table including a pure-insertion case
      (the uncovered `default` op), and `splitDiffLines("")` → nil.
- [x] `recipient verify --repair` on an already-in-sync vault → no
      commit, and not an error.
- [x] A recipient-change confirmation with a nil prompter → the
      `Conflict` refusal rather than a nil dereference (driven directly
      against the unexported `confirmRecipientTrust`, since every real
      `Identity`-producing path today requires a real `Prompter` to
      succeed, so `Identity.frontend()` can never actually be nil in
      practice — this proves the function's own contract rather than one
      specific caller's current inability to trigger it); `--repair` with
      a nil prompter likewise (that one *is* reachable as an ordinary
      library call, no caveat needed).
- [x] The `OnlyInConfig` reporting branch (a key in `.gage/config.toml`
      that is missing from `.age-recipients`), for both `VerifyRecipients`
      and what `--repair` does with it.

### 5. Validation errors

Direct library calls; each pins the `exitcode` as well as the message.

- [x] `enrollseal.go` `sealEnrollment`: invalid device name, invalid
      pubkey, unknown method, and a vault with no valid ID.
      `validateSealedPayload` on sealed contents that decrypt but are not
      TOML.
- [x] `recipient.go` `AddRecipient` and the batch form
      (`addRecipientsLocked`): invalid device name, invalid pubkey. Each
      re-validates independently, so both got their own test.
- [x] `recovery_enroll.go`: invalid device name and invalid pubkey in the
      recipient list were already covered by the existing
      `TestRecoverDeviceRefusalsWriteNothing` table (driven through the
      exported `RecoverDevice`) — no new test needed there. The "this
      change would leave nothing to encrypt to" refusal (`ErrLastRecipient`)
      was the actual gap: neither current caller of the pure `applySwap`
      (`RecoverDevice`, `RotateRecoveryKey`) can produce an empty result,
      since both always add at least one recipient, so this drives
      `applySwap` directly.
- [x] `identity_add.go`: invalid device name; the identity listing skips
      directories and invalidly-named files rather than failing.
- [x] `vault_create.go` `Create`: empty `Name`, empty `Path`, and a
      target path that already exists as a regular file.
- [x] `generate.go`: `randomString`'s own `length <= 0` and empty-alphabet
      guards — unreachable through `GenerateValue` with today's
      `GenerateOptions` (its length/alphabet resolution can't produce
      either), so driven directly, the same way the existing
      `TestRandomStringPropagatesReaderFailure` already does for the
      reader-failure case.
- [x] `crypt.go`: `Recipient.String()`; a zero-value `Recipient` →
      `ErrInvalidRecipient`. `ParseRecipient` on garbage was already
      covered by the existing `TestParseRecipientRejectsNonEncryptableStrings`.

### 6. Session — `internal/gage/session.go`

- [x] `Use("")` → `ErrNoCurrentVault` at `Usage`. (An entry operation
      with no current vault — `Vault("")`'s own guard, with the "run
      `use <vault>` first" guidance — was already covered by the
      existing `TestSessionWithNoCurrentVaultSaysSo`.)
- [x] An ambiguous resolve with a nil prompter returns the candidate list
      — the same outcome one-shot mode has — rather than dereferencing
      nil. A session with a nil `Prompter` can never unlock a vault
      through its own normal path, so this seeds the held vault directly
      with an `Identity` obtained outside the session.

### 7. Leaf packages

Small, pure, high statement-per-test ratio.

- [x] `exitcode`: `Code.String()` on an unregistered code → `code(N)`.
- [x] `xdgpaths`: all three `panic("xdgpaths: unknown role")` sites
      (`xdgVar`, `unixDefault`, `windowsDefault`), driven directly —
      `resolve()` itself can never reach an invalid `Role` into
      `unixDefault`/`windowsDefault`, since `xdgVar` runs the identical
      switch first and panics there before either would see one.
- [x] `agekey`: a bech32 string whose human-readable part holds a
      non-printable byte (a literal space, which also has no case, so it
      clears the earlier all-lower-or-all-upper check too) → the
      "non-printable characters" message; `isPrintableASCII` at its
      boundaries (32, 33, 126, 127).
- [x] `memlock`: `Lock` and `Unlock` on an empty slice → nil, with no
      syscall attempted. Already covered by the existing
      `TestEmptySliceIsANoOp`; no new test needed.
- [x] `syncerr`: there is no exported `IsUnreachable` — corrected to
      `isUnreachable`'s two branches, reached through the exported
      `ClassifyPush`/`ClassifyFetch`. An error already wrapping
      `ErrUnreachable` (idempotent re-classification) was the actual gap;
      the `*net.OpError`-specific branch turns out to be dead code,
      shadowed by the generic `net.Error` interface check immediately
      above it in the same function (any `*net.OpError` or `*net.DNSError`
      satisfies that check first), and is already incidentally exercised
      via that path by the existing "connection refused" test case.
- [x] `vaultlock`: `(*Lock)(nil).Release()` → nil.
- [x] `atomicfile`: the existing `TestWriteFileFailsForMissingDir`
      already covers a missing-directory error, but trivially so for "no
      temp file left behind" — `os.CreateTemp` itself never gets far
      enough to create one there. Added a second test that fails at
      `os.Rename` instead (destination path is an existing directory),
      which is a real failure *after* the temp file exists, so the
      cleanup defer's own contract is what's actually being checked.
- [x] `recipients`, `config`, `vaultconfig`: the parse-error paths on
      malformed input files — `recipients.Read`'s `scanner.Err()`
      (a line past `bufio.MaxScanTokenSize`, a real failure mode of a
      real file rather than an injected one) and `config.Read`'s
      malformed-TOML path were the two genuine gaps (`vaultconfig.Read`'s
      own malformed-input paths were already covered). Also added the
      `Write`-side atomicfile-wrap test to `recipients` and `vaultconfig`,
      both previously untested in that direction.

## Definition of done

Every box above ticked, green on Linux, macOS and Windows CI. `make lint`
and `make test` clean.

**Achieved: 87.95%** excluding `scripts/` (up from a measured 86.55%
baseline), against this doc's original **>= 88.5%** estimate. The
shortfall is real and worth recording rather than rounding away: every
task-list item is done, and what remains uncovered in `internal/gage`
past this point is, on inspection, overwhelmingly the same
`if err != nil { return fmt.Errorf(...) }` I/O-wrapper shape the plan
index rules out of scope (D2) — confirmed by walking the largest
remaining files (`session.go`, `recipient.go`, `enrollapprove.go`,
`identityfile.go`, `vault_create.go`) after the task list was complete.
The 88.5% figure was a planning-time estimate from the task list alone,
not a target derived from measuring what's actually reachable; 87.95% is
what's honestly reachable without a new fault-injection seam.

One item surfaced only by measuring the real profile, outside the
original task list: `RecipientChangeWarning.delta()`'s own default
case (a routine, in-sync change whose `config.toml` diff is real but
adds/removes no recipient — `[method].plugin` changing alone, say) was
untested, since every existing scenario test that changes `config.toml`
also changes a recipient. Added as `TestDeltaWithNoRecipientChangeSaysEditedAlone`
in `trustcache_test.go`.

No file outside `_test.go` changed. If a test appears to require a
production change, stop and raise it rather than making the change:
that is a finding about the code, not a step in this milestone.
