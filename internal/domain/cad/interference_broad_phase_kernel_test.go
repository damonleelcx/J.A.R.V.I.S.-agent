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

// The interference check's broad phase tests only boxes that could overlap,
// instead of comparing every pair. Phase 4, stage K2b of
// docs/plan-2026-09-13-millions-of-parts.md, as a sweep; since Phase 5, stage V1,
// a grid over all three axes (sidecar.py, _candidate_pairs), which these fences
// hold to the same bounds.
//
// Its cost is fenced by the box tests the kernel COUNTS, not by a timer: the
// count is the same on a laptop and a loaded CI runner, and it separates a broad
// phase (a handful per part) from every pair (n(n-1)/2) by orders of magnitude
// where a time only separates them by a noisy ratio.

// studGrid is rows copies of one 4×6×8 mm stud stepping 10 mm along x (or z),
// patterned 8 times 20 mm apart across that. No two studs touch.
func studGrid(rows int, along string) geometry.Document {
	step, across := []float64{10, 0, 0}, []float64{0, 0, 20}
	if along == "z" {
		step, across = []float64{0, 0, 10}, []float64{20, 0, 0}
	}
	stud := geometry.Part{ID: "stud", Name: "Stud", Shape: "box", Size: map[string]float64{"width": 4, "height": 6, "depth": 8},
		Repeat: &geometry.Repeat{Count: rows, Offset: step}}
	return geometry.Document{Name: "grid", Units: "mm", Root: "grid", Definitions: []geometry.Part{stud},
		Assemblies: []geometry.Assembly{{ID: "grid", Children: []geometry.Child{
			{ID: "row", Ref: "stud", Pattern: &geometry.Pattern{Kind: "linear", Count: 8, Offset: across}},
		}}}}
}

