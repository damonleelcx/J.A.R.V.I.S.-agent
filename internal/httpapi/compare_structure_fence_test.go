package httpapi

import (
	"encoding/json"
	"slices"
	"sort"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
)

// The compare response, on the wire, for flat documents and for trees.
//
// Phase 7, stage E2 of docs/plan-2026-09-13-millions-of-parts.md. The handler reads
// Postgres, so these read comparisonBody, which is everything the handler writes.
// The workbench renders the response by field name, and a renamed field breaks the
// panel and nothing else.

func compareVariant(name string, units geometry.Unit, doc geometry.Document) geometry.Variant {
	doc.Name, doc.Units, doc.NotVerified = name, string(units), []string{"nothing checked"}
	return geometry.Variant{VersionID: "ver_" + name, ProjectID: "prj_1", Path: "geometry/bracket.forge.json",
		Version: 1, Name: name, Units: units, UnitsDeclared: string(units), Frame: geometry.FrameAssembly,
		Generator: "claude-opus-5", Verification: workspace.Unverified, Disposition: workspace.Pending,
		Document: doc}
}

// Pinned from the response as it was BEFORE trees could be compared (taken from
// kernel/interference-broad-phase, 42f42fa): a comparison of flat documents that
// exercises every part-row behaviour at once — a unit conversion, a difference, a
// part in one column only, a name match and a unit that cannot be converted.
const (
	flatCompareParts         = `[{"cells":[{"dimensions":"60 mm × 3 mm × ?","position":"(0 mm, 0 mm, 0 mm)","present":true,"shape":"box"},{"dimensions":"7.2 cm × 3 cm × ?","position":"(0 cm, 0.8 cm, 0 cm)","present":true,"shape":"box"},{"dimensions":"60 (unit not stated) × 3 (unit not stated) × ?","position":"(0 (unit not stated), 0 (unit not stated), 0 (unit not stated))","present":true,"shape":"box"}],"differences":["position Y (up): 0 mm in column 1, 0.8 cm in column 2","height: 3 mm in column 1, 3 cm in column 2","width: 60 mm in column 1, 7.2 cm in column 2"],"differs":true,"label":"Base plate","matched_by":"id","missing_from":[],"part_id":"plate"},{"cells":[{"dimensions":"5 mm × 3 mm × ?","position":"(0 mm, 3 mm, 0 mm)","present":true,"shape":"box"},{"dimensions":"","position":"","present":false,"shape":""},{"dimensions":"","position":"","present":false,"shape":""}],"differences":[],"differs":true,"label":"Rib","matched_by":"none","missing_from":[2,3],"part_id":"rib"},{"cells":[{"dimensions":"42 mm × 3 mm × ?","position":"(0 mm, 10 mm, 0 mm)","present":true,"shape":"box"},{"dimensions":"4.2 cm × 3 cm × ?","position":"(0 cm, 1 cm, 0 cm)","present":true,"shape":"box"},{"dimensions":"","position":"","present":false,"shape":""}],"differences":["height: 3 mm in column 1, 3 cm in column 2"],"differs":true,"label":"NEMA 17 Motor","matched_by":"name","missing_from":[3],"part_id":"motor-a"}]`
	flatCompareNotComparable = `["Base plate could not be compared between columns 1 and 3: one of them has no unit FORGE can convert, so its numbers mean no particular length."]`
	flatCompareMatchNotes    = `["NEMA 17 Motor: matched BY NAME rather than by identity — \"motor-a\" in column 1 and \"motor\" in column 2. FORGE renamed the part between proposals, so this row assumes they are the same part."]`
)

func TestCompare_AFlatComparisonComesBackExactlyAsItDidBeforeTreesCouldBeCompared(t *testing.T) {
	plate := func(id, name string, w, y float64) geometry.Part {
		return geometry.Part{ID: id, Name: name, Shape: "box", Size: map[string]float64{"width": w, "height": 3},
			Position: []float64{0, y, 0}, Rotation: []float64{0, 0, 0}}
	}
	flat := func(parts ...geometry.Part) geometry.Document { return geometry.Document{Parts: parts} }
	body := comparisonBody(geometry.Compare([]geometry.Variant{
		compareVariant("a", geometry.Millimetre, flat(plate("plate", "Base plate", 60, 0), plate("rib", "Rib", 5, 3),
			plate("motor-a", "NEMA 17 Motor", 42, 10))),
		compareVariant("b", geometry.Centimetre, flat(plate("plate", "Base plate", 7.2, 0.8),
			plate("motor", "NEMA 17 Motor", 4.2, 1))),
		compareVariant("c", geometry.UnitUnspecified, flat(plate("plate", "Base plate", 60, 0))),
	}))

	var keys []string
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"match_notes", "not_comparable", "parts", "project_id", "provenance", "variants"}; !slices.Equal(keys, want) {
		t.Fatalf("a comparison of flat documents carries %v; before trees it carried exactly %v", keys, want)
	}
	for key, want := range map[string]string{
		"parts": flatCompareParts, "not_comparable": flatCompareNotComparable, "match_notes": flatCompareMatchNotes,
	} {
		got, err := json.Marshal(body[key])
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s changed for flat documents.\n want: %s\n  got: %s", key, want, got)
		}
	}
}

