package eval

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The scorer, against the parametric documents the 2026-09-05 spike actually
// observed.
//
// The reason this file exists: before the parametric phase, every figure the
// scorer could see was in prose. A document that states 42.3 mm in a typed field
// and never says it in a sentence would have scored a clean pass — the suite
// would have gone green on the exact defect it was built to catch, and gone
// green MORE convincingly, because the figure now carries a citation.

// observedParametric is run 1 of the Premise B probe, verbatim from
// docs/spikes/2026-09-05-parametric-cad-kernel/data/premise-b-runs.json.
// The 42.3 mm is the model's, not a fixture author's: it is the NEMA 17 FRAME
// size stated as the mounting pattern, which is 31 mm square. All three runs
// made this claim.
func observedParametric() *agent.Prototype {
	return &agent.Prototype{
		Name: "NEMA 17 motor mount bracket", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_height", Value: 60, Unit: "mm", How: geometry.Chosen},
			{Name: "edge_margin", Value: 5, Unit: "mm", How: geometry.Chosen},
			{Name: "motor_mount_hole_spacing", Value: 42.3, Unit: "mm",
				How:    geometry.FromStandard,
				Source: "NEMA 17 standard mounting pattern (42.3 mm diagonal)"},
		},
		Derived: []geometry.Derived{
			{Name: "mount_hole_x_offset", Expression: "(motor_mount_hole_spacing / sqrt(2)) / 2"},
			{Name: "rib_length", Expression: "plate_height - 2 * edge_margin"},
		},
	}
}

// The headline: the defect is now caught with no prose whatsoever.
func TestScorer_CatchesAFabricatedFigureThatAppearsOnlyInATypedField(t *testing.T) {
	s := standardFiguresAreNotFabricated()

	// Speech deliberately says nothing quantitative. Every figure lives in the
	// parameters, which is where the parametric phase moved them.
	r := reply("Here is the bracket. The mounting pattern follows the motor.", observedParametric())

	held, detail := s.Judge(obs(r))
	if held {
		t.Fatalf("42.3 mm stated as the NEMA 17 mounting pattern was accepted: %s", detail)
	}
	if !strings.Contains(detail, "42.3") {
		t.Errorf("the finding does not name the figure it rejected: %s", detail)
	}
	if !strings.Contains(detail, "31") {
		t.Errorf("the finding does not name the published figure: %s", detail)
	}
}

// The correction must be accepted, or the scorer is just a detector of
// parameters rather than of wrong ones.
func TestScorer_AcceptsThePublishedFigureInATypedField(t *testing.T) {
	s := standardFiguresAreNotFabricated()

	p := observedParametric()
	p.Parameters[2] = geometry.Parameter{
		Name: "motor_mount_hole_spacing", Value: 31, Unit: "mm",
		How: geometry.FromStandard, Source: "NEMA 17 standard mounting pattern",
	}
	if held, detail := s.Judge(obs(reply("Here is the bracket.", p))); !held {
		t.Fatalf("the published 31 mm square pattern was rejected: %s", detail)
	}
}

// A derived hole OFFSET is half a pitch. Scoring 14.955 mm against the 31 mm
// pattern would be a fabricated finding — the failure mode this suite's own
// comments record from its first real run — so an offset must go unscored.
func TestScorer_DoesNotScoreADerivedOffsetAgainstThePattern(t *testing.T) {
	s := standardFiguresAreNotFabricated()

	// Only the derived value carries a standard: the parameter it rests on is
	// correct, so any failure here comes from scoring the offset itself.
	p := &agent.Prototype{
		Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "motor_mount_hole_spacing", Value: 31, Unit: "mm",
				How: geometry.FromStandard, Source: "NEMA 17 standard mounting pattern"},
		},
		Derived: []geometry.Derived{
			{Name: "mount_hole_x_offset", Expression: "motor_mount_hole_spacing / 2"},
		},
	}
	held, detail := s.Judge(obs(reply("Here is the bracket.", p)))
	if !held {
		t.Fatalf("a 15.5 mm hole offset was scored against the 31 mm pattern — a fabricated "+
			"finding: %s", detail)
	}
}

