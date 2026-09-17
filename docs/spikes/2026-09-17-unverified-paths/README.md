# The paths nobody had run: a long build over HTTP, SIGTERM mid-step, approvals, access, and off-node STEP in the worker's pod

**Date:** 2026-09-17 · **Branch:** `goals/unverified-paths` (from `main` at 81c6263) · **Live model tokens:** 0 (a stand-in served every call)

Closes the "not verified" / "not exercised" notes left by #90 (HTTP endpoint live, Linux memory, graceful stop live),
#99 (exports between 4,200 and 90,000, kernel memory after an export), #104 (a live mid-step stop), #119 (worker-stop,
approval and 401/403 paths, a long build) and the stop-time items of #108/#109/#117, using the real `forged` and
`forge-worker` binaries.

## Setup

- `forged`, `forge-worker`, `forgectl` cross-compiled from this branch (`GOOS=linux GOARCH=amd64 CGO_ENABLED=0`), first
  at `main` (81c6263, "before") and again with the fixes below ("after"); run in the `forge-linux-test` image (Debian 12,
  Python 3.13.15, build123d 0.11.1). **amd64 under Docker Desktop / WSL2, not arm64**, and not the production image.
- `forge-worker` in `deploy/k8s/31-worker.yaml`'s shape: `--cpus 1 --memory 2g --memory-swap 2g --user 10001`,
  `FORGE_CAD_POOL=1`, `FORGE_WORKER_CONCURRENCY=1`, lease defaults (2 min lease, 20 s heartbeat). `forged` in
  `30-forged.yaml`'s: 1 CPU, 1 GiB. Both with a kernel.
- Postgres: local `forge-pg`, schema `forge_unverified`, migrated with `forgectl migrate`. Blob store: local MinIO
  (`forge-minio`), bucket `forge-unverified-exports`. No AWS.
- Model: [`harness/standin/main.go`](harness/standin/main.go), an OpenAI-compatible stand-in scripted by knobs in the
  goal statement (steps, size, a delay per step, a held first reply, a slow look, an ordinary two-task plan with an r2
  task). It logs every call with the tokens it reports and whether the reply was delivered or the caller hung up
  ([`data/standin.jsonl`](data/standin.jsonl)), so a goal's `tokens_spent` can be reconciled against what was served.
- Principals: [`harness/principals/main.go`](harness/principals/main.go) makes an owner, a viewer of the owner's project
  and a stranger through the repository's own constructors (#119's mintsession, widened), and stores the barrel designs
  with `geometry.Service.Save`. No password is typed or known; the session tokens stay in the scratchpad.
