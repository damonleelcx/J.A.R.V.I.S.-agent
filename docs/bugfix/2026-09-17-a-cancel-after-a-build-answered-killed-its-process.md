# A cancel after a kernel build answered could kill the process that answered it

**Status:** fixed on `fix/kernel-timeout-race`.
**Found:** 2026-09-16/17. Two kernel fences failed now and then in CI's `check` job (`go test -count=1 -race ./...`) and
passed when the same commit ran again. They failed in opposite directions.
**Severity:** low in production, and a real product race. It never gave wrong geometry. It never retried a build that
really ran out of time. Its cost was a wasted process start for some later build, the crash retry that build needed,
and, in a second defect fixed here, the wrong code when a caller's deadline ended while a process was starting.
**Owner:** the CAD kernel pool (`internal/domain/cad`: `sidecar.roundTrip` in `sidecar_process.go`).

## Summary

`roundTrip` bounds a build with a goroutine that kills the slot's process when the limit or the caller's context runs
out. PR 73 made that goroutine record a `lateError` before it kills. That ordering was right, but two things around it
were wrong:

1. **The goroutine could outlive the round trip, and could kill a round trip that had already ended.** `roundTrip`
   returned through `defer close(done)` without waiting for the goroutine. When the caller cancelled its context right
   after a build answered (`defer cancel()`), the goroutine could reach its `select` with **both** `done` and
   `ctx.Done()` ready. `select` picks one at random, so about half those times it killed the process of a build that
   had **succeeded**. The same thing happened when the deadline ended just as the reply arrived. The slot kept the dead
   process marked `started`. The next build on that slot got EOF, took it for a crash, and used its one retry to
   replace the process. `s.kill` reads `s.cmd` at the moment it runs, so a goroutine that ran late enough could even
   kill the *next* holder's process in the middle of its build.
2. **A caller whose deadline ended while its process was starting was treated as a crash.** `start` returned a plain
   `ctx.Err()`. `BuildDocument` retried it, started a second process for a caller that was already gone, and refused
   the build as `CONNECTOR_UNAVAILABLE` ("restarting it did not help") when it should have been `CAD_KERNEL_TIMEOUT`.

Now the holder and the goroutine each try to claim the end of the round trip with one compare-and-swap. Only a goroutine
that wins the claim kills the process, and `roundTrip` waits for the goroutine to exit before it returns. A caller whose
context has already ended, or ends during `start`, gets its `lateError`.

## Symptom

CI, `check` job, fake kernel (`cadtest`). Each failure passed when the same commit ran again:

```
TestKernel_AProcessThatDiesMidBuildIsStillRetriedOnce
  timeout_internal_test.go:176: a process that died once was not retried: cad.Kernel.BuildDocument: CONNECTOR_UNAVAILABLE:
  ... (the CAD kernel did not answer, and restarting it did not help): reading from the kernel: EOF      (0.03 s)

TestKernel_ABuildThatRunsOutOfTimeIsNotRetriedAndSaysSo/the_caller's_deadline
  timeout_internal_test.go:108: 2 kernel processes started for one build that ran out of time, want 1: it was retried
```

These line numbers are from 2955885 ("Put the kernel's build timeout on each pool slot"). At that commit `warm()` in
`timeout_internal_test.go` was itself a `BuildDocument` with `defer cancel()`.

Reproduced in Linux (`golang:1.26`, `CGO_ENABLED=1`, from a clean `git archive`), with the two fences looped as
`-race -count=N -cpu 1,2,8` and several containers running at once:

| tree | top-level runs | failed | rate |
|---|---|---|---|
| 2955885 (the CI commit), 3 containers × 200 | 3,600 | 11 | 0.31 % |
| 2955885 instrumented, 1 × 200 | 1,200 | 2 | 0.17 % |
| `main` (ebb4ada) as it is, 4 containers × 500 | 12,000 | **0** | — |
| `main` with the CI-era `warm()` put back, 1 × 300 | 1,800 | 22 | 1.2 % |

Both signatures appeared at every tree that failed: "2 kernel processes started" (in either subtest) and "a process that
died once was not retried … EOF".

`main` does not reproduce because 160b28c changed `warm()` to start processes by hand, so these two fences stopped doing a
build before the one they check. **The fences stopped showing the race. The race itself was still there.** A probe on
`main` that cancelled while a round trip was in flight found the process killed after a *successful* round trip 132 times
in 200.

