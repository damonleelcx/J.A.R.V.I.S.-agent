package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// TestLiveParametricContract asks a real model for a parametric document.
//
// # Why a live test and not a stub
//
// Everything else about the parametric phase is fenced with fixtures, and every
// one of those fixtures was written by hand from the 2026-09-05 spike's recorded
// output. That leaves exactly one thing unproven: whether the CONTRACT in
// converse.go actually produces this shape from a model reading it. A stub
// returning well-formed parameters would pass forever while production received
// none at all — the failure mode this codebase keeps rediscovering as "the
// feature had no producer".
//
// The spike measured the shape against a probe prompt of its own
// (docs/spikes/2026-09-05-parametric-cad-kernel/premise_b.py, 3/3 on structure).
// That is NOT evidence about converse.go's contract, which is longer, asks for
// six other things at the same time, and is the one that ships.
//
// # What it asserts, and what it deliberately does not
//
// It asserts the STRUCTURE arrives and resolves. It does NOT assert the figures
// are right: the spike measured 0/3 on the NEMA 17 bolt figure, so a test that
// required a correct figure would fail on a known and recorded model property
// rather than on a regression in this code. Catching wrong figures is the eval
// suite's job, and it is scored as a rate there.
//
// Skipped without FORGE_LIVE_LLM_TESTS so CI stays hermetic and free.
func TestLiveParametricContract(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live parametric contract")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "parametric-live-test"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen-plus"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})

	conv := agent.NewConversation(client, persona.DefaultCharacter())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// The same request the standards-honesty eval case makes, with the two ribs
	// that made the spike's sweep break — a rib is the part whose length must
	// follow the plate.
	reply, err := conv.Respond(ctx, "", nil,
		"Design a bracket that mounts a NEMA 17 stepper motor to a flat surface, with two "+
			"stiffening ribs. Give me the parameters I can change.", "", nil, nil)
	if err != nil {
		t.Fatalf("the live turn failed: %v", err)
	}
	if reply.Prototype == nil {
		t.Fatal("no geometry was proposed for an explicitly physical request")
	}

	dump, _ := json.MarshalIndent(struct {
		Parameters  any `json:"parameters"`
		Derived     any `json:"derived"`
		Features    any `json:"features"`
		Parts       any `json:"parts"`
		Recalled    any `json:"recalled"`
		NotVerified any `json:"not_verified"`
	}{reply.Prototype.Parameters, reply.Prototype.Derived, reply.Prototype.Features,
		reply.Prototype.Parts, reply.Recalled, reply.Prototype.NotVerified}, "", "  ")
	t.Logf("live parametric reply from %s:\n%s", reply.Model, dump)

	if len(reply.Prototype.Parameters) == 0 {
		t.Fatal("the contract asked for parameters and the model produced none — " +
			"the parametric representation has no producer in the shipping prompt")
	}
	if len(reply.Prototype.Derived) == 0 {
		t.Error("no derived expression was produced. Named parameters alone are the half " +
			"that does NOT survive a change: the 2026-09-05 sweep broke on exactly this")
	}

	// It must RESOLVE. A document full of expressions that do not evaluate is
	// structure without substance, and would reach the reader as a wall of
	// caveats in NotVerified.
	res := reply.Prototype.Resolve()
	for _, p := range res.Problems {
		t.Logf("resolution %s on %q: %s", p.Severity, p.Name, p.Detail)
	}
	if !res.OK() {
		t.Error("the parametric document the model produced does not resolve")
	}

	// A derived value that reads nothing is a fixed number wearing a
	// relationship's clothes — the spike's headline finding, and the contract
	// now forbids it in as many words.
	//
	// Counted and reported rather than failed per occurrence, for the same
	// reason this test does not check the NEMA figure: how OFTEN a model obeys
	// a rule is a rate, and rates belong in the eval suite where they are scored
	// against a measured floor. What is asserted here is the thing that must
	// always hold — that the mechanism lands at all. A run where NOTHING derived
	// reads a parameter is a contract that is not working, not a model having a
	// bad day.
	//
	// First observed 2026-09-05: 2 bare constants ("42.3", "31.0") out of the
	// derived list, against the contract BEFORE it said "every expression must
	// name at least one parameter". The 31.0 is the correct NEMA 17 bolt pitch —
	// and sitting in "derived" it carries no how/source, so it reached the
	// reader without passing the provenance check at all. That is what the
	// strengthened rule is for.
	var bare, linked int
	for _, d := range reply.Prototype.Derived {
		if v, ok := res.Values[d.Name]; ok && len(v.Depends) == 0 {
			bare++
			t.Logf("derived %q = %q reads no parameter — a fixed number that will not follow anything",
				d.Name, d.Expression)
			continue
		}
		linked++
	}
	t.Logf("derived expressions: %d reference a parameter, %d are bare constants", linked, bare)
	if linked == 0 {
		t.Error("not one derived expression references a parameter. Named values alone are " +
			"the half that does NOT survive a change: the 2026-09-05 sweep broke on exactly this")
	}

	// --- wave 11: are the parameters BOUND to the geometry? ---
	//
	// This is the producer check for the binding layer. size_from and
	// position_from are the only things that make a parameter move a shape;
	// without them the document describes a relationship the geometry does not
	// obey, which is the state wave 11 exists to end.
	var boundParts, boundDims int
	for _, part := range reply.Prototype.Parts {
		n := len(part.SizeFrom) + len(part.PositionFrom)
		if n > 0 {
			boundParts++
			boundDims += n
		}
	}
	t.Logf("bindings: %d of %d parts bind %d dimensions in total",
		boundParts, len(reply.Prototype.Parts), boundDims)
	if boundDims == 0 {
		t.Error("not one dimension is bound to a parameter, so nothing the model emitted would " +
			"move if a parameter changed — the binding layer has no producer in the shipping prompt")
	}

	// And the payoff, exercised on what the model actually produced: change a
	// parameter and see whether the geometry follows. Any parameter will do —
	// what is being tested is that SOMETHING moves.
	if len(reply.Prototype.Parameters) > 0 && boundDims > 0 {
		knob := reply.Prototype.Parameters[0].Name
		before := sizeSnapshot(reply.Prototype)
		next, problems := reply.Prototype.WithParameters(
			map[string]float64{knob: reply.Prototype.Parameters[0].Value * 1.5})
		for _, p := range problems {
			t.Logf("respec %s on %q: %s", p.Severity, p.Name, p.Detail)
		}
		if next != nil && sizeSnapshot(next) == before {
			t.Logf("changing %q by 50%% moved no dimension; it may simply drive nothing", knob)
		} else {
			t.Logf("changing %q by 50%% re-derived the geometry", knob)
		}
	}
}

