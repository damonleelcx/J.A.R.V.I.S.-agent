package cad_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The interference check's large boxes: indexed by a level per axis instead of
// tested against every box, and a clash inside a box reused wherever it slides
// (sidecar.py, _candidate_pairs and _INTERFERENCE_SLIDE). The first wall at
// 1,000,000 occurrences: docs/spikes/2026-09-15-one-million-occurrences, and
// docs/spikes/2026-09-15-large-box-index for what this changed.

// deck is `lanes` lanes side by side along z. Each lane is `bays` bays along x
// and ONE rail the whole lane long; each bay is a 96 mm panel and 16 studs under
// it. Nothing touches.
//
// The studs are most of the parts, so the grid's cell is a stud (8 mm) and every
// panel (96 mm) and every rail (100 mm a bay) is a large box: 4 × bays + 4 of them,
// growing with the model the way an airframe's panels, frames and stringers do.
// Testing each against every box is ~52 × 820 ≈ 41,000 tests at 12 bays and
// ~196 × 3,268 ≈ 620,000 at 48 — quadratic. The index measured 648 and 2,604.
func deck(bays int) geometry.Document {
	const lanes = 4
	stud := geometry.Part{ID: "stud", Name: "Stud", Shape: "box", Size: map[string]float64{"width": 4, "height": 6, "depth": 8}}
	panel := geometry.Part{ID: "panel", Name: "Panel", Shape: "box", Size: map[string]float64{"width": 96, "height": 2, "depth": 36}}
	rail := geometry.Part{ID: "rail", Name: "Rail", Shape: "box",
		Size: map[string]float64{"width": 100 * float64(bays), "height": 4, "depth": 4}}
	return geometry.Document{Name: "deck", Units: "mm", Root: "deck",
		Definitions: []geometry.Part{stud, panel, rail},
		Assemblies: []geometry.Assembly{
			{ID: "bay", Children: []geometry.Child{
				{ID: "panel", Ref: "panel", Position: []float64{50, 10, 0}},
				{ID: "studs", Ref: "stud", Position: []float64{10, 0, -10}, Pattern: &geometry.Pattern{Kind: "grid",
					Rows: 2, Columns: 8, RowOffset: []float64{0, 0, 20}, ColumnOffset: []float64{10, 0, 0}}},
			}},
			{ID: "lane", Children: []geometry.Child{
				{ID: "bays", Ref: "bay", Pattern: &geometry.Pattern{Kind: "linear", Count: bays, Offset: []float64{100, 0, 0}}},
				{ID: "rail", Ref: "rail", Position: []float64{50 * float64(bays), -10, 0}},
			}},
			{ID: "deck", Children: []geometry.Child{
				{ID: "lanes", Ref: "lane", Pattern: &geometry.Pattern{Kind: "linear", Count: lanes, Offset: []float64{0, 0, 50}}},
			}},
		}}
}

// The large-box index's acceptance: when the long parts grow with the model, the
// box tests grow with the model, not with its square.
//
// 4× the bays is 4× the parts AND 4× the large boxes, so testing each large box
// against every box is ~15× the tests; an index is ~4×.
func TestKernel_BoxTestsGrowLinearlyWhenTheLongPartsGrowWithTheModel(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	count := func(bays int) (int, int) {
		t.Helper()
		got, err := k.BuildDocument(ctx, deck(bays), geometry.Millimetre, "")
		if err != nil {
			t.Fatal(err)
		}
		if want := 4 * (17*bays + 1); got.Parts != want {
			t.Fatalf("%d parts, want %d", got.Parts, want)
		}
		if len(got.Interferences) != 0 || got.InterferencesTruncated {
			t.Fatalf("a deck where nothing touches reported %d interference(s), truncated=%v: %+v",
				len(got.Interferences), got.InterferencesTruncated, got.Interferences)
		}
		// Reported at all: a count that reads zero passes every bound below.
		if got.InterferenceBoxTests <= 0 {
			t.Fatalf("%d parts: the build did not report its box tests", got.Parts)
		}
		t.Logf("%d bays, %d parts (%d large boxes): %d box tests (%.1f a part), interference phase %v",
			bays, got.Parts, 4*bays+4, got.InterferenceBoxTests,
			float64(got.InterferenceBoxTests)/float64(got.Parts), got.Phases.Interferences)
		return got.Parts, got.InterferenceBoxTests
	}

	smallParts, small := count(12)
	largeParts, large := count(48)
	if ratio := float64(large) / float64(small); ratio > 6 {
		t.Errorf("4× the bays made %.1f× the box tests (%d → %d); an index is ~4×, "+
			"large boxes tested against every box ~15×", ratio, small, large)
	}
	// Measured 0.8. One level for all three axes (a cubic cell as long as a rail)
	// puts every stud in a cell with every rail, ~5.6 a part here and 80 a rivet
	// in an airframe barrel, and grows no faster than the model, so only a bound
	// per part sees it.
	if perPart := float64(large) / float64(largeParts); perPart > 2 {
		t.Errorf("%.1f box tests a part at %d parts; the index measured 0.8, and testing the 196 large boxes "+
			"against every box is ~190", perPart, largeParts)
	}
	_ = smallParts
}

