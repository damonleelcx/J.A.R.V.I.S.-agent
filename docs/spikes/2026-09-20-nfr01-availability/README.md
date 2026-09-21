# NFR-01 — availability: the measurement method, and a ruling on room mute

`docs/prd.md:103` — **NFR-01** "99.9% monthly; mute/stop/end remain available
during cloud degradation".

Two clauses, two different kinds of claim. The second is a property of the code
and is now fenced. The first is a property of the deployment, is **not measured
today**, and this document says exactly what would have to exist before anybody
may write the number down.

---

## 1. The second clause: mute / stop / end during degradation

### What the code actually does

The workbench's single-user controls contain **no network call at all**. This is
structural, not incidental — the whole of `internal/httpapi/assets/voice.js`
reaches the network from exactly two places, `_upload` (POST `/v1/transcribe`)
and `_speakRemote` (POST `/v1/speech`), and neither is reachable from a control:

| Control | `voice.js` | Requests made | Effect with the cloud gone |
|---|---|---|---|
| `toggleMute` | flips `muted`, calls the two stops, publishes state | none | mute applies immediately |
| `stopSpeaking` | cancels synthesis, pauses the element, **aborts** the in-flight `/v1/speech` | none new | she stops |
| `cancelListening` | `_finishRecording(true)`, returns before `_upload` | none | the hold is discarded |
| `stopListening` | `_finishRecording(false)` → `_upload` | **one**, to `/v1/transcribe` | the hold is delivered, or the failure is named |

The last row is the boundary and it is deliberate. Ending a hold is a
*delivery*: what was already said is still owed to the conversation. Cancelling
discards, so nothing is owed. The two must not converge — a "cancel" that
needed the server would be the single control guaranteed to fail while a
microphone is open.

### The one nuance worth stating plainly

`stopSpeaking` calls `AbortController.abort()` on a `/v1/speech` request that is
already in flight. **Aborting a request is not making one.** No bytes go out,
nothing is awaited, and the abort takes effect whether or not the vendor ever
answers — which is precisely what keeps the stop inside AUD-02's 250 ms budget
while a speech vendor hangs. A fence on this function must therefore count
**new** requests, not network activity; counting activity would forbid the
mechanism that makes the guarantee true.

Leaving the request in flight is the real failure mode: the audio arrives late
and she starts speaking *after* the person interrupted her.

### Fences

`internal/httpapi/nfr01_degradation_test.go`, all run against the real assets in
node with a browser world whose `fetch` is broken four ways — **slow** (accepted,
never answered), **erroring** (rejected), **refusing** (500) and
**disconnecting** (taken, then dropped mid-flight):

- `TestNFR01_MuteAndStopTakeEffectWithNoNetworkAtAll`
- `TestNFR01_MuteAndStopStillWorkWhileTheEndpointIsSlowErroringOrDisconnecting`
- `TestNFR01_ARefusedRoomMuteSaysItIsNotInForceRatherThanShowingItApplied`

Each asserts both halves: the request count does not grow, **and** the control
takes effect (mute flips, speech stops, the recogniser is released). Either half
alone would pass for a button that does nothing.

---

## 2. Ruling — the room-mute conflict (issue 16, Gap 1)

### The conflict

Room mute is **server-authoritative on purpose**. `media.SFU.forward` drops the
packets, and `internal/httpapi/rooms_media.go` says why: *a mute that only stops
the browser sending is a picture of a mute*. `Room.setState` in
`assets/room.js` disables the local track first — a latency optimisation — then
POSTs the control that actually enforces it.

That is in direct tension with "remain available during cloud degradation". If
the server cannot be reached, the room mute cannot be applied.

### The ruling

**NFR-01's guarantee is honoured by the single-user controls. The multi-party
room controls are SCOPED OUT of it. This is scoping, not a defect, and nothing
is to be changed to "fix" it.**

Three reasons, in order of weight:

1. **The alternative is a lie.** A client that reported "muted" on the strength
   of its local track alone would be telling a person that a room full of people
   cannot hear them, while having no way to know whether that is true — and it
   would be saying so in exactly the state where the client's own view of the
   world is least reliable. A privacy failure is a worse thing to be down than a
   control is.
