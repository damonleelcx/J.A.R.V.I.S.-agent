package agent

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
)

// Measurement for issue 13: the planner takes ~128 s against a 180 s timeout.
//
// # What this is and is not
//
// It is a measurement, not a fence. It always passes; its output is the point,
// so run it with -v. Asserting a wall-clock budget here would produce a test
// that goes red when somebody else's build is running on the same laptop, and a
// flaky test teaches people to ignore red.
//
// There are NO live model calls. The model is a stub that sleeps for an injected
// duration and returns a canned plan, so what is measured is FORGE's own code
// with the provider's latency held at a number we choose. That is the only way
// to answer the question issue 13 actually asks — "is the 128 s ours or theirs?"
// — because a live call measures the two added together and cannot separate them.
//
// # Why the 128 s figure is projected rather than slept
//
// Sleeping 128 s five times is 11 minutes of test suite for a number that is
// exactly (our cost + 128 s): time.Sleep is not the interesting variable. The
// projection is labelled as such everywhere it appears. Set
// FORGE_PLANNER_LATENCY_SOAK=1 to make one real 128 s sample happen and print
// the measured total beside the projection.
//
// The results are written up in docs/spikes/2026-09-20-planner-latency/README.md.

// latencyStub is an llm.Client that costs a fixed, known amount of time.
//
// It also keeps the assembled messages, because the prompt's SIZE is half of
// what issue 13 is about: a prompt is the one part of a model call's cost FORGE
// controls directly, and the only remedy available to us if the provider's
// thinking time is the rest.
type latencyStub struct {
	latency time.Duration
	plan    string

	system string
	user   string
	calls  int
}

