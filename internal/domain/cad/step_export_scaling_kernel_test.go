package cad_test

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A STEP export stays linear past the 4,096-part build ceiling, and the file it
// writes there is the one the writer wrote before the fix. Measured in
// docs/spikes/2026-09-15-step-export-scaling: STEPCAFControl_Writer's validation-
// property walk visits an assembly's children by index, and each lookup walks the
// child list, so the barrel's export took 0.6 s at 10k occurrences and 20 s at 90k.
//
// # Why these go through testdata/step_export_scaling.py
//
// cad.BuildDocument refuses more than 4,096 parts before the kernel is asked, and
// the walk is quadratic only in the occurrences under ONE assembly: K2's fence
// (TestKernel_ExportingManyOccurrencesGrowsLinearly, 512 → 4,096) was green on the
// quadratic writer. The script calls the sidecar's _build directly, like the K4
// mesh comparison, at 4,096 and 32,768 occurrences.

// stepScaling is what testdata/step_export_scaling.py reports.
type stepScaling struct {
	Small            int        `json:"small"`
	Large            int        `json:"large"`
	Parts            int        `json:"parts"`
	ReadBack         int        `json:"readback"`
	ReadBackParts    int        `json:"readback_parts"`
	ReadBackBounds   [6]float64 `json:"readback_bounds"`
	SmallExport      float64    `json:"small_export_s"`
	LargeExport      float64    `json:"large_export_s"`
	WalkedExport     float64    `json:"walked_export_s"`
	SmallTransfer    float64    `json:"small_transfer_s"`
	LargeTransfer    float64    `json:"large_transfer_s"`
	WalkedTransfer   float64    `json:"walked_transfer_s"`
	PropsModeShipped bool       `json:"props_mode_shipped"`
	Bytes            int        `json:"bytes"`
	SameFile         bool       `json:"same_file"`
	Instances        int        `json:"instances"`
	Breps            int        `json:"breps"`

	step []byte // the read-back file, as written
}

const (
	scalingSmall = 4096
	scalingLarge = 16 * 4096
	// ‼️ Smaller than scalingLarge on purpose: OCCT's STEP reader is quadratic in the
	// occurrences under one assembly. Measured 2026-09-15, reading the fixture back
	// took 5.5 s at 4,096, 19–27 s at 8,192, 67–86 s at 16,384, over 300 s at 32,768,
	// and did not finish in 18 minutes at 65,536. See the note in the script.
	scalingReadBack = 2 * 4096
)

var (
	scalingOnce sync.Once
	scalingRun  stepScaling
	scalingErr  string
)