2. **The failure modes are not symmetric.** A server-enforced mute is
   *unavailable* during an outage: visible, recoverable, and it says so. A
   client-applied mute would be *unreliable*, and would look identical whether it
   worked or not.
3. **During real degradation there is nobody to be overheard by.** If the cloud
   is down far enough that `/v1/rooms/{id}/media/state` cannot be reached, the
   SFU is not forwarding that audio either. The unavailable mute is protecting a
   conversation no one is receiving.

### What IS owed in that state

Not a working mute — the truth. `room.js` `setState`'s `.catch` tells the person
the server did not accept the change, so the button is never shown as applied
when it is not. `setTranscribing` (AUD-07's *end-recording*) rejects rather than
resolving, so a caller cannot mistake an unreachable server for a stopped
transcription. Both are now fenced; that honesty, not the mute, is the
NFR-01-adjacent invariant on the room path.

### Recorded where a reader will meet it

- `internal/httpapi/assets/room.js`, above `Room.prototype.setState`.
- `internal/httpapi/rooms_media.go`, above `SetMediaState`.
- Here, in full.

### What a future amendment to NFR-01 would say

The requirement is currently ambiguous in a way that invites this argument
again. An amendment would replace the second clause with something like:

> **NFR-01** 99.9% monthly. The single-user audio controls — mute,
> stop-speaking and cancel-hold — are local and remain available during cloud
> degradation; they make no request and are fenced as making none. The
> multi-party room controls — room mute, pause and end-recording — are enforced
> at the server and are therefore unavailable during an outage of it; in that
> state the client must report them as **not in force** rather than as applied.

That wording changes no code. It records the decision in the requirement rather
than leaving it in a comment, which is where the next reader will look.

---

## 3. The first clause: how 99.9% monthly would be measured

### 3.1 There is no number today. Nothing computes one.

Stated plainly because the requirement reads as though a figure exists:

- There is **no metrics or uptime aggregation anywhere in this repository** — no
  Prometheus, no `promhttp`, no `/metrics`, no `expvar`, no scrape config.
- `internal/httpapi/telemetry.go` (NFR-05) is the only measurement layer, and it
  is per-user and per-turn **by design**. It records what a person's turn cost
  and how long it took. It is not deployment-wide and must not be repurposed:
  a tenant-scoped record cannot answer "was the service up".
- The k8s probes (`deploy/k8s/30-forged.yaml`) *make decisions* on
  `/healthz` and `/readyz` but keep **no history**. Kubelet probe outcomes
  surface as Events, which age out.
- `deploy/verify.sh` check 6 curls both endpoints over public TLS, but it is a
  one-shot post-deploy check a person runs. It proves the endpoints answered
  once; it measures nothing over time.

Any availability figure quoted for FORGE today is an assertion, not a
measurement.

### 3.2 What is measured: `/readyz`, not `/healthz`

**A sample is *available* when `GET https://forge.heros-agent.space/readyz`
answers `200` within the probe timeout.** Everything else is a failed sample.

Why `/readyz` and not `/healthz`, given that both are 200 on a working instance:

- **`/healthz` answers a different question.** `HealthHandlers.Live` reports only
  that the process is running, and *deliberately does not touch the database* —
  if it did, a database outage would make every orchestrator restart every
  instance and turn a recoverable dependency failure into a restart storm. So
  `/healthz` returns 200 from a process that can serve nothing but static pages.
  Measuring it would report near-100% through a total database outage.
- **`/readyz` answers "can this instance serve traffic".** `HealthHandlers.Ready`
  runs `db.HealthCheck` with a 3 s budget and returns **503 +
  `"status":"unavailable"`** when it fails. That 503 is the same signal the load
  balancer acts on, so it is the closest available proxy for *a request from a
  person would have been served*.
- **A failing `/readyz` is a real outage, not a caveat.** It means the database
  is unreachable, and every project, conversation, room and geometry read in
  FORGE goes through it. The only things that still work in that state are the
  local controls in §1 — which is the whole reason the two clauses of NFR-01
  belong in one requirement.

The separation between the two probes is load-bearing for the measurement, not
just for operations, and is fenced:
`TestNFR01_LivenessAnswersWithoutTouchingTheDatabase` and
`TestNFR01_ReadinessReportsTheDatabaseAndRefusesTrafficWhenItIsUnreachable`. A
refactor folding them together — they look almost identical — would make a
database outage read as the process being dead, and the figure would then be
computed over a fleet its own monitoring was restarting.

### 3.3 How: the probe, the interval, the sample, the arithmetic

| Parameter | Value | Why this value |
|---|---|---|
| Target | `https://forge.heros-agent.space/readyz` | The public name, over TLS, through the ingress — so DNS, certificate and ingress failures are counted. An in-cluster check would miss all three. |
| Method | `GET`, no auth | `/readyz` is unauthenticated; a probe that needed a session would measure the session store too. |
| Interval `I` | **30 s** | The resolution of the figure. 30 s makes a one-minute outage two failed samples rather than a coin toss, and costs 86,400 requests a month, which is nothing. |
| Timeout `T` | **5 s** | Must exceed `db.HealthCheck`'s own 3 s budget, or a slow-but-working database scores as down. 5 s leaves headroom and still fails well inside a person's patience. |
| Redirects | not followed | A redirect is not a ready service. |

**A sample FAILS on any of:** DNS failure, TCP refused or reset, TLS handshake
failure, no response within `T`, or any HTTP status other than 200 —
**explicitly including 503**. A 503 from `/readyz` is the endpoint working
correctly and reporting that the service cannot serve; for this measurement the
service is down.

**A sample is MISSING when the checker itself did not run.** Missing samples are
neither successes nor failures: they leave the denominator and their count is
published beside the figure. **A month with more than 1% missing samples yields
no figure at all** — a checker that was down half the month cannot certify
anything, and the failure mode to guard against is a checker that dies with its
subject and records 100%.

**Arithmetic.** Over a calendar month, with `S` successful and `F` failed
samples:

```
availability %  = 100 × S / (S + F)          reported to two decimals
downtime (min)  = F × I / 60                  = F / 2 for I = 30 s
```

**Outages, for the incident list only.** An *outage* is a maximal run of **two or
more** consecutive failed samples. It begins at the first failed sample's
timestamp, ends at the first subsequent successful one, and its duration is
`(failed samples in the run) × I`. A lone failed sample is a *blip*: recorded,
named in the report, not promoted to an incident.

‼️ **The percentage is computed from samples, never from grouped outages.**
Grouping with any hysteresis rule ("ignore outages under two minutes") lets a
service that flaps every 90 seconds all month score 100% while being unusable.
Blips count against the budget; they merely do not get a name.

### 3.4 Where the number would come from

Nothing runs this today. The smallest honest thing that would:

**An external HTTP checker, off this node.** It must not run on
`i-05f4712279b04fac5`, and it must not run in the `forge` namespace: a checker
that shares fate with its subject stops sampling exactly when the samples
matter. Two viable shapes, in order of how little work they are:

1. **A hosted uptime check** (any of the commodity ones) pointed at
   `/readyz` at 30 s with a 5 s timeout, keyed on status 200. Its monthly report
   *is* the number. Zero code, and it independently covers DNS, TLS and the
   security group. This is the recommendation.
2. **A cron'd `curl` on a machine outside the node**, appending one line per
   sample — `timestamp,http_code,total_time_ms` — to a file, and from there to
   the existing S3 blob bucket. It is the same request `deploy/verify.sh` check 6
   already makes:

   ```sh
   curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
     https://forge.heros-agent.space/readyz
   ```

   The monthly figure is then `awk` over the month's lines. Cheap, in-repo, and
   it keeps the raw samples, which a hosted service generally does not.

Either way the samples must be **durable** — the figure is a monthly claim and
has to survive the thing it measures being rebuilt.

Anything larger (a `/metrics` endpoint, a Prometheus, an SLO recording rule) is
a bigger change than the requirement needs and would introduce a
deployment-wide measurement layer where NFR-05 deliberately has none. Do not
reach for it first.

### 3.5 What 99.9% monthly actually allows

| Window | Total minutes | 0.1% error budget |
|---|---:|---:|
| 30-day month | 43,200 | **43.2 min** (43 min 12 s) |
| 31-day month | 44,640 | 44.64 min (44 min 38 s) |
| 28-day month | 40,320 | 40.32 min (40 min 19 s) |
| One week | 10,080 | 10.08 min |
| One day | 1,440 | 1.44 min (86.4 s) |

At `I = 30 s`, a 30-day month is **86,400 samples** and the budget is **86 failed
samples** (86.4, floored — the budget is the conservative direction).

**What that budget is already committed to.** `deploy/k8s/30-forged.yaml` runs
`forged` with `replicas: 1` and `strategy: Recreate` on `hostNetwork`. Recreate
means the old pod is torn down **before** the new one starts, so *every deploy is
a planned outage* — image pull, process start, first successful `/readyz` — and
the startup probe alone allows up to 120 s for it (`periodSeconds: 3`,
`failureThreshold: 40`). A 2-minute deploy spends **4.6%** of the month's budget;
ten deploys in a month spend **46%** of it before anything has gone wrong. A
single 10-minute incident spends **23%**.

That arithmetic is the honest reading of NFR-01 on today's topology: 99.9%
monthly and a single-replica Recreate deployment are in tension, and the budget
is mostly spent on deploys. Either deploys become non-disruptive (a second
replica and a rolling update, which `hostNetwork` currently forbids), or the
requirement's window needs to exclude announced maintenance and say so. This is
a deployment decision, not a code change, and it is out of scope here — but it
should not be discovered by missing the target.

### 3.6 What this method cannot see

Every item below is a state in which `/readyz` answers 200 and a person cannot
do their work. The measured figure is therefore an **upper bound** on what people
experienced, and must be reported as one.

- 🔴 **The model endpoint.** `/readyz` checks the database and nothing else.
  A deployment whose LLM vendor is unreachable, rate-limiting, or whose
  `FORGE_LLM_API_KEY` has expired answers `ready` while every conversation fails.
  This is the largest blind spot and it is deliberate: a probe that spent a model
  call every 30 s would cost money, consume rate limit, and make the health of
  the service depend on a vendor's billing.
- 🔴 **The CAD kernel and the worker.** `/readyz` is served by `forged`.
  `forge-worker`, its ReadWriteOnce PVC and its `build123d` venv are not checked,
  and neither is queue depth. Geometry can be dead with the figure at 100%.
  (`deploy/verify.sh` check 7 proves the kernel builds a solid — but only at
  deploy time.)
- 🔴 **The media plane.** The SFU binds a WebRTC UDP range on `hostNetwork`.
  Rooms can be entirely broken — which is where §2's multi-party controls live —
  with `/readyz` green.
- **The blob store.** S3 reachability, the IMDSv2 hop limit, bucket policy. STEP
  exports fail; the probe does not notice.
- **The mail relay.** Confirmations and resets never deliver, silently. Not
  probed.
- **Per-user failures.** Authorisation faults, a single tenant's data, one
  project that will not load. The probe is unauthenticated and sees none of it.
- **Slow but answering.** The definition is binary. `/readyz` reports
  `latency_ms`, and a checker that kept it could add a latency objective later —
  but availability as defined here scores a 4-second page as available. NFR-02's
  300 ms budget is a separate requirement with a separate measurement.
- **Partial fleet outage.** Moot at `replicas: 1`, but with more than one
  instance an external check measures the *service*, not the instances, and one
  dead replica behind a healthy load balancer is invisible.

A monthly report should carry the figure, the missing-sample count, the incident
list, **and this section**, so that nobody reads 99.95% as "the product worked".

---

## Summary

| Question | Answer |
|---|---|
| Do mute/stop/end survive cloud degradation? | Single-user: **yes**, structurally — no request exists to fail. Fenced four ways. |
| Does room mute? | **No, and by design.** Scoped out; what is owed instead is an honest "not in force". |
| Is 99.9% monthly measured? | **No. Nothing measures it.** §3.4 names the smallest thing that would. |
| What does 99.9% allow? | **43.2 min** per 30-day month — much of it already committed to single-replica `Recreate` deploys. |
| What would the number miss? | The model endpoint, the CAD kernel, the media plane, blobs, mail, and every per-user failure. |
