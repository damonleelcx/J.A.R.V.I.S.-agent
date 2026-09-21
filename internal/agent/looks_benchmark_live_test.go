package agent_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/looks"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The looks benchmark: a fixed set of prompts, built, rendered and judged, under
// a hard token ceiling. 2026-09-20, looks stage D2.
//
// # What it is for
//
// Stages A–C changed how FORGE's output is drawn and what vocabulary it can
// reach for, and every one of them was accepted on screenshots somebody looked
// at. That is the right evidence for one change and no evidence at all about
// drift: nothing in this repository can answer "is today's output better looking
// than last week's" without a person opening two PNGs.
//
// So: five prompts that do not change, one build each, the same two renders of
// each, and the judge in internal/looks scoring today's render against the one a
// named earlier renderer produces. The prompts are deliberately ordinary — a
// bracket, a gear, a car, an enclosure, a lever — because the claim being
// measured is about FORGE's ordinary output, not about a showcase.
//
// # The ceiling is part of the measurement
//
// This endpoint is a shared weekly plan. damon approved 100,000 tokens for the
// whole of stage D, so there are TWO ceilings, both enforced by refusing the
// call rather than by hoping: a total, and a per-prompt one, so that a car that
// will not settle cannot eat the lever's budget. A refused call degrades exactly
// as a provider outage does — the prompt reports what it got and the run
// continues — because a partial benchmark measured honestly is worth more than a
// cancelled one (the same reasoning as meteredClient in car_ceiling_live_test.go,
// whose reserve arithmetic this copies; the two ceilings are why it is not simply
// reused).
//
// # Why the conversation is NOT given a kernel
//
// A turn with solids attached runs the defect check, the sketch comparison and
// their repairs — several vision calls per prompt, each of which would come out
// of the judge's budget. The kernel is run here instead, once, over the finished
// document, which produces exactly the same numbers this gate reads (faults,
// buried pairs, parts the kernel could not build) for one call's worth of
// nothing. What is lost is the repair loop's effect on the document, and the
// record says so rather than implying these are production documents.
//
// Run it with: make looks-benchmark
func TestLiveLooksBenchmark(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the looks benchmark")
	}
	chrome := findChromeForLooks()
	if chrome == "" {
		t.Skip("no Chrome or Chromium found (set FORGE_CHROME); nothing could be rendered, and a " +
			"looks benchmark with no pictures would spend tokens to measure nothing")
	}
	// 100,000 is what damon approved for the whole of stage D, and it is an
	// ALL-TIME ceiling over this spike directory rather than a per-run one: see
	// readLedger. 24,000 per prompt is above the 18,046 one measured build turn
	// cost on 2026-09-20 — a per-prompt ceiling under the cost of a single turn
	// stops every prompt after the first, which is what happened.
	total := envInt(t, "FORGE_LOOKS_BENCH_BUDGET", 100_000)
	perPrompt := envInt(t, "FORGE_LOOKS_BENCH_PER_PROMPT", 24_000)

	outDir := os.Getenv("FORGE_LOOKS_BENCH_DIR")
	if outDir == "" {
		outDir = filepath.Join("..", "..", "docs", "spikes", "2026-09-20-looks-benchmark")
	}
	for _, sub := range []string{"verdicts", "models", "shots"} {
		if err := os.MkdirAll(filepath.Join(outDir, sub), 0o755); err != nil {
			t.Fatalf("cannot write the benchmark's record: %v", err)
		}
	}
	already, lErr := readLedger(outDir)
	if lErr != nil {
		t.Fatalf("the benchmark's ledger could not be read, so the ceiling cannot be enforced: %v", lErr)
	}
	t.Logf("BENCH-LEDGER already_spent=%d ceiling=%d left=%d", already, total, total-already)

	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "looks-benchmark"})

	// The renderer today's pictures are compared AGAINST. Absent, there is no
	// "before" and the run renders and measures but judges nothing — which is
	// said in the record rather than reported as an unjudged pass.
	beforeRenderer := os.Getenv("FORGE_LOOKS_BEFORE_RENDERER")
	afterSrc, err := os.ReadFile(filepath.Join("..", "httpapi", "assets", "forge3d.js"))
	if err != nil {
		t.Fatalf("this branch's renderer could not be read: %v", err)
	}
	var beforeSrc []byte
	if beforeRenderer != "" {
		if beforeSrc, err = os.ReadFile(beforeRenderer); err != nil {
			t.Fatalf("FORGE_LOOKS_BEFORE_RENDERER=%s could not be read: %v", beforeRenderer, err)
		}
	}

	var kernel *cad.Kernel
	if python := os.Getenv("FORGE_CAD_PYTHON"); python != "" {
		kernel = cad.New(python, log).WithScripts(true)
		defer kernel.Close()
	} else {
		t.Logf("BENCH-KERNEL absent — set FORGE_CAD_PYTHON; buried pairs and skipped parts " +
			"will be reported as unknown and the judge's guard cannot refuse on them")
	}

	inner := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		Vision:         envOrDefault("FORGE_LLM_VISION_MODEL", "qwen3.8-max"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})
	meter := &twoCeilingClient{inner: inner, total: total, perPrompt: perPrompt, already: already}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Minute)
	defer cancel()

	report := benchReport{
		Started: time.Now().UTC().Format(time.RFC3339), TotalCeiling: total, PerPromptCeiling: perPrompt,
		BeforeRenderer: beforeRenderer, VisionModel: inner.ModelFor(llm.RoleVision),
		ConverseModel: inner.ModelFor(llm.RoleConverse),
	}

	for _, p := range looksBenchmarkPrompts() {
		row := benchRow{ID: p.ID, Asked: p.Asked}
		meter.start(p.ID)
		began := time.Now()

		// ‼️ A model this benchmark has already paid for is REUSED, and the row says
		// so. Two reasons, and the second is the one that matters: a fixed prompt
		// built once is the same fixed prompt, so paying again measures nothing;
		// and the ceiling is all-time (readLedger), so a run that rebuilt what it
		// already had would spend the budget on the prompts it has and never reach
		// the ones it has not. Delete models/<id>.json to build it again.
		doc, reused := reuseModel(t, outDir, p.ID)
		if reused {
			row.Reused = true
		} else {
			conv := agent.NewConversation(meter, persona.DefaultCharacter())
			reply, rErr := conv.Respond(ctx, "", nil, p.Asked, "", nil, nil)
			row.BuildTokens = meter.spentOn(p.ID)
			switch {
			case rErr != nil:
				row.Note = "the build did not answer: " + rErr.Error()
			case reply == nil || reply.Prototype == nil:
				// ‼️ What it SAID instead is kept. On 2026-09-20 the enclosure prompt
				// produced no model and cost 17,708 tokens, and the record said only
				// "the turn produced no model" — which cannot be told apart from a
				// refusal, a clarifying question, or a bug without paying again.
				row.Note = "the turn produced no model"
				if reply != nil {
					row.Said = append(row.Said, "it said instead: "+strings.TrimSpace(reply.Speech))
				}
			default:
				doc = reply.Prototype
			}
			if row.Note != "" {
				row.Seconds = time.Since(began).Seconds()
				report.Rows = append(report.Rows, row)
				t.Logf("BENCH-PROMPT id=%s %s", p.ID, row.Note)
				continue
			}
		}
		row.Parts = len(doc.Expanded().Parts)
		row.Faults = len(doc.Faults())
		row.Shapes = shapeWords(doc.Expanded())

		unit, known := geometry.ParseUnit(doc.Units)
		if !known {
			unit = geometry.Millimetre
		}
		side := looks.Side{Faults: row.Faults}
		if kernel != nil {
			bctx, bcancel := context.WithTimeout(ctx, 8*time.Minute)
			build, bErr := kernel.BuildMesh(bctx, *doc, unit)
			bcancel()
			switch {
			case bErr != nil:
				row.KernelError = bErr.Error()
			default:
				row.Buried, row.Skipped = build.InterferencesBuried, len(build.Skipped)
				row.BuriedCounted = build.InterferencesBuriedCounted
				row.FeatureFailures = build.FeatureFailures
				row.SkippedParts = build.Skipped
				side.Buried, side.Skipped = row.Buried, row.Skipped
			}
		}

		if !reused {
			if raw, mErr := json.MarshalIndent(doc, "", "  "); mErr == nil {
				_ = os.WriteFile(filepath.Join(outDir, "models", p.ID+".json"), raw, 0o644)
			}
		}

		// The pictures. The AFTER pair is always taken; the BEFORE pair only when
		// a previous renderer was named.
		after, sErr := shotPair(ctx, chrome, afterSrc, *doc)
		if sErr != nil {
			row.Note = "this branch's renderer drew nothing: " + sErr.Error()
			row.Seconds = time.Since(began).Seconds()
			report.Rows = append(report.Rows, row)
			continue
		}
		side.Shaded, side.Normals = after.shaded, after.normals
		writeShots(t, outDir, p.ID+"-after", after)

		if beforeSrc == nil {
			row.Note = "no earlier renderer was named (FORGE_LOOKS_BEFORE_RENDERER), so there is " +
				"no before to compare against and nothing was judged"
			row.Seconds = time.Since(began).Seconds()
			report.Rows = append(report.Rows, row)
			t.Logf("BENCH-PROMPT id=%s parts=%d faults=%d — rendered, not judged", p.ID, row.Parts, row.Faults)
			continue
		}
		before, bsErr := shotPair(ctx, chrome, beforeSrc, *doc)
		if bsErr != nil {
			row.Note = "the earlier renderer drew nothing: " + bsErr.Error()
			row.Seconds = time.Since(began).Seconds()
			report.Rows = append(report.Rows, row)
			continue
		}
		writeShots(t, outDir, p.ID+"-before", before)

		// ‼️ Both sides are the SAME document, so the check's counts are the same
		// on both. That is a property of judging a RENDERER change, not a
		// weakness hidden here: the guard's discriminating behaviour is fenced
		// offline (looks.TestAPrettierPictureCannotPayForAWorseModel), and what
		// it does live is confirm that the change added nothing. The record says
		// this in as many words.
		change := looks.Change{
			ID:     p.ID,
			What:   "the same document drawn by the earlier renderer and by this branch's",
			Before: looks.Side{Shaded: before.shaded, Normals: before.normals, Faults: side.Faults, Buried: side.Buried, Skipped: side.Skipped},
			After:  side,
		}
		// Said plainly rather than left to be inferred from a round count: the
		// normals confirmation needs a normals view on BOTH sides, and a renderer
		// from before setSurfaceView existed has none. A reader must not read
		// "two rounds" as "the surfaces were checked and were fine".
		if change.Before.Normals == "" || change.After.Normals == "" {
			row.Note = "the earlier renderer has no surface-normals view, so the judge could ask " +
				"the shaded pair only; the normals confirmation was not available on this pair"
		}
		d, jErr := (looks.Judge{Client: meter}).Judge(ctx, change)
		row.JudgeTokens = d.Tokens
		row.Rounds = len(d.Rounds)
		if jErr != nil {
			row.Note = "the judge could not decide: " + jErr.Error()
		} else {
			row.Verdict, row.Accepted, row.Agreed, row.Reason = string(d.Verdict), d.Accepted, d.Agreed, d.Reason
			for _, r := range d.Rounds {
				row.Said = append(row.Said, fmt.Sprintf("%s/%s:%s", r.View, orderWord(r.NewIsFirst), r.Said))
				if r.Why != "" {
					row.Why = append(row.Why, r.View+": "+r.Why)
				}
			}
			if aErr := looks.WriteAudit(filepath.Join(outDir, "verdicts"), d); aErr != nil {
				t.Errorf("the verdict for %s could not be kept: %v", p.ID, aErr)
			}
		}
		row.Seconds = time.Since(began).Seconds()
		row.Tokens = meter.spentOn(p.ID)
		report.Rows = append(report.Rows, row)
		t.Logf("BENCH-PROMPT id=%s parts=%d faults=%d buried=%d skipped=%d verdict=%s accepted=%v "+
			"tokens=%d (build %d, judge %d) seconds=%.0f", p.ID, row.Parts, row.Faults, row.Buried,
			row.Skipped, orNone(row.Verdict), row.Accepted, row.Tokens, row.BuildTokens, row.JudgeTokens, row.Seconds)
	}

	spent, calls, refused := meter.report()
	report.Spent, report.Calls, report.Refused = spent, calls, refused
	report.SpentBefore, report.SpentAllTime = already, already+spent
	report.Finished = time.Now().UTC().Format(time.RFC3339)
	// ‼️ Written BEFORE anything that can fail the test, and written even when the
	// run spent nothing: a ledger that only records successful runs is a ceiling
	// that resets whenever a run goes wrong, which is precisely when the next one
	// is about to be started.
	if lErr := appendLedger(outDir, spent, calls, refused); lErr != nil {
		t.Errorf("the ledger could not be written, so the next run would not know this one's "+
			"spend: %v", lErr)
	}
	t.Logf("BENCH-SPEND tokens=%d of %d calls=%d refused=%d all_time=%d",
		spent, total, calls, refused, already+spent)
	if refused > 0 {
		t.Logf("‼️  the ceiling was reached: %d calls were refused, so everything above is a "+
			"PARTIAL benchmark and says so in the record.", refused)
	}
	if raw, mErr := json.MarshalIndent(report, "", "  "); mErr == nil {
		if wErr := os.WriteFile(filepath.Join(outDir, "benchmark.json"), append(raw, '\n'), 0o644); wErr != nil {
			t.Errorf("the record could not be written: %v", wErr)
		}
	}

	// This is a MEASUREMENT: the only thing it fails on is not having measured
	// anything, and on spending past the ceiling it was given.
	if already+spent > total {
		t.Errorf("this benchmark has now spent %d tokens of a %d ceiling (%d before this run, %d "+
			"in it) — the refusal arithmetic let a call through that could not be paid for",
			already+spent, total, already, spent)
	}
	if len(report.Rows) == 0 {
		t.Fatal("no prompt produced a row, so nothing was measured")
	}
}

