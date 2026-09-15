package httpapi

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The Go tessellator's instance reply, written back out, is Tessellate's mesh: every
// placed part's triangles, corner for corner and in the same winding, in millimetres —
// a mirrored copy included, and a document authored in centimetres. So a subtree
// served past the kernel's ceiling is the surface an OBJ export of it would contain,
// each shape sent once.
func TestMeshSubtree_TheGoInstancesAreTessellateWrittenOutOncePerShape(t *testing.T) {
	for unit, scale := range map[geometry.Unit]float64{"mm": 1, "cm": 10} {
		doc := instancedCar()
		doc.Units = string(unit)
		mirrored := 0
		for _, p := range doc.Expanded().Parts {
			if p.Mirrored {
				mirrored++
			}
		}
		want := geometry.Tessellate(doc, unit)
		built := goBuild(doc, unit)
		if mirrored == 0 || len(want.Groups) < 60 || len(built.MeshDefinitions)*4 > len(built.MeshInstances) {
			t.Fatalf("%s: the fixture no longer exercises what it claims: %d mirrored, %d parts, %d definitions for %d copies",
				unit, mirrored, len(want.Groups), len(built.MeshDefinitions), len(built.MeshInstances))
		}
		got := map[string]cad.MeshPart{}
		for _, m := range built.WorldMeshes() {
			got[m.ID] = m
		}
		if len(got) != len(want.Groups) {
			t.Fatalf("%s: %d parts written out from the instances, Tessellate places %d", unit, len(got), len(want.Groups))
		}
		for _, g := range want.Groups {
			m, ok := got[g.PartID]
			if !ok || len(m.Triangles) != 3*len(g.Triangles) {
				t.Fatalf("%s: %s has %d triangles from the instances and %d from Tessellate", unit, g.PartID, len(m.Triangles)/3, len(g.Triangles))
			}
			for i, tri := range g.Triangles {
				for c, corner := range [3][3]float64{tri.A, tri.B, tri.C} {
					v := int(m.Triangles[i*3+c]) * 3
					for k := 0; k < 3; k++ {
						if math.Abs(m.Vertices[v+k]-corner[k]*scale) > 1e-6 {
							t.Fatalf("%s: %s triangle %d corner %d is at %v from the instances; Tessellate puts it at %v %s",
								unit, g.PartID, i, c, m.Vertices[v:v+3], corner, unit)
						}
					}
				}
			}
		}
	}
}

// One subtree's mesh is the whole design's mesh, filtered to the path. Phase 6, stage W2.
//
// # What could go wrong that needs holding
//
// The workbench asks for a subtree when somebody opens a row, and draws the reply in
// place of the row's parts. If the server's idea of "under this path" differs from the
// row's — a copy number not read at one level, a top-level repeat matched by name
// only, a feature kept whose tool is in a sibling — the row is drawn with parts
// missing, doubled, or placed differently from how the whole design places them, and
// every fence on the whole reply stays green.
//
// So the subtree is held to the WHOLE reply, and "under this path" is worked out
// without reading a path: placedBy takes the child away and sees what disappears.

func payloadOf(t *testing.T, body map[string]any) ([]meshDefinitionDTO, []meshInstanceDTO) {
	t.Helper()
	defs, ok1 := body["definitions"].([]meshDefinitionDTO)
	insts, ok2 := body["instances"].([]meshInstanceDTO)
	if !ok1 || !ok2 {
		t.Fatalf("the subtree reply carries no definitions or instances: %v", body)
	}
	return defs, insts
}