## Impact

**Could a real user have had a timed-out build retried, and waited twice as long?** No. A build that really ran out of
time was always seen as one. The goroutine stores the `lateError` before it kills, and the read cannot fail until the
kill has happened, so `stopped.Load()` always found it. The "2 processes started" failure came from the *previous*
build's goroutine killing the process. The timed-out build's first attempt met that dead process and was retried as a
crash. The retry then timed out properly.

**Could a real user have had a crashed build not retried?** Yes, in principle, and rarely:

- **After a stray kill, the next build on that slot pays for it.** Its first attempt meets the dead process and gets EOF.
  Its one retry starts a new process, which costs one kernel start (about 2.5 s of build123d import) on top of the build,
  and a `forge.cad.restarted` log line that says `EOF`. If the design then crashes that fresh process once, nothing is
  left to retry it with, and the caller gets 501 `CONNECTOR_UNAVAILABLE` ("restarting it did not help").
- **What causes a stray kill in production.** The caller's context has to end *at the same moment* as the round trip:
  - the request's 30 s deadline, or the export job's context, ends while the reply is being read; or
  - the handler returns and `net/http` cancels `r.Context()` before the goroutine has run at all.

  With real build123d the holder waits on the pipe for milliseconds or more, so the goroutine is almost always already
  parked in its `select` long before that. A parked `select` woken by `close(done)` takes `done`. The second path
  therefore needs a CPU-starved process. The first path needs a build that finishes within microseconds of its
  deadline. Against the fake, which answers in microseconds, the holder often never waits at all, and that is why CI saw
  it.
- **A deadline that ends while a slot's process is starting.** This happens to the first build on each slot and to every
  build after a reset. It is the only realistic way to hit defect 2, and it is not rare for a caller with a short
  deadline. The caller got **501 `CONNECTOR_UNAVAILABLE`, "the CAD kernel did not answer, and restarting it did not
  help"**, when it should have been 504 `CAD_KERNEL_TIMEOUT`. A second process was spawned and killed at once. The
  caller did not wait any longer, because the retry's `start` returned as soon as it saw the finished context.
- **Where to look in logs:** `forge.cad.restarted` with detail `reading from the kernel: EOF` or `writing to the kernel:
  … broken pipe`, on a slot whose previous build succeeded and did not time out. We have not searched production logs
  for it. Nobody reported it.
- **Not affected:** geometry, what a reply contains, other slots (the kill only ever reached `s.cmd` of the goroutine's
  own slot), and the export job's caller-supplied limit, which is passed into the same code.

## Preconditions

- A kernel is configured.
- Defect 1: a caller's context ends together with a round trip, either right after it answered or during the read. Or
  a limit fires as the reply arrives.
- Defect 2: a caller's deadline ends before `roundTrip`, or while `start` waits for the ready banner.

## Root cause

### Defect 1: the deadline goroutine outlived and overrode the round trip

`sidecar_process.go` before the fix:

```go
done := make(chan struct{})
defer close(done)
var stopped atomic.Pointer[lateError]
go func() {
    ...
    select {
    case <-done:
    case <-ctx.Done():
        stopped.Store(&lateError{caller: ctx.Err()})
        s.kill()
    ...
line, err := s.stdout.ReadBytes('\n')
if err != nil {
    if late := stopped.Load(); late != nil { return nil, late }
    return nil, fmt.Errorf("reading from the kernel: %w", err)
}
return &res, nil
```

Nothing stopped the goroutine from killing *after* `ReadBytes` had returned a good line, and nothing made `roundTrip`
wait for it. Here is the interleaving CI hit, confirmed by instrumenting 2955885. The goroutine logs whether `done` was
already closed when it chose `ctx.Done()`:

```
=== RUN   TestKernel_AProcessThatDiesMidBuildIsStillRetriedOnce
TRACE slot=0 ctx-kill pid=1990 roundTripAlreadyReturned=true      <- warm()'s build answered; its defer cancel() fired
TRACE slot=0 read-error-without-lateError pid=1990 err=EOF        <- CrashOnce build, 1st attempt, meets the dead process
TRACE slot=0 read-error-without-lateError pid=1997 err=EOF        <- the retry's fresh process is the FIRST to see CrashOnce
    timeout_internal_test.go:176: a process that died once was not retried: ... reading from the kernel: EOF
```