// looksBenchmarkPrompt is one fixed prompt of the set.
type looksBenchmarkPrompt struct{ ID, Asked string }

// looksBenchmarkPrompts is the set, and it does not change between runs.
//
// Five ordinary things, chosen so that the answer to "does FORGE's output look
// designed" is not decided by one flattering subject: a flat plate with holes
// (nothing to style), a gear (a category template builds it deterministically), a
// car (the case the whole "looks designed" goal came from), a box with a lid (a
// shell, the one operation B4 asks for by name) and a lever (a grip and a clevis
// — the shapes section outlines had no vocabulary for until stage B).
//
// Each is ONE sentence, in the words a person would use, because a benchmark
// prompt written to suit the system measures the prompt.
func looksBenchmarkPrompts() []looksBenchmarkPrompt {
	return []looksBenchmarkPrompt{
		{"bracket", "an L-shaped steel mounting bracket about 120 mm by 80 mm with four bolt holes"},
		{"gear", "a spur gear with 24 teeth, 3 mm module, 12 mm thick, on a hub with a keyed bore"},
		{"car", "a sports car: a flowing body, a glasshouse and four wheels"},
		{"enclosure", "a sealed aluminium electronics enclosure 160 by 100 by 60 mm with a lid"},
		{"lever", "a hand lever with a rounded grip at one end and a forked clevis at the other"},
	}
}

