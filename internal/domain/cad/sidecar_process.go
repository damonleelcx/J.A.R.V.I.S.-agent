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

// sidecar is one kernel process. Whoever holds it (see Kernel.acquire) is the only
// caller of roundTrip, start and stop.
type sidecar struct {
	python string
	log    *logx.Logger
	// slot is this process's place in the pool, logged so a restart names which
	// process was replaced.
	slot int
	// timeout is how long one build on this process may take, copied from the
	// kernel when the pool is made. Not a setting: see Kernel.timeout.
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
func (s *sidecar) roundTrip(ctx context.Context, req request) (*reply, error) {
	if err := s.start(ctx); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := s.stdin.Write(append(body, '\n')); err != nil {
		return nil, fmt.Errorf("writing to the kernel: %w", err)
	}

	// The deadline is enforced by a goroutine that kills the process, because a
	// blocking Read on a pipe does not observe a context. Killing is the only
	// thing that ends it, and it is also the right outcome: a kernel that has
	// not answered in thirty seconds is not going to.
	//
	// ‼️ The goroutine records WHY it killed the process before it does. To the
	// read below, a process killed for its time and one that crashed are the same
	// EOF, and until 2026-09-15 they were treated the same: retried on a fresh
	// process, killed again, reported as "no working backend". See lateError.
	done := make(chan struct{})
	defer close(done)
	var stopped atomic.Pointer[lateError]
	go func() {
		timer := time.NewTimer(s.timeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-ctx.Done():
			stopped.Store(&lateError{caller: ctx.Err()})
			s.kill()
		case <-timer.C:
			stopped.Store(&lateError{limit: s.timeout})
			s.kill()
		}
	}()

	line, err := s.stdout.ReadBytes('\n')
	if err != nil {
		if late := stopped.Load(); late != nil {
			return nil, late
		}
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
