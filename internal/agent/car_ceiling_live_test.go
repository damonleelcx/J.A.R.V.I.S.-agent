package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// TestLiveCarCeiling measures WHERE the current build stops, rather than whether
// it passes.
//
// # Why this exists when TestLiveAssemble already builds a car
//
// TestLiveAssemble asserts the build beat one reply — more than six parts, no
// faults — and that is the right assertion for a regression test. It answers
// none of the questions asked of it on 2026-09-12: how far does this actually
// go, and which of the limits in
// docs/research-2026-09-12-vehicles-aircraft-and-structures.md is the one that
// bites first. Two of its blind spots matter enough to name:
//
//   - it checks doc.Faults(), which is the DOCUMENT's opinion of itself. Stage 11
//     measured 6 of 10 scripts "running" and 0 of 10 surviving to the export, so
//     a document that reports no faults is not a model that builds. This runs the
//     real kernel over the finished assembly when one is configured.
//   - a car whose parts sit inside each other has no fault and looks fine in a
//     part count. Nothing in this system performs an interference test
//     (geometry/assembly.go), so this reports a bounding-box proxy for it and
//     says, in the name of the line it prints, that a proxy is what it is.
//
// Everything after the build is computed from the finished document and costs no
// further model call, which is why this measures nine things for the price of the
// one build it was already going to run.
//
// # The spend ceiling is part of the measurement, not a nicety
//
// This endpoint is a shared weekly token plan, and one earlier live spike on it
// spent the week in eighteen calls. So every call goes through a counter that
// REFUSES to place one once the budget is gone: the build then degrades exactly
// as it does when the provider is down — each remaining step reports it could not
// be built, the passes already made are kept, and the measurement is of a partial
// car rather than of nothing. FORGE_MEASURE_TOKEN_BUDGET sets it; the default is
// deliberately low enough to survive a surprise.
//
// Run it with: make measure-car
func TestLiveCarCeiling(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live car ceiling measurement")
	}
	// 300k: the ceiling damon approved for the Phase 2 live milestone (A4, 2026-09-15).
	budget := int64(300_000)
	if s := os.Getenv("FORGE_MEASURE_TOKEN_BUDGET"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			t.Fatalf("FORGE_MEASURE_TOKEN_BUDGET must be a positive number of tokens, got %q", s)
		}
		budget = n
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "car-ceiling"})

	// The kernel is OPTIONAL and its absence is reported rather than skipped
	// over: a run without it still measures eight of the nine things, and a run
	// that silently omitted the build check would read as a car that builds.
	var kernel *cad.Kernel
	if python := os.Getenv("FORGE_CAD_PYTHON"); python != "" {
		kernel = cad.New(python, log).WithScripts(true)
		defer kernel.Close()
	}

	inner := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		Vision:         envOrDefault("FORGE_LLM_VISION_MODEL", "qwen3.8-max"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})
	meter := &meteredClient{inner: inner, budget: budget, step: 1}

	conv := agent.NewConversation(meter, persona.DefaultCharacter())
	if kernel != nil {
		// ‼️ And the kernel draws what every step's checks look at, as it does in
		// production (httpapi: WithSolids). The 2026-09-15 run gave the build only the
		// script runner, so each step's look was of the DESCRIBED model and its
		// interference check never ran: the 77 overlaps were found after the build,
		// by this harness, when no repair could act on them. Measured runs before
		// 2026-09-15 (car-quality) are therefore not comparable on repair calls.
		conv = conv.WithScripts(liveRunner{kernel}).WithSolids(liveSolids{kernel})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	// Everything this run keeps, in one directory that outlives the test: the car,
	// every model call's prompt and reply, and every step's note. Not t.TempDir(),
	// which Go removes when the test ends (the 2026-09-15 run lost its car to it).
	outDir := filepath.Join(os.TempDir(), "forge-car-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(filepath.Join(outDir, "calls"), 0o700); err != nil {
		t.Fatalf("cannot keep this run's output: %v", err)
	}
	meter.dir = filepath.Join(outDir, "calls")
	t.Logf("CAR-OUTPUT dir=%s", outDir)

	const asked = "a sports car, in as much mechanical detail as you can manage"
	start := time.Now()
	var steps []agent.BuildStep
	doc, notes, err := agent.AssembleForTest(ctx, conv, asked, nil, func(s agent.BuildStep) error {
		steps = append(steps, s)
		meter.setStep(s.N + 1)
		t.Logf("step %d/%d %-24s parts=%-3d %s", s.N, s.Of, s.Name, s.Parts, s.Note)
		return nil
	})
	elapsed := time.Since(start)
	spent, calls, refused := meter.report()

	t.Logf("CAR-SPEND tokens=%d of %d calls=%d refused=%d seconds=%.0f",
		spent, budget, calls, refused, elapsed.Seconds())
	if refused > 0 {
		t.Logf("‼️  the token budget ran out mid-build: %d calls were refused. Everything below "+
			"describes a PARTIAL car and is a floor on the ceiling, not the ceiling.", refused)
	}
	if err != nil {
		t.Fatalf("the build did not run at all: %v", err)
	}
	if doc == nil {
		t.Fatal("the build returned no document and no error")
	}
	for _, n := range notes {
		t.Logf("note: %s", n)
	}
	// Which gate refused each step that lost its work, and the reply it read.
	records := meter.recorded()
	for _, s := range steps {
		gate := agent.StepGateOf(s.Note)
		reply := ""
		for _, r := range records {
			if r.Step == s.N && r.Role == string(llm.RoleConverse) && strings.HasPrefix(r.Prompt, "Building:") {
				reply = fmt.Sprintf("%s finish=%s completion_tokens=%d", r.File, r.FinishReason, r.CompletionTokens)
				break
			}
		}
		t.Logf("CAR-STEP step=%d gate=%s reply=%s", s.N, orNone(gate), reply)
	}
	if raw, mErr := json.MarshalIndent(steps, "", "  "); mErr == nil {
		_ = os.WriteFile(filepath.Join(outDir, "steps.json"), raw, 0o600)
	}
	// The visual check, per step and per sub-assembly (stage V4): read from the
	// calls themselves, so the product needs no hook to be measured.
	for _, line := range lookLines(looksOf(records)) {
		t.Logf("%s", line)
	}

	unit, ok := geometry.ParseUnit(doc.Units)
	if !ok {
		unit = geometry.Millimetre
		t.Logf("CAR-UNIT unreadable=%q assuming=mm", doc.Units)
	}

	// 1 — the raw ceiling, the number every earlier stage quoted.
	planned := 0
	if len(steps) > 0 {
		planned = steps[len(steps)-1].Of
	}
	// ‼️ A car written as a tree has no top-level parts, so it is counted by what
	// it places (car_tree_measure_test.go). Counted by len(doc.Parts) it measured
	// as nothing.
	tree := countTree(*doc)
	expanded := doc.Expanded()
	placed := *doc
	placed.Parts = doc.PlacedParts()
	t.Logf("CAR-CEILING steps_run=%d steps_planned=%d parts=%d features=%d minutes=%.1f",
		len(steps), planned, tree.Occurrences, len(expanded.Features), elapsed.Minutes())

	// 1b — the tree (Phase 2, stage A4): what was described once, how often it is
	// placed, and what each distinct design cost.
	t.Logf("CAR-TREE %s", tree.line())
	t.Logf("CAR-TOKENS per_design=%d per_occurrence=%d designs=%d",
		tokensPer(spent, tree.Designs()), tokensPer(spent, tree.Occurrences), tree.Designs())
	for i, r := range doc.EnumeratedRepetition() {
		if i == 5 {
			t.Logf("  … and %d more", tree.Repetitions-5)
			break
		}
		t.Logf("  could be one pattern: %d × %s in %s", len(r.Children), r.Ref, r.Assembly)
	}

	// 2 — what it reached for. A car built entirely out of boxes and a car that
	// used lofts and revolves are the same part count and not the same model.
	t.Logf("CAR-SHAPES %s", histogram(shapesOf(placed)))
	t.Logf("CAR-FEATURES %s", histogram(opsOf(expanded)))

	// 3 — does the finished assembly build in the real kernel? The document's own
	// Faults() is the weaker question and is reported beside it, because the gap
	// between the two is the thing Stage 11 was about.
	faults := doc.Faults()
	t.Logf("CAR-FAULTS document_faults=%d", len(faults))
	for _, f := range faults {
		t.Logf("  fault: %s — %s", f.Name, f.Detail)
	}
	var kernelBuild *cad.Build
	if kernel == nil {
		t.Logf("CAR-KERNEL absent — set FORGE_CAD_PYTHON to answer whether this car builds")
	} else {
		bctx, bcancel := context.WithTimeout(context.Background(), 10*time.Minute)
		build, berr := kernel.BuildDocument(bctx, *doc, unit, "step")
		bcancel()
		kernelBuild = build
		switch {
		case berr != nil:
			t.Logf("CAR-KERNEL builds=no reason=%s", errs.DetailOf(berr))
		default:
			missing := 0
			for _, n := range build.Inferred {
				if strings.Contains(n, "not in this file") {
					missing++
				}
			}
			t.Logf("CAR-KERNEL builds=yes volume=%.0f skipped=%d feature_failures=%d parts_not_in_file=%d",
				build.Volume, len(build.Skipped), len(build.FeatureFailures), missing)
			for _, s := range build.Skipped {
				t.Logf("  skipped: %s", s)
			}
			for _, f := range build.FeatureFailures {
				t.Logf("  feature failed: %s", f)
			}
		}
	}

	// 4 — interference, from the kernel. This was a bounding-box PROXY when the
	// measurement was first run on 2026-09-12; the proxy is what showed the gap
	// was worth closing, and geometry/interference.go closed it. There is one
	// answer now and it is the kernel's, because a proxy kept alongside the real
	// thing is a second truth that will eventually disagree with it.
	//
	// Every finding is printed whatever its fraction, not just the buried ones,
	// because this is where geometry.BuriedFraction gets calibrated: the
	// constant was chosen without live data and the distribution below is what
	// should decide it.
	if kernelBuild == nil {
		t.Logf("CAR-INTERFERENCE unknown — no kernel, and a bounding-box guess would report " +
			"every bolt hole and every part inside a hollow case")
	} else {
		buried := len(geometry.InterferenceProblems(kernelBuild.Interferences))
		t.Logf("CAR-INTERFERENCE pairs=%d buried=%d truncated=%v of parts=%d",
			len(kernelBuild.Interferences), buried, kernelBuild.InterferencesTruncated, tree.Occurrences)
		t.Logf("CAR-COVERAGE %s", coverageLine(kernelBuild))
		for i, f := range kernelBuild.Interferences {
			if i == 10 {
				t.Logf("  … and %d more", len(kernelBuild.Interferences)-10)
				break
			}
			t.Logf("  %s is %.0f%% inside %s (%.0f mm³)", f.A, f.Fraction*100, f.B, f.Volume)
		}
	}

	// 5 — what a "mirror" would have saved. Wall B. Bounding boxes are the right
	// tool HERE, unlike for interference: "same size, opposite x" is a question
	// about extents, and a box answers it exactly.
	boxes := boundingBoxes(expanded, unit)
	pairs := mirrorPairs(boxes)
	t.Logf("CAR-SYMMETRY mirror_pairs=%d parts_in_a_pair=%d of %d",
		pairs, pairs*2, len(boxes))

	// 6 — how the model placed things. Wall A: a literal position is arithmetic
	// the model did in its head; a bound one is arithmetic the document does.
	literal, bound, subsystems := placement(placed)
	t.Logf("CAR-PLACEMENT literal_positions=%d bound_positions=%d id_prefixes=%d",
		literal, bound, subsystems)
	// ‼️ In a tree most positions are children's and interfaces', and since 2026-09-15
	// (bound child positions) they carry "position_from" too, so they are counted apart
	// from the flattened parts above, which carry none of it. Attaching "at" an
	// interface is the other way a place follows the design, so it is counted beside it.
	children, attached, interfaces, boundPlacements := attachments(*doc)
	t.Logf("CAR-ATTACH children=%d attached_at_an_interface=%d at_coordinates=%d interfaces_declared=%d "+
		"bound_placements=%d", children, attached, children-attached, interfaces, boundPlacements)
	// And designs nothing places: built, paid for, and not in the car (run 1 of
	// 2026-09-15 car-quality held five of them).
	unplaced := unplacedAssemblies(*doc)
	t.Logf("CAR-UNPLACED assemblies=%d ids=%s", len(unplaced), strings.Join(unplaced, ","))

	// 7 — is anything hollow? Wall C. A car whose every part is solid is a car
	// that weighs four tonnes.
	hollow, cuts := hollowness(expanded)
	t.Logf("CAR-HOLLOW parts_with_holes=%d cut_features=%d of parts=%d", hollow, cuts, tree.Occurrences)

	// 8 — did any pass fail or lose work? Wall G, and the silent-drop bug. By the
	// gate the step's own note names (stepgates.go), plus a pass that removed something.
	failed := 0
	for _, s := range steps {
		if agent.StepGateOf(s.Note) != "" || strings.Contains(s.Note, "removed") {
			failed++
		}
	}
	t.Logf("CAR-PASSES with_a_problem=%d of %d", failed, len(steps))

	// The document itself, kept. The 2026-09-12 run did not save it, and the
	// threshold in geometry.BuriedFraction could not then be calibrated against
	// the only real car this system has ever built without paying for another
	// one. A measurement that throws away its subject can only be repeated, not
	// examined.
	if raw, mErr := json.MarshalIndent(doc, "", "  "); mErr == nil {
		// ‼️ Not t.TempDir(): Go removes it when the test ends, and the 2026-09-15 run
		// lost its car to exactly that. The run's own directory keeps it, beside the
		// replies that built it.
		path := filepath.Join(outDir, "car.json")
		if os.WriteFile(path, raw, 0o600) == nil {
			t.Logf("CAR-DOCUMENT saved=%s bytes=%d", path, len(raw))
		}
	}

	// 9 — the mesh the viewport would have to draw.
	t.Logf("CAR-MESH triangles=%d", len(geometry.Tessellate(*doc, unit).Triangles()))

	// This is a MEASUREMENT, and the only thing it fails on is not having
	// measured anything. Asserting a part count here would turn a number that is
	// supposed to move into a fence that has to be edited every time it does.
	if !doc.HasGeometry() {
		t.Fatal("the build produced no parts at all, so there is nothing to measure")
	}
}