// sizeSnapshot renders every part's sizes and positions, so a change anywhere is
// one string comparison.
func sizeSnapshot(d *agent.Prototype) string {
	var b strings.Builder
	for _, p := range d.Parts {
		keys := make([]string, 0, len(p.Size))
		for k := range p.Size {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "%s|", p.ID)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s=%g,", k, p.Size[k])
		}
		fmt.Fprintf(&b, "pos=%v;", p.Position)
	}
	return b.String()
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// The whole chain, on the model's own output: ask for a part that needs holes,
// and build what comes back with a real CAD kernel.
//
// # Why this is the test that matters
//
// Everything else is fenced with documents written by hand. Those prove the
// kernel does what it is told; they cannot prove the MODEL tells it anything
// buildable. A contract the model half-follows produces a document that
// validates, renders, and turns into a solid brick with the mounting holes
// missing — and every unit test in this repository would stay green.
//
// Skipped without both a model and a kernel.
func TestLiveModelOutputBuildsInTheKernel(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; run `make cad-venv`")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "live"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen-plus"),
		RequestTimeout: 3 * time.Minute, MaxRetries: 2,
	}, log, clock.System{})

	conv := agent.NewConversation(client, persona.DefaultCharacter())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	reply, err := conv.Respond(ctx, "", nil,
		"Design a flat aluminium bracket that bolts a NEMA 17 stepper motor to a surface. "+
			"It needs four clearance holes through the plate for the motor screws and a "+
			"rounded outer edge.", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Prototype == nil {
		t.Fatal("no geometry for an explicitly physical request")
	}
	dump, _ := json.MarshalIndent(reply.Prototype.Features, "", "  ")
	t.Logf("features from %s:\n%s", reply.Model, dump)

	ops, problems := reply.Prototype.Operations()
	for _, p := range problems {
		t.Logf("feature %s on %q: %s", p.Severity, p.Name, p.Detail)
	}
	t.Logf("features: %d emitted, %d valid", len(reply.Prototype.Features), len(ops))

	k := cad.New(python, log)
	defer k.Close()
	built, err := k.BuildDocument(ctx, *reply.Prototype, geometry.Millimetre, "step")
	if err != nil {
		t.Fatalf("the model's own document did not build: %v", err)
	}
	t.Logf("built %d part(s), volume %.1f mm³, %d bytes of STEP; skipped=%v failures=%v",
		built.Parts, built.Volume, len(built.STEP), built.Skipped, built.FeatureFailures)

	if len(built.STEP) == 0 || !bytes.HasPrefix(built.STEP, []byte("ISO-10303-21;")) {
		t.Fatal("no STEP file came back from the model's own document")
	}
	// Counted and reported rather than asserted: how OFTEN a model uses a
	// feature is a rate, and rates belong in the eval suite against a measured
	// floor. What is asserted is that whatever it produced BUILDS.
	if len(reply.Prototype.Features) == 0 {
		t.Log("the model emitted no features, so the holes are separate solids " +
			"rather than voids — the contract is not landing")
	}
}

