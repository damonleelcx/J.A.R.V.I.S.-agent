package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// solidsStub builds a surface a test decides.
type solidsStub struct {
	parts []geometry.RenderPart
	err   error
	calls int
}

func (s *solidsStub) BuildSurface(context.Context, *Prototype) ([]geometry.RenderPart, error) {
	s.calls++
	return s.parts, s.err
}

// cube returns one part's surface, enough for the rasterizer to draw something.
func cube(id string) geometry.RenderPart {
	t := func(a, b, c [3]float64) geometry.Triangle {
		return geometry.Triangle{A: a, B: b, C: c, Normal: [3]float64{0, 0, 1}}
	}
	return geometry.RenderPart{ID: id, Triangles: []geometry.Triangle{
		t([3]float64{0, 0, 0}, [3]float64{10, 0, 0}, [3]float64{10, 10, 0}),
		t([3]float64{0, 0, 0}, [3]float64{10, 10, 0}, [3]float64{0, 10, 0}),
	}}
}

func drilledPlate() *Prototype {
	return &geometry.Document{
		Name: "Plate", Units: "mm",
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size: map[string]float64{"width": 120, "height": 10, "depth": 80}},
			{ID: "hole", Name: "Bolt Hole", Shape: "cylinder",
				Size: map[string]float64{"radius": 5, "height": 30}},
		},
		Features: []geometry.Feature{{ID: "drill", Op: "cut", Of: "plate", With: []string{"hole"}}},
	}
}

// ‼️ A kernel render carries NO apology, because on it a hole really is a hole.
//
// # What this closes
//
// The two notes exist because geometry.Tessellate performs no boolean and runs
// no script, so a bolt hole is a solid post and a scripted part is a bounding
// box. Both were live false positives. On the kernel's surface neither is true —
// the cut has been made and the script has run — and repeating the apology there
// would teach the vision model to ignore a real defect: told "a solid where a
// hole should be is correct here", it would stop reporting a hole that genuinely
// failed to cut.
//
// So the note is a property of WHICH PICTURE this is, and this is the fence that
// holds those two apart.
func TestRender_TheKernelPictureCarriesNoApology(t *testing.T) {
	doc := drilledPlate()

	t.Run("described render explains the solid post", func(t *testing.T) {
		c := &Conversation{}
		sheet := c.render(context.Background(), doc)
		if sheet.FromKernel {
			t.Fatal("a deployment with no kernel reported a kernel render")
		}
		if sheet.Image == "" {
			t.Fatal("nothing was drawn at all")
		}
		note := describedRenderNote(doc, sheet)
		if !strings.Contains(note, "Bolt Hole") || !strings.Contains(note, "TOOLS that cut") {
			t.Errorf("the described render does not explain that the hole is drawn as a "+
				"solid, which is the false positive that was live:\n%s", note)
		}
	})

	t.Run("kernel render says nothing about tools or scripts", func(t *testing.T) {
		solids := &solidsStub{parts: []geometry.RenderPart{cube("plate")}}
		c := (&Conversation{}).WithSolids(solids)
		sheet := c.render(context.Background(), doc)
		if !sheet.FromKernel {
			t.Fatal("the kernel built a surface and the render did not say so")
		}
		if note := describedRenderNote(doc, sheet); note != "" {
			t.Errorf("the kernel's picture carries an apology for something that is not true "+
				"of it. Told a solid where a hole should be is correct, the checker stops "+
				"reporting a hole that really failed to cut:\n%s", note)
		}
	})
}

// A kernel that cannot build falls back, and says which picture this is.
//
// Losing the turn over a busy kernel would be strictly worse than looking at the
// described picture and knowing that is what it is.
func TestRender_FallsBackAndSaysSo(t *testing.T) {
	doc := drilledPlate()
	for _, tc := range []struct {
		name   string
		solids *solidsStub
	}{
		{"the kernel errored", &solidsStub{err: errors.New("the kernel did not answer")}},
		{"the kernel built nothing", &solidsStub{parts: nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := (&Conversation{}).WithSolids(tc.solids)
			sheet := c.render(context.Background(), doc)
			if tc.solids.calls != 1 {
				t.Errorf("the kernel was asked %d times, want 1", tc.solids.calls)
			}
			if sheet.Image == "" {
				t.Error("no picture at all — the checks that read it are then silently skipped")
			}
			if sheet.FromKernel {
				t.Error("a fallback render claimed to be the kernel's, so the apology it " +
					"needs would be withheld")
			}
			if describedRenderNote(doc, sheet) == "" {
				t.Error("the fallback picture carries no apology, and it is the one that " +
					"needs it")
			}
		})
	}
}

// The kernel is asked ONCE per turn, not once per check.
//
// Building the surface runs the kernel and, for a scripted part, the script —
// up to 30 seconds each. Rendering inside every check would pay that twice, and
// a document with three scripted parts would spend three minutes drawing
// pictures of itself.
func TestRender_TheKernelIsAskedOncePerTurn(t *testing.T) {
	solids := &solidsStub{parts: []geometry.RenderPart{cube("plate")}}
	stub := &sketchStub{problems: `{"problems":[]}`}
	c := (&Conversation{client: stub}).WithSolids(solids)

	reply := &Reply{Prototype: drilledPlate()}
	sheet := c.render(context.Background(), reply.Prototype)
	c.repairIfItLooksWrong(context.Background(), reply, "a plate", &sheet)
	c.repairAgainstSketch(context.Background(), reply,
		&Sketch{Image: "https://example.invalid/ref.png"}, &sheet, nil)

	if solids.calls != 1 {
		t.Errorf("the kernel built the surface %d times for one turn. Each build runs every "+
			"script in the document, so this is seconds a person waits through for a picture "+
			"that already existed", solids.calls)
	}
}
