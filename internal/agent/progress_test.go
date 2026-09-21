package agent_test

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The ticker for work that holds a caller and writes nothing while it runs
// (PRD NFR-02: "long jobs report progress at least every 10 s").
//
// The sibling fence in alive_test.go holds the other half — a task the worker
// is running. This one holds the half that never becomes a task row: planning,
// which runs inside the HTTP handler for over two minutes on a live model
// (GitHub issue 13) and, before agent.Progress existed, put nothing on the
// goal's timeline between goal.created and plan.created.
//
// These do not sleep for the production interval. The interval is a field so a
// fence can watch several reports inside a few hundred milliseconds, exactly as
// Worker.SetAliveEveryForTest lets alive_test.go do; the claim about the
// PRODUCTION number is arithmetic, and is asserted as arithmetic below.

// progressCollector records every report, so a fence can state both how many
// arrived and how far apart they really were.
type progressCollector struct {
	mu   sync.Mutex
	reps []agent.ProgressReport
	at   []time.Time
	err  error
}

func (c *progressCollector) emit(_ context.Context, rep agent.ProgressReport) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reps = append(c.reps, rep)
	c.at = append(c.at, time.Now())
	return c.err
}

func (c *progressCollector) reports() ([]agent.ProgressReport, []time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]agent.ProgressReport(nil), c.reps...), append([]time.Time(nil), c.at...)
}

func (c *progressCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reps)
}

func TestProgress_AJobHeldLongerThanNFR02sTenSecondsReportsInsideIt(t *testing.T) {
	// The production number first, because no amount of scaled-down ticking
	// proves anything about it. A report at most ProgressEvery old, read by a
	// client polling at the same rate, is at most twice that old when it is
	// seen — the same arithmetic AliveEvery is held to in alive_test.go.
	if 2*agent.ProgressEvery > 10*time.Second {
		t.Fatalf("agent.ProgressEvery is %s: a client polling at that rate can see a report %s old, "+
			"past NFR-02's 10 s", agent.ProgressEvery, 2*agent.ProgressEvery)
	}
	// ‼️ One number, not two. A running task's stamp and a held job's report are
	// read by the same client at the same rate against the same ceiling, and two
	// constants that must agree are two constants that will eventually not.
	if agent.ProgressEvery != agent.AliveEvery {
		t.Fatalf("agent.ProgressEvery is %s and agent.AliveEvery is %s. NFR-02 has one ceiling; "+
			"held work and running tasks must be reported at one interval, or tuning one silently "+
			"leaves the other behind", agent.ProgressEvery, agent.AliveEvery)
	}

	// Now that the interval is honoured at all: hold a job for ten intervals
	// and count what a watcher would have seen.
	const every = 30 * time.Millisecond
	const held = 10 * every

	c := &progressCollector{}
	stop := agent.Progress{
		What: "still planning", Every: every,
		Clock: clock.System{}, Log: logx.Discard(), Lost: logx.EventGoalPlanFailed,
		Emit: c.emit,
	}.Start(context.Background())

	// Stand in for the held model call. Real time, because the thing under test
	// is a real time.Ticker; the repository has no timer to fake, only a clock
	// (internal/platform/clock), which is what the elapsed fence below uses.
	time.Sleep(held)
	stop()

	reps, at := c.reports()
	// Ten intervals is ten reports; 4 leaves room for a loaded machine and is
	// still impossible for work that reported only when it finished.
	if len(reps) < 4 {
		t.Fatalf("a job held for %s at one report every %s produced %d report(s). Anything watching "+
			"it sees nothing for the whole call, however long it runs: NFR-02 asks for progress at "+
			"least every 10 s", held, every, len(reps))
	}

	// The gaps, in the units the interval was scaled from: no watcher should
	// ever wait longer than the poll-allowance the production number is chosen
	// for, which is twice the interval.
	worst := at[0].Sub(at[0])
	for i := 1; i < len(at); i++ {
		if gap := at[i].Sub(at[i-1]); gap > worst {
			worst = gap
		}
	}
	t.Logf("%d reports in %s at every=%s; worst gap between reports %s", len(reps), held, every, worst)

	// Each one says how long, and they only ever go up. A report whose elapsed
	// time did not move is a report that is not measuring anything.
	for i, rep := range reps {
		if rep.Elapsed <= 0 {
			t.Fatalf("report %d carries elapsed %s. A progress report that does not say how long the "+
				"work has been running says nothing a watcher can use", i, rep.Elapsed)
		}
		if i > 0 && rep.Elapsed <= reps[i-1].Elapsed {
			t.Fatalf("report %d says %s elapsed and report %d says %s: the elapsed time is not moving, "+
				"so it is not being measured", i-1, reps[i-1].Elapsed, i, rep.Elapsed)
		}
	}
}

