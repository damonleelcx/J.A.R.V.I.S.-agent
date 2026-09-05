package cad_test

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The kernel tests run against a REAL build123d, or they skip.
//
// # Why there is no fake
//
// A stub returning a plausible STEP file would pass forever while production
// wrote nothing, and every property worth checking here — that the solid is
// valid, that its volume is right, that a cylinder points the way this system
// draws it — is a property of OpenCASCADE and not of this code. A fake would be
// asserting that the test author knows what OCCT does, which is exactly the
// thing in doubt.
func kernel(t *testing.T) *cad.Kernel {
	t.Helper()
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests. " +
			"python3 -m venv venv && ./venv/bin/pip install build123d, then point it at ./venv/bin/python")
	}
	k := cad.New(python, logx.Discard())
	t.Cleanup(k.Close)
	return k
}

func plate() geometry.Document {
	return geometry.Document{
		Name: "bracket", Units: "mm",
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size:     map[string]float64{"width": 60, "height": 6, "depth": 60},
				Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
			{ID: "boss", Name: "Boss", Shape: "cylinder",
				Size:     map[string]float64{"radius": 11, "height": 8},
				Position: []float64{0, 7, 0}, Rotation: []float64{0, 0, 0}},
		},
	}
}

// A real B-Rep, and the volume proves it is the shape that was asked for.
func TestKernel_BuildsARealSolidWithTheRightVolume(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	got, err := k.BuildDocument(ctx, plate(), geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parts != 2 {
		t.Errorf("built %d parts, want 2", got.Parts)
	}
	// 60x6x60 = 21600, plus a cylinder r=11 h=8 = 3041.06.
	if want := 21600.0 + 3041.0616; got.Volume < want-1 || got.Volume > want+1 {
		t.Errorf("volume = %.4f mm³, want %.4f — the solid is not the shape that was asked for",
			got.Volume, want)
	}
}

// Real ISO-10303-21, not tessellated facets with a .step extension on them.
func TestKernel_ExportsGenuineSTEP(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	got, err := k.BuildDocument(ctx, plate(), geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.STEP) == 0 {
		t.Fatal("no file came back")
	}
	head := string(got.STEP[:min(200, len(got.STEP))])
	if !strings.HasPrefix(head, "ISO-10303-21;") {
		t.Fatalf("this is not a STEP file: %.60q", head)
	}
	// The point of the whole exercise: analytic surfaces, not triangles. A
	// tessellated file would carry TRIANGULATED_FACE_SET or POLY_LOOP and no
	// CYLINDRICAL_SURFACE, and would be exactly the lie the export path used to
	// refuse to tell.
	body := string(got.STEP)
	if !strings.Contains(body, "CYLINDRICAL_SURFACE") {
		t.Error("no analytic cylinder in the file; the boss was tessellated")
	}
	if strings.Contains(body, "TRIANGULATED_FACE_SET") {
		t.Error("the file contains a triangulated face set — this is a mesh wearing a STEP extension")
	}
	if !bytes.Contains(got.STEP, []byte("END-ISO-10303-21;")) {
		t.Error("the file is truncated")
	}
}

// A dimension nobody stated travels with the file. There is no provenance banner
// attached to a download, so a defaulted 1 and a stated 1 must not be
// indistinguishable.
func TestKernel_ReportsTheDimensionsItHadToInvent(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := plate()
	delete(doc.Parts[0].Size, "depth")

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	var told bool
	for _, note := range got.Inferred {
		if strings.Contains(note, "depth") && strings.Contains(note, "Plate") {
			told = true
		}
	}
	if !told {
		t.Errorf("the invented depth was not reported: %v", got.Inferred)
	}
}

// A shape the kernel cannot build must not take the rest of the file with it.
func TestKernel_OneBadPartDoesNotLoseTheOthers(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := plate()
	// A NEGATIVE radius. Measured against build123d 0.11.1 rather than assumed:
	// a zero radius is accepted and builds a solid of volume 0, and only a
	// negative one is refused. The first version of this test used zero and
	// passed for the wrong reason — three parts built, nothing skipped, and the
	// assertion was about a refusal that never happened.
	doc.Parts = append(doc.Parts, geometry.Part{ID: "bad", Name: "Impossible", Shape: "cylinder",
		Size: map[string]float64{"radius": -3, "height": 5}, Position: []float64{0, 0, 0}})

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("one impossible part lost the whole assembly: %v", err)
	}
	if got.Parts != 2 {
		t.Errorf("built %d parts, want the 2 good ones", got.Parts)
	}
	if len(got.Skipped) != 1 || !strings.Contains(got.Skipped[0], "Impossible") {
		t.Fatalf("the part that could not be built was not named: %v", got.Skipped)
	}
	// OCCT raises Standard_Failure with an EMPTY message for this, so a reason
	// composed only of str(exc) would read "Impossible: " and say nothing.
	if strings.TrimSpace(strings.TrimPrefix(got.Skipped[0], "Impossible:")) == "" {
		t.Errorf("the part was named with no reason: %q", got.Skipped[0])
	}
}

// A CLOSED kernel starts again on the next build.
//
// Not the crash path: Close resets the kernel's own state, so the next call
// simply starts a fresh process and the retry never runs. A drill removed the
// retry entirely and this test stayed green, which is how that was found. The
// crash path is covered by TestRetryAfterTheProcessDies, which has to reach
// inside the package to kill the process without telling the kernel.
func TestKernel_AClosedKernelStartsAgain(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if _, err := k.BuildDocument(ctx, plate(), geometry.Millimetre, ""); err != nil {
		t.Fatal(err)
	}
	k.Close() // the process is now gone, exactly as if it had died

	got, err := k.BuildDocument(ctx, plate(), geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("the kernel did not come back after its process died: %v", err)
	}
	if got.Parts != 2 {
		t.Errorf("built %d parts after the restart, want 2", got.Parts)
	}
}

// The warm process is the whole reason this is a sidecar: the import costs 2.5 s
// and the build costs milliseconds. If a second build paid the import again the
// design would be pointless, and nothing else would notice.
func TestKernel_TheSecondBuildDoesNotPayForTheImport(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if _, err := k.BuildDocument(ctx, plate(), geometry.Millimetre, ""); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := k.BuildDocument(ctx, plate(), geometry.Millimetre, ""); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("second build took %s", elapsed)
	// The import alone was measured at 2.5 s. A second build anywhere near that
	// means the process is not being reused.
	if elapsed > time.Second {
		t.Errorf("the second build took %s; the kernel is not staying warm", elapsed)
	}
}

