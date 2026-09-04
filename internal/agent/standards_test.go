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

// A sentence saying what was NOT checked is not a claim that something is true.
//
// Regression: "No interference check with actual NEMA 17 motor housing" was
// flagged as a figure quoted from memory, which is the opposite of what that
// sentence does. Observed live 2026-09-02.
func TestStandardsClaim_IgnoresNotVerifiedDisclaimers(t *testing.T) {
	r := &Reply{
		Speech: "Here it is.",
		Prototype: &Prototype{
			Parts: []PrototypePart{{ID: "p", Shape: "box"}},
			NotVerified: []string{
				"No interference check with actual NEMA 17 motor housing",
				"Not stress-analysed against the M3 fastener torque",
			},
		},
	}
	if claims := FindStandardsClaims(r); len(claims) != 0 {
		t.Fatalf("disclaimers were flagged as recalled claims: %+v", claims)
	}
}

// The guard covers every industry the product offers (2026-09-04).
//
// # Why this is a fence and not a nicety
//
// The fabrication guard only fires on families it can NAME. Before the industry
// list existed the table was mechanical and electrical, which was the right
// scope for the product at the time. Adding civil, aerospace, automotive,
// construction and architecture without adding their standards bodies would have
// shipped nine industries whose answers cannot be checked: a recalled ACI 318
// figure or a DO-178C claim would carry a specific number, name a published
// standard, and be reported as ordinary prose.
//
// That is worse than not covering the industry at all, because the panel's
// silence reads as "nothing here was recalled". Partial coverage that says so
// beats total coverage that is asserted — so the coverage has to be real where
// the product claims it.
func TestStandardsClaim_CoversTheIndustriesTheProductOffers(t *testing.T) {
	// One representative citation per industry the selector offers, each with a
	// figure attached so the claim is quantitative and would mislead if missed.
	for _, tc := range []struct{ industry, sentence, want string }{
		{"Civil engineering", "Development length per ACI 318 is 305 mm here.", "ACI 318"},
		{"Construction", "The assembly meets IBC 2021 at 1200 mm clear width.", "IBC 2021"},
		{"Architecture", "Ramp slope follows ADA 405.2 at 4.8 degrees.", "ADA 405.2"},
		{"Civil engineering", "Wind load from ASCE 7-22 gives 1.2 kPa.", "ASCE 7-22"},
		{"Aerospace", "DO-178C level B allows a 3.5 mm margin here.", "DO-178C"},
		{"Aerospace", "Per MIL-STD-810 the fixture holds 40 N.", "MIL-STD-810"},
		{"Automotive", "FMVSS 208 requires the sensor within 25 mm.", "FMVSS 208"},
		{"Automotive", "ECE R94 gives a 30 mm intrusion limit.", "ECE R94"},
		{"Electrical engineering", "IPC-2221 asks for 0.5 mm clearance at that voltage.", "IPC-2221"},
		{"Electrical engineering", "IEEE 802.3 allows 100 m of cable.", "IEEE 802.3"},
		{"Mechanical engineering", "Functional safety per IEC 61508 at 5 mm travel.", "IEC 61508"},
	} {
		claims := FindStandardsClaims(&Reply{Speech: tc.sentence})
		if len(claims) == 0 {
			t.Errorf("%s: %q named a published standard with a figure and was not flagged at all.\n"+
				"An answer in this industry can recite a recalled number and the panel will "+
				"say nothing", tc.industry, tc.sentence)
			continue
		}
		if !namesStandard(claims[0], tc.want) {
			t.Errorf("%s: %q was flagged but not as %q — got %v.\n"+
				"A claim named as something that does not exist is worse than an unnamed one: "+
				"the reader cannot look it up", tc.industry, tc.sentence, tc.want, claims[0].Standards)
		}
		if len(claims[0].Figures) == 0 {
			t.Errorf("%s: %q carried a dimensioned figure and none was captured",
				tc.industry, tc.sentence)
		}
	}
}

// The widened IEC pattern names the whole designation, not its first digits.
//
// Regression: the pattern was \d{2,3}, sized for IEC motor FRAMES. "IEC 61508"
// matched its leading "IEC 615", so the claim was detected and then reported
// under a designation that does not exist — which sends a reader looking for a
// standard they will never find.
func TestStandardsClaim_IECDesignationIsNotTruncated(t *testing.T) {
	claims := FindStandardsClaims(&Reply{Speech: "Rated to IEC 61508 SIL 2 at 24 V."})
	if len(claims) == 0 {
		t.Fatal("IEC 61508 was not flagged")
	}
	if namesStandard(claims[0], "IEC 615") {
		t.Errorf("the claim names the truncated %q; got %v", "IEC 615", claims[0].Standards)
	}
	if !namesStandard(claims[0], "IEC 61508") {
		t.Errorf("the claim does not name IEC 61508: %v", claims[0].Standards)
	}
	// The motor-frame form the pattern was originally sized for still works.
	if c := FindStandardsClaims(&Reply{Speech: "An IEC 80 frame is 80 mm to the shaft."}); len(c) == 0 {
		t.Error("widening the pattern lost the IEC motor-frame case it was written for")
	}
}

// A family NAME on its own is prose, not a claim.
//
// The rule the table is built on: "ISO" is a word, "ISO 4762" is a claim. A
// pattern whose quantifiers all permit zero matches the bare token and turns
// every mention of a standards body into a recalled-figure banner — which trains
// people to ignore the banner, destroying it for the cases that matter.
//
// Caught by review of the 2026-09-04 additions: the AASHTO pattern was
// `AASHTO\s?[A-Z]{0,4}\s?-?\s?\d{0,3}`, and both quantifiers permitted zero.
func TestStandardsClaim_ABareFamilyNameIsNotAClaim(t *testing.T) {
	for _, bare := range []string{
		"We should check what AASHTO says about this.",
		"The ACI has guidance on that.",
		"Ask whether ASCE covers it.",
		"IPC has a document on the subject.",
		"That is an IEEE matter.",
		"The IBC is the governing code here.",
	} {
		if claims := FindStandardsClaims(&Reply{Speech: bare}); len(claims) > 0 {
			t.Errorf("%q was flagged as a standards claim: %+v.\n"+
				"Naming a standards body is not citing a standard, and a banner on ordinary "+
				"prose is a banner people learn to skip", bare, claims)
		}
	}
}
