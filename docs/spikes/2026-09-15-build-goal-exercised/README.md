# A build goal from the workbench, a graceful worker stop, and the viewport in a hidden pane

**Run 2026-09-15, 13:56–14:32 local, one Windows 11 laptop. Live spend: 82,654 tokens of a 100,000 cap.**
Data: [`data/`](data). Harness: [`harness/`](harness) (no secrets; every committed file was checked for the model
key, the session secret, the test password and the session token: 0 occurrences each).

**Why.** #90 made a build startable from the API and from the workbench's proposal card, and #85 made a stopped worker
hand its step back. Both were fenced against Postgres with a stub model; neither had been used through the page, and
the only live stop so far ([`2026-09-15-build-goal-live`](../2026-09-15-build-goal-live)) was `taskkill /F`. #93's
measurement lost its frame numbers when the Browser pane went hidden, and nobody knew what a hidden pane does to the
lazy loader.

## Summary

1. **From the workbench, a build runs end to end on the live model.** A conversation turn proposed the work, the
   proposal card's "Build it as a model" box planned it as 5 steps, "Start it" activated it, and one forge-worker built
   all 5 in 45 s: 5 versions, 1 → 6 parts, the goal `succeeded`, **59,560 tokens, exactly the goal's `tokens_spent`**.
2. **`POST /v1/goals` with `"build": true` works over HTTP** (201 in 2.35 s, a draft of 2 build steps, 437 tokens), and
   so does `POST /v1/goals/{id}/start`.
3. **A real console Ctrl-Break stops forge-worker gracefully on Windows** (exit 0 in 6–19 ms), and **a step it was
   building is handed back at once**: `ready`, no lease owner, at the first sample 0.5 s after the stop, 46 s before
   its lease would have run out. A restarted worker claimed it within 0.5 s and **did not rebuild the step kept before
   it**. The stop-and-resume run used a local stub model (below, and why).
4. **Defect found and fixed:** every graceful stop mid-task logged two `DATABASE_UNAVAILABLE` warnings for a healthy
   database, and a task that finished as the stop arrived left its next step pending until an idle poll
   ([bugfix](../../bugfix/2026-09-15-a-stopping-worker-reported-its-own-stop-as-a-database-outage.md)).
5. **A hidden pane does not break the lazy loader.** A 30,023-occurrence car loaded its whole first view, and later its
   25,326-part seam subtree, with `visibilityState: hidden` and no animation frames at all; the loader's state was
   the same as a visible load's. No viewport defect, so no change to `viewport/subtree-loading`; the measurement is
   recorded here.

## 1. A build goal from the workbench (#90)

**Setup.** `forged` and `forge-worker` built from `agent/build-goal-entry` (917d9a2), local Postgres (migrated with
`forgectl migrate`), the production model endpoint (`token-plan.cn-beijing.maas.aliyuncs.com`, converse `qwen3.7-plus`,
vision `qwen3.8-max`), the CAD kernel (build123d 0.11.1, `FORGE_CAD_POOL=1`, scripts off). A local account
`livegoal-exercise@forge.local` made with `POST /v1/auth/sign-up` and `/sign-in`. The Claude desktop Browser pane
(Chrome 152) reached forged through [`proxy.js`](harness/proxy.js), which adds the session as a Bearer header, so the
page never held a credential. `FORGE_MAX_TOKENS_PER_GOAL=100000`; each goal's `max_tokens` set to 100,000 before it was
started (there is no API field for it, so by SQL). From the second turn on, forged's and the worker's model calls went
through [`rec-proxy.js`](harness/rec-proxy.js), which records the provider's usage for every call
([`data/model-calls.jsonl`](data/model-calls.jsonl), metadata only). A watchdog ([`watch.sh`](harness/watch.sh))
would have stopped the worker at 88,000; it never fired.

**What a person saw**, in order:

1. **Workbench, Talk.** Typed: *"I want you to build a small desk lamp as a model for me: a round weighted base, a
   two-segment arm with a hinge, and a conical shade. Keep it simple, three steps at most. Please propose it as work to
   do."* The first attempt failed after 2.7 s with a red note: *"An external service replied in a shape this build
   cannot use. Do not retry…"* (forged: `Reply.validate … the reply carried nothing to say or show`). The same
   sentence again, after a reload: *"I will model a simple desk lamp with a weighted base, two-segment arm, and
   conical shade. Shall I start building it now?"* — first token 982 ms, full reply 2,285 ms, 11,275 tokens (10,880
   of them cached prompt).
2. **Work tab, Proposed work.** A card: "R1 · PROPOSED — Build simple desk lamp model", the statement, a **Start this**
   button, an unticked box **"Build it as a model, one step per task, each kept as a version"**, and "Nothing has been
   created. Starting this writes a draft goal and plans it — it does not run it."
