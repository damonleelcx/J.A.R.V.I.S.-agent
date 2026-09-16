package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The browser loads a whole design's kernel mesh up to what the kernel builds for a view,
// and a subtree at a time past it. forge3d.js's LAZY_OCCURRENCES and Go's MaxBuiltParts
// are one number written twice: if the browser's were lower, a design the kernel builds
// would be loaded in pieces; if higher, the browser would ask for a whole mesh the kernel
// refuses. docs/spikes/2026-09-15-ceiling-on-linux.
func TestRendererLoadsLazilyExactlyPastTheKernelsViewCeiling(t *testing.T) {
	if geometry.MaxBuiltParts() != 8192 {
		t.Fatalf("the kernel builds a view of %d parts; this fixture places 8192", geometry.MaxBuiltParts())
	}
	stud := geometry.Part{ID: "stud", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}, Color: "#cccccc", Opacity: 1}
	panel := func(extra int) geometry.Document {
		d := geometry.Document{Name: "panel", Units: "mm", Root: "panel", Parts: []geometry.Part{},
			Definitions: []geometry.Part{stud},
			Assemblies: []geometry.Assembly{
				{ID: "panel", Children: []geometry.Child{{ID: "row", Ref: "row",
					Pattern: &geometry.Pattern{Kind: "linear", Count: 16, Offset: []float64{0, 3, 0}}}}},
				{ID: "row", Children: []geometry.Child{{ID: "stud", Ref: "stud",
					Pattern: &geometry.Pattern{Kind: "linear", Count: 512, Offset: []float64{2, 0, 0}}}}},
			}}
		for i := 0; i < extra; i++ {
			d.Parts = append(d.Parts, geometry.Part{ID: "extra", Shape: "box",
				Size: map[string]float64{"width": 1, "height": 1, "depth": 1}, Color: "#cccccc", Opacity: 1})
		}
		return d
	}
	at, over := panel(0), panel(1)
	if at.BuildRefusal() != "" || over.BuildRefusal() == "" {
		t.Fatal("the fixtures are no longer either side of the kernel's view ceiling")
	}

	out := runNodeHarness(t, `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  process.stdout.write(JSON.stringify({ at: F.loadsLazily(input.at), over: F.loadsLazily(input.over) }));
`, map[string]any{"at": at, "over": over})
	var got struct{ At, Over *bool }
	if err := json.Unmarshal(out, &got); err != nil || got.At == nil || got.Over == nil {
		t.Fatalf("unreadable renderer output %q: %v", out, err)
	}
	if *got.At {
		t.Errorf("a design of %d parts, which the kernel builds whole, is loaded a subtree at a time", geometry.MaxBuiltParts())
	}
	if !*got.Over {
		t.Errorf("a design of %d parts, which the kernel refuses to build whole, is loaded whole", geometry.MaxBuiltParts()+1)
	}
}
