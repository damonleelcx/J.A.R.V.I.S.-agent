package agent_test

import (
	"context"
	"log/slog"
	"os"
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

// TestLiveScriptRepair asks a REAL model to fix a script a REAL build123d
// refused, and accepts the answer only when the kernel builds it.
//
// # Why this cannot be a stub
//
// Every fence in scriptrepair_test.go hands the model's answer to a runner the
// test controls, which proves the loop, the caps, the acceptance rule and the
// wiring — and nothing at all about the premise. The premise is that a model
// handed build123d's own words can fix the script it just wrote. If it cannot,
// this whole path is four extra model calls and a longer wait for the same
// missing part, and it should not ship.
//
// # Why the failures are the ones that actually happened
//
// The first case is verbatim the defect found on the live deployment: a profile
// swept or extruded as a WIRE, which build123d refuses with "A face or sketch
// must be provided". The second is a script that names a builder that does not
// exist, which is what a model reaches for when it is guessing.
func TestLiveScriptRepair(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live script repair test")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("set FORGE_CAD_PYTHON to a Python with build123d — this test refuses to " +
			"substitute a fake kernel, because a fake kernel cannot refuse a script")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "script-live-test"})
	kernel := cad.New(python, log).WithScripts(true)
	defer kernel.Close()

	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})

	conv := agent.NewConversation(client, persona.DefaultCharacter()).
		WithScripts(liveRunner{kernel})

	cases := []struct {
		name   string
		script string
		why    string
	}{
		{
			name: "a profile extruded as a wire",
			// The live defect, reduced. extrude() wants a face; a Polyline is a
			// wire, and build123d says so in the words the repair is handed.
			script: `pts = [(0, 0), (20, 0), (20, 10), (0, 10)]
profile = Polyline(*pts, close=True)
result = extrude(profile, amount=5)`,
			why: "this is the shape of the failure found on the live deployment",
		},
		{
			name:   "a builder that does not exist",
			script: `result = InvoluteGear(module=2, teeth=20, thickness=6)`,
			why:    "what a model reaches for when it is guessing at an API",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()

			// It really does not build to begin with. Without this the test can
			// pass by never exercising anything.
			if _, err := kernel.RunScript(ctx, tc.script); err == nil {
				t.Fatalf("the fixture builds as written, so this test proves nothing (%s)", tc.why)
			} else {
				t.Logf("build123d refuses it: %v", err)
			}

			doc := &geometry.Document{
				Name: "Live", Units: "mm",
				Parts: []geometry.Part{{ID: "part", Name: "Gear", Shape: "script", Script: tc.script}},
			}
			reply, changed := agent.ScriptRepairForTest(ctx, conv, doc)

			t.Logf("changed=%v\nnotes: %s\nscript now:\n%s", changed, reply.Repaired,
				reply.Prototype.Parts[0].Script)

			if !changed {
				t.Fatalf("the model could not fix a script build123d refused. %s\n"+
					"That is the premise of this whole path: if it does not hold, the loop is "+
					"four model calls and a longer wait for the same missing part.", reply.Repaired)
			}
			// The acceptance rule already ran the kernel. Running it once more
			// here is the independent check that the rule means what it says.
			if _, err := kernel.RunScript(ctx, reply.Prototype.Parts[0].Script); err != nil {
				t.Fatalf("a script was accepted that does not build: %v", err)
			}
		})
	}
}

// liveRunner is the real kernel behind the agent's narrow interface — the same
// adapter httpapi uses, kept here so the live test exercises the real thing
// rather than a copy of the loop.
type liveRunner struct{ k *cad.Kernel }

func (r liveRunner) RunScript(ctx context.Context, source string) error {
	_, err := r.k.RunScript(ctx, source)
	return err
}

