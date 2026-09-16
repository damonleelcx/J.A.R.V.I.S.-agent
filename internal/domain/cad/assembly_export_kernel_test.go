package cad_test

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A STEP export is an XDE assembly: one solid B-rep per definition, and one
// located, named instance per occurrence, assembled in time that grows with the
// part count rather than its square. Phase 4, stage K2 of
// docs/plan-2026-09-13-millions-of-parts.md; measured in
// docs/spikes/2026-09-14-xde-assembly-export.

// studPanel is rows×8 occurrences of one 4×6×8 mm stud.
func studPanel(rows int) geometry.Document {
	stud := geometry.Part{ID: "stud", Name: "Stud", Shape: "box", Size: map[string]float64{"width": 4, "height": 6, "depth": 8},
		Repeat: &geometry.Repeat{Count: rows, Offset: []float64{10, 0, 0}}}
	return geometry.Document{Name: "panel", Units: "mm", Root: "panel", Definitions: []geometry.Part{stud},
		Assemblies: []geometry.Assembly{{ID: "panel", Children: []geometry.Child{
			{ID: "row", Ref: "stud", Pattern: &geometry.Pattern{Kind: "linear", Count: 8, Offset: []float64{0, 0, 20}}},
		}}}}
}

var (
	stepBreps     = regexp.MustCompile(`=\s*MANIFOLD_SOLID_BREP\(`)
	stepInstances = regexp.MustCompile(`=\s*NEXT_ASSEMBLY_USAGE_OCCURRENCE\(`)
)

// The plan's acceptance, at S0's build ceiling: many occurrences export as one
// definition and N instances, and assembling and writing them grows linearly.
//
// The time fence reads the kernel's own phase times, not the whole build: the
// interference check is fenced on its own, by the box tests it counts
// (TestKernel_InterferenceBoxTestsGrowLinearly, stage K2b), and timing the whole
// build would fence the wrong step. Before K2, build123d's Compound(children=...)
// took 2.5 s to assemble 4,096 copies and 0.06 s for 512 (quadratic: 8× the parts,
// ~40× the time); an XDE assembly took 0.11 s for 4,096.
//
// ‼️ This fence was green on a writer that is quadratic past it. The STEP writer's
// validation-property walk costs ~N² in the occurrences under one assembly and is
// lost in noise below ~10k (measured: docs/spikes/2026-09-15-step-export-scaling).
// TestKernel_ExportTimeGrowsLinearlyPastTheBuildCeiling covers 4,096 → 65,536.
func TestKernel_ExportingManyOccurrencesGrowsLinearly(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	export := func(rows int) (*cad.Build, time.Duration) {
		t.Helper()
		got, err := k.BuildDocument(ctx, studPanel(rows), geometry.Millimetre, "step")
		if err != nil {
			t.Fatal(err)
		}
		want := rows * 8
		body := string(got.STEP)
		if got.Parts != want {
			t.Fatalf("%d parts, want %d", got.Parts, want)
		}
		// One definition written once, placed N times. A file with N B-reps would be
		// N copies of the same solid: correct to look at, and N times the size.
		if n := len(stepBreps.FindAllStringIndex(body, -1)); n != 1 {
			t.Errorf("%d occurrences: %d solid B-reps in the file, want 1 shared definition", want, n)
		}
		if n := len(stepInstances.FindAllStringIndex(body, -1)); n != want {
			t.Errorf("%d occurrences: %d instances in the file, want one each", want, n)
		}
		if v := float64(want) * 4 * 6 * 8; math.Abs(got.Volume-v) > 1e-6*v {
			t.Errorf("%d occurrences: volume %.6f mm³, want %.0f", want, got.Volume, v)
		}
		// Reported at all: a build whose phases read zero would pass every
		// timing below without measuring anything.
		if got.Phases.Assembly <= 0 || got.Phases.Export <= 0 {
			t.Fatalf("%d occurrences: phases not reported (%+v)", want, got.Phases)
		}
		return got, got.Phases.Assembly + got.Phases.Export
	}

	small, smallCost := export(64)  // 512 occurrences
	large, largeCost := export(512) // 4,096 occurrences
	t.Logf("512 occurrences: assembly %v, export %v; 4,096: assembly %v, export %v, interferences %v",
		small.Phases.Assembly, small.Phases.Export, large.Phases.Assembly, large.Phases.Export, large.Phases.Interferences)

	// A floor under the small build, so timer noise on a few milliseconds cannot
	// make the ratio.
	const floor = 50 * time.Millisecond
	base := max(smallCost, floor)
	if ratio := float64(largeCost) / float64(base); ratio > 16 {
		t.Errorf("8× the occurrences took %.1f× as long to assemble and export (%v → %v); "+
			"linear is ~8×, and the quadratic Compound(children=...) path measured ~40×", ratio, smallCost, largeCost)
	}
	if largeCost > 2*time.Second {
		t.Errorf("4,096 occurrences took %v to assemble and export; measured ~0.4 s as an XDE "+
			"assembly and ~2.9 s through Compound(children=...)", largeCost)
	}
}

// stepContents is what testdata/step_reimport.py reads back from a STEP file.
type stepContents struct {
	Components int        `json:"components"`
	Shapes     int        `json:"shapes"`
	Names      []string   `json:"names"`
	Bounds     [6]float64 `json:"bounds"`
}

