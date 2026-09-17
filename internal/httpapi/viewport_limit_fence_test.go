package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// yardOfRows is a design of rows × perRow studs, each row its own child of the root, so
// the first view has rows it may take and rows it must leave.
func yardOfRows(rows, perRow int) geometry.Document {
	stud := geometry.Part{ID: "stud", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}, Color: "#cccccc", Opacity: 1}
	children := make([]geometry.Child, 0, rows)
	for i := 0; i < rows; i++ {
		children = append(children, geometry.Child{ID: fmt.Sprintf("row-%d", i), Ref: "row", Position: []float64{0, float64(3 * i), 0}})
	}
	return geometry.Document{Name: "yard", Units: "mm", Root: "yard", Parts: []geometry.Part{},
		NotVerified: []string{"a fence fixture"}, Definitions: []geometry.Part{stud},
		Assemblies: []geometry.Assembly{
			{ID: "yard", Children: children},
			{ID: "row", Children: []geometry.Child{{ID: "stud", Ref: "stud",
				Pattern: &geometry.Pattern{Kind: "linear", Count: perRow, Offset: []float64{2, 0, 0}}}}},
		}}
}

// The viewport's limit stays 100,000, and the reason it may stay is still true.
//
// Decided 2026-09-15 (PR #88's open question "100,000 or ~30k?", delegated): keep
// 100,000, because it was measured at 99,971 occurrences, and because a design that
// large is not drawn whole on first view — the workbench asks for at most 8,192 parts
// and the rest arrives a subtree at a time (#93). geometry/limits.go maxViewportParts
// states both. This holds the number in Go and in the browser, and holds the second
// reason on a design AT the ceiling: if the first view stopped being bounded, the
// limit would no longer rest on what it was kept for.
func TestViewportLimitIsTheMeasuredOneAndAFirstViewStaysSmall(t *testing.T) {
	const decided, firstViewAtMost = 100_000, 8192
	if geometry.MaxViewportParts() != decided {
		t.Fatalf("the viewport draws %d parts at once; the limit decided on a measured 99,971-part car is %d "+
			"(geometry/limits.go says why; change it only on a new measurement)", geometry.MaxViewportParts(), decided)
	}
	at, over := yardOfRows(200, 500), yardOfRows(200, 500)
	over.Parts = []geometry.Part{{ID: "one-more", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}, Color: "#cccccc", Opacity: 1}}
	if n := len(at.Expanded().Parts); n != decided {
		t.Fatalf("the fixture places %d parts, not the ceiling's %d", n, decided)
	}
	if at.ViewportRefusal() != "" || over.ViewportRefusal() == "" {
		t.Fatalf("Go refuses %q at the ceiling and %q one past it", at.ViewportRefusal(), over.ViewportRefusal())
	}
	out := runNodeHarness(t, `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  process.stdout.write(JSON.stringify({ atRefusal: F.drawRefusal(input.at), overRefusal: F.drawRefusal(input.over),
    lazily: F.loadsLazily(input.at), firstView: F.firstViewPaths(input.at) }));
`, map[string]any{"at": at, "over": over})
	var got struct {
		AtRefusal, OverRefusal string
		Lazily                 bool
		FirstView              []string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output %q: %v", out, err)
	}
	if got.AtRefusal != "" {
		t.Fatalf("the browser refuses a design of %d parts, which Go's viewport limit draws: %q", decided, got.AtRefusal)
	}
	if got.OverRefusal != over.ViewportRefusal() {
		t.Fatalf("one part past the ceiling, the browser says %q and Go %q", got.OverRefusal, over.ViewportRefusal())
	}
	if !got.Lazily {
		t.Fatalf("a design of %d parts is loaded whole, not a subtree at a time", decided)
	}
	// Counted by Go, from what each asked-for path places.
	perPath := map[string]int{}
	for _, p := range at.Expanded().Parts {
		perPath[strings.SplitN(p.ID, "/", 2)[0]]++
	}
	total := 0
	for _, path := range got.FirstView {
		n, ok := perPath[path]
		if !ok {
			t.Fatalf("the first view asks for %q, which places nothing", path)
		}
		total += n
	}
	if total == 0 || total > firstViewAtMost {
		t.Fatalf("the first view of a design at the viewport's ceiling asks for %d parts (%d rows); the limit is kept "+
			"because that stays within %d and above nothing", total, len(got.FirstView), firstViewAtMost)
	}
}