// The large-box index finds exactly the pairs comparing every box finds.
//
// testdata/interference_all_pairs.py, fixture "long": among 120 studs, rails the
// envelope long on each axis, rails turned in a plane, panels long on two axes,
// a stringer with rivets along it and one over its end, large boxes face to face,
// and a box around everything — every combination of levels meets. The same
// comparison runs on three box-only sets of 1,500 boxes whose sizes spread over
// four decades on each axis independently.
func TestKernel_LongBoxesAreFoundAsEveryPairFindsThem(t *testing.T) {
	got := compareWithEveryPair(t, "long")
	if got.Reference.Truncated {
		t.Fatalf("the fixture hit the pair budget (%d), so it compares truncated answers", got.Budget)
	}
	if len(got.Reference.Found) < 20 {
		t.Fatalf("the fixture has only %d interference(s); it needs enough to disagree about", len(got.Reference.Found))
	}
	if got.BoxTests >= got.EveryPair {
		t.Errorf("the broad phase made %d box tests, and comparing every pair is %d", got.BoxTests, got.EveryPair)
	}
	if len(got.Synthetic) != 3 {
		t.Fatalf("%d box-only comparison(s), want 3", len(got.Synthetic))
	}
	for i, c := range got.Synthetic {
		t.Logf("box-only set %d: %d pairs, %d box tests (every pair: %d)", i, c.Pairs, c.BoxTests, c.EveryPair)
		samePairs(t, fmt.Sprintf("box-only set %d", i), c)
		if c.BoxTests*4 > c.EveryPair {
			t.Errorf("box-only set %d: %d box tests, more than a quarter of every pair (%d)", i, c.BoxTests, c.EveryPair)
		}
	}
}

// railWithPins is one 4 m rail with 191 pins through it 20 mm apart, and a pin
// standing on each end of it, half over the edge. A pin shares 20 of its 30 mm
// with the rail; an end pin half of that.
//
// Beside it, 500 mm away, a second 4 m rail with 95 crossbars through it 40 mm
// apart and one on its end, half over. A crossbar (10 × 30 × 60) is taller and
// deeper than the rail: the rail lies inside the crossbar's height and depth, and
// the crossbar inside the rail's length. Seen from the crossbar two translations
// are free and from the rail one, so the crossbar's frame is the key — and the
// crossbar's position along the rail is only free there because the rail lies
// along the crossbar's width (carried). A crossbar shares 10 × 20 × 20 mm; the end
// one half of that.
//
// And a third rail, 1 m away, with 95 pins leaning 35° along it. A leaning pin is
// inside the rail's length and depth, but no axis of the pin lies along the rail,
// so nothing can be carried into the pin's frame: only the rail's frame sees the
// pins are one clash.
func railWithPins() geometry.Document {
	rail := geometry.Part{ID: "rail", Name: "Rail", Shape: "box", Size: map[string]float64{"width": 4000, "height": 20, "depth": 20}}
	pin := geometry.Part{ID: "pin", Name: "Pin", Shape: "cylinder", Size: map[string]float64{"radius": 2, "height": 30}}
	bar := geometry.Part{ID: "crossbar", Name: "Crossbar", Shape: "box", Size: map[string]float64{"width": 10, "height": 30, "depth": 60}}
	// The assembly is not called "rail": an assembly and a definition sharing an id
	// is a document with no part to build, which is how this fence's first run failed.
	return geometry.Document{Name: "track", Units: "mm", Root: "track",
		Definitions: []geometry.Part{rail, pin, bar},
		Assemblies: []geometry.Assembly{{ID: "track", Children: []geometry.Child{
			{ID: "rail", Ref: "rail"},
			{ID: "pins", Ref: "pin", Position: []float64{-1900, 0, 0},
				Pattern: &geometry.Pattern{Kind: "linear", Count: 191, Offset: []float64{20, 0, 0}}},
			{ID: "left-end", Ref: "pin", Position: []float64{-2000, 0, 0}},
			{ID: "right-end", Ref: "pin", Position: []float64{2000, 0, 0}},
			{ID: "beam", Ref: "rail", Position: []float64{0, 0, 500}},
			{ID: "bars", Ref: "crossbar", Position: []float64{-1900, 0, 500},
				Pattern: &geometry.Pattern{Kind: "linear", Count: 95, Offset: []float64{40, 0, 0}}},
			{ID: "end-bar", Ref: "crossbar", Position: []float64{2000, 0, 500}},
			{ID: "girder", Ref: "rail", Position: []float64{0, 0, 1000}},
			{ID: "leaning", Ref: "pin", Position: []float64{-1880, 0, 1000}, Rotation: []float64{0, 0, 35},
				Pattern: &geometry.Pattern{Kind: "linear", Count: 95, Offset: []float64{40, 0, 0}}},
		}}}}
}

