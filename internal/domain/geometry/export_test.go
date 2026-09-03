package geometry

import (
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

func bracket() *Variant {
	return &Variant{
		VersionID: "ver_1", ProjectID: "prj_1", Path: "geometry/bracket.forge.json", Version: 2,
		Name:  "bracket",
		Units: Millimetre, Frame: FrameAssembly, Generator: "claude-opus-5",
		Verification: workspace.Unverified, Disposition: workspace.Pending,
		Document: Document{
			Name: "bracket", Units: "mm",
			Parts: []Part{
				{ID: "plate", Name: "base plate", Shape: "box",
					Size:     map[string]float64{"width": 60, "height": 5, "depth": 60},
					Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
				{ID: "boss", Name: "pilot boss", Shape: "cylinder",
					Size:     map[string]float64{"radius": 11, "height": 6},
					Position: []float64{0, -3, 0}, Rotation: []float64{0, 0, 0}},
			},
			Assumptions: []string{"60 mm plate — not a standard figure"},
			NotVerified: []string{"nothing here has been analysed"},
		},
	}
}

// Every parametric format is declared and refused, with a reason. Leaving them
// out is what invites a mesh to be written with a .step extension.
func TestExport_ParametricFormatsAreDeclaredAndRefused(t *testing.T) {
	var parametric int
	for _, f := range Formats() {
		if f.Kind != KindParametric {
			continue
		}
		parametric++
		if f.Available {
			t.Errorf("%s claims to be available; nothing in this build can produce a parametric model", f.Name)
		}
		if strings.TrimSpace(f.Reason) == "" {
			t.Errorf("%s is refused with no reason, which leaves the caller a dead end", f.Name)
		}
		_, err := Export(bracket(), f.Name)
		if errs.CodeOf(err) != errs.CodeConnectorUnavailable {
			t.Errorf("%s export returned %v; a declared-but-absent backend is CONNECTOR_UNAVAILABLE", f.Name, err)
		}
		if !strings.Contains(err.Error(), "CAD kernel") && !strings.Contains(err.Error(), "parametric") {
			t.Errorf("%s refusal does not say why: %v", f.Name, err)
		}
	}
	if parametric == 0 {
		t.Fatal("no parametric format is declared at all, so nothing tells a caller STEP is impossible here")
	}
}

// Every format the build says it can write must have a writer. A format listed
// as available with no writer would answer a download with an empty file.
func TestExport_EveryAvailableFormatWrites(t *testing.T) {
	for _, f := range Formats() {
		if !f.Available {
			continue
		}
		res, err := Export(bracket(), f.Name)
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if len(res.Content) == 0 {
			t.Errorf("%s produced an empty file", f.Name)
		}
		if res.Triangles == 0 {
			t.Errorf("%s produced no triangles", f.Name)
		}
		if !strings.HasSuffix(res.Filename, f.Extension) {
			t.Errorf("%s wrote %q, which does not carry its own extension", f.Name, res.Filename)
		}
	}
}

// A mesh file's numbers become a length in whatever opens it, and no mesh format
// carries a scale its consumers act on. An assembly with no convertible unit is
// therefore refused rather than exported with a comment nobody downstream reads.
func TestExport_RefusesGeometryWithNoUsableUnit(t *testing.T) {
	for _, declared := range []string{"", "furlongs"} {
		v := bracket()
		v.Units = UnitUnspecified
		v.UnitsDeclared = declared

		for _, format := range []string{"obj", "stl"} {
			_, err := Export(v, format)
			if err == nil {
				t.Fatalf("%s export of unit-less geometry succeeded; its numbers would be read as millimetres", format)
			}
			if !strings.Contains(err.Error(), "mm") {
				t.Errorf("%s refusal does not tell the person what to do about it: %v", format, err)
			}
		}
	}
}

// The label states the tessellation error as a NUMBER. "Approximated" is a word;
// a person deciding whether a preview is close enough needs the figure.
func TestLabel_TessellationErrorIsMeasuredNotDescribed(t *testing.T) {
	label, _, err := LabelFor(bracket(), "obj")
	if err != nil {
		t.Fatal(err)
	}
	if len(label.Tessellation) != 1 {
		t.Fatalf("expected the one curved part to be labelled, got %d entries", len(label.Tessellation))
	}
	d := label.Tessellation[0]
	if d.PartID != "boss" {
		t.Errorf("the labelled part is %q; the box is not tessellated and must not be listed", d.PartID)
	}
	// r(1 - cos(pi/n)) for r = 11 mm, n = 40.
	want := 11 * (1 - math.Cos(math.Pi/40))
	if math.Abs(d.Max.Value()-want) > 1e-6 {
		t.Errorf("deviation reported as %v; the sagitta of a 40-sided ⌀22 cylinder is %.6f", d.Max, want)
	}
	if !strings.Contains(d.Max.String(), "mm") {
		t.Errorf("the deviation %q travels without its unit", d.Max)
	}
}

// A dimension nobody stated must not leave the system looking like one somebody
// chose. The renderer defaults silently beside a provenance banner; a file has
// no banner attached.
func TestTessellate_DefaultedDimensionsAreLabelledAsInferred(t *testing.T) {
	doc := Document{
		Name: "thing", Units: "mm",
		Parts: []Part{{ID: "p", Name: "mystery", Shape: "box",
			Size: map[string]float64{"width": 60}}}, // height and depth absent
		NotVerified: []string{"nothing checked"},
	}
	mesh := Tessellate(doc, Millimetre)
	joined := strings.Join(mesh.Inferences, "\n")
	for _, key := range []string{"height", "depth"} {
		if !strings.Contains(joined, key) {
			t.Errorf("a defaulted %s is not reported as inferred: %v", key, mesh.Inferences)
		}
	}
	if strings.Contains(joined, "width") {
		t.Errorf("width WAS stated and must not be reported as inferred: %v", mesh.Inferences)
	}
	if !strings.Contains(joined, "FORGE chose") {
		t.Errorf("the inference does not say the number was FORGE's rather than the person's: %v", mesh.Inferences)
	}
}

// A tube's bore is not modelled. Somebody who machines the exported file gets a
// solid, and the file has to say so.
func TestTessellate_TubeSaysItsBoreIsNotThere(t *testing.T) {
	doc := Document{
		Name: "sleeve", Units: "mm",
		Parts:       []Part{{ID: "s", Name: "sleeve", Shape: "tube", Size: map[string]float64{"radius": 10, "height": 20}}},
		NotVerified: []string{"nothing checked"},
	}
	mesh := Tessellate(doc, Millimetre)
	if !strings.Contains(strings.Join(mesh.Inferences, "\n"), "bore") {
		t.Fatalf("a tube exported as a solid does not say so: %v", mesh.Inferences)
	}
}

// An unsupported shape is exported as a box and the file says the group is not
// what it is named after. Same rule as the renderer's substitution note.
func TestTessellate_UnsupportedShapeIsNotSilentlyABox(t *testing.T) {
	doc := Document{
		Name: "rib", Units: "mm",
		Parts:       []Part{{ID: "r", Name: "stiffener", Shape: "triangle-prism", Size: map[string]float64{"width": 5, "height": 5, "depth": 5}}},
		NotVerified: []string{"nothing checked"},
	}
	mesh := Tessellate(doc, Millimetre)
	joined := strings.Join(mesh.Inferences, "\n")
	if !strings.Contains(joined, "triangle-prism") || !strings.Contains(joined, "bounding box") {
		t.Fatalf("an unsupported shape is exported silently: %v", mesh.Inferences)
	}
}

// OBJ can carry the label, so it must. A file that leaves with no provenance is
// the render mistaken for a result, one step further from anything that could
// correct it.
func TestExport_OBJCarriesTheWholeLabel(t *testing.T) {
	res, err := Export(bracket(), "obj")
	if err != nil {
		t.Fatal(err)
	}
	body := string(res.Content)
	for _, want := range []string{
		"unverified proposal",                 // the headline, case-folded below
		"claude-opus-5",                       // generator
		"geometry/bracket.forge.json v2",      // geometry version
		"60 mm plate — not a standard figure", // assumptions
		"nothing here has been analysed",      // not-verified
		"TESSELLATION",
		"LOST IN THIS CONVERSION",
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Errorf("the OBJ header does not carry %q", want)
		}
	}
	if !strings.Contains(body, "g base_plate") || !strings.Contains(body, "g pilot_boss") {
		t.Error("part identity was not written as OBJ groups, so the parts list did not survive the export")
	}
}

// STL cannot carry the label, so the label must SAY it cannot. The failure this
// prevents is a lossy list that quietly describes the OBJ path for both formats.
func TestExport_STLDeclaresThatTheLabelDidNotTravel(t *testing.T) {
	res, err := Export(bracket(), "stl")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Label.Lossy, "\n")
	if !strings.Contains(joined, "Part identity is gone") {
		t.Errorf("STL's lossy list does not mention losing part identity: %v", res.Label.Lossy)
	}
	if !strings.Contains(joined, "Nothing of this label is in the file") {
		t.Errorf("STL's lossy list does not admit the label stays behind: %v", res.Label.Lossy)
	}
	body := string(res.Content)
	if !strings.HasPrefix(body, "solid ") || !strings.Contains(body, "UNVERIFIED") {
		t.Error("the solid name line carries nothing, and it is the only text STL has")
	}
	if strings.Contains(body, "#") {
		t.Error("comment syntax was written into an STL, which has none")
	}
}

