package geometry_test

import (
	"math"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// boundBracket is the 2026-09-05 spike's bracket with its rib BOUND to the
// plate, which is the arrangement that survived the sweep.
func boundBracket() *geometry.Document {
	return &geometry.Document{
		Name: "NEMA 17 bracket", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_size", Value: 60, Unit: "mm", How: geometry.Chosen},
			{Name: "plate_thickness", Value: 6, Unit: "mm", How: geometry.Chosen},
			{Name: "fillet_radius", Value: 3, Unit: "mm", How: geometry.Chosen},
			{Name: "rib_width", Value: 6, Unit: "mm", How: geometry.Chosen},
		},
		Derived: []geometry.Derived{
			{Name: "rib_length", Expression: "plate_size - 2 * fillet_radius",
				Why: "a rib that does not follow the plate overhangs it when the plate shrinks"},
			{Name: "rib_offset", Expression: "plate_size / 4",
				Why: "the ribs stay quartered across the plate at any size"},
		},
		Parts: []geometry.Part{
			{
				ID: "base-plate", Name: "Plate", Shape: "box",
				Size:     map[string]float64{"width": 60, "height": 6, "depth": 60},
				SizeFrom: map[string]string{"width": "plate_size", "height": "plate_thickness", "depth": "plate_size"},
				Position: []float64{0, 0, 0},
			},
			{
				ID: "rib-front", Name: "Front rib", Shape: "box",
				Size:         map[string]float64{"width": 54, "height": 12, "depth": 6},
				SizeFrom:     map[string]string{"width": "rib_length", "depth": "rib_width"},
				Position:     []float64{0, 9, 15},
				PositionFrom: map[string]string{"z": "rib_offset"},
			},
		},
	}
}

// THE PHASE 2 RESULT, and the one the kernel spike measured by hand nine times.
//
// Premise A swept plate_size across a 3.4x range. With rib_length derived, all
// six sizes built a valid solid; with it fixed at 52 mm, plate_size=50 put the
// ribs over the edge of the plate and OCCT refused the fillet.
//
// This is that sweep, run through the document rather than through the kernel:
// change one parameter, and every bound dimension follows.
func TestWithParameters_TheRibFollowsThePlateAcrossTheSweep(t *testing.T) {
	base := boundBracket()

	for _, plate := range []float64{35, 42.3, 50, 60, 80, 120} {
		got, problems := base.WithParameters(map[string]float64{"plate_size": plate})
		for _, p := range problems {
			if p.Severity == geometry.Error {
				t.Fatalf("plate_size=%g: %s: %s", plate, p.Name, p.Detail)
			}
		}
		plateW := got.Parts[0].Size["width"]
		plateD := got.Parts[0].Size["depth"]
		ribW := got.Parts[1].Size["width"]
		ribZ := got.Parts[1].Position[2]

		if plateW != plate || plateD != plate {
			t.Errorf("plate_size=%g: the plate did not follow (%g x %g)", plate, plateW, plateD)
		}
		if want := plate - 6; math.Abs(ribW-want) > 1e-9 {
			t.Errorf("plate_size=%g: rib width = %g, want %g", plate, ribW, want)
		}
		if ribW > plateW {
			t.Errorf("plate_size=%g: the rib (%g) overhangs the plate (%g) — the sweep failure",
				plate, ribW, plateW)
		}
		if want := plate / 4; math.Abs(ribZ-want) > 1e-9 {
			t.Errorf("plate_size=%g: rib position z = %g, want %g", plate, ribZ, want)
		}
	}
}

// The counter-case, so the sweep above is known to be capable of failing.
//
// An UNBOUND rib keeps the number the model typed. At plate_size=50 the 54 mm
// rib hangs over the edge, exactly as the spike's 52 mm one did — and nothing
// about the document says so, which is why binding is the fix rather than a
// warning.
func TestWithParameters_AnUnboundRibOverhangsTheShrunkPlate(t *testing.T) {
	base := boundBracket()
	base.Parts[1].SizeFrom = nil // the rib is now a fixed number again

	got, _ := base.WithParameters(map[string]float64{"plate_size": 50})
	if got.Parts[1].Size["width"] <= got.Parts[0].Size["width"] {
		t.Fatal("an unbound rib did not overhang; this test can no longer detect the defect " +
			"the whole binding layer exists to remove")
	}
}

