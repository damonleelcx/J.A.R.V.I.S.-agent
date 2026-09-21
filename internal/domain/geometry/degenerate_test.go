package geometry_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Issue 7. A part whose radius is zero built a solid of volume 0 — OCCT accepts
// it, the mesh path agrees with it, and the meaningless result travels all the
// way to the exported file. It is refused here instead, by the name of the
// dimension that collapsed.
func TestSolids_AZeroRadiusPartIsRefusedByTheNameOfTheDimension(t *testing.T) {
	doc := geometry.Document{
		Name: "bracket", Units: "mm",
		Parts: []geometry.Part{
			{ID: "plate", Shape: "box",
				Size: map[string]float64{"width": 60, "height": 6, "depth": 60}},
			{ID: "boss", Name: "Boss", Shape: "cylinder",
				Size: map[string]float64{"radius": 0, "height": 8}},
		},
	}
	got, notes := geometry.Solids(doc, geometry.Millimetre)
	if len(got) != 1 || got[0].ID != "plate" {
		t.Fatalf("built %d solids (%v); the zero-radius part must not be sent to the kernel",
			len(got), ids(got))
	}
	said := strings.Join(notes, "\n")
	if !strings.Contains(said, "Boss") || !strings.Contains(said, `"radius"`) {
		t.Errorf("the notes do not name the part and the dimension that collapsed:\n%s", said)
	}
	if !strings.Contains(said, "not in this file") {
		t.Errorf("a refused part must say it is absent; got:\n%s", said)
	}
}

// The remedy names the FIELD and, when the number came from an expression, the
// expression — which is the thing to fix. A resolved zero is almost always a
// parameter that failed or a unit conversion that collapsed, and this is the
// last point at which either is still in view.
func TestSolids_ACollapsedDimensionNamesTheExpressionItResolvedFrom(t *testing.T) {
	doc := geometry.Document{
		Name: "bracket", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "wall", Value: 0, Unit: "mm"}},
		Parts: []geometry.Part{{ID: "post", Name: "Post", Shape: "cylinder",
			Size:     map[string]float64{"radius": 4, "height": 10},
			SizeFrom: map[string]string{"radius": "wall / 2"}}},
	}
	// Not bound here on purpose. The radius the document TYPED is 4, and only
	// the expression collapses it — so a check that read the typed number would
	// see a perfectly good part, which is the upstream mistake travelling.
	said := strings.Join(faultDetails(doc.Faults()), "\n")
	if !strings.Contains(said, `"radius"`) || !strings.Contains(said, "wall / 2") {
		t.Errorf("the refusal must name the field and the expression behind it; got:\n%s", said)
	}
	if doc.Parts[0].Size["radius"] != 4 {
		t.Error("Faults rewrote the caller's document; it must bind into a copy")
	}
}

// And it is a FAULT, not a note: the part is not in the model afterwards, which
// is what hands it to the repair loop.
func TestFaults_AZeroRadiusPartIsAFault(t *testing.T) {
	doc := geometry.Document{
		Name: "bracket", Units: "mm",
		Parts: []geometry.Part{{ID: "boss", Name: "Boss", Shape: "cylinder",
			Size: map[string]float64{"radius": 0, "height": 8}}},
	}
	faults := doc.Faults()
	if len(faults) == 0 {
		t.Fatal("a part of no volume is a part not in the model, and Faults did not say so")
	}
	if faults[0].Severity != geometry.Error || faults[0].Name != "Boss" {
		t.Errorf("fault = %+v, want an Error named after the part", faults[0])
	}
}

// Negative is refused with zero, for the same reason and more so: a negative
// dimension is not a small part, it is a direction nobody asked for.
func TestFaults_ANegativeDimensionIsRefusedToo(t *testing.T) {
	doc := geometry.Document{
		Name: "bracket", Units: "mm",
		Parts: []geometry.Part{{ID: "rail", Name: "Rail", Shape: "box",
			Size: map[string]float64{"width": 20, "height": -4, "depth": 60}}},
	}
	said := strings.Join(faultDetails(doc.Faults()), "\n")
	if !strings.Contains(said, `"height"`) {
		t.Errorf("a negative height must be refused by name; got:\n%s", said)
	}
}

// The answer to the issue's open question, fenced. A dimension a shape does not
// READ is never refused, so a section with no thickness and a plane with no
// height — both legitimate — are not made into errors by a rule aimed at a
// zero-radius cylinder. A refusal that fires on a correct document is how a
// refusal stops being believed.
func TestFaults_ADimensionAShapeDoesNotReadIsNotRefused(t *testing.T) {
	doc := geometry.Document{
		Name: "drawing", Units: "mm",
		Parts: []geometry.Part{
			// A plane has width and depth in this contract and no height at all.
			{ID: "ground", Name: "Ground", Shape: "plane",
				Size: map[string]float64{"width": 100, "depth": 100, "height": 0}},
			// A cone's top radius is zero BY DEFINITION; solid.go sets it itself.
			{ID: "tip", Name: "Tip", Shape: "cone",
				Size: map[string]float64{"radius": 5, "height": 10, "radius_top": 0}},
			// A cylinder whose top radius is zero is a cone, which is a real shape.
			{ID: "taper", Name: "Taper", Shape: "cylinder",
				Size: map[string]float64{"radius": 5, "height": 10, "radius_top": 0}},
		},
	}
	if faults := doc.Faults(); len(faults) != 0 {
		t.Errorf("refused a correct document: %v", faultDetails(faults))
	}
}

// A dimension nobody stated is not a refusal either. sizeOr fills one in and
// says it did, and refusing a part over a number FORGE chose would be FORGE
// refusing its own arithmetic.
func TestSolids_AMissingDimensionIsStillDefaultedRatherThanRefused(t *testing.T) {
	doc := geometry.Document{
		Name: "bracket", Units: "mm",
		Parts: []geometry.Part{{ID: "boss", Name: "Boss", Shape: "cylinder",
			Size: map[string]float64{"height": 8}}},
	}
	got, notes := geometry.Solids(doc, geometry.Millimetre)
	if len(got) != 1 {
		t.Fatalf("built %d solids, want 1 — a missing radius is defaulted, not refused", len(got))
	}
	if !strings.Contains(strings.Join(notes, "\n"), "no radius was given") {
		t.Errorf("the default was not reported: %v", notes)
	}
}

func ids(s []geometry.Solid) []string {
	out := make([]string, 0, len(s))
	for _, one := range s {
		out = append(out, one.ID)
	}
	return out
}

func faultDetails(problems []geometry.Problem) []string {
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, p.Name+" "+p.Detail)
	}
	return out
}