// benchReport is the whole run, as it is written to the spike directory.
type benchReport struct {
	Started          string     `json:"started"`
	Finished         string     `json:"finished"`
	TotalCeiling     int64      `json:"total_ceiling"`
	PerPromptCeiling int64      `json:"per_prompt_ceiling"`
	Spent            int64      `json:"tokens_spent"`
	SpentBefore      int64      `json:"tokens_spent_by_earlier_runs"`
	SpentAllTime     int64      `json:"tokens_spent_all_time"`
	Calls            int        `json:"calls"`
	Refused          int        `json:"calls_refused"`
	BeforeRenderer   string     `json:"before_renderer"`
	VisionModel      string     `json:"vision_model"`
	ConverseModel    string     `json:"converse_model"`
	Rows             []benchRow `json:"prompts"`
}

// benchRow is one prompt's result.
type benchRow struct {
	ID    string `json:"id"`
	Asked string `json:"asked"`
	// Reused says this model was built by an earlier run of this benchmark and
	// cost nothing here. Recorded because "0 build tokens" and "built for free"
	// must not be read as "built cheaply".
	Reused          bool     `json:"built_by_an_earlier_run"`
	Parts           int      `json:"parts"`
	Shapes          string   `json:"shapes"`
	Faults          int      `json:"faults"`
	Buried          int      `json:"buried_pairs"`
	BuriedCounted   bool     `json:"buried_counted"`
	Skipped         int      `json:"skipped_parts"`
	SkippedParts    []string `json:"skipped,omitempty"`
	FeatureFailures []string `json:"feature_failures,omitempty"`
	KernelError     string   `json:"kernel_error,omitempty"`
	Verdict         string   `json:"verdict,omitempty"`
	Accepted        bool     `json:"accepted"`
	Agreed          bool     `json:"both_ways_agreed"`
	Reason          string   `json:"reason,omitempty"`
	Rounds          int      `json:"rounds"`
	Said            []string `json:"rounds_said,omitempty"`
	Why             []string `json:"why,omitempty"`
	BuildTokens     int64    `json:"build_tokens"`
	JudgeTokens     int64    `json:"judge_tokens"`
	Tokens          int64    `json:"tokens"`
	Seconds         float64  `json:"seconds"`
	Note            string   `json:"note,omitempty"`
}

