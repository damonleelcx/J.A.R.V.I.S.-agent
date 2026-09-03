// Package eval measures how the MODEL behaves inside the harness (PRD §7,
// "evaluation suites"; README phase 7).
//
// # What this is for, and how it differs from every other test in this repo
//
// The test suite proves the harness is correct: the engine, the schema, the
// fences. It cannot prove that the model behaves acceptably inside it — and that
// is where this codebase has actually been hurt. Every case in cases.go exists
// because of a specific observed defect:
//
//	a fabricated NEMA 17 bolt pattern, stated as a standard's figure
//	(docs/bugfix/2026-09-02-fabricated-standards-figures.md)
//	dimensions that travelled without their unit (PRD WRK-05)
//	geometry offered with nothing said about what it does not establish (VIS-06)
//	part ids that changed between turns, so two proposals of one bracket read as
//	two unrelated designs (wave 7)
//
// # The rule the whole package turns on
//
// **Nothing grades its own homework.** Every scorer is deterministic Go over the
// reply. There is no model judging a model anywhere in here, and there is no
// place to put one. Where a scorer needs a fact about the world — the published
// dimensions of a NEMA 17 face — that fact is written down HERE, with its source
// named, exactly as the Zoo spike wrote its reference figures into analyse.py
// rather than taking them from the thing it was measuring.
//
// # Why the output is a RATE and not a pass
//
// A model is not deterministic. The same prompt produced a correct bolt pattern
// and a fabricated one in the same spike, four runs apart. So a single run
// proves nothing in either direction, and reporting "PASS" from one would be the
// most misleading thing this package could do. Every case runs N times and the
// report is k/n per property, with the raw replies kept so a person can re-judge
// the scoring rather than taking it on trust.
//
// Floors turn those rates into an exit status, because a suite nothing can fail
// is decorative. Each floor is written down with the measurement it came from —
// they are observations, not targets.
package eval

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Case is one fixed conversation, run repeatedly.
type Case struct {
	ID string `json:"id"`
	// Why names the defect this case exists because of. Not decoration: a case
	// nobody can trace to a real failure is one that gets deleted the first time
	// it is inconvenient, and a suite of those measures nothing.
	Why string
	// Turns are the user's messages, in order. More than one where the property
	// only exists ACROSS turns — part-id stability cannot be observed in a
	// single reply.
	Turns   []string `json:"turns"`
	Scorers []Scorer `json:"scorers"`
}

// Scorer is one deterministic judgement about a run.
type Scorer struct {
	Name string `json:"name"`
	// Asserts is the property in words, as a reader of the report needs it.
	Asserts string `json:"asserts"`
	// Floor is the fraction of runs that must satisfy this, in [0,1]. Ignored
	// when Tracked.
	//
	// Below it the suite exits non-zero. Above it nothing is claimed: a rate of
	// 1.0 over four runs is four runs, not a guarantee.
	Floor float64 `json:"floor"`
	// Tracked marks a property this build MEASURES but does not require.
	//
	// # Why the distinction is a field rather than a floor of zero
	//
	// Some properties are requirements and some are observations the design
	// already accounts for. Part-id stability is the second kind: the comparison
	// view has a name fallback precisely because the model does not hold it, so
	// requiring it would be demanding something the design assumes is absent —
	// and the suite would sit permanently red until somebody lowered the number
	// to make the red go away. That is how every floor in a suite eventually
	// becomes meaningless.
	//
	// A floor of zero would express the same thing and read as a broken
	// requirement. Named, it reads as what it is, and ScorersAreFlooredOrTracked
	// stops a scorer acquiring a zero floor by accident.
	Tracked bool `json:"tracked"`
	// FloorWhy is where the number came from. A floor with no measurement behind
	// it is a target dressed as an observation, and the first time it fails
	// somebody will lower it because nobody can say why it was there.
	FloorWhy string `json:"floor_why"`
	// Judge returns whether the property held, and what was observed either way.
	//
	// The detail is required on success as well as failure. A scorer that only
	// explains itself when it fails gives a reader nothing to sanity-check when
	// it passes, which is how a vacuous scorer survives — the same rule the
	// recovery drills follow.
	//
	// Not serialised: a report is read and archived, and a function is neither.
	Judge func(o *Observation) (held bool, detail string) `json:"-"`
}

// Observation is one run of one case: everything the model said, and what it
// cost.
type Observation struct {
	Case string `json:"case"`
	Run  int    `json:"run"`
	// Replies is one per turn, in order. Shorter than the case's turns when the
	// run failed part-way.
	Replies []*agent.Reply `json:"replies"`
	// Err is rendered as text: an error is not JSON, and a report that dropped
	// it would show a run with no replies and no reason.
	Err     error         `json:"-"`
	ErrText string        `json:"error,omitempty"`
	Elapsed time.Duration `json:"elapsed_ns"`
}

