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

// panelOf8192 places exactly 16 × 512 studs through a tree, and extra more at the top.
func panelOf8192(extra int) geometry.Document {
	stud := geometry.Part{ID: "stud", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}}
	d := geometry.Document{Name: "panel", Units: "mm", Root: "panel", Definitions: []geometry.Part{stud},
		Assemblies: []geometry.Assembly{
			{ID: "panel", Children: []geometry.Child{{ID: "row", Ref: "row",
				Pattern: &geometry.Pattern{Kind: "linear", Count: 16, Offset: []float64{0, 3, 0}}}}},
			{ID: "row", Children: []geometry.Child{{ID: "stud", Ref: "stud",
				Pattern: &geometry.Pattern{Kind: "linear", Count: 512, Offset: []float64{2, 0, 0}}}}},
		}}
	for i := 0; i < extra; i++ {
		d.Parts = append(d.Parts, geometry.Part{ID: "extra", Shape: "box",
			Size: map[string]float64{"width": 1, "height": 1, "depth": 1}, Position: []float64{0, -10, 0}})
	}
	return d
}

// The kernel is asked to build a view of 8192 parts and refuses 8193 by name; a STEP
// export, a mass report and a build with no format of the same 8192 are refused at
// 4096, which is all that was measured for them. Runs against cadtest's fake process,
// so it needs no build123d. docs/spikes/2026-09-15-ceiling-on-linux.
func TestKernel_BuildsAViewOf8192PartsAndRefusesEveryOtherBuildPast4096(t *testing.T) {
	python, dir := cadtest.FakeKernel(t)
	k := New(python, logx.Discard())
	defer k.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	at, over := panelOf8192(0), panelOf8192(1)

	if _, err := k.BuildMesh(ctx, at, geometry.Millimetre); err != nil {
		t.Fatalf("a view of 8192 parts was not built: %v", err)
	}
	if cadtest.Starts(t, dir) != 1 {
		t.Fatalf("a view of 8192 parts never reached the kernel (%d processes started)", cadtest.Starts(t, dir))
	}
	if _, err := k.BuildMesh(ctx, over, geometry.Millimetre); errs.CodeOf(err) != errs.CodeValidationFailed ||
		!strings.Contains(err.Error(), "more than 8192 parts") {
		t.Errorf("a view of 8193 parts was not refused by name: %v", err)
	}
	for name, build := range map[string]func() (*Build, error){
		"a STEP export":        func() (*Build, error) { return k.BuildDocument(ctx, at, geometry.Millimetre, "step") },
		"a mass report":        func() (*Build, error) { return k.BuildProperties(ctx, at, geometry.Millimetre) },
		"a build of no format": func() (*Build, error) { return k.BuildDocument(ctx, at, geometry.Millimetre, "") },
	} {
		if _, err := build(); errs.CodeOf(err) != errs.CodeValidationFailed || !strings.Contains(err.Error(), "more than 4096 parts") {
			t.Errorf("%s of 8192 parts was not refused at 4096: %v", name, err)
		}
	}
}
