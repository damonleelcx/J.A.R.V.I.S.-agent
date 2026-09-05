package geometry

import (
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
)

// Engineering overlays (PRD VIS-03).
//
// The rule these are all built around: FORGE may derive a dimension from the
// model and may never originate a tolerance, because nothing about a shape
// implies one. A tolerance is read as an instruction to a machinist.

func dimension() Overlay {
	return Overlay{
		ID: "d1", Kind: Dimension, Label: "bore",
		From: []float64{0, 0, 0}, To: []float64{12, 0, 0},
		Value: 12, Unit: "mm", How: claim.Observed, Source: "drawing 41-A rev 3",
	}
}

// The whole point of the file: only labels that mean "from outside FORGE" may
// carry a tolerance.
func TestAToleranceFORGEDerivedIsAToleranceFORGEInvented(t *testing.T) {
	for _, how := range []claim.Epistemic{
		claim.Calculated, claim.Inferred, claim.Assumed, claim.Proposed, claim.Simulated,
	} {
		o := dimension()
		o.How = how
		o.Tolerance = "±0.05"

		err := o.Validate()
		if err == nil {
			t.Errorf("a tolerance labelled %q was accepted.\n"+
				"Nothing about a shape implies a tolerance — it is a manufacturing decision about "+
				"process and fit — so a derived one is an invented one, and it is read as an "+
				"instruction to a machinist", how)
			continue
		}
		if !strings.Contains(err.Error(), "manufacturing") {
			t.Errorf("%s: the refusal does not say why a tolerance cannot be derived: %v", how, err)
		}
	}

	// And the two that may: they mean somebody else established it.
	for _, how := range ToleranceLabels() {
		o := dimension()
		o.How = how
		o.Tolerance = "±0.05"
		if err := o.Validate(); err != nil {
			t.Errorf("a tolerance labelled %q with a source was refused: %v\n"+
				"Refusing these would mean a drawing's own tolerances cannot be shown, which is "+
				"the requirement rather than a violation of it", how, err)
		}
	}
}

// A tolerance from outside FORGE has something to point at, and must.
func TestAToleranceWithoutASourceIsUnfalsifiable(t *testing.T) {
	o := dimension()
	o.Tolerance = "H7"
	o.Source = "   "

	err := o.Validate()
	if err == nil {
		t.Fatal("a tolerance was stored as observed with no source.\n" +
			"That label claims somebody established it, so there is something to point at; " +
			"without one the claim cannot be checked in the one place being wrong is expensive")
	}
	if !strings.Contains(err.Error(), "source") {
		t.Errorf("the refusal does not name what is missing: %v", err)
	}
}

// PRD WRK-05, on the surface where a number is most likely to be copied off.
func TestADimensionWithoutAUnitIsRefused(t *testing.T) {
	for _, unit := range []string{"", "furlongs", "  "} {
		o := dimension()
		o.Unit = unit
		if err := o.Validate(); err == nil {
			t.Errorf("a dimension in %q was accepted; nothing downstream could know what it meant", unit)
		}
	}
	// A known unit is normalised to its canonical spelling rather than kept as
	// typed, so two renders of the same model do not disagree in the legend.
	o := dimension()
	o.Unit = "millimetres"
	if err := o.Validate(); err != nil {
		t.Fatalf("a spelled-out unit was refused: %v", err)
	}
	if o.Unit != "mm" {
		t.Errorf("unit stayed %q rather than normalising", o.Unit)
	}
}

// An unlabelled overlay is downgraded, not refused — matching claim.Validate.
// Refusing would teach people to write "observed" to get past the check.
func TestAnUnlabelledOverlayIsDowngradedRatherThanRefused(t *testing.T) {
	o := dimension()
	o.How = ""
	o.Source = ""
	if err := o.Validate(); err != nil {
		t.Fatalf("an unlabelled dimension was refused: %v", err)
	}
	if o.How != claim.Assumed {
		t.Errorf("downgraded to %q; the weakest label is the safe direction", o.How)
	}
	// But the downgrade must not become a way to smuggle a tolerance: assumed
	// is not a tolerance label, so it is now refused for that reason instead.
	o2 := dimension()
	o2.How = ""
	o2.Tolerance = "±0.1"
	if err := o2.Validate(); err == nil {
		t.Error("an unlabelled overlay carrying a tolerance was downgraded to assumed and then " +
			"accepted; the downgrade must not be a way past the tolerance rule")
	}
}

