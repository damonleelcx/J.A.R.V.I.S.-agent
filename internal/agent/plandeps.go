package agent

import (
	"fmt"
	"sort"
	"strings"
)

// Dependency derivation: turning "what this task reads and writes" into edges.
//
// # What was wrong
//
// Every plan came back a straight line. The framing asked the model to "declare
// dependencies honestly", the schema offered `depends_on`, and nothing else in
// the system had an opinion — so the planner wrote the tasks in the order it
// thought of them and made each one depend on the previous, because that is what
// a numbered list looks like from the inside. The execution side was genuinely
// concurrent the whole time (the claim query takes one ready row with SKIP
// LOCKED, PromoteReadyTasks gates only on forge_task_deps, and the worker runs
// FORGE_WORKER_CONCURRENCY goroutines), so a four-task plan that could have
// finished in one model call's time took four. The DAG was decorative.
// internal/eval/scorers.go's someTasksCanStartAtOnce() measures exactly this.
//
// # The rule, exactly
//
// Two new fields carry what the model actually knows: `produces`, the stable
// artifact keys a task leaves behind, and `needs`, the keys it reads. Then:
//
//	For each task B that DECLARED a `needs` list (present, possibly empty):
//	    depends_on(B) := { A.key : A.produces ∩ B.needs ≠ ∅ }   sorted, deduped,
//	                                                            self excluded
//	For each task B that declared NO `needs` field:
//	    depends_on(B) := whatever the model wrote, unchanged
//	If the derived graph contains a cycle, the WHOLE derivation is discarded and
//	every task keeps the model's declared edges.
//
// # Why it is conservative, and why it must stay so
//
// `Needs` is a *[]string and not a []string, and that is the entire safety
// argument. "The model did not mention needs" and "the model said this task
// needs nothing" are opposite claims that both decode to an empty slice, and
// only the second one is permission to throw away an edge. A planner — a model,
// a prompt revision, a different provider, buildgoal.go's hand-built chains —
// that ignores these fields therefore gets today's behaviour byte for byte. Only
// a task that explicitly says what it consumes has its edges recomputed.
//
// The all-or-nothing cycle rule is the same instinct. A derivation that would
// deadlock the goal is not partially applied and not repaired: it is dropped,
// and PlanResult.Validate then judges the model's own graph on its own merits,
// exactly as it did before this file existed.
//
// # What this deliberately does not do
//
// It does not invent `needs` from prose, filenames or instruction text; a
// heuristic that guesses at consumption would drop real edges on the strength of
// a regex. It does not add edges to tasks that stayed quiet. It does not
// validate anything — Validate still runs afterwards on the final graph and
// still refuses self-dependencies, unknown keys and cycles. And it is not
// silent: what it changed comes back in a Derivation, which Plan records.

// DerivedEdge is one dependency edge, named by task key.
type DerivedEdge struct {
	// Task is the dependent, DependsOn the prerequisite: Task waits for DependsOn.
	Task      string
	DependsOn string
}

func (e DerivedEdge) String() string { return e.Task + " → " + e.DependsOn }

// Derivation is the record of what deriveDependencies changed.
//
// Returned rather than logged from inside, because this package's planner may
// have no logger (nil is a legal Planner.log, forgectl builds one that way) and
// a change to the shape of a plan that happens only when somebody configured a
// logger is a change that happens silently in the deployments least able to
// debug it. Plan writes it into the rationale AND logs it.
type Derivation struct {
	// Considered is how many tasks declared a `needs` list and so had their
	// edges recomputed. Zero means this did nothing at all, which is the
	// expected reading for any planner that has not been taught the fields.
	Considered int
	// Added and Dropped are the difference between what the model wrote and what
	// the artifact keys imply. Dropped is the interesting one: those are the
	// edges that were costing wall-clock time for nothing.
	Added   []DerivedEdge
	Dropped []DerivedEdge
	// Cycle is non-nil when the derived graph would have deadlocked. The whole
	// derivation was then discarded and Added/Dropped are empty — they describe
	// what was applied, not what was contemplated.
	Cycle []string
}

// Changed reports whether the plan's edges are different from the model's.
func (d Derivation) Changed() bool { return len(d.Added) > 0 || len(d.Dropped) > 0 }