// The Go tessellator's instances for a subtree are exactly its instances for the whole
// design that Go places beneath that path — same ids, labels, matrices and triangles —
// and each is placed where the exporter places the part.
func TestMeshSubtree_TheSubtreeReplysInstancesAreTheWholeReplysFilteredToThePath(t *testing.T) {
	doc := instancedCar()
	doc.Definitions[0].Repeat = &geometry.Repeat{Count: 3, Offset: []float64{0, 0, 40}}
	expanded := doc.Expanded().Parts
	byID := map[string]geometry.Part{}
	for _, p := range expanded {
		byID[p.ID] = p
	}
	whole := goBuild(doc, "mm")
	wholeParts, wholeDefs, wholeInsts := meshPayload(whole)
	if len(wholeParts) != 0 || len(wholeInsts) != len(expanded) {
		t.Fatalf("the whole Go reply has %d instances and %d placed parts for %d parts", len(wholeInsts), len(wholeParts), len(expanded))
	}
	wholeByID := map[string]meshInstanceDTO{}
	for _, in := range wholeInsts {
		wholeByID[in.ID] = in
	}

	prefixed := func(prefix string) map[string]bool {
		out := map[string]bool{}
		for _, p := range expanded {
			if strings.HasPrefix(p.ID, prefix) {
				out[p.ID] = true
			}
		}
		return out
	}
	cases := []struct {
		path string
		want map[string]bool
	}{
		{"front-left", placedBy(doc, "car", "front-left")},
		{"front-right", placedBy(doc, "car", "front-right")},
		{"lamp", placedBy(doc, "car", "lamp")},
		{"trim", placedBy(doc, "car", "trim")},
		{"front-right/damper", both(placedBy(doc, "car", "front-right"), placedBy(doc, "corner", "damper"))},
		{"front-left-2", prefixed("front-left-2/")},
		{"frame", map[string]bool{"frame": true}},
	}
	for _, tc := range cases {
		body, err := subtreeMesh(context.Background(), nil, doc, "mm", tc.path)
		if err != nil {
			t.Fatalf("%s: refused: %v", tc.path, err)
		}
		if body["source"] != sourceGo || body["occurrences"] != len(tc.want) {
			t.Errorf("%s: source %v, %v occurrences; want go and %d", tc.path, body["source"], body["occurrences"], len(tc.want))
		}
		defs, insts := payloadOf(t, body)
		got := map[string]bool{}
		for _, in := range insts {
			if got[in.ID] {
				t.Fatalf("%s: %s is sent twice", tc.path, in.ID)
			}
			got[in.ID] = true
			w, ok := wholeByID[in.ID]
			if !ok {
				t.Fatalf("%s: the subtree sends %s, which the whole design's reply does not place", tc.path, in.ID)
			}
			if in.Label != w.Label || in.Matrix != w.Matrix {
				t.Fatalf("%s: %s is %q at %v in the subtree and %q at %v in the whole reply", tc.path, in.ID, in.Label, in.Matrix, w.Label, w.Matrix)
			}
			sd, wd := defs[in.Definition], wholeDefs[w.Definition]
			if len(sd.Triangles) != len(wd.Triangles) || len(sd.Vertices) != len(wd.Vertices) {
				t.Fatalf("%s: %s is drawn with %d triangles in the subtree and %d in the whole reply", tc.path, in.ID, len(sd.Triangles)/3, len(wd.Triangles)/3)
			}
			for k := range sd.Vertices {
				if sd.Vertices[k] != wd.Vertices[k] {
					t.Fatalf("%s: %s's definition differs from the whole reply's at vertex %d", tc.path, in.ID, k/3)
				}
			}
			// Where the exporter places it: T·R, the reflection in the definition.
			p := byID[in.ID]
			want := placement(geometry.Part{Position: p.Position, Rotation: p.Rotation})
			for k := range want {
				if math.Abs(in.Matrix[k]-want[k]) > 1e-9 {
					t.Fatalf("%s: %s is at %v, the exporter places it at %v", tc.path, in.ID, in.Matrix, want)
				}
			}
		}
		if strings.Join(keys(got), ",") != strings.Join(keys(tc.want), ",") {
			t.Fatalf("%s: the subtree sends\n  %v\nGo places beneath it\n  %v", tc.path, keys(got), keys(tc.want))
		}
	}

	// A cut inside the subtree is kept with it; asked for the damper alone, the cut
	// whose tools are its sibling pins is named as reaching outside.
	inside, _ := subtreeMesh(context.Background(), nil, doc, "mm", "front-left-2")
	alone, _ := subtreeMesh(context.Background(), nil, doc, "mm", "front-left-2/damper")
	if n := len(inside["features_outside"].([]string)); n != 0 {
		t.Errorf("front-left-2 holds its own cut, and %d feature(s) were named as reaching outside it", n)
	}
	if out := alone["features_outside"].([]string); len(out) != 1 || !strings.Contains(out[0], "front-left-2/damper") {
		t.Errorf("the damper's cut uses pins outside front-left-2/damper and was not named: %v", out)
	}
}