// Coordinates never leave in exponent form. Several widely-used readers refuse
// them, and the symptom is a file that opens empty rather than an error.
func TestExport_CoordinatesAreNeverInExponentForm(t *testing.T) {
	v := bracket()
	v.Document.Parts[1].Size["radius"] = 0.0000123
	for _, format := range []string{"obj", "stl"} {
		res, err := Export(v, format)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(res.Content), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			// Only the numeric fields are checked; the keywords themselves
			// ("vertex", "facet normal") contain an e and would make this fence
			// fail on every correct file.
			switch fields[0] {
			case "v", "vn", "vertex":
				fields = fields[1:]
			case "facet":
				fields = fields[2:]
			default:
				continue
			}
			for _, f := range fields {
				if strings.ContainsAny(f, "eE") {
					t.Fatalf("%s wrote a coordinate in exponent form: %q", format, line)
				}
			}
		}
	}
}

// Every exported solid must be wound OUTWARD.
//
// # Why this is measured rather than read
//
// Mesh consumers decide which side of a facet is outside from its winding order.
// The renderer does not — it is handed each normal explicitly and draws with
// back-face culling off — so an inside-out solid looks perfectly correct on
// screen, opens without complaint in a viewer, and only misbehaves at the point
// where somebody tries to make the thing. The first bracket this code exported
// had exactly that: a box wound outward beside a cylinder wound inward, in one
// file.
//
// The signed volume of a closed mesh is positive when its facets face out. It is
// four lines of arithmetic and it catches, in one number, a class of defect that
// reading the primitives did not.
func TestExport_EverySolidIsWoundOutward(t *testing.T) {
	v := bracket()
	v.Document.Parts = append(v.Document.Parts,
		Part{ID: "dome", Name: "dome", Shape: "sphere",
			Size: map[string]float64{"radius": 8}, Position: []float64{0, 10, 0}, Rotation: []float64{0, 0, 0}},
		Part{ID: "spike", Name: "spike", Shape: "cone",
			Size: map[string]float64{"radius": 4, "height": 9}, Position: []float64{20, 0, 0}, Rotation: []float64{0, 0, 0}},
		Part{ID: "sleeve", Name: "sleeve", Shape: "tube",
			Size: map[string]float64{"radius": 6, "height": 12}, Position: []float64{-20, 0, 0}, Rotation: []float64{0, 0, 0}},
	)
	mesh := Tessellate(v.Document, Millimetre)

	for _, g := range mesh.Groups {
		if g.Shape == "plane" {
			// Not a solid: two triangles enclosing nothing. It has no inside to
			// be turned out, and it is labelled as unmakeable already.
			continue
		}
		var volume float64
		for _, tr := range g.Triangles {
			volume += (tr.A[0]*(tr.B[1]*tr.C[2]-tr.B[2]*tr.C[1]) -
				tr.A[1]*(tr.B[0]*tr.C[2]-tr.B[2]*tr.C[0]) +
				tr.A[2]*(tr.B[0]*tr.C[1]-tr.B[1]*tr.C[0])) / 6
		}
		if volume <= 0 {
			t.Errorf("%s (%s) has signed volume %.3f — it is inside out. "+
				"It will render correctly and mislead anything that tries to make it.",
				g.Label, g.Shape, volume)
		}
	}
}

