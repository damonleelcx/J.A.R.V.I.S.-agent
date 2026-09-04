package geometry_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
)

// A pack's default unit is a unit this package can convert.
//
// # Why this fence lives HERE
//
// `pack.Definition.GeometryUnit` holds a plain string, because geometry imports
// pack and importing back would be a cycle. So nothing but this holds the table
// to the unit vocabulary — and a unit geometry cannot resolve is worse than no
// default at all: PRD WRK-05 exists because a dimension that travels without a
// convertible unit is one that gets read in the wrong one.
func TestPackGeometryUnitsAreConvertible(t *testing.T) {
	for _, d := range pack.All() {
		if d.GeometryUnit == "" {
			continue // legitimate: `general` and `software` imply no unit
		}
		if !geometry.Unit(d.GeometryUnit).Known() {
			t.Errorf("the %s pack defaults to unit %q and geometry cannot convert it.\n"+
				"A default nothing resolves is worse than none: it looks like a stated "+
				"unit and behaves like a missing one", d.Pack, d.GeometryUnit)
		}
	}
}

// Every physical domain states both a unit and the frame its coordinates assume.
//
// A position is meaningless without a frame, and the frames DIFFER: a vehicle is
// X-forward, a building is Z-up against a site datum. A coordinate read in the
// wrong frame is wrong without looking wrong, which is the failure mode that
// makes stating it worth a table column.
func TestEveryPhysicalPackStatesItsFrame(t *testing.T) {
	// Written out rather than derived: a fence that asks the table which packs
	// are physical would accept a table that had quietly decided none were.
	for _, name := range []string{
		"mechanical", "manufacturing", "automotive", "aerospace",
		"civil", "electrical", "construction", "product-design", "architecture",
	} {
		d, ok := pack.Lookup(name)
		if !ok {
			t.Fatalf("%s is not a pack", name)
		}
		if d.GeometryUnit == "" {
			t.Errorf("%s proposes physical geometry and states no default unit", name)
		}
		if strings.TrimSpace(d.GeometryAxes) == "" {
			t.Errorf("%s states no coordinate frame, so a position in it means nothing", name)
		}
	}
}