// The shape checks: a mark nobody can read is not an overlay.
func TestAnOverlayMustBeReadable(t *testing.T) {
	unknown := dimension()
	unknown.Kind = "callout"
	if err := unknown.Validate(); err == nil {
		t.Error("an unknown overlay kind was accepted")
	}
	unlabelled := dimension()
	unlabelled.Label = " "
	if err := unlabelled.Validate(); err == nil {
		t.Error("an overlay with no label was accepted; a reader would have to decode it from position")
	}
	short := dimension()
	short.To = []float64{1, 2}
	if err := short.Validate(); err == nil {
		t.Error("a dimension with a two-component endpoint was accepted")
	}
	// A datum needs neither span nor unit — it marks a reference, not a length.
	datum := Overlay{ID: "a", Kind: Datum, Label: "A", From: []float64{0, 0, 0}, How: claim.Proposed}
	if err := datum.Validate(); err != nil {
		t.Errorf("a datum was refused for lacking a dimension's fields: %v", err)
	}
	// Ids must be unique — the viewer addresses them.
	if err := ValidateOverlays([]Overlay{dimension(), dimension()}); err == nil {
		t.Error("two overlays shared an id")
	}
}

// What FORGE derives, and what it refuses to derive.
func TestMeasureDerivesExtentsAndNothingElse(t *testing.T) {
	doc := Document{
		Name: "bracket", Units: "mm",
		Parts: []Part{
			{ID: "base", Shape: "box", Size: map[string]float64{"width": 40, "height": 6, "depth": 20},
				Position: []float64{0, 0, 0}},
			{ID: "post", Shape: "cylinder", Size: map[string]float64{"radius": 4, "height": 30},
				Position: []float64{10, 18, 0}},
		},
		Assumptions: []string{"wall thickness", "hole spacing"},
	}
	got := Measure(doc, "mm")
	if len(got) != 3 {
		t.Fatalf("expected three extents, got %d: %+v", len(got), got)
	}
	for _, o := range got {
		if o.Kind != Dimension {
			t.Errorf("%s: derived a %q; FORGE must not invent datums — which face a part is measured "+
				"from is a decision about how it is made and held", o.Label, o.Kind)
		}
		if o.Tolerance != "" {
			t.Errorf("%s: derived the tolerance %q, which is the one thing this must never do",
				o.Label, o.Tolerance)
		}
		if o.How != claim.Calculated {
			t.Errorf("%s: labelled %q; extents are arithmetic over values the document carries",
				o.Label, o.How)
		}
		if o.Source == "" {
			t.Errorf("%s: calculated with no derivation named", o.Label)
		}
		if o.Unit != "mm" {
			t.Errorf("%s: unit %q", o.Label, o.Unit)
		}
		// The honesty that makes a derived dimension safe to show: it measured
		// the model, and the model was largely assumed.
		if !strings.Contains(o.Note, "not from a part") {
			t.Errorf("%s: the note does not say what was measured: %q", o.Label, o.Note)
		}
		if !strings.Contains(o.Note, "assumed") {
			t.Errorf("%s: two dimensions were assumed and the note does not say so: %q", o.Label, o.Note)
		}
	}

	// The arithmetic itself. Width spans the base; height reaches the top of the
	// post; depth is the base's.
	byLabel := map[string]float64{}
	for _, o := range got {
		byLabel[o.Label] = o.Value
	}
	if byLabel["overall width"] != 40 {
		t.Errorf("width = %v, expected 40", byLabel["overall width"])
	}
	if byLabel["overall height"] != 36 {
		t.Errorf("height = %v, expected 36 (post centre 18 + half of 30)", byLabel["overall height"])
	}
	if byLabel["overall depth"] != 20 {
		t.Errorf("depth = %v, expected 20", byLabel["overall depth"])
	}

	// A flat axis is omitted rather than reported as zero: a zero-length
	// dimension reads as a measurement that came out at zero.
	flat := Document{Parts: []Part{{ID: "p", Shape: "plane",
		Size: map[string]float64{"width": 10, "depth": 10}, Position: []float64{0, 0, 0}}}}
	for _, o := range Measure(flat, "mm") {
		if o.Label == "overall height" {
			t.Error("a zero-height model produced a height dimension")
		}
	}
	if len(Measure(Document{}, "mm")) != 0 {
		t.Error("a document with no parts produced dimensions")
	}
}

