package cad_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// What the manufacturability pass costs, at the two part counts the plan names.
// Run with FORGE_MFG_COST=1; the numbers it prints are in
// docs/spikes/2026-09-20-manufacturability-cost/.
func TestCost_Manufacturability(t *testing.T) {
	if os.Getenv("FORGE_MFG_COST") == "" {
		t.Skip("measurement; set FORGE_MFG_COST=1")
	}
	k := kernel(t)
	for _, n := range []int{512, 4096} {
		for _, distinct := range []bool{false, true} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			doc := costDoc(n, distinct)
			// Interleaved: the same document built without the pass and with it,
			// back to back, because this machine runs other work in parallel.
			plain, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
			if err != nil {
				cancel()
				t.Fatalf("%d parts (distinct=%v) plain: %v", n, distinct, err)
			}
			checked, err := k.BuildEvaluated(ctx, doc, geometry.Millimetre, "", nil)
			if err != nil {
				cancel()
				t.Fatalf("%d parts (distinct=%v) checked: %v", n, distinct, err)
			}
			again, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
			if err != nil {
				cancel()
				t.Fatalf("%d parts (distinct=%v) plain again: %v", n, distinct, err)
			}
			cancel()
			t.Logf("n=%d distinct=%v: manufacturability %v over %d faces (%d reused of %d), "+
				"shapes %v/%v/%v, interferences %v/%v/%v",
				n, distinct, checked.Phases.Manufacturability, checked.ManufacturabilityFaces,
				checked.ManufacturabilityReused, checked.ManufacturabilityParts,
				plain.Phases.Shapes, checked.Phases.Shapes, again.Phases.Shapes,
				plain.Phases.Interferences, checked.Phases.Interferences, again.Phases.Interferences)
		}
	}
}

// costDoc is n boxes in a row, either all the same shape (the usual case: one
// definition, n placed copies) or every one a different size (the worst case for
// the measurement cache: nothing can be reused).
func costDoc(n int, distinct bool) geometry.Document {
	doc := geometry.Document{Name: "cost", Units: "mm"}
	for i := 0; i < n; i++ {
		w := 10.0
		if distinct {
			w = 10.0 + float64(i)*1e-3
		}
		doc.Parts = append(doc.Parts, geometry.Part{
			ID: fmt.Sprintf("part-%d", i), Name: fmt.Sprintf("part %d", i), Shape: "box",
			Size:     map[string]float64{"width": w, "height": 6, "depth": 4},
			Position: []float64{float64(i%64) * 30, float64(i/64) * 30, 0},
			Rotation: []float64{0, 0, 0},
		})
	}
	return doc
}
