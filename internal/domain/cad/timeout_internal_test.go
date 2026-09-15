package cad

import (
	"context"
	"fmt"
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
// dies, when a solid asks it to — so each of these takes about one short limit.
// docs/bugfix/2026-09-15-a-kernel-build-that-ran-out-of-time-was-reported-as-no-kernel.md

// cue is a one-part document whose part asks the fake kernel for a behaviour.
func cue(id string) geometry.Document {
	d := block()
	d.Parts[0].ID = id
	return d
}

// warm starts every process in the pool now, so a fence times the build and not
// a test binary starting.
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

// ‼️ Until this fix, a build past its limit was killed, RETRIED on a fresh process,
// killed again, and reported as CONNECTOR_UNAVAILABLE — "no working backend in this
// deployment", HTTP 501. A 31 s build cost 60+ s and read as a missing kernel
// (measured: docs/spikes/2026-09-15-kernel-build-ceiling).
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

// After a timeout, the slot the killed process held gets a fresh one, and the next
// build does not pay for the kill.
//
// # Why the next build is one that crashes once
//
// A slot left holding the killed process still works for the next build — its
// first round trip fails and the one retry starts a fresh process — so "the next
// build succeeds" cannot tell a restored slot from a dead one. What the dead one
// costs is that retry. A next build whose fresh process crashes once needs it, and
// only gets it if the timeout already replaced the process.
func TestKernel_AfterATimeoutTheSlotHasAFreshProcessForTheNextBuild(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	k := New(python, logx.Discard())
	k.timeout = 500 * time.Millisecond
	defer k.Close()
	warm(t, k)
	killed := k.all[0].process().Pid

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := k.BuildDocument(ctx, cue(cadtest.Slow), geometry.Millimetre, ""); !errs.Is(err, errs.CodeKernelTimeout) {
		t.Fatalf("the slow build did not time out: %v", err)
	}
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
	if p := k.all[0].process(); p == nil || p.Pid == killed {
		t.Error("the slot still holds the process the timeout killed")
	}
	if n := cadtest.Starts(t, dir); n != 3 {
		t.Errorf("%d processes started, want 3: the one killed, the one that crashed, its replacement", n)
	}
}

// A process that DIES is still retried exactly once — K3's retry, which the timeout
// must not take away.
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
