# A stopped worker logged a retry of a model it had hung up on

**Date:** 2026-09-17 · **Status:** fixed on `goals/unverified-paths` · **Severity:** low — log noise that reads as an unreachable model endpoint on every graceful stop inside a model call

## Summary

`OpenAICompatible.Complete` retries retryable failures. A call whose context is cancelled fails in `http.Client.Do`,
which `attempt` wraps as `EXTERNAL_UNAVAILABLE` "cannot reach the model endpoint" — retryable — so the loop logged
`forge.llm.retrying` with that reason before its backoff noticed the cancel and returned.

## Symptom

SIGTERM to a real `forge-worker` in a Linux container, inside a step's model call
(`docs/spikes/2026-09-17-unverified-paths`, tag `stop1`), logged between `forge.worker.stopping` and
`forge.task.handed_back`:

```
forge.llm.retrying role=converse attempt=2 of=4 reason="... EXTERNAL_UNAVAILABLE ... (cannot reach the model endpoint at http://forge-uv-standin:18390/v1): ... context canceled"
```

and the same for a look (`role=vision`) in tag `stop2`. #105/#108/#109/#117 removed the other stop-time lines that
blamed a healthy dependency; this one is the model client's.

## Fix

After a failed attempt, a cancelled context returns at once (`INTERNAL`, "cancelled during attempt N; not retried"),
the same code the backoff's cancel path already returned. Nothing is retried for a caller that has gone.

## Verification

- `TestComplete_ACallItsCallerCancelsIsNotRetriedOrBlamedOnTheEndpoint` — the endpoint is asked once, no
  `forge.llm.retrying` is logged.
- Live (tag `stop4`): the stopped worker's log is `stopping`, `task.handed_back`, `stopped`, and nothing else.

## Regression prevention

Drill "a model call its caller cancelled is retried", seen red.