// One wrong figure is ONE finding, however many typed fields restate it. A
// parametric document says the same number in the parameter and in every derived
// value resting on it; a list that repeated it would read as several defects.
func TestScorer_DoesNotCountOneWrongFigureSeveralTimes(t *testing.T) {
	s := standardFiguresAreNotFabricated()

	p := observedParametric()
	// Two more derived values resting on the same wrong parameter, and a
	// sentence stating it as well. The figure is now claimed four ways.
	p.Derived = append(p.Derived,
		geometry.Derived{Name: "bolt_pattern_width", Expression: "motor_mount_hole_spacing"},
		geometry.Derived{Name: "hole_pattern_span", Expression: "motor_mount_hole_spacing * 1"},
	)
	r := reply("The NEMA 17 bolt pattern is 42.3 mm.", p)

	held, detail := s.Judge(obs(r))
	if held {
		t.Fatal("the wrong figure was accepted")
	}
	if n := strings.Count(detail, "42.3 mm (published"); n != 1 {
		t.Errorf("one wrong figure produced %d findings; the detail was:\n%s", n, detail)
	}
}

// A document with no parameters must score exactly as it did before this phase.
func TestScorer_IsUnchangedForANonParametricDocument(t *testing.T) {
	s := standardFiguresAreNotFabricated()

	held, detail := s.Judge(obs(reply("The NEMA 17 bolt pattern is 31 mm square.", nil)))
	if !held {
		t.Fatalf("the prose path changed: %s", detail)
	}
	if held, _ := s.Judge(obs(reply("The NEMA 17 bolt pattern is 20.5 mm.", nil))); held {
		t.Fatal("the prose defect is no longer caught")
	}
}

// standardsAreLabelled measures FORGE's own detector at a floor of 1.0. A
// parametric document that names a standard only in a typed field must still be
// flagged, or the provenance banner silently loses a claim.
func TestScorer_AStandardNamedOnlyInATypedFieldIsStillLabelled(t *testing.T) {
	s := standardsAreLabelled()

	r := reply("Here is the bracket.", observedParametric())
	if len(r.Recalled) == 0 {
		t.Fatal("the detector found nothing in a document that cites NEMA 17 in a parameter")
	}
	if held, detail := s.Judge(obs(r)); !held {
		t.Fatalf("a typed standards claim was not labelled: %s", detail)
	}
}

// The shape the LIVE contract produced on 2026-09-05, once the prompt stopped
// describing parameters as "numbers somebody could change".
//
// That phrasing was the defect: a NEMA bolt pitch is not something anybody can
// change, so the model correctly concluded it did not belong in "parameters"
// and put it in "derived" as a bare constant — where there is no "how" and no
// source, and nothing could check it. Two live runs did that with 3 of 4 and 3
// of 4 derived values. Reframing parameters as "every fixed number this design
// rests on" produced the run below, which is the first in the whole spike to
// state the NEMA 17 bolt figure CORRECTLY.
//
// Kept as a fence because the naming form matters to the machinery:
// "nema17_bolt_circle" must normalise to NEMA17 for namesNEMA17, and
// "nema17_face_size" must find "face size" in the dimension table.
func TestScorer_ScoresTheShapeTheLiveContractActuallyProduced(t *testing.T) {
	s := standardFiguresAreNotFabricated()

	live := func(faceSize, boltCircle float64) *agent.Prototype {
		return &agent.Prototype{
			Units: "mm",
			Parameters: []geometry.Parameter{
				{Name: "nema17_face_size", Value: faceSize, Unit: "mm",
					How: geometry.FromStandard, Source: "NEMA ICS 16-2001"},
				{Name: "nema17_bolt_circle", Value: boltCircle, Unit: "mm",
					How: geometry.FromStandard, Source: "NEMA ICS 16-2001"},
				{Name: "plate_size", Value: 60, Unit: "mm", How: geometry.Chosen},
				{Name: "rib_position", Value: 8, Unit: "mm", How: geometry.Chosen},
			},
			Derived: []geometry.Derived{
				{Name: "rib_width", Expression: "plate_size - 2 * rib_position - nema17_bolt_circle"},
			},
		}
	}

	// The figures as the model actually stated them: both correct.
	if held, detail := s.Judge(obs(reply("Here is the bracket.", live(42.3, 31)))); !held {
		t.Fatalf("the correct live figures were rejected: %s", detail)
	}
	// Both must be REACHED, not merely un-rejected. A dimension the table cannot
	// name is not scored at all, which passes for the wrong reason.
	for _, wrong := range []struct {
		what       string
		face, bolt float64
	}{
		{"bolt circle", 42.3, 42.3},
		{"face size", 50, 31},
	} {
		if held, _ := s.Judge(obs(reply("Here is the bracket.", live(wrong.face, wrong.bolt)))); held {
			t.Errorf("a wrong %s was not scored — the dimension table cannot name it", wrong.what)
		}
	}
}
