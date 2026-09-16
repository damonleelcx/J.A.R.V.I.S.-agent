package cad_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The shapes and assembly phases at scale (next scale walls): a located copy made
// without the B-rep copy build123d makes and discards, a Plane built once per
// distinct rotation, and the assembly's volume counted once per definition
// (sidecar.py, _located, _placement and _ASSEMBLY_VOLUME_PER_DEFINITION).
// docs/spikes/2026-09-15-next-scale-walls.

type placedCopies struct {
	Error        string                           `json:"error"`
	Checked      int                              `json:"checked"`
	Problems     []string                         `json:"problems"`
	ProblemCount int                              `json:"problem_count"`
	Counts       map[string]map[string]placeTally `json:"counts"`
}

type placeTally struct {
	BRepCopies      int `json:"brep_copies"`
	Planes          int `json:"planes"`
	VolumeIntegrals int `json:"volume_integrals"`
	Occurrences     int `json:"occurrences"`
	Parts           int `json:"parts"`
	ShapeBuilds     int `json:"shape_builds"`
	// Added 2026-09-15 (last hot spots): the two costs a placed occurrence still
	// paid after #113 — a copy.deepcopy of every attribute of the definition, and a
	// build123d Location built through __init__'s nine keyword arguments. Fallbacks
	// counts the attributes _located's per-definition plan did not recognize and
	// handed to copy.deepcopy anyway.
	Deepcopies int `json:"deepcopies"`
	Locations  int `json:"locations"`
	Fallbacks  int `json:"fallbacks"`
}

// testdataJSON runs a script under testdata against this package's sidecar and
// decodes the one JSON object it prints.
func testdataJSON(t *testing.T, script string, out any, args ...string) {
	t.Helper()
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	cmd := exec.Command(python, append([]string{filepath.Join("testdata", script), "sidecar.py"}, args...)...)
	body, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("%s: %v\n%s\n%s", script, err, exit.Stderr, body)
		}
		t.Fatalf("%s: %v", script, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("reading %s: %v\n%s", script, err, body)
	}
}

func placedCopiesOf(t *testing.T) placedCopies {
	t.Helper()
	var got placedCopies
	testdataJSON(t, "placed_copies.py", &got)
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	return got
}

// A copy placed the sidecar's way is the copy build123d's `location * shape` made:
// the same class and attributes, the same B-rep, a placement equal to the bit, the
// same bounds, mesh, part properties, interferences and STEP file — and the
// assembly's volume to 1e-9 of it, because OCCT integrates a moved solid in its
// moved coordinates.
//
// testdata/placed_copies.py: boxes, cylinders, cones, spheres, extrusions plain and
// mirrored, and revolves, each at quarter turns, -0.0 beside 0, and random turns,
// with a plate a feature changed beside a plate it did not.
func TestKernel_APlacedCopyIsTheCopyBuild123dMade(t *testing.T) {
	got := placedCopiesOf(t)
	t.Logf("%d solid(s) compared across the three formats, %d difference(s)", got.Checked, got.ProblemCount)
	// 7 kinds × 8 matrices + 2 plates, in three formats.
	if got.Checked < 3*58 {
		t.Fatalf("compared %d solids; the fixture has 58 in each of three formats", got.Checked)
	}
	for _, p := range got.Problems {
		t.Error(p)
	}
	if got.ProblemCount > len(got.Problems) {
		t.Errorf("and %d more difference(s)", got.ProblemCount-len(got.Problems))
	}
}

