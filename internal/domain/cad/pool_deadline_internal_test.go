package cad

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad/cadtest"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The kernel pool's open items after #100 and #137: a wait for a busy kernel, a
// start that runs out of time, the build limit as a setting, and the first queue
// of subtree meshes paying for a start. All against cadtest's fake process.

// ‼️ Until 2026-09-17 a caller whose deadline ended while every kernel process was
// busy got CONNECTOR_UNAVAILABLE, HTTP 501, "a capability is declared but has no
// working backend in this deployment" — sending an operator to FORGE_CAD_PYTHON for
// a kernel that was there, building somebody else's design.
func TestKernel_ADeadlineThatEndsWaitingForABusyKernelIsATimeout(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	k := New(python, logx.Discard())
	defer k.Close()

	// Every process in the pool is taken, as a long build would take it.
	busy := <-k.pool()
	const deadline = 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	_, err := k.BuildDocument(ctx, cue("fine"), geometry.Millimetre, "")

	if got := errs.CodeOf(err); got != errs.CodeKernelTimeout {
		t.Errorf("code %s, want %s: a deadline that ended waiting for a busy kernel is not a missing kernel (%v)",
			got, errs.CodeKernelTimeout, err)
	}
	if def, _ := errs.Lookup(errs.CodeOf(err)); def.HTTPStatus != 504 {
		t.Errorf("HTTP %d, want 504", def.HTTPStatus)
	}
	if d := errs.DetailOf(err); !strings.Contains(d, "waited for a free CAD kernel process") || !strings.Contains(d, "all 1 were building") {
		t.Errorf("detail %q does not say the build waited for a busy kernel, and how many processes it has", d)
	}
	if n := cadtest.Starts(t, dir); n != 0 {
		t.Errorf("%d processes started for a build that never got one", n)
	}

	// A caller that CANCELLED has gone, and keeps the answer lateRefusal gives it.
	ctx2, cancel2 := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel2)
	_, err = k.BuildDocument(ctx2, cue("fine"), geometry.Millimetre, "")
	if got := errs.CodeOf(err); got != errs.CodeConnectorUnavailable {
		t.Errorf("a cancelled caller waiting for a busy kernel got %s, want %s", got, errs.CodeConnectorUnavailable)
	}
	k.release(busy)
}

// ‼️ Until 2026-09-17 a process still starting when startTimeout (60 s) ran out was
// taken for a crash: stopped, started again, and after another minute refused as
// "restarting it did not help". A request in forged has no deadline of its own, so
// a person waited two minutes for the same answer.
func TestKernel_AKernelThatDoesNotStartInTimeIsNotStartedAgain(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	cadtest.SlowStart(t, 10*time.Second)
	k := New(python, logx.Discard())
	const limit = 300 * time.Millisecond
	k.startLimit = limit
	defer k.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := k.BuildDocument(ctx, cue("fine"), geometry.Millimetre, "")
	if err == nil {
		t.Fatal("a build whose kernel never started succeeded")
	}
	if got := errs.CodeOf(err); got != errs.CodeKernelTimeout {
		t.Errorf("code %s, want %s (%v)", got, errs.CodeKernelTimeout, err)
	}
	if d := errs.DetailOf(err); !strings.Contains(d, "did not finish starting within "+limit.String()) {
		t.Errorf("detail %q does not say the kernel did not start within %s", d, limit)
	}
	if n := cadtest.Starts(t, dir); n != 1 {
		t.Errorf("%d kernel processes started, want 1: a start that ran out of time was retried", n)
	}

	// The slot is not left holding the slow process: the next build starts afresh.
	cadtest.SlowStart(t, 0)
	if _, err := k.BuildDocument(ctx, cue("fine"), geometry.Millimetre, ""); err != nil {
		t.Fatalf("the build after a start that ran out of time failed: %v", err)
	}
	if n := cadtest.Starts(t, dir); n != 2 {
		t.Errorf("%d processes started, want 2", n)
	}
}

