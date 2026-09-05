package agent_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// observedRun1 is run 1 of the 2026-09-05 Premise B probe, verbatim from
// docs/spikes/2026-09-05-parametric-cad-kernel/data/premise-b-runs.json.
//
// Not a fixture composed to pass: these are the names, values, units, sources
// and expressions qwen-plus actually produced, including the defect. All three
// runs stated 42.3 mm as the NEMA 17 mounting pattern. It is the FRAME size; the
// pattern is 31 mm square.
func observedRun1() *agent.Prototype {
	return &agent.Prototype{
		Name: "NEMA 17 motor mount bracket", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_width", Value: 60, Unit: "mm", How: geometry.Chosen},
			{Name: "plate_height", Value: 60, Unit: "mm", How: geometry.Chosen},
			{Name: "edge_margin", Value: 5, Unit: "mm", How: geometry.Chosen},
			{Name: "rib_height", Value: 10, Unit: "mm", How: geometry.Chosen},
			{Name: "plate_thickness", Value: 6, Unit: "mm",
				How: geometry.FromStandard, Source: "common CNC aluminum bracket thickness"},
			{Name: "motor_mount_hole_diameter", Value: 3.2, Unit: "mm",
				How: geometry.FromStandard, Source: "M3 screw clearance hole (ISO 273)"},
			{Name: "motor_mount_hole_spacing", Value: 42.3, Unit: "mm",
				How:    geometry.FromStandard,
				Source: "NEMA 17 standard mounting pattern (42.3 mm diagonal, square pitch = 30 mm)"},
		},
		Derived: []geometry.Derived{
			{Name: "mount_hole_x_offset", Expression: "(motor_mount_hole_spacing / sqrt(2)) / 2"},
			{Name: "mount_hole_y_offset", Expression: "mount_hole_x_offset"},
			{Name: "rib_length", Expression: "plate_height - 2 * edge_margin",
				Why: "keeps the rib inside the plate when the plate changes"},
			{Name: "rib_position_x", Expression: "plate_width / 2"},
			{Name: "rib_position_y1", Expression: "edge_margin + rib_height / 2"},
			{Name: "rib_position_y2", Expression: "plate_height - edge_margin - rib_height / 2"},
		},
	}
}

func claimsFor(p *agent.Prototype) []agent.StandardsClaim {
	return agent.FindStandardsClaims(&agent.Reply{Prototype: p})
}

func findClaim(claims []agent.StandardsClaim, text string) (agent.StandardsClaim, bool) {
	for _, c := range claims {
		if strings.HasPrefix(c.Text, text) {
			return c, true
		}
	}
	return agent.StandardsClaim{}, false
}

// The claim the prose detector could never have seen: a figure that appears in
// no sentence at all, only in a typed field.
func TestTypedClaim_AParameterQuotingAStandardIsCaught(t *testing.T) {
	claims := claimsFor(observedRun1())

	c, ok := findClaim(claims, "motor_mount_hole_spacing")
	if !ok {
		t.Fatalf("the parameter carrying the observed defect produced no claim; got %+v", claims)
	}
	if c.Where != "parameter" {
		t.Errorf("Where = %q, want %q", c.Where, "parameter")
	}
	if !containsFold(c.Standards, "NEMA 17") {
		t.Errorf("the claim does not name NEMA 17: %v", c.Standards)
	}
	if len(c.Figures) != 1 || !strings.HasPrefix(c.Figures[0], "42.3") {
		t.Errorf("Figures = %v, want the 42.3 mm the model stated", c.Figures)
	}
	if !strings.Contains(c.Via, "NEMA 17 standard mounting pattern") {
		t.Errorf("Via does not carry the source the model cited: %q", c.Via)
	}
}

// The dependency edge. 14.955 mm is stated nowhere; it descends from a NEMA 17
// claim through an expression, and nothing about the number reveals that.
func TestTypedClaim_ProvenanceTravelsToADerivedFigure(t *testing.T) {
	claims := claimsFor(observedRun1())

	c, ok := findClaim(claims, "mount_hole_x_offset")
	if !ok {
		t.Fatalf("a figure derived from a standard parameter produced no claim; got %+v", claims)
	}
	if !containsFold(c.Standards, "NEMA 17") {
		t.Errorf("the derived figure lost its provenance: %v", c.Standards)
	}
	if !strings.Contains(c.Via, "motor_mount_hole_spacing") {
		t.Errorf("Via does not name the parameter it rests on: %q", c.Via)
	}
	if !strings.Contains(c.Via, "sqrt(2)") {
		t.Errorf("Via does not carry the expression: %q", c.Via)
	}
}