// The tessellated volume must be close to the analytic one. A mesh that is wound
// correctly and built wrongly passes the winding fence and is still not the part
// that was described.
func TestExport_TessellatedVolumeIsCloseToTheRealSolid(t *testing.T) {
	cases := []struct {
		name string
		part Part
		want float64
	}{
		{"box 60×5×60", Part{ID: "b", Shape: "box",
			Size: map[string]float64{"width": 60, "height": 5, "depth": 60}}, 60 * 5 * 60},
		{"cylinder ⌀22 h6", Part{ID: "c", Shape: "cylinder",
			Size: map[string]float64{"radius": 11, "height": 6}}, math.Pi * 121 * 6},
		{"cone r4 h9", Part{ID: "n", Shape: "cone",
			Size: map[string]float64{"radius": 4, "height": 9}}, math.Pi * 16 * 9 / 3},
		{"sphere r8", Part{ID: "s", Shape: "sphere",
			Size: map[string]float64{"radius": 8}}, 4.0 / 3 * math.Pi * 512},
	}
	for _, c := range cases {
		mesh := Tessellate(Document{Name: "x", Units: "mm", Parts: []Part{c.part},
			NotVerified: []string{"x"}}, Millimetre)
		var volume float64
		for _, tr := range mesh.Triangles() {
			volume += (tr.A[0]*(tr.B[1]*tr.C[2]-tr.B[2]*tr.C[1]) -
				tr.A[1]*(tr.B[0]*tr.C[2]-tr.B[2]*tr.C[0]) +
				tr.A[2]*(tr.B[0]*tr.C[1]-tr.B[1]*tr.C[0])) / 6
		}
		// An inscribed tessellation is always SMALLER than the solid it
		// approximates, never larger, and by no more than 2% at these counts.
		// Both bounds matter: too small catches a missing cap, and larger than
		// the analytic solid means the facets are not chords at all.
		if volume > c.want || volume < c.want*0.98 {
			t.Errorf("%s tessellates to %.3f mm³; the solid is %.3f mm³", c.name, volume, c.want)
		}
	}
}
