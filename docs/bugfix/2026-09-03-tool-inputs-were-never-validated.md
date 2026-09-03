# Tool inputs were documented as validated and never were

**Date:** 2026-09-03 · **Phase:** carried defect from wave 4 (tools) ·
**Severity:** high — every tool affected, and the contract asserted the
opposite · **Owner:** tools

## Symptom

`Contract.InputSchema` was documented as *"Validated before the tool runs, so a
malformed call fails at the boundary"*, and `Invocation.Input` as *"the raw
arguments, already validated against InputSchema"*. Nothing in the build did it.
The schema reached the model provider in the tool definitions and went no
further.

So an argument a contract forbade arrived at `Run`, and `encoding/json`
discarded it in silence. Two of the four real tools defended themselves with a
strict decoder; `workspace` and `shell` did not.

The sharpest instance, found while writing the fence: `shell_run` requires a
`reason` alongside its `command` — *"one short sentence on what this is meant to
establish, recorded in the audit trail"*. Nothing enforced it. A command could
run with no justification and the audit trail would record an empty one.

## Why two false promises are worse than one gap

An undocumented gap gets found. A documented promise gets **built on**: the next
tool is written against "arguments are already validated", skips its own
checking, and inherits a hole nobody put there deliberately. That is how the two
tools with strict decoders and the two without ended up in the same registry.

## The fix, and the rule that shapes it

`internal/tools/schema.go` validates a **bounded subset** of JSON Schema —
`type`, `properties`, `required`, `additionalProperties`, `enum`, `minimum`,
`maximum`, `minLength`, `maxLength`, `items`, plus the annotations. Between them
the shipped contracts use eight keywords, and implementing the rest to a standard
nobody exercises is how a validator acquires bugs in code no call site reaches.

**A schema using a keyword outside that set is refused at registration.** That
rule is the whole design. A validator that silently ignored `oneOf` would accept
everything `oneOf` was written to reject while the contract went on claiming the
arguments were checked — the original defect, reintroduced one level down.
Refusing means a tool whose schema outgrows the validator cannot start, rather
than cannot be trusted. `additionalProperties: {schema}` is refused for the same
reason: read as `true` it would silently widen every contract that used it.

Enforcement sits in the executor, after the permission check and **before** the
idempotency key. A call the grant forbids is refused whatever its shape; a
malformed call must not claim a key, or a later well-formed call with the same
arguments would deduplicate against a record of something that never ran.

The outcome is recorded as `refused` rather than `failed`: the tool did not run,
and `failed` would put a row in the ledger asserting an execution that never
happened. The model receives every problem at once — which field, and what was
wrong with it — because one error per round trip turns a two-field mistake into
two model calls.

## What was NOT done

The schemas were not widened to describe everything the tools actually care
about. `workspace_write` still validates that `path` is a string and not that it
stays inside the workspace — that is the tool's own check and belongs there. The
schema constrains what the **contract promised**, which is now true rather than
aspirational.

## Fences

- `TestSchema_RefusesAPropertyTheContractForbids` — the original defect.
- `TestSchema_RefusesAKeywordItCannotEnforce` — the load-bearing rule, over four
  real JSON Schema constructs this build does not implement.
- `TestSchema_RefusesASchemaValuedAdditionalProperties` — the silent widening.
- `TestBoundary_AMalformedCallNeverReachesTheTool` — written against a tool that
  records being run, because "validated" and "the tool coped" are
  indistinguishable from any assertion about the result.
- `TestBoundary_ShippedToolsRefuseWhatTheirContractsForbid` — including
  `shell_run` without its `reason`.
- `TestBoundary_EveryShippedToolAcceptsItsOwnDocumentedCall` — a gate that
  refuses correct arguments is an outage, not a control.
- `TestRegistry_ValidatingAnUnknownToolIsNotAPass`.

Four of these were mutation-drilled and go red.

## What to take from it

The comment was written when the intention was fresh and the implementation was
going to follow. It never did, and nothing noticed for four waves because the
comment read like a description. A promise in a doc comment is load-bearing:
somebody will build on it. Either make it true or write down that it is not.