// The conversation door drops rather than refuses, and says what it removed.
func TestDrawableOverlaysDropsAndExplains(t *testing.T) {
	good := dimension()
	invented := dimension()
	invented.ID = "d2"
	invented.Label = "post diameter"
	invented.How = claim.Calculated
	invented.Tolerance = "±0.02"

	kept, dropped := DrawableOverlays([]Overlay{good, invented})
	if len(kept) != 1 || kept[0].ID != "d1" {
		t.Fatalf("kept %+v; the good overlay must survive a bad neighbour", kept)
	}
	if len(dropped) != 1 {
		t.Fatalf("dropped %v", dropped)
	}
	// The sentence has to tell a reader what FORGE tried to do. "An overlay was
	// invalid" leaves them wondering what is missing from the render.
	for _, want := range []string{"±0.02", "post diameter", "manufacturing decision"} {
		if !strings.Contains(dropped[0], want) {
			t.Errorf("the explanation does not mention %q: %q", want, dropped[0])
		}
	}
}

// The storage door refuses a variant carrying an invented tolerance.
//
// # Why this exists on top of the tests above
//
// They call Overlay.Validate directly, so they pass whether or not anything
// calls it. This package's own history and this codebase's — SAF-02, RSN-02,
// SEC-05 — is that a rule with no call site is a comment. NewVariant.Validate is
// the one door a stored variant comes through.
func TestStoringAVariantRefusesAnInventedTolerance(t *testing.T) {
	invented := dimension()
	invented.How = claim.Assumed
	invented.Tolerance = "±0.05"

	n := &NewVariant{
		InitiatorID: "usr_1", Agent: workspace.AgentSystem, Generator: "test-model",
		Inputs: []byte(`{}`),
		Document: Document{
			Name: "bracket", Units: "mm",
			Parts:       []Part{{ID: "p", Shape: "box", Size: map[string]float64{"width": 10}}},
			NotVerified: []string{"nothing checked"},
			Overlays:    []Overlay{invented},
		},
	}
	if err := n.Validate(); err == nil {
		t.Fatal("a variant carrying a tolerance FORGE derived was accepted for storage.\n" +
			"Overlay.Validate may be correct and simply not called from the door a variant " +
			"comes through")
	}

	// And the same variant with the overlay removed still stores, so the check
	// is refusing the tolerance rather than the feature.
	n.Document.Overlays = nil
	if err := n.Validate(); err != nil {
		t.Fatalf("a variant with no overlays was refused: %v", err)
	}
	// As does one whose tolerance came from a drawing.
	n.Document.Overlays = []Overlay{dimension()}
	n.Document.Overlays[0].Tolerance = "±0.05"
	if err := n.Validate(); err != nil {
		t.Fatalf("a variant carrying a drawing's own tolerance was refused: %v", err)
	}
}

// An extrusion's extent is its OUTLINE's, which is not symmetric about the
// part's position.
//
// # The defect this holds
//
// Extents were computed as position ± a symmetric half-extent, which is exact
// for every primitive because they are centred by construction. A profile is
// deliberately NOT re-centred (profile.go), so an L-bracket drawn from (0,0) to
// (40,40) sits entirely on the positive side of its own origin — and the
// symmetric form described it as reaching 20 mm the wrong way, so Measure drew
// a dimension line against extents nothing has.
//
// A drill disabled the extrusion case in localBox and every existing test
// stayed green, because none of them had an extrusion in it.
func TestMeasureUsesAnOutlinesOwnExtentAndNotASymmetricGuess(t *testing.T) {
	doc := Document{
		Name: "angle", Units: "mm",
		Parts: []Part{{ID: "a", Shape: "extrusion",
			Profile: []Point{{X: 0, Y: 0}, {X: 40, Y: 0}, {X: 40, Y: 8}, {X: 8, Y: 8},
				{X: 8, Y: 40}, {X: 0, Y: 40}},
			Size:     map[string]float64{"depth": 20},
			Position: []float64{0, 0, 0}}},
	}
	got := Measure(doc, "mm")
	if len(got) != 3 {
		t.Fatalf("expected three extents, got %d", len(got))
	}
	// The outline spans 40 in x and 40 in y, and the depth 20 is centred.
	want := map[int][2]float64{0: {0, 40}, 1: {0, 40}, 2: {-10, 10}}
	for axis, w := range want {
		o := got[axis]
		if math.Abs(o.From[axis]-w[0]) > 1e-9 || math.Abs(o.To[axis]-w[1]) > 1e-9 {
			t.Errorf("axis %d measured %v..%v, want %v..%v — a symmetric half-extent puts "+
				"this part where it is not", axis, o.From[axis], o.To[axis], w[0], w[1])
		}
	}
}