// TestLiveGearTurn is the user's actual path: ask a real model for a gear and
// check that what comes back BUILDS.
//
// # Why this exists beside the repair test above
//
// That one hands the model a script known to be broken. This one asks the
// question a person asks and lets the model choose everything — whether to reach
// for a script at all, what to write, and whether it needs fixing. It is the only
// test here that can fail because the CONTRACT is wrong rather than the loop.
//
// "make me a gear" is the exact prompt that produced, on the live deployment:
// shape "gear" (a word that does not exist, drawn as a bounding box) before the
// availability line, and then a script that ran and raised inside build123d.
func TestLiveGearTurn(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live gear turn")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("set FORGE_CAD_PYTHON to a Python with build123d")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "gear-live-test"})
	kernel := cad.New(python, log).WithScripts(true)
	defer kernel.Close()

	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})

	conv := agent.NewConversation(client, persona.DefaultCharacter()).
		WithScripts(liveRunner{kernel})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	reply, err := conv.Respond(ctx, "", nil,
		"Make me a 20-tooth involute spur gear, module 2, 6mm thick.", "", nil, nil)
	if err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	t.Logf("speech: %s", reply.Speech)
	t.Logf("notes:  %s", reply.Repaired)
	if reply.Prototype == nil {
		t.Fatal("the turn produced no geometry at all")
	}

	// Whatever it chose, every scripted part it kept must build. That is the
	// promise: the turn does not hand back a part it has been told cannot exist.
	scripted := 0
	for _, p := range reply.Prototype.Parts {
		t.Logf("part %q shape=%q scripted=%v", p.Label(), p.Shape, p.Script != "")
		if !strings.EqualFold(strings.TrimSpace(p.Shape), "script") || p.Script == "" {
			continue
		}
		scripted++
		if _, err := kernel.RunScript(ctx, p.Script); err != nil {
			t.Errorf("the turn kept a scripted part that does not build.\n"+
				"part: %s\nerror: %v\nnotes: %s\nscript:\n%s",
				p.Label(), err, reply.Repaired, p.Script)
		}
	}
	if scripted == 0 {
		// Not a failure. A gear the vocabulary can express without a script is a
		// better answer than one that needs one, and `repeat` exists for exactly
		// this. Logged so a run that never exercises the path says so, rather
		// than reporting a pass that measured nothing.
		t.Log("the model built this without a script, so this run did not exercise the " +
			"script path. Not a defect — but not evidence for it either.")
	}
}

