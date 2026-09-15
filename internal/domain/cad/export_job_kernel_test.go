package cad_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The kernel call the off-node STEP export job makes (cad.Kernel.ExportSTEPJob).
//
// Against the real kernel, for the reason kernel() gives: whether the file has
// every part and whether the interference phase ran are properties of the sidecar,
// and a fake would only agree with this test.

// boxRows is rows × per occurrences of one small box, placed by repeats, apart.
func boxRows(rows, per int) geometry.Document {
	d := geometry.Document{Name: "rows", Units: "mm"}
	for i := 0; i < rows; i++ {
		d.Parts = append(d.Parts, geometry.Part{ID: "row" + string(rune('a'+i%26)) + strings.Repeat("z", i/26),
			Name: "Box", Shape: "box", Size: map[string]float64{"width": 4, "height": 4, "depth": 4},
			Position: []float64{0, float64(10 * i), 0}, Rotation: []float64{0, 0, 0},
			Repeat: &geometry.Repeat{Count: per, Offset: []float64{10, 0, 0}}})
	}
	return d
}

// Above the building ceiling, with no interference phase, and with every part in
// the file — while the ordinary build of the same design is still refused, and an
// ordinary build of a small one still runs the check.
func TestKernel_AnExportJobBuildsAboveTheBuildingCeilingWithoutTheInterferenceCheck(t *testing.T) {
	k := kernel(t)
	ctx := context.Background()
	big := boxRows(21, 200) // 4,200 occurrences

	if _, err := k.BuildDocument(ctx, big, geometry.Unit("mm"), "step"); errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("an ordinary STEP build of 4,200 parts answered %v; the 4,096 ceiling must still refuse it", err)
	}

	got, err := k.ExportSTEPJob(ctx, big, geometry.Unit("mm"))
	if err != nil {
		t.Fatalf("the export job's build of 4,200 parts failed: %v", err)
	}
	if got.Parts != 4200 {
		t.Errorf("the kernel built %d parts, want 4,200", got.Parts)
	}
	if !bytes.HasPrefix(got.STEP, []byte("ISO-10303-21")) {
		t.Errorf("the file does not start as a STEP file: %q", got.STEP[:min(len(got.STEP), 40)])
	}
	if got.InterferenceBoxTests != 0 || got.Phases.Interferences != 0 || len(got.Interferences) != 0 {
		t.Errorf("the export job ran the interference check (%d box tests, %s): its ceiling rests on "+
			"STEP export measured without it", got.InterferenceBoxTests, got.Phases.Interferences)
	}

	// Two boxes sharing material. The ordinary build finds the clash, so the check
	// is still on by default; the job's build of the same design does not look.
	clash := geometry.Document{Name: "clash", Units: "mm", Parts: []geometry.Part{
		{ID: "a", Name: "A", Shape: "box", Size: map[string]float64{"width": 20, "height": 20, "depth": 20},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		{ID: "b", Name: "B", Shape: "box", Size: map[string]float64{"width": 20, "height": 20, "depth": 20},
			Position: []float64{10, 0, 0}, Rotation: []float64{0, 0, 0}},
	}}
	plain, err := k.BuildDocument(ctx, clash, geometry.Unit("mm"), "step")
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Interferences) == 0 {
		t.Errorf("an ordinary build of two boxes sharing material reported no interference; the check must " +
			"stay on unless asked off")
	}
	job, err := k.ExportSTEPJob(ctx, clash, geometry.Unit("mm"))
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Interferences) != 0 || job.Phases.Interferences != 0 {
		t.Errorf("the export job looked for interference in the same design: %d found in %s",
			len(job.Interferences), job.Phases.Interferences)
	}
}

// Above the job's ceiling, refused before any process is asked, naming it.
func TestKernel_AnExportJobAboveItsCeilingIsRefusedBeforeTheKernel(t *testing.T) {
	k := kernel(t)
	_, err := k.ExportSTEPJob(context.Background(), boxRows(1954, 512), geometry.Unit("mm")) // 1,000,448
	if errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("a 1,000,448-part export job answered %v, want VALIDATION_FAILED", err)
	}
	if !strings.Contains(err.Error(), "90000") {
		t.Errorf("the refusal does not name the job's ceiling: %v", err)
	}
}