```
=== RUN   TestKernel_ABuildThatRunsOutOfTimeIsNotRetriedAndSaysSo/the_kernel's_limit
TRACE slot=0 ctx-kill pid=797 roundTripAlreadyReturned=true       <- warm()'s process killed after warm returned
TRACE slot=0 read-error-without-lateError pid=797 err=EOF         <- the slow build's 1st attempt: "crash", retried
    timeout_internal_test.go:108: 2 kernel processes started for one build that ran out of time, want 1: it was retried
```

In the whole instrumented run, the goroutine killed a process after `roundTrip` had returned exactly twice. Each of those
two kills was followed by one of the two failures.

### Defect 2: start's context error was not a lateError

`roundTrip` returned `s.start(ctx)`'s error unchanged. When the context ended while `start` waited for the banner, that
error was `ctx.Err()`. `BuildDocument` gates the retry on `errors.As(err, &late)`, so it retried this error as a crash.
A probe on `main` showed it every time: a cold slot whose caller had 1 ms, 5 ms or 20 ms got `CONNECTOR_UNAVAILABLE` from
the retry path, 3 times out of 3. The same happened 6 times in 20 for an already-expired caller, whenever `acquire`'s
`select` picked the free slot over `ctx.Done()`.

### Hypotheses ruled out

- **`cadtest.CrashOnce` racing on its marker file (harness race):** ruled out. The first process writes `crashed-once`
  before it exits. The kernel reads EOF only after it exits, and `stop()` `Wait`s for it before the replacement starts,
  so the replacement always sees the marker. In the failing trace the first process (pid 1990) never received the
  CrashOnce request at all.
- **A read returning EOF before the goroutine's `Store`, for a real timeout:** ruled out. The kill that makes the read
  fail comes after the `Store`.
- **Slot reuse / `stop()` not reaping:** ruled out. `stop()` kills, `Wait`s and clears the pipes before `start` runs
  again. The retry does start a new process (pid 1997 above). It is the *previous* round trip's goroutine that killed
  the process.
- **The export job's caller-supplied limit:** not involved. It only changes the timer's duration.

This is a product race in both cases. The harness did one thing: at 2955885, `warm()` was a real build followed by
`defer cancel()`, and that built the exact interleaving in front of every fence.

## Why it did not show up before

- The goroutine only misfires when it has not run at all by the time the round trip ends. Against real build123d the
  holder waits on the pipe for milliseconds, and the goroutine runs during that wait. The fake answers in microseconds.
  The fake fences arrived with the timeout work (PR 73), and that was the first time round trips were short enough for
  this to happen.
- Before PR 73 there was one kill per round trip and nothing recorded why, so a stray kill looked like a crash the next
  build recovered from.
- 160b28c changed `warm()` to start processes by hand, for a different reason. That removed the build before each fence,
  so `main` hid it: 0 failures in 12,000 runs.
- The retry fences kill by hand, and no fence cancelled a context right after a build that succeeded.

## Fix

`internal/domain/cad/sidecar_process.go`, `roundTrip`:

- **The end of the round trip is claimed once.** `end` is an `atomic.Int32` with three values: `running`, `answered` and
  `outOfTime`.
  - The holder claims `answered` when its write fails or its read returns.
  - The goroutine claims `outOfTime` when the limit or the context fires.
  - Only a goroutine that wins its claim records the `lateError` and kills. A goroutine that loses exits without doing
    anything.
- **The goroutine never outlives `roundTrip`.** `finish()` claims, closes `done`, and waits on `exited`. Any kill has
  therefore landed before the holder resets or releases the slot, and the `lateError` is read only after it was
  written, so it can be a plain variable. If the holder lost the claim, `roundTrip` returns the `lateError` whatever the
  read returned. A reply that raced the kill came from a process that is now gone, so `BuildDocument` resets the slot as
  it does after any timeout.
- **The goroutine starts before the request is written**, so a kernel that stops reading its stdin is also bounded by
  the limit. Before, a blocked `Write` had no deadline.
- **A caller whose context has ended gets a `lateError`, not a crash.** `roundTrip` checks `ctx.Err()` before `start`,
  and starts nothing if the context has ended. It also returns `&lateError{caller: ctx.Err()}` when `start` fails
  because the context ended. `BuildDocument` does not change. It still retries only errors that are not a `lateError`,
  and `lateRefusal` still decides the code: `CAD_KERNEL_TIMEOUT` for the kernel's limit or the caller's deadline, and
  `CONNECTOR_UNAVAILABLE` for a caller that cancelled.