// A variant is a NEW document. The stored one has to stay exactly what the model
// said, or a replay stops matching what the person saw (PRD VIS-04).
func TestWithParameters_DoesNotTouchTheDocumentItCameFrom(t *testing.T) {
	base := boundBracket()

	got, _ := base.WithParameters(map[string]float64{"plate_size": 120})
	if got.Parts[0].Size["width"] != 120 {
		t.Fatalf("the variant did not change: %g", got.Parts[0].Size["width"])
	}
	if base.Parts[0].Size["width"] != 60 {
		t.Errorf("the ORIGINAL part was rewritten to %g — Size maps are shared, so a "+
			"comparison would show two identical shapes", base.Parts[0].Size["width"])
	}
	if base.Parameters[0].Value != 60 {
		t.Errorf("the original parameter was rewritten to %g", base.Parameters[0].Value)
	}
	if base.Parts[1].Position[2] != 15 {
		t.Errorf("the original position was rewritten to %g", base.Parts[1].Position[2])
	}
}

// Setting a derived value reads as working and changes nothing, because the next
// Bind recomputes it from its expression. So it is refused, and the refusal says
// what to change instead.
func TestWithParameters_ADerivedValueCannotBeSetDirectly(t *testing.T) {
	_, problems := boundBracket().WithParameters(map[string]float64{"rib_length": 99})
	if !hasProblem(geometry.Resolution{Problems: problems}, geometry.Error, "rib_length", "is derived") {
		t.Fatalf("setting a derived value was accepted: %+v", problems)
	}
	if !hasProblem(geometry.Resolution{Problems: problems}, geometry.Error, "rib_length", "plate_size - 2 * fillet_radius") {
		t.Errorf("the refusal does not say what it follows: %+v", problems)
	}
}

func TestWithParameters_AnUnknownNameIsRefused(t *testing.T) {
	_, problems := boundBracket().WithParameters(map[string]float64{"plaet_size": 80})
	if !hasProblem(geometry.Resolution{Problems: problems}, geometry.Error, "plaet_size", "not a parameter") {
		t.Fatalf("a typo was accepted silently, changing nothing: %+v", problems)
	}
}

// Overriding a recalled figure makes it the caller's number. Leaving
// how:"standard" on it would attribute somebody's typed value to a published
// standard — the laundering standards.go exists to stop.
func TestWithParameters_OverridingAStandardFigureClearsItsProvenance(t *testing.T) {
	d := boundBracket()
	d.Parameters = append(d.Parameters, geometry.Parameter{
		Name: "bolt_circle", Value: 31, Unit: "mm",
		How: geometry.FromStandard, Source: "NEMA 17",
	})

	got, _ := d.WithParameters(map[string]float64{"bolt_circle": 40})
	for _, p := range got.Parameters {
		if p.Name != "bolt_circle" {
			continue
		}
		if p.How == geometry.FromStandard || p.Source != "" {
			t.Fatalf("a value somebody typed is still attributed to %q as a standard", p.Source)
		}
	}
}

// The model's own number and the model's own relationship disagreeing is a real
// finding, and there is nowhere else it would surface.
func TestBind_TheExpressionWinsAndTheDisagreementIsReported(t *testing.T) {
	d := boundBracket()
	d.Parts[0].Size["width"] = 999 // contradicts size_from: "plate_size" (60)

	problems := d.Bind()
	if got := d.Parts[0].Size["width"]; got != 60 {
		t.Fatalf("width = %g; the expression must win over the snapshot", got)
	}
	if !hasProblem(geometry.Resolution{Problems: problems}, geometry.Warning, "Plate", "999") {
		t.Errorf("the disagreement was not reported: %+v", problems)
	}
}

// Agreement is the normal case and must say nothing. A panel that fires on
// correct input is a panel people stop reading.
func TestBind_AgreementIsSilent(t *testing.T) {
	if problems := boundBracket().Bind(); len(problems) != 0 {
		t.Fatalf("a consistent parametric document produced %d problems: %+v", len(problems), problems)
	}
}

