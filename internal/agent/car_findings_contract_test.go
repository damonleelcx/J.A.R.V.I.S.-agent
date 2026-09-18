package agent

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Fences for what the 2026-09-15 live cars showed the contract did not say.

// contractWords is the contract with its line breaks and indents folded, so a
// sentence is found wherever it wraps.
func contractWords() string { return strings.Join(strings.Fields(geometryContract), " ") }

// extentsOf is the size along x, y and z of what a document draws.
func extentsOf(d geometry.Document) [3]float64 {
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, tri := range geometry.Tessellate(d, geometry.Millimetre).Triangles() {
		for _, v := range [3][3]float64{tri.A, tri.B, tri.C} {
			for i := range v {
				lo[i], hi[i] = math.Min(lo[i], v[i]), math.Max(hi[i], v[i])
			}
		}
	}
	return [3]float64{hi[0] - lo[0], hi[1] - lo[1], hi[2] - lo[2]}
}

// ‼️ The contract says which way a cylinder stands, and it is the way FORGE draws one.
//
// It never said. The build-goal car laid its tyres "horizontally like a plate" and
// stood its rotors wrong; its tubes were turned [90, 0, 0], which faces a wheel
// forward. A sentence about an axis is only worth having if it is the code's axis,
// so the drawing is measured here, unturned and turned as the sentence says.
func TestTheContractSaysACylinderStandsOnItsOwnY(t *testing.T) {
	words := contractWords()
	for _, want := range []string{`A "cylinder" or "cone" STANDS UPRIGHT: its "height" runs along the part's own Y`,
		`has its axles along X, and a wheel is turned "rotation": [0, 0, 90]`} {
		if !strings.Contains(words, want) {
			t.Errorf("the contract does not say %q", want)
		}
	}
	wheel := func(rotation []float64) geometry.Document {
		return geometry.Document{Units: "mm", Parts: []geometry.Part{{ID: "wheel", Shape: "cylinder",
			Size: map[string]float64{"radius": 300, "height": 200}, Position: []float64{0, 0, 0}, Rotation: rotation}}}
	}
	if e := extentsOf(wheel([]float64{0, 0, 0})); math.Abs(e[1]-200) > 1 || math.Abs(e[0]-600) > 5 {
		t.Errorf("an unturned cylinder is %.0f x %.0f x %.0f; the contract says its height runs along Y", e[0], e[1], e[2])
	}
	if e := extentsOf(wheel([]float64{0, 0, 90})); math.Abs(e[0]-200) > 1 || math.Abs(e[1]-600) > 5 {
		t.Errorf("a cylinder turned [0, 0, 90] is %.0f x %.0f x %.0f; the contract says its axis is then X", e[0], e[1], e[2])
	}
}

// ‼️ The contract says where a polar pattern turns its copies, and the expansion agrees.
//
// The 2026-09-15 in-request car buried each lug nut of a pattern inside its hub, rim
// and tyre. The contract showed a polar pattern once, on a flange lying flat, and
// never said the axis passes through the frame's origin, that the child's position
// must be off it, or that it has to be the axis of the thing the copies go round.
func TestTheContractSaysWhereAPolarPatternTurns(t *testing.T) {
	words := contractWords()
	for _, want := range []string{`A "polar" pattern turns its copies about an axis THROUGH THE ORIGIN of the frame the child is measured in`,
		`[57, 0, 0] with "about": "y" is a ring of radius 57 round Y, and a child ON the axis puts every copy in the same place`,
		`"about" must be the axis of the thing the copies go round`} {
		if !strings.Contains(words, want) {
			t.Errorf("the contract does not say %q", want)
		}
	}
	ring := func(position []float64) []geometry.Part {
		d := geometry.Document{Units: "mm", Root: "wheel",
			Definitions: []geometry.Part{{ID: "nut", Shape: "cylinder", Size: map[string]float64{"radius": 8, "height": 15}}},
			Assemblies: []geometry.Assembly{{ID: "wheel", Children: []geometry.Child{{ID: "nut", Ref: "nut",
				Position: position, Pattern: &geometry.Pattern{Kind: "polar", Count: 5, About: "y"}}}}}}
		return d.PlacedParts()
	}
	seen := map[[3]int]bool{}
	for _, p := range ring([]float64{57, 0, 0}) {
		if r := math.Hypot(p.Position[0], p.Position[2]); math.Abs(r-57) > 1e-6 || math.Abs(p.Position[1]) > 1e-9 {
			t.Errorf("%s is at %v, not on the radius-57 ring round Y the contract describes", p.ID, p.Position)
		}
		seen[[3]int{int(math.Round(p.Position[0])), 0, int(math.Round(p.Position[2]))}] = true
	}
	if len(seen) != 5 {
		t.Errorf("five copies off the axis landed in %d places", len(seen))
	}
	for _, p := range ring([]float64{0, 20, 0}) {
		if math.Abs(p.Position[0]) > 1e-9 || math.Abs(p.Position[1]-20) > 1e-9 || math.Abs(p.Position[2]) > 1e-9 {
			t.Errorf("%s of a child on the axis is at %v; the contract says every copy is in the same place", p.ID, p.Position)
		}
	}
}

// ‼️ A repair of a buried pattern copy is told which child and which pattern put it
// there, not only the path it was placed at.
func TestInterference_ARepairIsToldWhichChildPlacesABuriedCopy(t *testing.T) {
	doc := &geometry.Document{Name: "car", Units: "mm", Root: "car",
		Definitions: []geometry.Part{
			{ID: "rim", Shape: "cylinder", Size: map[string]float64{"radius": 200, "height": 180}},
			{ID: "lug-nut", Shape: "cylinder", Size: map[string]float64{"radius": 10, "height": 20}},
		},
		Assemblies: []geometry.Assembly{
			{ID: "wheel", Children: []geometry.Child{{ID: "rim", Ref: "rim"},
				{ID: "nut", Ref: "lug-nut", Position: []float64{57, 0, 0}, Pattern: &geometry.Pattern{Kind: "polar", Count: 5, About: "x"}}}},
			{ID: "car", Children: []geometry.Child{{ID: "left-wheel", Ref: "wheel"}}},
		}}
	stub := &repairStub{reply: mustJSON(t, doc)}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: doc}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Interferences: []geometry.Interference{{
		A: "left-wheel/nut-3", B: "left-wheel/rim", ALabel: "Lug nut 3", BLabel: "Rim", Volume: 1809, Fraction: 1}}}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if stub.calls == 0 {
		t.Fatal("a buried copy drove no repair")
	}
	for _, want := range []string{`left-wheel/nut-3 is copy 3 of child "nut" of assembly "wheel"`,
		`a polar pattern about "x"`, `the definition "lug-nut"`, `left-wheel/rim is placed by child "rim" of assembly "wheel"`} {
		if !strings.Contains(stub.asked, want) {
			t.Errorf("the repair was not told %q:\n%.1200s", want, stub.asked)
		}
	}
}
