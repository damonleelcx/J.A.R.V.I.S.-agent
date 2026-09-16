# The proposal card, checked in a browser over two real builds

**Run 2026-09-15, 21:19–21:40 local, one Windows 11 laptop. Model spend: zero.**
Every model reply came from [`harness/stand-in.js`](harness/stand-in.js) on `127.0.0.1:18223`. No provider was
called and no key was used. Data: [`data/`](data). Harness: [`harness/`](harness) — checked for the session token
and the session secret: **0 occurrences each**.

**Why.** #107 built five things on top of #104's live exercise and could only fence four of them from the outside:
"the card in a real browser over a real build. The markup and CSS were not looked at on screen." It also left one
question open — whether a build's versions all land on one artifact — and #104 had listed that as "found, not
fixed". This is that look.

## How a browser got a session without a password being typed

The previous attempt at this spike stopped at the sign-up form, correctly: typing a credential into a form is not
this agent's to do. The way past it is the way the test suites make a principal —
[`harness/mintsession`](harness/mintsession/main.go) calls the domain's own constructors against the local
development database: `auth.HashPassword` over a generated throwaway that is never printed or reused,
`identity.Repository.CreateUser`, `MarkEmailVerified` (the consequential surface is gated on it),
a project written the way httpapi's `newProject` fixture writes one plus `access.Service.EnsureOwner`, and then a
session row built exactly as `identity.Service.SignIn` builds one: `auth.NewToken`, only its SHA-256 stored, both
timestamps from the application clock, inserted through `Repository.CreateSession`. Sign-in is not the route in;
the session row is.

The token is then held by [`harness/pane-proxy.js`](harness/pane-proxy.js), which adds it as a Bearer header to
everything the pane sends. **The page never holds a credential** — and `forged` on its own port refuses the same
request with 401, which is how we know the header is what is getting in.

Principal `cardcheck@forge.local` (`usr_01M2KWQNBPQ11KFGJBEJ95KD1Q`), project **"card check"**
(`prj_01M2KWQNC11A1VBJ1KT7E0Y77J`). Local development database only.

## Summary

1. **The card follows a build to the end, and the numbers are right.** Three states over 8.4 s: steps done/total,
   the step in hand, tokens, versions kept, and the goal's own word when it settled. Its 7,240 tokens are exactly
   the stand-in's eight calls.
2. **Polling starts on "Start it" and stops when the goal settles** — 3 poll pairs then silence for the first
   goal, 2 then silence for the second, measured from the browser's own network log.
3. **A budget stop is named as one**, with the ceiling shown beside the spend — but the sentence beside it was the
   raw Go error, which told a person they were "not permitted" to spend their own budget. **Fixed.**
4. **A goal started from the card was filed in a brand new project**, so the build's versions landed where the
   workbench's own panels do not look. **Fixed.**
5. **A failed turn is charged and kept.** The unusable reply shows in Telemetry as failed with its 2,950 tokens,
   and is left out of both medians.