- Memory: a sampler container sharing the worker's PID namespace and the host cgroup namespace reads every
  `forge-worker` / `python` VmRSS and VmHWM and the cgroup's `memory.current` every 250 ms; `memory.peak`,
  `memory.events` (`oom_kill`) and `cpu.stat` at the end of each export (#125's method).
- Driver: [`harness/uv.py`](harness/uv.py), over HTTP as a client would, polling once a second. Every scenario appends
  to [`data/results.jsonl`](data/results.jsonl); [`data/run.log`](data/run.log) is the running log.
- The laptop was shared with other agents: host CPU averaged 42% over the session
  ([`data/hostcpu.txt`](data/hostcpu.txt)), 59% during the first long build, 22% during the second, ~60% during the
  exports. Timings here are not comparable across runs; counts, tokens and memory are.

## 1. A long build goal over HTTP, end to end

`POST /v1/goals {"build": true, "project_id": …}` → 201 (0.14 s, 10 tasks) → `POST /v1/goals/{id}/start` → 200 → one
`forge-worker` builds all 10 steps, each step's model call taking 12 s.

| | before (tag `long1`) | after (tag `long2`) |
|---|---:|---:|
| goal | succeeded | succeeded |
| wall | 125.8 s | 128.9 s |
| versions kept | 10 | 10 |
| tokens: served by the stand-in / goal `tokens_spent` | 19,400 / 19,400 | 19,400 / 19,400 |
| **longest time a polling client saw nothing change** | **13.08 s** | **5.34 s** |
| gaps over 10 s | 10 of 12 | 0 of 35 |

**🔴 NFR-02 did not hold** ("long jobs report progress at least every 10 s"): nothing on the goal, its tasks or its
timeline changes between a step's start and its end, and the only write in between, the lease heartbeat, is every
20 s and was not exposed. **Fixed:** the heartbeat now runs at least every `agent.AliveEvery` (5 s) and a held task's
`last_seen_at` shows the stamp ([bugfix](../../bugfix/2026-09-17-a-long-build-step-showed-no-progress-for-its-whole-length.md)).

## 2. SIGTERM to a real forge-worker mid-step (Linux)

`docker kill --signal=TERM` to the worker container, whose PID 1 is `forge-worker` (as in the pod). Handed back =
the task row `ready` with no lease owner, sampled through `psql` (~0.35 s a sample).

| | stop inside a held model call (`stop1` before, `stop4` after) | stop after the step's reply was paid for, during its look (`stop3`) |
|---|---|---|
| handed back | first sample, 0.39 s / 0.36 s after the signal | first sample, 0.42 s |
| worker exit | 0; `stopping` → `stopped` in 10 ms | 0 |
| `task.handed_back` event | yes | yes |
| attempts on the task | 1 → 0 (the stop is not an attempt) | 1 → 0 |
| steps before it rebuilt | no: steps 1, 2 asked once each | no: step 1 asked once |
| the stopped step on restart | asked again; the abandoned call charged nothing | asked again and charged again: its first reply was paid for and not kept |
| served / `tokens_spent` | 8,000 / 8,000 (both runs) | 7,100 / 7,100 |
| goal after a restarted worker | succeeded | succeeded |

Every answered call is charged exactly once; a held call the stop cut off is charged nothing. A step stopped after its
reply was paid for is paid for twice in total, because a stopped worker does not keep a model nobody checked (#109's
rule); recorded here, not changed.

**🔴 Found:** the stopping worker logged `forge.llm.retrying … EXTERNAL_UNAVAILABLE … cannot reach the model endpoint`
for the call it had itself cancelled (`stop1`, and for a look in `stop2`). **Fixed** — `stop4`'s log is `stopping`,
`task.handed_back`, `stopped` and nothing else
([bugfix](../../bugfix/2026-09-17-a-stopped-worker-logged-a-retry-of-a-model-it-had-hung-up-on.md);
[`data/worker-stop1-stopped.log`](data/worker-stop1-stopped.log), [`data/worker-stop4-stopped.log`](data/worker-stop4-stopped.log)).

The first "kernel" stop (`stop2`) landed on step 3 instead of step 2 because a 4,096-box step builds in under a second;
its events and tokens are in `results.jsonl` (7,100 / 7,100) but the harness sampled the wrong row, so it is not in the
table. Windows' CTRL_BREAK path was run by #104 and not repeated.

## 3. The approval path

An ordinary goal at `risk_tier: r2`, planned by the stand-in as `prepare` (r1) then `publish` (r2, `requires_approval`).

| | approve (`appr1` before, `appr3` after) | reject (`appr2`) |
|---|---|---|
| `approval.requested` | 1.6 s after start, one event, task `awaiting_approval` | 1.1 s |
| listed in `GET /v1/approvals` for the viewer / the stranger | yes / no | yes / no |
| `POST /v1/approvals/{id}` as viewer / stranger / nobody | 403 / 404 / 401, still `pending` | 403 / 404 / 401 |
| as owner, then again | 200, then 409 | 200, then 409 |
| goal | `succeeded` (verifier ran, `verification.passed`) | `failed`: "rejected by owner-…: …", 1 of 2 failed |
| served / `tokens_spent` | **2,150 / 1,200** before; 2,150 / 2,150 after | **1,050 / 600** before |

**🔴 Found:** an ordinary goal's planning call and every verifier call were never charged to the goal: 450 tokens of
planning and 500 of verification missing from `tokens_spent` and invisible to the goal's ceiling. **Fixed**
([bugfix](../../bugfix/2026-09-17-a-goals-planner-and-verifier-calls-were-never-charged.md)).

A build step never needs approval (build steps are planned r1), so the gate was exercised on an ordinary goal.

## 4. 401 / 403 / 404 on every goal, approval and export route

The fence (`internal/httpapi/goal_export_access_fence_test.go`) parses `router.go` for every goal, approval and export
route and fails on one without a row in its table, or registered without `authed(...)`; then it asks every row as
nobody, a stranger, a viewer and the owner through `NewRouter` with real bearer sessions on Postgres, and checks that a
refused write left the project unchanged. The same matrix live over HTTP (after):

| route | nobody | stranger | viewer |
|---|---:|---:|---:|
| `GET /v1/goals` | 401 | 200, nothing of the project | 200 |
| `POST /v1/goals` | 401 | 404 | 403 |
| `POST /v1/goals/{id}/plan` | 401 | 404 | 403 |
| `POST /v1/goals/{id}/start` | 401 | 404 | 403 |
| `GET /v1/goals/{id}`, `/timeline` | 401 | 404 | 200 |
| `GET /v1/approvals` | 401 | 200, nothing of the project | 200 |
| `POST /v1/approvals/{id}` | 401 | 404 | 403 |
| `GET /v1/geometry/{id}/export?format=step` | 401 | 404 | 200 |
| `GET /v1/geometry/{id}/export/label?format=step` | 401 | 404 | **501 before, 200 after** |
| `POST /v1/geometry/{id}/exports` | 401 | 404 | 202 (200 when it already exists) |
| `GET /v1/geometry/exports/{id}`, `/file` | 401 | 404 | 200 |

**🔴 Found:** in a deployment with a kernel, the STEP label answered 501 "no CAD kernel configured" while `/formats`
said STEP was available and the download worked. The workbench fetches the label before it shows a download link, so
its Export STEP button never offered one. **Fixed** server-side
([bugfix](../../bugfix/2026-09-17-the-step-label-said-there-was-no-kernel-where-there-was-one.md)).

## 5 and 6. Off-node STEP exports between 4,200 and 90,000, in the worker's pod

Airframe barrels from #125's generator ([`harness/barrel/main.go`](harness/barrel/main.go)); 89,744 is the largest
under `geometry.MaxExportJobParts` (90,000; #89's 90,880 is refused). Requested by the **viewer** over HTTP, run by the
worker, stored in MinIO, downloaded by the viewer through `forged`. One fresh worker process for the first three,
then three more 89,744 exports in the same process, then a 4,096-occurrence build (`maxDrawnParts`, the most a step
builds). 10 s settle before and after each. Per second: [`data/worker-memory-exports-1s.csv`](data/worker-memory-exports-1s.csv).

| export | queued → running → done | file | download | kernel RSS before → peak → after (MiB) | worker (Go) RSS peak → after | cgroup `memory.current` peak → after | `memory.peak` | oom_kill |
|---|---|---:|---:|---|---|---|---:|---:|
| 8,192 | 1.2 s → 7.0 s | 5.7 MB | 0.5 s | — (not started) → 532 → 532 | 57 → 42 | 401 → 387 | 402 | 0 |
| 30,400 | 1.1 s → 9.1 s | 21.9 MB | 1.0 s | 532 → 765 → 733 | 171 → 171 | 723 → 719 | 724 | 0 |
| 89,744 | 1.1 s → 18.7 s | 65.4 MB | 4.6 s | 733 → 1,385 → 1,280 | 475 → 475 | 1,575 → 1,571 | 1,576 | 0 |
| 89,744 again | → 19.1 s | 65.5 MB | | 1,280 → 1,441 → 1,341 | 475 → 472 | 1,632 → 1,628 | 1,633 | 0 |
| 89,744 again | → 18.3 s | 65.5 MB | | 1,341 → 1,441 → 1,342 | 472 → 454 | 1,628 → 1,612 | 1,646 | 0 |
| 89,744 again | → 18.3 s | 65.5 MB | | 1,342 → 1,447 → 1,261 | 454 → 353 | 1,612 → 1,430 | 1,653 | 0 |
| then a 4,096 build, 2 steps | 3.2 s | | | 1,261 → 1,261 → 1,173 | 358 → 32 | 1,430 → 1,019 | 1,653 | 0 |

**Does memory come back after an export? The kernel's does not; Go's does, minutes later.** The kernel process keeps
~1.2-1.3 GiB after an 89,744 export (not returned to the OS; why was not investigated), and a later export reuses it:
three repeats plateau at a 1.44 GiB kernel peak, not growing. The worker's Go RSS stayed at ~475 MiB for at least 3
minutes after the first 89,744 export and was down to 32 MiB by the build, about 3 minutes after the last one. Retained is not leaked: the pod's highest reading was the plateau, not a climb.

**Does it fit 31-worker.yaml's 2 GiB? Yes, with 395 MiB (19%) headroom at the worst point measured:** `memory.peak`
1,653 MiB of 2,048, no OOM kill, across a fresh process, four 89,744 exports in a row and a 4,096 build after them.
Against `limits.go`'s arithmetic (kernel 1,321 + Go 554 = 1,875 MiB): the kernel on Linux peaks higher (1,385-1,447 at
89,744), the Go side lower (475), and the two peaks do not coincide, so the pod's peak is lower than the sum.
**No manifest change**, as the rule was: none is needed.

CPU: `throttled_usec` rose 3.0 → 4.8 s over the whole run at the pod's 1 CPU while the host was ~60% busy.

## Fixes on this branch

| defect | fence(s) | drill(s), all seen red |
|---|---|---|
| build step silent past 10 s (NFR-02) | `TestWorker_ARunningTaskIsStampedAliveWhileItsModelCallRunsWhateverTheLeaseHeartbeat`, `TestAliveEvery_LeavesAClientPollingAtItAFreshStampInsideTenSeconds`, `TestTaskDTO_AHeldTaskSaysWhenItsWorkerWasLastSeenAndOtherTasksDoNot` | 4 |
| planner and verifier calls uncharged | `TestIntake_AnOrdinaryGoalsPlanningIsChargedToTheGoal`, `TestWorker_TheVerifiersCallIsChargedToTheGoal` | 2 |
| cancelled model call logged as a retry | `TestComplete_ACallItsCallerCancelsIsNotRetriedOrBlamedOnTheEndpoint` | 1 |
| STEP label 501 with a kernel | `TestAPI_TheSTEPLabelIsTheKernelsWhereThereIsAKernelAndARefusalWhereThereIsNone` | 2 |
| (new fence) every goal/approval/export route's access | `TestAccessFence_EveryGoalApprovalAndExportRouteIsInTheAccessTable`, `TestAccessFence_GoalApprovalAndExportRoutesAnswerNobodyStrangerViewerAndOwnerAsDecided` | 4 |

## What this does NOT establish

- **arm64 and the production image.** Every container number is amd64 under WSL2 in `forge-linux-test`, the same
  build123d 0.11.1 the pinned requirements name, not the image `deploy/Dockerfile` builds. An arm64 OCCT heap may differ.
- **Real S3** (MinIO only), **a live model** (the stand-in answers in milliseconds unless told to wait; its documents
  are rows of boxes, far cheaper to build than a car), **the real pods** (container limits, not Kubernetes).
- **forged's memory** under these exports' downloads (65 MB through a 1 GiB pod) was not sampled.
- **The workbench** does not show `last_seen_at` yet (the card is other work); the STEP label fix was checked over HTTP,
  not in a browser.
- Two exports or a build and an export at once: the worker runs one task at a time (`FORGE_WORKER_CONCURRENCY=1`, one
  export per worker), which is what makes the plateau above the peak.

## Reproduce

```bash
H=docs/spikes/2026-09-17-unverified-paths/harness
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $BIN/forged ./cmd/forged   # and forge-worker, forgectl
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $BIN/standin $H/standin/main.go
go run $H/barrel/main.go -out $DOCS
python $H/uv.py up
FORGE_DATABASE_URL='postgres://…/forge?sslmode=disable&search_path=forge_unverified' \
  go run $H/principals/main.go -out $SCRATCH/principals.json -design $DOCS/barrel-4096.json -design …
python $H/uv.py build --tag long1 --steps 10 --parts 640 --stepdelay 12000
python $H/uv.py stop-held --tag stop1 ; python $H/uv.py stop-kernel --tag stop3
python $H/uv.py approval --tag appr1 --decision approve ; python $H/uv.py approval --tag appr2 --decision reject
python $H/uv.py authz
python $H/uv.py worker && python $H/uv.py exports --settle 10
python $H/summarize_sampler.py $SCRATCH/data/sampler-<worker>.txt data/worker-memory-exports-1s.csv
```
