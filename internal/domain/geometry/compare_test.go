package geometry

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
)

func variant(name string, units Unit, parts ...Part) Variant {
	return Variant{
		VersionID: "ver_" + name, ProjectID: "prj_1", Path: "geometry/" + name + ".forge.json", Version: 1,
		Name: name, Units: units, Frame: FrameAssembly, Generator: "claude-opus-5",
		Verification: workspace.Unverified, Disposition: workspace.Pending,
		Document: Document{
			Name: name, Units: string(units), Parts: parts,
			NotVerified: []string{"nothing checked"},
		},
	}
}

func box60mm(id string, w float64) Part {
	return Part{ID: id, Name: id, Shape: "box",
		Size: map[string]float64{"width": w}, Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
}

// The trap this whole file is arranged around: 60 mm and 6 cm are the same
// length, and a comparison that reads the numbers reports them as different.
func TestCompare_ConvertsBeforeCallingAnythingDifferent(t *testing.T) {
	c := Compare([]Variant{
		variant("a", Millimetre, box60mm("plate", 60)),
		variant("b", Centimetre, box60mm("plate", 6)),
	})
	row := c.Parts[0]
	if row.Differs() {
		t.Fatalf("60 mm and 6 cm were reported as different: %v", row.Differences)
	}
	// And the renders still show each variant in the unit it was authored in.
	if !strings.Contains(row.Cells[0].Dimensions, "mm") || !strings.Contains(row.Cells[1].Dimensions, "cm") {
		t.Errorf("dimensions were rewritten into a common unit: %q vs %q",
			row.Cells[0].Dimensions, row.Cells[1].Dimensions)
	}
}

// The same conversion must not hide a real difference.
func TestCompare_ARealDifferenceSurvivesConversion(t *testing.T) {
	c := Compare([]Variant{
		variant("a", Millimetre, box60mm("plate", 60)),
		variant("b", Centimetre, box60mm("plate", 7.2)),
	})
	row := c.Parts[0]
	if !row.Differs() {
		t.Fatal("60 mm and 7.2 cm were reported as the same length")
	}
	joined := strings.Join(row.Differences, "\n")
	if !strings.Contains(joined, "60 mm") || !strings.Contains(joined, "7.2 cm") {
		t.Errorf("the difference does not show each value in its own unit: %v", row.Differences)
	}
}

// A variant with no convertible unit cannot be compared with anything, and
// saying "these are the same" because the numbers match is the most convincing
// wrong answer available here.
func TestCompare_RefusesToJudgeAnUnstatedUnit(t *testing.T) {
	c := Compare([]Variant{
		variant("a", Millimetre, box60mm("plate", 60)),
		variant("b", UnitUnspecified, box60mm("plate", 60)),
	})
	row := c.Parts[0]
	if row.Differs() {
		t.Errorf("an incomparable pair was reported as a difference: %v", row.Differences)
	}
	if len(c.NotComparable) == 0 {
		t.Fatal("an incomparable pair was silently treated as equal; identical numbers in unknown " +
			"units are not the same length")
	}
	if !strings.Contains(strings.Join(c.NotComparable, "\n"), "no unit") {
		t.Errorf("the note does not say why: %v", c.NotComparable)
	}
}

// A part in one design and not another is the biggest difference there is, and
// it is invisible in a table of dimensions.
func TestCompare_AMissingPartIsADifference(t *testing.T) {
	c := Compare([]Variant{
		variant("a", Millimetre, box60mm("plate", 60), box60mm("rib", 5)),
		variant("b", Millimetre, box60mm("plate", 60)),
	})
	var rib *PartRow
	for i := range c.Parts {
		if c.Parts[i].PartID == "rib" {
			rib = &c.Parts[i]
		}
	}
	if rib == nil {
		t.Fatal("a part present in only one variant did not appear in the comparison at all")
	}
	if len(rib.MissingFrom) != 1 || rib.MissingFrom[0] != 2 {
		t.Fatalf("the rib is absent from column 2 and the comparison says %v", rib.MissingFrom)
	}
	if !rib.Differs() {
		t.Error("a part missing from one variant is not reported as a difference")
	}
}

// A part that moved is a different design, and a dimension table alone says the
// two are identical.
func TestCompare_AMovedPartIsADifference(t *testing.T) {
	moved := box60mm("boss", 10)
	moved.Position = []float64{0, 8, 0}
	c := Compare([]Variant{
		variant("a", Millimetre, box60mm("boss", 10)),
		variant("b", Millimetre, moved),
	})
	if !c.Parts[0].Differs() {
		t.Fatal("a part moved 8 mm up was reported as identical")
	}
	if !strings.Contains(strings.Join(c.Parts[0].Differences, "\n"), "Y (up)") {
		t.Errorf("the difference does not name the axis in the frame's own terms: %v", c.Parts[0].Differences)
	}
}

// VIS-04's six facts are each a row, and a row must exist even when nothing
// differs — the reader's question is "what is this render", not only "what
// changed".
func TestCompare_EveryProvenanceFactIsARow(t *testing.T) {
	c := Compare([]Variant{
		variant("a", Millimetre, box60mm("plate", 60)),
		variant("b", Millimetre, box60mm("plate", 60)),
	})
	want := []string{"geometry version", "units", "assumptions", "generator", "verification", "human disposition"}
	have := map[string]bool{}
	for _, r := range c.Provenance {
		have[r.Field] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("VIS-04 requires every render to link to %q, and the comparison has no such row", w)
		}
	}
}