func (s *latencyStub) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	s.calls++
	for _, m := range req.Messages {
		switch m.Role {
		case llm.System:
			s.system = m.Content
		case llm.User:
			s.user = m.Content
		}
	}
	if s.latency > 0 {
		select {
		case <-time.After(s.latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &llm.Response{Content: s.plan, FinishReason: "stop", Model: "stub-planner"}, nil
}

func (s *latencyStub) ModelFor(llm.Role) string { return "stub-planner" }

// approxTokens is chars/4. An APPROXIMATION and labelled as one everywhere it is
// printed: it is not a measurement, no tokenizer was run, and the real count for
// this prompt depends on the provider's vocabulary. It is here because "22k
// characters" means nothing to anyone reasoning about a context window and
// "~5.5k tokens" means something to everyone, and because the ratio between the
// three parts is what the spike needed, not the absolute number.
func approxTokens(s string) int    { return approxTokensOf(len(s)) }
func approxTokensOf(chars int) int { return chars / 4 }

// TestPlannerLatency_WhereTheWallClockOfAPlanActuallyGoes measures and prints;
// it asserts only that the harness measured the thing it claims to.
func TestPlannerLatency_WhereTheWallClockOfAPlanActuallyGoes(t *testing.T) {
	plan := canonicalPlanReply(t)
	goal := &engine.Goal{
		ID: "goal_latency", ProjectID: "prj_latency",
		Title:     "Build the reference bracket",
		Statement: "Design, analyse and document a mounting bracket for the reference assembly.",
		Autonomy:  engine.AutonomySandboxExecute, RiskTier: engine.RiskR1,
		CompletionCriteria: []engine.CompletionCriterion{
			{Statement: "the bracket carries 2 kN with a factor of safety of at least 2"},
			{Statement: "a STEP file and a drawing are exported"},
		},
	}

	// --- prompt size -------------------------------------------------------
	//
	// Measured by running one plan and reading what the stub was handed, rather
	// than by rebuilding the strings here: a measurement of a reconstruction is
	// a measurement of the reconstruction.
	sizer := &latencyStub{plan: plan}
	p := NewPlanner(sizer, persona.DefaultCharacter())
	if _, err := p.Plan(context.Background(), goal, nil, ""); err != nil {
		t.Fatalf("the measurement harness could not plan at all: %v", err)
	}
	// persona.SystemPrompt writes the persona, a "---" rule, then the framing, so
	// the framing is not a suffix and cannot be trimmed off the end. Split at
	// where the framing actually starts, and count the rule with the persona —
	// it is persona.SystemPrompt's byte, not the planner's.
	frameAt := strings.Index(sizer.system, plannerFraming)
	if frameAt < 0 {
		t.Fatalf("plannerFraming is not in the assembled system prompt at all; nothing below is a "+
			"measurement of this planner:\n%s", truncate(sizer.system, 400))
	}
	personaOnly := sizer.system[:frameAt]
	whole := len(sizer.system) + len(sizer.user)

	t.Logf("PROMPT SIZE (characters, and ~tokens at the chars/4 APPROXIMATION — no tokenizer was run)")
	t.Logf("  persona (identity, soul, voice, character) %6d chars  ~%5d tok  %4.1f%%",
		len(personaOnly), approxTokens(personaOnly), 100*float64(len(personaOnly))/float64(whole))
	t.Logf("  plannerFraming (static role framing)       %6d chars  ~%5d tok  %4.1f%%",
		len(plannerFraming), approxTokens(plannerFraming), 100*float64(len(plannerFraming))/float64(whole))
	t.Logf("  per-goal user message                      %6d chars  ~%5d tok  %4.1f%%",
		len(sizer.user), approxTokens(sizer.user), 100*float64(len(sizer.user))/float64(whole))
	t.Logf("  TOTAL assembled prompt                     %6d chars  ~%5d tok",
		whole, approxTokensOf(whole))

	// --- FORGE's own cost --------------------------------------------------
	//
	// Five samples, median and range reported rather than one figure, because
	// this laptop runs several builds at once and a single sample measures the
	// contention as much as the code.
	//
	// Each sample is a BATCH of plans, divided out, and that is not tidiness.
	// Go's monotonic clock on Windows advances in ~0.5–15.6 ms ticks, and one
	// plan costs far less than a tick — measured one at a time, every sample of
	// every stage reads exactly 0s, which is a measurement of the clock and not
	// of the code. Over a batch the ticks accumulate honestly.
	//
	// The split is taken at the boundary: the stub records when it was entered
	// and when it returned, so everything before is assembly and everything after
	// is unmarshal + derive + Validate + hazard coverage.
	const samples, reps = 5, 4000
	var assembly, after, total []time.Duration
	for i := 0; i < samples; i++ {
		st := &latencyStub{plan: plan}
		var entered, left time.Time
		timed := &timingStub{inner: st, entered: &entered, left: &left}
		planner := NewPlanner(timed, persona.DefaultCharacter())

		var assemblySum, afterSum time.Duration
		batchStart := time.Now()
		for r := 0; r < reps; r++ {
			start := time.Now()
			if _, err := planner.Plan(context.Background(), goal, nil, ""); err != nil {
				t.Fatalf("sample %d rep %d: %v", i, r, err)
			}
			end := time.Now()
			// Each term is quantised to a clock tick, so each is usually 0 and
			// occasionally one whole tick. Summed over the batch that is an
			// unbiased estimate of the real total; divided by reps it is the
			// per-plan cost, with the noise the range column reports.
			assemblySum += entered.Sub(start)
			afterSum += end.Sub(left)
		}
		batch := time.Since(batchStart)

		assembly = append(assembly, assemblySum/reps)
		after = append(after, afterSum/reps)
		total = append(total, batch/reps)
	}

	t.Logf("FORGE'S OWN CODE PER PLAN, model latency injected at 0 (%d samples of %d plans each, "+
		"divided out; this machine had other builds running)", samples, reps)
	logStat(t, "  prompt assembly            ", assembly)
	logStat(t, "  unmarshal+derive+Validate  ", after)
	logStat(t, "  everything that is not the model call", total)

	ours := median(total)
	const issue13 = 128 * time.Second
	t.Logf("PROJECTED at the issue's measured 128s model latency: total %s, of which FORGE is %s "+
		"(%.4f%%). PROJECTION, not a measurement: it is the median above plus a constant.",
		issue13+ours, ours, 100*float64(ours)/float64(issue13+ours))

	if os.Getenv("FORGE_PLANNER_LATENCY_SOAK") == "1" {
		st := &latencyStub{plan: plan, latency: issue13}
		planner := NewPlanner(st, persona.DefaultCharacter())
		start := time.Now()
		if _, err := planner.Plan(context.Background(), goal, nil, ""); err != nil {
			t.Fatalf("soak sample: %v", err)
		}
		measured := time.Since(start)
		t.Logf("SOAK (real, one sample, 128s injected): total %s, FORGE's share %s (%.4f%%)",
			measured, measured-issue13, 100*float64(measured-issue13)/float64(measured))
	} else {
		t.Logf("SOAK skipped. Set FORGE_PLANNER_LATENCY_SOAK=1 for one real 128s sample.")
	}

	// The only assertions: that the harness measured what it says it did.
	if sizer.calls != 1 {
		t.Errorf("the planner made %d model calls; every number above is per-plan and assumes one",
			sizer.calls)
	}
	if len(personaOnly) == 0 || len(personaOnly)+len(plannerFraming) > len(sizer.system) {
		t.Error("the system prompt is not persona then framing, so the split printed above is fiction")
	}
	if median(total) <= 0 {
		t.Error("the harness measured no time at all in FORGE's code, which cannot be right")
	}
}

// timingStub wraps a client and records when the call was entered and left, so
// the split between assembly and post-processing is taken at the boundary rather
// than guessed at.
type timingStub struct {
	inner         llm.Client
	entered, left *time.Time
}

func (s *timingStub) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	*s.entered = time.Now()
	resp, err := s.inner.Complete(ctx, req)
	*s.left = time.Now()
	return resp, err
}

func (s *timingStub) ModelFor(r llm.Role) string { return s.inner.ModelFor(r) }

// canonicalPlanReply is a plan of the shape this work produces: a fan-out of
// independent surveys joined by a synthesis, with needs/produces filled in, so
// the post-call measurement includes a derivation that actually does work.
func canonicalPlanReply(t *testing.T) string {
	t.Helper()
	tasks := []PlannedTask{
		{Key: "survey-loads", Title: "Survey loads", Instruction: "Establish the load cases.",
			Produces: []string{"loads"}, Needs: &[]string{}},
		{Key: "survey-materials", Title: "Survey materials", Instruction: "Choose candidate materials.",
			Produces: []string{"materials"}, Needs: &[]string{}, DependsOn: []string{"survey-loads"}},
		{Key: "survey-interfaces", Title: "Survey interfaces", Instruction: "Record the bolt pattern.",
			Produces: []string{"interfaces"}, Needs: &[]string{}, DependsOn: []string{"survey-materials"}},
		{Key: "model", Title: "Model the bracket", Instruction: "Build the solid.",
			Produces: []string{"solid"}, Needs: &[]string{"loads", "materials", "interfaces"}},
		{Key: "export", Title: "Export", Instruction: "Write STEP and the drawing.",
			Produces: []string{"step", "drawing"}, Needs: &[]string{"solid"}},
	}
	b, err := json.Marshal(PlanResult{Rationale: "fan out, then join", Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func logStat(t *testing.T, label string, ds []time.Duration) {
	t.Helper()
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	t.Logf("%s median %-12s range %s … %s", label, median(ds), s[0], s[len(s)-1])
}