// TestLiveSketchLoop draws a REAL reference, reads it, builds against it, and
// compares the built model back to it.
//
// # Why this cannot be a stub
//
// Every fence in sketch_test.go controls both ends of the exchange, which proves
// the loop, the caps, the ordering and the safety rule — and nothing about the
// premise. The premise is that a generated picture is a useful reference for a
// CAD model. The spike says it is useful for FORM and wrong about NUMBERS
// (28-30 teeth for 20, no dimensions), and this is what checks that the code
// actually holds that line against a real generator.
func TestLiveSketchLoop(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live sketch loop")
	}
	image := envOrDefault("FORGE_LLM_IMAGE_MODEL", "wan2.7-image")
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "sketch-live-test"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		Vision:         envOrDefault("FORGE_LLM_VISION_MODEL", "qwen3.8-max"),
		Illustrator:    image,
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})
	conv := agent.NewConversation(client, persona.DefaultCharacter()).WithIllustrator(client)

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	const asked = "an L-shaped steel bracket, 80mm tall and 120mm long, 6mm thick, " +
		"with two 8mm bolt holes in the upright and a slot in the base"

	sketch := agent.SketchForTest(ctx, conv, asked)
	if sketch == nil {
		t.Fatal("nothing was drawn for a request that is plainly about an object. " +
			"That is the premise of this whole path.")
	}
	t.Logf("reference drawn: %s", sketch.Image)
	t.Logf("read as form:    %s", sketch.Form)

	if sketch.Form == "" {
		t.Fatal("the drawing was made and could not be read, so nothing would reach the prompt")
	}
	// ‼️ The reading must carry no numbers. A generated picture's counts are
	// wrong and its dimensions do not exist; a number here would be written into
	// the geometry prompt as if the person had asked for it.
	for _, digit := range "0123456789" {
		if strings.ContainsRune(sketch.Form, digit) {
			t.Errorf("the form read out of the drawing contains a NUMBER, and a generated "+
				"picture has no authority over any number:\n%s", sketch.Form)
			break
		}
	}

	// And the comparison, against a model that plainly matches and one that
	// plainly does not. The second is the one that decides whether this is
	// usable: a checker that finds nothing wrong with the wrong model is an
	// expensive way to add latency.
	// ‼️ The holes and the slot are HERE on purpose, and this fixture took three
	// attempts to write. Version one was two plain boxes, and the comparison
	// reported "the mounting holes clearly shown in the drawing are entirely
	// absent". Version two added the holes, and it reported "the elongated slot
	// shown in the drawing is entirely absent from the built model". Both were
	// RIGHT — the request names two bolt holes AND a slot, and the fixture had
	// neither and then only one.
	//
	// That is the strongest evidence this loop works that the repository has: the
	// checker found a real missing feature twice, unprompted, against a fixture
	// its author believed was correct. A fixture that does not match is not a
	// test of a false positive; it is a true positive wearing the wrong label.
	right := &geometry.Document{
		Name: "Bracket", Units: "mm",
		Parts: []geometry.Part{
			{ID: "upright", Name: "Upright", Shape: "box", Color: "#8899aa",
				Size: map[string]float64{"width": 6, "height": 80, "depth": 60}, Position: []float64{0, 40, 0}},
			{ID: "base", Name: "Base", Shape: "box", Color: "#aa8899",
				Size: map[string]float64{"width": 120, "height": 6, "depth": 60}, Position: []float64{60, 3, 0}},
			{ID: "hole-a", Name: "Bolt hole A", Shape: "cylinder", Color: "#222222",
				Size: map[string]float64{"radius": 4, "height": 20}, Position: []float64{0, 60, -15}, Rotation: []float64{0, 0, 90}},
			{ID: "hole-b", Name: "Bolt hole B", Shape: "cylinder", Color: "#222222",
				Size: map[string]float64{"radius": 4, "height": 20}, Position: []float64{0, 60, 15}, Rotation: []float64{0, 0, 90}},
			{ID: "slot", Name: "Slot", Shape: "box", Color: "#222222",
				Size: map[string]float64{"width": 70, "height": 20, "depth": 12}, Position: []float64{65, 3, 0}},
		},
		Features: []geometry.Feature{
			{ID: "drill", Op: "cut", Of: "upright", With: []string{"hole-a", "hole-b"}},
			{ID: "mill", Op: "cut", Of: "base", With: []string{"slot"}},
		},
	}
	wrong := &geometry.Document{
		Name: "Sphere", Units: "mm",
		Parts: []geometry.Part{
			{ID: "ball", Name: "Ball", Shape: "sphere", Color: "#8899aa",
				Size: map[string]float64{"radius": 50}},
		},
	}
	rp := agent.MatchForTest(ctx, conv, right, sketch)
	wp := agent.MatchForTest(ctx, conv, wrong, sketch)
	t.Logf("against an L bracket: %d problem(s)", len(rp))
	for _, p := range rp {
		t.Logf("   %s", p.Detail)
	}
	t.Logf("against a sphere:     %d problem(s)", len(wp))
	for _, p := range wp {
		t.Logf("   %s", p.Detail)
	}
	if len(wp) == 0 {
		t.Error("the comparison found nothing wrong with a SPHERE built for an L bracket. " +
			"A checker that passes the wrong model is an expensive way to add latency.")
	}
	if len(rp) > 0 {
		// Not a failure on its own — a false positive here is survivable because
		// the acceptance rule refuses a damaging correction. Logged loudly
		// because a checker that complains about the right answer drives repairs
		// that make it worse, and this repository has already deleted one rule
		// for exactly that.
		t.Logf("‼️ the comparison complained about a model that matches. Survivable — the "+
			"acceptance rule refuses a resize — but watch it: %s", rp[0].Detail)
	}
}

// liveSolids is the real kernel behind the agent's render contract — the same
// conversion httpapi uses, sharing geometry.TrianglesFrom rather than copying it.
type liveSolids struct{ k *cad.Kernel }

