package geometry

import (
	"strings"
	"testing"
)

// A prototype_edit may name a design by a placed path, and every edit says which
// placed parts it reached. Phase 7, stage E1 of
// docs/plan-2026-09-13-millions-of-parts.md; the tree is axleTree
// (edit_tree_test.go): a wheel of a hub and six patterned spokes, placed twice on
// an axle, the right one mirrored.

func problemText(problems []Problem) string {
	var b strings.Builder
	for _, p := range problems {
		b.WriteString(p.Detail + "\n")
	}
	return b.String()
}

// The hub is placed once in the wheel, and the wheel twice on the axle: patching
// it reaches both hubs, and neither the spokes beside them nor the bar.
func TestEdit_ADefinitionInASubAssemblyPlacedTwiceReportsEveryOccurrenceAndNothingElse(t *testing.T) {
	for _, name := range []string{"hub", "right-wheel/hub"} {
		t.Run(name, func(t *testing.T) {
			base := axleTree()
			hub := base.Definitions[0]
			hub.ID = name
			hub.Size = map[string]float64{"radius": 25, "height": 10}
			out, reached, problems := Edit{Patch: &Document{Definitions: []Part{hub}}}.ApplyAndReport(base)
			if len(problems) != 0 {
				t.Fatalf("problems: %s", problemText(problems))
			}
			if len(reached) != 1 {
				t.Fatalf("one definition was patched and the report has %d entries: %+v", len(reached), reached)
			}
			r := reached[0]
			if r.Kind != "definition" || r.ID != "hub" || r.Removed {
				t.Errorf("reported %+v; want the definition hub, patched", r)
			}
			if got := strings.Join(r.Occurrences, ","); got != "left-wheel/hub,right-wheel/hub" {
				t.Errorf("patching the hub reached %q; want both hubs and nothing else", got)
			}
			// Every reported id is a part the reader draws, and it drew the change.
			drawn := map[string]Part{}
			for _, p := range out.Expanded().Parts {
				drawn[p.ID] = p
			}
			for _, id := range r.Occurrences {
				if drawn[id].Size["radius"] != 25 {
					t.Errorf("%s is reported as reached but is drawn with radius %v", id, drawn[id].Size["radius"])
				}
			}
		})
	}
}

// An assembly named by one of its placements is the assembly: patched whole, it
// reaches every part beneath every placement of it, and not the bar beside them.
func TestEdit_AnAssemblyNamedByAPlacementReachesEveryPartBeneathEveryPlacement(t *testing.T) {
	base := axleTree()
	wheel := base.Assemblies[0]
	wheel.ID = "left-wheel"
	wheel.Children = append([]Child(nil), wheel.Children...)
	wheel.Children[1].Pattern = &Pattern{Kind: "polar", Count: 4, About: "y"}
	out, reached, problems := Edit{Patch: &Document{Assemblies: []Assembly{wheel}}}.ApplyAndReport(base)
	if len(problems) != 0 {
		t.Fatalf("problems: %s", problemText(problems))
	}
	if len(out.Assemblies) != 2 || out.Assemblies[0].ID != "wheel" {
		t.Fatalf("the path did not replace the wheel assembly: %+v", out.Assemblies)
	}
	if len(reached) != 1 || reached[0].ID != "wheel" || reached[0].Path != "left-wheel" {
		t.Fatalf("reported %+v; want the assembly wheel, named by left-wheel", reached)
	}
	// Both wheels as they were (a hub and six spokes each), in the order drawn.
	got := reached[0].Occurrences
	if len(got) != 14 {
		t.Errorf("reached %d parts; the two wheels had 14: %v", len(got), got)
	}
	for _, id := range got {
		if !strings.HasPrefix(id, "left-wheel/") && !strings.HasPrefix(id, "right-wheel/") {
			t.Errorf("reached %s, which is not beneath a wheel", id)
		}
	}
}

