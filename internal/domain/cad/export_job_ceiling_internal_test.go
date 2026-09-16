package cad

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad/cadtest"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// studPanel places rows × per studs through a tree, and extra more at the top.
func studPanel(rows, per, extra int) geometry.Document {
	stud := geometry.Part{ID: "stud", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}}
	d := geometry.Document{Name: "panel", Units: "mm", Root: "panel", Definitions: []geometry.Part{stud},
		Assemblies: []geometry.Assembly{
			{ID: "panel", Children: []geometry.Child{{ID: "row", Ref: "row",
				Pattern: &geometry.Pattern{Kind: "linear", Count: rows, Offset: []float64{0, 3, 0}}}}},
			{ID: "row", Children: []geometry.Child{{ID: "stud", Ref: "stud",
				Pattern: &geometry.Pattern{Kind: "linear", Count: per, Offset: []float64{2, 0, 0}}}}},
		}}
	for i := 0; i < extra; i++ {
		d.Parts = append(d.Parts, geometry.Part{ID: "extra", Shape: "box",
			Size: map[string]float64{"width": 1, "height": 1, "depth": 1}, Position: []float64{0, -10, 0}})
	}
	return d
}

// The off-node export job is bounded by MaxExportJobParts and by nothing tighter.
//
// # Why sizes past BOTH building ceilings
//
// Two ceilings refuse ordinary builds: 4,096 for anything that is not a view and
// 8,192 for a view (#110). A STEP build that fell through to either would still pass
// a job of 4,200 parts, which is all the real-kernel fence builds, so the job is
// asked here for 8,193 parts (past the view's ceiling too) and for exactly 90,000,
// and each must REACH the kernel. Against cadtest's fake process, so what is
// checked is the size decision and that the request carried every part, not the file; the
// real file is TestKernel_AnExportJobBuildsAboveTheBuildingCeilingWithoutTheInterferenceCheck.
func TestKernel_AnExportJobIsBoundedByItsOwnCeilingAndNotTheBuildingOnes(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	k := New(python, logx.Discard())
	defer k.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if geometry.MaxExportJobParts != 90_000 {
		t.Fatalf("MaxExportJobParts is %d; this fence's sizes are chosen around 90,000", geometry.MaxExportJobParts)
	}
	pastView := studPanel(16, 512, 1)   // 8,193
	atCeiling := studPanel(225, 400, 0) // 90,000 (a pattern repeats at most 512)
	over := studPanel(225, 400, 1)      // 90,001

	// The fake answers with how many solids it was sent, so Parts is what the kernel
	// request carried: a design cut or emptied on the way would show here, not pass.
	for _, c := range []struct {
		name string
		doc  geometry.Document
		want int
	}{{"8,193", pastView, 8193}, {"90,000", atCeiling, 90_000}} {
		built, err := k.ExportSTEPJob(ctx, c.doc, geometry.Millimetre)
		if err != nil {
			t.Errorf("an export job of %s parts was not sent to the kernel whole: %v", c.name, err)
			continue
		}
		if built.Parts != c.want {
			t.Errorf("an export job of %s parts sent the kernel %d solids, want %d", c.name, built.Parts, c.want)
		}
	}
	if cadtest.Starts(t, dir) == 0 {
		t.Errorf("no export job reached the kernel")
	}

	// The ordinary STEP export of the same 8,193 is still refused at 4,096.
	if _, err := k.BuildDocument(ctx, pastView, geometry.Millimetre, "step"); errs.CodeOf(err) != errs.CodeValidationFailed ||
		!strings.Contains(err.Error(), "more than 4096 parts") {
		t.Errorf("an ordinary STEP export of 8,193 parts was not refused at 4096: %v", err)
	}

	// One over the job's ceiling is refused, naming it, before the kernel is asked.
	starts := cadtest.Starts(t, dir)
	_, err := k.ExportSTEPJob(ctx, over, geometry.Millimetre)
	if errs.CodeOf(err) != errs.CodeValidationFailed || !strings.Contains(err.Error(), "more than 90000 parts") {
		t.Errorf("an export job of 90,001 parts was not refused naming MaxExportJobParts: %v", err)
	}
	if cadtest.Starts(t, dir) != starts {
		t.Errorf("an export job of 90,001 parts started a kernel process before it was refused")
	}
}