// A deployment with no kernel refuses, with the sentence that fixes it. This is
// the DEFAULT configuration and must never degrade into something else.
func TestKernel_WithoutAPythonItRefusesAndSaysHow(t *testing.T) {
	k := cad.New("", logx.Discard())
	if k.Available() {
		t.Fatal("a kernel with no interpreter reported itself available")
	}
	_, err := k.BuildDocument(context.Background(), plate(), geometry.Millimetre, "step")
	if err == nil {
		t.Fatal("it produced something without a kernel")
	}
	if !errs.Is(err, errs.CodeConnectorUnavailable) {
		t.Errorf("the refusal is not CONNECTOR_UNAVAILABLE: %v", err)
	}
	if !strings.Contains(err.Error(), "FORGE_CAD_PYTHON") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Orientation, which is the property nothing else here can see.
//
// # Why volume could never have caught this
//
// A drill removed the axis correction from the sidecar — so every cylinder was
// built along +Z where this system draws them along +Y — and every test still
// passed. Volume is identical however a solid is turned, the STEP file still
// contained a CYLINDRICAL_SURFACE, and the byte count barely moved. A part
// rotated ninety degrees in a downloaded file is the one failure a label cannot
// soften, and nothing was looking at it.
//
// build123d builds a cylinder along +Z. mesh.go draws one along +Y (its rings
// sit at ±height/2 on y). The correction belongs to the sidecar, and this is
// what holds it there.
func TestKernel_ACylinderPointsTheWayThisSystemDrawsIt(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Deliberately long and thin, so the axis is unmistakable in the extent.
	doc := geometry.Document{
		Name: "pin", Units: "mm",
		Parts: []geometry.Part{{ID: "pin", Name: "Pin", Shape: "cylinder",
			Size:     map[string]float64{"radius": 1, "height": 40},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	// minX,minY,minZ,maxX,maxY,maxZ
	if got.Bounds[1] > -19.9 || got.Bounds[4] < 19.9 {
		t.Errorf("the cylinder does not run along Y: bounds %v", got.Bounds)
	}
	if got.Bounds[0] < -1.1 || got.Bounds[3] > 1.1 || got.Bounds[2] < -1.1 || got.Bounds[5] > 1.1 {
		t.Errorf("the cylinder is not thin in X and Z: bounds %v", got.Bounds)
	}
}

// A box reads width as X, height as Y and depth as Z, the same way the renderer
// does. Three different numbers, so a transposition cannot pass.
func TestKernel_ABoxReadsItsDimensionsTheSameWayTheRendererDoes(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "block", Units: "mm",
		Parts: []geometry.Part{{ID: "b", Name: "Block", Shape: "box",
			Size:     map[string]float64{"width": 2, "height": 20, "depth": 200},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float64{1, 10, 100} {
		if lo, hi := got.Bounds[i], got.Bounds[i+3]; lo > -want*0.99 || hi < want*0.99 {
			t.Errorf("axis %d spans %v..%v, want ±%v — width/height/depth are transposed: %v",
				i, lo, hi, want, got.Bounds)
		}
	}
}

// The kernel and the renderer agree about where a part is.
//
// # Why this is the fence and the earlier one was not
//
// A first version rotated a cylinder ninety degrees about Z and checked its
// extent. It could not fail: reading the rotation matrix's ROWS instead of its
// COLUMNS applies the inverse rotation, and for a single-axis turn of a
// symmetric part the two differ only in sign, so the bounding box is identical.
// A drill swapped rows for columns and the test stayed green.
//
// Two things fix it. A COMPOUND rotation, because M and its transpose then
// disagree about where the part goes; and a box with three different
// dimensions, so no axis can stand in for another.
//
// And the expected answer is not a number written here. It is the RENDERER's
// answer: the same document tessellated by mesh.go, whose triangle vertices for
// a box are exactly its eight corners. That makes this an assertion about the
// property that actually matters — the exported file and the thing on screen
// are the same shape in the same place — rather than about arithmetic the test
// author did once.
func TestKernel_PlacesAPartWhereTheRendererDrawsIt(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "block", Units: "mm",
		Parts: []geometry.Part{{ID: "b", Name: "Block", Shape: "box",
			Size:     map[string]float64{"width": 2, "height": 20, "depth": 200},
			Position: []float64{7, -3, 11},
			// Two axes. One is not enough — see above.
			Rotation: []float64{math.Pi / 2, 0, math.Pi / 2}}},
	}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}

	// The renderer's own answer for the same document.
	mesh := geometry.Tessellate(doc, geometry.Millimetre)
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, tri := range mesh.Triangles() {
		for _, v := range [][3]float64{tri.A, tri.B, tri.C} {
			for i := 0; i < 3; i++ {
				lo[i] = math.Min(lo[i], v[i])
				hi[i] = math.Max(hi[i], v[i])
			}
		}
	}

	const tol = 1e-6
	want := [6]float64{lo[0], lo[1], lo[2], hi[0], hi[1], hi[2]}
	for i := range want {
		if math.Abs(got.Bounds[i]-want[i]) > tol {
			t.Fatalf("the kernel and the renderer disagree about where the part is:\n"+
				"  kernel   %v\n  renderer %v\n"+
				"An exported file that puts a part somewhere other than where it was drawn "+
				"cannot be labelled out of it.", got.Bounds, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Features: the operations that make an assembly a PART (wave 15)
// ---------------------------------------------------------------------------

// bracket is the 2026-09-05 spike's reference part, expressed in this
// vocabulary: a plate with four holes through it and a rounded edge.
//
// It is the part that motivated the whole investigation, and until features
// existed this system could not describe it — only a plate with four small
// cylinders standing on top.
func bracket() geometry.Document {
	const plate, thick, pitch, holeR = 60.0, 6.0, 31.0, 1.75
	doc := geometry.Document{
		Name: "NEMA 17 bracket", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_size", Value: plate, Unit: "mm", How: geometry.Chosen},
			{Name: "fillet_radius", Value: 3, Unit: "mm", How: geometry.Chosen},
		},
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size:     map[string]float64{"width": plate, "height": thick, "depth": plate},
				Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		},
	}
	for i, xz := range [][2]float64{{-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
		doc.Parts = append(doc.Parts, geometry.Part{
			ID: fmt.Sprintf("hole-%d", i), Name: fmt.Sprintf("Hole %d", i), Shape: "cylinder",
			Size:     map[string]float64{"radius": holeR, "height": thick * 4},
			Position: []float64{xz[0] * pitch / 2, 0, xz[1] * pitch / 2},
			Rotation: []float64{0, 0, 0},
		})
	}
	doc.Features = []geometry.Feature{
		{ID: "bolt-holes", Op: "cut", Of: "plate",
			With: []string{"hole-0", "hole-1", "hole-2", "hole-3"}},
		{ID: "rounded-corners", Op: "fillet", Of: "plate",
			RadiusFrom: "fillet_radius", Edges: "vertical"},
	}
	return doc
}

// The headline: a hole is a VOID, and the material it removed is gone.
func TestKernel_CutsAHoleAndRemovesTheMaterial(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := bracket()
	doc.Features = doc.Features[:1] // the cut alone, so the arithmetic is exact

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	// One part. Four tools consumed — a plate with four holes in it is one part,
	// not five, and if the tools survived the holes would be filled by the
	// things that made them.
	if got.Parts != 1 {
		t.Errorf("the file has %d parts, want 1: the cutting tools were not consumed", got.Parts)
	}
	// 60x6x60 minus four ⌀3.5 bores through 6 mm.
	want := 60*6*60 - 4*math.Pi*1.75*1.75*6
	if math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f mm³, want %.4f — the material was not removed", got.Volume, want)
	}
}

// Features apply IN ORDER, and the order changes the part.
func TestKernel_AFilletRoundsWhatIsLeft(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cutOnly := bracket()
	cutOnly.Features = cutOnly.Features[:1]
	a, err := k.BuildDocument(ctx, cutOnly, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := k.BuildDocument(ctx, bracket(), geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.FeatureFailures) > 0 {
		t.Fatalf("the fillet was not applied: %v", b.FeatureFailures)
	}
	// Rounding four corners of a 60 mm plate removes material: (1 - π/4) r² per
	// corner, through the thickness. Small, and it must be there.
	removed := a.Volume - b.Volume
	want := 4 * (1 - math.Pi/4) * 9 * 6
	if math.Abs(removed-want) > 0.01 {
		t.Errorf("the fillet removed %.4f mm³, want %.4f", removed, want)
	}
	// The plate is still 60 mm across: a fillet rounds corners, it does not
	// shrink the part.
	if math.Abs((b.Bounds[3]-b.Bounds[0])-60) > 1e-6 {
		t.Errorf("the plate is now %.4f mm across", b.Bounds[3]-b.Bounds[0])
	}
}

// A real STEP file of a part with holes in it — which is the point of all of it.
func TestKernel_ExportsAPartWithHolesAsOneSolid(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	got, err := k.BuildDocument(ctx, bracket(), geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	body := string(got.STEP)
	if !strings.HasPrefix(body, "ISO-10303-21;") || !strings.Contains(body, "END-ISO-10303-21;") {
		t.Fatal("not a complete STEP file")
	}
	// The bores are analytic cylinders in the solid, not four separate bodies.
	if !strings.Contains(body, "CYLINDRICAL_SURFACE") {
		t.Error("no cylindrical surface: the holes are not in the file")
	}
	if got.Parts != 1 {
		t.Errorf("%d parts in the file, want one bracket", got.Parts)
	}
}

// Fusing says two parts are ONE body. It must actually weld them.
func TestKernel_FuseMakesOneBody(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "tee", Units: "mm",
		Parts: []geometry.Part{
			{ID: "web", Name: "Web", Shape: "box",
				Size:     map[string]float64{"width": 40, "height": 6, "depth": 20},
				Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
			{ID: "rib", Name: "Rib", Shape: "box",
				Size:     map[string]float64{"width": 6, "height": 20, "depth": 20},
				Position: []float64{0, 10, 0}, Rotation: []float64{0, 0, 0}},
		},
		Features: []geometry.Feature{{ID: "weld", Op: "fuse", Of: "web", With: []string{"rib"}}},
	}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parts != 1 {
		t.Fatalf("%d parts, want one fused body", got.Parts)
	}
	// The two OVERLAP: the web spans y −3..3 and the rib y 0..20, so they share
	// 6 x 3 x 20 mm³. A fuse is a union and must not count that twice — which is
	// the property that distinguishes it from adding two numbers, and the reason
	// this fixture deliberately overlaps rather than touching face to face.
	//
	// (The first version of this test asserted the plain sum, 7200. The kernel
	// answered 6840 and was right.)
	const overlap = 6 * 3 * 20
	want := float64(40*6*20 + 6*20*20 - overlap)
	if math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f, want %.0f — the shared material was counted twice, "+
			"so this is a sum and not a union", got.Volume, want)
	}
}

// A feature that cannot be applied must be NAMED. An assembly quietly missing
// the hole somebody asked for is wrong in a way nobody notices.
func TestKernel_AFeatureThatCannotBeAppliedIsNamed(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := bracket()
	// A sphere has no vertical edges, so this rule selects nothing.
	doc.Parts = append(doc.Parts, geometry.Part{ID: "ball", Name: "Ball", Shape: "sphere",
		Size: map[string]float64{"radius": 5}, Position: []float64{0, 40, 0}})
	doc.Features = append(doc.Features, geometry.Feature{
		ID: "impossible", Op: "fillet", Of: "ball", Radius: 1, Edges: "vertical"})

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("one impossible feature lost the whole assembly: %v", err)
	}
	if len(got.FeatureFailures) != 1 || !strings.Contains(got.FeatureFailures[0], "impossible") {
		t.Fatalf("the feature that could not be applied was not named: %v", got.FeatureFailures)
	}
	// And the rest of the part is still there.
	if got.Parts != 2 {
		t.Errorf("%d parts, want the bracket and the ball", got.Parts)
	}
}

// A document in inches exports at the right SCALE.
//
// # The defect this holds shut
//
// A STEP file declares its own unit, and build123d writes
// SI_UNIT(.MILLI.,.METRE.) unconditionally. So a 2 inch cube sent through
// unconverted produced a file saying it was 2 MILLIMETRES — confidently, in a
// format everything downstream treats as exact. A factor of 25.4 in a
// manufacturable artefact, silently.
//
// It was wrong from the first build of this kernel and no test asked, because
// every fixture was already in millimetres. That is the shape of this bug: it
// cannot be found by a suite that only ever speaks one unit.
func TestKernel_ADocumentInInchesIsBuiltAtTheRightScale(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "cube", Units: "in",
		Parts: []geometry.Part{{ID: "c", Name: "Cube", Shape: "box",
			Size:     map[string]float64{"width": 2, "height": 2, "depth": 2},
			Position: []float64{1, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
	got, err := k.BuildDocument(ctx, doc, geometry.Inch, "step")
	if err != nil {
		t.Fatal(err)
	}
	// 2 in = 50.8 mm on a side.
	if want := 50.8 * 50.8 * 50.8; math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.2f mm³, want %.2f — the file states millimetres, so the "+
			"numbers in it have to be millimetres", got.Volume, want)
	}
	// The POSITION converts too. A part an inch off the origin is 25.4 mm off it,
	// and converting the sizes while leaving the placement alone would put a
	// correctly-sized part in the wrong place.
	if want := 25.4 - 25.4; math.Abs(got.Bounds[0]-want) > 1e-6 {
		t.Errorf("minX = %.4f mm, want %.4f — the position was not converted", got.Bounds[0], want)
	}
	if want := 25.4 + 25.4; math.Abs(got.Bounds[3]-want) > 1e-6 {
		t.Errorf("maxX = %.4f mm, want %.4f", got.Bounds[3], want)
	}
	// And the file really does say millimetres, so the numbers above are the
	// ones a machine reads.
	if !strings.Contains(string(got.STEP), "SI_UNIT(.MILLI.,.METRE.)") {
		t.Error("the STEP file does not declare millimetres, so the conversion is against " +
			"the wrong target")
	}
}

// A metre document, to prove the factor is read from the unit and not hardcoded
// for inches.
func TestKernel_AMetreDocumentScalesToo(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "slab", Units: "m",
		Parts: []geometry.Part{{ID: "s", Name: "Slab", Shape: "box",
			Size:     map[string]float64{"width": 1, "height": 1, "depth": 1},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
	got, err := k.BuildDocument(ctx, doc, geometry.Metre, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := 1000.0 * 1000.0 * 1000.0; math.Abs(got.Volume-want) > 1 {
		t.Errorf("volume = %.0f mm³, want %.0f (a 1 m cube)", got.Volume, want)
	}
}

// A unit FORGE cannot convert must produce NOTHING rather than a file at a
// guessed scale.
func TestKernel_AnUnconvertibleUnitIsRefusedRatherThanGuessed(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "thing", Units: "furlongs",
		Parts: []geometry.Part{{ID: "c", Shape: "box",
			Size: map[string]float64{"width": 2, "height": 2, "depth": 2}}},
	}
	if _, err := k.BuildDocument(ctx, doc, geometry.UnitUnspecified, "step"); err == nil {
		t.Fatal("a file was produced for an assembly whose scale nobody stated")
	} else if !strings.Contains(err.Error(), "scale") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Extrusions: the shape that is not a primitive (wave 17)
// ---------------------------------------------------------------------------

// lBracket is the ordinary cross-section that could not be described at all
// before extrusions: concave, which is the case a triangle fan gets wrong and
// the reason the tessellator ear-clips.
func lBracket(depth float64) geometry.Document {
	pts := [][2]float64{{0, 0}, {40, 0}, {40, 8}, {8, 8}, {8, 40}, {0, 40}}
	profile := make([]geometry.Point, len(pts))
	for i, p := range pts {
		profile[i] = geometry.Point{X: p[0], Y: p[1]}
	}
	return geometry.Document{
		Name: "angle", Units: "mm",
		Parts: []geometry.Part{{ID: "angle", Name: "Angle", Shape: "extrusion",
			Profile:  profile,
			Size:     map[string]float64{"depth": depth},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
}

func TestKernel_ExtrudesAConcaveOutline(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	got, err := k.BuildDocument(ctx, lBracket(20), geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	// 40x8 flange plus 8x32 web = 576 mm², swept 20 mm.
	if want := 576.0 * 20; math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f mm³, want %.4f", got.Volume, want)
	}
	// Centred on the extrude axis, and NOT re-centred in the outline's own
	// plane: the author drew the corner at the origin and it stays there.
	want := [6]float64{0, 0, -10, 40, 40, 10}
	for i := range want {
		if math.Abs(got.Bounds[i]-want[i]) > 1e-6 {
			t.Fatalf("bounds %v, want %v — the outline was re-centred, so every coordinate "+
				"somebody wrote has moved", got.Bounds, want)
		}
	}
	if !strings.HasPrefix(string(got.STEP), "ISO-10303-21;") {
		t.Error("no STEP file for an extrusion")
	}
}

// The outline follows the parameters, which is the whole point of writing it as
// expressions rather than numbers.
func TestKernel_AnOutlineFollowsItsParameters(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "angle", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "leg", Value: 40, Unit: "mm", How: geometry.Chosen},
			{Name: "thickness", Value: 8, Unit: "mm", How: geometry.Chosen},
		},
		Parts: []geometry.Part{{ID: "angle", Name: "Angle", Shape: "extrusion",
			Profile: []geometry.Point{
				{X: 0, Y: 0},
				{XFrom: "leg", Y: 0},
				{XFrom: "leg", YFrom: "thickness"},
				{XFrom: "thickness", YFrom: "thickness"},
				{XFrom: "thickness", YFrom: "leg"},
				{X: 0, YFrom: "leg"},
			},
			Size:     map[string]float64{"depth": 20},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}

	first, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := 576.0 * 20; math.Abs(first.Volume-want) > 0.01 {
		t.Fatalf("volume = %.4f, want %.4f", first.Volume, want)
	}

	// Change one parameter, and the outline moves with it.
	bigger, problems := doc.WithParameters(map[string]float64{"leg": 60})
	for _, p := range problems {
		if p.Severity == geometry.Error {
			t.Fatalf("%s: %s", p.Name, p.Detail)
		}
	}
	got, err := k.BuildDocument(ctx, *bigger, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	// 60x8 + 8x52 = 896 mm².
	if want := 896.0 * 20; math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f mm³, want %.4f — the outline did not follow the parameter",
			got.Volume, want)
	}
	if math.Abs(got.Bounds[3]-60) > 1e-6 {
		t.Errorf("the flange still reaches %.4f, want 60", got.Bounds[3])
	}
}

// Features apply to an extrusion like any other part, which is what makes the
// vocabulary compose rather than being two separate systems.
func TestKernel_AnExtrusionTakesHolesAndFillets(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := lBracket(20)
	// A bolt hole through the FLANGE, so the axis runs along Y — which is where
	// a cylinder points here without any rotation at all.
	//
	// The first version of this rotated the bore 90 degrees about X, which put
	// it along the extrude direction and through 20 mm of depth rather than
	// through 8 mm of flange. The kernel returned the volume for that correctly
	// and the assertion was the thing that was wrong.
	doc.Parts = append(doc.Parts, geometry.Part{ID: "bore", Name: "Bore", Shape: "cylinder",
		Size:     map[string]float64{"radius": 3, "height": 40},
		Position: []float64{24, 4, 0}, Rotation: []float64{0, 0, 0}})
	doc.Features = []geometry.Feature{{ID: "hole", Op: "cut", Of: "angle", With: []string{"bore"}}}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FeatureFailures) > 0 {
		t.Fatalf("the cut was not applied to the extrusion: %v", got.FeatureFailures)
	}
	if got.Parts != 1 {
		t.Errorf("%d parts, want one bracket with a hole in it", got.Parts)
	}
	// A ⌀6 bore through the 8 mm flange.
	if want := 576.0*20 - math.Pi*9*8; math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f mm³, want %.4f", got.Volume, want)
	}
}

// An outline that crosses itself must not become a shape.
func TestKernel_ASelfCrossingOutlineIsNotBuilt(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	doc := lBracket(20)
	doc.Parts[0].Profile = []geometry.Point{{X: 0, Y: 0}, {X: 10, Y: 10}, {X: 10, Y: 0}, {X: 0, Y: 10}}

	_, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err == nil {
		t.Fatal("a bow-tie was built into a solid")
	}
	if !strings.Contains(err.Error(), "nothing to export") {
		t.Logf("refusal: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Revolves: the turned part (wave 18)
// ---------------------------------------------------------------------------

func revolveDoc(pts [][2]float64, axis string) geometry.Document {
	profile := make([]geometry.Point, len(pts))
	for i, p := range pts {
		profile[i] = geometry.Point{X: p[0], Y: p[1]}
	}
	return geometry.Document{
		Name: "turned", Units: "mm",
		Parts: []geometry.Part{{ID: "t", Name: "Turned", Shape: "revolve",
			Profile: profile, Axis: axis,
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
}

// The figures come from Pappus, so the expectations are arithmetic somebody can
// check rather than numbers observed once.
func TestKernel_RevolvesAnOutline(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, tc := range []struct {
		name string
		pts  [][2]float64
		axis string
		want float64
	}{
		{"a ring about Y", [][2]float64{{10, 0}, {20, 0}, {20, 5}, {10, 5}}, "y",
			math.Pi * (400 - 100) * 5},
		{"a ring about X", [][2]float64{{0, 10}, {5, 10}, {5, 20}, {0, 20}}, "x",
			math.Pi * (400 - 100) * 5},
		// The outline TOUCHES the axis, which is the usual case for a dome or a
		// point and the one where a swept facet collapses to nothing.
		{"a cone", [][2]float64{{0, 0}, {10, 0}, {0, 20}}, "y", math.Pi * 100 * 20 / 3},
		// Wound the other way. The kernel must not care.
		{"a clockwise ring", [][2]float64{{10, 5}, {20, 5}, {20, 0}, {10, 0}}, "y",
			math.Pi * (400 - 100) * 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := k.BuildDocument(ctx, revolveDoc(tc.pts, tc.axis), geometry.Millimetre, "")
			if err != nil {
				t.Fatal(err)
			}
			// A real B-Rep, so the surface is exact rather than tessellated.
			if math.Abs(got.Volume-tc.want) > 0.01 {
				t.Errorf("volume = %.4f mm³, want %.4f", got.Volume, tc.want)
			}
		})
	}
}

// A revolved solid exports as a real B-Rep with analytic surfaces, which is the
// whole reason for turning it in the kernel rather than sweeping facets.
func TestKernel_ARevolveExportsAnalyticSurfaces(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	got, err := k.BuildDocument(ctx,
		revolveDoc([][2]float64{{10, 0}, {20, 0}, {20, 5}, {10, 5}}, "y"),
		geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	body := string(got.STEP)
	if !strings.Contains(body, "CYLINDRICAL_SURFACE") {
		t.Error("the turned walls are not analytic cylinders")
	}
	if strings.Contains(body, "TRIANGULATED_FACE_SET") {
		t.Error("the file contains facets — this is a mesh wearing a STEP extension")
	}
	// The extent is the full swept circle, not the outline's own reach.
	if math.Abs(got.Bounds[0]+20) > 1e-6 || math.Abs(got.Bounds[3]-20) > 1e-6 {
		t.Errorf("x spans %v..%v, want -20..20 — the outline was not swept all the way round",
			got.Bounds[0], got.Bounds[3])
	}
}

// An outline that crosses its own axis sweeps through itself. OCCT refuses it
// with "BRep_API: command not done", which names nothing — so it is caught
// earlier, where the axis and the offending coordinate can both be named.
func TestKernel_AnOutlineCrossingItsAxisIsRefusedWithAReason(t *testing.T) {
	doc := revolveDoc([][2]float64{{-5, 0}, {10, 0}, {10, 5}, {-5, 5}}, "y")

	problems := doc.ProfileProblems()
	if len(problems) == 0 {
		t.Fatal("an outline crossing its axis was accepted")
	}
	detail := problems[0].Detail
	if !strings.Contains(detail, "one side of that axis") {
		t.Errorf("the refusal does not say what the rule is: %q", detail)
	}
	// BOTH points, because naming one is arbitrary: the fault is that two of
	// them disagree, and "point 2 has x = 10" reads as an accusation against a
	// point that may be perfectly correct.
	if !strings.Contains(detail, "-5") || !strings.Contains(detail, "10") {
		t.Errorf("the refusal does not name both sides of the axis: %q", detail)
	}
	// And nothing is built from it.
	solids, _ := geometry.Solids(doc, geometry.Millimetre)
	if len(solids) != 0 {
		t.Errorf("%d solids were built from an outline that is not a solid", len(solids))
	}
}

// Features apply to a revolve like anything else, which is what keeps this one
// vocabulary rather than three.
func TestKernel_ARevolveTakesAHole(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A disc, 40 across and 6 thick, with a 10 mm bore through the middle.
	doc := revolveDoc([][2]float64{{0, 0}, {20, 0}, {20, 6}, {0, 6}}, "y")
	doc.Parts = append(doc.Parts, geometry.Part{ID: "bore", Name: "Bore", Shape: "cylinder",
		Size: map[string]float64{"radius": 5, "height": 40}, Position: []float64{0, 3, 0}})
	doc.Features = []geometry.Feature{{ID: "bore-it", Op: "cut", Of: "t", With: []string{"bore"}}}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FeatureFailures) > 0 {
		t.Fatalf("the cut was not applied to the revolve: %v", got.FeatureFailures)
	}
	if want := math.Pi * (400 - 25) * 6; math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f mm³, want %.4f", got.Volume, want)
	}
}

func sweepDoc(profile [][2]float64, path [][3]float64) geometry.Document {
	pts := make([]geometry.Point, len(profile))
	for i, p := range profile {
		pts[i] = geometry.Point{X: p[0], Y: p[1]}
	}
	way := make([]geometry.Point, len(path))
	for i, p := range path {
		way[i] = geometry.Point{X: p[0], Y: p[1], Z: p[2]}
	}
	return geometry.Document{
		Name: "run", Units: "mm",
		Parts: []geometry.Part{{ID: "s", Name: "Run", Shape: "sweep",
			Profile: pts, Path: way,
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
}

func pathLength(path [][3]float64) float64 {
	var total float64
	for i := 0; i+1 < len(path); i++ {
		d := [3]float64{path[i+1][0] - path[i][0], path[i+1][1] - path[i][1], path[i+1][2] - path[i][2]}
		total += math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])
	}
	return total
}

// A real B-Rep sweep encloses area × path length, exactly, however it bends.
//
// # Why this figure and not one observed once
//
// At every bend the section sits in the plane bisecting the two segments, which
// cuts a wedge off the inside of the corner and adds an equal one outside. They
// cancel when the outline's centroid rides the path — so the volume is
// arithmetic a reader can check, in the same way the revolve figures come from
// Pappus.
//
// It is also the assertion that pins the KERNEL's transition mode. Measured
// 2026-09-05 on build123d 0.11.1, OCCT's default (Transition.TRANSFORMED)
// returned 1600 mm³ for the elbow below, whose correct volume is 5000, and
// Transition.ROUND returned 4946 by rounding a corner nobody asked to have
// rounded. Neither is a small error and neither announces itself.
func TestKernel_SweepsAnOutlineAlongAPath(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	square := [][2]float64{{-5, -5}, {5, -5}, {5, 5}, {-5, 5}}
	const area = 100.0

	for _, tc := range []struct {
		name string
		pts  [][2]float64
		path [][3]float64
	}{
		{"straight up", square, [][3]float64{{0, 0, 0}, {0, 0, 20}}},
		// Setting off along +X: OCCT sweeps a section along the path WITHOUT
		// reorienting it, so a section left in its own plane comes back flat
		// with zero volume (measured 2026-09-05). The section frame FORGE sends
		// is what stops that.
		{"straight along x", square, [][3]float64{{0, 0, 0}, {20, 0, 0}}},
		{"a right-angled elbow", square, [][3]float64{{0, 0, 0}, {0, 0, 20}, {30, 0, 20}}},
		{"three bends, out of plane", square,
			[][3]float64{{0, 0, 0}, {0, 0, 20}, {30, 0, 20}, {30, 25, 20}}},
		// A concave section, so the mitre is applied to a shape that ear
		// clipping and OCCT have to agree about.
		{"a channel section", [][2]float64{{-10, -5}, {10, -5}, {10, 5}, {6, 5}, {6, -1},
			{-6, -1}, {-6, 5}, {-10, 5}}, [][3]float64{{0, 0, 0}, {0, 0, 40}, {35, 0, 40}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := sweepDoc(tc.pts, tc.path)
			got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
			if err != nil {
				t.Fatal(err)
			}
			sectionArea := area
			if tc.name == "a channel section" {
				sectionArea = 20*10 - 12*6 // 200 less the notch
			}
			want := sectionArea * pathLength(tc.path)
			if math.Abs(got.Volume-want) > 0.01 {
				t.Errorf("volume = %.4f mm³, want %.4f — a short figure means the mitre is "+
					"eating material the bend should keep, and a long one means the corners "+
					"overlap", got.Volume, want)
			}
		})
	}
}

// The kernel builds the solid the viewport DREW, down to its extents.
//
// # Why volume is not enough here, and what this is really guarding
//
// Three things about a sweep have more than one defensible answer and none of
// them changes the volume: which way up the section starts, how it is carried
// round a bend, and where the mitre puts the corner. A kernel that framed the
// section its own way would return the same number for a solid rotated out of
// the drawing.
//
// So the section frame is computed ONCE, in geometry.sweptSections, and travels
// to the kernel as a matrix — the same bargain the placement matrix already
// makes. This asserts the bargain held: same volume AND same box, from an
// outline with no rotational symmetry at all, on a path that leaves its first
// plane.
//
// A swept polygon has no curved surface anywhere on it, so the two are the same
// polyhedron and the agreement is EXACT. That is not true of a revolve, whose
// facets are inscribed in the real surface.
func TestKernel_ASweptSolidIsTheOneTheRendererDrew(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// An L, so a section rolled by 90° would put the long leg somewhere else.
	//
	// The path sets off along +X and NOT along +Z, deliberately: a path that
	// starts up local Z is framed by the identity, so a kernel that ignored the
	// frame entirely would build the right solid anyway and this test would hold
	// nothing. A drill on 2026-09-05 made exactly that substitution against an
	// earlier version of this test and it stayed green.
	//
	// The two bends are in DIFFERENT planes for the same class of reason: a
	// single bend cannot tell a carried frame from a recomputed one.
	profile := [][2]float64{{-2, -6}, {6, -6}, {6, -2}, {2, -2}, {2, 6}, {-2, 6}}
	path := [][3]float64{{0, 0, 0}, {30, 0, 0}, {30, 0, 25}, {30, 20, 25}}
	doc := sweepDoc(profile, path)

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}

	mesh := geometry.Tessellate(doc, geometry.Millimetre)
	var drawn float64
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, tr := range mesh.Triangles() {
		drawn += (tr.A[0]*(tr.B[1]*tr.C[2]-tr.C[1]*tr.B[2]) -
			tr.A[1]*(tr.B[0]*tr.C[2]-tr.C[0]*tr.B[2]) +
			tr.A[2]*(tr.B[0]*tr.C[1]-tr.C[0]*tr.B[1])) / 6
		for _, v := range [][3]float64{tr.A, tr.B, tr.C} {
			for axis := 0; axis < 3; axis++ {
				lo[axis] = math.Min(lo[axis], v[axis])
				hi[axis] = math.Max(hi[axis], v[axis])
			}
		}
	}

	if math.Abs(got.Volume-drawn) > 1e-6*math.Max(1, drawn) {
		t.Errorf("the kernel built %.6f mm³ and the viewport drew %.6f — a swept polygon has "+
			"no curved surface, so these are the same polyhedron or one of them is wrong",
			got.Volume, drawn)
	}
	for axis, name := range []string{"x", "y", "z"} {
		if math.Abs(got.Bounds[axis]-lo[axis]) > 1e-6 || math.Abs(got.Bounds[axis+3]-hi[axis]) > 1e-6 {
			t.Errorf("%s: the kernel spans %v..%v and the viewport %v..%v — same volume, "+
				"different box, which is what a section framed differently looks like",
				name, got.Bounds[axis], got.Bounds[axis+3], lo[axis], hi[axis])
		}
	}
}

// A sweep along a straight path IS the extrusion, in the kernel too.
//
// The renderer's copy of this property is asserted facet for facet
// (TestSwept_AStraightPathUpZIsExactlyTheExtrusion). This is the other half:
// that the kernel does not quietly build a different solid from the same
// document — which it would if the section were framed, placed or centred
// differently on the two paths through the code.
func TestKernel_AStraightSweepIsTheExtrusion(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	outline := [][2]float64{{0, 0}, {40, 0}, {40, 8}, {8, 8}, {8, 40}, {0, 40}}
	swept := sweepDoc(outline, [][3]float64{{0, 0, -10}, {0, 0, 10}})

	extruded := swept
	extruded.Parts = []geometry.Part{{ID: "e", Name: "Angle", Shape: "extrusion",
		Profile: swept.Parts[0].Profile, Size: map[string]float64{"depth": 20},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}}

	a, err := k.BuildDocument(ctx, swept, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := k.BuildDocument(ctx, extruded, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(a.Volume-b.Volume) > 1e-6 {
		t.Errorf("the sweep encloses %.6f and the extrusion %.6f", a.Volume, b.Volume)
	}
	for i := range a.Bounds {
		if math.Abs(a.Bounds[i]-b.Bounds[i]) > 1e-6 {
			t.Errorf("the sweep spans %v and the extrusion %v — a straight path is an "+
				"extrusion, so anything else here is a second convention that has drifted",
				a.Bounds, b.Bounds)
			break
		}
	}
}

// The three paths that are not solids never reach the kernel.
//
// # Why each is caught in Go
//
// A repeated point comes back from OCCT as "BRep_API: command not done" and a
// reversal comes back with an EMPTY message, so neither reaches a reader as
// anything actionable. The third is worse and is the reason this test exists: a
// bend tighter than its own outline is NOT refused by OCCT at all. It returned a
// solid of 14546 mm³ for a shape whose surface folds through itself — a
// plausible number, a file that opens, and nothing anywhere saying it is wrong.
func TestKernel_APathThatIsNotASolidIsRefusedWithAReason(t *testing.T) {
	square := [][2]float64{{-5, -5}, {5, -5}, {5, 5}, {-5, 5}}
	for _, tc := range []struct {
		name, wants string
		path        [][3]float64
	}{
		{"a repeated point", "zero length", [][3]float64{{0, 0, 0}, {0, 0, 0}, {0, 0, 20}}},
		{"a reversal", "doubles back", [][3]float64{{0, 0, 0}, {0, 0, 20}, {0, 0, 5}}},
		{"a bend tighter than the outline", "bends too tightly",
			[][3]float64{{0, 0, 0}, {0, 0, 3}, {20, 0, 3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := sweepDoc(square, tc.path)
			problems := doc.ProfileProblems()
			if len(problems) == 0 {
				t.Fatal("this path was accepted, and what it sweeps is not a solid")
			}
			if !strings.Contains(problems[0].Detail, tc.wants) {
				t.Errorf("the refusal does not say what is wrong: %q", problems[0].Detail)
			}
			if solids, _ := geometry.Solids(doc, geometry.Millimetre); len(solids) != 0 {
				t.Errorf("%d solids were built from a path that is not one", len(solids))
			}
		})
	}
}

// Features apply to a sweep like anything else, which is what keeps this one
// vocabulary rather than four.
func TestKernel_ASweepTakesAHole(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A 20×10 bar carried 60 mm up local Z, with a 6 mm hole through its side.
	doc := sweepDoc([][2]float64{{-10, -5}, {10, -5}, {10, 5}, {-10, 5}},
		[][3]float64{{0, 0, 0}, {0, 0, 60}})
	doc.Parts = append(doc.Parts, geometry.Part{ID: "hole", Name: "Hole", Shape: "cylinder",
		Size: map[string]float64{"radius": 3, "height": 40}, Position: []float64{0, 0, 30},
		Rotation: []float64{0, 0, 0}})
	doc.Features = []geometry.Feature{{ID: "drill", Op: "cut", Of: "s", With: []string{"hole"}}}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FeatureFailures) > 0 {
		t.Fatalf("the cut was not applied to the sweep: %v", got.FeatureFailures)
	}
	// The bar is 200 mm² × 60 mm; the hole is a 3 mm cylinder through 10 mm of
	// it, drilled along the bar's own y.
	if want := 200.0*60 - math.Pi*9*10; math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f mm³, want %.4f — the hole was not taken out of the sweep",
			got.Volume, want)
	}
}

// A rounded corner comes out of the kernel as a REAL ARC, not as facets.
//
// # Why that is the whole point of sending curves rather than points
//
// The tessellators flatten an arc into chords, because they draw triangles. If
// the kernel were sent the same chords, a rounded corner would arrive in the
// exported STEP as a forty-sided prism — a mesh wearing a solid model's
// extension, which this repository refuses to write anywhere else and would have
// no business writing here.
//
// So the volume is checked against arithmetic a reader can do — each rounded
// right angle removes r² and puts back πr²/4, so a w×h plate loses (4−π)r² — and
// the FILE is checked for the surface that proves it was not faked.
func TestKernel_ARoundedCornerIsARealArc(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "plate", Units: "mm",
		Parts: []geometry.Part{{ID: "p", Name: "Plate", Shape: "extrusion",
			Profile: []geometry.Point{
				{X: 0, Y: 0, Radius: 10}, {X: 40, Y: 0, Radius: 10},
				{X: 40, Y: 40, Radius: 10}, {X: 0, Y: 40, Radius: 10}},
			Size:     map[string]float64{"depth": 5},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	if want := (1600 - (4-math.Pi)*100) * 5; math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f mm³, want %.4f — a rounded corner removes r² and puts back "+
			"a quarter circle, and this is four of them", got.Volume, want)
	}
	body := string(got.STEP)
	if !strings.Contains(body, "CYLINDRICAL_SURFACE") {
		t.Error("the rounded corners are not analytic cylinders — the kernel was sent chords " +
			"rather than arcs, and every radius in this file is a many-sided prism")
	}
	if strings.Contains(body, "TRIANGULATED_FACE_SET") {
		t.Error("the file contains facets — this is a mesh wearing a STEP extension")
	}
	// The plate still measures 40 across: rounding takes material off the
	// corners and must not shrink the part.
	if math.Abs(got.Bounds[0]) > 1e-6 || math.Abs(got.Bounds[3]-40) > 1e-6 {
		t.Errorf("x spans %v..%v, want 0..40", got.Bounds[0], got.Bounds[3])
	}
}

// A slot: the radius is half the width, so the two arcs at each end meet and the
// straight between them vanishes entirely.
//
// # Why this case and not another rounded rectangle
//
// It is the one where an off-by-anything in the corner arithmetic produces a
// zero-length edge, and OCCT refuses those outright rather than tolerating them.
// A kernel that built this is a kernel that got the tangent points exactly
// right. The area is also arrived at two independent ways — 40×20 − (4−π)·10²,
// and a 20×20 rectangle plus a full circle of radius 10 — which agree at
// 714.159.
func TestKernel_ASlotIsTwoArcsThatMeet(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "slot", Units: "mm",
		Parts: []geometry.Part{{ID: "s", Name: "Slot", Shape: "extrusion",
			Profile: []geometry.Point{
				{X: 0, Y: 0, Radius: 10}, {X: 40, Y: 0, Radius: 10},
				{X: 40, Y: 20, Radius: 10}, {X: 0, Y: 20, Radius: 10}},
			Size:     map[string]float64{"depth": 6},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("a slot did not build — the two arcs at each end almost certainly left a "+
			"zero-length edge between them: %v", err)
	}
	if want := (20*20 + math.Pi*100) * 6; math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f mm³, want %.4f (a 20×20 rectangle plus a ⌀20 circle, 6 thick)",
			got.Volume, want)
	}
}

// A bend radius on a path is a real bend, and it measures what a bender would
// set.
//
// # The arithmetic
//
// Rounding a right-angled corner of the path with radius r shortens each leg by
// r and replaces the corner with a quarter circle of length πr/2. Pappus does
// the rest: a section whose centroid rides the path sweeps area × the distance
// the centroid travels, arcs included. So a 10×10 bar up 40, round R12, and out
// 30 encloses 100 × (28 + 18.8496 + 18).
//
// This is also the case that separates a real bend from a mitre: a mitred corner
// is 100 × 70 = 7000, and this is 6484.96. Nothing subtle would tell them apart
// on a screen; the numbers do.
func TestKernel_ABendRadiusIsARealBend(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := sweepDoc([][2]float64{{-5, -5}, {5, -5}, {5, 5}, {-5, 5}},
		[][3]float64{{0, 0, 0}, {0, 0, 40}, {30, 0, 40}})
	doc.Parts[0].Path[1].Radius = 12

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	want := 100 * (28 + math.Pi*12/2 + 18)
	if math.Abs(got.Volume-want) > 0.01 {
		mitred := 100.0 * 70
		t.Errorf("volume = %.4f mm³, want %.4f. A MITRED corner would be %.4f — if that is "+
			"what came back, the bend radius was dropped somewhere between the document and "+
			"the kernel", got.Volume, want, mitred)
	}
	if !strings.Contains(string(got.STEP), "TOROIDAL_SURFACE") &&
		!strings.Contains(string(got.STEP), "CYLINDRICAL_SURFACE") {
		t.Error("the bend is not a swept analytic surface, so the exported elbow is faceted")
	}
}

// With a bend radius, the kernel and the viewport no longer agree exactly — and
// they agree to within the number FORGE reports for that.
//
// # Why this is a separate test from the exact one
//
// TestKernel_ASweptSolidIsTheOneTheRendererDrew asserts EQUALITY, and that is
// still true and still worth holding: a sweep with sharp corners is a
// polyhedron, and both build the same one. An arc is the only thing that makes a
// sweep an approximation, and once there is one the honest claim changes shape —
// from "the same solid" to "inside it, by no more than we said".
//
// The direction matters as much as the size. Chords are INSIDE the arc, so the
// drawn solid must be smaller than the built one. A drawn solid that came out
// LARGER would mean the deviation is being reported in the wrong direction, and
// somebody machining to the exported file would be cutting into material the
// picture said was there.
func TestKernel_AroundABendTheViewportIsInsideTheSolidBySomethingWeCanState(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := sweepDoc([][2]float64{{-5, -5}, {5, -5}, {5, 5}, {-5, 5}},
		[][3]float64{{0, 0, 0}, {0, 0, 40}, {30, 0, 40}})
	doc.Parts[0].Path[1].Radius = 12

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	mesh := geometry.Tessellate(doc, geometry.Millimetre)
	var drawn float64
	for _, tr := range mesh.Triangles() {
		drawn += (tr.A[0]*(tr.B[1]*tr.C[2]-tr.C[1]*tr.B[2]) -
			tr.A[1]*(tr.B[0]*tr.C[2]-tr.C[0]*tr.B[2]) +
			tr.A[2]*(tr.B[0]*tr.C[1]-tr.C[0]*tr.B[1])) / 6
	}
	if drawn > got.Volume {
		t.Errorf("the viewport drew %.6f and the kernel built %.6f — the drawn solid is "+
			"BIGGER, so the chords are outside the arc and the deviation is reported the "+
			"wrong way round", drawn, got.Volume)
	}
	if len(mesh.Deviations) != 1 {
		t.Fatalf("%d deviations reported for a part with a bend radius in it; a person is "+
			"told nothing about what the flattening cost", len(mesh.Deviations))
	}
	// The shortfall is bounded by the deviation over the swept surface. A loose
	// bound on purpose: what is asserted is that the gap is EXPLAINED by the
	// number reported, not that it equals some other formula.
	dev := mesh.Deviations[0].Max.Value()
	if dev <= 0 {
		t.Fatal("the reported deviation is zero for a shape that is demonstrably faceted")
	}
	if shortfall := got.Volume - drawn; shortfall > dev*got.Volume {
		t.Errorf("the viewport is %.6f mm³ short of the built solid, which the reported "+
			"deviation of %.6f mm does not account for", shortfall, dev)
	}
}

// The case holes exist for: a bent tube whose bore follows the path.
//
// # Why a cut cannot do this, and why that is the whole argument
//
// "A hole is not a part — it is the absence of one" is still right, and a bolt
// hole through a plate is still a cylinder cut out of it. That rule was written
// when the only outline shape was an extrusion, where the two are
// interchangeable.
//
// They are not interchangeable here. The bore of a bent tube turns the corner
// with the tube. It is not a cylinder, and it is not any other shape the feature
// vocabulary can place in space — it is whatever the path is, offset inward. So
// it has to be part of the SECTION.
//
// The mitre identity does the checking, on the wall's area rather than the
// outline's: 256 mm² carried 70 mm. The outline's own 400 mm² would give 28000,
// which is a solid bar with no bore at all.
func TestKernel_ABentTubeIsHollowRoundTheCorner(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := sweepDoc([][2]float64{{-10, -10}, {10, -10}, {10, 10}, {-10, 10}},
		[][3]float64{{0, 0, 0}, {0, 0, 40}, {30, 0, 40}})
	doc.Parts[0].Holes = [][]geometry.Point{{
		{X: -6, Y: -6}, {X: 6, Y: -6}, {X: 6, Y: 6}, {X: -6, Y: 6}}}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	if want := float64(400-144) * 70; math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f mm³, want %.4f. %.0f would mean the bore was dropped and "+
			"the tube exported solid", got.Volume, want, 400.0*70)
	}
	if !bytes.HasPrefix(got.STEP, []byte("ISO-10303-21;")) {
		t.Fatal("no STEP came back")
	}
}

// A hollow section survives the rest of the vocabulary: rounded corners on both
// loops, a bend radius, and a feature applied afterwards.
//
// # Why all of it at once
//
// Each of these was built and tested on its own. What this asks is whether they
// COMPOSE — whether the bore is rounded by its own radii and carried round a
// real bend while a cut is taken out of the result — because the failure mode of
// three features that each work alone is a document that uses two of them.
//
// The volume is arithmetic again. The wall is a 20×20 square with R4 corners,
// less a 12×12 bore with R2 corners; the path is 40 up, R10 bend, 30 out.
func TestKernel_AHollowSectionComposesWithRadiiAndBends(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	round := func(pts [][2]float64, r float64) []geometry.Point {
		out := make([]geometry.Point, len(pts))
		for i, p := range pts {
			out[i] = geometry.Point{X: p[0], Y: p[1], Radius: r}
		}
		return out
	}
	doc := geometry.Document{
		Name: "tube", Units: "mm",
		Parts: []geometry.Part{{ID: "t", Name: "Tube", Shape: "sweep",
			Profile: round([][2]float64{{-10, -10}, {10, -10}, {10, 10}, {-10, 10}}, 4),
			Holes: [][]geometry.Point{
				round([][2]float64{{-6, -6}, {6, -6}, {6, 6}, {-6, 6}}, 2)},
			// The bend radius is 25 and NOT 10. A section reaching 10 mm inward
			// round a 10 mm bend puts the inside of the bend at radius zero: the
			// surface folds through itself, and FORGE refuses the document before the
			// kernel ever sees it. That is the fold check meeting real geometry, and
			// it is also the real rule — a bender needs a radius bigger than the tube.
			Path:     []geometry.Point{{}, {Z: 40, Radius: 25}, {X: 30, Z: 40}},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	// Each rounded right angle removes (4−π)r²/4; four of them remove (4−π)r².
	wall := (400 - (4-math.Pi)*16) - (144 - (4-math.Pi)*4)
	length := 15 + math.Pi*25/2 + 5
	if want := wall * length; math.Abs(got.Volume-want) > 0.5 {
		t.Errorf("volume = %.4f mm³, want %.4f — a wall of %.4f mm² carried %.4f mm",
			got.Volume, want, wall, length)
	}
}

// A closed path sweeps a ring, with no ends and no caps.
//
// # The arithmetic, and what a wrong answer would mean
//
// Every vertex of a closed path is a mitre joint, the seam included, so the
// identity survives: area × the whole perimeter. A figure near zero would mean
// the seam did not meet; one that is too large would mean caps were built at a
// seam that has no ends to cap.
//
// The rounded case is the two features composing: a bend radius at every corner
// of a closed loop, which is what a real formed ring is. Its centreline is four
// straights of 30 plus a full circle of radius 15 — the four quarter-turns add up
// to one — so 120 + 2π·15.
func TestKernel_AClosedPathSweepsARing(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	square := [][2]float64{{-5, -5}, {5, -5}, {5, 5}, {-5, 5}}
	ring := [][3]float64{{0, 0, 0}, {60, 0, 0}, {60, 60, 0}, {0, 60, 0}}

	for _, tc := range []struct {
		name   string
		radius float64
		want   float64
	}{
		{"mitred corners", 0, 100 * 240},
		{"a bend radius at every corner", 15, 100 * (120 + 2*math.Pi*15)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := sweepDoc(square, ring)
			doc.Parts[0].PathClosed = true
			if tc.radius > 0 {
				for i := range doc.Parts[0].Path {
					doc.Parts[0].Path[i].Radius = tc.radius
				}
			}
			got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got.Volume-tc.want) > 0.01 {
				t.Errorf("volume = %.4f mm³, want %.4f", got.Volume, tc.want)
			}
			// A ring is hollow in the middle: it spans the whole loop and the
			// section's half-width either side of it.
			if math.Abs(got.Bounds[0]+5) > 1e-6 || math.Abs(got.Bounds[3]-65) > 1e-6 {
				t.Errorf("x spans %v..%v, want -5..65", got.Bounds[0], got.Bounds[3])
			}
		})
	}
}

// The viewport and the kernel agree about a closed sweep, exactly.
//
// The seam is the one place a closed sweep can differ from an open one, and it
// is where two implementations would differ if either got the wrap-around wrong:
// a missing pair of walls, or a cap built where there is no end. Neither shows
// in a silhouette, and a missing seam wall shows in the VOLUME as a collapse
// rather than as a small error — which is what makes this worth asserting as
// equality rather than as a tolerance.
func TestKernel_AClosedSweepIsTheOneTheRendererDrew(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := sweepDoc([][2]float64{{-2, -6}, {6, -6}, {6, -2}, {2, -2}, {2, 6}, {-2, 6}},
		[][3]float64{{0, 0, 0}, {60, 0, 0}, {60, 60, 0}, {0, 60, 0}})
	doc.Parts[0].PathClosed = true

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	mesh := geometry.Tessellate(doc, geometry.Millimetre)
	var drawn float64
	for _, tr := range mesh.Triangles() {
		drawn += (tr.A[0]*(tr.B[1]*tr.C[2]-tr.C[1]*tr.B[2]) -
			tr.A[1]*(tr.B[0]*tr.C[2]-tr.C[0]*tr.B[2]) +
			tr.A[2]*(tr.B[0]*tr.C[1]-tr.C[0]*tr.B[1])) / 6
	}
	if math.Abs(got.Volume-drawn) > 1e-6*math.Max(1, drawn) {
		t.Errorf("the kernel built %.6f mm³ and the viewport drew %.6f — nothing here is "+
			"curved, so these are the same polyhedron or one of them has its seam wrong",
			got.Volume, drawn)
	}
}

// The two drawings the eval suite lost, built.
//
// # Why these exact documents
//
// Both are transcribed from what qwen-plus actually returned on 2026-09-05, and
// both were refused outright — the part simply absent from the file. Six of the
// seven refusals across 24 eval runs were the first pattern.
//
// A test that only checked they no longer error would miss the thing that
// matters about the second: the repeated closing point CARRIED the bend radius
// while the original did not, so reading the point away and leaving the radius
// behind would build a loop with a mitred corner where one was asked to be bent.
// Same volume to within a fraction, same silhouette, different part. So the
// volume is checked against the arithmetic for a loop that really is bent.
func TestKernel_TheDrawingsTheEvalSuiteLostNowBuild(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Run("an outline that repeats its first point", func(t *testing.T) {
		// A pulley's vee groove, closed the way every polygon format closes a
		// ring. Pappus: a triangle of area 28.8 mm² whose centroid sits at
		// x = 38.4 sweeps 2π · 38.4 · 28.8.
		doc := geometry.Document{
			Name: "groove", Units: "mm",
			Parts: []geometry.Part{{ID: "g", Name: "Groove", Shape: "revolve", Axis: "y",
				Profile: []geometry.Point{{X: 40, Y: -10}, {X: 40, Y: -22},
					{X: 35.2, Y: -22}, {X: 40, Y: -10}},
				Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
		}
		got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
		if err != nil {
			t.Fatalf("a drawing closed the way GeoJSON closes one was refused: %v", err)
		}
		if want := 2 * math.Pi * 38.4 * 28.8; math.Abs(got.Volume-want) > 0.01 {
			t.Errorf("volume = %.4f mm³, want %.4f", got.Volume, want)
		}
	})

	t.Run("a closed path whose repeated point carries the radius", func(t *testing.T) {
		doc := geometry.Document{
			Name: "handle", Units: "mm",
			Parts: []geometry.Part{{ID: "h", Name: "Handle", Shape: "sweep",
				Profile: []geometry.Point{{X: -4, Y: -4}, {X: 4, Y: -4}, {X: 4, Y: 4}, {X: -4, Y: 4}},
				Path: []geometry.Point{{}, {X: 240, Radius: 20}, {X: 240, Y: 90, Radius: 20},
					{Y: 90, Radius: 20}, {Radius: 20}},
				PathClosed: true,
				Position:   []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
		}
		got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
		if err != nil {
			t.Fatalf("a closed loop closed the way every polygon format closes one was "+
				"refused: %v", err)
		}
		// 64 mm² carried round: two sides of 200, two of 50, and four quarter
		// turns of R20, which between them make one whole circle.
		want := 64 * (400 + 100 + 2*math.Pi*20)
		if math.Abs(got.Volume-want) > 0.5 {
			mitred := 64.0 * (2*240 + 2*90)
			t.Errorf("volume = %.4f mm³, want %.4f. A loop with MITRED corners is %.4f — if "+
				"that is what came back, the radius on the repeated closing point was dropped "+
				"with the point", got.Volume, want, mitred)
		}
	})
}