// A path that is not an occurrence of this design is refused, naming the path — never
// answered with an empty mesh, which the viewport would draw as an empty subtree.
func TestMeshSubtree_AnUnknownPathIsRefusedByName(t *testing.T) {
	doc := instancedCar()
	for _, tc := range []struct {
		path string
		code errs.Code
	}{
		{"front-middle", errs.CodeNotFound},
		{"front-left/wheel", errs.CodeNotFound},
		{"front-left-9", errs.CodeNotFound},
		{"lamp-7", errs.CodeNotFound},
		{"", errs.CodeValidationFailed},
		{"front-left//pin", errs.CodeValidationFailed},
		{"/front-left", errs.CodeValidationFailed},
	} {
		body, err := subtreeMesh(context.Background(), nil, doc, "mm", tc.path)
		if err == nil {
			t.Fatalf("%q was answered with %v occurrences instead of refused", tc.path, body["occurrences"])
		}
		if errs.CodeOf(err) != tc.code {
			t.Errorf("%q was refused as %s, want %s: %v", tc.path, errs.CodeOf(err), tc.code, err)
		}
		if tc.path != "" && !strings.Contains(errs.DetailOf(err), tc.path) {
			t.Errorf("%q was refused without naming it: %s", tc.path, errs.DetailOf(err))
		}
	}
}

// The viewport's ceiling and the kernel's are applied to the parts a subtree places, not
// to the design: a small subtree of a design past both is served, a subtree past the
// viewport's is refused naming itself, and one past the kernel's is tessellated in Go
// even where a kernel is configured.
func TestMeshSubtree_LimitsAreTheSubtreesOwn(t *testing.T) {
	before := geometry.CurrentLimits()
	geometry.SetLimits(geometry.Limits{MaxOccurrences: 120_000})
	defer geometry.SetLimits(before)

	rivet := geometry.Part{ID: "rivet", Shape: "cylinder", Size: map[string]float64{"radius": 1, "height": 3},
		Color: "#cccccc", Opacity: 1}
	line := func(n int) *geometry.Pattern {
		return &geometry.Pattern{Kind: "linear", Count: n, Offset: []float64{4, 0, 0}}
	}
	doc := geometry.Document{Name: "panels", Units: "mm", Root: "car",
		Definitions: []geometry.Part{rivet},
		Assemblies: []geometry.Assembly{
			{ID: "car", Children: []geometry.Child{
				{ID: "big", Ref: "row", Pattern: &geometry.Pattern{Kind: "linear", Count: 200, Offset: []float64{0, 10, 0}}},
				{ID: "mid", Ref: "row", Position: []float64{0, 0, 100}, Pattern: &geometry.Pattern{Kind: "linear", Count: 9, Offset: []float64{0, 10, 0}}},
				{ID: "few", Ref: "rivet", Position: []float64{0, 0, 200}, Pattern: line(10)},
			}},
			{ID: "row", Children: []geometry.Child{{ID: "rivet", Ref: "rivet", Pattern: line(512)}}},
		}}
	if doc.ViewportRefusal() == "" || doc.BuildRefusal() == "" {
		t.Fatal("the fixture no longer places more than both ceilings")
	}

	_, err := subtreeMesh(context.Background(), nil, doc, "mm", "big")
	if err == nil || errs.CodeOf(err) != errs.CodeValidationFailed ||
		!strings.Contains(errs.DetailOf(err), "big") || !strings.Contains(errs.DetailOf(err), "100000") {
		t.Errorf("a subtree of 102,400 parts was not refused naming itself and the viewport's ceiling: %v", err)
	}
	for path, n := range map[string]int{"mid": 9 * 512, "few": 10, "big-3": 512} {
		body, err := subtreeMesh(context.Background(), nil, doc, "mm", path)
		if err != nil {
			t.Fatalf("%s places %d parts and was refused because the design is large: %v", path, n, err)
		}
		if _, insts := payloadOf(t, body); len(insts) != n || body["source"] != sourceGo {
			t.Errorf("%s: %d instances from %v, want %d", path, len(insts), body["source"], n)
		}
	}

	if s, _ := subtreeSource(true, geometry.MaxBuiltParts()); s != sourceKernel {
		t.Errorf("a subtree of exactly %d parts is not built by the kernel", geometry.MaxBuiltParts())
	}
	if s, note := subtreeSource(true, geometry.MaxBuiltParts()+1); s != sourceGo || !strings.Contains(note, "8192") {
		t.Errorf("a subtree one past the kernel's ceiling is sent to it (%s: %s)", s, note)
	}
	if s, note := subtreeSource(false, 3); s != sourceGo || !strings.Contains(note, "no CAD kernel") {
		t.Errorf("with no kernel the subtree says %s: %s", s, note)
	}
}