// The plan's acceptance: the number of pairs compared is counted and grows about
// linearly on a spread grid.
//
// Each column of 8 studs costs 0+1+…+7 = 28 box tests and closes before the next
// one opens, so the sweep is 28 per row. Comparing every pair is 130,816 tests at
// 512 parts and 8,386,560 at 4,096.
func TestKernel_InterferenceBoxTestsGrowLinearly(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	count := func(rows int, along string) int {
		t.Helper()
		got, err := k.BuildDocument(ctx, studGrid(rows, along), geometry.Millimetre, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Parts != rows*8 {
			t.Fatalf("%d parts, want %d", got.Parts, rows*8)
		}
		if len(got.Interferences) != 0 || got.InterferencesTruncated {
			t.Fatalf("studs 2 mm apart were reported as interfering (truncated=%v): %+v",
				got.InterferencesTruncated, got.Interferences)
		}
		// Reported at all: a count that reads zero passes every bound below
		// without measuring anything.
		if got.InterferenceBoxTests <= 0 {
			t.Fatalf("%d parts: the build did not report its box tests", got.Parts)
		}
		t.Logf("%d parts along %s: %d box tests, interference phase %v",
			got.Parts, along, got.InterferenceBoxTests, got.Phases.Interferences)
		return got.InterferenceBoxTests
	}

	small, large := count(64, "x"), count(512, "x")
	if ratio := float64(large) / float64(small); ratio > 16 {
		t.Errorf("8× the parts made %.1f× the box tests (%d → %d); a sweep is ~8×, every pair ~64×",
			ratio, small, large)
	}
	const ceiling = 8 * 4096
	if large > ceiling {
		t.Errorf("4,096 parts made %d box tests, want at most %d; every pair is 8,386,560", large, ceiling)
	}
	// The same grid standing along z. A sweep that always runs along x meets each
	// of its 8 columns whole, 512 parts at once.
	if tall := count(512, "z"); tall > ceiling {
		t.Errorf("4,096 parts spread along z made %d box tests, want at most %d: "+
			"the sweep is not running along the axis the parts spread", tall, ceiling)
	}
}

// allPairs is what testdata/interference_all_pairs.py reports: the sidecar's
// check and the every-pair loop it replaced, on the same solids.
type allPairs struct {
	Parts     int              `json:"parts"`
	Budget    int              `json:"budget"`
	BoxTests  int              `json:"box_tests"`
	EveryPair int              `json:"every_pair"`
	Sweep     interferenceList `json:"sweep"`
	Reference interferenceList `json:"all_pairs"`
	// Candidates is _candidate_pairs against every pair of the same boxes, and
	// Synthetic the same on box-only sets (the "long" fixture only).
	Candidates candidateCheck   `json:"candidates"`
	Synthetic  []candidateCheck `json:"synthetic"`
}

type candidateCheck struct {
	Pairs          int  `json:"pairs"`
	EveryPairPairs int  `json:"every_pair_pairs"`
	Match          bool `json:"match"`
	Duplicates     int  `json:"duplicates"`
	BoxTests       int  `json:"box_tests"`
	EveryPair      int  `json:"every_pair"`
}

// samePairs fails unless the broad phase returned exactly the pairs comparing
// every box would — the same pairs, in index order, each once.
func samePairs(t *testing.T, what string, c candidateCheck) {
	t.Helper()
	if !c.Match || c.Duplicates != 0 || c.Pairs != c.EveryPairPairs {
		t.Errorf("%s: the broad phase returned %d pair(s) (%d duplicated) and every pair %d; match=%v",
			what, c.Pairs, c.Duplicates, c.EveryPairPairs, c.Match)
	}
	if c.EveryPairPairs == 0 {
		t.Errorf("%s: no box overlaps any other, so the comparison proves nothing", what)
	}
}

type interferenceList struct {
	Found     []geometry.Interference `json:"found"`
	Truncated bool                    `json:"truncated"`
}

func compareWithEveryPair(t *testing.T, fixture string) allPairs {
	t.Helper()
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	out, err := exec.Command(python, filepath.Join("testdata", "interference_all_pairs.py"), "sidecar.py", fixture).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("comparing with every pair: %v\n%s", err, exit.Stderr)
		}
		t.Fatalf("comparing with every pair: %v", err)
	}
	var got allPairs
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("reading the comparison: %v\n%s", err, out)
	}
	t.Logf("%s: %d parts, %d box tests (every pair: %d), %d interference(s), truncated=%v at a budget of %d",
		fixture, got.Parts, got.BoxTests, got.EveryPair, len(got.Reference.Found), got.Reference.Truncated, got.Budget)

	samePairs(t, fixture, got.Candidates)
	if got.Sweep.Truncated != got.Reference.Truncated {
		t.Errorf("truncated: sweep %v, every pair %v", got.Sweep.Truncated, got.Reference.Truncated)
	}
	if len(got.Sweep.Found) != len(got.Reference.Found) {
		t.Fatalf("the sweep found %d interference(s) and every pair %d\nsweep: %+v\nevery pair: %+v",
			len(got.Sweep.Found), len(got.Reference.Found), got.Sweep.Found, got.Reference.Found)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }
	for i, want := range got.Reference.Found {
		have := got.Sweep.Found[i]
		if have.A != want.A || have.B != want.B || have.ALabel != want.ALabel || have.BLabel != want.BLabel ||
			!near(have.Volume, want.Volume) || !near(have.Fraction, want.Fraction) {
			t.Errorf("interference %d: sweep %+v, every pair %+v", i, have, want)
		}
	}
	return got
}

// The plan's acceptance: the interference list is identical to comparing every
// pair. The fixture is scattered along a long envelope with a rail running most
// of its length, face-to-face contacts (a miss by _boxes_miss's rule), identical
// and nested boxes, turned cylinders, a solid whose bounds cannot be read, and
// parts listed in shuffled order so index order and sweep order disagree.
func TestKernel_TheBroadPhaseFindsWhatEveryPairFinds(t *testing.T) {
	got := compareWithEveryPair(t, "scatter")
	if got.Reference.Truncated {
		t.Fatalf("the fixture hit the pair budget (%d), so it compares truncated answers, not whole ones", got.Budget)
	}
	// Something to compare: two empty lists are identical and prove nothing.
	if len(got.Reference.Found) < 10 {
		t.Fatalf("the fixture has only %d interference(s); it needs enough to disagree about", len(got.Reference.Found))
	}
	if got.BoxTests >= got.EveryPair {
		t.Errorf("the sweep made %d box tests, and comparing every pair is %d", got.BoxTests, got.EveryPair)
	}
}

// A dense model stops at the pair budget. Which pairs were measured before it
// stopped depends on the order they are taken in, so the sweep takes them in
// the order comparing every pair did, and a truncated answer is unchanged.
func TestKernel_ATruncatedBroadPhaseStopsWhereEveryPairStops(t *testing.T) {
	got := compareWithEveryPair(t, "dense")
	if !got.Reference.Truncated {
		t.Fatalf("the dense fixture did not reach the pair budget of %d, so it tests nothing about truncation", got.Budget)
	}
}
