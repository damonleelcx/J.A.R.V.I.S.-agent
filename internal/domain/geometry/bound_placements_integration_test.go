package geometry_test

import (
	"context"
	"encoding/json"
	"testing"
)

// ‼️ Through the real database: a child bound to a parameter is stored with its binding,
// read back as the same bytes, and a respec of the stored copy moves it — twice, so the
// variant a respec stores is itself re-specifiable (2026-09-15, bound child positions).
func TestRespec_ABoundChildMovesAndItsBindingSurvivesStorage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	n := h.proposal("car")
	car := boundCar()
	n.Document.Parts = nil
	n.Document.Parameters, n.Document.Definitions = car.Parameters, car.Definitions
	n.Document.Assemblies, n.Document.Root = car.Assemblies, car.Root
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
		t.Fatalf("the bound tree changed in storage.\n want: %s\n  got: %s", want, got)
	}

	next, caveats, err := h.svc.Respec(ctx, read.VersionID, h.userID, map[string]float64{"half_track": 900})
	if err != nil {
		t.Fatal(err)
	}
	if len(caveats) != 0 {
		t.Fatalf("the respec reported: %+v", caveats)
	}
	if w := placedPositions(&next.Document)["left/wheel"]; !sameXYZ(w, -1060, 330, 0) {
		t.Errorf("left/wheel is at %v after half_track = 900; want (-1060, 330, 0)", w)
	}

	stored, err := h.svc.Find(ctx, next.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := childOf(&stored.Document, "car", "left").PositionFrom["x"]; got != "-half_track" {
		t.Fatalf("the re-specified variant lost the child's binding: position_from.x = %q", got)
	}
	again, _, err := h.svc.Respec(ctx, stored.VersionID, h.userID, map[string]float64{"half_track": 700})
	if err != nil {
		t.Fatal(err)
	}
	if w := placedPositions(&again.Document)["right/wheel"]; !sameXYZ(w, 860, 330, 0) {
		t.Errorf("right/wheel is at %v after a second respec to half_track = 700; want (860, 330, 0)", w)
	}
}