// A derived value resting only on chosen parameters is NOT a recalled claim.
// rib_length = plate_height - 2 * edge_margin is the good relationship the spike
// praised; flagging it as quoted from a standard would bury the real finding.
func TestTypedClaim_ADerivedValueFromChosenParametersIsNotAClaim(t *testing.T) {
	claims := claimsFor(observedRun1())
	for _, name := range []string{"rib_length", "rib_position_x", "rib_position_y1"} {
		if c, ok := findClaim(claims, name); ok {
			t.Errorf("%s rests only on chosen parameters but was reported as recalled: %+v", name, c)
		}
	}
}

// THE FENCE FOR THE DESIGN DECISION IN standards_typed.go.
//
// The eval scorer finds a figure and looks for a dimension name within about a
// clause of it. Putting the expression in Text would place the phrase "hole
// spacing" 28 characters from 14.955 mm and the scorer would report a hole
// OFFSET as a wrong bolt PITCH — a fabricated finding, which this codebase holds
// to be worse than a missed one. If someone later folds Via back into Text to
// make the panel read better, this goes red.
func TestTypedClaim_TheClaimTextCarriesNothingButTheNameAndTheFigure(t *testing.T) {
	claims := claimsFor(observedRun1())

	c, ok := findClaim(claims, "mount_hole_x_offset")
	if !ok {
		t.Fatal("no derived claim to check")
	}
	if strings.Contains(c.Text, "motor_mount_hole_spacing") || strings.Contains(c.Text, "sqrt") {
		t.Fatalf("Text carries the expression, which lets the scorer pair a figure with a "+
			"dimension nobody asserted: %q", c.Text)
	}
	if want := "mount_hole_x_offset = "; !strings.HasPrefix(c.Text, want) {
		t.Errorf("Text = %q, want it to start %q", c.Text, want)
	}
}

func TestTypedClaim_AChosenParameterIsNotAStandardsClaim(t *testing.T) {
	p := &agent.Prototype{
		Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_size", Value: 60, Unit: "mm", How: geometry.Chosen,
				Source: "NEMA 17 datasheet"},
		},
	}
	if c, ok := findClaim(claimsFor(p), "plate_size"); ok {
		t.Fatalf("a CHOSEN parameter was reported as quoted from a standard: %+v", c)
	}
}

// Marked as quoted, naming nothing. Resolve raises that as a warning on the
// parameter; inventing a reference here would be the very failure this file is
// about.
func TestTypedClaim_AStandardWithNoSourceInventsNoReference(t *testing.T) {
	p := &agent.Prototype{
		Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "bolt_spacing", Value: 31, Unit: "mm", How: geometry.FromStandard},
		},
	}
	if c, ok := findClaim(claimsFor(p), "bolt_spacing"); ok {
		t.Fatalf("a reference was invented for a sourceless claim: %+v", c)
	}
	res := (&geometry.Document{
		Parameters: []geometry.Parameter{
			{Name: "bolt_spacing", Value: 31, Unit: "mm", How: geometry.FromStandard},
		},
	}).Resolve()
	var warned bool
	for _, pr := range res.Problems {
		if pr.Name == "bolt_spacing" && strings.Contains(pr.Detail, "names no source") {
			warned = true
		}
	}
	if !warned {
		t.Error("nothing reported the sourceless standard claim at all — it must be caught somewhere")
	}
}

// A source naming nothing this build has a pattern for is still a provenance
// claim, and reporting it verbatim is the true statement.
func TestTypedClaim_AnUnrecognisedSourceIsReportedVerbatim(t *testing.T) {
	p := &agent.Prototype{
		Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "flange_width", Value: 42, Unit: "mm", How: geometry.FromStandard,
				Source: "our shop's stepper handbook"},
		},
	}
	c, ok := findClaim(claimsFor(p), "flange_width")
	if !ok {
		t.Fatal("a claim citing an unrecognised source was dropped; the guard must not be " +
			"weakest where the citation is vaguest")
	}
	if !containsFold(c.Standards, "our shop's stepper handbook") {
		t.Errorf("the source was not reported: %v", c.Standards)
	}
}

// Every stored variant predates these fields. Absence must keep meaning "not
// parametric", and the prose path must be untouched by any of this.
func TestTypedClaim_ANonParametricDocumentIsUnchanged(t *testing.T) {
	before := agent.FindStandardsClaims(&agent.Reply{
		Speech: "The NEMA 17 bolt pattern is 31 mm square.",
		Prototype: &agent.Prototype{Units: "mm",
			Assumptions: []string{"an M3 clearance hole of 3.4 mm"}},
	})
	if len(before) != 2 {
		t.Fatalf("the prose path changed: got %d claims, want 2 — %+v", len(before), before)
	}
	for _, c := range before {
		if c.Via != "" {
			t.Errorf("a prose claim gained a Via: %+v", c)
		}
	}
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.ReplaceAll(s, " ", ""), strings.ReplaceAll(want, " ", "")) {
			return true
		}
	}
	return false
}
