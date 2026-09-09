package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// TestLiveRevisionKeepsTheRestOfTheModel reshapes one part of a finished car
// and measures what happens to the twelve parts nobody asked about.
//
// # The failure this measures
//
// "Make the body less boxy" names one part out of thirteen. The model answers
// with a WHOLE prototype, so every part it does not retype is deleted — the
// document is the model, and omission is removal. That is not a bug in the
// wire format; it is what a rewrite means. The question is whether the model
// understands it.
//
// Measured 2026-09-09 against qwen3.7-plus, three runs from this exact
// document, BEFORE the contract said anything about it:
//
//	run 1 — the body came back under a different id, so the old body survived
//	        alongside the new one and the car had two bodies
//	run 2 — the spoiler (wing + both supports) was gone, unmentioned
//	run 3 — the spoiler was gone AND the new outline crossed itself, so the
//	        body did not build at all
//
// 0/3. Not one of those three was reported to the person, who had asked about
// the body and would have been looking at the body.
//
// # What it asserts, and what it does not
//
// The two failures are not the same KIND of thing, and are asserted
// differently:
//
//   - A silent disappearance is OUR bug. vanished.go is supposed to catch
//     every one of them, so a loss that reaches the reader unannounced means
//     that safety net has a hole. Any occurrence fails.
//   - Geometry that will not build is the MODEL having a bad day, and
//     georepair.go exists because it always will. Curing every slip is not
//     achievable, so this is counted and reported as a rate, and only a run
//     where NOTHING built is treated as a failure — that would mean the
//     contract is not working rather than a model missing once.
//
// It does NOT assert the body is prettier: "less boxy" has no machine test,
// and a test that guessed at one would fail on taste rather than on a
// regression here.
//
// Runs once by default. FORGE_LIVE_RUNS=n repeats it, because the build number
// above is a rate and a single green run is not evidence that a rate moved.
func TestLiveRevisionKeepsTheRestOfTheModel(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live revision test")
	}
	base := loadPrototype(t, "testdata/live-sports-car.json")
	if len(base.Parts) < 5 {
		t.Fatalf("the fixture has %d parts; this test is about what a revision does to the "+
			"parts nobody mentioned, and needs a model with several of them", len(base.Parts))
	}

	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "revision-live-test"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL: envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:  os.Getenv("FORGE_LLM_API_KEY"),
		// The same default the shipping config uses (config.go), so this
		// measures the model production actually talks to. A stale default here
		// measures a model nobody runs — or, as on 2026-09-09, one the provider
		// has retired, and every run fails for a reason that has nothing to do
		// with the behaviour under test.
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})
	conv := agent.NewConversation(client, persona.DefaultCharacter())

	runs := 1
	if n, err := strconv.Atoi(os.Getenv("FORGE_LIVE_RUNS")); err == nil && n > 0 {
		runs = n
	}
	var built int
	for i := 1; i <= runs; i++ {
		if revisionRun(t, conv, base, i) {
			built++
		}
	}
	t.Logf("RESULT: %d/%d revisions produced geometry that builds", built, runs)
	if built == 0 {
		t.Errorf("not one of %d revisions built. One model slip is expected and is what the "+
			"repair pass is for; none of them building means the contract is not working", runs)
	}
}

// revisionRun performs one revision. It FAILS the test outright on a silent
// loss, and returns whether the result builds so the caller can score that as
// a rate.
func revisionRun(t *testing.T, conv *agent.Conversation, base *geometry.Document, n int) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	reply, err := conv.Respond(ctx, "", nil, "make the body less boxy", "", base, nil)
	if err != nil {
		t.Errorf("run %d: the live turn failed: %v", n, err)
		return false
	}
	if reply.Prototype == nil && reply.PrototypeEdit == nil {
		t.Errorf("run %d: a revision request produced no geometry at all", n)
		return false
	}
	after := reply.Prototype
	if after == nil {
		t.Errorf("run %d: an edit reached the caller unresolved", n)
		return false
	}

	gone := missingParts(base, after)
	told := reply.Repaired

	if len(gone) > 0 {
		// Only unannounced losses count. A removal the reply names is a thing
		// the person can undo; a silent one is a thing they will find weeks later.
		var unannounced []string
		for _, name := range gone {
			if !strings.Contains(told, name) {
				unannounced = append(unannounced, name)
			}
		}
		if len(unannounced) > 0 {
			t.Errorf("run %d: %d part(s) vanished with no word to the reader: %s\nnotice was: %q",
				n, len(unannounced), strings.Join(unannounced, ", "), told)
		} else {
			t.Logf("run %d: removed %s — and said so", n, strings.Join(gone, ", "))
		}
	}

	// The envelope must survive. "Less boxy" is a styling change; a body that
	// comes back 2.4x wider has not been restyled, it has been misdrawn.
	//
	// Observed live 2026-09-09: the model drew the car's SIDE elevation as the
	// outline (4500 mm long) and gave 4500 again as the extrusion depth, so a
	// body that should have stayed 1900 mm wide became a 4.5 m square slab. The
	// length was used twice and the width was thrown away. Nothing refused it,
	// and the parts panel showed "? x ? x 4500 mm" — the one number that was
	// right — so it looked exactly like a correct body.
	// A resize is allowed to happen — the model does it and no wording stopped
	// it (see turned.go). What is NOT allowed is for it to reach the reader
	// unannounced, which is the same standard vanished.go is held to.
	if grew := envelopeGrowth(base, after, "chassis-body"); grew > 1.5 {
		if !strings.Contains(told, "Main Body") {
			t.Errorf("run %d: the body came back %.1fx its size and the reply never says so.\n"+
				"notice: %q\n%s", n, grew, told, describePart(after, "chassis-body"))
		} else {
			t.Logf("run %d: body resized %.1fx — repaired or reported\n%s",
				n, grew, describePart(after, "chassis-body"))
		}
	}

	faults := after.Faults()
	for _, f := range faults {
		// Logged, not failed: see the rate note on the parent test. The repair
		// pass has already had its go by the time this runs, so a fault here is
		// one it could not cure.
		t.Logf("run %d: does not build after repair: %s — %s", n, f.Name, f.Detail)
	}

	t.Logf("run %d: %d parts in, %d out, builds=%v; notice: %q; speech: %s",
		n, len(base.Parts), len(after.Parts), len(faults) == 0, told, reply.Speech)
	return len(faults) == 0
}