6. **The console lists a goal's tasks in step order** (1, 2, 3; #104 saw 1, 2, 5, 3, 4).
7. **#107's open question, answered: a rename does start a second artifact.** Deliberate and documented; not
   changed. See [§5](#5-the-open-question-answered-a-rename-starts-a-second-artifact).

## 1. A build watched to the end

Typed into Talk: *"I want you to build a small desk lamp as a model for me: a round weighted base, a two-segment
arm with a hinge, and a conical shade. Keep it simple, three steps at most. Please propose it as work to do."*
(#104's sentence, so the two runs are comparable.)

**What a person saw**, in order:

1. **Talk.** A reply in 21 ms: *"I can build a small desk lamp as a model: a weighted base, a hinged arm and a
   conical shade. Shall I start?"*
2. **Work tab.** "R1 · PROPOSED — Build a small desk lamp model (stand-in)", the statement, **Start this**, an
   unticked **"Build it as a model, one step per task, each kept as a version"**, and "Nothing has been created."
3. **Ticked the box, pressed Start this.** "R1 · PLANNED, NOT RUNNING", the goal id, **3 steps** — Base, Arm,
   Shade — "Build it in 3 steps, each adding to the model the step before it kept.", **Start it — run 3 tasks**,
   **Open in operations**. *Three steps were asked for and three were planned*: #107's stated-limit fix, working
   on the statement the conversation produced. (#104's live planner produced 5.)
4. **Start it**, then the card, every 4 s — the whole point of #107, and the thing that showed nothing before:

| ms | what the card said |
|---|---|
| 405 | `ACTIVE` · 0 of 3 steps done · 400 tokens · Now: Step 1 of 3 — Base — ready · Base `READY` |
| 4,401 | 0 of 3 steps done · 1,500 tokens · Now: Step 1 of 3 — Base — **running** |
| 8,403 | `SUCCEEDED` · 3 of 3 steps done · 7,240 tokens · 2 versions kept, the latest `ver_01M2KX01EQQQ…` · Finished. All 3 task(s) finished: 3 succeeded, 0 skipped. |

Every state is in [`data/card-states.md`](data/card-states.md), verbatim.

**The tokens are exact.** Plan 400 + step 1,100 + look 920 + step 1,100 (a deliberately faulty reply) + repair 780
+ look 920 + step 1,100 + look 920 = **7,240**, which is the goal's `tokens_spent` to the token. A refused reply
and the repair it forced are both charged and both counted.

**"2 versions kept" for 3 succeeded steps is the card being honest, not miscounting.** Step 2's own timeline entry
reads *"Step 2 of 3 (Arm): 1 part(s), kept as version ver_01M2KX019E28AQXZQXYYGA0G1Y. Step 2 (Arm) was left out:
it would have broken the model."* — the same version id step 1 kept. `BuildSteps.run` reports the previous version
when a step keeps nothing, and the card counts distinct ids. Two versions were written and the card said two.

## 2. Polling starts, and stops

From the browser's own network log, which is the page and nothing else:

| goal | poll pairs (`GET /v1/goals/{id}` + `/timeline`) | after it settled |
|---|---|---|
| succeeded build | 3 — at 0 s, ~4 s, ~8 s | **no further request** |
| budget-stopped build | 2 — at 0 s, ~4 s | **no further request** |

`ForgeGoalProgress.interval` read 4000 ms in the live page. Later `/v1/goals` lines in
[`data/goals-proxy.log`](data/goals-proxy.log) are this spike's own `curl` calls.

## 3. A budget stop

The card has no ceiling field; the API has one. So `pane-proxy.js` added `"max_tokens": 800` to the card's own
`POST /v1/goals` and changed nothing else — everything else about the request is the page's. Planning cost 400 and
fitted; step 1's call cost 1,100 and breached.

The card settled to `FAILED`, step 1 `FAILED`, steps 2 and 3 `SKIPPED`, **"0 of 3 steps done · 1,500 of 800
tokens"**, and the stop classified as a budget stop rather than an ordinary failure — all correct. The text beside
it was not:

> Stopped by its budget: engine.Budget: FORBIDDEN: The authenticated principal is not permitted to perform this
> action on this resource. (goal budget exhausted on tokens: used 1500 tokens of 800. Raise …)

**Fixed** ([bugfix](../../bugfix/2026-09-15-a-budget-stop-was-shown-as-a-permissions-error.md)). Re-run after the
fix, on screen:

> Stopped by its budget: goal budget exhausted on tokens: used 1500 tokens of 800. Raise
> FORGE_MAX_TOKENS_PER_GOAL or the goal's own ceiling, or narrow the goal so it needs less context.

## 4. Where a card-started goal was filed

The conversation was in project "card check". Its build was not:

| | project | holds |
|---|---|---|
| the conversation | `prj_01M2KWQNC11A1VBJ1KT7E0Y77J` "card check" | the turns — and **0 artifacts** |
| the goal it started | `prj_01M2KWYKFENHFCSZHPY15V1S7Z` "Build a small desk lamp model (stand-in)" | the goal, and both versions |

The workbench's **Files panel said "This project has no files yet."** while the two versions it had just watched
being kept sat in a project of their own; the console listed both projects for one account. `startThis()` sent no
`project_id`, while the line below it blanked the `industry` *because* a project existed — a guard that had
outlived the field it was guarding, so the chosen industry was dropped too.

**Fixed** ([bugfix](../../bugfix/2026-09-15-a-card-started-goal-landed-in-a-new-project.md)). Re-run after the fix:
the third goal was filed in `prj_01M2KWQNC11A1VBJ1KT7E0Y77J` "card check" with its ceiling of 800, and **no new
project was created**.

## 5. The open question, answered: a rename starts a second artifact

#107 left this open, and #104 recorded the opposite symptom ("every step's version lands on one artifact named
after step 1 — Desk Lamp Base v1–v5"). The stand-in renames the document at every step — *Desk lamp base*, *Desk
lamp with arm*, *Desk lamp* — precisely to settle it.

**A rename starts a new artifact.** From the worker's log:

```
forge.artifact.versioned  artifact_id=art_01M2KX0198AHN2CPZD0XWK9A77  path=geometry/desk-lamp-base.forge.json  version=1
forge.artifact.versioned  artifact_id=art_01M2KX01ENWW7V84YM92YEW6EP  path=geometry/desk-lamp.forge.json       version=1
```

Two artifacts, each at **version 1** — not one artifact at v1→v2. (Two rather than three because step 2 kept
nothing.) The console's Artifacts panel duly listed "Desk lamp v1" and "Desk lamp base v1" side by side. The
mechanism is `geometry.Service.Save` → `artifactPath(doc.Name)`, and artifacts are unique per
`(project_id, path)`, so the document's **name** alone chooses the history it joins. #104 saw one artifact only
because its live model happened to keep the name stable across all five steps.

**Decision: correct as designed; no code changed.** `artifactPath` states this trade-off in its own "# Why", and
states it accurately: *"a model that renames slightly … starts a second history. That is visible and harmless,
because comparison takes arbitrary version ids and can span artifacts."* The alternative it rejects — threading a
variant id through the conversation — puts state in the client that the client is then trusted to report back.
Nothing was lost in this run: every version is reachable, the build's chain is intact through each step's
`from_version` input and the goal's timeline, and comparison spans artifacts.

It is worth revisiting, but not here, and this is the argument to hand whoever does: a **build goal** is the one
case where the chain is known in advance — step *n* is defined as adding to what step *n−1* kept — so a build
could pin the artifact chosen by step 1 and pass it down, without any client being trusted, and without changing
what a free conversation does. That is a change to #107's author's design, not a defect in it, so it is recorded
here rather than made.

## 6. A failed turn, and the console's ordering

**The unusable reply.** Sending a message containing "shape check" makes the stand-in answer with a `proposed_goal`
and nothing to say or show, which `Reply.validate` refuses. What happened, all of it new in #107:

- The transcript showed the refusal **in red**, in place: *"An external service replied in a shape this build
  cannot use. Do not retry…"*
- `forged` logged `forge.converse.turn` with `model=stand-in-converse tokens=2950 failed_reply_kept=true`.
- The turn row kept `failure = EXTERNAL_PROTOCOL_ERROR` and the refused reply itself, and
  `GET /v1/conversations/{id}` returned both to the owner.
- **Telemetry**, on screen: *"FAILED — THE REPLY COULD NOT BE USED … round trip 33ms server … 2950 tokens"*, and
  the medians (20 ms / 21 ms, n=1) are computed from the successful turn alone.

**Task order.** The console listed Step 1, Step 2, Step 3 in step order (#104 saw 1, 2, 5, 3, 4).

## Found, not fixed

- **No `budget.exceeded` event for a breach inside a step.** `agent.Worker` appends one when the guard refuses a
  task *before* it starts; a breach mid-step comes back through `BuildSteps.run` as an ordinary task failure. The
  card reads the event first and falls back to the task's detail, so for the commonest budget stop the fallback is
  always the path taken. Writing the event would give the card and the console timeline a purpose-written
  sentence; it is an engine change touching the audit chain, and the card no longer misinforms anybody without it.
- **`FORBIDDEN` as the error code for a budget refusal.** A budget is not an authorisation. It is the engine's
  contract and other readers key off it; not a card fix.
- **Step 2's repair was "left out: it would have broken the model"** and the step still reported `succeeded`,
  keeping the previous version. Correct as far as this run can tell — the stand-in's repaired document genuinely
  was refused — but a step that keeps nothing and still succeeds is worth a second look by someone who knows the
  intent.

## Not exercised

- **A real model.** Every reply was scripted. Whether a live planner keeps to a stated limit once told (#107
  finding 2) and whether a live conversation carries the limit into the proposal's statement are still unmeasured;
  this run's statement carried it because the stand-in was written to.
- **A build long enough to watch.** The whole three-step build took 8.4 s, so the card showed 3 states. A live
  build takes 5–15 s per step and would show more; nothing here says the card behaves differently over minutes.
- **The card while a worker stops**, approvals, a `clarification_needed` reply, and the 401/403 polling stops
  (the 404 path is fenced in node, not seen in the browser).
- **Light theme, and any viewport but 799 × 694.** The card was read at one size in dark theme.
- **CSS.** Layout was checked by eye on screen — nothing overflowed or overlapped — not measured.

## Conditions

Windows 11, Chrome in the Claude desktop Browser pane at 799 × 694 CSS px. `forged`, `forge-worker` and
`forgectl` built from `agent/build-goal-card-checked` (191ebde, plus this branch's fixes for the re-run) with
`GOWORK=off`. Local Postgres 17.11 on `127.0.0.1:55840`, migrated to 0023. CAD kernel build123d,
`FORGE_CAD_POOL=1`, scripts off. `FORGE_MAX_TOKENS_PER_GOAL=100000`. The database is shared with other agents'
work; nothing here deleted anything. One run of each.

## Method

```bash
. harness/env.sh                                     # GOWORK=off, the stand-in endpoint, no key
node harness/stand-in.js 18223 stand-in.jsonl &      # every reply, scripted
bash harness/forged.sh &                             # 127.0.0.1:18220
go run ./docs/spikes/2026-09-15-card-checked/harness/mintsession \
  -email cardcheck@forge.local -project "card check" -out session.json
node harness/pane-proxy.js 18221 18220 ceiling.txt goals.log session.json &   # the pane's origin
bash harness/worker.sh 2 &                           # ctrlrun + forge-worker (ctrlrun from #104's harness)
# then, in the pane: http://127.0.0.1:18221/workbench?project=<the project>
echo 800 > ceiling.txt                               # arms the ceiling for the NEXT card-started goal
```

Card states were captured by a read-only `setInterval` sampler reading `#proposal`'s `innerText`, recording only
when it changed. Nothing in the page was driven except by clicking and typing.

[`data/worker-2.log`](data/worker-2.log) has its `forge.worker.idle` polling lines removed — the worker polls every
2 s and sat idle for most of the run — and nothing else is filtered. The other logs are whole.
`data/goals-proxy.log` is the pane proxy's own record of every `/v1/goals*` request, which is how polling was shown
to start and stop.