func orderWord(newFirst bool) string {
	if newFirst {
		return "new-first"
	}
	return "old-first"
}

func shapeWords(d geometry.Document) string {
	counts := map[string]int{}
	for _, p := range d.Parts {
		s := strings.ToLower(strings.TrimSpace(p.Shape))
		if s == "" {
			s = "(none)"
		}
		counts[s]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, " ")
}

func envInt(t *testing.T, key string, fallback int64) int64 {
	t.Helper()
	s := os.Getenv(key)
	if s == "" {
		return fallback
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		t.Fatalf("%s must be a positive number of tokens, got %q", key, s)
	}
	return n
}

// twoCeilingClient refuses a call that neither the run nor the current prompt can
// pay for.
//
// # Why two, and why it refuses BEFORE the ceiling
//
// One ceiling is enough to keep a run inside a budget and useless for keeping it
// fair: the car is the fourth of five prompts and, given one pot, would arrive
// having spent the lever's share. The per-prompt ceiling is what makes the five
// rows comparable.
//
// Both are checked against a RESERVE — what the next call may cost, taken as the
// largest call seen plus a quarter — rather than against what is already spent,
// because "spent < budget" lets the last call land past the ceiling: the
// 2026-09-15 verified run spent 301,142 of a 300,000 cap that way
// (docs/spikes/2026-09-17-live-verification).
type twoCeilingClient struct {
	inner     llm.Client
	total     int64
	perPrompt int64

	// already is what every EARLIER run of this benchmark spent, from the ledger.
	// The total ceiling is all-time, so this is added to everything checked
	// against it.
	already int64

	mu      sync.Mutex
	spent   int64
	calls   int
	refused int
	// largest is the costliest call SO FAR OF EACH ROLE: see reserve.
	largest  map[llm.Role]int64
	inflight int
	prompt   string
	byPrompt map[string]int64
}

