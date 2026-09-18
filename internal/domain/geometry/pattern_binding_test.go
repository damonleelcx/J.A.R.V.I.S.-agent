package geometry_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Fences for a pattern's offsets and a polar pattern's angle bound to parameters
// (2026-09-17, bound patterns; pattern_binding.go). PR 112 bound positions and listed
// these as still typed as numbers.

// boundPlate is a plate carrying a row of bolts at bolt_pitch, a fan of blades over
// fan_sweep and a grid of studs at row_pitch by col_pitch, every step bound.
func boundPlate() *geometry.Document {
	return &geometry.Document{Name: "plate", Units: "mm", Root: "plate",
		Parameters: []geometry.Parameter{
			{Name: "bolt_pitch", Value: 30, Unit: "mm", How: geometry.Chosen},
			{Name: "fan_sweep", Value: 90, Unit: "deg", How: geometry.Chosen},
			{Name: "row_pitch", Value: 20, Unit: "mm", How: geometry.Chosen},
			{Name: "col_pitch", Value: 25, Unit: "mm", How: geometry.Chosen},
		},
		Definitions: []geometry.Part{
			{ID: "bolt", Shape: "cylinder", Size: map[string]float64{"radius": 3, "height": 10}},
		},
		Assemblies: []geometry.Assembly{{ID: "plate", Children: []geometry.Child{
			{ID: "bolt", Ref: "bolt", Pattern: &geometry.Pattern{Kind: "linear", Count: 4,
				Offset: []float64{0, 0, 30}, OffsetFrom: map[string]string{"z": "bolt_pitch"}}},
			{ID: "blade", Ref: "bolt", Position: []float64{50, 0, 0}, Pattern: &geometry.Pattern{Kind: "polar", Count: 3,
				About: "y", Angle: 90, AngleFrom: "fan_sweep"}},
			{ID: "stud", Ref: "bolt", Position: []float64{0, 100, 0}, Pattern: &geometry.Pattern{Kind: "grid", Rows: 2, Columns: 2,
				RowOffset: []float64{0, 0, 20}, RowOffsetFrom: map[string]string{"z": "row_pitch"},
				ColumnOffset: []float64{25, 0, 0}, ColumnOffsetFrom: map[string]string{"x": "col_pitch"}}},
		}}}}
}

// sweptBetween is the angle in degrees between two points round the y axis.
func sweptBetween(a, b []float64) float64 {
	cos := (a[0]*b[0] + a[2]*b[2]) / (math.Hypot(a[0], a[2]) * math.Hypot(b[0], b[2]))
	return math.Acos(math.Max(-1, math.Min(1, cos))) * 180 / math.Pi
}

// ‼️ A respec moves every copy of a pattern whose step or angle is bound to the
// parameters it changes, the stored numbers follow, and the source is left alone.
func TestBind_APatternsStepAndAngleFollowTheirParameters(t *testing.T) {
	d := boundPlate()
	if problems := d.Bind(); len(problems) != 0 {
		t.Fatalf("a document whose patterns agree with their bindings reported: %+v", problems)
	}
	got, problems := d.WithParameters(map[string]float64{"bolt_pitch": 40, "fan_sweep": 120, "row_pitch": 22, "col_pitch": 31})
	if len(problems) != 0 {
		t.Fatalf("the respec reported: %+v", problems)
	}
	placed := placedPositions(got)
	if !sameXYZ(placed["bolt-4"], 0, 0, 120) {
		t.Errorf("bolt-4 is at %v after bolt_pitch = 40; want (0, 0, 120)", placed["bolt-4"])
	}
	if a := sweptBetween(placed["blade-1"], placed["blade-3"]); math.Abs(a-120) > 1e-9 {
		t.Errorf("the fan sweeps %v degrees after fan_sweep = 120", a)
	}
	if !sameXYZ(placed["stud-4"], 31, 100, 22) {
		t.Errorf("stud-4 is at %v after row_pitch = 22 and col_pitch = 31; want (31, 100, 22)", placed["stud-4"])
	}
	if p := childOf(got, "plate", "bolt").Pattern; p.Offset[2] != 40 || p.OffsetFrom["z"] != "bolt_pitch" {
		t.Errorf("the stored linear pattern is %+v; want offset z 40 bound to bolt_pitch", p)
	}
	if p := childOf(got, "plate", "blade").Pattern; p.Angle != 120 || p.AngleFrom != "fan_sweep" {
		t.Errorf("the stored polar pattern is %+v; want angle 120 bound to fan_sweep", p)
	}
	if again := got.Bind(); len(again) != 0 {
		t.Errorf("the re-specified document disagrees with itself: %+v", again)
	}
	if b := placedPositions(d)["bolt-4"]; !sameXYZ(b, 0, 0, 90) {
		t.Errorf("the respec moved the document it came from: bolt-4 is at %v", b)
	}
	if a := sweptBetween(placedPositions(d)["blade-1"], placedPositions(d)["blade-3"]); math.Abs(a-90) > 1e-9 {
		t.Errorf("the respec turned the source's fan: it sweeps %v", a)
	}
}

