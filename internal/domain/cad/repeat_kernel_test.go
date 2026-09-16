package cad_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Repeated parts, through the real kernel.
//
// Until 2026-09-13 no kernel test built a document with a repeat in it — the
// repeat fences stopped at Faults and Tessellate, which both expand before they
// read the features. BuildDocument did not: it expanded the solids and read the
// operations from the authored document, so a fuse naming "spoke" reached a
// sidecar holding only "spoke-1" … "spoke-6" and was reported as
// "spoke could not be built, so this was not applied".
// docs/bugfix/2026-09-13-features-on-repeated-parts-were-never-applied.md

func spokedHub(spokes int) geometry.Document {
	hub := geometry.Part{ID: "hub", Name: "Hub", Shape: "cylinder",
		Size:     map[string]float64{"radius": 20, "height": 10},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
	spoke := geometry.Part{ID: "spoke", Name: "Spoke", Shape: "box",
		Size:     map[string]float64{"width": 4, "height": 4, "depth": 60},
		Position: []float64{0, 0, 30}, Rotation: []float64{0, 0, 0},
		Repeat: &geometry.Repeat{Count: spokes, About: "y"}}
	return geometry.Document{Name: "wheel", Units: "mm", Parts: []geometry.Part{hub, spoke},
		Features: []geometry.Feature{{ID: "weld", Op: "fuse", Of: "hub", With: []string{"spoke"}}}}
}

// The case the contract promises in words: "a feature that names the part acts
// on EVERY copy". A fused wheel is ONE solid, with nothing left unapplied and
// no spoke reported as overlapping another.
func TestKernel_AFeatureNamingARepeatedPartIsApplied(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	got, err := k.BuildDocument(ctx, spokedHub(6), geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FeatureFailures) != 0 {
		t.Fatalf("the fuse naming the repeated spokes was not applied: %v", got.FeatureFailures)
	}
	if got.Parts != 1 {
		t.Errorf("a hub fused with its six spokes is %d solids, want 1", got.Parts)
	}
	// Before the fix the unwelded spokes were still six separate solids crossing
	// at the hub, and the interference check reported every one of them.
	if len(got.Interferences) != 0 {
		t.Errorf("a welded wheel reported %d interference(s): %+v", len(got.Interferences), got.Interferences)
	}
	hubVolume := math.Pi * 20 * 20 * 10
	if got.Volume <= hubVolume*1.1 {
		t.Errorf("the fused solid is %.0f mm³, barely the hub's %.0f — the spokes are not in it",
			got.Volume, hubVolume)
	}
}

// Every copy of a repeated SCRIPTED part is built. The script used to be looked
// up by the copy's id in the authored document, where "block-2" does not exist,
// so every copy was left out as "a scripted part with no script".
func TestKernel_ARepeatedScriptedPartIsBuiltEveryTime(t *testing.T) {
	k := scriptedKernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	block := geometry.Part{ID: "block", Name: "Scripted Block", Shape: "script",
		Script:   "result = Box(4, 4, 4)\n",
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0},
		Repeat: &geometry.Repeat{Count: 3, Offset: []float64{20, 0, 0}}}
	doc := geometry.Document{Name: "blocks", Units: "mm", Parts: []geometry.Part{block}}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("three copies of a scripted block did not build: %v", err)
	}
	for _, n := range got.Inferred {
		if strings.Contains(n, "not in this file") {
			t.Errorf("a copy was left out: %s", n)
		}
	}
	if got.Parts != 3 {
		t.Errorf("built %d of 3 copies", got.Parts)
	}
	if math.Abs(got.Volume-3*64) > 1e-3 {
		t.Errorf("volume %.3f mm³, want %d", got.Volume, 3*64)
	}
}