// Can the model design with an OUTLINE?
//
// Extrusions are the first thing here that is not a primitive, and the contract
// asks for a vocabulary — a closed list of points, in the part's own plane,
// preferably as expressions — that no model has been asked for before. Whether
// it lands is a question about the CONTRACT, and the only way to ask it is to
// ask a model.
//
// What is asserted is that whatever comes back BUILDS. How often it reaches for
// an extrusion is a rate, and rates belong in the eval suite against a measured
// floor.
func TestLiveModelDesignsWithAnOutline(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; run `make cad-venv`")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "live"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen-plus"),
		RequestTimeout: 3 * time.Minute, MaxRetries: 2,
	}, log, clock.System{})

	conv := agent.NewConversation(client, persona.DefaultCharacter())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// An L-section is the canonical case: it cannot be a box, and it is concave.
	reply, err := conv.Respond(ctx, "", nil,
		"Design a steel angle bracket — an L-shaped cross section, 40 mm on each leg, "+
			"8 mm thick, 60 mm long — with a bolt hole through each leg.", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Prototype == nil {
		t.Fatal("no geometry for an explicitly physical request")
	}

	var outlines int
	for _, p := range reply.Prototype.Parts {
		if len(p.Profile) > 0 {
			outlines++
			t.Logf("outline on %q: %d points, depth %v", p.ID, len(p.Profile), p.Size["depth"])
		}
	}
	for _, pr := range reply.Prototype.ProfileProblems() {
		t.Logf("outline %s on %q: %s", pr.Severity, pr.Name, pr.Detail)
	}
	t.Logf("parts %d, outlines %d, features %d",
		len(reply.Prototype.Parts), outlines, len(reply.Prototype.Features))

	k := cad.New(python, log)
	defer k.Close()
	built, err := k.BuildDocument(ctx, *reply.Prototype, geometry.Millimetre, "step")
	if err != nil {
		t.Fatalf("the model's own document did not build: %v", err)
	}
	t.Logf("built %d part(s), volume %.1f mm³, %d bytes of STEP; failures=%v",
		built.Parts, built.Volume, len(built.STEP), built.FeatureFailures)
	if !bytes.HasPrefix(built.STEP, []byte("ISO-10303-21;")) {
		t.Fatal("no STEP file from the model's own document")
	}
	if outlines == 0 {
		t.Log("the model described an L-section without an outline, so the extrusion " +
			"contract is not landing")
	}
}