// Summary is one sentence for a log line and for the plan's rationale.
//
// Always says something, including "nothing", because "the derivation ran and
// changed nothing" and "the derivation never ran" are the two readings a person
// debugging a linear plan needs to tell apart.
func (d Derivation) Summary() string {
	if len(d.Cycle) > 0 {
		return fmt.Sprintf("Dependency derivation was DISCARDED: the artifact keys imply a cycle (%s), "+
			"so the planner's own declared edges were kept and validated instead.",
			strings.Join(d.Cycle, " → "))
	}
	if d.Considered == 0 {
		return "Dependency derivation did not run: no task declared a \"needs\" list, so every " +
			"\"depends_on\" is the planner's own."
	}
	if !d.Changed() {
		return fmt.Sprintf("Dependency derivation checked %d task(s) against what they declared they "+
			"need and produce, and agreed with every edge the planner wrote.", d.Considered)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Dependency derivation recomputed %d task(s) from their \"needs\"/\"produces\" keys: ",
		d.Considered)
	if len(d.Dropped) > 0 {
		fmt.Fprintf(&b, "dropped %d edge(s) [%s]", len(d.Dropped), joinEdges(d.Dropped))
		if len(d.Added) > 0 {
			b.WriteString("; ")
		}
	}
	if len(d.Added) > 0 {
		fmt.Fprintf(&b, "added %d edge(s) [%s]", len(d.Added), joinEdges(d.Added))
	}
	b.WriteString(". A dropped edge means those tasks may now run at the same time.")
	return b.String()
}

func joinEdges(es []DerivedEdge) string {
	parts := make([]string, 0, len(es))
	for _, e := range es {
		parts = append(parts, e.String())
	}
	return strings.Join(parts, ", ")
}

// deriveDependencies rewrites depends_on from the artifact keys tasks declared.
//
// Pure: no clock, no database, no model, no logging. Returns a new slice; the
// input is not mutated, so a caller that keeps the model's reply around still
// has the model's reply.
func deriveDependencies(tasks []PlannedTask) ([]PlannedTask, Derivation) {
	var d Derivation

	out := make([]PlannedTask, len(tasks))
	copy(out, tasks)
	if len(tasks) == 0 {
		return out, d
	}

	// producers maps an artifact key to the task keys that claim to produce it.
	// A key produced by two tasks makes both of them prerequisites, which is the
	// honest reading: a consumer that does not say which one it meant needs
	// whichever finishes last.
	producers := map[string][]string{}
	for _, t := range tasks {
		if t.Key == "" {
			// Validate refuses this a moment later with a better message. Here it
			// would only produce edges pointing at "".
			continue
		}
		for _, a := range t.Produces {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			producers[a] = append(producers[a], t.Key)
		}
	}

	derived := make([][]string, len(tasks))
	for i, t := range tasks {
		if t.Needs == nil {
			// Declared nothing. Keep the model's edges verbatim — this is the
			// branch every planner that has not been taught the fields takes, and
			// it is why upgrading changes nothing.
			derived[i] = t.DependsOn
			continue
		}
		d.Considered++
		seen := map[string]bool{}
		var deps []string
		for _, a := range *t.Needs {
			for _, producer := range producers[strings.TrimSpace(a)] {
				if producer == t.Key || seen[producer] {
					continue
				}
				seen[producer] = true
				deps = append(deps, producer)
			}
		}
		sort.Strings(deps)
		derived[i] = deps
	}

	// Compare before committing, so the report describes the model's graph
	// against the derived one rather than against a half-applied mixture.
	for i, t := range tasks {
		if t.Needs == nil {
			continue
		}
		was, now := set(t.DependsOn), set(derived[i])
		for dep := range was {
			if !now[dep] {
				d.Dropped = append(d.Dropped, DerivedEdge{Task: t.Key, DependsOn: dep})
			}
		}
		for dep := range now {
			if !was[dep] {
				d.Added = append(d.Added, DerivedEdge{Task: t.Key, DependsOn: dep})
			}
		}
	}
	sortEdges(d.Dropped)
	sortEdges(d.Added)

	for i := range out {
		out[i].DependsOn = derived[i]
	}

	// All or nothing. A derived cycle is not the model's fault and not a defect
	// to repair by guessing which edge to cut — it is a signal that the artifact
	// keys do not describe a DAG, and the safe answer is to stop using them.
	if cycle := findCycle(out); cycle != nil {
		copy(out, tasks)
		return out, Derivation{Considered: d.Considered, Cycle: cycle}
	}
	return out, d
}

func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// sortEdges makes the report stable, so two runs over the same plan produce the
// same log line and the same rationale. Map iteration above is not ordered.
func sortEdges(es []DerivedEdge) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Task != es[j].Task {
			return es[i].Task < es[j].Task
		}
		return es[i].DependsOn < es[j].DependsOn
	})
}