// A row of pins along a rail is a different pose per pin, and the same clash: a
// pin wholly inside the rail's length shares the same volume wherever it sits
// along it. The pins over the ends are not inside, and are measured.
//
// Before this, 193 poses were 193 booleans — how an airframe barrel's rivets along
// its stringers truncated the check from ~20 bays.
func TestKernel_PinsAlongARailPayForOneBooleanAndTheEndsAreMeasured(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	got, err := k.BuildDocument(ctx, railWithPins(), geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d parts: %d pair(s), %d boolean(s) paid for, %d reused, %d interference(s), truncated=%v",
		got.Parts, got.InterferencePairs, got.InterferenceBooleans, got.InterferenceReused,
		len(got.Interferences), got.InterferencesTruncated)
	if got.InterferencesTruncated || len(got.Interferences) != 384 || got.InterferencePairs != 384 {
		t.Fatalf("%d interference(s) from %d pair(s), truncated=%v; want every pin and crossbar in its rail, 384",
			len(got.Interferences), got.InterferencePairs, got.InterferencesTruncated)
	}
	if got.InterferenceBooleans > 6 || got.InterferenceBooleans+got.InterferenceReused != 384 {
		t.Errorf("%d boolean(s) paid for and %d reused; the straight pins inside a rail are one boolean, the "+
			"leaning pins one, the crossbars one, and each end one more", got.InterferenceBooleans, got.InterferenceReused)
	}
	var leaning []float64
	for _, c := range got.Interferences {
		if c.B == "girder" || c.A == "girder" {
			leaning = append(leaning, c.Volume)
		}
	}
	if len(leaning) != 95 {
		t.Fatalf("found %d of the 95 leaning pins in the girder", len(leaning))
	}
	inside, half := math.Pi*4*20, math.Pi*4*10
	barInside, barHalf := 4000.0, 2000.0
	ends := 0
	for _, c := range got.Interferences {
		if c.A == "end-bar" || c.B == "end-bar" {
			ends++
			if math.Abs(c.Volume-barHalf) > 0.01*barHalf {
				t.Errorf("the end crossbar shares %.2f mm³ with its rail; half over the edge it is %.2f", c.Volume, barHalf)
			}
			continue
		}
		if c.A == "girder" || c.B == "girder" {
			if math.Abs(c.Volume-leaning[0]) > 1e-6*leaning[0] {
				t.Errorf("%s in %s shares %.4f mm³, and the first leaning pin %.4f; every one stands the same "+
					"way inside the girder", c.A, c.B, c.Volume, leaning[0])
			}
			continue
		}
		if c.A == "beam" || c.B == "beam" {
			if math.Abs(c.Volume-barInside) > 0.01*barInside {
				t.Errorf("%s in %s shares %.2f mm³, want %.2f", c.A, c.B, c.Volume, barInside)
			}
			continue
		}
		if c.A == "left-end" || c.A == "right-end" {
			ends++
			if math.Abs(c.Volume-half) > 0.01*half {
				t.Errorf("%s shares %.2f mm³ with the rail; half over the edge it is %.2f — "+
					"a volume reused from a pin inside the rail is %.2f", c.A, c.Volume, half, inside)
			}
			continue
		}
		if math.Abs(c.Volume-inside) > 0.01*inside {
			t.Errorf("%s shares %.2f mm³ with the rail, want %.2f", c.A, c.Volume, inside)
		}
	}
	if ends != 3 {
		t.Errorf("found %d of the 2 end pins and the end crossbar", ends)
	}
}