// ‼️ A respec's copy shares no pattern slice or binding map with its source, and binding
// an edited model leaves the model it was made from byte-identical (PR 112's bug class).
func TestBind_APatternsBindingIsNotSharedWithItsSource(t *testing.T) {
	d := boundPlate()
	// And a pattern bound to nothing: Bind re-copies a bound pattern, so only an unbound
	// one shows whether the respec's own copy shares anything.
	d.Assemblies[0].Children = append(d.Assemblies[0].Children, geometry.Child{ID: "nut", Ref: "bolt",
		Position: []float64{0, -100, 0}, Pattern: &geometry.Pattern{Kind: "linear", Count: 2, Offset: []float64{7, 0, 0}}})
	d.Bind()
	before, _ := json.Marshal(d)
	v, _ := d.WithParameters(map[string]float64{"bolt_pitch": 35})
	for _, c := range v.Assemblies[0].Children {
		p := c.Pattern
		for _, m := range []map[string]string{p.OffsetFrom, p.RowOffsetFrom, p.ColumnOffsetFrom} {
			for k := range m {
				m[k] = "999"
			}
		}
		for _, s := range [][]float64{p.Offset, p.RowOffset, p.ColumnOffset} {
			for i := range s {
				s[i] = -1
			}
		}
		p.AngleFrom = "999"
	}
	if after, _ := json.Marshal(d); string(after) != string(before) {
		t.Errorf("writing into a respec's patterns rewrote its source.\n want %s\n  got %s", before, after)
	}

	out, problems := geometry.Edit{Patch: &geometry.Document{Parameters: []geometry.Parameter{
		{Name: "bolt_pitch", Value: 50, Unit: "mm", How: geometry.Chosen},
		{Name: "fan_sweep", Value: 60, Unit: "deg", How: geometry.Chosen},
	}}}.Apply(*d)
	if len(problems) != 0 {
		t.Fatalf("the edit reported: %+v", problems)
	}
	for _, p := range out.Bind() {
		if p.Severity == geometry.Error {
			t.Fatalf("binding the edited model refused %s: %s", p.Name, p.Detail)
		}
	}
	if after, _ := json.Marshal(d); string(after) != string(before) {
		t.Errorf("binding the edited model rewrote the model it was applied to.\n want %s\n  got %s", before, after)
	}
	if p := childOf(&out, "plate", "bolt").Pattern; p.Offset[2] != 50 {
		t.Errorf("the edited bolt pitch is %v; bound to bolt_pitch = 50", p.Offset[2])
	}
	if p := childOf(&out, "plate", "blade").Pattern; p.Angle != 60 {
		t.Errorf("the edited fan sweeps %v; bound to fan_sweep = 60", p.Angle)
	}
}

// The binding is stored: read back from its JSON it is the same bytes, and a respec of
// what was read moves the copies. A pattern with no binding stores as it always did.
func TestBind_ABoundPatternSurvivesStorageAndMovesWhenReadBack(t *testing.T) {
	d := boundPlate()
	d.Bind()
	body, _ := json.Marshal(d)
	for _, key := range []string{`"offset_from":{"z":"bolt_pitch"}`, `"angle_from":"fan_sweep"`,
		`"row_offset_from":{"z":"row_pitch"}`, `"column_offset_from":{"x":"col_pitch"}`} {
		if !strings.Contains(string(body), key) {
			t.Errorf("the stored document lacks %s: %s", key, body)
		}
	}
	var read geometry.Document
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatal(err)
	}
	if again, _ := json.Marshal(&read); string(again) != string(body) {
		t.Fatalf("the bound patterns changed in a round trip.\n want %s\n  got %s", body, again)
	}
	moved, _ := read.WithParameters(map[string]float64{"bolt_pitch": 10})
	if b := placedPositions(moved)["bolt-3"]; !sameXYZ(b, 0, 0, 20) {
		t.Errorf("bolt-3 read back and re-specified to bolt_pitch = 10 is at %v; want (0, 0, 20)", b)
	}

	plain := &geometry.Document{Name: "p", Units: "mm", Root: "a",
		Definitions: []geometry.Part{{ID: "b", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}}},
		Assemblies: []geometry.Assembly{{ID: "a", Children: []geometry.Child{{ID: "b", Ref: "b",
			Pattern: &geometry.Pattern{Kind: "linear", Count: 2, Offset: []float64{5, 0, 0}}}}}}}
	was, _ := json.Marshal(plain)
	plain.Bind()
	if now, _ := json.Marshal(plain); string(now) != string(was) || strings.Contains(string(now), "_from") {
		t.Errorf("an unbound pattern changed when bound.\n want %s\n  got %s", was, now)
	}
}

