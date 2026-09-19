package cad_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Looks designed (damon's decision of 2026-09-18), kernel half: fillets that fail
// safely (B1), more edge rules (B2), shells and thickened skins (B4). Every part
// here stays an exact OCCT solid that exports to STEP.

func buildLooks(t *testing.T, doc geometry.Document) (volume float64, failures, reductions []string, edges map[string]int) {
	t.Helper()
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	unit, ok := geometry.ParseUnit(doc.Units)
	if !ok {
		t.Fatalf("unit %q", doc.Units)
	}
	got, err := k.BuildDocument(ctx, doc, unit, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return got.Volume, got.FeatureFailures, got.FeatureReductions, got.FeatureEdges
}

// B1: a fillet OCCT refuses at the radius asked is built at half of it, and the
// reply says so — where, at what size, and against what was asked.
//
// A 60×6×60 plate's four vertical corners: R45 cannot go on all four (two arcs of
// 45 on a 60 mm side overlap), so every corner after the first gets 22.5.
func TestKernel_ARefusedFilletIsBuiltSmallerAndSaysSo(t *testing.T) {
	doc := geometry.Document{Name: "plate", Units: "mm", Parts: []geometry.Part{box("plate", 60, 6, 60, 0, 0, 0)},
		Features: []geometry.Feature{{ID: "corners", Op: "fillet", Of: "plate", Radius: 45, Edges: "vertical"}}}
	volume, failures, reductions, _ := buildLooks(t, doc)
	if len(failures) != 0 {
		t.Fatalf("the fillet was dropped whole rather than built smaller: %v", failures)
	}
	if len(reductions) != 1 {
		t.Fatalf("a fillet built smaller than asked was not reported: reductions=%v", reductions)
	}
	msg := reductions[0]
	t.Logf("reduction: %s", msg)
	for _, want := range []string{"corners:", "22.5 mm", "0.5×", "45 mm asked for", "near ("} {
		if !strings.Contains(msg, want) {
			t.Errorf("the reduction does not say %q: %s", want, msg)
		}
	}
	// The corners really are rounded: less than the square plate, by at least four
	// R22.5 corners' worth, (1 - π/4)·22.5²·6 each.
	corner := (1 - math.Pi/4) * 22.5 * 22.5 * 6
	if volume > 60*6*60-4*corner+1e-6 {
		t.Errorf("volume %.3f mm³: the corners were not rounded (square plate %d, four R22.5 corners %.3f)",
			volume, 60*6*60, 60*6*60-4*corner)
	}
}

// B1: one group of edges that takes no fillet at any retried size does not drop the
// rest. A 1 mm fin fused onto the plate: R10, R5 and R2.5 all fail on the fin's four
// vertical edges, and the plate's four corners still get R10 — measured exactly.
func TestKernel_OneEdgeGroupThatTakesNoFilletDoesNotDropTheRest(t *testing.T) {
	doc := geometry.Document{Name: "finned plate", Units: "mm",
		Parts: []geometry.Part{box("plate", 60, 6, 60, 0, 0, 0), box("fin", 1, 20, 30, 20, 13, 0)},
		Features: []geometry.Feature{
			{ID: "weld", Op: "fuse", Of: "plate", With: []string{"fin"}},
			{ID: "corners", Op: "fillet", Of: "plate", Radius: 10, Edges: "vertical"}}}
	volume, failures, reductions, edges := buildLooks(t, doc)
	if len(failures) != 0 {
		t.Fatalf("one fin too thin for the radius dropped every corner: %v", failures)
	}
	if edges["corners"] != 8 {
		t.Errorf("the vertical rule selected %d edges, want 8 (4 plate corners, 4 fin edges)", edges["corners"])
	}
	if len(reductions) != 1 || strings.Count(reductions[0], "left square") != 4 {
		t.Fatalf("the four fin edges left square were not each reported: %v", reductions)
	}
	t.Logf("reduction: %s", reductions[0])
	want := 60.0*6*60 + 1*20*30 - 4*(1-math.Pi/4)*10*10*6
	if math.Abs(volume-want) > 1e-3 {
		t.Errorf("volume %.4f mm³, want %.4f: the plate's four corners at the full R10 and the fin square", volume, want)
	}
}

// B1: when no group takes any retried size, the feature fails as before — named,
// with the largest radius that does build — and is not reported as reduced.
func TestKernel_AFilletNoRetryFitsStillFailsWithTheLargestThatDoes(t *testing.T) {
	doc := geometry.Document{Name: "plate", Units: "mm", Parts: []geometry.Part{box("plate", 60, 6, 60, 0, 0, 0)},
		Features: []geometry.Feature{{ID: "corners", Op: "fillet", Of: "plate", Radius: 400, Edges: "vertical"}}}
	_, failures, reductions, _ := buildLooks(t, doc)
	if len(failures) != 1 || len(reductions) != 0 {
		t.Fatalf("failures=%v reductions=%v; want one failure and no reduction", failures, reductions)
	}
	for _, want := range []string{"400, 200, 100 mm", "largest that DOES build"} {
		if !strings.Contains(failures[0], want) {
			t.Errorf("the failure does not say %q: %s", want, failures[0])
		}
	}
}

// B2: every edge rule, counted on solids whose edges can be counted by hand. The
// count is the kernel's own (feature_edges); a 0.2 mm chamfer is small enough to
// build on every selection.
func TestKernel_EveryEdgeRuleSelectsExactlyTheEdgesItNames(t *testing.T) {
	plateWithHoles := func() geometry.Document {
		d := geometry.Document{Name: "plate", Units: "mm", Parts: []geometry.Part{box("plate", 60, 6, 60, 0, 0, 0)}}
		var holes []string
		for i, xz := range [][2]float64{{-15, -15}, {15, -15}, {-15, 15}, {15, 15}} {
			id := fmt.Sprintf("hole-%d", i)
			holes = append(holes, id)
			d.Parts = append(d.Parts, geometry.Part{ID: id, Name: id, Shape: "cylinder",
				Size:     map[string]float64{"radius": 2, "height": 24},
				Position: []float64{xz[0], 0, xz[1]}, Rotation: []float64{0, 0, 0}})
		}
		d.Features = []geometry.Feature{{ID: "drill", Op: "cut", Of: "plate", With: holes}}
		return d
	}
	ribbed := func() geometry.Document {
		return geometry.Document{Name: "ribbed", Units: "mm",
			Parts:    []geometry.Part{box("plate", 60, 6, 60, 0, 0, 0), box("rib", 10, 20, 30, 0, 13, 0)},
			Features: []geometry.Feature{{ID: "weld", Op: "fuse", Of: "plate", With: []string{"rib"}}}}
	}
	angle := func() geometry.Document {
		return geometry.Document{Name: "angle", Units: "mm", Parts: []geometry.Part{{
			ID: "plate", Name: "Angle", Shape: "extrusion",
			Profile: []geometry.Point{{X: 0, Y: 0}, {X: 60, Y: 0}, {X: 60, Y: 10}, {X: 10, Y: 10},
				{X: 10, Y: 40}, {X: 0, Y: 40}},
			Size:     map[string]float64{"depth": 30},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}}}
	}
	plain := func() geometry.Document {
		return geometry.Document{Name: "block", Units: "mm", Parts: []geometry.Part{box("plate", 100, 20, 50, 0, 0, 0)}}
	}
	for _, tc := range []struct {
		name   string
		doc    func() geometry.Document
		rule   string
		length float64
		want   int
	}{
		// A 100×20×50 block: 12 edges, 4 of each length (100, 20, 50), all convex.
		{"block", plain, "all", 0, 12},
		{"block", plain, "vertical", 0, 4},
		{"block", plain, "top", 0, 4},
		{"block", plain, "convex", 0, 12},
		{"block", plain, "outer", 0, 12},
		{"block", plain, "longer", 30, 8},
		{"block", plain, "longer", 50, 4}, // strictly longer: the 50s are not
		// An L extruded 30 deep: 6 sides, 18 edges, one inside corner.
		{"angle", angle, "convex", 0, 17},
		{"angle", angle, "concave", 0, 1},
		// A plate with four through holes: 12 box edges, 8 hole rims, 4 seams.
		{"drilled plate", plateWithHoles, "all", 0, 24},
		{"drilled plate", plateWithHoles, "convex", 0, 20},
		{"drilled plate", plateWithHoles, "outer", 0, 12},
		{"drilled plate", plateWithHoles, "holes", 0, 8},
		// A rib fused on a plate: its footprint is 4 inside corners, and the seam.
		{"ribbed plate", ribbed, "concave", 0, 4},
		{"ribbed plate", ribbed, "joins", 0, 4},
		{"ribbed plate", ribbed, "convex", 0, 20},
	} {
		t.Run(tc.name+"/"+tc.rule, func(t *testing.T) {
			doc := tc.doc()
			doc.Features = append(doc.Features, geometry.Feature{ID: "edge", Op: "chamfer", Of: "plate",
				Radius: 0.2, Edges: tc.rule, EdgeLength: tc.length})
			_, failures, _, edges := buildLooks(t, doc)
			if got := edges["edge"]; got != tc.want {
				t.Errorf("%s on the %s selected %d edges, want %d", tc.rule, tc.name, got, tc.want)
			}
			if len(failures) != 0 {
				t.Errorf("failures: %v", failures)
			}
		})
	}
	// And a rule that selects nothing says so rather than doing nothing silently.
	doc := plain()
	doc.Features = []geometry.Feature{{ID: "edge", Op: "chamfer", Of: "plate", Radius: 0.2, Edges: "concave"}}
	_, failures, _, edges := buildLooks(t, doc)
	if edges["edge"] != 0 || len(failures) != 1 || !strings.Contains(failures[0], "selected no concave edges") {
		t.Errorf("concave edges on a block: edges=%v failures=%v", edges, failures)
	}
}

// B4: a shell and a thickened skin are exact solids, measured against the formula.
func TestKernel_AShellAndASkinHaveTheVolumeTheFormulaSays(t *testing.T) {
	cylinder := geometry.Part{ID: "cup", Name: "Cup", Shape: "cylinder",
		Size:     map[string]float64{"radius": 3, "height": 8},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
	skin := geometry.Part{ID: "skin", Name: "Skin", Shape: "plane",
		Size:     map[string]float64{"width": 50, "depth": 30},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
	for _, tc := range []struct {
		name string
		doc  geometry.Document
		want float64
	}{
		{"a box open at the top", geometry.Document{Name: "tray", Units: "mm", Parts: []geometry.Part{box("tray", 100, 60, 40, 0, 0, 0)},
			Features: []geometry.Feature{{ID: "hollow", Op: "shell", Of: "tray", Thickness: 5, Open: []string{"top"}}}},
			100*60*40 - 90*55*30},
		{"a box open at both ends", geometry.Document{Name: "duct", Units: "mm", Parts: []geometry.Part{box("duct", 100, 60, 40, 0, 0, 0)},
			Features: []geometry.Feature{{ID: "hollow", Op: "shell", Of: "duct", Thickness: 5, Open: []string{"top", "bottom"}}}},
			100*60*40 - 90*60*30},
		// Written in cm, so the wall is converted like every other length: R30 H80 mm, 4 mm wall.
		{"a cylinder open at the top, in cm", geometry.Document{Name: "cup", Units: "cm", Parts: []geometry.Part{cylinder},
			Features: []geometry.Feature{{ID: "hollow", Op: "shell", Of: "cup", Thickness: 0.4, Open: []string{"top"}}}},
			math.Pi*30*30*80 - math.Pi*26*26*76},
		{"a thickened plane", geometry.Document{Name: "panel", Units: "mm", Parts: []geometry.Part{skin},
			Features: []geometry.Feature{{ID: "skin", Op: "thicken", Of: "skin", Thickness: 4}}},
			50 * 30 * 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			volume, failures, _, _ := buildLooks(t, tc.doc)
			if len(failures) != 0 {
				t.Fatalf("failures: %v", failures)
			}
			if math.Abs(volume-tc.want) > 1e-6*tc.want {
				t.Errorf("volume %.4f mm³, want %.4f", volume, tc.want)
			}
		})
	}
	// And the open face is the one named: volume cannot tell top from bottom, the
	// centre of volume can. The tray's 90×55×30 cavity is open at +Y, so its centre
	// sits 2.5 mm up and the material's sits (0 − 148,500 × 2.5) / 91,500 below 0.
	{
		k := kernel(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		doc := geometry.Document{Name: "tray", Units: "mm", Parts: []geometry.Part{box("tray", 100, 60, 40, 0, 0, 0)},
			Features: []geometry.Feature{{ID: "hollow", Op: "shell", Of: "tray", Thickness: 5, Open: []string{"top"}}}}
		got, err := k.BuildProperties(ctx, doc, geometry.Millimetre)
		if err != nil || len(got.Properties) != 1 {
			t.Fatalf("properties: %v %+v", err, got)
		}
		want := -148500 * 2.5 / 91500
		if y := got.Properties[0].Centroid[1]; math.Abs(y-want) > 1e-6 {
			t.Errorf("the tray open at the top has its centre of volume at y=%.4f, want %.4f (open at the bottom "+
				"puts it at %+.4f)", y, want, -want)
		}
	}
	// A wall that does not fit is refused by name, and the part is left whole.
	doc := geometry.Document{Name: "tray", Units: "mm", Parts: []geometry.Part{box("tray", 100, 60, 40, 0, 0, 0)},
		Features: []geometry.Feature{{ID: "hollow", Op: "shell", Of: "tray", Thickness: 30, Open: []string{"top"}}}}
	volume, failures, _, _ := buildLooks(t, doc)
	if len(failures) != 1 || !strings.Contains(failures[0], "does not fit") || volume != 100*60*40 {
		t.Errorf("a 30 mm wall in a 40 mm box: volume=%g failures=%v", volume, failures)
	}
}

// B2 and B4: the kernel knows exactly the edge rules and open faces Go validates
// and the contract teaches. Read from sidecar.py's own text, so a rule added on one
// side only fails here rather than in a person's build.
func TestTheKernelSelectsEdgesByExactlyTheRulesGoValidates(t *testing.T) {
	raw, err := os.ReadFile("sidecar.py")
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout has CRLF line ends.
	src := []byte(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	keys := func(dict string) []string {
		t.Helper()
		body := regexp.MustCompile(`(?s)\n` + dict + ` = \{\n(.*?)\n\}`).FindSubmatch(src)
		if body == nil {
			t.Fatalf("sidecar.py has no %s dict", dict)
		}
		var out []string
		for _, m := range regexp.MustCompile(`(?m)^    "([a-z]+)":`).FindAllSubmatch(body[1], -1) {
			out = append(out, string(m[1]))
		}
		sort.Strings(out)
		return out
	}
	names := func(rules []geometry.EdgeRule) []string {
		var out []string
		for _, r := range rules {
			out = append(out, r.Name)
		}
		sort.Strings(out)
		return out
	}
	if got, want := keys("_EDGE_RULES"), names(geometry.EdgeRules); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the kernel's edge rules are %v and Go validates %v", got, want)
	}
	if got, want := keys("_OPEN_FACES"), names(geometry.OpenFaceRules); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the kernel's open faces are %v and Go validates %v", got, want)
	}
}
