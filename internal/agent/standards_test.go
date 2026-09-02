package agent

import (
	"strings"
	"testing"
)

// These fences are built from the ACTUAL text the model produced, captured in
// docs/spikes/2026-09-02-zoo-text-to-cad/data/forge-baseline-runs.json. A test
// written from an invented example proves the detector handles invented
// examples.

// The bug, exactly as it happened: a fabricated figure for a real standard,
// stated as fact in the assumptions list.
func TestStandardsClaim_CatchesTheFabricatedNema17Pattern(t *testing.T) {
	r := &Reply{
		Speech: "Here is a motor mount bracket.",
		Prototype: &Prototype{
			Name: "nema17-motor-mount", Units: "mm",
			Parts: []PrototypePart{{ID: "base-plate", Name: "Base Plate", Shape: "box"}},
			Assumptions: []string{
				"Base plate is 50x50x5 mm with centered NEMA 17 bolt pattern (holes at ±20.5 mm on both axes)",
				"Boss height is 15 mm and positioned to clear motor face",
			},
			NotVerified: []string{"Not stress-analysed"},
		},
	}
	claims := FindStandardsClaims(r)
	if len(claims) == 0 {
		t.Fatal("the fabricated NEMA 17 bolt pattern was not flagged at all")
	}
	var found *StandardsClaim
	for i := range claims {
		if namesStandard(claims[i], "NEMA 17") {
			found = &claims[i]
		}
	}
	if found == nil {
		t.Fatalf("no NEMA 17 claim in %+v", claims)
	}
	if found.Where != "assumption" {
		t.Errorf("claim located in %q, want %q — the field a reader trusts most", found.Where, "assumption")
	}
	// The figure has to travel with the claim. "FORGE mentioned a standard" is
	// not actionable; "FORGE said ±20.5 mm" is the thing a person can check.
	if !containsFigure(found.Figures, "±20.5 mm") {
		t.Errorf("figures %v do not carry the number that was wrong", found.Figures)
	}
}

// The second assumption in the same reply — a dimension FORGE CHOSE — must not
// be flagged. If everything is flagged, the flag means nothing.
func TestStandardsClaim_DoesNotFlagAChosenDimension(t *testing.T) {
	r := &Reply{
		Speech: "Done.",
		Prototype: &Prototype{
			Parts: []PrototypePart{{ID: "p", Shape: "box"}},
			Assumptions: []string{
				"Base plate is 50x50x5 mm",
				"Boss height is 10 mm, radius 11.5 mm (23 mm diameter)",
				"Ribs are conical frustums positioned at ±15 mm along X",
				"No mounting holes drilled in base (to be added per use case)",
			},
			NotVerified: []string{"Not analysed"},
		},
	}
	if claims := FindStandardsClaims(r); len(claims) != 0 {
		t.Fatalf("chosen dimensions were flagged as standards claims: %+v", claims)
	}
}

// A standard cited in PROSE, with no geometry anywhere, is exactly as
// unverifiable. Scanning only the prototype would miss the case where FORGE is
// asked a question and simply answers with a figure.
func TestStandardsClaim_CatchesAFigureWithNoGeometry(t *testing.T) {
	r := &Reply{
		Speech: "A NEMA 17 flange is 42.3 mm across the face.",
	}
	claims := FindStandardsClaims(r)
	if len(claims) != 1 {
		t.Fatalf("want 1 claim from prose, got %d: %+v", len(claims), claims)
	}
	if claims[0].Where != "spoken" {
		t.Errorf("where = %q, want %q", claims[0].Where, "spoken")
	}
	if !containsFigure(claims[0].Figures, "42.3 mm") {
		t.Errorf("figures %v missing the quoted dimension", claims[0].Figures)
	}
}

// Claiming conformance without quoting a number is still a claim the system did
// not check, and it must not slip through for lack of a figure.
func TestStandardsClaim_FlagsBareConformance(t *testing.T) {
	r := &Reply{
		Speech: "The plate matches the NEMA 17 footprint.",
	}
	claims := FindStandardsClaims(r)
	if len(claims) != 1 {
		t.Fatalf("want 1 claim, got %d: %+v", len(claims), claims)
	}
	if len(claims[0].Figures) != 0 {
		t.Errorf("figures = %v, want none — nothing was quoted", claims[0].Figures)
	}
}

