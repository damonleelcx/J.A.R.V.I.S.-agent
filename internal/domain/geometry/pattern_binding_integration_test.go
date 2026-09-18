package geometry_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
)

// ‼️ Through the real database: a pattern whose step and angle are bound is stored with
// its bindings, read back as the same bytes, and a respec of the stored copy moves its
// copies — twice, so the variant a respec stores is itself re-specifiable (2026-09-17,
// bound patterns).
func TestRespec_ABoundPatternMovesAndItsBindingSurvivesStorage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	n := h.proposal("plate")
	plate := boundPlate()
	n.Document.Parts = nil
	n.Document.Parameters, n.Document.Definitions = plate.Parameters, plate.Definitions
	n.Document.Assemblies, n.Document.Root = plate.Assemblies, plate.Root
	first, err := h.svc.Save(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	read, err := h.svc.Find(ctx, first.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(n.Document)
	if got, _ := json.Marshal(read.Document); string(got) != string(want) {
		t.Fatalf("the bound patterns changed in storage.\n want: %s\n  got: %s", want, got)
	}

	next, caveats, err := h.svc.Respec(ctx, read.VersionID, h.userID, map[string]float64{"bolt_pitch": 40, "fan_sweep": 120})
	if err != nil {
		t.Fatal(err)
	}
	if len(caveats) != 0 {
		t.Fatalf("the respec reported: %+v", caveats)
	}
	placed := placedPositions(&next.Document)
	if !sameXYZ(placed["bolt-4"], 0, 0, 120) {
		t.Errorf("bolt-4 is at %v after bolt_pitch = 40; want (0, 0, 120)", placed["bolt-4"])
	}
	if a := sweptBetween(placed["blade-1"], placed["blade-3"]); math.Abs(a-120) > 1e-9 {
		t.Errorf("the fan sweeps %v degrees after fan_sweep = 120", a)
	}

	stored, err := h.svc.Find(ctx, next.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	p := childOf(&stored.Document, "plate", "bolt").Pattern
	if p == nil || p.OffsetFrom["z"] != "bolt_pitch" {
		t.Fatalf("the re-specified variant lost the pattern's binding: %+v", p)
	}
	if b := childOf(&stored.Document, "plate", "blade").Pattern; b == nil || b.AngleFrom != "fan_sweep" {
		t.Fatalf("the re-specified variant lost the polar pattern's binding: %+v", b)
	}
	again, _, err := h.svc.Respec(ctx, stored.VersionID, h.userID, map[string]float64{"bolt_pitch": 15, "row_pitch": 9})
	if err != nil {
		t.Fatal(err)
	}
	placed = placedPositions(&again.Document)
	if !sameXYZ(placed["bolt-4"], 0, 0, 45) {
		t.Errorf("bolt-4 is at %v after a second respec to bolt_pitch = 15; want (0, 0, 45)", placed["bolt-4"])
	}
	if !sameXYZ(placed["stud-3"], 0, 100, 9) {
		t.Errorf("stud-3 is at %v after row_pitch = 9; want (0, 100, 9)", placed["stud-3"])
	}
	if a := sweptBetween(placed["blade-1"], placed["blade-3"]); math.Abs(a-120) > 1e-9 {
		t.Errorf("the second respec lost the first one's sweep: %v degrees", a)
	}
}
