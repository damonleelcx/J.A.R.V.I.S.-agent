# Tool input schemas are documented as validated and are not

**Date:** 2026-09-02 · **Phase:** wave 3 (memory) · **Severity:** medium —
silent, and it defeats a control that several tools rely on · **Owner:** tools

## Symptom

Running the `memory_remember` tool by hand against the development database, with
the argument shape a model would actually emit:

```
model claiming 'how': ACCEPTED (this is a defect)
```

The tool declares no input for how something is known. Sending one anyway was
accepted, the field was discarded, and the item was stored with the derived label
as though nothing had been sent.

Nothing failed. Nothing was logged. The stored value was the *safe* one, which is
exactly why this would not have been noticed: a model that sent
`"how": "observed"` would have had it dropped, the row would say `inferred`, and
the model would carry on believing it had recorded a measurement.

## Root cause

Two comments in `internal/tools/tool.go` state that this is handled:

```go
// InputSchema is JSON Schema. Validated before the tool runs, so a
// malformed call fails at the boundary with a message the model can act on
// rather than inside the tool with a nil dereference.
InputSchema json.RawMessage
```

```go
// Input is the raw arguments, already validated against InputSchema.
Input json.RawMessage
```

Neither is true in this build. `InputSchema` has exactly one consumer:
`internal/tools/registry.go:183` passes it to the model provider as a function's
`Parameters`. No validator exists in the executor or anywhere else, so
`"additionalProperties": false` is enforced only by whatever the provider chooses
to do, and an unexpected field arrives at `Run` where `encoding/json` discards it
without comment.

This is not a regression. It has been true since the registry was written; the
comments described an intended design that was never built, and every reader
since has had a reason to believe the check existed.

**Why it surfaced now:** the memory tools are the first whose correctness depends
on a field being *absent*. `memory_remember` deliberately has no epistemic-label
input, because a model that can state how it knows something will state it, and
the label will be worth nothing (PRD RSN-05, and the fabricated-standards bug in
this directory). For every earlier tool an ignored extra field was harmless.

## What was fixed

`internal/tools/memory.go` decodes with `DisallowUnknownFields`, so an unexpected
argument is refused with `VALIDATION_FAILED` and a message naming the rule. The
refusal is made where it is certainly enforced rather than relying on a step that
is described but does not exist.

## What was NOT fixed

**The general defect remains, and every tool is affected.** `workspace_read`,
`workspace_write`, `workspace_list` and `shell` all decode leniently and would
also discard an unexpected field in silence.

It was left deliberately. Fixing it properly is a decision about the whole tool
surface — either a JSON Schema validator in the executor, or strict decoding as a
registry-wide rule with a fence — and making that call while building memory
would have widened a wave into a change touching every capability. It is recorded
in `docs/implementation-plan.md` under Carried defects.

Whoever takes it: the two comments above must be corrected or made true. A false
comment about a security-relevant check is worse than no comment, because it stops
the next person looking.

## Acceptance

`TestTool_ForgeCannotDeclareHowItKnowsSomething`
(`internal/tools/memory_test.go`) asserts both halves:

- the contract's schema declares no `how` and no additional properties, and
- a call that sends one is **refused**, and writes no row.

The second half is the one that matters. The first half existed before the fix
and passed throughout — a schema assertion cannot tell you whether anything reads
the schema.

## Prevention

The lesson is narrower than "test behaviour not configuration": it is that a
declaration is only a control if something in the same repository consumes it.
`InputSchema` was declared, validated at registration for *presence*, and never
read for *enforcement*. When asserting that a contract forbids something, assert
that the forbidden thing is actually refused at runtime.