// meteredClient counts tokens and refuses to spend past a budget.
//
// It fails the CALL rather than the run: assemble already handles a provider that
// will not answer, by keeping the passes it has and noting the ones it lost, and
// a partial car measured honestly is worth more than a cancelled run.
type meteredClient struct {
	inner   llm.Client
	budget  int64
	mu      sync.Mutex
	spent   int64
	calls   int
	refused int
	// largest is the costliest call so far, and inflight the calls placed and not
	// yet answered: together what the next call must leave room for (reserve).
	largest  int64
	inflight int
	// step is the build step the next call belongs to; the plan's call is step 0.
	step int
	// dir keeps every call's prompt and reply when set (see callRecord).
	dir     string
	records []callRecord
}

// callRecord is one model call as this run saw it: what the model was asked and
// what it answered, word for word.
//
// # Why every reply is kept
//
// Two live car builds lost their first step and neither kept the reply, so
// "produced no geometry" could not be diagnosed after about 330,000 tokens between
// them. The note now names the gate (stepgates.go); the reply is what the gate read.
// Images are not kept: they are the model's own render, rebuilt from the car.
type callRecord struct {
	N                int     `json:"n"`
	Step             int     `json:"step"`
	Role             string  `json:"role"`
	Model            string  `json:"model"`
	System           string  `json:"system,omitempty"`
	Prompt           string  `json:"prompt"`
	Images           int     `json:"images,omitempty"`
	Reply            string  `json:"reply"`
	FinishReason     string  `json:"finish_reason"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Seconds          float64 `json:"seconds"`
	Error            string  `json:"error,omitempty"`
	File             string  `json:"-"`
}

func (m *meteredClient) setStep(n int) {
	m.mu.Lock()
	m.step = n
	m.mu.Unlock()
}

// firstCallReserve is what a call is assumed to cost before any call of the run
// has been seen. The largest call of the 2026-09-15 verified run was 19,648 tokens.
const firstCallReserve = 12_000

// reserve is what the next call is assumed to cost: the largest call this run has
// seen, with a quarter on top, because a build's calls grow with the model it
// carries (6,692 a call on average in the verified run, 19,648 at the largest).
func (m *meteredClient) reserve() int64 {
	if m.largest == 0 {
		return firstCallReserve
	}
	return m.largest + m.largest/4
}

func (m *meteredClient) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	m.mu.Lock()
	// ‼️ A call is placed only if it can be paid for: what is spent, plus what every
	// call already in flight and this one may cost, must fit. The check used to be
	// "spent < budget", which let the last call land past the ceiling: the
	// 2026-09-15 verified run spent 301,142 of a 300,000 cap
	// (docs/spikes/2026-09-17-live-verification).
	if need := m.spent + int64(m.inflight+1)*m.reserve(); need > m.budget {
		m.refused++
		spent, budget, reserve := m.spent, m.budget, m.reserve()
		m.mu.Unlock()
		return nil, fmt.Errorf("the measurement's token budget cannot pay for another call: %d of %d "+
			"tokens used and a call may cost %d, so no further model call was placed "+
			"(FORGE_MEASURE_TOKEN_BUDGET)", spent, budget, reserve)
	}
	m.inflight++
	m.mu.Unlock()

	began := time.Now()
	resp, err := m.inner.Complete(ctx, req)
	took := time.Since(began)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight--
	m.calls++
	rec := callRecord{N: m.calls, Step: m.step, Role: string(req.Role), Model: m.inner.ModelFor(req.Role),
		Seconds: took.Seconds()}
	for _, msg := range req.Messages {
		switch msg.Role {
		case llm.System:
			rec.System = msg.Content
		case llm.User:
			rec.Prompt = msg.Content
			rec.Images += len(msg.Images)
		}
	}
	if strings.HasPrefix(rec.Prompt, "Plan the build of:") {
		rec.Step = 0
	}
	if err != nil {
		rec.Error = err.Error()
	}
	if resp != nil {
		total := resp.Usage.TotalTokens
		if total == 0 {
			total = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
		}
		m.spent += total
		if total > m.largest {
			m.largest = total
		}
		rec.Reply, rec.FinishReason = resp.Content, resp.FinishReason
		rec.PromptTokens, rec.CompletionTokens = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	if m.dir != "" {
		rec.File = filepath.Join(m.dir, fmt.Sprintf("%03d-step%02d-%s.json", rec.N, rec.Step, rec.Role))
		if raw, mErr := json.MarshalIndent(rec, "", "  "); mErr == nil {
			_ = os.WriteFile(rec.File, raw, 0o600)
		}
	}
	m.records = append(m.records, rec)
	return resp, err
}

func (m *meteredClient) recorded() []callRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]callRecord(nil), m.records...)
}

func (m *meteredClient) ModelFor(role llm.Role) string { return m.inner.ModelFor(role) }

func (m *meteredClient) report() (spent int64, calls, refused int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spent, m.calls, m.refused
}

// The kernel renders the build's checks through liveSolids (scriptrepair_live_test.go).

// lookCall is one vision call of the build's visual check (stage V4), read back
// from what was sent: the whole model, or one sub-assembly drawn on its own.
type lookCall struct {
	Step     int
	Sub      string // the sub-assembly's path, "" for the whole model
	Findings int    // -1 when the answer could not be read
	Seconds  float64
	Tokens   int64
}

// subAssemblyLook is how look.go opens the prompt for one sub-assembly.
const subAssemblyLook = "This is one sub-assembly of the model, "

// looksOf reads the vision calls out of a run's records.
func looksOf(records []callRecord) []lookCall {
	var out []lookCall
	for _, r := range records {
		if r.Role != string(llm.RoleVision) || r.Images == 0 {
			continue
		}
		l := lookCall{Step: r.Step, Seconds: r.Seconds, Tokens: r.PromptTokens + r.CompletionTokens, Findings: -1}
		if rest, ok := strings.CutPrefix(r.Prompt, subAssemblyLook); ok {
			l.Sub, _, _ = strings.Cut(rest, " (an occurrence of")
		}
		var answer struct {
			Problems []json.RawMessage `json:"problems"`
		}
		if r.Error == "" && json.Unmarshal([]byte(r.Reply), &answer) == nil {
			l.Findings = len(answer.Problems)
		}
		out = append(out, l)
	}
	return out
}

// lookLines are the CAR-LOOKS lines: one per step that looked, and the total.
func lookLines(looks []lookCall) []string {
	type perStep struct {
		whole, subs, findings int
		paths                 []string
		seconds               []float64
		tokens                int64
	}
	byStep := map[int]*perStep{}
	var order []int
	var all []float64
	var total perStep
	for _, l := range looks {
		s := byStep[l.Step]
		if s == nil {
			s = &perStep{}
			byStep[l.Step] = s
			order = append(order, l.Step)
		}
		for _, acc := range []*perStep{s, &total} {
			if l.Sub == "" {
				acc.whole++
			} else {
				acc.subs++
				acc.paths = append(acc.paths, l.Sub)
			}
			if l.Findings > 0 {
				acc.findings += l.Findings
			}
			acc.tokens += l.Tokens
		}
		s.seconds = append(s.seconds, l.Seconds)
		all = append(all, l.Seconds)
	}
	sort.Ints(order)
	lines := make([]string, 0, len(order)+1)
	for _, n := range order {
		s := byStep[n]
		lines = append(lines, fmt.Sprintf("CAR-LOOKS step=%d whole=%d sub_assemblies=%d findings=%d tokens=%d "+
			"seconds_per_call=%s looked_at=%s", n, s.whole, s.subs, s.findings, s.tokens, secondsList(s.seconds),
			strings.Join(s.paths, ",")))
	}
	mean, most := 0.0, 0.0
	for _, x := range all {
		mean += x / float64(len(all))
		most = math.Max(most, x)
	}
	lines = append(lines, fmt.Sprintf("CAR-LOOKS-TOTAL calls=%d whole=%d sub_assemblies=%d findings=%d tokens=%d "+
		"mean_seconds=%.1f max_seconds=%.1f", len(looks), total.whole, total.subs, total.findings, total.tokens, mean, most))
	return lines
}

func secondsList(xs []float64) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.FormatFloat(x, 'f', 1, 64)
	}
	return strings.Join(parts, ",")
}

// attachments counts how a tree's children are placed: at an interface, or at
// coordinates in their assembly's frame, how many interfaces were declared, and how
// many children and interfaces bind their position to the parameters.
func attachments(d geometry.Document) (children, attached, interfaces, bound int) {
	for _, a := range d.Assemblies {
		interfaces += len(a.Interfaces)
		for _, f := range a.Interfaces {
			if len(f.PositionFrom) > 0 {
				bound++
			}
		}
		for _, c := range a.Children {
			children++
			if strings.TrimSpace(c.At) != "" {
				attached++
			}
			if len(c.PositionFrom) > 0 {
				bound++
			}
		}
	}
	return children, attached, interfaces, bound
}

// box is one part's axis-aligned bounds in the assembly frame.
type box struct {
	id       string
	lo, hi   [3]float64
	volume   float64
	centreX  float64
	extents  [3]float64
	hasSolid bool
}

// boundingBoxes measures the same triangles the viewport draws, for the same
// reason PartExtents does: "size" does not describe an extrusion or a sweep.
func boundingBoxes(d geometry.Document, unit geometry.Unit) []box {
	m := geometry.Tessellate(d, unit)
	out := make([]box, 0, len(m.Groups))
	for _, g := range m.Groups {
		if len(g.Triangles) == 0 {
			continue
		}
		b := box{id: g.PartID,
			lo: [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)},
			hi: [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}}
		for _, t := range g.Triangles {
			for _, v := range [3][3]float64{t.A, t.B, t.C} {
				for i := 0; i < 3; i++ {
					b.lo[i] = math.Min(b.lo[i], v[i])
					b.hi[i] = math.Max(b.hi[i], v[i])
				}
			}
		}
		for i := 0; i < 3; i++ {
			b.extents[i] = b.hi[i] - b.lo[i]
		}
		b.volume = b.extents[0] * b.extents[1] * b.extents[2]
		b.centreX = (b.lo[0] + b.hi[0]) / 2
		b.hasSolid = b.volume > 0
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// mirrorPairs counts parts that are each other's reflection in the x=0 plane:
// the same size, the same y and z, and opposite x. Each such pair is a part a
// "mirror" would not have had to be authored twice.
func mirrorPairs(boxes []box) int {
	used := make(map[int]bool, len(boxes))
	pairs := 0
	near := func(a, b, tol float64) bool { return math.Abs(a-b) <= tol }
	for i := range boxes {
		if used[i] || !boxes[i].hasSolid || math.Abs(boxes[i].centreX) < 1 {
			continue
		}
		for j := i + 1; j < len(boxes); j++ {
			if used[j] || !boxes[j].hasSolid {
				continue
			}
			tol := 1.0
			if !near(boxes[i].centreX, -boxes[j].centreX, tol) {
				continue
			}
			if !near(boxes[i].lo[1], boxes[j].lo[1], tol) || !near(boxes[i].lo[2], boxes[j].lo[2], tol) {
				continue
			}
			same := true
			for k := 0; k < 3; k++ {
				if !near(boxes[i].extents[k], boxes[j].extents[k], tol) {
					same = false
					break
				}
			}
			if !same {
				continue
			}
			used[i], used[j] = true, true
			pairs++
			break
		}
	}
	return pairs
}

// placement separates the positions the model computed in its head from the ones
// the document computes, and counts how many subsystems the ids imply.
func placement(d geometry.Document) (literal, bound, prefixes int) {
	seen := map[string]bool{}
	for _, p := range d.Parts {
		if len(p.PositionFrom) > 0 {
			bound++
		} else {
			literal++
		}
		if i := strings.Index(p.ID, "-"); i > 0 {
			seen[p.ID[:i]] = true
		} else if p.ID != "" {
			seen[p.ID] = true
		}
	}
	return literal, bound, len(seen)
}

func hollowness(d geometry.Document) (partsWithHoles, cuts int) {
	for _, p := range d.Parts {
		if len(p.Holes) > 0 {
			partsWithHoles++
		}
	}
	for _, f := range d.Features {
		if strings.EqualFold(f.Op, "cut") {
			cuts++
		}
	}
	return partsWithHoles, cuts
}

func shapesOf(d geometry.Document) map[string]int {
	out := map[string]int{}
	for _, p := range d.Parts {
		s := strings.ToLower(strings.TrimSpace(p.Shape))
		if s == "" {
			s = "(none)"
		}
		out[s]++
	}
	return out
}

func opsOf(d geometry.Document) map[string]int {
	out := map[string]int{}
	for _, f := range d.Features {
		out[strings.ToLower(strings.TrimSpace(f.Op))]++
	}
	return out
}

func histogram(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " ")
}