func TestProgress_AProgressReportSaysOnlyHowLongItHasBeenRunning(t *testing.T) {
	// ‼️ The shape of the report, held on purpose. `forgectl goal new` states
	// the standard this fence enforces: "A fake progress bar would be worse
	// than silence — it would imply the command can see how far along the model
	// is, which it cannot." A percentage, a step count or an estimate added to
	// this struct would be exactly that, so the field list is fenced rather
	// than trusted to review.
	rt := reflect.TypeOf(agent.ProgressReport{})
	want := map[string]bool{"Elapsed": true, "Summary": true}
	for i := range rt.NumField() {
		if !want[rt.Field(i).Name] {
			t.Fatalf("agent.ProgressReport has a field %q. A progress report may carry only what the "+
				"ticker actually knows — how long the work has been running — and a field beyond "+
				"elapsed time and the sentence describing it can only be a guess about how far "+
				"along it is. A fake progress bar is worse than silence", rt.Field(i).Name)
		}
	}
	if rt.NumField() != len(want) {
		t.Fatalf("agent.ProgressReport has %d fields, expected %d (%v)", rt.NumField(), len(want), want)
	}

	// The elapsed time is measured on the caller's clock, not on the wall. A
	// fake clock is the only way to state what a report SHOULD carry, and it is
	// how this fence avoids sleeping for the seconds it asserts about.
	fake := clock.NewFake(time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	done := make(chan agent.ProgressReport, 4)
	stop := agent.Progress{
		What: "still planning", Every: 20 * time.Millisecond,
		Clock: fake, Log: logx.Discard(), Lost: logx.EventGoalPlanFailed,
		Emit: func(_ context.Context, rep agent.ProgressReport) error {
			select {
			case done <- rep:
			default:
			}
			// Move the fake on by an interval, so the NEXT report has a stated
			// elapsed time this fence can predict exactly.
			fake.Advance(agent.ProgressEvery)
			return nil
		},
	}.Start(context.Background())
	defer stop()

	var first agent.ProgressReport
	select {
	case first = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("no progress report arrived at all")
	}
	// The first report is emitted before the callback advances anything, so on a
	// clock that has not moved it is exactly zero. That is the point: the
	// elapsed time comes from Progress.Clock and from nowhere else. On the wall
	// clock it would be ~20 ms.
	if first.Elapsed != 0 {
		t.Fatalf("the first report says %s elapsed on a clock that has not moved. Elapsed time must "+
			"be measured on Progress.Clock, or no fence can ever state what a report should say",
			first.Elapsed)
	}

	var second agent.ProgressReport
	select {
	case second = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("only one progress report arrived")
	}
	if second.Elapsed != agent.ProgressEvery {
		t.Fatalf("the clock was moved on by %s between reports and the second says %s elapsed",
			agent.ProgressEvery, second.Elapsed)
	}

	// And the sentence a person reads carries that same number, because
	// httpapi.EventDTO exposes a timeline entry's summary and not its payload:
	// the text IS how elapsed time reaches an HTTP client.
	if !strings.Contains(second.Summary, "still planning") ||
		!strings.Contains(second.Summary, agent.ProgressEvery.Round(time.Second).String()) ||
		!strings.Contains(second.Summary, "elapsed") {
		t.Fatalf("a report at %s reads %q. It must name the work and say how long it has been "+
			"running, in words, because the summary is the only part of a timeline event an HTTP "+
			"client is given", agent.ProgressEvery, second.Summary)
	}
}

func TestProgress_TheTickerStopsWhenTheWorkDoes(t *testing.T) {
	const every = 20 * time.Millisecond

	t.Run("stopped by the caller", func(t *testing.T) {
		c := &progressCollector{}
		stop := agent.Progress{
			What: "still planning", Every: every,
			Clock: clock.System{}, Log: logx.Discard(), Lost: logx.EventGoalPlanFailed,
			Emit: c.emit,
		}.Start(context.Background())

		time.Sleep(5 * every)
		stop()
		// ‼️ Read IMMEDIATELY after stop returns. stop must block until the
		// reporting goroutine is gone, so that a report cannot land after the
		// work it describes has finished — a "still planning" line written
		// after plan.created is a timeline that contradicts itself.
		atStop := c.count()
		if atStop == 0 {
			t.Fatal("nothing was reported at all, so this proves nothing about stopping")
		}
		time.Sleep(5 * every)
		if after := c.count(); after != atStop {
			t.Fatalf("%d report(s) had been written when stop returned and %d after waiting another "+
				"%s. The ticker outlived the work it was reporting on, so the timeline gains "+
				"'still planning' entries for a plan that has already landed",
				atStop, after, 5*every)
		}
	})

	t.Run("stopped by the context", func(t *testing.T) {
		c := &progressCollector{}
		ctx, cancel := context.WithCancel(context.Background())
		stop := agent.Progress{
			What: "still planning", Every: every,
			Clock: clock.System{}, Log: logx.Discard(), Lost: logx.EventGoalPlanFailed,
			Emit: c.emit,
		}.Start(ctx)
		defer stop()

		time.Sleep(5 * every)
		before := c.count()
		if before == 0 {
			t.Fatal("nothing was reported at all, so this proves nothing about cancellation")
		}
		// A cancelled plan is a plan nobody is waiting on. The ticker must die
		// with its context rather than go on pinging about abandoned work —
		// this is why the emit runs on the planner's own context and not on
		// agent.outliving's.
		cancel()
		time.Sleep(5 * every)
		settled := c.count()
		time.Sleep(5 * every)
		if after := c.count(); after != settled {
			t.Fatalf("the context was cancelled and reports kept arriving (%d, then %d). A progress "+
				"report about work that has been abandoned is worse than the silence it was added "+
				"to fix", settled, after)
		}
	})
}

func TestProgress_AProgressReportThatCannotBeWrittenIsLoggedAndDoesNotStopTheWork(t *testing.T) {
	// The trade-off keepSaid makes in internal/httpapi/converse.go, made here
	// for the same reason: a write that failed must not fail the thing it is
	// reporting on, and must not be silent either.
	const every = 20 * time.Millisecond

	var logged strings.Builder
	var logMu sync.Mutex
	c := &progressCollector{err: errors.New("the timeline is unavailable")}

	stop := agent.Progress{
		What: "still planning", Every: every,
		Clock: clock.System{},
		Log: logx.New(logx.Options{Level: slog.LevelWarn, Format: "json",
			Output: &syncWriter{w: &logged, mu: &logMu}}),
		Lost: logx.EventGoalPlanFailed,
		Emit: c.emit,
	}.Start(context.Background())

	// The "work" carries on regardless — nothing about a failing report may
	// reach it, and in particular Start must not panic or block the caller.
	time.Sleep(6 * every)
	stop()

	// Every report was attempted, not abandoned after the first failure. The
	// next tick is the retry; giving up would mean one unavailable moment
	// silencing a two-minute plan for the rest of its life.
	if n := c.count(); n < 3 {
		t.Fatalf("%d report(s) were attempted in %s at every=%s. A failing write must not stop the "+
			"ticker: the work is still running and the next tick is the retry", n, 6*every, every)
	}

	logMu.Lock()
	out := logged.String()
	logMu.Unlock()
	if !strings.Contains(out, "the timeline is unavailable") {
		t.Fatalf("a progress report failed and the log says %q. A timeline with a hole in it looks "+
			"exactly like work that was never done, so a lost report must be greppable even though "+
			"it does not fail anything", out)
	}
	if !strings.Contains(out, string(logx.EventGoalPlanFailed)) {
		t.Fatalf("the lost report was logged without its caller's event name %q, so an operator "+
			"asking what went wrong for this goal cannot find it: %q", logx.EventGoalPlanFailed, out)
	}
}

// syncWriter serialises writes from the reporting goroutine so the fence can
// read the log without racing it.
type syncWriter struct {
	w  *strings.Builder
	mu *sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