3. **Ticked the box, pressed Start this.** 3.7 s later: "R1 · PLANNED, NOT RUNNING", the goal id, **5 steps** (Base,
   Lower Arm Segment, Elbow Joint, Upper Arm Segment, Lampshade — three were asked for), "Build it in 5 steps, each
   adding to the model the step before it kept.", **Start it — run 5 tasks**, **Open in operations**, and "The goal is
   a draft. These tasks exist and no worker can claim them until you start it. Each is one step of the build; a worker
   with the CAD kernel builds it and keeps the model as a version before the next begins." Planning: 602 tokens.
4. **Start it.** "Started. Its tasks are now claimable — they execute when a FORGE worker is running." The card shows
   nothing more after that.
5. **Operations console** (`/console#goal=…`): Goals "Done · 5/5 done"; the goal panel "5 / 5 tasks done · 59,560
   tokens · autonomy sandbox_execute · ceiling r1"; five tasks SUCCEEDED, "CHECK NOT REQUIRED"; Artifacts "Desk Lamp
   Base" v1–v5; the timeline `plan.created` → `goal.activated` → per step `task.started`, `artifact.changed`,
   `task.succeeded` naming the version kept → `goal.ended` "All 5 task(s) finished: 5 succeeded, 0 skipped."

**The run** ([`data/worker-1-live-build.log`](data/worker-1-live-build.log)):

| step | calls (tokens) | took | kept |
|---|---|---|---|
| plan (forged) | 1 (602) | 3.7 s | 5 steps |
| 1 Base | step 10,054 + look 1,104 | 14.8 s (kernel start 7.7 s) | v1, 1 part |
| 2 Lower Arm Segment | 10,337 + 1,107 | 6.1 s | v2, 2 parts |
| 3 Elbow Joint | 10,530 + look 1,121 + interference repair 1,260 | 13.4 s | v3, 4 parts ("FORGE moved them apart") |
| 4 Upper Arm Segment | 10,567 + 1,122 | 5.2 s | v4, 5 parts |
| 5 Lampshade | 10,629 + 1,127 | 6.0 s | v5, 6 parts |

Every look answered `{"problems": []}`. The worker's 11 calls (58,958) plus planning (602) are **exactly** the goal's
59,560. The final model ([`data/lamp-final.json`](data/lamp-final.json)) is 6 flat parts: a base cylinder, a lower arm,
an elbow pivot block and pin, an upper arm turned −90° and a cone shade on its end; steps 4 and 5 report the pin 31%
inside the pivot block, 30% inside the upper arm and the block 20% inside the arm, "deliberate at this stage, so nothing
was moved".

**Over HTTP** ([`data/http-goal.json`](data/http-goal.json)): `POST /v1/goals` with `{"title": "HTTP build goal: bench
vise (live exercise)", …, "project_id": <the lamp's project>, "build": true}` → **201 in 2.35 s**, a draft with keys
`build, goal, plan_version, rationale, running, tasks`: "Step 1 of 2 — Base and Fixed Jaw", "Step 2 of 2 — Sliding
Mechanism", both pending, 437 tokens. `POST /v1/goals/{id}/start` → 200, `active`. That goal was then built by the stub
model in the stop drill below.

### What it cost

| what | tokens | how known |
|---|---|---|
| workbench turn 1 (failed) | **~11,300 (estimate)** | recorded nowhere; turn 2 is the same prompt |
| a 200-token probe of the endpoint (diagnosing turn 1) | 82 | provider usage |
| workbench turn 2 (the proposal) | 11,275 | provider usage via rec-proxy |
| lamp planning | 602 | goal `tokens_spent` (the proxy could not read that gzip reply; fixed before the build) |
| lamp build, 11 calls | 58,958 | provider usage; goal `tokens_spent` agrees |
| vise planning over HTTP | 437 | provider usage; goal agrees |
| **total** | **82,654** | |

The stop drill spent no live tokens.

### Found, not fixed

- **A failed workbench turn is charged nowhere and its reply is lost.** Turn 1 left only the person's row in
  `forge_conversation_turns`; the streamed path logs no `forge.llm.completed`, and a turn's `tokens` are logged only when
  it succeeds. Its reply was not captured, so why it had neither `speech` nor `detail` is unknown — a reply carrying only
  a `proposed_goal` would be rejected whole by `Reply.validate`, but that is a guess.
- **The planner ignored "three steps at most"** and planned 5.
- **The proposal card goes quiet after "Started."** Progress is only in the operations console.
- **The console lists a goal's tasks out of step order** (1, 2, 5, 3, 4).
- **Every step's version lands on one artifact named after step 1** ("Desk Lamp Base" v1–v5).
- **A goal's token ceiling cannot be set from the API or the card.**