// A path to one patterned copy names the definition every copy follows. The
// change is made to the definition, and the report names all twelve spokes.
func TestEdit_APathToAPatternCopyChangesTheDefinitionAndReportsEveryCopy(t *testing.T) {
	base := axleTree()
	spoke := base.Definitions[1]
	spoke.ID = "left-wheel/spoke-3"
	spoke.Size = map[string]float64{"width": 4, "height": 4, "depth": 80}
	out, reached, problems := Edit{Patch: &Document{Definitions: []Part{spoke}}}.ApplyAndReport(base)
	if len(problems) != 0 {
		t.Fatalf("problems: %s", problemText(problems))
	}
	if len(out.Definitions) != 3 {
		t.Errorf("the design has %d definitions after patching one; a path must not add a fork of it", len(out.Definitions))
	}
	for _, d := range out.Definitions {
		if strings.Contains(d.ID, PathSeparator) {
			t.Errorf("the patch added a definition called %q instead of changing the spoke", d.ID)
		}
	}
	spokes := 0
	for _, p := range out.PlacedParts() {
		if strings.Contains(p.ID, "/spoke-") {
			spokes++
			if p.Size["depth"] != 80 {
				t.Errorf("%s is %v deep; every copy follows the definition the path named", p.ID, p.Size["depth"])
			}
		}
	}
	if spokes != 12 {
		t.Fatalf("placed %d spokes, want 12", spokes)
	}
	if len(reached) != 1 {
		t.Fatalf("report %+v", reached)
	}
	r := reached[0]
	if r.ID != "spoke" || r.Path != "left-wheel/spoke-3" {
		t.Errorf("reported %+v; want the definition spoke, named by left-wheel/spoke-3", r)
	}
	want := []string{}
	for _, wheel := range []string{"left-wheel", "right-wheel"} {
		for _, n := range []string{"1", "2", "3", "4", "5", "6"} {
			want = append(want, wheel+"/spoke-"+n)
		}
	}
	if strings.Join(r.Occurrences, ",") != strings.Join(want, ",") {
		t.Errorf("reached %v; want every spoke on both wheels: %v", r.Occurrences, want)
	}
	if base.Definitions[1].Size["depth"] != 60 {
		t.Error("the edit changed the base's spoke")
	}
}

// A definition's own repeat copy is a path too, and removing a design by a path
// reports what is gone.
func TestEdit_APathToARepeatCopyNamesTheDefinitionAndARemovalReportsWhatWent(t *testing.T) {
	base := Document{Name: "rail", Units: "mm", Root: "rail",
		Definitions: []Part{
			{ID: "rivet", Shape: "cylinder", Size: map[string]float64{"radius": 2, "height": 4},
				Repeat: &Repeat{Count: 3, Offset: []float64{10, 0, 0}}},
			{ID: "plate", Shape: "box", Size: map[string]float64{"width": 40, "height": 2, "depth": 10}},
		},
		Assemblies: []Assembly{{ID: "rail", Children: []Child{
			{ID: "a", Ref: "rivet"}, {ID: "b", Ref: "rivet", Mirror: "z"}, {ID: "plate", Ref: "plate"},
		}}},
	}
	out, reached, problems := Edit{Remove: Removals{Definitions: []string{"b-2"}}}.ApplyAndReport(base)
	if len(problems) != 0 {
		t.Fatalf("problems: %s", problemText(problems))
	}
	if len(out.Definitions) != 1 || out.Definitions[0].ID != "plate" {
		t.Errorf("removing b-2 left definitions %+v; want the rivet gone", out.Definitions)
	}
	if len(reached) != 1 || reached[0].ID != "rivet" || !reached[0].Removed || reached[0].Path != "b-2" {
		t.Fatalf("reported %+v; want the definition rivet, removed, named by b-2", reached)
	}
	if got := strings.Join(reached[0].Occurrences, ","); got != "a-1,a-2,a-3,b-1,b-2,b-3" {
		t.Errorf("removing the rivet reported %q; want every rivet that went, and not the plate", got)
	}
}

// Removing one child reports it in every placement of its assembly.
func TestEdit_RemovingAChildReportsItInEveryPlacementOfItsAssembly(t *testing.T) {
	_, reached, problems := Edit{Remove: Removals{Children: []string{"wheel/hub"}}}.ApplyAndReport(axleTree())
	if len(problems) != 0 {
		t.Fatalf("problems: %s", problemText(problems))
	}
	if len(reached) != 1 || reached[0].Kind != "child" || !reached[0].Removed ||
		strings.Join(reached[0].Occurrences, ",") != "left-wheel/hub,right-wheel/hub" {
		t.Errorf("reported %+v; want the child wheel/hub, gone from both wheels", reached)
	}
}