// looksFirstCallReserve is what a call of each role is assumed to cost before
// this run has seen one. Measured 2026-09-20: a build turn is ~18,000 tokens and
// one of the judge's questions, two 640×400 renders and a short contract, is
// ~810.
var looksFirstCallReserve = map[llm.Role]int64{llm.RoleVision: 3_000}

const looksDefaultReserve = 20_000

// reserve is what the next call of THIS ROLE is assumed to cost.
//
// # ‼️ Why it is per role and not one number
//
// It was one number — the largest call anywhere in the run, plus a quarter, as
// car_ceiling_live_test.go does — and on 2026-09-20 that silently stopped the
// judge from ever running. A build turn costs about 18,000 tokens and one of the
// judge's questions costs about 810, so after the first build the reserve stood
// at 22,877 and every 810-token question was refused as unaffordable against a
// 24,000 per-prompt ceiling. The record read "the judge could not decide" on two
// prompts that had plenty of budget left; a benchmark whose whole point is the
// judge had refused to ask it, for a reason that looks like a budget being tight.
//
// One reserve is right where every call costs roughly the same, which is true of
// the car measurement it was copied from and false here: this harness mixes calls
// that differ by more than twenty times.
func (m *twoCeilingClient) reserve(role llm.Role) int64 {
	if seen := m.largest[role]; seen > 0 {
		return seen + seen/4
	}
	if first, ok := looksFirstCallReserve[role]; ok {
		return first
	}
	return looksDefaultReserve
}

func (m *twoCeilingClient) start(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompt = id
	if m.byPrompt == nil {
		m.byPrompt = map[string]int64{}
	}
	if _, ok := m.byPrompt[id]; !ok {
		m.byPrompt[id] = 0
	}
}

func (m *twoCeilingClient) spentOn(id string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byPrompt[id]
}