// Verification and disposition are separate rows and must stay separate: a
// machine's pass is not a person's sign-off (PRD SAF-05).
func TestCompare_VerificationAndDispositionAreNeverOneRow(t *testing.T) {
	a := variant("a", Millimetre, box60mm("plate", 60))
	a.Verification = workspace.Passed
	b := variant("b", Millimetre, box60mm("plate", 60))
	b.Disposition = workspace.Accepted

	c := Compare([]Variant{a, b})
	var verification, disposition *FieldRow
	for i := range c.Provenance {
		switch c.Provenance[i].Field {
		case "verification":
			verification = &c.Provenance[i]
		case "human disposition":
			disposition = &c.Provenance[i]
		}
	}
	if verification == nil || disposition == nil {
		t.Fatal("the two were merged into one row")
	}
	if verification.Values[0] != "passed" || verification.Values[1] != "unverified" {
		t.Errorf("verification row reads %v", verification.Values)
	}
	if disposition.Values[0] != "pending" || disposition.Values[1] != "accepted" {
		t.Errorf("disposition row reads %v", disposition.Values)
	}
}

// Columns stay in the order the person named them. Re-sorting rearranges the
// comparison somebody asked to see.
func TestCompare_ColumnsKeepTheOrderTheyWereNamedIn(t *testing.T) {
	c := Compare([]Variant{
		variant("second", Millimetre, box60mm("plate", 60)),
		variant("first", Millimetre, box60mm("plate", 60)),
	})
	if c.Variants[0].Name != "second" || c.Variants[1].Name != "first" {
		t.Fatalf("columns were reordered: %s then %s", c.Variants[0].Name, c.Variants[1].Name)
	}
}

