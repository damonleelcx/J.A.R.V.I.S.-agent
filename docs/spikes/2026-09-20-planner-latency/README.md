# Spike: where the planner's 128 seconds actually go

**Date:** 2026-09-20 · **Status:** done · **Issue:** 13 ("planner ~128 s against a 180 s timeout")

## Summary

- **FORGE's own code is 69 microseconds per plan.** Median over 5 samples of 4000 plans each, with the
  model's latency injected at zero. Prompt assembly is 15 µs; unmarshalling the reply, deriving
  dependencies, validating the graph and checking hazard coverage together are 55 µs.
- **At the issue's measured 128 s that is 0.0001% of the wall clock.** Everything else is the provider
  thinking. There is no FORGE-side optimisation worth writing.
- **So the remedy is NOT "stream or parse incrementally".** Streaming a plan would save microseconds and
  cost the property that makes a plan safe — `Truncated()` is checked before anything is believed, and a
  plan is useless until its last task arrives. The two things that move the number are the **timeout**
  and the **prompt size**.
- **The assembled prompt is 7341 characters, ~1835 tokens** (chars/4, an approximation — no tokenizer
  was run). The planner framing is 50.4% of it, the persona 45.6%, and the goal itself 3.9%.
- **Action taken:** `FORGE_PLANNER_REQUEST_TIMEOUT`, defaulting to `FORGE_LLM_REQUEST_TIMEOUT` so no
  existing deployment changes on upgrade. See "The timeout" below for why this is not the blanket raise
  `.env.example` already refused.

## Why this spike

Issue 13 records the planner at 128 s wall clock against a 180 s budget, 2982 reasoning and 4493
completion tokens, and three consecutive timeouts during one evaluation run — with the same call at 17 s
when deliberation is off. `.env.example:143-149` records the decision NOT to raise
`FORGE_LLM_REQUEST_TIMEOUT`, on the grounds that raising it hides the shape of the problem.

The issue's "done looks like" is *either the planner gets faster, or the timeout stops being a single
number shared with every other LLM call*. Choosing between those two needs one fact nobody had: how much
of the 128 s is ours. A live call measures FORGE and the provider added together and cannot separate
them, which is why this was measured with a stand-in.

## Method

`internal/agent/planner_latency_test.go`, run with `-v`. It is a measurement, not a fence: it always
passes and its output is the point. Asserting a wall-clock budget there would produce a test that goes
red whenever somebody else's build is running on the same laptop, and a flaky test teaches people to
ignore red.

**No live model calls.** The model is an in-test `llm.Client` stub that optionally sleeps for an injected
duration and returns a canned plan — a fan-out of three independent surveys joined by a synthesis and an
export, with `needs`/`produces` filled in, so the post-call measurement includes a dependency derivation
that actually has work to do. The real planner (`agent.Planner.Plan`) does everything else: persona
assembly, hazard brief, settlement brief, JSON extraction and repair, unmarshal, `deriveDependencies`,
`PlanResult.Validate`, `checkHazardCoverage`.

Prompt sizes are read from what the stub was handed, not rebuilt in the test — a measurement of a
reconstruction measures the reconstruction.

**On batching.** Each sample is 4000 plans, divided out. Go's monotonic clock on Windows advances in
~0.5–15.6 ms ticks and one plan costs far less than a tick, so measured one at a time every stage of
every sample reads exactly `0s`. That is a measurement of the clock. The first run of this test
reported precisely that, which is how the batching got written.

**Environment.** Windows 11, Go 1.26.5 windows/amd64, 16 logical CPUs. Contended: several other agents
were building and running tests in sibling worktrees on this machine throughout. That is why 5 samples
are taken and both the median and the full range are reported — the range column below is mostly a
measurement of the contention, and it is small enough that the conclusion does not depend on it.

## Raw results

Prompt size, characters and the chars/4 approximation (labelled an approximation because it is one):

```
persona (identity, soul, voice, character)   3350 chars  ~ 837 tok   45.6%
plannerFraming (static role framing)         3702 chars  ~ 925 tok   50.4%
per-goal user message                         288 chars  ~  72 tok    3.9%
TOTAL assembled prompt                       7341 chars  ~1835 tok
```