func (m *twoCeilingClient) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	m.mu.Lock()
	need := int64(m.inflight+1) * m.reserve(req.Role)
	here := m.byPrompt[m.prompt]
	// ‼️ The per-prompt ceiling is checked only once the prompt has spent
	// something. A prompt's FIRST call is weighed against the run's total alone.
	//
	// Measured 2026-09-20: one build turn on this deployment cost 18,046 tokens,
	// which made the reserve 22,557 — and with a 14,000 per-prompt ceiling, every
	// prompt after the first was refused before it placed a single call. The run
	// reported four prompts "the build did not answer" and looked like a provider
	// outage. A ceiling meant to stop ONE prompt running away had become a rule
	// that only the first prompt may run at all, because the reserve is a guess
	// about the next call taken from the largest call anywhere in the run.
	//
	// So the per-prompt ceiling does what it is for — bounding a prompt that keeps
	// asking — and cannot silently bound the number of prompts. A first call that
	// turns out to cost more than the ceiling is reported, in the row, rather than
	// prevented: the total is the ceiling that actually protects the plan.
	overRun := m.already+m.spent+need > m.total
	overPrompt := here > 0 && here+need > m.perPrompt
	if overRun || overPrompt {
		m.refused++
		spent, here, reserve, prompt := m.already+m.spent, here, m.reserve(req.Role), m.prompt
		m.mu.Unlock()
		return nil, fmt.Errorf("the looks benchmark cannot pay for another call: %d of %d tokens "+
			"used all time and %d of %d on %q, and a %s call may cost %d, so no further model call "+
			"was placed (FORGE_LOOKS_BENCH_BUDGET, FORGE_LOOKS_BENCH_PER_PROMPT)",
			spent, m.total, here, m.perPrompt, prompt, req.Role, reserve)
	}
	m.inflight++
	prompt := m.prompt
	m.mu.Unlock()

	resp, err := m.inner.Complete(ctx, req)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight--
	m.calls++
	if resp != nil {
		spent := resp.Usage.TotalTokens
		if spent == 0 {
			spent = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
		}
		m.spent += spent
		m.byPrompt[prompt] += spent
		if m.largest == nil {
			m.largest = map[llm.Role]int64{}
		}
		if spent > m.largest[req.Role] {
			m.largest[req.Role] = spent
		}
	}
	return resp, err
}

func (m *twoCeilingClient) ModelFor(role llm.Role) string { return m.inner.ModelFor(role) }

func (m *twoCeilingClient) report() (spent int64, calls, refused int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spent, m.calls, m.refused
}

// ledger is every run this benchmark has ever made, and what each one spent.
//
// # Why the ceiling is all-time and not per-run
//
// damon approved 100,000 tokens for stage D, not 100,000 per attempt. A per-run
// ceiling is no ceiling at all the moment a run has to be repeated — and the
// first attempt at this benchmark WAS repeated twice, once because a headless
// Chrome hung and once because the per-prompt rule refused four of five prompts.
// Under a per-run ceiling those three attempts would have been entitled to
// 300,000 tokens between them, and nothing in the harness would have said so.
//
// So each run appends what it spent, and the next one starts from the total. The
// file lives with the record it belongs to — deleting it is how a person
// deliberately starts a new budget, and it is visible in the diff when they do.
type ledger struct {
	Ceiling string      `json:"ceiling_note"`
	Total   int64       `json:"total_spent"`
	Runs    []ledgerRun `json:"runs"`
}

type ledgerRun struct {
	At      string `json:"at"`
	Spent   int64  `json:"spent"`
	Calls   int    `json:"calls"`
	Refused int    `json:"calls_refused"`
	Note    string `json:"note,omitempty"`
}

const ledgerFile = "ledger.json"

// readLedger is what every earlier run of this benchmark spent.
//
// A missing file is zero — the first run. An UNREADABLE one is an error, and the
// caller refuses to run: a corrupt ledger read as zero is a ceiling that quietly
// resets, which is the one failure this file exists to prevent.
func readLedger(dir string) (int64, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ledgerFile))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var l ledger
	if err := json.Unmarshal(raw, &l); err != nil {
		return 0, unreadableLedger(err)
	}
	var sum int64
	for _, r := range l.Runs {
		sum += r.Spent
	}
	if sum != l.Total {
		return 0, fmt.Errorf("%s says %d spent but its runs add up to %d", ledgerFile, l.Total, sum)
	}
	return sum, nil
}

// unreadableLedger is the refusal a corrupt ledger gets. Its own function so the
// refusal can be removed by one mutation and the drill can show what that costs.
func unreadableLedger(err error) error {
	return fmt.Errorf("%s is not readable (%w); a ceiling cannot be enforced against a "+
		"ledger nobody can read, so delete it deliberately or fix it", ledgerFile, err)
}

