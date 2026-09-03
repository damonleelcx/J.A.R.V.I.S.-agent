# `forgectl memory forget` ignored the flags it then demanded

**Date:** 2026-09-02 · **Phase:** wave 3 (memory) · **Severity:** low —
one command was unusable · **Owner:** forgectl

## Symptom

```
$ forgectl memory forget mem_01M1H16CW0B8S8JF24CM0S1FYS --as usr_01M1H16CVN75PW0KN4EV92J2N3 --reason "measured wrong"

forgectl failed.
  error : VALIDATION_FAILED: (usage: forgectl memory forget <item-id> --as <user-id> [--reason ...]
A deletion has to name who asked, or it cannot be accounted for.)
```

The command refused for want of `--as`, which was supplied on the same line. The
same shape broke `forgectl memory purge --dry-run`.

## Root cause

Go's `flag` package stops parsing at the first non-flag argument. The command did:

```go
fs.Parse(args)          // args = ["mem_...", "--as", "usr_...", "--reason", "..."]
rest := fs.Args()       // = the whole slice; nothing was parsed as a flag
if len(rest) != 1 || *as == "" { ... }
```

`fs.Parse` saw the item id first and stopped, so `--as` was never bound and
`fs.Args()` returned five elements rather than one. Both branches of the guard
were true, and the error message named the flag that had in fact been given —
which is the worst version of this failure, because it sends the reader looking
at their own command line.

`forgectl approve` already had the correct shape (`cmd/forgectl/goal.go`): take
the positional argument, then `fs.Parse(args[1:])`. The new commands did not
follow it.

## What was fixed

`cmd/forgectl/memory.go` takes the id from `args[0]` and parses `args[1:]`, for
both `memory forget` and `memory purge`.

## Acceptance

Verified by running the binary against the development database:

```
$ forgectl memory forget mem_... --as usr_... --reason "measured wrong"
forgotten: mem_01M1H16CW0B8S8JF24CM0S1FYS
The key stays claimed, so FORGE will refuse to learn it again.
```

followed by `memory recall` returning nothing and `memory list` showing the item
as `forgotten 2026-09-02` with a null value.

## Prevention

**Not prevented by a test, and that is the finding.** Nothing in the suite
invokes `forgectl`'s argument parsing — the tests call handlers and services
directly, so every command's flag handling is unfenced. This bug was found by
running the command, and the next one of its kind will be too.

Recorded in `docs/implementation-plan.md` under Carried defects. The fix is a
table-driven test over `run(ctx, cmd, args)` asserting which commands accept
flags after a positional argument; it was not written here because it belongs to
the CLI rather than to memory.