type wireMember struct {
	ID          string   `json:"id"`
	MissingFrom []int    `json:"missing_from"`
	Changed     []string `json:"changed"`
	Differs     bool     `json:"differs"`
}

type wireStructure struct {
	Root struct {
		Values  []string `json:"values"`
		Differs bool     `json:"differs"`
	} `json:"root"`
	Definitions []struct {
		ID          string   `json:"id"`
		Label       string   `json:"label"`
		Occurrences []int    `json:"occurrences"`
		MissingFrom []int    `json:"missing_from"`
		Changed     []string `json:"changed"`
		Differs     bool     `json:"differs"`
	} `json:"definitions"`
	Assemblies []struct {
		ID         string       `json:"id"`
		Differs    bool         `json:"differs"`
		Children   []wireMember `json:"children"`
		Interfaces []wireMember `json:"interfaces"`
		Features   []wireMember `json:"features"`
	} `json:"assemblies"`
}

// Two trees: the wheel, placed four times, grows; the hub moves; a pattern gains a
// copy; a feature is added. Every section arrives, by the names the workbench reads.
func TestCompare_TheResponseCarriesTheStructure(t *testing.T) {
	car := func(radius, hubZ float64, bolts int, features ...geometry.Feature) geometry.Document {
		return geometry.Document{
			Definitions: []geometry.Part{
				{ID: "wheel", Name: "Wheel", Shape: "cylinder", Size: map[string]float64{"radius": radius, "height": 30}},
				{ID: "bolt", Shape: "cylinder", Size: map[string]float64{"radius": 2, "height": 12}},
			},
			Assemblies: []geometry.Assembly{
				{ID: "car", Children: []geometry.Child{
					{ID: "fl", Ref: "corner", Position: []float64{1200, 0, 800}},
					{ID: "fr", Ref: "corner", Position: []float64{1200, 0, -800}},
					{ID: "rl", Ref: "corner", Position: []float64{-1200, 0, 800}},
					{ID: "rr", Ref: "corner", Position: []float64{-1200, 0, -800}},
				}},
				{ID: "corner", Interfaces: []geometry.Interface{{ID: "hub", Position: []float64{0, 0, hubZ}}},
					Features: features,
					Children: []geometry.Child{
						{ID: "wheel", Ref: "wheel", At: "hub"},
						{ID: "bolt", Ref: "bolt", At: "hub", Position: []float64{50, 0, 0},
							Pattern: &geometry.Pattern{Kind: "polar", About: "z", Count: bolts}},
					}},
			},
			Root: "car",
		}
	}
	body := comparisonBody(geometry.Compare([]geometry.Variant{
		compareVariant("a", geometry.Millimetre, car(300, 40, 5)),
		compareVariant("b", geometry.Millimetre, car(320, 55, 6,
			geometry.Feature{ID: "weld", Op: "fuse", Of: "wheel", With: []string{"bolt"}})),
	}))
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Structure *wireStructure `json:"structure"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	s := wire.Structure
	if s == nil {
		t.Fatalf("two trees were compared and the response has no structure: %s", raw)
	}
	if !slices.Equal(s.Root.Values, []string{"car", "car"}) || s.Root.Differs {
		t.Errorf("root reads %+v", s.Root)
	}

	defs := map[string]int{}
	for i, d := range s.Definitions {
		defs[d.ID] = i
	}
	wheel := s.Definitions[defs["wheel"]]
	if !slices.Equal(wheel.Changed, []string{"size"}) || !slices.Equal(wheel.Occurrences, []int{4, 4}) || !wheel.Differs {
		t.Errorf("the wheel arrived as %+v; want one changed size, placed 4 and 4 times", wheel)
	}
	if bolt := s.Definitions[defs["bolt"]]; !slices.Equal(bolt.Occurrences, []int{20, 24}) {
		t.Errorf("the bolt arrived placed %v times; want 20 and 24", bolt.Occurrences)
	}

	var corner *struct {
		ID         string       `json:"id"`
		Differs    bool         `json:"differs"`
		Children   []wireMember `json:"children"`
		Interfaces []wireMember `json:"interfaces"`
		Features   []wireMember `json:"features"`
	}
	for i := range s.Assemblies {
		if s.Assemblies[i].ID == "corner" {
			corner = &s.Assemblies[i]
		}
	}
	if corner == nil || !corner.Differs {
		t.Fatalf("the corner did not arrive as changed: %s", raw)
	}
	find := func(what string, rows []wireMember, id string) wireMember {
		for _, m := range rows {
			if m.ID == id {
				return m
			}
		}
		t.Fatalf("no %s %q arrived on the corner: %+v", what, id, rows)
		return wireMember{}
	}
	if hub := find("interface", corner.Interfaces, "hub"); !slices.Equal(hub.Changed, []string{"position"}) {
		t.Errorf("the moved hub arrived as %+v", hub)
	}
	if bolt := find("child", corner.Children, "bolt"); !slices.Equal(bolt.Changed, []string{"pattern"}) {
		t.Errorf("the bolt child arrived as %+v", bolt)
	}
	if weld := find("feature", corner.Features, "weld"); !slices.Equal(weld.MissingFrom, []int{1}) || !weld.Differs {
		t.Errorf("the added weld arrived as %+v", weld)
	}
}