// appendLedger records this run. It rewrites the whole file, so the total and the
// rows can never disagree.
func appendLedger(dir string, spent int64, calls, refused int) error {
	path := filepath.Join(dir, ledgerFile)
	var l ledger
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &l); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	l.Ceiling = "FORGE_LOOKS_BENCH_BUDGET is an ALL-TIME ceiling over this directory, not a " +
		"per-run one: every run starts from total_spent. Delete this file to start a new budget."
	l.Runs = append(l.Runs, ledgerRun{
		At: time.Now().UTC().Format(time.RFC3339), Spent: spent, Calls: calls, Refused: refused,
	})
	l.Total = 0
	for _, r := range l.Runs {
		l.Total += r.Spent
	}
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// reuseModel loads a model an earlier run of this benchmark already paid for.
//
// The prompts do not change, so neither does what they are worth building twice:
// nothing. A model that cannot be read is treated as absent and rebuilt rather
// than failing the run — but the reason is logged, because "rebuilt because the
// file was corrupt" and "rebuilt because it was the first run" are different
// facts and both cost tokens.
func reuseModel(t *testing.T, dir, id string) (*geometry.Document, bool) {
	t.Helper()
	path := filepath.Join(dir, "models", id+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var doc geometry.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Logf("BENCH-REUSE id=%s unreadable=%v — rebuilding it, which costs tokens", id, err)
		return nil, false
	}
	if !doc.HasGeometry() {
		t.Logf("BENCH-REUSE id=%s holds no geometry — rebuilding it, which costs tokens", id)
		return nil, false
	}
	t.Logf("BENCH-REUSE id=%s built by an earlier run, no tokens spent", id)
	return &doc, true
}

// pair is one document drawn twice by one renderer: as a reader sees it, and by
// surface direction.
type pair struct{ shaded, normals string }

// shotPair draws one document with one forge3d.js, in headless Chrome on
// SwiftShader, and returns both views as PNG data URIs.
//
// # Why a browser and not the Go rasteriser
//
// geometry.ContactSheet is four flat-shaded orthographic views and says so in its
// own header — it is deliberately not a good renderer, and a looks judge fed a
// picture of it would be answering a question about the check's draughtsman, not
// about FORGE's output. forge3d.js IS the output, and
// TestShadersCompileInARealBrowser already drives it headlessly on SwiftShader,
// so no GPU is needed. Nothing is labelled: a caption reading "AFTER" is the
// answer written on the question.
func shotPair(ctx context.Context, chrome string, renderer []byte, doc geometry.Document) (pair, error) {
	spec, err := json.Marshal(doc)
	if err != nil {
		return pair{}, err
	}
	dir, err := os.MkdirTemp("", "forge-looks-shot")
	if err != nil {
		return pair{}, err
	}
	defer os.RemoveAll(dir)

	page := `<!doctype html><meta charset="utf-8"><body style="margin:0">
<canvas id="c" width="640" height="400"></canvas>
<pre id="result">pending</pre>
<script src="forge3d.js"></script>
<script>
  var DOC = ` + string(spec) + `;
  var out = {};
  try {
    var studio = new Forge3D.Studio(document.getElementById('c'), { onError: function (m) { out.error = m; } });
    if (!studio.gl) { out.error = 'no WebGL context'; }
    else {
      studio.load(DOC);
      studio.resetView();
      out.shaded = studio.snapshot();
      if (studio.setSurfaceView) { studio.setSurfaceView('normals'); out.normals = studio.snapshot(); }
      out.path = studio.renderPath;
    }
  } catch (e) { out.error = String(e && e.message || e); }
  document.getElementById('result').textContent = JSON.stringify(out);
</script>`
	for name, data := range map[string][]byte{"forge3d.js": renderer, "index.html": []byte(page)} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return pair{}, err
		}
	}
	url := "file://" + filepath.ToSlash(filepath.Join(dir, "index.html"))
	if runtime.GOOS == "windows" {
		url = "file:///" + filepath.ToSlash(filepath.Join(dir, "index.html"))
	}
	dom, err := dumpDOM(ctx, chrome, dir, url)
	if err != nil {
		return pair{}, err
	}
	s := string(dom)
	i, j := strings.Index(s, `<pre id="result">`), strings.Index(s, "</pre>")
	if i < 0 || j < i {
		return pair{}, fmt.Errorf("no result in the page Chrome rendered: %.300s", s)
	}
	var got struct{ Shaded, Normals, Error, Path string }
	if err := json.Unmarshal([]byte(html.UnescapeString(s[i+len(`<pre id="result">`):j])), &got); err != nil {
		return pair{}, err
	}
	if got.Error != "" {
		return pair{}, fmt.Errorf("the renderer refused: %s", got.Error)
	}
	if !strings.HasPrefix(got.Shaded, "data:image/png;base64,") {
		return pair{}, fmt.Errorf("the page produced no PNG")
	}
	return pair{shaded: got.Shaded, normals: got.Normals}, nil
}

