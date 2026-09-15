package cad_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Interference at scale: a grid broad phase over all three axes, boxes and volumes
// read once per definition, and each clash measured once per pose. Phase 5, stage
// V1 of docs/plan-2026-09-13-millions-of-parts.md.

// studPlane is side × side copies of one 4×6×8 mm stud spread evenly over the XZ
// plane, 10 mm apart along x and 20 mm along z. No two studs touch.
func studPlane(side int) geometry.Document {
	stud := geometry.Part{ID: "stud", Name: "Stud", Shape: "box", Size: map[string]float64{"width": 4, "height": 6, "depth": 8},
		Repeat: &geometry.Repeat{Count: side, Offset: []float64{10, 0, 0}}}
	return geometry.Document{Name: "plane", Units: "mm", Root: "plane", Definitions: []geometry.Part{stud},
		Assemblies: []geometry.Assembly{{ID: "plane", Children: []geometry.Child{
			{ID: "row", Ref: "stud", Pattern: &geometry.Pattern{Kind: "linear", Count: side, Offset: []float64{0, 0, 20}}},
		}}}}
}

// Parts spread evenly over a plane cost a few box tests each.
//
// K2b's one-axis sweep met every column of this grid whole: C(64,2) × 64 =
// 129,024 tests at 4,096 studs, about 31 a part, which is the √n it was measured
// to cost. A grid does not care which way the parts spread.
func TestKernel_APlaneOfPartsCostsAFewBoxTestsEach(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	got, err := k.BuildDocument(ctx, studPlane(64), geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parts != 4096 || len(got.Interferences) != 0 || got.InterferencesTruncated {
		t.Fatalf("%d parts, %d interference(s), truncated=%v; want 4096 studs that touch nothing",
			got.Parts, len(got.Interferences), got.InterferencesTruncated)
	}
	if got.InterferenceBoxTests <= 0 {
		t.Fatal("the build did not report its box tests")
	}
	t.Logf("4,096 studs over a plane: %d box tests (%.1f a part), interference phase %v",
		got.InterferenceBoxTests, float64(got.InterferenceBoxTests)/4096, got.Phases.Interferences)
	if perPart := float64(got.InterferenceBoxTests) / 4096; perPart > 8 {
		t.Errorf("%.1f box tests a part over a plane; a grid is a few, the one-axis sweep was ~31", perPart)
	}
}

// cellsWithPins is rows × perRow copies of a 20×4×20 mm plate with two 2 mm pins
// standing through it, 40 mm apart each way. Every pin shares 4 mm of its 10 mm
// length with its plate: two clashes a copy, each 40% of the pin.
//
// Two nested patterns rather than one, because one pattern places at most 512
// copies (geometry/repeat.go) and a 1,200-copy pattern is refused — which is how
// the first version of this fixture built nothing at all.
func cellsWithPins(rows, perRow int) geometry.Document {
	plate := geometry.Part{ID: "plate", Name: "Plate", Shape: "box", Size: map[string]float64{"width": 20, "height": 4, "depth": 20}}
	pin := geometry.Part{ID: "pin", Name: "Pin", Shape: "cylinder", Size: map[string]float64{"radius": 2, "height": 10}}
	return geometry.Document{Name: "pinned", Units: "mm", Root: "rack",
		Definitions: []geometry.Part{plate, pin},
		Assemblies: []geometry.Assembly{
			{ID: "cell", Children: []geometry.Child{
				{ID: "plate", Ref: "plate"},
				{ID: "left", Ref: "pin", Position: []float64{-5, 0, 0}},
				{ID: "right", Ref: "pin", Position: []float64{5, 0, 0}},
			}},
			{ID: "row", Children: []geometry.Child{
				{ID: "cells", Ref: "cell", Pattern: &geometry.Pattern{Kind: "linear", Count: perRow, Offset: []float64{40, 0, 0}}},
			}},
			{ID: "rack", Children: []geometry.Child{
				{ID: "rows", Ref: "row", Pattern: &geometry.Pattern{Kind: "linear", Count: rows, Offset: []float64{0, 0, 40}}},
			}},
		}}
}

// The plan's acceptance, at the build ceiling: a repetitive assembly is checked in
// FULL. 1,200 copies are 2,400 clashes, more than the 2,000 booleans the budget
// allows — before V1 this came back truncated. Two poses (a pin left of centre,
// a pin right of it) are two booleans; every other clash is the same answer again.
func TestKernel_RepeatedClashesPayForOneBooleanEachPose(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	got, err := k.BuildDocument(ctx, cellsWithPins(30, 40), geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("3,600 parts: %d pair(s), %d boolean(s) paid for, %d reused, %d interference(s), truncated=%v, phase %v",
		got.InterferencePairs, got.InterferenceBooleans, got.InterferenceReused, len(got.Interferences),
		got.InterferencesTruncated, got.Phases.Interferences)
	if got.InterferencesTruncated {
		t.Fatalf("the check stopped at the budget with %d boolean(s) paid for; a repeated clash should be measured once",
			got.InterferenceBooleans)
	}
	if len(got.Interferences) != 2400 || got.InterferencePairs != 2400 {
		t.Fatalf("%d interference(s) from %d pair(s); want every pin in every plate, 2,400", len(got.Interferences), got.InterferencePairs)
	}
	if got.InterferenceBooleans != 2 || got.InterferenceReused != 2398 {
		t.Errorf("%d boolean(s) paid for and %d reused; two poses are two booleans, and 2,398 answers again",
			got.InterferenceBooleans, got.InterferenceReused)
	}
	c := got.Interferences[0]
	if want := math.Pi * 4 * 4; math.Abs(c.Volume-want) > 0.01*want || math.Abs(c.Fraction-0.4) > 0.005 {
		t.Errorf("a pin shares %.2f mm³ (%.3f of it); want %.2f mm³ (0.4)", c.Volume, c.Fraction, want)
	}
}

// cacheComparison is what testdata/interference_cache.py reports.
type cacheComparison struct {
	Uncached cacheRun `json:"uncached"`
	Cached   cacheRun `json:"cached"`
	Error    string   `json:"error"`
}

type cacheRun struct {
	Found     []geometry.Interference `json:"found"`
	Truncated bool                    `json:"truncated"`
	Pairs     int                     `json:"pairs"`
	Booleans  int                     `json:"booleans"`
	Reused    int                     `json:"reused"`
}

// A reused clash is the clash measured again.
//
// The same fixture through _build with _INTERFERENCE_CACHE off and on. Pins stand
// in plates at several turns — some turns repeated, so answers are reused, and
// some not, so a pose that ignored its rotation would reuse a wrong volume — beside
// a mirrored L through a plate, and a plate cut by a drill, which is changed and
// must be measured on its own.
func TestKernel_AReusedClashIsTheClashMeasuredAgain(t *testing.T) {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	out, err := exec.Command(python, filepath.Join("testdata", "interference_cache.py"), "sidecar.py").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("comparing cached and uncached clashes: %v\n%s", err, exit.Stderr)
		}
		t.Fatalf("comparing cached and uncached clashes: %v", err)
	}
	var got cacheComparison
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("reading the comparison: %v\n%s", err, out)
	}
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	u, c := got.Uncached, got.Cached
	t.Logf("uncached: %d pairs, %d booleans; cached: %d booleans, %d reused; %d interference(s)",
		u.Pairs, u.Booleans, c.Booleans, c.Reused, len(u.Found))
	if u.Truncated || c.Truncated || u.Pairs != c.Pairs {
		t.Fatalf("the runs are not comparable: pairs %d and %d, truncated %v and %v", u.Pairs, c.Pairs, u.Truncated, c.Truncated)
	}
	if c.Reused == 0 || c.Booleans+c.Reused != u.Booleans {
		t.Errorf("cached run paid %d boolean(s) and reused %d for %d pair(s); the fixture must reuse answers",
			c.Booleans, c.Reused, u.Booleans)
	}
	if len(u.Found) < 8 {
		t.Fatalf("the fixture has %d interference(s); it needs enough to disagree about", len(u.Found))
	}
	if len(c.Found) != len(u.Found) {
		t.Fatalf("cached found %d interference(s), uncached %d\ncached: %+v\nuncached: %+v", len(c.Found), len(u.Found), c.Found, u.Found)
	}
	// Matched by the pair, not by position. The list is sorted worst first, and
	// clashes of one pose tie: measured separately, their volumes differ in the
	// thirteenth digit and sort one way; reused, they are equal and sort another.
	// The first version compared positions and failed on exactly that.
	cached := map[[2]string]geometry.Interference{}
	for _, f := range c.Found {
		cached[[2]string{f.A, f.B}] = f
	}
	for _, want := range u.Found {
		have, ok := cached[[2]string{want.A, want.B}]
		if !ok {
			t.Errorf("uncached found %s in %s, and the cached run did not", want.A, want.B)
			continue
		}
		if math.Abs(have.Volume-want.Volume) > 1e-6*math.Max(1, want.Volume) ||
			math.Abs(have.Fraction-want.Fraction) > 1e-9 {
			t.Errorf("%s in %s: cached %+v, uncached %+v", want.A, want.B, have, want)
		}
	}
	for i := 1; i < len(c.Found); i++ {
		if c.Found[i].Fraction > c.Found[i-1].Fraction {
			t.Errorf("the cached list is not worst first at %d: %v after %v", i, c.Found[i].Fraction, c.Found[i-1].Fraction)
		}
	}
}
