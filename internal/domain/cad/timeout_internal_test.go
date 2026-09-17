package cad

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad/cadtest"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A build that runs out of time is not a crashed kernel.
//
// # Why these run against a fake process
//
// What they hold is about time and about which process died, and a real build
// that is reliably slower than a limit makes the fence either slow or
// load-sensitive. cadtest's fake answers the sidecar's protocol and is slow, or
// dies, when a solid asks it to — so each of these takes about one short limit,
// and none needs build123d.
// docs/bugfix/2026-09-15-a-kernel-build-that-ran-out-of-time-was-reported-as-no-kernel.md

// cue is a one-part document whose part asks the fake kernel for a behaviour.
func cue(id string) geometry.Document {
	return geometry.Document{
		Name: "block", Units: "mm",
		Parts: []geometry.Part{{ID: id, Shape: "box",
			Size:     map[string]float64{"width": 10, "height": 10, "depth": 10},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
}

// warm starts EVERY process in the pool now, so a fence times the build and not
// a test binary starting.
//
// It starts them by hand rather than by building: a build starts only the one
// slot it takes, which is the same thing in a pool of one and not in a pool of
// two. Released in slot order, so the FIFO hands them out in that order too.
func warm(t *testing.T, k *Kernel) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var held []*sidecar
	for i := 0; i < k.size; i++ {
		s, err := k.acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.start(ctx); err != nil {
			t.Fatalf("the fake kernel did not start: %v", err)
		}
		held = append(held, s)
	}
	for _, s := range held {
		k.release(s)
	}
}

// pid is the process slot n of the pool holds, or 0 when it holds none.
//
// It used to read k.cmd, because a Kernel WAS one process. K3 made it N slots
// that each own one, so the same question is now asked of a slot: these fences
// run a pool of one, where slot 0 is the process the kernel used to be.
func pid(k *Kernel, n int) int {
	if n >= len(k.all) {
		return 0
	}
	p := k.all[n].process()
	if p == nil {
		return 0
	}
	return p.Pid
}

// ‼️ Until this fix, a build past its limit was killed, RETRIED on a fresh process,
// killed again, and reported as CONNECTOR_UNAVAILABLE — "no working backend in this
// deployment", HTTP 501. A 31 s build cost 60+ s and read as a missing kernel
// (measured on the millions-of-parts stack: the 2026-09-15 kernel build ceiling
// spike, PR #95).
func TestKernel_ABuildThatRunsOutOfTimeIsNotRetriedAndSaysSo(t *testing.T) {
	const limit = 500 * time.Millisecond

	for _, c := range []struct {
		name string
		// kernel is the kernel's own limit; caller is the caller's deadline.
		kernel, caller time.Duration
		says           string
	}{
		{"the kernel's limit", limit, time.Minute, "took longer than the CAD kernel's " + limit.String() + " limit"},
		{"the caller's deadline", buildTimeout, limit, "the request's own deadline ended"},
	} {
		t.Run(c.name, func(t *testing.T) {
			python, dir := cadtest.FakeKernel(t)
			k := New(python, logx.Discard())
			k.timeout = c.kernel
			defer k.Close()
			warm(t, k)

			ctx, cancel := context.WithTimeout(context.Background(), c.caller)
			defer cancel()
			began := time.Now()
			_, err := k.BuildDocument(ctx, cue(cadtest.Slow), geometry.Millimetre, "")
			took := time.Since(began)

			if err == nil {
				t.Fatal("a build that never answered succeeded")
			}
			if got := errs.CodeOf(err); got != errs.CodeKernelTimeout {
				t.Errorf("code %s, want %s: a build that ran out of time is not a missing kernel (%v)",
					got, errs.CodeKernelTimeout, err)
			}
			if errs.IsRetryable(err) {
				t.Error("a timeout is retryable; the same build takes as long again")
			}
			if d := errs.DetailOf(err); !strings.Contains(d, c.says) {
				t.Errorf("detail %q does not say %q", d, c.says)
			}
			if n := cadtest.Starts(t, dir); n != 1 {
				t.Errorf("%d kernel processes started for one build that ran out of time, want 1: it was retried", n)
			}
			if took >= 2*limit {
				t.Errorf("took %s, two limits or more (%s): the build was retried", took, 2*limit)
			}
		})
	}

	// The remedy a person reads is static text in the registry, so it cannot name
	// the limit by reference. This keeps the two from drifting.
	def, _ := errs.Lookup(errs.CodeKernelTimeout)
	if want := fmt.Sprintf("%d seconds", int(buildTimeout.Seconds())); !strings.Contains(def.Remedy, want) {
		t.Errorf("the registry's remedy for %s does not say %q, which is buildTimeout: %q",
			errs.CodeKernelTimeout, want, def.Remedy)
	}
}

// After a timeout the kernel holds a fresh process for the next build, and the
// next build does not pay for the kill.
//
// # Why the next build is one that crashes once
//
// A kernel left holding the killed process still works for the next build — its
// first round trip fails and the one retry starts a fresh process — so "the next
// build succeeds" cannot tell a reset kernel from a dead one. What the dead one
// costs is that retry. A next build whose fresh process crashes once needs it, and
// only gets it if the timeout already let go of the killed process.
func TestKernel_AfterATimeoutTheKernelStartsAFreshProcessForTheNextBuild(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	k := New(python, logx.Discard())
	k.timeout = 500 * time.Millisecond
	defer k.Close()
	warm(t, k)
	killed := pid(k, 0)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := k.BuildDocument(ctx, cue(cadtest.Slow), geometry.Millimetre, ""); !errs.Is(err, errs.CodeKernelTimeout) {
		t.Fatalf("the slow build did not time out: %v", err)
	}
	// ‼️ From #100: the late build RELEASED its slot. Resetting a slot and losing
	// it look identical from the next build's side when the pool has spares, and
	// a pool that shrinks by one process per timeout ends up serving nothing.
	if n := len(k.slots); n != k.size {
		t.Fatalf("%d of %d slots are free after the timeout: the pool shrank", n, k.size)
	}

	got, err := k.BuildDocument(ctx, cue(cadtest.CrashOnce), geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("the build after a timeout could not recover from one crash — its retry was spent on the process the timeout killed: %v", err)
	}
	if got.Parts != 1 {
		t.Errorf("built %d parts, want 1", got.Parts)
	}
	if p := pid(k, 0); p == 0 || p == killed {
		t.Error("the kernel still holds the process the timeout killed")
	}
	if n := cadtest.Starts(t, dir); n != 3 {
		t.Errorf("%d processes started, want 3: the one killed, the one that crashed, its replacement", n)
	}
}

// A process that DIES is still retried exactly once — the retry the timeout must
// not take away. TestRetryAfterTheProcessDies holds the same retry against a real
// build123d; this one needs no Python, so it runs everywhere.
func TestKernel_AProcessThatDiesMidBuildIsStillRetriedOnce(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	k := New(python, logx.Discard())
	defer k.Close()
	warm(t, k)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := k.BuildDocument(ctx, cue(cadtest.CrashOnce), geometry.Millimetre, ""); err != nil {
		t.Fatalf("a process that died once was not retried: %v", err)
	}
	if n := cadtest.Starts(t, dir); n != 2 {
		t.Fatalf("%d processes started, want 2", n)
	}

	_, err := k.BuildDocument(ctx, cue(cadtest.Crash), geometry.Millimetre, "")
	if err == nil {
		t.Fatal("a document that kills every process built")
	}
	if errs.Is(err, errs.CodeKernelTimeout) {
		t.Errorf("a crash was reported as a timeout: %v", err)
	}
	if n := cadtest.Starts(t, dir); n != 3 {
		t.Errorf("%d processes started, want 3: a crash is retried once, and only once", n)
	}
}

// A build that runs out of time costs ITS slot's process, and nobody else's.
//
// # Why neither #73 nor #103 could hold this
//
// The timeout work (#100/#103/#110) was written against a Kernel that WAS one
// process: "the kernel is reset after a late error" and "the other processes are
// untouched" were the same sentence there, because there were no others. The pool
// (#73) had no timeout to cross. Reconciling them makes the second claim
// separable, and separable claims need their own fence: a kill that reached
// k.cmd-as-it-was, or a reset that emptied the pool rather than one slot, would
// pass every fence on both sides and take a working process away from whoever was
// mid-build in it.
//
// The pool is FIFO, so starting both processes by hand and giving them back in
// slot order makes the slow build take slot 0 and the build beside it slot 1.
func TestKernel_ATimeoutInOneSlotLeavesTheOtherSlotsServing(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	k := New(python, logx.Discard()).WithPool(2)
	k.timeout = 500 * time.Millisecond
	defer k.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	warm(t, k)
	if n := cadtest.Starts(t, dir); n != 2 {
		t.Fatalf("%d processes started warming a pool of two, want 2", n)
	}
	survivor := pid(k, 1)
	if survivor == 0 {
		t.Fatal("slot 1 holds no process to survive the timeout")
	}

	late := make(chan error, 1)
	go func() {
		_, err := k.BuildDocument(ctx, cue(cadtest.Slow), geometry.Millimetre, "")
		late <- err
	}()
	// Wait for the slow build to actually hold a slot, so the build below cannot
	// race it for slot 0. One slot left in the channel says it has taken one.
	for deadline := time.Now().Add(30 * time.Second); len(k.slots) > 1; {
		if time.Now().After(deadline) {
			t.Fatal("the slow build never took a slot")
		}
		time.Sleep(time.Millisecond)
	}

	// Slot 1 serves a build while slot 0 is running past its limit and being
	// killed for it.
	got, err := k.BuildDocument(ctx, cue("fine"), geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("a build in another slot failed while one slot ran out of time: %v", err)
	}
	if got.Parts != 1 {
		t.Errorf("the build beside the timeout made %d parts, want 1", got.Parts)
	}

	if err := <-late; !errs.Is(err, errs.CodeKernelTimeout) {
		t.Fatalf("the slow build did not time out: %v", err)
	}
	if p := pid(k, 0); p != 0 {
		t.Errorf("slot 0 still holds process %d after its build ran out of time", p)
	}
	if p := pid(k, 1); p != survivor {
		t.Errorf("slot 1 holds process %d, was %d: the timeout in slot 0 killed another slot's process", p, survivor)
	}
	if n := cadtest.Starts(t, dir); n != 2 {
		t.Fatalf("%d processes started, want 2: the timeout in slot 0 replaced a process somewhere else", n)
	}

	// And slot 1 is still SERVING, not merely alive: the slow build gave slot 0
	// back after the fast one gave slot 1 back, so the FIFO hands this build slot
	// 1 — which answers without a restart only if its process was left alone.
	if _, err := k.BuildDocument(ctx, cue("fine"), geometry.Millimetre, ""); err != nil {
		t.Fatalf("the slot that did not time out stopped serving: %v", err)
	}
	if n := cadtest.Starts(t, dir); n != 2 {
		t.Errorf("%d processes started, want 2: the build after the timeout had to restart the other slot's process", n)
	}
}

// A caller that cancels its context right after a build ANSWERED keeps the
// process that answered it.
//
// ‼️ Until 2026-09-17 the deadline goroutine in roundTrip could still be on its
// way into its select when roundTrip returned, and `defer cancel()` right behind
// the build then made done and ctx.Done() ready together. select chose at random,
// so about half of those times it killed a process whose build had succeeded. The
// slot kept it as started, and the next build on it met EOF and spent its one
// retry there. CI saw it as the two fences above failing in opposite directions:
// warm() was then itself a BuildDocument with `defer cancel()`.
// docs/bugfix/2026-09-17-a-cancel-after-a-build-answered-killed-its-process.md
//
// # Why many builds and not one
//
// Whether the goroutine has reached its select when roundTrip returns is up to
// the scheduler, and nothing outside roundTrip can hold it back. The fix makes
// the outcome not depend on it — roundTrip waits for the goroutine, and the
// goroutine kills only a round trip nobody has ended — so every one of these
// builds must keep the process. Before it, each was a coin that could land wrong:
// with the old roundTrip restored, eight runs of this fence went red by build 2,
// 2, 8, 40, 47, 61, 65 and 108. The builds are microseconds each against the
// fake, so a thousand cost about a second.
func TestKernel_ACancelAfterABuildAnsweredLeavesItsProcessServing(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	k := New(python, logx.Discard())
	defer k.Close()
	warm(t, k)
	served := pid(k, 0)

	// One P, so the goroutine is not run until the holder yields — and against a
	// fake that answers in microseconds the holder often does not before the
	// build returns. That is the interleaving: done and ctx.Done() both ready at
	// the goroutine's first select. More Ps only make it rarer.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	const builds = 1000
	for i := 0; i < builds; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		got, err := k.BuildDocument(ctx, cue("fine"), geometry.Millimetre, "")
		cancel()
		if err != nil {
			t.Fatalf("build %d of %d, on a kernel that answers every build, failed: %v", i+1, builds, err)
		}
		if got.Parts != 1 {
			t.Fatalf("build %d made %d parts, want 1", i+1, got.Parts)
		}
		if n := cadtest.Starts(t, dir); n != 1 {
			t.Fatalf("%d processes started by build %d of %d, want 1: a cancel after a build answered "+
				"killed the process that answered it, and the next build spent its retry replacing it", n, i+1, builds)
		}
	}
	if p := pid(k, 0); p != served {
		t.Errorf("slot 0 holds process %d, was %d", p, served)
	}

	// And the retry that a stray kill would have spent is still there for a real
	// crash on the same slot.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := k.BuildDocument(ctx, cue(cadtest.CrashOnce), geometry.Millimetre, ""); err != nil {
		t.Fatalf("a process that died once was not retried after %d cancelled builds: %v", builds, err)
	}
}