// Reply returns the reply to turn i, or nil.
func (o *Observation) Reply(i int) *agent.Reply {
	if i < 0 || i >= len(o.Replies) {
		return nil
	}
	return o.Replies[i]
}

// Last returns the final reply, or nil.
func (o *Observation) Last() *agent.Reply { return o.Reply(len(o.Replies) - 1) }

// Score is one scorer's result across every run of a case.
type Score struct {
	Scorer Scorer `json:"scorer"`
	// Runs is how many runs the scorer was applied to — runs that ERRORED are
	// excluded, and counted separately. Scoring a failed request as a failed
	// property would blame the model for a network timeout.
	Runs   int      `json:"runs"`
	Held   int      `json:"held"`
	Detail []string `json:"detail"`
}

// Rate is the fraction of scored runs where the property held.
//
// Zero scored runs returns 0, and Met reports false: a case that could not be
// exercised has not demonstrated anything, and reporting 100% of nothing is the
// vacuous-pass failure this package is arranged against.
func (s *Score) Rate() float64 {
	if s.Runs == 0 {
		return 0
	}
	return float64(s.Held) / float64(s.Runs)
}

// Met reports whether this scorer is satisfied.
//
// A TRACKED scorer is satisfied by having been measured at all: it reports a
// rate and never fails the run. A scorer that was never applied is satisfied by
// neither — zero runs have demonstrated nothing, and reporting 100% of nothing
// is the vacuous pass this package is arranged against.
func (s *Score) Met() bool {
	if s.Runs == 0 {
		return false
	}
	if s.Scorer.Tracked {
		return true
	}
	return s.Rate() >= s.Scorer.Floor
}

// CaseReport is one case's outcome.
type CaseReport struct {
	Case Case `json:"case"`
	// Errors is how many runs could not be completed at all. Kept apart from the
	// scores: a model that timed out is a different finding from a model that
	// answered badly, and merging them would let an outage read as a regression.
	Errors       int           `json:"errors"`
	ErrorDetail  []string      `json:"error_detail,omitempty"`
	Scores       []Score       `json:"scores"`
	Observations []Observation `json:"observations"`
}

// Met reports whether every scorer reached its floor.
func (c *CaseReport) Met() bool {
	for i := range c.Scores {
		if !c.Scores[i].Met() {
			return false
		}
	}
	return len(c.Scores) > 0
}

// Report is a whole run of the suite.
type Report struct {
	Model    string        `json:"model"`
	Repeats  int           `json:"repeats"`
	Started  time.Time     `json:"started"`
	Elapsed  time.Duration `json:"elapsed_ns"`
	Cases    []CaseReport  `json:"cases"`
	Tokens   int64         `json:"tokens"`
	Requests int           `json:"requests"`
}

// Met reports whether every case met every floor.
func (r *Report) Met() bool {
	for i := range r.Cases {
		if !r.Cases[i].Met() {
			return false
		}
	}
	return len(r.Cases) > 0
}

// Runner executes cases against a real model.
//
// A real one, deliberately. A fake that returns canned replies would measure the
// fake — and this repository has already been caught by exactly that: a stub
// matching the provider's shape hid five shipped defects. There is no fake
// client in this package and no way to inject one.
type Runner struct {
	conv    *agent.Conversation
	model   string
	repeats int
	// OnProgress is called as each run completes, so a command can print
	// something during the minutes this takes rather than going silent.
	OnProgress func(caseID string, run, of int, err error)
}

// NewRunner wires the suite to a model client.
func NewRunner(client llm.Client, repeats int) (*Runner, error) {
	const op = "eval.NewRunner"

	if client == nil {
		return nil, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("evaluation needs a real model: it measures how the model behaves inside the " +
				"harness, and a stub would measure the stub. Set FORGE_LLM_API_KEY and try again.")
	}
	if repeats < 1 {
		repeats = 1
	}
	return &Runner{
		conv:    agent.NewConversation(client, persona.DefaultCharacter()),
		model:   client.ModelFor(llm.RoleConverse),
		repeats: repeats,
	}, nil
}