func (r liveSolids) BuildSurface(ctx context.Context, doc *geometry.Document) ([]geometry.RenderPart, error) {
	unit, known := geometry.ParseUnit(doc.Units)
	if !known {
		unit = geometry.Millimetre
	}
	built, err := r.k.BuildMesh(ctx, *doc, unit)
	if err != nil {
		return nil, err
	}
	out := make([]geometry.RenderPart, 0, len(built.Mesh))
	for _, m := range built.Mesh {
		if tris := geometry.TrianglesFrom(m.Vertices, m.Triangles); len(tris) > 0 {
			out = append(out, geometry.RenderPart{ID: m.ID, Triangles: tris})
		}
	}
	return out, nil
}

// TestLiveKernelRenderShowsTheHole is the whole point of rendering the kernel's
// surface, put to a real kernel and a real vision model.
//
// # What it decides
//
// Every check that reads a picture used to read a render of the DESCRIPTION, in
// which a cut is not performed — so a bolt hole is a solid post, and the vision
// model said exactly that about a correct plate: "a solid cylinder protruding
// from the plate surface rather than a hole passing through it." That was
// patched by apologising for the picture. This renders the real surface instead.
//
// The claim to check is not "the code runs" — the stub fences cover that. It is
// that the kernel's picture SHOWS THE HOLE, and that the same model asked the
// same closed question about the same document answers differently depending on
// which picture it is given. If both pictures read the same, this change bought
// nothing and the apologies were the right answer after all.
func TestLiveKernelRenderShowsTheHole(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("set FORGE_CAD_PYTHON to a Python with build123d — a fake kernel cannot cut")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "render-live-test"})
	kernel := cad.New(python, log)
	defer kernel.Close()

	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		Vision:         envOrDefault("FORGE_LLM_VISION_MODEL", "qwen3.8-max"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})

	// A plate with a bolt hole cut clean through it, and nothing else.
	doc := &geometry.Document{
		Name: "Plate", Units: "mm",
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box", Color: "#8899aa",
				Size: map[string]float64{"width": 120, "height": 10, "depth": 80}},
			{ID: "hole", Name: "Bolt Hole", Shape: "cylinder", Color: "#222222",
				Size: map[string]float64{"radius": 12, "height": 40}},
		},
		Features: []geometry.Feature{{ID: "drill", Op: "cut", Of: "plate", With: []string{"hole"}}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	described := agent.NewConversation(client, persona.DefaultCharacter())
	fromKernel := agent.NewConversation(client, persona.DefaultCharacter()).
		WithSolids(liveSolids{kernel})

	dImg, dKernel := agent.RenderForTest(ctx, described, doc)
	kImg, kKernel := agent.RenderForTest(ctx, fromKernel, doc)

	if dKernel {
		t.Fatal("the deployment with no kernel produced a kernel render")
	}
	if !kKernel {
		t.Fatal("the kernel did not build the surface, so this test compares two copies of " +
			"the same picture and proves nothing")
	}
	if dImg == kImg {
		t.Fatal("‼️ the two pictures are byte-identical. The kernel render is not reaching " +
			"the rasterizer, and every apology this change removes is still needed.")
	}
	t.Logf("described render %d bytes, kernel render %d bytes", len(dImg), len(kImg))

	// The closed question, asked of both pictures with NO apology attached to
	// either — so the only thing that differs is the picture.
	const q = `Look at these four orthographic views of a metal plate. Answer JSON only:
{"hole_through_plate": true|false, "solid_post_on_plate": true|false}
"hole_through_plate" is true if you can see an opening passing through the plate.
"solid_post_on_plate" is true if a solid cylinder stands on or sticks out of it.`

	ask := func(img string) string {
		resp, err := agent.AskVisionForTest(ctx, described, q, img)
		if err != nil {
			t.Fatalf("could not look: %v", err)
		}
		return resp
	}
	dSaw, kSaw := ask(dImg), ask(kImg)
	t.Logf("described picture: %s", dSaw)
	t.Logf("kernel picture:    %s", kSaw)

	if !strings.Contains(strings.ToLower(kSaw), `"hole_through_plate": true`) &&
		!strings.Contains(strings.ToLower(kSaw), `"hole_through_plate":true`) {
		t.Errorf("the KERNEL's picture does not show a hole through the plate. That is the "+
			"entire reason for building the surface rather than describing it:\n%s", kSaw)
	}
}
