# A failed workbench turn left no trace: its reply was lost and its tokens were counted nowhere

**Date:** 2026-09-15 · **Status:** fixed (stacked on #104, build goal UX) · **Severity:** medium: paid model output
disappeared without a record, so neither the cost nor the cause of a failed turn could be established

## Summary

When the model answered a workbench turn with a reply the turn could not use, `RespondStream` returned the refusal and
nothing else. The handler logged the error and sent the red note, and that was all: FORGE's half of the turn was not
recorded, the reply was gone, and the tokens the call cost were written nowhere. The live exercise that found it had to
estimate the spend (~11,300 tokens) and could only guess why the reply was refused.

The refusal now travels as an `agent.UnusableReply` carrying the bounded reply, the model and the provider's token count.
The handler records FORGE's half of the turn: what the person was shown, the error code, the refused reply and the
tokens (migration 0023). The Telemetry history lists it, marked failed. A failed turn is never handed back to the model
as history.

## Symptom

Seen live on 2026-09-15 ([`docs/spikes/2026-09-15-build-goal-exercised`](../spikes/2026-09-15-build-goal-exercised), §1):
the first workbench turn failed after 2.7 s with *"An external service replied in a shape this build cannot use. Do not
retry…"*, and forged logged `Reply.validate … the reply carried nothing to say or show`. `forge_conversation_turns` held
only the person's row. The streamed path logs no `forge.llm.completed`, and a turn's `tokens` were logged and stored only
when it succeeded.

## Impact

- **Spend on failed turns was invisible.** The provider charged for the reply; no row, no log line and no panel said so.
  A deployment whose model fell into this shape repeatedly would spend on turns that the Telemetry history showed as
  never having happened.
- **The cause was unrecoverable.** Without the reply, "the reply carried nothing to say or show" could not be traced to
  what the model actually sent. The spike's best guess, a reply carrying only a `proposed_goal`, stays a guess.
- **The record did not match the screen.** A reload showed the person's sentence with no answer under it, although the
  person had been shown a failure.

## Preconditions

A streamed or buffered workbench turn whose model reply arrives complete and is then refused: `Reply.validate` (no
speech and no detail), or JSON that neither parses nor falls back to readable text. A model that cannot be reached, or a
stream cut off, fails before a reply arrives and is out of scope (below).

## Root cause

`internal/agent/converse_stream.go`, inside the final chunk's callback:

```go
if err := reply.validate(); err != nil {
    return err
}
```

The accumulated text and `chunk.Usage` were in scope and discarded. `Respond` did the same with `resp`. In
`internal/httpapi/converse.go` the `emitErr != nil` branch had only the error, so it could write neither a turn nor a
cost. Migration 0022 §4 had named the gap ("A turn that FAILED. Nothing is written for one today") and deferred it.

## Why it did not show up before

- **Every converse fence asserts successful turns.** The record, telemetry and history fences all drive a stub that
  answers well. None drove a reply that is refused after it arrives.
- **The panel said so, and it read as a design choice.** Telemetry's `excludes` said failed turns were absent. That was
  true, and it hid the case where a failed turn had also cost money.
- **Nobody had watched a failed turn's spend.** The live spend accounting in the spike was the first to reconcile
  provider usage against what FORGE recorded, and it came up ~11,300 short.

## Fix

- `agent.UnusableReply{Err, Raw, Model, Tokens}` (`internal/agent/unusable.go`) wraps the refusal, and `Unwrap` keeps its
  code for `errs.CodeOf`. The raw reply is clipped to `UnusableReplyLimit` (8,000 characters) with a marked cut.
  `RespondStream` returns one for a refused reply and for a stream that produced neither JSON nor text; `Respond` does
  the same.
- Migration `0023_failed_turns.sql` adds `failure` and `unusable_reply` to `forge_conversation_turns`. Checks: only a
  FORGE turn fails; a kept reply needs a failure; the reply is bounded. `conversation.Turn.Validate` mirrors all three.
- `ConverseHandlers.keepFailed` records FORGE's half. The text is the sentence the person was shown, so the table's
  "said something" rule holds without inventing speech. It also writes the error code, the refused reply, and a timing
  with the model, the tokens and the round trip. It runs on its own bounded context. The warning log gains `model`,
  `tokens` and `failed_reply_kept`. The raw reply stays out of the log: a log cannot be deleted with the conversation
  (AUD-07), and a reply can quote the person.
- **‼️ `historyFor` skips failed turns.** An error message or refused model output handed back as FORGE's words would be
  a turn that never happened (SEC-04, RSN-06).
- `GET /v1/conversations/{id}` returns `failure` and `unusable_reply` to the conversation's owner, marked untrusted. The
  workbench does not render it. `GET /v1/telemetry/turns` lists a failed turn with `failed: true` and its tokens, and
  leaves it out of both medians; its `excludes` and the panel's "Not measured here" now say which failures are absent.

## Verification

- `TestRespondStream_AReplyThatCouldNotBeUsedComesBackWithTheReplyAndItsCost` and
  `TestRespond_AReplyThatCouldNotBeUsedComesBackWithTheReplyAndItsCost` (internal/agent/unusable_test.go) drive a reply
  carrying only a `proposed_goal`. Each requires an `UnusableReply` with the reply, the model, the tokens and
  `EXTERNAL_PROTOCOL_ERROR`. `TestUnusable_AReplyIsKeptBoundedAndSaysItWasCut` checks the bound.
- `TestConverse_AReplyThatCouldNotBeUsedIsKeptWithWhatItCost` (internal/httpapi/converse_failed_test.go, Postgres) drives
  `Converse` with a streaming stub that reports 11,275 tokens. It requires two turns, FORGE's with the failure code, the
  reply, 11,275 tokens and the sentence shown, and Telemetry listing it as failed with its tokens.
- `TestConverse_AFailedTurnIsNotGivenToTheModelAsHistory` requires the next turn's request to hold the person's earlier
  sentence and neither the refused reply, the failure text, nor any assistant turn.
- `TestTurn_OnlyAFailedForgeTurnKeepsARefusedReply` holds the three validation rules.

## Regression prevention

`scripts/drill-fences.sh`, section "Build goal UX": "a refused reply is returned without the reply or its cost"
(`converse_stream.go`), "a failed turn is not recorded" (`httpapi/converse.go`) and "a failed turn is replayed as
history" (`httpapi/converse.go`). Each went red.

## Not in this fix

- **Turns that fail before a reply arrives.** An unreachable model, a stream cut off, or a stream that ends with no
  content (`llm.Stream`'s own `EXTERNAL_PROTOCOL_ERROR`) carry no reply and no usage, and are still only logged. A cut
  stream may still have been charged by the provider; nothing here can know how much.
- **A refused `resolveEdit`** still emits its error as an event and ends the turn without `done`. It is a different
  shape (the reply was usable, the edit was not) and is unchanged.
- **Why the live reply was refused.** Still unknown: the reply was lost before this fix. The next live occurrence will be
  in `unusable_reply`.
- **`forge.llm.completed` on the streamed path** is still not logged; the turn row now carries the tokens instead.