// missingParts names the parts of before that the after document neither keeps
// nor consumes as a cutting tool, by the label a person would recognise.
func missingParts(before, after *geometry.Document) []string {
	kept := map[string]bool{}
	for _, p := range after.Parts {
		kept[p.ID] = true
	}
	consumed := map[string]bool{}
	for _, f := range append(append([]geometry.Feature{}, before.Features...), after.Features...) {
		for _, id := range f.With {
			consumed[id] = true
		}
	}
	var gone []string
	for _, p := range before.Parts {
		if !kept[p.ID] && !consumed[p.ID] {
			gone = append(gone, p.Label())
		}
	}
	sort.Strings(gone)
	return gone
}

// envelopeGrowth is how much bigger one part's bounding box got, on its worst
// axis. 1.0 is unchanged. Zero when either side cannot be measured, which is a
// miss rather than a wrong number.
// describePart says exactly what the model wrote, so a failing run can be
// diagnosed from the log instead of guessed at. The first attempt to fix this
// aimed at "extrusion" because the reply said so; the documents were sweeps.
func describePart(d *geometry.Document, id string) string {
	for _, p := range d.Parts {
		if p.ID != id {
			continue
		}
		lo := [2]float64{math.Inf(1), math.Inf(1)}
		hi := [2]float64{math.Inf(-1), math.Inf(-1)}
		for _, pt := range p.Profile {
			lo[0], hi[0] = math.Min(lo[0], pt.X), math.Max(hi[0], pt.X)
			lo[1], hi[1] = math.Min(lo[1], pt.Y), math.Max(hi[1], pt.Y)
		}
		out := fmt.Sprintf("  shape=%s size=%v rotation=%v", p.Shape, p.Size, p.Rotation)
		if len(p.Profile) > 0 {
			out += fmt.Sprintf("\n  outline: %d pts spanning %.0f x %.0f",
				len(p.Profile), hi[0]-lo[0], hi[1]-lo[1])
		}
		if len(p.Path) > 0 {
			plo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
			phi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
			for _, pt := range p.Path {
				v := [3]float64{pt.X, pt.Y, pt.Z}
				for i := 0; i < 3; i++ {
					plo[i], phi[i] = math.Min(plo[i], v[i]), math.Max(phi[i], v[i])
				}
			}
			out += fmt.Sprintf("\n  path: %d pts spanning %.0f x %.0f x %.0f",
				len(p.Path), phi[0]-plo[0], phi[1]-plo[1], phi[2]-plo[2])
		}
		return out
	}
	return "  (the part is not in the document)"
}

func envelopeGrowth(before, after *geometry.Document, id string) float64 {
	b, okB := partExtent(before, id)
	a, okA := partExtent(after, id)
	if !okB || !okA {
		return 0
	}
	worst := 0.0
	for i := 0; i < 3; i++ {
		if b[i] <= 0 {
			continue
		}
		if r := a[i] / b[i]; r > worst {
			worst = r
		}
	}
	return worst
}

// partExtent measures a part the way the viewport draws it: through the mesh
// builder, so the outline of an extrusion counts and a stated size that the
// shape does not use does not. Reading "size" alone would report the square
// slab as 4500 deep and nothing else, which is exactly the blindness under test.
func partExtent(d *geometry.Document, id string) ([3]float64, bool) {
	m := geometry.Tessellate(*d, geometry.Millimetre)
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	seen := false
	for _, g := range m.Groups {
		if g.PartID != id {
			continue
		}
		for _, t := range g.Triangles {
			for _, v := range [3][3]float64{t.A, t.B, t.C} {
				for i := 0; i < 3; i++ {
					lo[i] = math.Min(lo[i], v[i])
					hi[i] = math.Max(hi[i], v[i])
				}
			}
			seen = true
		}
	}
	if !seen {
		return [3]float64{}, false
	}
	return [3]float64{hi[0] - lo[0], hi[1] - lo[1], hi[2] - lo[2]}, true
}

func loadPrototype(t *testing.T, path string) *geometry.Document {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d geometry.Document
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}
