package geometry

import (
	"strings"
	"testing"
)

// The off-node STEP export job's ceiling (limits.go, MaxExportJobParts).
//
// Counted, like the building ceiling, so a million-part design is refused without
// placing one part. The job's ceiling sits between the two numbers that matter to
// a reader of it: above the 4,096 a request builds, and far below the 1,000,000 the
// owner decision keeps refused.

// repeated is parts × copies occurrences, placed by repeats rather than listed.
func repeated(parts, copies int) Document {
	d := Document{Name: "rivets", Units: "mm"}
	for i := 0; i < parts; i++ {
		d.Parts = append(d.Parts, Part{ID: "p" + strings.Repeat("x", i%3) + string(rune('a'+i%26)), Name: "Rivet",
			Shape: "box", Size: map[string]float64{"width": 4, "height": 4, "depth": 4},
			Position: []float64{0, float64(10 * i), 0},
			Repeat:   &Repeat{Count: copies, Offset: []float64{10, 0, 0}}})
	}
	return d
}

func TestExportJobRefusal_BuildsUpToNinetyThousandAndRefusesOneMillionNamingTheCeiling(t *testing.T) {
	if MaxExportJobParts != 90_000 {
		t.Fatalf("MaxExportJobParts = %d; it is set from measured numbers (limits.go), and changing it "+
			"changes this test deliberately or not at all", MaxExportJobParts)
	}

	at := repeated(225, 400) // 90,000
	if got := occurrences(at, MaxExportJobParts); got != 90_000 {
		t.Fatalf("the fixture places %d occurrences, want 90,000", got)
	}
	if r := at.ExportJobRefusal(); r != "" {
		t.Errorf("90,000 occurrences, the ceiling itself, was refused: %s", r)
	}
	// ‼️ And the request still refuses it: the job's ceiling is not the building one.
	if at.DrawRefusal() == "" {
		t.Error("90,000 occurrences passed the 4,096 building ceiling; the job's ceiling leaked into the request path")
	}

	for _, tc := range []struct {
		name          string
		parts, copies int
	}{
		{"one over", 226, 400},      // 90,400
		{"one million", 1954, 512},  // 1,000,448
		{"measured 300k", 591, 512}, // 302,592
	} {
		d := repeated(tc.parts, tc.copies)
		r := d.ExportJobRefusal()
		if r == "" {
			t.Errorf("%s: %d × %d occurrences was not refused", tc.name, tc.parts, tc.copies)
			continue
		}
		if !strings.Contains(r, "90000") {
			t.Errorf("%s: the refusal does not name the ceiling: %s", tc.name, r)
		}
		if !strings.Contains(r, "2 GiB") || !strings.Contains(r, "302,560") {
			t.Errorf("%s: the refusal does not say why, in the measured numbers: %s", tc.name, r)
		}
	}
}
