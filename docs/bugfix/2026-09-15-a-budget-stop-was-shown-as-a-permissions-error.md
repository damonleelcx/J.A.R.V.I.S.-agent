# A build stopped by its own token ceiling was shown as a permissions error

**Date:** 2026-09-15 · **Status:** fixed (follow-up to #107, found by checking the card in a browser) ·
**Severity:** low — nothing ran wrongly; the card told the person the wrong reason

## Summary

When a build runs out of tokens, the proposal card names it as a budget stop — #107 added exactly that — but the
text beside the label was the raw Go error off the failed task:

> Stopped by its budget: engine.Budget: FORBIDDEN: The authenticated principal is not permitted to perform this
> action on this resource. (goal budget exhausted on tokens: used 1500 tokens of 800. Raise
> FORGE_MAX_TOKENS_PER_GOAL or the goal's own ceiling, or narrow the goal so it needs less context.)

So a person watching their own build, stopped by a ceiling they set, was told they are **not permitted to do
this**. The sentence that actually says what happened, and the remedy, were in the brackets at the end.

The fix retells the stop in the words written for it: the card shows the detail, and drops the `op:` and the error
code's generic sentence.

## Symptom

Seen on screen (`docs/spikes/2026-09-15-card-checked`): goal `gol_01M2KX8EDH1CFBMH541B7E5H8E`, `max_tokens` 800,
planning spent 400 and step 1's model call spent 1,100. The card settled to:

```
FAILED   0 of 3 steps done · 1,500 of 800 tokens
Stopped by its budget: engine.Budget: FORBIDDEN: The authenticated principal is not permitted to perform
this action on this resource. (goal budget exhausted on tokens: used 1500 tokens of 800. …)
```

Everything except that sentence was right: the step is `failed`, the two after it `skipped`, the count and the
ceiling are exact, and the stop is correctly classified as a budget stop rather than an ordinary failure.

## Impact

Presentational, and confined to the card. A reader is pointed at access control for a spending limit — the one
stop #107 singled out as "the one a person can act on, by raising a ceiling". The operations console, the
timeline and the API were unaffected: they show the same string, where the code and the op are what an operator
wants.

## Preconditions

A build goal whose token ceiling is exhausted **part-way through a step**, watched on the proposal card.

## Root cause

Two things met.

1. `errs.Error()` renders `"<op>: <CODE>: <what that code means in general> (<the detail written for this
   failure>)"`. Only the bracketed half was written about this stop; the rest is the error's own plumbing. The
   card rendered `task.error_detail` verbatim.
2. There was no cleaner string to prefer. `goalProgress` reads a `budget.exceeded` timeline event first and falls
   back to the task's detail — and **no such event is written for this breach**. `agent.Worker` appends
   `budget.exceeded` when the guard refuses a task *before* it starts; this breach happened inside the step, where
   `BuildSteps.run` returns `charged.Breach().Error()` as an ordinary task failure. The timeline for the goal above
   holds `plan.created`, `goal.activated`, `task.started`, `task.failed`, `goal.ended` and no budget event, so the
   fallback was always the path taken for a mid-step breach.

`FORBIDDEN` for a spending limit is itself arguable — a budget is not an authorisation — but that code is the
engine's contract, it is what `BudgetGuard` has always returned, and other readers key off it. Left alone
deliberately: see "Not fixed here".

## Why it did not show up before

`TestWorkbench_TheCardSaysABuildWasStoppedByItsBudget` supplies the detail by hand as
`"engine.Budget: goal budget exhausted on tokens: used 150 tokens of 150."` — a plausible string, and a tidier one
than the code produces. It has no error code in it and no brackets, so the fence asserted the label and the
numbers and never saw the sentence a real breach puts on screen. The fence was right about everything it checked.

## The fix

`plainStopText()` in `internal/httpapi/assets/workbench.js`, applied to the budget text and to a failed step's
detail:

- `"<op>: <CODE>: <cause> (<detail>)"` → the detail. Lazy before the bracket and greedy inside it, so a detail
  containing brackets survives whole.
- otherwise, a bare `"<pkg>.<Thing>: "` prefix is dropped.
- a plain sentence — "the model could not be reached" — is left exactly as it arrived.

The card now reads:

> Stopped by its budget: goal budget exhausted on tokens: used 1500 tokens of 800. Raise
> FORGE_MAX_TOKENS_PER_GOAL or the goal's own ceiling, or narrow the goal so it needs less context.

‼️ Presentation only, and after the decision: `goalProgress` still recognises a budget stop from the **unclipped**
detail, so cleaning the text cannot change how a stop is classified.

## Not fixed here

- **No `budget.exceeded` event for a mid-step breach.** Writing one would give the card (and the console timeline)
  a purpose-written sentence and would make the two breach paths consistent. That is an engine change, it affects
  the audit chain, and it is not needed to stop the card misinforming anybody. Recorded in the spike as found, not
  fixed.
- **`FORBIDDEN` as the code for a budget refusal.** The engine's contract, relied on elsewhere; changing it is not
  a card fix.

## Fence

`TestWorkbench_TheCardSaysWhatABudgetStopMeantWithoutTheErrorsPlumbing`
(`internal/httpapi/goal_progress_fence_test.go`) drives the card with the **exact** `error_detail` a real breach
produced and requires that the card shows the detail and its remedy, and shows neither `FORBIDDEN`, nor
`engine.Budget`, nor "authenticated principal".

## Drill

"a budget stop is shown as a raw error" (`scripts/drill-fences.sh`, section "Build goal UX") disables the
code-prefix branch of `plainStopText`. The new fence goes red; the older budget fence stays green, which is the
point — its hand-written string never exercised this.