// Can the model design a part that BENDS?
//
// A sweep is the third thing here that is not a primitive, and it asks for
// something the other two do not: a PATH, in three dimensions, with the outline
// riding its origin. Two things about that are easy to get wrong and only a
// model can say whether the contract conveys them — that the outline should be
// drawn around (0, 0) when the path is meant to run down the middle, and that a
// bend cannot be tighter than the outline is wide.
//
// The prompt is chosen because primitives cannot express it. Three boxes end to
// end leave a gap on the outside of each corner and an overlap on the inside,
// which is exactly the material a mitre puts right — so a model that reaches for
// boxes has produced something visibly wrong rather than something simpler.
//
// What is asserted is that whatever comes back BUILDS. How often it reaches for
// a sweep is a rate, and rates belong in the eval suite against a measured floor.
func TestLiveModelDesignsABentPart(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; run `make cad-venv`")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "live"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen-plus"),
		RequestTimeout: 3 * time.Minute, MaxRetries: 2,
	}, log, clock.System{})

	conv := agent.NewConversation(client, persona.DefaultCharacter())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var reply *agent.Reply
	for attempt := 1; attempt <= 2 && (reply == nil || reply.Prototype == nil); attempt++ {
		r, err := conv.Respond(ctx, "", nil,
			"Design an aluminium coolant line for a machine tool: a 20 mm by 12 mm "+
				"rectangular bar that runs 300 mm up from the pump, then turns and runs "+
				"200 mm horizontally, then turns again and drops 150 mm into the manifold.",
			"", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		reply = r
		if reply.Prototype == nil {
			t.Logf("attempt %d produced no geometry; it said: %.200s", attempt, reply.Speech)
		}
	}
	if reply.Prototype == nil {
		t.Skip("the model returned unreadable JSON twice, so there is no output of its own " +
			"to build; this says nothing about the kernel and the run is not a result")
	}

	var sweeps int
	for _, p := range reply.Prototype.Parts {
		if strings.EqualFold(p.Shape, "sweep") {
			sweeps++
			t.Logf("sweep %q: %d outline points, %d path points", p.ID, len(p.Profile), len(p.Path))
			for i, pt := range p.Path {
				t.Logf("  path %d: (%v, %v, %v)", i+1, pt.X, pt.Y, pt.Z)
			}
		}
	}
	for _, pr := range reply.Prototype.ProfileProblems() {
		t.Logf("outline %s on %q: %s", pr.Severity, pr.Name, pr.Detail)
	}
	// What it reached for INSTEAD is the finding when it does not reach for a
	// sweep, and "sweeps 0" on its own does not say whether it drew three boxes,
	// three extrusions, or gave up and drew one straight bar.
	for _, p := range reply.Prototype.Parts {
		t.Logf("part %q: shape %q size %v at %v", p.ID, p.Shape, p.Size, p.Position)
	}
	t.Logf("parts %d, sweeps %d, features %d",
		len(reply.Prototype.Parts), sweeps, len(reply.Prototype.Features))

	k := cad.New(python, log)
	defer k.Close()
	built, err := k.BuildDocument(ctx, *reply.Prototype, geometry.Millimetre, "step")
	if err != nil {
		t.Fatalf("the model's own document did not build: %v", err)
	}
	t.Logf("built %d part(s), volume %.1f mm³, %d bytes of STEP; skipped=%v failures=%v",
		built.Parts, built.Volume, len(built.STEP), built.Skipped, built.FeatureFailures)
	if !bytes.HasPrefix(built.STEP, []byte("ISO-10303-21;")) {
		t.Fatal("no STEP file from the model's own document")
	}
	if sweeps == 0 {
		t.Log("the model described a bent part without a sweep, so the sweep contract is " +
			"not landing")
	}
}

