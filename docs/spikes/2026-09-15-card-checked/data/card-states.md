# Every distinct state the proposal card showed, with its timing

Captured in the Browser pane by a read-only sampler (`setInterval`, 400 ms, recording
`document.getElementById('proposal').innerText` only when it changed), so these are the words a
person actually had on screen and nothing derived. `ms` is from the moment the sampler started,
which is just before "Start it" for goal 1 and just before "Start this" for goal 2.

Both goals ran against the local stand-in model (`harness/stand-in.js`); no provider was called.

---

## Goal 1 — a build watched to completion

`gol_01M2KWYKFNWPZ7N8CES44N1A0J`, no ceiling. Three steps, `succeeded`, 7,240 tokens.

### 405 ms — activated, first poll

```
ACTIVE
Build a small desk lamp model (stand-in)
Build a small desk lamp as a model in three steps at most: a round weighted base, a two-segment arm with a hinge, and a conical shade.
gol_01M2KWYKFNWPZ7N8CES44N1A0J
R1  Step 1 of 3 — Base    READY
R1  Step 2 of 3 — Arm     PENDING
R1  Step 3 of 3 — Shade   PENDING
Build it in 3 steps, each adding to the model the step before it kept.
Open in operations
Started. Its tasks are now claimable — they execute when a FORGE worker is running (`make work`, or the forge-worker binary).
0 of 3 steps done · 400 tokens
Now: Step 1 of 3 — Base — ready
```

The 400 tokens are the planning call, already charged before any step ran.

### 4,401 ms — second poll, a step in hand

```
0 of 3 steps done · 1,500 tokens
Now: Step 1 of 3 — Base — running
R1  Step 1 of 3 — Base    RUNNING
```

1,500 = planning 400 + step 1's model call 1,100. The step is `running`, and the card says which.

### 8,403 ms — third poll, settled; polling stops here

```
SUCCEEDED
…
R1  Step 1 of 3 — Base    SUCCEEDED
R1  Step 2 of 3 — Arm     SUCCEEDED
R1  Step 3 of 3 — Shade   SUCCEEDED
3 of 3 steps done · 7,240 tokens
2 versions kept, the latest ver_01M2KX01EQQQ8CKS75XMNQ8XVX
Finished. All 3 task(s) finished: 3 succeeded, 0 skipped.
```

**"2 versions kept" for 3 succeeded steps is correct.** Step 2's own timeline entry reads
*"Step 2 of 3 (Arm): 1 part(s), kept as version ver_01M2KX019E28AQXZQXYYGA0G1Y. Step 2 (Arm) was
left out: it would have broken the model."* — the same version id step 1 kept. Step 2 kept nothing
new, so the card counts two distinct versions. It is reporting what happened, not miscounting.

Tokens reconcile exactly against the stand-in's log: plan 400 + step 1,100 + look 920 + step 1,100
(the faulty reply) + repair 780 + look 920 + step 1,100 + look 920 = **7,240**, which is the goal's
`tokens_spent`. A refused reply and its repair are both charged, and both are counted here.

---

## Goal 2 — stopped by a token ceiling of 800

`gol_01M2KX8EDH1CFBMH541B7E5H8E`, `max_tokens` 800 (added to the card's own `POST /v1/goals` by
`harness/pane-proxy.js`, because the card has no ceiling field and the API does).

### 17 ms / 422 ms — proposed, then planned

```
R1 · PROPOSED   →   R1 · PLANNED, NOT RUNNING
gol_01M2KX8EDH1CFBMH541B7E5H8E
R1  Step 1 of 3 — Base
R1  Step 2 of 3 — Arm
R1  Step 3 of 3 — Shade
Build it in 3 steps, each adding to the model the step before it kept.
Start it — run 3 tasks
```

Planning cost 400 and fitted inside the 800 ceiling, so the plan landed.

### 26,017 ms — activated

```
0 of 3 steps done · 400 of 800 tokens
Now: Step 1 of 3 — Base — ready
```

The ceiling is shown beside the spend, which the uncapped goal above had no figure for.

### 30,013 ms — the budget stop; polling stops here

```
FAILED
R1  Step 1 of 3 — Base    FAILED
R1  Step 2 of 3 — Arm     SKIPPED
R1  Step 3 of 3 — Shade   SKIPPED
0 of 3 steps done · 1,500 of 800 tokens
Stopped by its budget: engine.Budget: FORBIDDEN: The authenticated principal is not permitted to
perform this action on this resource. (goal budget exhausted on tokens: used 1500 tokens of 800.
Raise FORGE_MAX_TOKENS_PER_GOAL or the goal's own ceiling, or narrow the goal so it needs less
context.)
```

Everything is right except the sentence: the stop is classified as a budget stop and not an
ordinary failure, the counts and the ceiling are exact, the failed step is named and the two after
it are `skipped`. But the text is the raw Go error, so a person who set a ceiling is told they are
**not permitted** to do this.

Fixed in this branch — the card now reads:

```
Stopped by its budget: goal budget exhausted on tokens: used 1500 tokens of 800. Raise
FORGE_MAX_TOKENS_PER_GOAL or the goal's own ceiling, or narrow the goal so it needs less context.
```

docs/bugfix/2026-09-15-a-budget-stop-was-shown-as-a-permissions-error.md

---

## Polling started and stopped

From the browser's own network log, which is the page and nothing else:

| goal | poll pairs (`GET /v1/goals/{id}` + `/timeline`) | after it settled |
|---|---|---|
| 1 | 3, at 0 s, ~4 s, ~8 s | no further request |
| 2 | 2, at 0 s and ~4 s | no further request |

`ForgeGoalProgress.interval` read 4000 ms in the live page. The later entries in
`data/goals-proxy.log` are this spike's own `curl` calls, not the card.
