package agent

import (
	"context"
	"sync"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Progress is the ticker for long work that holds a caller and writes nothing
// while it runs (PRD NFR-02: "long jobs report progress at least every 10 s").
//
// # What this is for, and what already covers the rest
//
// A task the worker runs is already covered: runTask starts a heartbeat before
// it branches into the build loop, the export loop or the tool loop
// (worker.go), each beat writes the task's row, the row's trigger stamps
// updated_at, and TaskDTO.last_seen_at is that stamp. So every step of every
// goal — build or not — already has a signal a client can poll.
//
// What has none is work that never becomes a task row. Planning is the case
// that prompted this: POST /v1/goals runs the planner INSIDE the handler, which
// takes over two minutes on a live model, and until agent.PlanApplier writes
// plan.created there is nothing on the goal for a client to see. An open
// connection is not progress.
//
// # Why this is a ticker and not a percentage
//
// It reports elapsed time and nothing else, because elapsed time is the only
// thing it actually knows. `forgectl goal new` reached the same conclusion in
// startElapsedTicker (cmd/forgectl/goal.go) and states it plainly: "A fake
// progress bar would be worse than silence — it would imply the command can see
// how far along the model is, which it cannot." A caller that wants a fraction
// wants the work to be divisible into countable pieces, and a single model call
// is not. ProgressReport therefore has exactly two fields and neither is
// invented; see the fence in progress_test.go, which holds that shape on
// purpose.
//
// # What it deliberately does not do
//
// It does not know what a report IS. The emit callback writes it — a timeline
// event, a row stamp, a line on a terminal — because where progress belongs is
// a property of the surface reporting, not of the clock.
//
// It does not retry a failed report, and it does not carry one forward. A ping
// saying "still running at t+10 s" is worthless at t+15 s, when the next one is
// already due; the next tick is the retry.
type Progress struct {
	// What is the label a report leads with, in the present continuous as a
	// person would read it: "still planning", "still exporting".
	What string
	// Every is the reporting interval. Zero means ProgressEvery, which is what
	// production always wants; it is a field only so a fence can hold a job for
	// several intervals without holding a test for half a minute.
	Every time.Duration
	// Clock is what elapsed time is measured against. Nil means the wall clock.
	// Taking it from the caller rather than calling time.Since keeps a fence
	// able to state the exact elapsed time a report should carry.
	Clock clock.Clock
	// Log is where a report that could not be written goes. Nil is legal and
	// means the failure is swallowed, which no caller in this repository wants.
	Log *logx.Logger
	// Lost is the event name logged when Emit fails. The caller names it
	// because the caller owns the operation being reported on, and an operator
	// greps the operation, not the ticker.
	Lost logx.Event
	// Emit writes one report. Nil means the ticker runs and reports nothing,
	// which is only useful to a test.
	//
	// ‼️ Its error is LOGGED AND DROPPED. A progress write that failed must not
	// fail the work it is reporting on: the planner call that is still running
	// is the valuable thing, and killing it because a ping did not land would
	// turn a cosmetic fault into a lost two-minute model call. It must not be
	// silent either — a timeline with a hole in it looks exactly like work that
	// was never done. This is the same trade-off, and the same resolution, as
	// ConverseHandlers.keepSaid in internal/httpapi/converse.go.
	Emit func(ctx context.Context, rep ProgressReport) error
}

// ProgressEvery is how often held work says it is still running.
//
// It is AliveEvery, deliberately the SAME NUMBER a running task is stamped at
// rather than a second one next to it. NFR-02's ceiling is 10 s for both, both
// are read by a client polling, and two constants that must agree and are
// written down twice are two constants that will eventually disagree — the
// reason AliveEvery exists at all is that FORGE_LEASE_HEARTBEAT was a different
// number tuned for something else.
//
// Why the value is 5 s and not 10 is argued where it is defined (worker.go): a
// report at most ProgressEvery old, read by a client polling at the same rate,
// is at most twice that old when it is seen, so the interval has to leave room
// for the poll. Changing AliveEvery changes this with it, which is the point.
const ProgressEvery = AliveEvery

// ProgressReport is one "still running" ping.
//
// ‼️ Two fields, both derived from elapsed time and the caller's own label.
// Nothing here is a guess about how far along the work is, and nothing may be
// added that would be — see the comment on Progress, and the fence that counts
// these fields.
type ProgressReport struct {
	// Elapsed is how long the work has been running, measured on Progress.Clock
	// from the moment the ticker started.
	Elapsed time.Duration
	// Summary is the one sentence a person reads, in the wording
	// `forgectl goal new` already prints to a terminal, so the two surfaces of
	// NFR-02 say the same thing in the same words.
	//
	// It carries the elapsed time IN TEXT because that is the only way it
	// reaches an HTTP client: httpapi.EventDTO exposes a timeline entry's
	// summary and not its payload.
	Summary string
}

// Start begins reporting and returns the function that stops it.
//
// The shape is the caller's `stop := p.Start(ctx); defer stop()` — the same
// shape as startElapsedTicker in forgectl and as runTask's heartbeat, so the
// three long-running reporters in this repository are read the same way.
//
// stop blocks until the reporting goroutine has returned, so no report can land
// after the work that owns it has finished. It is safe to call more than once,
// because a caller that stops on the happy path AND defers a stop is the
// correct way to use this and must not panic on the second close.
//
// The ticker dies with ctx as well. It holds no reference to anything the
// caller does not, and it cannot outlive the call it reports on: see the note
// on the emit context at the call site in internal/httpapi/goals_start.go —
// unlike a RECORD of something that happened (agent.outliving), a ping about
// work that has just been abandoned is a ping nobody wants.
func (p Progress) Start(ctx context.Context) func() {
	every := p.Every
	if every <= 0 {
		every = ProgressEvery
	}
	clk := p.Clock
	if clk == nil {
		clk = clock.System{}
	}
	started := clk.Now()

	done := make(chan struct{})
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		// A TICKER and not a timer reset per report: the interval is the
		// promise, and a report that took 400 ms to write must not push the
		// next one 400 ms further out. Over a 128 s plan that drift would
		// accumulate past the ceiling the interval was chosen to sit under.
		ticker := time.NewTicker(every)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if p.Emit == nil {
					continue
				}
				elapsed := clk.Now().Sub(started)
				rep := ProgressReport{
					Elapsed: elapsed,
					Summary: p.What + " … " + elapsed.Round(time.Second).String() + " elapsed",
				}
				if err := p.Emit(ctx, rep); err != nil && p.Log != nil {
					p.Log.WarnWith(ctx, p.Lost, err,
						"what", p.What, "elapsed", rep.Elapsed.String(),
						"detail", "a progress report was not written; the work is still running, "+
							"but anything watching it sees nothing until the next report lands")
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}
}