// A binding that cannot be read must not blank the dimension: the number the
// model typed is the best available answer, and a 0 would render as a part that
// vanished.
func TestBind_ABrokenBindingKeepsTheAuthoredNumber(t *testing.T) {
	d := boundBracket()
	d.Parts[1].SizeFrom["width"] = "rib_length - " // truncated expression

	problems := d.Bind()
	if got := d.Parts[1].Size["width"]; got != 54 {
		t.Fatalf("width = %g; the authored number must be kept when the binding fails", got)
	}
	if !hasProblem(geometry.Resolution{Problems: problems}, geometry.Error, "Front rib", "cannot be read") {
		t.Errorf("the failure was not reported: %+v", problems)
	}
	if !hasProblem(geometry.Resolution{Problems: problems}, geometry.Error, "Front rib", "was kept") {
		t.Errorf("the note does not say the old number survived: %+v", problems)
	}
}

func TestBind_AnUnknownAxisIsNamed(t *testing.T) {
	d := boundBracket()
	d.Parts[1].PositionFrom = map[string]string{"w": "rib_offset"}

	if !hasProblem(geometry.Resolution{Problems: d.Bind()}, geometry.Error, "Front rib", "not an axis") {
		t.Fatal("a nonsense axis was accepted")
	}
}

// Binding a dimension the part did not state is not a disagreement; it is the
// document supplying a number that was missing.
func TestBind_BindingAMissingDimensionIsNotADisagreement(t *testing.T) {
	d := boundBracket()
	delete(d.Parts[1].Size, "width")

	problems := d.Bind()
	if got := d.Parts[1].Size["width"]; got != 54 {
		t.Fatalf("width = %g, want 54 from the expression", got)
	}
	for _, p := range problems {
		if p.Name == "Front rib" {
			t.Errorf("supplying a missing dimension was reported as a disagreement: %s", p.Detail)
		}
	}
}

// Every stored variant predates these fields.
func TestBind_ADocumentWithNoBindingsIsUntouched(t *testing.T) {
	d := &geometry.Document{
		Units: "mm",
		Parts: []geometry.Part{{ID: "p", Shape: "box",
			Size: map[string]float64{"width": 60}, Position: []float64{1, 2, 3}}},
	}
	if problems := d.Bind(); len(problems) != 0 {
		t.Fatalf("a non-parametric document produced problems: %+v", problems)
	}
	if d.Parts[0].Size["width"] != 60 || d.Parts[0].Position[0] != 1 {
		t.Error("a non-parametric part was modified")
	}
}

// A re-specified variant must not arrive covered in caveats about numbers the
// caller deliberately changed.
//
// Observed live on 2026-09-05: one respec of a seven-part bracket produced eight
// warnings, every one of the form "states width = 60 but its own expression
// works out to 90" — after plate_size had been set to 90 on purpose. The stated
// numbers are stale BY CONSTRUCTION once a parameter moves, and a caveat on
// every bound dimension is how a caveat list stops being read.
func TestWithParameters_DoesNotReportTheNumbersItWasAskedToChange(t *testing.T) {
	got, problems := boundBracket().WithParameters(map[string]float64{"plate_size": 90})

	for _, p := range problems {
		t.Errorf("re-specifying reported %q: %s", p.Name, p.Detail)
	}
	// The result is still self-consistent: what was saved matches its own
	// expressions, so binding the stored variant again finds nothing to report.
	if again := got.Bind(); len(again) != 0 {
		t.Errorf("the re-specified document disagrees with itself: %+v", again)
	}
	if w := got.Parts[0].Size["width"]; w != 90 {
		t.Errorf("plate width = %g, want 90", w)
	}
}

// But a genuine fault must still come through a respec — the suppression above
// is about stale snapshots, not about silence.
func TestWithParameters_StillReportsARealFault(t *testing.T) {
	d := boundBracket()
	d.Parts[1].SizeFrom["width"] = "rib_length / missing_thing"

	_, problems := d.WithParameters(map[string]float64{"plate_size": 90})
	if !hasProblem(geometry.Resolution{Problems: problems}, geometry.Error, "Front rib", "missing_thing") {
		t.Fatalf("a broken binding was swallowed by the respec path: %+v", problems)
	}
}
