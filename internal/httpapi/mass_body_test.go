package httpapi

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The mass reply says when it claims no mass, and names what it rests on.
// Phase 5, stage V3.
func TestMassBody_SaysWhenNoMassIsClaimed(t *testing.T) {
	byVolume := massBody("v1", geometry.MassReport{Basis: geometry.MassByVolume,
		WithoutDensity: []string{"bracket", "gusset"}, Unmeasured: []string{"sketch"}}, []string{"Rib: nope"})
	note, _ := byVolume["note"].(string)
	if !strings.Contains(note, "no mass is claimed") || !strings.Contains(note, "2 part(s)") {
		t.Errorf("a roll-up weighed by volume said: %q", note)
	}
	if byVolume["basis"] != geometry.MassByVolume {
		t.Errorf("basis %v", byVolume["basis"])
	}
	if s, _ := byVolume["skipped"].([]string); len(s) != 1 {
		t.Errorf("the part the kernel could not build was not named: %v", byVolume["skipped"])
	}

	byMass := massBody("v1", geometry.MassReport{Basis: geometry.MassByDensity}, nil)
	if byMass["note"] != "" {
		t.Errorf("a roll-up with a density on every part carries a note: %v", byMass["note"])
	}
}
