package cad_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// What the kernel measures for a manufacturability check, against the real kernel.
//
// addresses issue 6. Every expected number below comes from a FORMULA and not from
// a previous run of this code: a plate's wall is its thickness, a rod's is its
// diameter, a tube's is the difference of its radii, an overhang is 90 degrees
// minus the arctangent of the lean, and a rectangle's second moment of area is
// b*h^3/12. That is the point — a measurement fenced by its own output is a
// snapshot, and this repository has already shipped two fences that could not fail.
//
// The NEGATIVE cases matter as much as the positive ones. This check reports to a
// reader, so a rule that fires on a correct part teaches the reader to ignore it.

// near fails unless got is within tol of want.
func aboutMM(t *testing.T, what string, got *float64, want, tol float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s was not measured at all; want %g", what, want)
	}
	if math.Abs(*got-want) > tol {
		t.Errorf("%s is %g, want %g (within %g)", what, *got, want, tol)
	}
}

// measured finds one part's row, and fails if the kernel did not measure it.
func measured(t *testing.T, b *cad.Build, id string) geometry.PartMeasure {
	t.Helper()
	for _, m := range b.Manufacturability {
		if m.ID == id {
			if m.Unchecked != "" {
				t.Fatalf("%s was not measured: %s", id, m.Unchecked)
			}
			return m
		}
	}
	t.Fatalf("the kernel returned no measurement for %q; it measured %d part(s)", id, len(b.Manufacturability))
	return geometry.PartMeasure{}
}

func evaluated(t *testing.T, doc geometry.Document, sections ...geometry.Section) *cad.Build {
	t.Helper()
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	got, err := k.BuildEvaluated(ctx, doc, geometry.Millimetre, "", sections)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skipped) > 0 || len(got.FeatureFailures) > 0 {
		t.Fatalf("the fence's own geometry did not build: skipped=%v failed=%v", got.Skipped, got.FeatureFailures)
	}
	return got
}