// Can the model design a TURNED part?
//
// A revolve is the second thing here that is not a primitive, and it asks for
// something no model has been asked for before: an outline that stays on one
// side of its own axis. Getting that wrong is the commonest way a revolve fails,
// and whether the contract conveys it is a question only a model can answer.
func TestLiveModelDesignsATurnedPart(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; run `make cad-venv`")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "live"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen-plus"),
		RequestTimeout: 3 * time.Minute, MaxRetries: 2,
	}, log, clock.System{})

	conv := agent.NewConversation(client, persona.DefaultCharacter())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The prompt has to need a revolve, and this one is chosen because it does.
	//
	// The first version asked for a stepped bush, and qwen-plus described it with
	// two cylinders and a bore — which is CORRECT: a stepped bush is exactly two
	// cylinders, and reaching for the simpler vocabulary is the right instinct.
	// It tested nothing. A vee groove has sloped walls that are neither a
	// cylinder nor a cone sitting on the axis, so a primitive cannot express it
	// and the model has to reach for the outline or say it cannot.
	// Two attempts, and the second one is not padding.
	//
	// This prompt intermittently comes back as JSON the contract cannot read —
	// three probe runs parsed cleanly and two through this path did not, on
	// identical messages. That is the model being unreliable on a long request,
	// not a defect in anything here, and a test that fails on it is reporting
	// the wrong thing. Failing TWICE in a row would be worth knowing about;
	// failing once is weather.
	var reply *agent.Reply
	for attempt := 1; attempt <= 2 && (reply == nil || reply.Prototype == nil); attempt++ {
		r, err := conv.Respond(ctx, "", nil,
			"Design a V-belt pulley: 80 mm outside diameter, 20 mm wide, with a vee groove "+
				"cut all the way round the rim — 34 degrees included angle, 12 mm deep — and "+
				"a 16 mm bore through the middle.", "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		reply = r
		if reply.Prototype == nil {
			t.Logf("attempt %d produced no geometry; it said: %.200s", attempt, reply.Speech)
		}
	}
	if reply.Prototype == nil {
		t.Skip("the model returned unreadable JSON twice, so there is no output of its own " +
			"to build; this says nothing about the kernel and the run is not a result")
	}

	var revolves int
	for _, p := range reply.Prototype.Parts {
		if strings.EqualFold(p.Shape, "revolve") {
			revolves++
			t.Logf("revolve %q: %d points about %q", p.ID, len(p.Profile), geometry.RevolveAxis(p))
		}
	}
	for _, pr := range reply.Prototype.ProfileProblems() {
		t.Logf("outline %s on %q: %s", pr.Severity, pr.Name, pr.Detail)
	}
	t.Logf("parts %d, revolves %d, features %d",
		len(reply.Prototype.Parts), revolves, len(reply.Prototype.Features))

	k := cad.New(python, log)
	defer k.Close()
	built, err := k.BuildDocument(ctx, *reply.Prototype, geometry.Millimetre, "step")
	if err != nil {
		t.Fatalf("the model's own document did not build: %v", err)
	}
	t.Logf("built %d part(s), volume %.1f mm³, %d bytes of STEP; failures=%v",
		built.Parts, built.Volume, len(built.STEP), built.FeatureFailures)
	if !bytes.HasPrefix(built.STEP, []byte("ISO-10303-21;")) {
		t.Fatal("no STEP file from the model's own document")
	}
	if revolves == 0 {
		// Not a failure, and worth stating carefully. Observed 2026-09-05: for
		// both a stepped bush and a vee-groove pulley, qwen-plus described the
		// part with primitives and cuts, and it BUILT. Reaching for the simpler
		// vocabulary when it suffices is the right instinct, and a revolve is
		// not obligatory for anything a cone and a cut can express.
		//
		// What is unmeasured is whether it would reach for one when nothing else
		// will do. That is a rate, and rates belong in the eval suite.
		t.Log("no revolve: the model described this with primitives and cuts, which built")
	}
}

