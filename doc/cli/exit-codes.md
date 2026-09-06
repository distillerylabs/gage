# Exit codes

Every `gage` command exits with one of a fixed set of codes, so scripts and
CI can branch on *why* a command failed rather than just that it did.

| Code | Name | Meaning |
|---|---|---|
| `0` | `success` | The command did what it was asked. |
| `1` | `conflict` | Something needs a human to resolve: a sync divergence, a `recipient verify` mismatch, a dirty `entries/` working tree from an interrupted operation, etc. |
| `2` | `usage` | The command line itself was invalid: bad flags, an unknown (sub)command, a mutually exclusive flag combination. |
| `3` | `not-found` | A query resolved to no entry. |
| `4` | `ambiguous` | A query resolved to more than one candidate and there was nobody to ask (one-shot mode only — see [Addressing entries](addressing-entries.md)). |
| `5` | `locked-or-auth` | An unlock failed: wrong passphrase, no local identity registered for this device, an expired git auth token, etc. |
| `6` | `internal` | An unexpected error that doesn't fit any of the above. |
| `7` | `unreachable` | A remote could not be reached at all (DNS failure, connection refused, timeout). Only an explicitly invoked `sync`/`pull`/`push` reaches this — `gage`'s automatic sync around `use` and writes never surfaces it, since being offline there just produces a warning and the triggering operation still succeeds on its own terms. See [Sync and conflicts](sync.md). |

`unreachable` (`7`) is deliberately distinct from `conflict` (`1`): nothing
about either side's state is wrong in an unreachable case, and there's
nothing for a human to resolve — the same command is simply worth retrying
once the network is back. A script can tell the two apart and act
accordingly.