// FORGE_CAD_BUILD_TIMEOUT reaches the slots, through the one constructor both
// processes use.
func TestKernel_FromConfigCarriesTheBuildTimeout(t *testing.T) {
	python, _ := cadtest.FakeKernel(t)
	const limit = 400 * time.Millisecond
	k := FromConfig(config.CADConfig{Python: python, Pool: 1, BuildTimeout: limit, AllowScripts: true}, logx.Discard())
	defer k.Close()
	if !k.ScriptsEnabled() {
		t.Error("FromConfig dropped AllowScripts")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := k.BuildDocument(ctx, cue(cadtest.Slow), geometry.Millimetre, "")
	if !errs.Is(err, errs.CodeKernelTimeout) {
		t.Fatalf("a build that never answers did not time out: %v", err)
	}
	if d := errs.DetailOf(err); !strings.Contains(d, "CAD kernel's "+limit.String()+" limit") {
		t.Errorf("detail %q does not name the configured %s limit", d, limit)
	}

	if got := FromConfig(config.CADConfig{Python: python, Pool: 2}, logx.Discard()); got.timeout != buildTimeout || got.size != 2 {
		t.Errorf("an unset build timeout gave %s and pool %d, want %s and 2", got.timeout, got.size, buildTimeout)
	}
}

// #93's open item: a design loaded a subtree at a time asks for a dozen subtree
// meshes at once, and they queue for forged's one kernel process. #125 kept the
// queue (a second process at 1 CPU was slower in every run, and does not fit in
// 1 GiB); what this holds is that the queue no longer starts with build123d's
// import in front of it.
//
// # How the queue is measured without load sensitivity
//
// The fake kernel takes `start` to start and `build` per build, and nothing else
// takes comparable time, so each request's latency is start (when it paid for one)
// plus its place in the queue times build. The assertions are on those terms, with
// a margin of half a start, not on a machine's speed.
func TestKernel_APrestartedKernelAnswersTheFirstQueueWithoutPayingForAStart(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	const (
		start    = 2 * time.Second
		build    = 50 * time.Millisecond
		subtrees = 12
	)
	cadtest.SlowStart(t, start)
	cadtest.SlowBuilds(t, build)

	queue := func(prestart bool) (first, last time.Duration) {
		k := New(python, logx.Discard())
		defer k.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if prestart {
			if err := k.Prestart(ctx); err != nil {
				t.Fatalf("prestart: %v", err)
			}
		}
		began := time.Now()
		took := make([]time.Duration, subtrees)
		var wg sync.WaitGroup
		for i := 0; i < subtrees; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if _, err := k.BuildMesh(ctx, cue(fmt.Sprintf("subtree-%d", i)), geometry.Millimetre); err != nil {
					t.Errorf("subtree %d: %v", i, err)
				}
				took[i] = time.Since(began)
			}(i)
		}
		wg.Wait()
		sort.Slice(took, func(a, b int) bool { return took[a] < took[b] })
		return took[0], took[subtrees-1]
	}

	coldFirst, coldLast := queue(false)
	startsCold := cadtest.Starts(t, dir)
	warmFirst, warmLast := queue(true)
	t.Logf("%d subtrees, start %s, build %s: cold first %s last %s; prestarted first %s last %s",
		subtrees, start, build, coldFirst, coldLast, warmFirst, warmLast)

	if startsCold != 1 || cadtest.Starts(t, dir) != 2 {
		t.Errorf("processes started: %d cold, %d in all, want 1 and 2: one process serves each queue",
			startsCold, cadtest.Starts(t, dir))
	}
	if coldFirst < start {
		t.Fatalf("the cold queue's first subtree took %s, under the %s start: the fence is not measuring a start", coldFirst, start)
	}
	if warmFirst >= start/2 {
		t.Errorf("after Prestart the first subtree still took %s, at least half the %s start: it paid for the start", warmFirst, start)
	}
	if coldLast-warmLast < start/2 {
		t.Errorf("after Prestart the last subtree took %s against %s cold: the queue still carries the start", warmLast, coldLast)
	}
	// Still one process: the queue itself is #125's CPU limit and is kept.
	if warmLast < time.Duration(subtrees)*build {
		t.Errorf("the prestarted queue ended in %s, under %d builds of %s: builds ran side by side", warmLast, subtrees, build)
	}
}

