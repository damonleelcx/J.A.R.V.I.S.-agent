package geometry

import (
	"fmt"
	"math"
	"testing"
)

// Looks integration, follow-up 3 (2026-09-19): the car's stations bow.
//
// PR 155 left stationCorner.Bulge at zero and refused anything else until PR 154's
// "via" landed. Every station now draws each flank as ONE exact circular arc from the
// sill to the shoulder (a via on the shoulder point and on the sill point), bowed so
// the arc's outermost point is (1 - carFenderFlare) of the station's half width
// between the wheels and the whole half width over them — a fender, never wider than
// the car was asked to be. This fence reads it off the written-out document: the
// section outlines every reader (kernel, Go measurement, browser) builds from.
func TestCar_TheFlanksAreExactArcsThatFlareOverTheWheels(t *testing.T) {
	for name, size := range CarFixturesForTest() {
		t.Run(name, func(t *testing.T) {
			d, _ := expandedCar(t, size)
			flanks(t, d, size["width"])
			// A respec moves the vias with the corners they bow between: they are
			// bound, not numbers, so the car is still inside its width afterwards.
			respec, problems := d.WithParameters(map[string]float64{"gt_wheelbase": size["wheelbase"] * 1.05})
			if anyError(problems) {
				t.Fatalf("respec: %v", problems)
			}
			flanks(t, *respec, size["width"])
		})
	}
}

func flanks(t *testing.T, d Document, width float64) {
	t.Helper()
	value := map[string]float64{}
	for _, p := range d.Parameters {
		value[p.Name] = p.Value
	}
	L, FO, WB := value["gt_length"], value["gt_front_overhang"], value["gt_wheelbase"]
	if L == 0 || WB == 0 || FO == 0 {
		t.Fatalf("no length, wheelbase or front overhang among the parameters: %v", value)
	}
	nearest := func(at float64) int { return int(math.Round(at * float64(carStations-1))) }
	wheels := map[int]bool{nearest(FO / L): true, nearest((FO + WB) / L): true}
	waist := nearest((FO + WB/2) / L)
	for i := 0; i < carStations; i++ {
		var st *Part
		for k := range d.Definitions {
			if d.Definitions[k].ID == fmt.Sprintf("gt-station-%d", i) {
				st = &d.Definitions[k]
			}
		}
		if st == nil {
			t.Fatalf("no station %d", i)
		}
		p := st.Profile
		var vias []int
		for k, pt := range p {
			if pt.Via == nil {
				continue
			}
			vias = append(vias, k)
			prev := (k + len(p) - 1) % len(p)
			if pt.Radius != 0 || p[prev].Radius != 0 || pt.RadiusFrom != "" || p[prev].RadiusFrom != "" {
				t.Errorf("station %d: a bowed flank ends on a rounded corner, which the outline ignores", i)
			}
			if pt.Via.XFrom == "" || pt.Via.YFrom == "" {
				t.Errorf("station %d: the via on point %d is a number, not bound to the car's parameters", i, k+1)
			}
		}
		if len(vias) != 2 || len(p) != 6 {
			t.Fatalf("station %d: %d points and vias on %v; a station is six corners with both flanks bowed",
				i, len(p), vias)
		}
		// Point 1 is the right sill, 2 the right shoulder (its via bows 1→2); point 0
		// the left sill, bowed from point 5, the left shoulder.
		hw := -p[0].X / 0.85
		right := arcThroughReachX(p[1].X, p[1].Y, p[2].Via.X, p[2].Via.Y, p[2].X, p[2].Y)
		if math.Abs(p[0].Via.X+p[2].Via.X) > 1e-6 || math.Abs(p[0].Via.Y-p[2].Via.Y) > 1e-6 {
			t.Errorf("station %d: the flanks are not mirror images: vias %+v and %+v", i, *p[0].Via, *p[2].Via)
		}
		if right > width/2+1e-6 {
			t.Errorf("station %d: a flank reaches %.1f from the centreline; the car is %.0f wide", i, right, width)
		}
		if wheels[i] && right/hw < 0.995 {
			t.Errorf("station %d is over a wheel and its flank reaches %.4f of its half width; the fender "+
				"flares to the whole of it", i, right/hw)
		}
		if i == waist && right/hw > 0.98 {
			t.Errorf("station %d is between the wheels and its flank reaches %.4f of its half width; the "+
				"waist is %.3f", i, right/hw, 1-carFenderFlare)
		}
		if right <= math.Max(p[1].X, p[2].X)*1.01 {
			t.Errorf("station %d: the flank barely bows (%.1f against corners at %.1f and %.1f)", i, right, p[1].X, p[2].X)
		}
	}
}