// Run executes the named cases, or all of them.
func (r *Runner) Run(ctx context.Context, only []string) (*Report, error) {
	cases, err := Select(only)
	if err != nil {
		return nil, err
	}
	report := &Report{Model: r.model, Repeats: r.repeats, Started: time.Now()}
	start := time.Now()

	for _, c := range cases {
		cr := CaseReport{Case: c}
		for run := 1; run <= r.repeats; run++ {
			obs := r.once(ctx, c, run)
			report.Requests += len(obs.Replies)
			for _, rep := range obs.Replies {
				report.Tokens += rep.Usage.TotalTokens
			}
			if r.OnProgress != nil {
				r.OnProgress(c.ID, run, r.repeats, obs.Err)
			}
			if obs.Err != nil {
				// Carried as text as well, because a report is archived and read
				// later: an error is not JSON, and a run with no replies and no
				// reason is indistinguishable from a run that returned nothing.
				obs.ErrText = obs.Err.Error()
			}
			cr.Observations = append(cr.Observations, obs)
			if obs.Err != nil {
				cr.Errors++
				cr.ErrorDetail = append(cr.ErrorDetail, fmt.Sprintf("run %d: %v", run, obs.Err))
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		cr.Scores = score(c, cr.Observations)
		report.Cases = append(report.Cases, cr)
	}
	report.Elapsed = time.Since(start)
	return report, nil
}

// once runs one case one time, carrying the conversation forward between turns.
func (r *Runner) once(ctx context.Context, c Case, run int) Observation {
	obs := Observation{Case: c.ID, Run: run}
	start := time.Now()

	var history []agent.Turn
	for _, message := range c.Turns {
		// No project: the evaluation harness measures FORGE at its default
		// character, so a run's score cannot drift with somebody's setting.
		reply, err := r.conv.Respond(ctx, "", history, message, onScreen(obs.Last()))
		if err != nil {
			obs.Err = err
			break
		}
		obs.Replies = append(obs.Replies, reply)
		history = append(history,
			agent.Turn{Role: "user", Content: message},
			agent.Turn{Role: "forge", Content: reply.Speech})
	}
	obs.Elapsed = time.Since(start)
	return obs
}

// onScreen describes what the workspace would be showing, so a second turn
// resolves "make it thicker" against the thing on screen exactly as the workbench
// does (PRD WRK-02).
//
// Reconstructed here rather than left empty: an eval that gives the model less
// context than the product does is measuring a different system.
func onScreen(prev *agent.Reply) string {
	if prev == nil || prev.Prototype == nil {
		return ""
	}
	p := prev.Prototype
	parts := make([]string, 0, len(p.Parts))
	for _, part := range p.Parts {
		// The IDS, not only the names. converse.go asks the model to keep part
		// ids stable across turns, and until this was added it had never been
		// shown the ids it was being asked to reuse — which is what
		// partIDsSurviveARevision measures.
		parts = append(parts, fmt.Sprintf("%s [id: %s]", part.Label(), part.ID))
	}
	units := p.Units
	if units == "" {
		units = "NOT STATED — every dimension here is unitless"
	}
	return fmt.Sprintf("%s — %d part(s): %s (units: %s). Keep these part ids when you revise it.",
		p.Name, len(p.Parts), strings.Join(parts, ", "), units)
}

// score applies every scorer to every completed run.
func score(c Case, obs []Observation) []Score {
	out := make([]Score, 0, len(c.Scorers))
	for _, s := range c.Scorers {
		sc := Score{Scorer: s}
		for i := range obs {
			o := &obs[i]
			if o.Err != nil {
				// Excluded, not failed. A request that never completed says
				// nothing about the property being measured.
				continue
			}
			sc.Runs++
			held, detail := s.Judge(o)
			if held {
				sc.Held++
			}
			sc.Detail = append(sc.Detail, fmt.Sprintf("run %d: %s %s",
				o.Run, mark(held), detail))
		}
		out = append(out, sc)
	}
	return out
}

func mark(held bool) string {
	if held {
		return "held —"
	}
	return "DID NOT HOLD —"
}

// Select resolves case ids, refusing an unknown one.
//
// Refusing rather than ignoring: a typo in --only that silently ran nothing
// would report a green suite that measured zero cases.
func Select(only []string) ([]Case, error) {
	all := Cases()
	if len(only) == 0 {
		return all, nil
	}
	byID := make(map[string]Case, len(all))
	names := make([]string, 0, len(all))
	for _, c := range all {
		byID[c.ID] = c
		names = append(names, c.ID)
	}
	out := make([]Case, 0, len(only))
	for _, id := range only {
		c, ok := byID[strings.TrimSpace(id)]
		if !ok {
			sort.Strings(names)
			return nil, errs.New("eval.Select", errs.CodeValidationFailed).
				WithDetail("no evaluation case %q. Registered cases are: %s.", id, strings.Join(names, ", "))
		}
		out = append(out, c)
	}
	return out, nil
}