## 2. A graceful worker stop, on Windows

**Sending the signal.** forge-worker stops on `signal.NotifyContext(os.Interrupt, SIGTERM)`. On Windows, Go delivers
`CTRL_C_EVENT` and `CTRL_BREAK_EVENT` as `os.Interrupt`; `taskkill` without `/F` posts `WM_CLOSE`, which a console
process never receives, and with `/F` is a kill. [`ctrlrun`](harness/ctrlrun/main.go) starts the worker with
`CREATE_NEW_PROCESS_GROUP` on its own console and, when a trigger file appears, calls
`GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, pid)` (Ctrl-C is disabled in a new process group; Ctrl-Break is not). This
is the worker's real signal path, not a context cancelled from a test.

**Idle.** Three stops of an idle worker: exit 0 in **6, 11 and 10 ms**, each logging `forge.worker.stopping` then
`forge.worker.stopped` ([`data/ctrlrun-*.log`](data)).

**Mid-step, then resume.** The live lamp finished too fast to stop (the driver waited for step 2 to be 20 s old; no
step took more than 15 s), and 17k tokens were left under the cap — not enough for a kept step plus half of another.
So the drill ran with the same binaries, kernel, database, lease (60 s) and console event, against
[`stub-model.js`](harness/stub-model.js): it answers a step with a whole document of one box per step so far and a look
with `{"problems": []}` (the shapes the live run's calls were answered with), reports 1,100 tokens a call, and holds
step 2's reply for 45 s. The goal was the vise drafted over HTTP. [`stop-resume.sh`](harness/stop-resume.sh) drove it
([`data/drill.log`](data/drill.log), [`data/stub-model.log`](data/stub-model.log)):

| time | what |
|---|---|
| 14:10:23.5 | step 1 kept (v1, 1 part); step 2 claimed, its model call held |
| 14:10:37.474 | `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT)`; the stub sees the worker hang up the same millisecond |
| 14:10:37.493 | worker exits, code 0 (**19 ms**) |
| 14:10:37.906 | step 2 `ready`, no lease owner — **first sample** (its lease ran to 14:11:23) |
| 14:10:44.13 | a second worker starts |
| 14:10:44.68 | step 2 claimed again, **attempt 2, within 0.5 s** |
| 14:11:34.6 | step 2 kept (v2, 2 parts); goal `succeeded` |

The stub was asked for step 1 **once**: the kept step was not rebuilt. The goal's 4,837 tokens are the 437 of live
planning and four stub calls (step 1 and its look, step 2's second attempt and its look); the abandoned call charged
nothing. Against the live model the abandoned call would still have been paid for and not recorded, as the earlier
live spike noted.

**Defect: the stop was logged as a database outage.** At 14:10:37.481 the stopping worker wrote
`forge.task.release_failed … DATABASE_UNAVAILABLE … context canceled` and `forge.goal.settle_failed …` — the task had
been handed back through the same pool 6 ms earlier. `Run` did the end-of-task bookkeeping on the cancelled context.
Fixed with `Worker.afterTask` on a context that outlives the stop (bounded at 10 s); fenced by
`TestWorker_AStoppingWorkerStillReleasesWhatItsLastTaskLeftWaiting` and
`TestBuildGoal_AStoppedWorkerDoesNotReportTheDatabaseUnavailable`; drill "a stopping worker's bookkeeping runs on the
cancelled context" went red, and "a finished task releases nothing" (re-anchored) still goes red.

## 3. The viewport with the pane hidden (#93)

**Setup.** `forged` built from `viewport/subtree-loading` (4268a7e), the model pointed at an unused local port (no
conversation), the kernel on. A local account `viewport-hidden-pane@forge.local`; the car from
`scripts/viewport-car.js carDocument(30000)` stored with #93's `store.go` (30,023 occurrences, 20 definitions). The
page `/workbench?project=…` in the Browser pane; [`measure-hidden.js`](harness/viewport/measure-hidden.js) injected
(the Studio is caught by wrapping `Forge3D.Studio.prototype.draw`). Frames: `requestAnimationFrame` intervals over 120
callbacks; synced draw: 5 warm-up, then 60 `draw()` + 1-px `readPixels`, orbiting 0.05 rad each.

**What "hidden" is in this browser.** Throughout, the desktop app reported *"The Browser pane is currently hidden"* (it
was not on screen). A document's `visibilityState` did **not** follow that, nor the tab being in front: it was fixed
when the document loaded — `visible` for a page loaded in the front tab, `hidden` for one loaded behind another tab —
and never changed afterwards. Animation frames did follow it: they stopped whenever the tab was not in front, and a
`hidden` document got none even when brought to the front. No `visibilitychange` was ever seen.

| condition | `visibilityState` | rAF | 100 ms interval (3 s) | synced draw median / p95 | loader |
|---|---|---|---|---|---|
| loaded in the front tab (pane off screen) | visible | **13.9 / 14.0 ms**, idle and drawing | 30 / 30 | **7.0 / 9.0 ms** (first view) | 12 loaded, 0 pending, 126 boxes |
| the same page, another tab in front | visible | **0 callbacks in 4 s** | 30 / 30 | 7.0 / 9.4 ms | unchanged |
| reloaded behind another tab (run 1) | hidden | 0 in 3 s | — | — | 12 requested at 10.5 s, done by 16.8 s; 12 loaded, 0 pending, 0 failed, 126 boxes, 40 batches |
| reloaded behind another tab (run 2) | hidden | — | max gap 113 ms | — | 12 requested at 0.65 s, done by 6.6 s |
| run 1, then `requestSubtree('seam')` while hidden | hidden | 0 in 2 s | — | — | seam (25,326 parts, Go tessellator) in **295 ms** (170 ms network, 3.47 MB); 13 loaded, 0 boxes |
| run 1 brought to the front, pane still off screen | hidden | 0 in 14 s | — | **14.1 / 18.7 ms** (all 30,023) | unchanged; context not lost |

For comparison, #93 (pane visible): lazy first view 7.0 / 7.8 ms, eager all 30,023 14.1 / 27.7 ms, rAF 13.9 ms — the
same as the first and last rows.

**What a person sees.** A workbench opened in a pane nobody is looking at loads its whole first view anyway, and so
does a subtree asked for while it is hidden: the loader is driven by `fetch` and draws on arrival, and nothing in it
waits for a frame or a timer. The first view took 0.4–16.4 s cold visible and 0.65–16.8 s hidden. When the pane is
shown the model is there to draw. A screenshot of the hidden front tab showed the car with the provenance banner
listing each subtree "built by the CAD kernel". Interaction cost is unchanged by hiding (7.0 ms first view, 14.1 ms with
the seams in).

**Not established.**

- **The moment of being shown.** A hidden-to-visible transition could not be produced (no tool shows the Browser pane,
  and bringing the tab forward left the document hidden), so the first frame a person sees on opening the pane, and
  mouse interaction straight after, were not exercised. The viewport code does not listen for `visibilitychange`, and
  every `draw()` made while hidden succeeded, so nothing is waiting to be replayed — but this was not watched.
- **Run 1's 10 s before the first request.** The stored-variant list had arrived at 179 ms and no other request ran
  until 10.5 s; run 2 started at 0.65 s with the main thread never blocked for more than 113 ms. Not reproduced, not
  explained; the machine was shared with other agents' kernel runs.
- **Frames with the pane on screen.** Every frame number here was taken with the pane off screen; the first row matches
  #93's visible numbers, which suggests the pane's own visibility does not matter to a front-tab document.

## Conditions

Windows 11, Chrome 152.0.7977.76 in the Claude desktop Browser pane (Intel UHD, WebGL2), page 799 × 541 CSS px, canvas
998 × 676. Go worker, forged and forgectl built from the branches above with `GOWORK=off`. Other agents' kernel
measurements and a live car run shared the machine and the database. One run of each; model output is sampled.

## Method

```bash
# forged and a worker (the key comes from jarvis-a4/.env inside env.sh and is never printed)
bash harness/forged.sh &                                  # 127.0.0.1:18120
node harness/proxy.js 18121 18120 signin.json &          # the pane's origin
python harness/signup.py http://127.0.0.1:18120 livegoal-exercise@forge.local "…" .
LLM_URL=http://127.0.0.1:18122/compatible-mode/v1 bash harness/forged.sh &   # after node harness/rec-proxy.js 18122 rec
bash harness/worker.sh 1 &                                # ctrlrun + forge-worker, through rec-proxy
bash harness/watch.sh &                                   # progress and the 88k watchdog
# the stop drill, zero live tokens
HOLD_STEP=2 HOLD_MS=45000 node harness/stub-model.js 18123 stub-model.log &
bash harness/worker-stub.sh 1 &
STOP_AFTER=10 RESTART_AFTER=5 WORKER_SCRIPT=worker-stub.sh bash harness/stop-resume.sh <goal id>
# the viewport
bash harness/viewport/vp-forged.sh &                      # 127.0.0.1:18130, no model
go run docs/spikes/2026-09-15-subtree-loading/store.go -user <usr_…> -design car30k.json   # on viewport/subtree-loading
```

A stray first watchdog kept writing to `data/progress.log` for a minute after it was replaced (lines with `conv=`),
which is why that file interleaves two formats.