// A caller whose deadline ends while its process is still STARTING is refused
// as out of time, and no second process is started for it.
//
// ‼️ Until 2026-09-17 start's ctx.Err() came back to BuildDocument as a plain
// error, so it was taken for a crash: the slot was reset, a second process was
// started for a caller that had gone, and the refusal said CONNECTOR_UNAVAILABLE,
// "restarting it did not help". In production every build that takes a slot
// after a crash or a timeout starts a process first — build123d's import is
// seconds — inside the request's 30 s.
func TestKernel_ACallerWhoseDeadlineEndsWhileTheKernelStartsIsNotRetried(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	cadtest.SlowStart(t, 10*time.Second)
	k := New(python, logx.Discard())
	defer k.Close()

	const deadline = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	began := time.Now()
	_, err := k.BuildDocument(ctx, cue("fine"), geometry.Millimetre, "")
	took := time.Since(began)

	if err == nil {
		t.Fatal("a build whose kernel never finished starting succeeded")
	}
	if got := errs.CodeOf(err); got != errs.CodeKernelTimeout {
		t.Errorf("code %s, want %s: the caller's deadline ended while the kernel started, and it was "+
			"taken for a crash (%v)", got, errs.CodeKernelTimeout, err)
	}
	if d := errs.DetailOf(err); !strings.Contains(d, "the request's own deadline ended") {
		t.Errorf("detail %q does not say the request's own deadline ended", d)
	}
	if n := cadtest.Starts(t, dir); n > 1 {
		t.Errorf("%d kernel processes started for a caller whose deadline ended, want at most 1: it was retried", n)
	}
	if took >= 5*time.Second {
		t.Errorf("took %s: the build waited for the kernel to start after its deadline ended", took)
	}

	// A caller that has already run out of time when it reaches the process is
	// refused the same way, and starts nothing.
	k2 := New(python, logx.Discard())
	defer k2.Close()
	s := <-k2.pool()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	_, err = s.roundTrip(expired, request{}, buildTimeout)
	k2.release(s)
	var late *lateError
	if !errors.As(err, &late) || !errors.Is(late.caller, context.DeadlineExceeded) {
		t.Errorf("a round trip for a caller already out of time returned %v, want its lateError", err)
	}
	if s.process() != nil {
		t.Error("a round trip for a caller already out of time started a process")
	}
}