// writeShots keeps both views beside the record. A failure is reported, never
// swallowed: a benchmark whose pictures silently did not land is one nobody can
// check.
func writeShots(t *testing.T, dir, name string, p pair) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "shots"), 0o755); err != nil {
		t.Errorf("%s: %v", name, err)
		return
	}
	for suffix, uri := range map[string]string{"shaded": p.shaded, "normals": p.normals} {
		if uri == "" {
			continue
		}
		raw, err := decodeDataURI(uri)
		if err != nil {
			t.Errorf("%s-%s: %v", name, suffix, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, "shots", name+"-"+suffix+".png"), raw, 0o644); err != nil {
			t.Errorf("%s-%s: %v", name, suffix, err)
		}
	}
}

// dumpDOM runs headless Chrome over one page and returns the DOM it dumped.
//
// # ‼️ Why this is not `exec.CommandContext(...).Output()`
//
// It was, and it hung the first live run of this benchmark (2026-09-20), 8 minutes
// on the first of five prompts with nothing written and nothing said.
//
// Chrome writes the whole dump and then does not exit. Reproduced outside Go:
// the same page, the same flags, a bracket in it — with a 6 KB dump Chrome exits
// in 21 s; with the two PNGs in it (295 KB) the dump lands COMPLETE on disk and
// the process is still alive two minutes later, killed only by `timeout`. And
// `Output()` cannot recover from that on Windows: it reads through a pipe whose
// write end Chrome's child processes also hold, so killing the one Go started
// leaves the pipe open and `Wait` blocks behind it — the context's deadline
// expires and nothing happens. TestShadersCompileInARealBrowser never met this
// because its page reports a few hundred bytes.
//
// So: Chrome's stdout is an ordinary FILE (no pipe, nothing for Wait to block
// behind), and this waits for the dump to be complete rather than for Chrome to
// decide it is finished — then kills it. The wait is bounded and its end is
// checked, as every wait in this repository must be.
func dumpDOM(ctx context.Context, chrome, dir, url string) ([]byte, error) {
	path := filepath.Join(dir, "dom.html")
	out, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-dev-shm-usage",
		"--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--ignore-gpu-blocklist",
		"--user-data-dir="+filepath.Join(dir, "profile"), "--virtual-time-budget=6000", "--dump-dom", url)
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("headless Chrome would not start: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Bounded, and it exits the moment the watched process is gone: 480 × 250ms
	// is two minutes, against 21 s measured for a page like this one.
	const tries, every = 480, 250 * time.Millisecond
	var dom []byte
	complete := false
	for i := 0; i < tries && !complete; i++ {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return nil, ctx.Err()
		case err := <-done:
			dom, _ = os.ReadFile(path)
			if err != nil && !bytes.Contains(dom, []byte("</html>")) {
				return nil, fmt.Errorf("headless Chrome failed: %w", err)
			}
			complete = true
		case <-time.After(every):
			dom, _ = os.ReadFile(path)
			complete = bytes.Contains(dom, []byte("</html>"))
		}
	}
	// Kill unconditionally: it has either finished and not noticed, or it is past
	// the deadline. A second kill of an exited process is harmless.
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
	}
	if !complete {
		return nil, fmt.Errorf("headless Chrome wrote no complete DOM in %s (%d bytes)",
			time.Duration(tries)*every, len(dom))
	}
	return dom, nil
}

// decodeDataURI turns a PNG data URI back into bytes.
func decodeDataURI(uri string) ([]byte, error) {
	_, payload, found := strings.Cut(uri, ",")
	if !found {
		return nil, fmt.Errorf("not a data URI")
	}
	return base64.StdEncoding.DecodeString(payload)
}

// findChromeForLooks is httpapi's findChrome, which is unexported in a package
// this one cannot import. Kept identical on purpose: two probes that drift apart
// is how one suite skips and the other fails on the same machine.
func findChromeForLooks() string {
	if c := os.Getenv("FORGE_CHROME"); c != "" {
		return c
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