// exportScaling runs the script once per test binary: both fences read the same
// run, which takes most of a minute.
func exportScaling(t *testing.T) stepScaling {
	t.Helper()
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	scalingOnce.Do(func() {
		dir, err := os.MkdirTemp("", "step-scaling-")
		if err != nil {
			scalingErr = err.Error()
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "readback.step")
		out, err := exec.Command(python, filepath.Join("testdata", "step_export_scaling.py"), "sidecar.py",
			itoa(scalingSmall), itoa(scalingLarge), itoa(scalingReadBack), path).Output()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				scalingErr = err.Error() + "\n" + string(exit.Stderr)
			} else {
				scalingErr = err.Error()
			}
			return
		}
		if err := json.Unmarshal(out, &scalingRun); err != nil {
			scalingErr = "reading the script's report: " + err.Error() + "\n" + string(out)
			return
		}
		if scalingRun.step, err = os.ReadFile(path); err != nil {
			scalingErr = err.Error()
		}
	})
	if scalingErr != "" {
		t.Fatalf("exporting past the build ceiling: %s", scalingErr)
	}
	return scalingRun
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// 16× the occurrences under one assembly cost about 16× the writer's Transfer, not
// the square the property walk cost.
//
// Timed, because OCCT exposes nothing to count, and timed on Transfer alone
// ("export_transfer"): the whole export phase adds writing and encoding, which are
// linear and hid the walk at 32,768 occurrences. Robust to a shared machine: both
// sizes run in one process, each is the faster of two runs (load slows a run,
// never speeds one), and the bound is twice linear. The walked writer is timed at
// the large size in the same run, so the fixture is shown to be big enough to see
// the walk; a fixture that could not see it would pass anything.
func TestKernel_ExportTimeGrowsLinearlyPastTheBuildCeiling(t *testing.T) {
	got := exportScaling(t)
	t.Logf("transfer: %d occurrences %.2f s, %d occurrences %.2f s, with the property walk %.2f s; "+
		"whole export %.2f s, %.2f s, %.2f s; %d bytes",
		got.Small, got.SmallTransfer, got.Large, got.LargeTransfer, got.WalkedTransfer,
		got.SmallExport, got.LargeExport, got.WalkedExport, got.Bytes)
	if got.Parts != scalingLarge || got.Instances != scalingLarge || got.Breps != 2 {
		t.Fatalf("%d parts, %d instances, %d solid B-reps; want %d, %d and 2 shared definitions",
			got.Parts, got.Instances, got.Breps, scalingLarge, scalingLarge)
	}
	// Reported at all: a transfer that reads zero passes every bound below.
	if got.SmallTransfer <= 0 || got.LargeTransfer <= 0 || got.WalkedTransfer <= 0 {
		t.Fatalf("the writer's transfer time was not reported: %+v", got)
	}
	if got.PropsModeShipped {
		t.Errorf("the sidecar ships with the writer's validation-property walk on (_STEP_WRITE_PROPS); " +
			"it is quadratic in the occurrences under one assembly and the sidecar writes no properties")
	}

	// A floor under the small transfer, so load on a fifth of a second cannot make
	// the ratio. Calibrated 2026-09-15: shipped, 4,096 transferred in 0.15–0.25 s and
	// 65,536 in 3.0–4.8 s (15–32×, the top under 47% machine CPU); the property walk
	// took 12.9 s at 65,536, ~52× the floor. The bound sits between them, and the
	// walk is also caught below by the comparison made in the same run.
	const floor = 0.25
	if ratio := got.LargeTransfer / math.Max(got.SmallTransfer, floor); ratio > 40 {
		t.Errorf("16× the occurrences took %.1f× as long to transfer (%.2f s → %.2f s); linear is ~16×, "+
			"and the property walk measured ~52×", ratio, got.SmallTransfer, got.LargeTransfer)
	}
	if got.WalkedTransfer < 2*got.LargeTransfer {
		t.Errorf("the property walk transferred %d occurrences in %.2f s and the shipped writer in %.2f s: "+
			"either the walk is back on, or this fixture is too small to show it and the ratio above proves nothing",
			scalingLarge, got.WalkedTransfer, got.LargeTransfer)
	}
}

// Past the ceiling, the file is the one the writer wrote with its property walk on,
// byte for byte below the header but for each instance's id string (at 65,536), and
// read back it holds every occurrence, named, where the build put it (at
// scalingReadBack, still past K2's 4,096).
//
// ‼️ The id strings are a counter OCCT keeps per PROCESS: the same export run
// three times in one sidecar wrote its first instance as '1', '32769' and '65537'.
// That was true before this change too. A longer id also moves where the writer
// wraps a long line, so the script joins wrapped lines and blanks the ids before
// comparing.
//
// Byte equality alone cannot see a mutation both paths share (a location dropped
// in _step_document is dropped in both files), and a read-back alone cannot see a
// setting the fix changed (names written by one path and not the other still read
// back as some names). So both.
func TestKernel_ALargeExportIsTheFileThePropertyWalkWrote(t *testing.T) {
	got := exportScaling(t)
	if !got.SameFile {
		t.Errorf("%d occurrences: the exported file differs from the one written with the property walk on; "+
			"turning the walk off must not change what is written", got.Large)
	}

	if got.ReadBackParts != scalingReadBack {
		t.Fatalf("the read-back export built %d parts, want %d", got.ReadBackParts, scalingReadBack)
	}
	file := reimport(t, got.step)
	if file.Components != scalingReadBack {
		t.Errorf("the file holds %d parts, the build %d", file.Components, scalingReadBack)
	}
	if file.Shapes != 2 {
		t.Errorf("the file holds %d distinct shapes, want 2 (the stud and the pin)", file.Shapes)
	}
	// The extent before the names. ‼️ A file written without placements also reads
	// back with its instances named by number, and the name check below stops the
	// test: in that order the placement drill went red on the names alone, and
	// nothing showed this fence sees a part in the wrong place.
	for i := range got.ReadBackBounds {
		if math.Abs(file.Bounds[i]-got.ReadBackBounds[i]) > 0.01 {
			t.Errorf("the file's extent %v is not the build's %v: a part was written somewhere it was not built",
				file.Bounds, got.ReadBackBounds)
			break
		}
	}
	names := map[string]bool{}
	for _, n := range file.Names {
		if !strings.HasPrefix(n, "Stud ") && !strings.HasPrefix(n, "Pin ") {
			t.Fatalf("an instance is named %q; every one is written as its part's label", n)
		}
		names[n] = true
	}
	if len(names) != scalingReadBack {
		t.Errorf("%d distinct names in the file for %d uniquely named parts", len(names), scalingReadBack)
	}
}
