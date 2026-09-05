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
			"stiffening ribs. Give me the parameters I can change.", "", nil)
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
			"rounded outer edge.", "", nil)
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
