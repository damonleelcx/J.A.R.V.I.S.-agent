package cad

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// One kernel process, and the pool of them a Kernel hands builds to.
//
// # Why a pool (Phase 4, stage K3 of docs/plan-2026-09-13-millions-of-parts.md)
//
// Until K3 a Kernel WAS one process behind one mutex, so every build in a
// deployment waited for every other: a thirty-second assembly held up the next
// person's fifty-millisecond bracket, and a car built in subsystems could only
// ever build one of them at a time. A process still serves one request at a time
// — it has one stdin — so the pool is N of them, each exactly what the single
// process was, and a build takes whichever is free.
//
// # Why a channel and not a mutex per process
//
// A build that finds every process busy has to wait, and it must stop waiting
// when its caller gives up. A mutex cannot be abandoned; a receive in a select
// with ctx.Done() can. The channel is also FIFO, which is what lets
// TestRetryAfterEveryProcessInThePoolDies reach each slot deterministically.
//
// # Where the build timeout lives
//
// On the SLOT, not on the kernel. The limit is enforced by killing the process
// that is over it (roundTrip), and with a pool there is no longer one process to
// kill: a slow assembly must cost its own slot's process and leave the others
// building. So each sidecar copies the kernel's timeout when the pool is made and
// enforces its own — and BuildDocument resets only the slot the late build ran
// in. See lateError in cad.go for why a build that ran out of time is refused
// rather than retried, which the pool does not change.

// sidecar is one kernel process. Whoever holds it (see Kernel.acquire) is the only
// caller of roundTrip, start and stop.
type sidecar struct {
	python string
	log    *logx.Logger
	// slot is this process's place in the pool, logged so a restart names which
	// process was replaced.
	slot int
	// timeout is how long one build on THIS process may take, copied from the
	// kernel when the pool was made. Each slot holds its own so the deadline
	// goroutine below kills this process and reads nothing shared.
	timeout time.Duration

	// proc guards cmd, because kill is also called by the deadline goroutine in
	// roundTrip while the holder is blocked reading, and stop clears cmd after.
	proc    sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	script  string
	started bool
}

// pool returns the kernel's slots, making them on first use. No process starts
// here: each starts on the first build that takes its slot.
func (k *Kernel) pool() chan *sidecar {
	k.once.Do(func() {
		k.slots = make(chan *sidecar, k.size)
		for i := 0; i < k.size; i++ {
			s := &sidecar{python: k.python, log: k.log, slot: i, timeout: k.timeout}
			k.all = append(k.all, s)
			k.slots <- s
		}
	})
	return k.slots
}

// acquire takes a free process, or gives up when ctx does.
func (k *Kernel) acquire(ctx context.Context) (*sidecar, error) {
	slots := k.pool()
	select {
	case s := <-slots:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// release puts a process back for the next build.
func (k *Kernel) release(s *sidecar) { k.slots <- s }

// Close stops every process in the pool. Safe on a kernel that was never started.
//
// It takes every slot first, so it waits for the builds in flight exactly as the
// single process's mutex made it wait before the pool existed.
func (k *Kernel) Close() {
	if k == nil {
		return
	}
	slots := k.pool()
	held := make([]*sidecar, 0, k.size)
	for i := 0; i < k.size; i++ {
		s := <-slots
		s.stop()
		held = append(held, s)
	}
	for _, s := range held {
		slots <- s
	}
}

// roundTrip sends one request and reads one reply. Caller holds the slot.
//
// It returns a *lateError when this slot's process was killed because time ran
// out — the limit or the caller's context — which is the one failure
// BuildDocument does not retry.
//
// limit is the slot's own timeout for every build but the off-node export job's,
// which passes exportJobTimeout (cad.go, ExportSTEPJob). The holder chooses it
// from this slot, so it is still this slot's limit enforced on this process.
func (s *sidecar) roundTrip(ctx context.Context, req request, limit time.Duration) (*reply, error) {
	// ‼️ A caller whose time has already run out is refused as the lateError it
	// is, and nothing is started; so is one whose time runs out while its process
	// is starting. Until 2026-09-17 both came back as a plain ctx error, which
	// BuildDocument took for a crash: it started a SECOND process for a caller
	// that had gone, and refused the build as CONNECTOR_UNAVAILABLE ("restarting
	// it did not help") instead of saying the deadline ended.
	// Fence: TestKernel_ACallerWhoseDeadlineEndsWhileTheKernelStartsIsNotRetried.
	if err := ctx.Err(); err != nil {
		return nil, &lateError{caller: err}
	}
	if err := s.start(ctx); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, &lateError{caller: cerr}
		}
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// The deadline is enforced by a goroutine that kills the process, because a
	// blocking Read on a pipe does not observe a context. Killing is the only
	// thing that ends it, and it is also the right outcome: a kernel that has
	// not answered by its limit is not going to. Thirty seconds for a build in a
	// request (buildTimeout); an export job's is longer (exportJobTimeout). It is
	// running before the request is written, so a kernel that stops reading its
	// stdin is bounded too.
	//
	// ‼️ The goroutine records WHY it killed the process before it does. To the
	// read below, a process killed for its time and one that crashed are the same
	// EOF, and until 2026-09-15 they were treated the same: retried on a fresh
	// process, killed again, reported as "no working backend". See lateError.
	//
	// ‼️ It kills THIS SLOT'S process and reads THIS SLOT'S limit. s.kill takes
	// s.proc, which is the mutex the holder of this slot and this goroutine
	// share; no other slot is touched, so a build that runs out of time in one
	// process does not interrupt the builds running in the others.
	// Fence: TestKernel_ATimeoutInOneSlotLeavesTheOtherSlotsServing.
	//
	// ‼️ The end of the round trip is decided ONCE, and the goroutine does not
	// outlive it. Until 2026-09-17 roundTrip returned with `defer close(done)`
	// and did not wait: a caller that cancelled its context right after a build
	// ANSWERED (`defer cancel()`, as every handler and fence does) could find the
	// goroutine not yet in its select, with done and ctx.Done() both ready — and
	// select picks at random. Half of those times it killed the process of a build
	// that had succeeded, and recorded a lateError nobody read. The slot kept the
	// dead process as started; the next build on it met EOF, took it for a crash
	// and spent its one retry there. CI saw both halves: a document that crashes
	// once then failed ("reading from the kernel: EOF"), and a build that ran out
	// of time started a second process. Because s.kill reads s.cmd when it runs,
	// a goroutine late enough could also kill the NEXT holder's process mid-build.
	// docs/bugfix/2026-09-17-a-cancel-after-a-build-answered-killed-its-process.md
	//
	// So: whoever ends the round trip first claims it by compare-and-swap — the
	// holder when its write or read returns, the goroutine when time runs out —
	// and only a goroutine that won kills. roundTrip waits for the goroutine to
	// exit before it returns, so any kill has landed before the holder resets or
	// releases the slot. Fence: TestKernel_ACancelAfterABuildAnsweredLeavesItsProcessServing.
	const (
		running = iota
		answered
		outOfTime
	)
	var end atomic.Int32
	var late *lateError // written by the goroutine only after it won; read after it exited
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		timer := time.NewTimer(limit)
		defer timer.Stop()
		var why *lateError
		select {
		case <-done:
			return
		case <-ctx.Done():
			why = &lateError{caller: ctx.Err()}
		case <-timer.C:
			why = &lateError{limit: limit}
		}
		if !end.CompareAndSwap(running, outOfTime) {
			return
		}
		late = why
		s.kill()
	}()
	// finish claims the end of the round trip for the holder and waits for the
	// goroutine. It returns the lateError when time ran out first instead.
	finish := func() *lateError {
		claimed := end.CompareAndSwap(running, answered)
		close(done)
		<-exited
		if claimed {
			return nil
		}
		return late
	}

	if _, err := s.stdin.Write(append(body, '\n')); err != nil {
		if late := finish(); late != nil {
			return nil, late
		}
		return nil, fmt.Errorf("writing to the kernel: %w", err)
	}
	line, err := s.stdout.ReadBytes('\n')
	if late := finish(); late != nil {
		// Time ran out first and the process was killed for it, whatever the read
		// returned: a reply that raced the kill came from a process that is gone.
		return nil, late
	}
	if err != nil {
		return nil, fmt.Errorf("reading from the kernel: %w", err)
	}
	var res reply
	if err := json.Unmarshal(line, &res); err != nil {
		return nil, fmt.Errorf("the kernel wrote something that is not a reply: %w", err)
	}
	return &res, nil
}