// Fastener sizes are the commonest standards claim of all, and the one most
// likely to be acted on directly.
func TestStandardsClaim_CatchesFastenerAndRatingFamilies(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"Four M3 clearance holes at 3.4 mm diameter", "M3"},
		{"Bearing is a 6802 with a 15 mm bore", "6802"},
		{"Enclosure is rated IP65 for washdown", "IP65"},
		{"Fasteners per ISO 4762 socket head", "ISO 4762"},
	} {
		claims := FindStandardsClaims(&Reply{Speech: tc.text})
		if len(claims) == 0 {
			t.Errorf("%q: nothing flagged, want %q", tc.text, tc.want)
			continue
		}
		if !namesStandard(claims[0], tc.want) {
			t.Errorf("%q: flagged %v, want %q", tc.text, claims[0].Standards, tc.want)
		}
	}
}

// Ordinary engineering prose with no standard in it must stay silent. A banner
// that appears on every reply trains people to ignore it, which is worse than
// not having one.
func TestStandardsClaim_QuietOnOrdinaryProse(t *testing.T) {
	for _, text := range []string{
		"I have modelled a bracket with two ribs. Dimensions are assumed for now.",
		"The base plate is 60 mm square and 4 mm thick, which I chose.",
		"This is a conceptual shape only — no material specification is included.",
	} {
		if claims := FindStandardsClaims(&Reply{Speech: text}); len(claims) != 0 {
			t.Errorf("%q flagged %+v, want silence", text, claims)
		}
	}
}

// The scan runs inside validate(), which is the one path BOTH the buffered and
// the streamed reply go through. A rule enforced in one of two paths holds only
// until somebody uses the other one.
func TestStandardsClaim_PopulatedByValidate(t *testing.T) {
	r := &Reply{
		Speech: "The plate follows NEMA 17 at 42.3 mm.",
		Prototype: &Prototype{
			Parts:       []PrototypePart{{ID: "p", Shape: "box"}},
			NotVerified: []string{"nothing checked"},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if len(r.Recalled) == 0 {
		t.Fatal("validate() did not populate Recalled, so neither reply path would carry it")
	}
}

// Deterministic output: two scans of one reply must agree, or a test asserting
// on the list is asserting on a coin flip.
func TestStandardsClaim_IsDeterministic(t *testing.T) {
	r := &Reply{
		Speech: "Uses M3 fasteners.",
		Prototype: &Prototype{
			Parts:       []PrototypePart{{ID: "p", Shape: "box", Note: "NEMA 17 face, 42.3 mm"}},
			Assumptions: []string{"Bolt circle is 31 mm per NEMA 17"},
			NotVerified: []string{"nothing checked"},
		},
	}
	a, b := FindStandardsClaims(r), FindStandardsClaims(r)
	if len(a) != len(b) {
		t.Fatalf("different lengths: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if strings.Join(a[i].Standards, ",") != strings.Join(b[i].Standards, ",") ||
			a[i].Where != b[i].Where || a[i].Text != b[i].Text {
			t.Fatalf("order or content differs at %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// A sentence naming two standards must produce ONE claim carrying both, not two
// claims each asserting the other's numbers. Regression: the first live run
// rendered "M3 — 42.3 mm" from a sentence about an M3 screw and a NEMA 17 face,
// which is a pairing the system invented while warning about invented numbers.
func TestStandardsClaim_DoesNotPairAFigureWithTheWrongStandard(t *testing.T) {
	text := "The plate matches the NEMA 17 face at 42.3 mm and takes M3 screws in 3.2 mm clearance holes"
	claims := FindStandardsClaims(&Reply{Speech: text})
	if len(claims) != 1 {
		t.Fatalf("want 1 claim for 1 sentence, got %d: %+v", len(claims), claims)
	}
	c := claims[0]
	if !namesStandard(c, "NEMA 17") || !namesStandard(c, "M3") {
		t.Fatalf("both standards must be on the one claim, got %v", c.Standards)
	}
	if c.Text != text {
		t.Errorf("the sentence must travel with the claim so a reader can pair them; got %q", c.Text)
	}
	// Every figure in the sentence is listed once, against the sentence — never
	// distributed onto an individual standard.
	if !containsFigure(c.Figures, "42.3 mm") || !containsFigure(c.Figures, "3.2 mm") {
		t.Errorf("figures %v should carry both numbers", c.Figures)
	}
}

func namesStandard(c StandardsClaim, want string) bool {
	for _, s := range c.Standards {
		if strings.EqualFold(strings.ReplaceAll(s, " ", ""), strings.ReplaceAll(want, " ", "")) {
			return true
		}
	}
	return false
}

func containsFigure(figures []string, want string) bool {
	for _, f := range figures {
		if strings.EqualFold(strings.ReplaceAll(f, " ", ""), strings.ReplaceAll(want, " ", "")) {
			return true
		}
	}
	return false
}