FORGE's own code per plan, model latency injected at 0, 5 samples × 4000 plans:

```
prompt assembly                       median  14.874 µs   range 13.399 µs … 21.949 µs
unmarshal + derive + Validate         median  55.215 µs   range 48.531 µs … 70.517 µs
everything that is not the model call median  69.316 µs   range 64.274 µs … 93.008 µs
```

At the issue's 128 s:

```
projected total   2m8.000069316s
FORGE's share     69.316 µs   =  0.0001%
```

The projection is arithmetic — the median above plus a constant. A real 128 s sample is available behind
`FORGE_PLANNER_LATENCY_SOAK=1` and is not run by default, because sleeping 128 s five times is eleven
minutes of test suite to confirm that `time.Sleep` sleeps. It was run once here, and it agrees:

```
SOAK (real, one sample, 128s injected): total 2m8.0002175s, FORGE's share 217.5 µs (0.0002%)
```

**A second run, under heavier contention**, is worth recording rather than hiding, because it is the
honest range on this machine and it changes nothing:

```
prompt assembly                       median  27.842 µs   range 19.393 µs … 30.630 µs
unmarshal + derive + Validate         median  79.960 µs   range 63.520 µs … 88.444 µs
everything that is not the model call median 107.388 µs   range 87.871 µs … 119.801 µs
```

So FORGE's per-plan cost on this laptop sits somewhere between ~65 µs and ~220 µs depending on what else
is compiling. Both ends round to the same conclusion at four decimal places.

## Conclusion, honestly

FORGE's share of a plan is a rounding error. Stated plainly so nobody re-opens this: **there is nothing
to optimise on our side.** 69 µs against 128 s is four decimal places below anything a person could
perceive, and it would stay a rounding error if it got a hundred times worse. Any proposal to stream the
planner's reply, parse it incrementally, or move validation off the request path is optimising a number
that is already zero — and streaming in particular would cost the truncation check that stops a
half-plan being executed as a whole one.

That leaves two levers.

### The prompt

7341 characters, ~1.8k tokens, of which 96% is the same on every single plan — the persona and the role
framing — and 3.9% is the goal being planned. This is the one input FORGE controls. It is not obviously
too large; a reasoning model's 128 s is mostly output and deliberation tokens (issue 13 records 2982
reasoning + 4493 completion against a prompt this size). But it is the number to watch, which is why it
is printed by the test rather than left to be guessed at. Issue 12's work added ~15 lines to
`plannerFraming` and this is the measurement that says what that cost: the framing grew, and the
framing is half the prompt.

### The timeout

`FORGE_PLANNER_REQUEST_TIMEOUT`, added in `internal/platform/config/config.go`.

`.env.example`'s standing argument is that raising `FORGE_LLM_REQUEST_TIMEOUT` hides the shape of the
problem. It does, and this change does not do that. The hiding in a blanket raise is that it applies to
every role: a hung converse turn, a stuck verifier, an unreachable endpoint would all be allowed to sit
for eight minutes before anything said so. A planner-specific number hides nothing — every other role
keeps the tight 3 m, the planner's slowness stays visible, and it is visible in a variable named after
the role that is slow. The general timeout going untouched is what makes that true, so it must stay
untouched.

Unset means `FORGE_LLM_REQUEST_TIMEOUT`, exactly, so upgrading changes no deployment's behaviour. 5 m is
the documented value to set once evaluation runs have actually timed out; it is commented out in
`.env.example` rather than shipped as a default, because a default that quietly raised every
deployment's planner budget is the blanket raise under a different name.

## What is not wired

**A planner timeout LARGER than the general one does not yet take effect end to end**, and the config
comment, `.env.example` and `Planner.WithRequestTimeout`'s doc all say so.

`Planner.Plan` applies its timeout as a context deadline around its own model call. A context deadline
can only make a call stricter. The `http.Client` inside `llm.NewOpenAICompatible`
(`internal/llm/openai_compatible.go:99`) is constructed with `Timeout: cfg.RequestTimeout` — the general
number — and that cap still bites first for anything above it. The HTTP-level change and the constructor
wiring are one line each and are listed in this PR's hand-off rather than made here, because both files
belong to other work in flight.

Everything below the general timeout — the direction the config fences prove — is live today.