// A kernel with no interpreter starts nothing, and Prestart says nothing.
func TestKernel_PrestartWithoutAKernelIsANoOp(t *testing.T) {
	k := New("", logx.Discard())
	if err := k.Prestart(context.Background()); err != nil {
		t.Errorf("prestart of a deployment with no kernel: %v", err)
	}
	if k.slots != nil {
		t.Error("prestart of a deployment with no kernel made a pool")
	}
}

// ‼️ Until 2026-09-17 a caller that cancelled mid-build — a closed tab, a navigation
// away from a design loading a subtree at a time — had its kernel process KILLED, so
// the next person to open a design paid build123d's import for a build nobody read.
// The cancelled caller now gets its answer at once; the build finishes for nobody,
// its reply is read and thrown away, and the SAME process serves the next build.
//
// The next build asks for two parts and the abandoned one for one, so a slot handed
// back with the abandoned reply still in its pipe answers the next build with 1.
func TestKernel_ACancelledCallerLeavesItsProcessToFinishAndServeTheNextBuild(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	const build = time.Second
	cadtest.SlowBuilds(t, build)
	k := New(python, logx.Discard())
	defer k.Close()
	warm(t, k)
	served := pid(k, 0)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	began := time.Now()
	_, err := k.BuildDocument(ctx, cue("abandoned"), geometry.Millimetre, "")
	if took := time.Since(began); took >= build/2 {
		t.Errorf("the cancelled caller waited %s, half the %s build or more: it waited for the build", took, build)
	}
	if got := errs.CodeOf(err); got != errs.CodeConnectorUnavailable {
		t.Errorf("a cancelled caller got %s, want %s (%v)", got, errs.CodeConnectorUnavailable, err)
	}
	// The abandoned build is still running and its reply is unread, so its slot
	// must not be free yet: whoever took it would share the pipe with that reply.
	if n := len(k.slots); n != 0 {
		t.Errorf("%d slot(s) free while the abandoned build's reply is still unread, want 0", n)
	}

	two := cue("first")
	two.Parts = append(two.Parts, cue("second").Parts...)
	next, cancelNext := context.WithTimeout(context.Background(), time.Minute)
	defer cancelNext()
	got, err := k.BuildDocument(next, two, geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("the build after a cancelled one failed: %v", err)
	}
	if got.Parts != 2 {
		t.Errorf("the next build was answered with %d parts, want its own 2: it read the abandoned build's reply", got.Parts)
	}
	if n := cadtest.Starts(t, dir); n != 1 {
		t.Errorf("%d processes started, want 1: the cancel killed the process and the next build restarted it", n)
	}
	if p := pid(k, 0); p != served {
		t.Errorf("slot 0 holds process %d, was %d", p, served)
	}
}

// An abandoned build is still bounded: past the limit its process is killed and the
// slot reset, and Close ends one rather than waiting out its limit.
func TestKernel_AnAbandonedBuildIsStillKilledAtItsLimit(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	k := New(python, logx.Discard())
	k.timeout = 500 * time.Millisecond
	defer k.Close()
	warm(t, k)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := k.BuildDocument(ctx, cue(cadtest.Slow), geometry.Millimetre, ""); err == nil {
		t.Fatal("a cancelled build that never answers succeeded")
	}
	next, cancelNext := context.WithTimeout(context.Background(), time.Minute)
	defer cancelNext()
	// A next build that crashes once needs its one retry, and only has it if the
	// killed process was already cleared from the slot (as in
	// TestKernel_AfterATimeoutTheKernelStartsAFreshProcessForTheNextBuild).
	if _, err := k.BuildDocument(next, cue(cadtest.CrashOnce), geometry.Millimetre, ""); err != nil {
		t.Fatalf("the build after an abandoned build that ran past its limit spent its retry on the killed process: %v", err)
	}
	if n := cadtest.Starts(t, dir); n != 3 {
		t.Errorf("%d processes started, want 3: the one killed at its limit, the one that crashed, its replacement", n)
	}

	// Close does not wait out a hung abandoned build's limit.
	k2 := New(python, logx.Discard())
	warm(t, k2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel2)
	_, _ = k2.BuildDocument(ctx2, cue(cadtest.Slow), geometry.Millimetre, "")
	began := time.Now()
	k2.Close()
	if took := time.Since(began); took >= 10*time.Second {
		t.Errorf("Close took %s with an abandoned build running: it waited for the build's limit", took)
	}
}