// A path that places nothing, a path that places the other kind of design, and a
// path two placements write out are refused, each by the name it was given.
// Nothing is added: a patch named by a path that resolves to nothing must not
// become a definition with a slash in its id.
func TestEdit_RefusesAPathThatPlacesNothingOrIsAmbiguousByName(t *testing.T) {
	base := axleTree()
	out, _, problems := Edit{
		Remove: Removals{Definitions: []string{"nowhere/hub"}, Assemblies: []string{"left-wheel/hub"}},
		Patch: &Document{
			Definitions: []Part{{ID: "left-wheel/ghost", Shape: "box"}},
			Assemblies:  []Assembly{{ID: "left-wheel/spoke-2", Children: []Child{{ID: "x", Ref: "hub"}}}},
		},
	}.ApplyAndReport(base)
	joined := problemText(problems)
	for _, want := range []string{
		`cannot remove definition "nowhere/hub": nothing is placed at that path`,
		`cannot remove assembly "left-wheel/hub": that path places the definition "hub"`,
		`cannot patch definition "left-wheel/ghost": nothing is placed at that path`,
		`cannot patch assembly "left-wheel/spoke-2": that path places the definition "spoke"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("want a problem containing %q, got:\n%s", want, joined)
		}
	}
	for _, d := range out.Definitions {
		if strings.Contains(d.ID, PathSeparator) {
			t.Errorf("a refused path was added as the definition %q", d.ID)
		}
	}
	for _, a := range out.Assemblies {
		if strings.Contains(a.ID, PathSeparator) {
			t.Errorf("a refused path was added as the assembly %q", a.ID)
		}
	}

	// "bolt" patterned three times writes "bolt-2"; so does the sibling child
	// "bolt-2", which places a washer.
	clash := Document{Name: "flange", Units: "mm", Root: "flange",
		Definitions: []Part{{ID: "bolt", Shape: "cylinder"}, {ID: "washer", Shape: "cylinder"}},
		Assemblies: []Assembly{{ID: "flange", Children: []Child{
			{ID: "bolt", Ref: "bolt", Pattern: &Pattern{Kind: "linear", Count: 3, Offset: []float64{10, 0, 0}}},
			{ID: "bolt-2", Ref: "washer"},
		}}},
	}
	_, _, problems = Edit{Patch: &Document{Definitions: []Part{{ID: "bolt-2", Shape: "box"}}}}.ApplyAndReport(clash)
	if !strings.Contains(problemText(problems), `cannot patch definition "bolt-2": more than one placement is written out as "bolt-2"`) {
		t.Errorf("an ambiguous path was not refused by name:\n%s", problemText(problems))
	}
}

// A flat document has no tree: an edit reports exactly the parts it named, a
// repeated one as the copies drawn, and nothing it did not touch.
func TestEdit_AFlatDocumentEditReportsExactlyThePartsItEdited(t *testing.T) {
	base := Document{Name: "bracket", Units: "mm", Parts: []Part{
		{ID: "plate", Shape: "box", Size: map[string]float64{"width": 60, "height": 6, "depth": 60}},
		{ID: "rib", Shape: "box", Size: map[string]float64{"width": 4, "height": 20, "depth": 60},
			Repeat: &Repeat{Count: 3, Offset: []float64{20, 0, 0}}},
		{ID: "bolt", Shape: "cylinder", Size: map[string]float64{"radius": 3, "height": 10},
			Repeat: &Repeat{Count: 4, Offset: []float64{0, 0, 15}}},
		{ID: "tab", Shape: "box"},
	}}
	rib := base.Parts[1]
	rib.Size = map[string]float64{"width": 5, "height": 20, "depth": 60}
	_, reached, problems := Edit{
		Remove: Removals{Parts: []string{"tab"}},
		Patch:  &Document{Parts: []Part{rib}},
	}.ApplyAndReport(base)
	if len(problems) != 0 {
		t.Fatalf("problems: %s", problemText(problems))
	}
	var got []string
	for _, r := range reached {
		got = append(got, r.Kind+" "+r.ID+" removed="+map[bool]string{true: "yes", false: "no"}[r.Removed]+
			" ["+strings.Join(r.Occurrences, ",")+"]")
	}
	want := []string{"part tab removed=yes [tab]", "part rib removed=no [rib-1,rib-2,rib-3]"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("reported:\n%s\nwant exactly:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