// A dimension declared in one variant and absent in another is a difference, not
// a match — iterating over one side's keys alone would miss it in one direction.
func TestCompare_ADimensionOnlyOneSideStatesIsADifference(t *testing.T) {
	tapered := Part{ID: "boss", Name: "boss", Shape: "cylinder",
		Size:     map[string]float64{"radius": 11, "height": 6, "radius_top": 8},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
	straight := Part{ID: "boss", Name: "boss", Shape: "cylinder",
		Size:     map[string]float64{"radius": 11, "height": 6},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}

	// Both directions: the absent key must be found whether it is missing from
	// the first column or a later one.
	for _, order := range [][]Variant{
		{variant("a", Millimetre, tapered), variant("b", Millimetre, straight)},
		{variant("a", Millimetre, straight), variant("b", Millimetre, tapered)},
	} {
		c := Compare(order)
		joined := strings.Join(c.Parts[0].Differences, "\n")
		if !strings.Contains(joined, "radius_top") {
			t.Fatalf("a dimension stated on only one side was missed: %v", c.Parts[0].Differences)
		}
	}
}

// The model does not keep part ids stable between turns. Matching on id alone
// rendered every part twice — once as "only in column 1", once as "only in
// column 2" — which read as two unrelated designs when what had happened was a
// revision. Observed against a real conversation, not imagined.
func TestCompare_RenamedIDsAreMatchedByNameAndSaidToBe(t *testing.T) {
	first := Part{ID: "nema-17-motor", Name: "NEMA 17 Motor", Shape: "cylinder",
		Size: map[string]float64{"radius": 21.15, "height": 40}, Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
	second := Part{ID: "motor", Name: "NEMA 17 Motor", Shape: "cylinder",
		Size: map[string]float64{"radius": 21.15, "height": 40}, Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}

	c := Compare([]Variant{
		variant("a", Millimetre, first),
		variant("b", Millimetre, second),
	})
	if len(c.Parts) != 1 {
		t.Fatalf("one part appeared as %d rows; a renamed id must not read as two designs", len(c.Parts))
	}
	row := c.Parts[0]
	if row.MatchedBy != MatchByName {
		t.Errorf("matched by %q; the ids differ, so this can only be a name match", row.MatchedBy)
	}
	if len(row.MissingFrom) != 0 {
		t.Errorf("the part is in both variants and the comparison says it is missing from %v", row.MissingFrom)
	}
	// And the guess must be declared, in its OWN list. A name match presented as
	// identity is the interface asserting something the system does not know;
	// filed under "not compared" it would be a qualified judgement sitting in the
	// box for withheld ones.
	if !strings.Contains(strings.Join(c.MatchNotes, "\n"), "BY NAME") {
		t.Fatalf("the name match was made silently: %v", c.MatchNotes)
	}
	if len(c.NotComparable) != 0 {
		t.Errorf("a part that WAS compared appears in the not-compared list: %v", c.NotComparable)
	}
}

// An id match is certain and must not be downgraded to a guess.
func TestCompare_MatchingIDsAreReportedAsIdentity(t *testing.T) {
	c := Compare([]Variant{
		variant("a", Millimetre, box60mm("plate", 60)),
		variant("b", Millimetre, box60mm("plate", 72)),
	})
	if c.Parts[0].MatchedBy != MatchByID {
		t.Fatalf("identical ids were matched by %q", c.Parts[0].MatchedBy)
	}
	if len(c.MatchNotes) != 0 || len(c.NotComparable) != 0 {
		t.Errorf("an exact match produced a caveat: %v / %v", c.MatchNotes, c.NotComparable)
	}
}

// Two parts that share a NAME inside one variant are two parts. Folding them
// together would invent a difference out of the fold.
func TestCompare_TheNamePassNeverMergesWithinOneVariant(t *testing.T) {
	front := Part{ID: "front-spacer", Name: "Spacer", Shape: "cylinder",
		Size: map[string]float64{"radius": 3, "height": 20}, Position: []float64{0, 0, 10}, Rotation: []float64{0, 0, 0}}
	rear := Part{ID: "rear-spacer", Name: "Spacer", Shape: "cylinder",
		Size: map[string]float64{"radius": 3, "height": 20}, Position: []float64{0, 0, -10}, Rotation: []float64{0, 0, 0}}

	c := Compare([]Variant{
		variant("a", Millimetre, front, rear),
		variant("b", Millimetre, front),
	})
	if len(c.Parts) != 2 {
		t.Fatalf("two spacers in one variant collapsed into %d row(s)", len(c.Parts))
	}
	for _, row := range c.Parts {
		for _, cell := range row.Cells {
			_ = cell
		}
	}
	// The rear spacer is genuinely absent from the second variant, and that is a
	// real difference rather than a matching failure.
	var rearRow *PartRow
	for i := range c.Parts {
		if c.Parts[i].PartID == "rear-spacer" {
			rearRow = &c.Parts[i]
		}
	}
	if rearRow == nil {
		t.Fatal("the rear spacer vanished from the comparison")
	}
	if len(rearRow.MissingFrom) != 1 || rearRow.MissingFrom[0] != 2 {
		t.Errorf("the rear spacer is absent from column 2 and the comparison says %v", rearRow.MissingFrom)
	}
}

// A part genuinely present in only one variant stays that way — the name pass
// must not manufacture a partner for it.
func TestCompare_APartInOneVariantOnlyIsNotMatchedToAnything(t *testing.T) {
	rib := Part{ID: "rib", Name: "Stiffening rib", Shape: "box",
		Size: map[string]float64{"width": 3}, Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
	c := Compare([]Variant{
		variant("a", Millimetre, box60mm("plate", 60), rib),
		variant("b", Millimetre, box60mm("plate", 60)),
	})
	var ribRow *PartRow
	for i := range c.Parts {
		if c.Parts[i].PartID == "rib" {
			ribRow = &c.Parts[i]
		}
	}
	if ribRow == nil {
		t.Fatal("a part in one variant only disappeared")
	}
	if ribRow.MatchedBy != MatchNone {
		t.Errorf("an unmatched part reports a match basis of %q", ribRow.MatchedBy)
	}
}