// A pattern binding that cannot be bound keeps its number and says which pattern.
func TestBind_APatternBindingThatCannotBeBoundKeepsItsNumberAndSaysWhich(t *testing.T) {
	d := boundPlate()
	d.Assemblies[0].Children[0].Pattern.OffsetFrom = map[string]string{"w": "bolt_pitch"}
	d.Assemblies[0].Children[1].Pattern.AngleFrom = "missing_sweep"
	var axis, angle bool
	for _, p := range d.Bind() {
		if p.Severity == geometry.Error && p.Name == "plate/bolt" && strings.Contains(p.Detail, `binds pattern offset "w", which is not an axis`) {
			axis = true
		}
		if p.Severity == geometry.Error && p.Name == "plate/blade" && strings.Contains(p.Detail, "pattern angle") &&
			strings.Contains(p.Detail, "missing_sweep") && strings.Contains(p.Detail, "on the placement was kept") {
			angle = true
		}
	}
	if !axis || !angle {
		t.Errorf("the broken pattern bindings were not named (offset %v, angle %v)", axis, angle)
	}
	if a := childOf(d, "plate", "blade").Pattern.Angle; a != 90 {
		t.Errorf("an angle whose binding does not evaluate became %v; its number is 90", a)
	}
}

// ‼️ The table the contract is rendered from is what the binder reads: every field it
// names round-trips under its JSON key, a pattern of its kind moves when only that field
// is bound and re-specified, and the guide names every field and its binding.
func TestPatternBinding_TheTableIsWhatTheBinderReads(t *testing.T) {
	guide := geometry.PatternBindingGuide()
	bindings := geometry.PatternBindings()
	if len(bindings) < 4 {
		t.Fatalf("the table names %d fields; offset, row_offset, column_offset and angle are bound", len(bindings))
	}
	for _, b := range bindings {
		if !strings.Contains(guide, `"`+b.Field+`" (`+b.Kind+`)`) || !strings.Contains(guide, `"`+b.From+`"`) {
			t.Errorf("the guide does not teach %s / %s:\n%s", b.Field, b.From, guide)
		}
		pattern := map[string]any{"kind": b.Kind, "count": 3, "rows": 2, "columns": 2, "about": "y",
			"offset": []any{0, 0, 7}, "row_offset": []any{0, 0, 7}, "column_offset": []any{0, 7, 0}}
		if b.Vector {
			pattern[b.Field], pattern[b.From] = []any{10, 0, 0}, map[string]any{"x": "step"}
		} else {
			pattern[b.Field], pattern[b.From] = 90, "step"
		}
		raw, _ := json.Marshal(map[string]any{"name": "t", "units": "mm", "root": "a",
			"parameters":  []any{map[string]any{"name": "step", "value": map[bool]int{true: 10, false: 90}[b.Vector], "unit": "mm", "how": "chosen"}},
			"definitions": []any{map[string]any{"id": "b", "shape": "box", "size": map[string]any{"width": 1, "height": 1, "depth": 1}}},
			"assemblies": []any{map[string]any{"id": "a", "children": []any{
				map[string]any{"id": "c", "ref": "b", "position": []any{50, 0, 0}, "pattern": pattern}}}}})
		var d geometry.Document
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if back, _ := json.Marshal(&d); !strings.Contains(string(back), `"`+b.From+`":`) {
			t.Errorf("%s does not survive as a JSON key: %s", b.From, back)
		}
		if problems := d.Bind(); len(problems) != 0 {
			t.Fatalf("%s: binding reported %+v", b.Field, problems)
		}
		moved, _ := d.WithParameters(map[string]float64{"step": map[bool]float64{true: 13, false: 120}[b.Vector]})
		last := map[bool]string{true: "c-4", false: "c-3"}[b.Kind == "grid"]
		if a, m := placedPositions(&d)[last], placedPositions(moved)[last]; a == nil || sameXYZ(m, a...) {
			t.Errorf("a %s pattern with only %s bound did not move when it was re-specified: %v then %v", b.Kind, b.From, a, m)
		}
	}
}