func (s *sidecar) start(ctx context.Context) error {
	if s.started {
		return nil
	}
	// The sidecar is embedded and written out, so a deployment is one binary and
	// the script cannot drift from the Go that speaks to it.
	dir, err := os.MkdirTemp("", "forge-cad-")
	if err != nil {
		return err
	}
	script := filepath.Join(dir, "sidecar.py")
	if err := os.WriteFile(script, sidecarSource, 0o600); err != nil {
		return err
	}

	cmd := exec.Command(s.python, script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// stderr is drained rather than inherited: a Python warning on a shared
	// stderr interleaves with this process's own logs, and a full pipe nobody
	// reads blocks the child forever.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the CAD kernel with %q: %w", s.python, err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	s.proc.Lock()
	s.cmd = cmd
	s.proc.Unlock()
	s.stdin, s.stdout, s.script = stdin, bufio.NewReaderSize(stdout, 1<<20), dir

	// The ready banner is written AFTER the import, so waiting for it is what
	// distinguishes "still starting" from "will never start".
	type banner struct {
		res *reply
		err error
	}
	ch := make(chan banner, 1)
	go func() {
		line, err := s.stdout.ReadBytes('\n')
		if err != nil {
			ch <- banner{err: err}
			return
		}
		var r reply
		if err := json.Unmarshal(line, &r); err != nil {
			ch <- banner{err: fmt.Errorf("the kernel's first line was not a banner: %w", err)}
			return
		}
		ch <- banner{res: &r}
	}()

	select {
	case b := <-ch:
		if b.err != nil || b.res == nil || !b.res.Ready {
			s.stop()
			detail := "the kernel exited while starting"
			if b.err != nil {
				detail = b.err.Error()
			} else if b.res != nil && b.res.Error != "" {
				detail = b.res.Error
			}
			return errors.New(detail)
		}
	case <-time.After(startTimeout):
		s.stop()
		return fmt.Errorf("the CAD kernel did not start within %s", startTimeout)
	case <-ctx.Done():
		s.stop()
		return ctx.Err()
	}

	s.started = true
	s.log.Info(ctx, logx.EventCADStarted, "python", s.python, "slot", s.slot)
	return nil
}

// process is the running process, or nil.
func (s *sidecar) process() *os.Process {
	s.proc.Lock()
	defer s.proc.Unlock()
	if s.cmd == nil {
		return nil
	}
	return s.cmd.Process
}

func (s *sidecar) kill() {
	if p := s.process(); p != nil {
		_ = p.Kill()
	}
}

func (s *sidecar) stop() {
	s.kill()
	s.proc.Lock()
	cmd := s.cmd
	s.cmd = nil
	s.proc.Unlock()
	if cmd != nil {
		_ = cmd.Wait()
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.script != "" {
		_ = os.RemoveAll(s.script)
	}
	s.stdin, s.stdout, s.script, s.started = nil, nil, "", false
}