What stays the same: a crash is retried exactly once on its own slot, a timeout is never retried, only the affected
slot is reset, and the export job still passes its own limit.

`internal/domain/cad/cadtest/fake.go`: new `cadtest.SlowStart(t, d)`, which holds the fake's ready banner back for `d`.
It stands in for build123d's import, so a fence can end a deadline while a process is starting. It does not change
`CrashOnce`, which was not at fault.

## Verification

- **Same loop, after the fix.** `main` + the fix, with the CI-era `warm()` put back so the fences build the interleaving
  again: `-race -count=500 -cpu 1,2,8`, 3 containers at once. **0 failures in 9,000 top-level runs.** Unfixed, the same
  tree failed 22 times in 1,800 runs. As committed (with its own `warm()`), the seven fake-kernel fences here: the two that
  failed in CI, `AfterATimeout…`, `ATimeoutInOneSlot…`, `AnExportJobIsBoundedByItsOwnCeiling…`, and the two new ones.
  `-race -count=100 -cpu 1,2,8`: 0 failures in 2,100 runs.
- **New fences** in `timeout_internal_test.go`:
  - `TestKernel_ACancelAfterABuildAnsweredLeavesItsProcessServing`: 1,000 builds on one process with `GOMAXPROCS(1)`,
    each followed by `cancel()`. It checks that the process count stays at 1 after every build, then that a CrashOnce
    build is still retried. This is not strictly deterministic, because the scheduler decides whether the goroutine
    has run and nothing outside `roundTrip` can hold it back. With the old `roundTrip` it went red in 11 runs out of 11,
    by build 2 to 203.
  - `TestKernel_ACallerWhoseDeadlineEndsWhileTheKernelStartsIsNotRetried`: deterministic. A 10 s `SlowStart` and a
    500 ms deadline must give `CAD_KERNEL_TIMEOUT` naming the request's deadline and at most one process. A round trip
    for a caller whose deadline has already passed must return its `lateError` and start no process. With the old code:
    `CONNECTOR_UNAVAILABLE`, "restarting it did not help", and the `ctx.Err()` returned unchanged.
- **Drills** (`scripts/drill-fences.sh`), each run on its own:
  - `a cancel after a build answered can kill its process` (put back the no-claim, no-wait goroutine): went red, 11 of 11.
  - `a deadline that ends while the kernel starts is retried as a crash`: went red.
  - `the kill does not record that the kernel's limit ran out`: re-anchored to the new goroutine, went red.
  - `check_anchors.py`: anchor moved 0, ambiguous 0, not backed up 0, duplicate names 0.
- `go vet ./...` is clean. `go test -count=1 ./internal/agent/` passes. `go test -count=1 ./internal/domain/cad/` was run
  on Windows against real build123d, and 120 tests passed, including `TestRetryAfterTheProcessDies` and
  `TestRetryAfterEveryProcessInThePoolDies`. The 11 that failed are the known Windows-only ones: the `TestScript_*`
  tests and the scripted-part kernel tests.

## Regression prevention

- The two fences and three drills above.
- The comment on `roundTrip` explains why the goroutine must be joined and must claim before it kills. The next person
  to "simplify" it back to `defer close(done)` will read why that is wrong.
- `TestKernel_ACancelAfterABuildAnsweredLeavesItsProcessServing` runs on the fake in about a second, with no build123d,
  so it runs in CI's `check` job everywhere.

## Not in this fix

- **`acquire` for a caller whose context has already ended** still picks at random between a free slot and
  `ctx.Done()`. Either way the result is correct: `acquire`'s own `CONNECTOR_UNAVAILABLE` ("no CAD kernel process
  became free"), or `roundTrip`'s `lateError`. The code a person sees can therefore differ between those two, and no
  process is started in either case.
- **`RunScript`** (`script.go`) runs its own process with its own timeout and does not go through `roundTrip`. It was not
  examined for the same pattern.
- **Production logs** have not been searched for the `forge.cad.restarted`-after-success signature described under
  Impact.
- **On Windows** the full `internal/domain/cad` package has known unrelated failures (`TestScript_*`, the scripted-part
  kernel tests, `TestKernel_ExportingManyOccurrencesGrowsLinearly`). The race loops ran in Linux containers, which
  matches CI.
