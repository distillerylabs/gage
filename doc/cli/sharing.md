# Sharing entries between vaults

`gage` has no per-directory or per-entry sharing inside a vault — recipients
are always vault-wide. If you want a subset of entries visible to a
different set of people than the rest of a vault, the answer is a
**different vault**, and `mv`/`cp` are how an entry gets from one to the
other.

```
gage mv <query> --to-vault <name> [--use NAME]   # move
gage cp <query> --to-vault <name> [--use NAME]   # copy, keeping the original
```

Both decrypt the entry from its source vault (which has to be unlocked) and
re-encrypt it to the destination vault's recipients. The destination itself
does **not** need to be unlocked — encrypting *to* a set of public keys
never requires holding any of the matching private keys. `mv` then removes
the entry from the source; `cp` leaves the original in place, so the same
secret ends up readable by both vaults' recipient sets.

```
$ gage init shared-family --method passphrase --recipient <family-member-pubkey>
$ gage mv family-wifi --to-vault shared-family
```

Both commands take two per-vault write locks (source and destination),
acquired in a fixed order, so two simultaneous moves between the same pair
of vaults in opposite directions can't deadlock.

The global `--yes` flag skips the recipient-change confirmation for the
destination vault, same as on other write commands — see
[Identities and recipients](identities-and-recipients.md#the-recipient-change-confirmation).

## Why not finer-grained sharing?

An earlier design let recipients be overridden per subtree, so one vault
could mix a household `shared/` tree with a private `personal/` tree. It
works, but a separate vault per trust boundary removes the whole class of
problem instead of mitigating it: with per-directory recipients, *which
directory an entry sits in* becomes a privilege boundary in itself —
exactly the kind of thing someone with git write access could exploit by
moving a file into a directory they happen to hold keys for. With one
recipient list per vault, there's no finer-grained boundary to attack:
"does this person have write access to this vault's repository" is the
entire question, and it's one your git host already answers.

What you give up: if one vault needs three different partial-access
groups, that's three vaults instead of one vault with three subtrees. In
practice that's usually the right shape anyway — separate vaults get
separate remotes, separate access lists on the git host, and separate
audit trails, which is what you'd want for genuinely different trust
groups regardless of how `gage` stored things internally.