// reimport reads a STEP file back with the kernel's own Python, the way a CAD
// tool would open it.
func reimport(t *testing.T, step []byte) stepContents {
	t.Helper()
	path := filepath.Join(t.TempDir(), "export.step")
	if err := os.WriteFile(path, step, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(os.Getenv("FORGE_CAD_PYTHON"), filepath.Join("testdata", "step_reimport.py"), path).Output()
	if err != nil {
		t.Fatalf("reading the STEP file back: %v", err)
	}
	var got stepContents
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("reading the STEP file back: %v (%s)", err, out)
	}
	return got
}

// The file puts every part where the build put it. The build's own volume and
// bounds are measured on the solids it BUILT, so they stay right even when the
// FILE is wrong. Measured 2026-09-14 while writing K2: a component written
// without its location collapsed every part onto the origin, and a shared shape
// written with its own location left on turned every cylinder back onto +Z, and
// both passed every check on the build's numbers. Only reading the file back
// sees it.
//
// The rack has cylinders (built along +Z and turned onto +Y inside the kernel,
// so the shared shape has an orientation of its own), mirrored ells and a turned
// child. The plates have a cut on one copy, which makes that copy a definition of
// its own while its siblings still share one.
func TestKernel_AnExportedFilePlacesEveryPartWhereTheBuildDid(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ell := geometry.Part{ID: "ell", Name: "Ell", Shape: "extrusion", Size: map[string]float64{"depth": 5},
		Profile: []geometry.Point{{X: 0, Y: 0}, {X: 20, Y: 0}, {X: 20, Y: 5}, {X: 5, Y: 5}, {X: 5, Y: 15}, {X: 0, Y: 15}},
		Repeat:  &geometry.Repeat{Count: 3, Offset: []float64{0, 0, 30}}}
	pin := geometry.Part{ID: "pin", Name: "Pin", Shape: "cylinder", Size: map[string]float64{"radius": 2, "height": 30},
		Repeat: &geometry.Repeat{Count: 4, Offset: []float64{10, 0, 0}}}
	rack := geometry.Document{Name: "rack", Units: "mm", Root: "rack", Definitions: []geometry.Part{ell, pin},
		Assemblies: []geometry.Assembly{{ID: "rack", Children: []geometry.Child{
			{ID: "left", Ref: "ell", Position: []float64{-100, 0, 0}},
			{ID: "right", Ref: "ell", Position: []float64{100, 0, 0}, Mirror: "x"},
			{ID: "pins", Ref: "pin", Position: []float64{0, 50, 0}},
			{ID: "crossbar", Ref: "pin", Position: []float64{0, 0, 120}, Rotation: []float64{0, 0, 90}},
		}}}}

	plate := geometry.Part{ID: "plate", Name: "Plate", Shape: "box", Size: map[string]float64{"width": 60, "height": 10, "depth": 60},
		Repeat: &geometry.Repeat{Count: 3, Offset: []float64{100, 0, 0}}}
	bore := geometry.Part{ID: "bore", Shape: "cylinder", Size: map[string]float64{"radius": 5, "height": 40},
		Position: []float64{100, 0, 0}}
	plates := geometry.Document{Name: "plates", Units: "mm", Parts: []geometry.Part{plate, bore},
		Features: []geometry.Feature{{ID: "drill", Op: "cut", Of: "plate-2", With: []string{"bore"}}}}

	for _, c := range []struct {
		name   string
		doc    geometry.Document
		shapes int // distinct shapes the file should hold
	}{
		{"rack", rack, 3},     // ell, mirrored ell, pin
		{"plates", plates, 2}, // the plate its two uncut copies share, and the cut one
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := k.BuildDocument(ctx, c.doc, geometry.Millimetre, "step")
			if err != nil {
				t.Fatal(err)
			}
			if len(got.FeatureFailures) != 0 || len(got.Skipped) != 0 {
				t.Fatalf("the build was not clean: failures %v, skipped %v", got.FeatureFailures, got.Skipped)
			}
			file := reimport(t, got.STEP)
			if file.Components != got.Parts {
				t.Errorf("the file holds %d parts, the build %d", file.Components, got.Parts)
			}
			if file.Shapes != c.shapes {
				t.Errorf("the file holds %d distinct shapes, want %d", file.Shapes, c.shapes)
			}
			// Every instance carries its part's name. Not necessarily a unique one: a
			// tree labels each child's copies by the definition ("Ell 1" under both
			// the left and the right child), and the file keeps what the build named.
			if len(file.Names) != got.Parts {
				t.Errorf("%d names in the file for %d parts", len(file.Names), got.Parts)
			}
			for i, n := range file.Names {
				if n == "" {
					t.Errorf("part %d of the file has no name: %q", i+1, file.Names)
					break
				}
			}
			// 0.01 mm: the reader's box carries OCCT's tolerance gap; a part put in
			// the wrong place or turned the wrong way is millimetres off.
			for i := range got.Bounds {
				if math.Abs(file.Bounds[i]-got.Bounds[i]) > 0.01 {
					t.Fatalf("the file's extent %v is not the build's %v: a part was written somewhere it was not built",
						file.Bounds, got.Bounds)
				}
			}
		})
	}
}