// The scaling fence, as counts: eight times the occurrences asks build123d for no
// more B-rep copies, no more Planes and no more volume integrals than one — where
// build123d's own path asks for one of each per occurrence. A count, not a time,
// so load on the machine cannot move it.
func TestKernel_PlacingMoreCopiesCopiesNoBRepsAndBuildsNoMorePlanes(t *testing.T) {
	got := placedCopiesOf(t)
	ref1, ref8 := got.Counts["reference"]["1"], got.Counts["reference"]["8"]
	one, eight := got.Counts["shipped"]["1"], got.Counts["shipped"]["8"]
	t.Logf("build123d's path: %+v at one copy, %+v at eight", ref1, ref8)
	t.Logf("the sidecar's:    %+v at one copy, %+v at eight", one, eight)
	if eight.Parts != ref8.Parts || eight.Parts <= 4*one.Parts || eight.ShapeBuilds != one.ShapeBuilds {
		t.Fatalf("the fixture did not grow: %d then %d parts, %d then %d shape builds",
			one.Parts, eight.Parts, one.ShapeBuilds, eight.ShapeBuilds)
	}
	// The fixture must see the cost it fences, or a count of zero proves nothing.
	if ref8.BRepCopies-ref1.BRepCopies < eight.Occurrences-one.Occurrences ||
		ref8.Planes-ref1.Planes < eight.Occurrences-one.Occurrences ||
		ref8.VolumeIntegrals-ref1.VolumeIntegrals < eight.Occurrences-one.Occurrences-8 {
		t.Fatalf("build123d's path did not pay per occurrence here (%+v → %+v); the fixture cannot see the fix", ref1, ref8)
	}
	if eight.BRepCopies != one.BRepCopies {
		t.Errorf("%d B-rep copies at %d occurrences, %d at %d: a located copy copies its B-rep again",
			eight.BRepCopies, eight.Occurrences, one.BRepCopies, one.Occurrences)
	}
	if eight.Planes != one.Planes {
		t.Errorf("%d Planes at %d occurrences, %d at %d: a placement builds a Plane per occurrence again",
			eight.Planes, eight.Occurrences, one.Planes, one.Occurrences)
	}
	if eight.VolumeIntegrals != one.VolumeIntegrals {
		t.Errorf("%d volume integrals at %d occurrences, %d at %d: the assembly integrates every copy again",
			eight.VolumeIntegrals, eight.Occurrences, one.VolumeIntegrals, one.Occurrences)
	}
	// Added 2026-09-15 (last hot spots). Profiled on the barrel, a placed occurrence
	// still paid a copy.deepcopy of every attribute of its definition (12 a copy at
	// 90k, and the worst of them rebuilt a Location from a transformation) and a
	// Location through __init__'s keyword parsing. Both are now per DEFINITION, so
	// eight times the occurrences asks for no more of either.
	t.Logf("deepcopies/Locations per run: build123d's %d/%d at one copy, %d/%d at eight; "+
		"the sidecar's %d/%d and %d/%d, with %d and %d unrecognized attribute(s)",
		ref1.Deepcopies, ref1.Locations, ref8.Deepcopies, ref8.Locations,
		one.Deepcopies, one.Locations, eight.Deepcopies, eight.Locations, one.Fallbacks, eight.Fallbacks)
	// The fixture must pay these per occurrence on build123d's path, or a flat count
	// on the sidecar's proves nothing.
	if ref8.Deepcopies-ref1.Deepcopies < eight.Occurrences-one.Occurrences ||
		ref8.Locations-ref1.Locations < eight.Occurrences-one.Occurrences {
		t.Fatalf("build123d's path did not deepcopy and build a Location per occurrence here (%+v → %+v); "+
			"the fixture cannot see the fix", ref1, ref8)
	}
	if eight.Deepcopies != one.Deepcopies {
		t.Errorf("%d deepcopies at %d occurrences, %d at %d: a placed copy deepcopies its definition again",
			eight.Deepcopies, eight.Occurrences, one.Deepcopies, one.Occurrences)
	}
	if eight.Locations != one.Locations {
		t.Errorf("%d Location.__init__ calls at %d occurrences, %d at %d: a placement parses keyword arguments again",
			eight.Locations, eight.Occurrences, one.Locations, one.Occurrences)
	}
	// ‼️ A fallback is CORRECT — it is the deepcopy loop, for an attribute the plan
	// does not recognize — but one per occurrence would mean the plan recognizes
	// nothing that matters, and the counts above would be flat for the wrong reason.
	if eight.Fallbacks > one.Fallbacks {
		t.Errorf("%d unrecognized attribute(s) at %d occurrences and %d at %d: _located's plan is not per definition",
			eight.Fallbacks, eight.Occurrences, one.Fallbacks, one.Occurrences)
	}
}
