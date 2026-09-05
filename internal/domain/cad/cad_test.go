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
