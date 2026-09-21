package geometry

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Mesh-only lattices (stage E1 of the "looks designed" work; damon's decision,
// 2026-09-18). See lattice.go.

func latticePart(pattern string, size map[string]float64) Part {
	return Part{ID: "infill", Name: "Infill", Shape: "lattice", Lattice: pattern, Size: size,
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
}

func latticeSize() map[string]float64 {
	return map[string]float64{"width": 60, "height": 40, "depth": 30, "cell": 15, "thickness": 2}
}

func latticeDoc(parts ...Part) Document {
	return Document{Name: "infill", Units: "mm", Parts: parts}
}

// Every lattice the kernel cannot build is refused by name, as a part NOT in the
// model, and the refusal says what would fit.
func TestLattice_RefusesByNameWhatItCannotBuild(t *testing.T) {
	size := func(edit func(map[string]float64)) map[string]float64 {
		s := latticeSize()
		edit(s)
		return s
	}
	cases := []struct {
		name string
		part Part
		says string
	}{
		{"no pattern", latticePart("", latticeSize()), `no "lattice" pattern; name one of "gyroid", "diamond" or "primitive"`},
		{"unknown pattern", latticePart("octet", latticeSize()), `lattice pattern "octet", which FORGE cannot draw`},
		{"no cell", latticePart("gyroid", size(func(s map[string]float64) { delete(s, "cell") })), `no "cell"`},
		{"negative width", latticePart("gyroid", size(func(s map[string]float64) { s["width"] = -1 })), "width of -1; it must be a positive length"},
		{"wall too thin", latticePart("gyroid", size(func(s map[string]float64) { s["thickness"] = 0.5 })), "must be between cell/20 (0.75) and cell/3 (5)"},
		{"wall too thick", latticePart("gyroid", size(func(s map[string]float64) { s["thickness"] = 6 })), "must be between cell/20"},
		{"past the budget", latticePart("gyroid", size(func(s map[string]float64) { s["cell"], s["thickness"] = 5, 0.5 })),
			"past the 200000 a mesh-only part may have; a cell of"},
	}
	for _, c := range cases {
		doc := latticeDoc(c.part)
		faults := doc.Faults()
		if len(faults) != 1 || faults[0].Name != "Infill" || !strings.Contains(faults[0].Detail, c.says) {
			t.Errorf("%s: faults %+v; want one naming Infill saying %q", c.name, faults, c.says)
		}
		solids, inferred := Solids(doc, Millimetre)
		if len(solids) != 0 {
			t.Errorf("%s: a refused lattice was sent to the kernel", c.name)
		}
		if !strings.Contains(strings.Join(inferred, " "), "so it is not in this file") {
			t.Errorf("%s: the build does not say the lattice is missing: %v", c.name, inferred)
		}
	}
	if faults := (&Document{Name: "ok", Units: "mm", Parts: []Part{latticePart("gyroid", latticeSize())}}).Faults(); len(faults) != 0 {
		t.Errorf("a lattice within every bound is refused: %+v", faults)
	}
}

// The cell a refusal names does fit.
func TestLattice_TheCellARefusalNamesFits(t *testing.T) {
	p := latticePart("gyroid", map[string]float64{"width": 60, "height": 40, "depth": 30, "cell": 5, "thickness": 0.5})
	_, refused := readLattice(p)
	if len(refused) != 1 {
		t.Fatalf("refusals %+v", refused)
	}
	m := regexp.MustCompile(`a cell of ([0-9.]+) or more`).FindStringSubmatch(refused[0].Detail)
	if m == nil {
		t.Fatalf("the refusal names no cell: %q", refused[0].Detail)
	}
	fits, _ := strconv.ParseFloat(m[1], 64)
	p.Size["cell"], p.Size["thickness"] = fits, 0.5*fits/5
	if _, problems := readLattice(p); anyError(problems) {
		t.Errorf("the named cell %v is refused too: %+v", fits, problems)
	}
}

// All mesh-only parts in a design share one budget, every placed copy counted.
func TestLattice_CopiesShareOneBudget(t *testing.T) {
	p := latticePart("gyroid", latticeSize())
	one, _ := LatticeTriangleEstimate(p)
	copies := maxMeshOnlyTriangles/one + 1
	p.Repeat = &Repeat{Count: copies, Offset: []float64{100, 0, 0}}
	d := latticeDoc(p)
	faults := d.Faults()
	if len(faults) == 0 || !strings.Contains(faults[0].Detail, "past the 400000 all mesh-only parts in one design may have together") {
		t.Errorf("%d copies of a %d-triangle lattice: faults %+v", copies, one, faults)
	}
}

// A lattice reaches the kernel marked mesh-only, in millimetres, with the sampling
// step decided here.
func TestLattice_IsSentToTheKernelMarkedMeshOnlyInMillimetres(t *testing.T) {
	doc := latticeDoc(latticePart("diamond", latticeSize()))
	doc.Units = "cm"
	solids, _ := Solids(doc, Centimetre)
	if len(solids) != 1 {
		t.Fatalf("%d solids", len(solids))
	}
	s := solids[0]
	if !s.MeshOnly || s.Lattice != "diamond" || s.Shape != "lattice" {
		t.Errorf("sent %+v; want a mesh-only diamond lattice", s)
	}
	if s.Dims["width"] != 600 || s.Dims["cell"] != 150 || s.Dims["thickness"] != 20 || math.Abs(s.Dims["edge"]-20/1.5) > 1e-9 {
		t.Errorf("dims %v; want millimetres, edge the finer of cell/8 and wall/1.5", s.Dims)
	}
	for _, other := range []string{"box", "cylinder"} {
		plain, _ := Solids(latticeDoc(Part{ID: "b", Shape: other, Size: map[string]float64{"width": 1, "height": 1, "depth": 1, "radius": 1},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}), Millimetre)
		if plain[0].MeshOnly {
			t.Errorf("a %s is sent as mesh-only", other)
		}
	}
}

// No feature may operate on a lattice or use one as a tool.
func TestLattice_NoFeatureMayCutOrUseIt(t *testing.T) {
	box := Part{ID: "block", Name: "Block", Shape: "box", Size: map[string]float64{"width": 70, "height": 50, "depth": 40},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
	for _, f := range []Feature{
		{ID: "round", Op: "fillet", Of: "infill", Radius: 1, Edges: "all"},
		{ID: "carve", Op: "cut", Of: "block", With: []string{"infill"}},
	} {
		doc := latticeDoc(box, latticePart("gyroid", latticeSize()))
		doc.Features = []Feature{f}
		ops, problems := doc.Operations()
		if len(ops) != 0 || len(problems) != 1 || !strings.Contains(problems[0].Detail, "mesh-only") ||
			!strings.Contains(problems[0].Detail, "never structural") {
			t.Errorf("%s: ops %v, problems %+v", f.ID, ops, problems)
		}
	}
}

// Mesh files leave the lattice out with a note rather than drawing its box, and
// every export label says mesh-only parts are left out.
func TestLattice_ExportsLeaveItOutAndSaySo(t *testing.T) {
	doc := latticeDoc(Part{ID: "block", Name: "Block", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}, latticePart("gyroid", latticeSize()))
	mesh := Tessellate(doc, Millimetre)
	for _, g := range mesh.Groups {
		if g.PartID == "infill" && len(g.Triangles) > 0 {
			t.Error("the Go tessellator drew the lattice")
		}
	}
	if !strings.Contains(strings.Join(mesh.Inferences, " "), "Infill: a lattice is mesh-only") {
		t.Errorf("the mesh does not say the lattice is left out: %v", mesh.Inferences)
	}
	v := &Variant{Document: doc, Units: Millimetre}
	label, err := KernelLabelFor(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(label.Lossy[0], "1 mesh-only part(s) are left out of this STEP file: Infill.") {
		t.Errorf("the STEP label does not lead with the mesh-only part: %q", label.Lossy[0])
	}
	obj, _, err := LabelFor(v, "obj")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(obj.Lossy, " "), "mesh-only part(s) are left out of this file: Infill") {
		t.Errorf("the OBJ label does not name the mesh-only part: %v", obj.Lossy)
	}
	plain := &Variant{Document: latticeDoc(doc.Parts[0]), Units: Millimetre}
	if l, _ := KernelLabelFor(plain); strings.Contains(strings.Join(l.Lossy, " "), "mesh-only") {
		t.Error("a design with no mesh-only part is labelled as having one")
	}
}

// The panel's one-line summary says what the lattice is and that it is mesh-only.
func TestLattice_ThePanelSummarySaysMeshOnly(t *testing.T) {
	got := Dimensions(latticePart("gyroid", latticeSize()), Millimetre)
	if !strings.HasPrefix(got, "gyroid lattice · ") || !strings.HasSuffix(got, " · "+MeshOnlyLabel) {
		t.Errorf("summary %q", got)
	}
}

// Mass never weighs a lattice, even handed a measure of one, and names it.
func TestLattice_MassLeavesItOutAndNamesIt(t *testing.T) {
	doc := latticeDoc(Part{ID: "block", Name: "Block", Shape: "box", Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		latticePart("gyroid", latticeSize()))
	report := MassProperties(doc, []SolidMeasure{
		{ID: "block", Volume: 1000, Measured: true, Bounds: [6]float64{-5, -5, -5, 5, 5, 5}},
		{ID: "infill", Volume: 5000, Measured: true, Centroid: [3]float64{100, 0, 0}},
	})
	if len(report.MeshOnly) != 1 || report.MeshOnly[0] != "Infill" {
		t.Errorf("mesh-only %v", report.MeshOnly)
	}
	if len(report.Groups) != 1 || report.Groups[0].Volume != 1000 || report.Groups[0].Parts != 1 {
		t.Errorf("groups %+v: the lattice was weighed", report.Groups)
	}
}
