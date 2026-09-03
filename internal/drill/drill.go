// Package drill runs recovery drills against a live database (PRD NFR-07).
//
// # What NFR-07 asks for, and why it needs a harness
//
// "Graceful degradation — preserve state, stop dependents safely, expose partial
// results, never imply completion." Four properties, all of which are true right
// up until something breaks, which is exactly when nobody is watching.
//
// A unit test can assert them in the happy path. What it cannot do is assert
// them AFTER a fault, because the fault has to be real: a worker that actually
// dies holding a lease, a dependency that actually fails, a goal that actually
// settles with work outstanding. So this injects faults into a real schema and
// checks what survives.
//
// # The rule that makes a drill worth running
//
// **A scenario must prove its fault landed before it may assert anything.** The
// failure mode of every drill harness is a scenario whose injection silently did
// nothing — the mutation missed, the row was already in that state, the query
// matched zero rows — after which every invariant passes and the report is a
// page of green ticks about a system nobody disturbed.
//
// So Result.FaultInjected is not set by the scenario saying so. It carries the
// evidence that proved it, and a scenario returning no evidence FAILS, however
// many invariants it went on to satisfy.
package drill

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Check is one invariant, and whether it held.
type Check struct {
	// Name is the property in the words NFR-07 uses.
	Name string
	Held bool
	// Detail says what was observed. Present whether it held or not: a check
	// that only explains itself on failure gives a reader nothing to sanity-check
	// when it passes, which is how a vacuous check survives.
	Detail string
}

// Result is one scenario's outcome.
type Result struct {
	Scenario string
	// FaultEvidence is what proved the fault actually happened. Empty means the
	// scenario did not disturb anything, and the run FAILS regardless of the
	// checks below.
	FaultEvidence string
	Checks        []Check
	Duration      time.Duration
	// Err is a scenario that could not run at all, as opposed to one whose
	// invariants did not hold. The two need different responses: a broken drill
	// is a broken drill, and a failing drill is a finding.
	Err error
}

// FaultInjected reports whether the scenario proved it disturbed anything.
func (r *Result) FaultInjected() bool { return strings.TrimSpace(r.FaultEvidence) != "" }

// Passed reports whether this drill actually demonstrated anything.
//
// Three conditions, and the first is the one that matters: a scenario that ran
// cleanly and injected nothing has proved nothing, and reporting it as a pass is
// how a suite of drills becomes decorative.
func (r *Result) Passed() bool {
	if r.Err != nil || !r.FaultInjected() {
		return false
	}
	for _, c := range r.Checks {
		if !c.Held {
			return false
		}
	}
	return true
}

// Summary is one line.
func (r *Result) Summary() string {
	switch {
	case r.Err != nil:
		return fmt.Sprintf("%-32s ERROR    %v", r.Scenario, r.Err)
	case !r.FaultInjected():
		return fmt.Sprintf("%-32s VACUOUS  the fault was never proved to land, so its %d check(s) mean nothing",
			r.Scenario, len(r.Checks))
	case r.Passed():
		return fmt.Sprintf("%-32s ok       %d invariant(s) held after: %s",
			r.Scenario, len(r.Checks), r.FaultEvidence)
	default:
		var broken []string
		for _, c := range r.Checks {
			if !c.Held {
				broken = append(broken, c.Name)
			}
		}
		return fmt.Sprintf("%-32s FAILED   %s", r.Scenario, strings.Join(broken, "; "))
	}
}

// Scenario is one fault and the invariants that must survive it.
type Scenario struct {
	Name string
	// Describes the fault and what must remain true, for the report and for
	// anyone deciding whether the drill is worth trusting.
	Describes string
	Run       func(ctx context.Context, h *Harness) (*Result, error)
}

// Harness is what a scenario is given: a pool on a schema of its own, and a
// clock it controls.
type Harness struct {
	Pool *db.Pool
	// Now is the drill's clock. Scenarios advance it rather than sleeping —
	// a lease expiry drill that waited out a real lease would take minutes and
	// would be skipped in CI, which is the same as not existing.
	Now time.Time

	// Fixture ids, seeded per scenario.
	UserID    string
	ProjectID string
	GoalID    string
	PlanID    string
}

// Advance moves the drill clock.
func (h *Harness) Advance(d time.Duration) { h.Now = h.Now.Add(d) }

// Report is a whole run.
type Report struct {
	Results []Result
	Started time.Time
}

// Passed reports whether every scenario demonstrated its invariants.
func (r *Report) Passed() bool {
	if len(r.Results) == 0 {
		// An empty run is a failure, not a pass. A drill suite that selected
		// nothing and exited 0 is the most expensive kind of green.
		return false
	}
	for i := range r.Results {
		if !r.Results[i].Passed() {
			return false
		}
	}
	return true
}

// Summary is the whole run in one line.
func (r *Report) Summary() string {
	passed, vacuous, failed, errored := 0, 0, 0, 0
	for i := range r.Results {
		switch {
		case r.Results[i].Err != nil:
			errored++
		case !r.Results[i].FaultInjected():
			vacuous++
		case r.Results[i].Passed():
			passed++
		default:
			failed++
		}
	}
	if len(r.Results) == 0 {
		return "no scenarios ran — an empty drill run is a failure, not a pass"
	}
	out := fmt.Sprintf("%d scenario(s): %d passed", len(r.Results), passed)
	if failed > 0 {
		out += fmt.Sprintf(", %d FAILED", failed)
	}
	if vacuous > 0 {
		out += fmt.Sprintf(", %d VACUOUS (the fault never landed)", vacuous)
	}
	if errored > 0 {
		out += fmt.Sprintf(", %d could not run", errored)
	}
	return out
}

// registry holds the scenarios, by name.
var registry = map[string]Scenario{}

// Register adds a scenario. Called from init in the scenario files.
func Register(s Scenario) {
	if _, dup := registry[s.Name]; dup {
		panic("drill: two scenarios named " + s.Name)
	}
	registry[s.Name] = s
}

// Scenarios returns every registered scenario, by name.
func Scenarios() []Scenario {
	out := make([]Scenario, 0, len(registry))
	for _, s := range registry {
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns one scenario.
func Lookup(name string) (Scenario, error) {
	s, ok := registry[name]
	if !ok {
		names := make([]string, 0, len(registry))
		for n := range registry {
			names = append(names, n)
		}
		sort.Strings(names)
		return Scenario{}, errs.New("drill.Lookup", errs.CodeValidationFailed).
			WithDetail("no drill named %q; the drills are %s", name, strings.Join(names, ", "))
	}
	return s, nil
}

// check is a small helper so a scenario reads as a list of invariants.
func check(name string, held bool, format string, args ...any) Check {
	return Check{Name: name, Held: held, Detail: fmt.Sprintf(format, args...)}
}