// A plate's thinnest wall is its thickness, and a rod's is its diameter. Both are
// the answer a ruler gives, and neither is what a bounding box would say.
func TestKernel_TheThinnestWallIsMeasuredThroughTheMaterial(t *testing.T) {
	doc := geometry.Document{Name: "walls", Units: "mm", Parts: []geometry.Part{
		{ID: "plate", Name: "plate", Shape: "box",
			Size:     map[string]float64{"width": 100, "height": 0.5, "depth": 40},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		{ID: "rod", Name: "rod", Shape: "cylinder",
			Size:     map[string]float64{"radius": 4, "height": 100},
			Position: []float64{0, 0, 200}, Rotation: []float64{0, 0, 0}},
	}}
	got := evaluated(t, doc)

	// 100 x 0.5 x 40: the thin way through is 0.5, and the feature that small is
	// the same 0.5 edge.
	plate := measured(t, got, "plate")
	aboutMM(t, "the plate's wall", plate.MinWall, 0.5, 1e-6)
	aboutMM(t, "the plate's smallest feature", plate.MinFeature, 0.5, 1e-6)
	// A box has no inside corner, and a rule about corners must not invent one.
	if plate.InternalRadius != nil {
		t.Errorf("a plain box reported an internal corner radius of %g", *plate.InternalRadius)
	}
	// Nothing overhangs: the only downward face is the one it rests on.
	if plate.MaxOverhang != nil {
		t.Errorf("a plate flat on its own floor reported a %g degree overhang", *plate.MaxOverhang)
	}

	// A rod's wall is its DIAMETER — 8, not its 100 mm length, which is what the
	// two flat ends alone would have said.
	rod := measured(t, got, "rod")
	aboutMM(t, "the rod's wall", rod.MinWall, 8, 1e-6)
	// And its smallest feature is that diameter too: the rim is a circle 25.1 mm
	// long, and a 2 mm hole is a 2 mm feature and not a 6.28 mm edge.
	aboutMM(t, "the rod's smallest feature", rod.MinFeature, 8, 1e-6)
	// ‼️ And a rod standing on its own end has NO overhang. OCCT's box around a
	// curved solid is a hair larger than the solid, so a floor taken from that box
	// leaves the end face the rod rests on just above it — and every cylinder in
	// every model then reports a 90 degree overhang it does not have.
	if over := rod.MaxOverhang; over != nil {
		t.Errorf("a rod standing on its own end reported a %g degree overhang", *over)
	}
}

// ‼️ The case a bounding box cannot answer and this design exists for: the wall of
// a tube is the difference of its radii, measured on the solid that SURVIVES the
// cut. Before the cut is applied the bore is a solid cylinder standing inside the
// tube, and anything measuring the described shape would answer 20.
func TestKernel_ATubesWallIsMeasuredAfterTheBoreIsCut(t *testing.T) {
	doc := geometry.Document{Name: "tube", Units: "mm", Parts: []geometry.Part{
		{ID: "tube", Name: "tube", Shape: "cylinder",
			Size:     map[string]float64{"radius": 10, "height": 50},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		{ID: "bore", Name: "bore", Shape: "cylinder",
			Size:     map[string]float64{"radius": 8, "height": 60},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
	}}
	doc.Features = []geometry.Feature{{ID: "hollow", Op: "cut", Of: "tube", With: []string{"bore"}}}
	got := evaluated(t, doc)

	tube := measured(t, got, "tube")
	aboutMM(t, "the tube's wall", tube.MinWall, 2, 1e-6)
	// The bore is a concave cylinder, so the smallest internal radius is 8.
	aboutMM(t, "the tube's internal radius", tube.InternalRadius, 8, 1e-6)
}

// A square inside corner is zero, and a filleted one is the radius of the fillet.
//
// Zero is not "unknown" and must never be reported as one: it is the measurement
// that says no rotating tool can cut this corner at all.
func TestKernel_ASquareInsideCornerIsZeroAndAFilletedOneIsItsRadius(t *testing.T) {
	ell := func(id string, z float64) geometry.Part {
		return geometry.Part{ID: id, Name: id, Shape: "extrusion",
			Size: map[string]float64{"depth": 10},
			Profile: []geometry.Point{{X: 0, Y: 0}, {X: 40, Y: 0}, {X: 40, Y: 10},
				{X: 10, Y: 10}, {X: 10, Y: 40}, {X: 0, Y: 40}},
			Position: []float64{0, 0, z}, Rotation: []float64{0, 0, 0}}
	}
	doc := geometry.Document{Name: "corners", Units: "mm",
		Parts: []geometry.Part{ell("square", 0), ell("rounded", 200)}}
	doc.Features = []geometry.Feature{
		{ID: "round", Op: "fillet", Of: "rounded", Radius: 3, Edges: "concave"}}
	got := evaluated(t, doc)

	square := measured(t, got, "square")
	aboutMM(t, "the square inside corner", square.InternalRadius, 0, 1e-9)

	rounded := measured(t, got, "rounded")
	aboutMM(t, "the filleted inside corner", rounded.InternalRadius, 3, 1e-6)
}

// Draft is measured against the pull direction, which is +Y because that is up in
// this system. A vertical wall gives none; a wall leaning by a known angle gives
// that angle.
func TestKernel_AVerticalWallHasNoDraftAndALeaningOneHasItsAngle(t *testing.T) {
	doc := geometry.Document{Name: "draft", Units: "mm", Parts: []geometry.Part{
		{ID: "square", Name: "square", Shape: "box",
			Size:     map[string]float64{"width": 40, "height": 20, "depth": 10},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		// ‼️ A truncated cone and not a tapered extrusion. An extrusion is swept
		// SIDEWAYS, so its two end faces are vertical walls however the outline
		// leans, and their zero is the honest answer for the part: the draft
		// reported is the flattest wall there is, not the average of them. A cone
		// has no such face, so its draft is the slope and nothing else.
		//
		// 20 mm of height for 10 mm of radius lost: 90 - atan(20/10) = 26.57
		// degrees off the pull direction.
		{ID: "tapered", Name: "tapered", Shape: "cylinder",
			Size:     map[string]float64{"radius": 20, "radius_top": 10, "height": 20},
			Position: []float64{0, 0, 200}, Rotation: []float64{0, 0, 0}},
	}}
	got := evaluated(t, doc)

	aboutMM(t, "a square box's draft", measured(t, got, "square").MinDraft, 0, 1e-9)
	want := 90 - math.Atan2(20, 10)*180/math.Pi
	aboutMM(t, "the tapered part's draft", measured(t, got, "tapered").MinDraft, want, 1e-6)
}

// An overhang is measured from VERTICAL, and the face the part rests on is not one.
//
// Both trapezia are the same solid turned over: one leans out as it rises and has a
// downward-facing wall, the other leans in and has none. A checker that called the
// second one an overhang would be firing on a part that prints perfectly.
func TestKernel_AnOverhangIsMeasuredFromVerticalAndAFloorIsNotOne(t *testing.T) {
	lean := func(id string, bottomRight, topRight, z float64) geometry.Part {
		return geometry.Part{ID: id, Name: id, Shape: "extrusion",
			Size: map[string]float64{"depth": 10},
			Profile: []geometry.Point{{X: 0, Y: 0}, {X: bottomRight, Y: 0},
				{X: topRight, Y: 20}, {X: 0, Y: 20}},
			Position: []float64{0, 0, z}, Rotation: []float64{0, 0, 0}}
	}
	doc := geometry.Document{Name: "overhang", Units: "mm", Parts: []geometry.Part{
		lean("overhung", 10, 40, 0),
		lean("undercut", 40, 10, 200),
	}}
	got := evaluated(t, doc)

	// It grows 30 mm sideways over 20 mm of height, so the underside stands
	// 90 - atan(20/30) = 56.31 degrees off vertical.
	want := 90 - math.Atan2(20, 30)*180/math.Pi
	aboutMM(t, "the overhang", measured(t, got, "overhung").MaxOverhang, want, 1e-6)

	if over := measured(t, got, "undercut").MaxOverhang; over != nil {
		t.Errorf("a wall that leans INWARD as it rises reported a %g degree overhang; "+
			"nothing is above air", *over)
	}
}

// A named section is its area, its centroid, and its second moments of area — the
// geometry half of a beam calculation, and never a stress.
func TestKernel_ANamedSectionIsMeasuredAgainstTheRectangleFormula(t *testing.T) {
	// ‼️ Placed AWAY from the origin. A cutting face built at the world origin
	// misses every part that is not there and comes back "the plane met no
	// material", which reads exactly like a plane outside the part and is not.
	doc := geometry.Document{Name: "beam", Units: "mm", Parts: []geometry.Part{
		{ID: "beam", Name: "beam", Shape: "box",
			Size:     map[string]float64{"width": 100, "height": 20, "depth": 10},
			Position: []float64{0, 0, 300}, Rotation: []float64{0, 0, 0}},
	}}
	got := evaluated(t, doc,
		geometry.Section{ID: "mid", Part: "beam", Axis: "x", At: 0},
		geometry.Section{ID: "past-the-end", Part: "beam", Axis: "x", At: 500},
		geometry.Section{ID: "nobody", Part: "ghost", Axis: "x", At: 0})
	if len(got.Sections) != 3 {
		t.Fatalf("three sections were asked for and %d came back: %+v", len(got.Sections), got.Sections)
	}
	by := map[string]geometry.SectionProperties{}
	for _, s := range got.Sections {
		by[s.ID] = s
	}

	// The cut is the 20 (y) by 10 (z) rectangle, so area = 200, the second moment
	// about y is 20*10^3/12 = 1666.67 and about z is 10*20^3/12 = 6666.67.
	mid := by["mid"]
	if mid.Unmeasured != "" {
		t.Fatalf("a plane through the middle of a box was not measured: %s", mid.Unmeasured)
	}
	if math.Abs(mid.Area-200) > 1e-6 {
		t.Errorf("the section's area is %g mm², want 200", mid.Area)
	}
	if mid.Axes != [2]string{"y", "z"} {
		t.Errorf("a cut normal to x lies in %v, want y and z", mid.Axes)
	}
	for i, want := range [2]float64{20 * 1000 / 12.0, 10 * 8000 / 12.0} {
		if math.Abs(mid.SecondMoments[i]-want) > 1e-3 {
			t.Errorf("the second moment about %s is %g mm⁴, want %g",
				mid.Axes[i], mid.SecondMoments[i], want)
		}
	}
	// The modulus is I over the distance to the furthest material the OTHER way:
	// 1666.67/5 and 6666.67/10.
	for i, want := range [2]float64{1666.6666666666667 / 5, 6666.666666666667 / 10} {
		if math.Abs(mid.Moduli[i]-want) > 1e-3 {
			t.Errorf("the section modulus about %s is %g mm³, want %g",
				mid.Axes[i], mid.Moduli[i], want)
		}
	}

	// ‼️ A plane that misses the part, and a section naming a part that is not
	// there, are REFUSED by name — never answered with a zero area, which reads
	// exactly like a part with no material in it.
	for _, id := range []string{"past-the-end", "nobody"} {
		if by[id].Unmeasured == "" {
			t.Errorf("the section %q was answered rather than refused: %+v", id, by[id])
		}
		if by[id].Area != 0 {
			t.Errorf("the refused section %q still carries an area of %g", id, by[id].Area)
		}
	}
}

// Every copy of a shape at the same rotation is one measurement.
//
// The measurements are invariant under translation, so the 4,096th bolt costs a
// dict lookup. Without this the pass could not run on a model of any size at all:
// 512 distinct shapes took 10.7 s and 4,096 did not finish inside the kernel's own
// build limit (docs/spikes/2026-09-20-manufacturability-cost).
func TestKernel_AMeasurementIsReusedForEveryCopyOfAShape(t *testing.T) {
	doc := geometry.Document{Name: "copies", Units: "mm"}
	const copies = 40
	for i := 0; i < copies; i++ {
		doc.Parts = append(doc.Parts, geometry.Part{
			ID: fmt.Sprintf("bolt-%d", i), Name: fmt.Sprintf("bolt %d", i), Shape: "box",
			Size:     map[string]float64{"width": 8, "height": 3, "depth": 8},
			Position: []float64{float64(i) * 30, 0, 0}, Rotation: []float64{0, 0, 0}})
	}
	got := evaluated(t, doc)

	if n := len(got.Manufacturability); n != copies {
		t.Fatalf("%d copies produced %d measurement(s)", copies, n)
	}
	if got.ManufacturabilityReused != copies-1 {
		t.Errorf("%d copies of one shape measured %d of them again; want %d reused",
			copies, copies-got.ManufacturabilityReused, copies-1)
	}
	// One box: six faces, once, however many copies place it.
	if got.ManufacturabilityFaces != 6 {
		t.Errorf("%d copies of a six-faced box measured %d faces, want 6",
			copies, got.ManufacturabilityFaces)
	}
	// And every copy still gets the same answer, by name.
	for _, m := range got.Manufacturability {
		aboutMM(t, m.ID+"'s wall", m.MinWall, 3, 1e-6)
	}
}

// ‼️ The budget binds, and the reply says so rather than coming back short.
//
// A truncated check that returned a short list would read exactly like a model with
// fewer parts in it — which is the defect Phase 5 stage V2 closed for the
// interference check, and it is closed here the same way: the reply carries how
// many parts there were, how many were measured, and that it stopped.
func TestKernel_TheFaceBudgetStopsTheMeasurementAndSaysSo(t *testing.T) {
	doc := geometry.Document{Name: "many", Units: "mm"}
	// Distinct sizes, so nothing can be reused: 101 boxes of six faces is 606,
	// past the 600-face budget, and 100 of them fit inside it.
	const parts = 101
	for i := 0; i < parts; i++ {
		doc.Parts = append(doc.Parts, geometry.Part{
			ID: fmt.Sprintf("part-%d", i), Name: fmt.Sprintf("part %d", i), Shape: "box",
			Size:     map[string]float64{"width": 10 + float64(i)*0.01, "height": 6, "depth": 4},
			Position: []float64{float64(i) * 30, 0, 0}, Rotation: []float64{0, 0, 0}})
	}
	got := evaluated(t, doc)

	if !got.ManufacturabilityTruncated {
		t.Fatalf("%d distinct parts of 6 faces (%d faces) did not exhaust a 600-face budget: "+
			"measured %d, %d faces", parts, parts*6, got.ManufacturabilityMeasured, got.ManufacturabilityFaces)
	}
	if got.ManufacturabilityMeasured >= parts {
		t.Errorf("a truncated check reported %d of %d parts measured", got.ManufacturabilityMeasured, parts)
	}
	if got.ManufacturabilityParts != parts {
		t.Errorf("a truncated check says the model has %d parts; it has %d, and a reader "+
			"cannot tell how much was left out of a count that shrank with the check",
			got.ManufacturabilityParts, parts)
	}
	if len(got.Manufacturability) != got.ManufacturabilityMeasured {
		t.Errorf("the reply lists %d measurements and claims %d",
			len(got.Manufacturability), got.ManufacturabilityMeasured)
	}
}

// A build that did not ask for any of this gets none of it, and pays for none of
// it: the manufacturability phase is absent from a plain build's timings.
func TestKernel_APlainBuildMeasuresNothingForManufacturability(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	doc := geometry.Document{Name: "plain", Units: "mm", Parts: []geometry.Part{
		{ID: "plate", Name: "plate", Shape: "box",
			Size:     map[string]float64{"width": 10, "height": 0.2, "depth": 10},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
	}}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Manufacturability) != 0 || got.ManufacturabilityParts != 0 {
		t.Errorf("a plain build measured manufacturability anyway: %+v", got.Manufacturability)
	}
	// ‼️ A phase that did not run reports no time, so a reply can never be read as
	// a check that ran and found nothing (the rule the interference phase follows).
	if got.Phases.Manufacturability != 0 {
		t.Errorf("a build that ran no manufacturability check reported %v in it",
			got.Phases.Manufacturability)
	}
}