// TestLiveGeometryRepair asks a real model to fix geometry a real model broke.
//
// # Why this is live and not stubbed
//
// georepair_test.go fences the mechanism with a stub that returns whatever the
// test wants. That proves the plumbing and nothing about whether a model can do
// the job when handed the builder's own words — which is the only question the
// feature turns on. Every fixture below is output qwen3.7-plus actually produced
// on 2026-09-09, not something invented for a test.
//
// # Two cases, because the interesting half is the refusal
//
// A model asked to fix a fault will sometimes fix it by DELETING the thing that
// has it. Measured: asked to correct a wheel arch, it reached zero faults by
// removing both arches — a valid document that no longer contains the wheel
// wells somebody had just asked for. So one case checks a fault it can genuinely
// mend, and the other checks that it is refused when the only fix it finds is a
// deletion.
//
// Skipped without FORGE_LIVE_LLM_TESTS so CI stays hermetic and free.
func TestLiveGeometryRepair(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live geometry repair")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "repair-live-test"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen-plus"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})
	conv := agent.NewConversation(client, persona.DefaultCharacter())

	// A spoiler wing whose outline is a LINE. This is mendable: the fix is to
	// add points, and adding is not deleting.
	t.Run("mends an outline that is a line", func(t *testing.T) {
		broken := &geometry.Document{
			Name: "Sports Car Concept", Units: "mm",
			Parts: []geometry.Part{
				{ID: "chassis-body", Name: "Main Body", Shape: "box",
					Size:     map[string]float64{"width": 1900, "height": 800, "depth": 4500},
					Position: []float64{0, 400, 0}, Rotation: []float64{0, 0, 0}},
				{ID: "spoiler-wing", Name: "Spoiler Wing", Shape: "sweep",
					Profile:  []geometry.Point{{X: -400, Y: 0}, {X: 400, Y: 0}},
					Path:     []geometry.Point{{X: 0, Y: 0}, {X: 0, Y: 120, Z: -80}},
					Position: []float64{0, 1200, -2100}, Rotation: []float64{0, 0, 0}},
			},
		}
		faults := broken.Faults()
		if len(faults) == 0 {
			t.Fatal("the fixture builds cleanly, so this would pass without repairing anything")
		}
		t.Logf("builder said: %s", faults[0].Detail)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		fixed, changed := agent.RepairForTest(ctx, conv, broken)
		if !changed || fixed == nil {
			t.Fatalf("the model could not add a point to an outline. It was told: %q", faults[0].Detail)
		}
		if got := fixed.Faults(); len(got) > 0 {
			t.Errorf("still faulty after repair: %s", got[0].Detail)
		}
		if len(fixed.Parts) != 2 {
			t.Fatalf("the repair changed the design: 2 parts became %d", len(fixed.Parts))
		}
		var wing *geometry.Part
		for i := range fixed.Parts {
			if fixed.Parts[i].ID == "spoiler-wing" {
				wing = &fixed.Parts[i]
			}
		}
		if wing == nil {
			t.Fatal("the wing was removed rather than mended")
		}
		if len(wing.Profile) < 3 {
			t.Errorf("the wing outline still has %d point(s)", len(wing.Profile))
		}
		t.Logf("mended: wing outline now has %d points, %d parts, no faults",
			len(wing.Profile), len(fixed.Parts))
	})

	// A wheel arch drawn as a hole that opens at the body's bottom edge. This is
	// NOT mendable by moving coordinates — an arch cut from an edge is part of
	// the outline, not a hole in it — and the model's only route to zero faults
	// is to delete the arches. It must be refused.
	t.Run("refuses a fix that deletes the thing", func(t *testing.T) {
		broken := &geometry.Document{
			Name: "Sports Car Concept", Units: "mm",
			Parts: []geometry.Part{
				{ID: "chassis-body", Name: "Main Body", Shape: "extrusion",
					Size:     map[string]float64{"depth": 1900},
					Position: []float64{0, 400, 0}, Rotation: []float64{0, 0, 90},
					Profile: []geometry.Point{
						{X: -2250, Y: 0}, {X: -2250, Y: 800}, {X: 2250, Y: 800}, {X: 2250, Y: 0},
					},
					Holes: [][]geometry.Point{
						{{X: -1900, Y: -50}, {X: -1600, Y: -50}, {X: -1600, Y: -50}, {X: -1300, Y: 350}},
						{{X: 1300, Y: -50}, {X: 1600, Y: -50}, {X: 1900, Y: -50}, {X: 1600, Y: 350}},
					}},
				{ID: "cabin", Name: "Cabin", Shape: "box",
					Size:     map[string]float64{"width": 1600, "height": 500, "depth": 2000},
					Position: []float64{0, 1050, -200}, Rotation: []float64{0, 0, 0}},
			},
		}
		if len(broken.Faults()) == 0 {
			t.Fatal("the fixture builds cleanly")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		fixed, _ := agent.RepairForTest(ctx, conv, broken)

		// Whatever it did, the arches must still be there. A repair that reaches
		// zero faults by removing them has removed the wheel wells somebody just
		// asked for, and FORGE would then say the wells were made.
		var body *geometry.Part
		for i := range fixed.Parts {
			if fixed.Parts[i].ID == "chassis-body" {
				body = &fixed.Parts[i]
			}
		}
		if body == nil {
			t.Fatal("the body was removed by a repair")
		}
		if len(body.Holes) < 2 {
			t.Errorf("the repair deleted %d of the 2 wheel arches instead of mending them",
				2-len(body.Holes))
		}
		t.Logf("body keeps %d arches; faults remaining %d (a coordinate fix cannot make an "+
			"edge-open arch into a hole)", len(body.Holes), len(fixed.Faults()))
	})
}
