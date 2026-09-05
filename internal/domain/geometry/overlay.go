package geometry

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Engineering overlays on a render (PRD VIS-03).
//
// # The requirement's two halves
//
// "Engineering overlays without confusing appearance with validated data." The
// second half already had an answer before this file existed — VIS-06's render
// label is permanent and undismissible. This is the first half, and building it
// makes the second half harder rather than easier: a dimension line with a
// number on it is the most authoritative-looking mark you can put on a drawing.
// It is what a drawing IS. Somebody who would never trust a rendered shape will
// read "42.00 mm ±0.05" off a picture and act on it.
//
// # Where an overlay's number comes from, and why that decides everything
//
// There are three kinds, and they are not the same kind of thing.
//
// A dimension MEASURED FROM THE MODEL can be computed exactly — the parts carry
// sizes and positions, and arithmetic over them is arithmetic. That is
// `calculated`, and the derivation can be shown. What it is NOT is a
// measurement of a part: it measures the model, and the model's sizes are
// largely what FORGE assumed because nobody said. A calculated value over
// assumed inputs is still assumed at the bottom, so every derived dimension
// says what it measured.
//
// A dimension STATED BY SOMEBODY — a bore that is 12.00 mm because a drawing
// says so — is `observed` or `retrieved`, and carries where it came from.
//
// A TOLERANCE is neither, and this is the whole design.
//
// # Why FORGE may never originate a tolerance
//
// There is nothing in a shape from which a tolerance follows. It is not a
// property of geometry; it is a manufacturing requirement, decided by somebody
// who knows the process, the fit, and what happens if the part is at the far
// end of its range. No amount of information about a cylinder tells you whether
// it is ±0.5 or ±0.005, and those two are different parts with different costs
// and different failure modes.
//
// So a model asked for a tolerance cannot derive one — it can only produce a
// plausible one. That is precisely PRD RSN-06's "no fabricated measurements",
// and it is the most dangerous single value this system could emit, because a
// tolerance is read as an instruction to a machinist.
//
// The rule is therefore mechanical rather than advisory: a tolerance is refused
// unless it is `observed` or `retrieved` AND names its source. Those are the two
// labels that mean "this came from outside FORGE", and the source is what makes
// the claim checkable. Every other label — calculated, inferred, assumed,
// proposed — is FORGE talking, and FORGE has nothing to say here.
//
// A DATUM is a statement of intent: which face measurements are taken from.
// FORGE can propose one and cannot know one, so a datum it originates is
// `proposed` and says so on the render.

// OverlayKind is what an overlay marks.
type OverlayKind string

const (
	// Dimension is a measured distance between two points.
	Dimension OverlayKind = "dimension"
	// Datum is a labelled reference feature that dimensions are taken from.
	Datum OverlayKind = "datum"
	// Note is an annotation pinned to a point on the model (PRD VIS-02).
	//
	// A kind here rather than a mechanism of its own. An annotation is anchored
	// text with a provenance, which is what this file already draws — and
	// routing it through the same table means a note FORGE wrote carries the
	// same label as a dimension FORGE guessed, instead of arriving as unmarked
	// prose floating over a render.
	Note OverlayKind = "note"
)

// Overlay is one engineering annotation drawn over the model.
//
// One struct with a kind rather than two types, because the list is rendered as
// one list, travels as one JSON array, and is validated by one table. What each
// kind requires is in overlayKinds below, where the rules are readable side by
// side instead of scattered through a switch.
type Overlay struct {
	ID    string      `json:"id"`
	Kind  OverlayKind `json:"kind"`
	Label string      `json:"label"`

	// From and To are points in model space. A dimension spans them; a datum
	// uses From as its position and To as the direction it faces.
	From []float64 `json:"from"`
	To   []float64 `json:"to"`

	// Value and Unit are the dimension's magnitude. Unit is required alongside
	// Value and never inferred from context — PRD WRK-05 asks for unit integrity
	// per value, and a number whose unit is "whatever the document said" is the
	// failure that requirement exists to prevent.
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`

	// Tolerance is drawn exactly as given: "±0.05", "+0.1/-0", "H7". FORGE never
	// writes one. See the file comment.
	Tolerance string `json:"tolerance,omitempty"`

	// How is where this came from, and Source is what it points at.
	How    claim.Epistemic `json:"how"`
	Source string          `json:"source,omitempty"`
	// Note is what a reader needs alongside the number — for a derived
	// dimension, what was actually measured.
	Note string `json:"note,omitempty"`
}

// overlayKinds is what each kind requires.
var overlayKinds = []struct {
	Kind OverlayKind
	// NeedsSpan marks a kind that is meaningless without two points.
	NeedsSpan bool
	// NeedsValue marks a kind that carries a magnitude and therefore a unit.
	NeedsValue bool
	Gloss      string
}{
	{Dimension, true, true, "a measured distance between two points on the model"},
	{Datum, false, false, "a labelled reference feature that dimensions are taken from"},
	{Note, false, false, "a comment pinned to a point on the model"},
}

func (k OverlayKind) def() (bool, bool, string, bool) {
	for _, d := range overlayKinds {
		if d.Kind == k {
			return d.NeedsSpan, d.NeedsValue, d.Gloss, true
		}
	}
	return false, false, "", false
}

// Valid reports whether k is a known kind.
func (k OverlayKind) Valid() bool { _, _, _, ok := k.def(); return ok }

// ToleranceLabels are the only two origins a tolerance may carry.
//
// Both mean "this came from outside FORGE", and both are checkable: observed
// points at something FORGE saw in this deployment, retrieved at a source
// somebody else can open. Everything else on the epistemic scale is FORGE
// reasoning, and no amount of reasoning produces a tolerance.
func ToleranceLabels() []claim.Epistemic { return []claim.Epistemic{claim.Observed, claim.Retrieved} }

// Validate checks one overlay before it is stored or drawn.
//
// Unlabelled overlays are DOWNGRADED to assumed rather than refused, matching
// claim.Claim.Validate: a missing label is a mistake, and refusing the whole
// render over it teaches people to write "observed" to get past the check. A
// tolerance is the exception, and is refused, because there the label is the
// entire content of the claim.
func (o *Overlay) Validate() error {
	const op = "geometry.Overlay.Validate"

	needsSpan, needsValue, _, known := o.Kind.def()
	if !known {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("overlay %q has kind %q, which is not one this build draws; expected %s",
				o.Label, o.Kind, strings.Join(kindNames(), " or "))
	}
	if strings.TrimSpace(o.Label) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("an overlay needs a label; an unlabelled mark on a drawing is noise a reader " +
				"has to decode from position alone")
	}
	if !o.How.Valid() {
		// Downgraded, not refused. The weakest label is the safe direction.
		o.How = claim.Assumed
	}
	if needsSpan && (len(o.From) != 3 || len(o.To) != 3) {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("dimension %q needs two three-component points to span; got %d and %d",
				o.Label, len(o.From), len(o.To))
	}
	if needsValue {
		if o.Value <= 0 || math.IsNaN(o.Value) || math.IsInf(o.Value, 0) {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("dimension %q has value %v, which is not a length", o.Label, o.Value)
		}
		// PRD WRK-05. A magnitude with no unit is the failure that requirement
		// names, and it is not fixable downstream: nothing later in the pipeline
		// knows whether 42 was millimetres or inches.
		if u, ok := ParseUnit(o.Unit); !ok {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("dimension %q is %v in %q, which is not a unit this build knows. A magnitude "+
					"with no unit cannot be corrected later — nothing downstream knows what it meant",
					o.Label, o.Value, o.Unit)
		} else {
			o.Unit = string(u)
		}
	}

	if strings.TrimSpace(o.Tolerance) != "" {
		if !isToleranceLabel(o.How) {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("overlay %q carries the tolerance %q labelled %q. A tolerance does not follow "+
					"from a shape — it is a manufacturing requirement decided by somebody who knows the "+
					"process and the fit, and nothing about this geometry implies one. Only %s may carry "+
					"a tolerance, because those are the two labels that mean it came from outside FORGE. "+
					"A tolerance FORGE derived is a tolerance FORGE invented, and it is read as an "+
					"instruction to a machinist",
					o.Label, o.Tolerance, o.How, labelList())
		}
		if strings.TrimSpace(o.Source) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("overlay %q carries the tolerance %q as %q and names no source. That label "+
					"means somebody else established it, so there is something to point at; without it "+
					"the claim is unfalsifiable in the one place where being wrong is expensive",
					o.Label, o.Tolerance, o.How)
		}
	}
	return nil
}

func isToleranceLabel(e claim.Epistemic) bool {
	for _, allowed := range ToleranceLabels() {
		if e == allowed {
			return true
		}
	}
	return false
}

func labelList() string {
	var out []string
	for _, e := range ToleranceLabels() {
		out = append(out, string(e))
	}
	return strings.Join(out, " and ")
}

func kindNames() []string {
	var out []string
	for _, d := range overlayKinds {
		out = append(out, string(d.Kind))
	}
	return out
}

// ValidateOverlays checks a document's overlays, returning the first problem.
//
// Ids must be unique for the same reason task keys must: the viewer addresses
// them, and two overlays answering to one id means one of them is unreachable.
func ValidateOverlays(overlays []Overlay) error {
	const op = "geometry.ValidateOverlays"

	seen := map[string]bool{}
	for i := range overlays {
		if err := overlays[i].Validate(); err != nil {
			return err
		}
		id := overlays[i].ID
		if id == "" {
			continue
		}
		if seen[id] {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("two overlays share the id %q", id)
		}
		seen[id] = true
	}
	return nil
}

// ---------------------------------------------------------------------------
// What FORGE may derive, and what it has to say about it
// ---------------------------------------------------------------------------

// Measure derives dimensions from the document's own parts.
//
// # Why derive any at all
//
// Without this, overlays exist only when the model volunteers them, which makes
// the feature a lottery: the same goal produces a dimensioned render on one roll
// and a bare shape on the next. Extents are arithmetic over values the document
// already carries, so this is the one part of VIS-03 that can be exact.
//
// # What it deliberately does not do
//
// It measures the MODEL. Every value here is `calculated` from part sizes and
// positions, and those sizes are largely what FORGE assumed because nobody said
// — Document.Assumptions is the list. A calculated value over assumed inputs is
// assumed at the bottom, and the note on each dimension says which, so a reader
// who takes 42.00 off the render knows they are reading FORGE's own arithmetic
// over FORGE's own guess.
//
// It produces no tolerances. There is nothing here to derive one from.
//
// It produces no datums either. A datum says which face somebody measures FROM,
// which is a decision about how the part is made and held; picking one from
// bounding boxes would be inventing intent, and it would look exactly like a
// datum somebody chose.
func Measure(doc Document, unit Unit) []Overlay {
	if len(doc.Parts) == 0 {
		return nil
	}
	min, max := bounds(doc)

	// Overall extents, one per axis. Three marks rather than one, because a
	// single "size" on an assembly answers no question anybody asks.
	axes := []struct {
		i     int
		name  string
		label string
	}{
		{0, "X", "overall width"},
		{1, "Y", "overall height"},
		{2, "Z", "overall depth"},
	}

	assumedNote := "measured from the model, not from a part."
	if len(doc.Assumptions) > 0 {
		assumedNote += fmt.Sprintf(" %d of the model's dimensions were assumed rather than given, "+
			"so this is arithmetic over a guess", len(doc.Assumptions))
	}

	out := make([]Overlay, 0, len(axes))
	for _, a := range axes {
		span := max[a.i] - min[a.i]
		if span <= 0 {
			// A flat assembly on this axis. Reported by omission rather than as a
			// zero: a dimension line of length zero reads as a measurement that
			// came out at zero, which is a different claim.
			continue
		}
		from := []float64{min[0], min[1], min[2]}
		to := []float64{min[0], min[1], min[2]}
		to[a.i] = max[a.i]

		out = append(out, Overlay{
			ID:    "measured-" + strings.ToLower(a.name),
			Kind:  Dimension,
			Label: a.label,
			From:  from,
			To:    to,
			Value: round(span, 3),
			Unit:  string(unit),
			// Calculated, and the derivation can be shown: it is max minus min
			// over the part positions and sizes in this document.
			How:    claim.Calculated,
			Source: "the part sizes and positions in this document",
			Note:   assumedNote,
		})
	}
	return out
}

// bounds returns the model's extent, using the same part sizing the tessellator
// uses so a dimension cannot disagree with the shape it is drawn against.
func bounds(doc Document) (min, max [3]float64) {
	min = [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	max = [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}

	for _, p := range doc.Parts {
		pos := padTo3(p.Position)
		loLocal, hiLocal := localBox(p)
		for i := 0; i < 3; i++ {
			if lo := pos[i] + loLocal[i]; lo < min[i] {
				min[i] = lo
			}
			if hi := pos[i] + hiLocal[i]; hi > max[i] {
				max[i] = hi
			}
		}
	}
	return min, max
}

// halfExtent is a part's half-size on each axis, before rotation.
//
// Rotation is deliberately ignored, and that is a limitation rather than an
// oversight: an axis-aligned box around a rotated part is larger than the part,
// so a derived overall dimension can read high. Correcting it means bounding the
// tessellated triangles, which is the honest fix and a larger one. Until then
// the note on every derived dimension says it measures the model.
// localBox is a part's extent in its OWN frame, as a low corner and a high one.
//
// # Why not a half-extent
//
// This returned a single symmetric half-extent until extrusions existed, and the
// caller did pos ± half. Every primitive is centred on its own position, so that
// was exact.
//
// A profile is not. Its coordinates are written by hand and deliberately not
// re-centred (profile.go), so an L-bracket drawn from (0,0) to (40,40) sits
// entirely on the positive side of its own origin. A symmetric half-extent
// describes that part as reaching 20 mm in the wrong direction, and Measure
// would draw a dimension line against extents nothing has.
//
// Two corners cost one extra return value and are exact for both.
func localBox(p Part) (min, max [3]float64) {
	s := p.Size
	get := func(k string, fallback float64) float64 {
		if v, ok := s[k]; ok && v > 0 {
			return v
		}
		return fallback
	}
	sym := func(h [3]float64) ([3]float64, [3]float64) {
		return [3]float64{-h[0], -h[1], -h[2]}, h
	}
	switch strings.ToLower(p.Shape) {
	case "sphere":
		r := get("radius", 0.5)
		return sym([3]float64{r, r, r})
	case "cylinder", "cone":
		r := math.Max(get("radius", 0.5), get("radius_top", 0))
		h := get("height", 1) / 2
		return sym([3]float64{r, h, r})
	case "plane":
		return sym([3]float64{get("width", 1) / 2, 0, get("depth", 1) / 2})
	case "extrusion":
		lo, hi, ok := profileExtent(p)
		if !ok {
			// An outline nothing could read contributes nothing rather than a
			// default box: a made-up extent would be drawn as a dimension.
			return [3]float64{}, [3]float64{}
		}
		d := get("depth", 1) / 2
		return [3]float64{lo[0], lo[1], -d}, [3]float64{hi[0], hi[1], d}
	default:
		return sym([3]float64{get("width", 1) / 2, get("height", 1) / 2, get("depth", 1) / 2})
	}
}

// profileExtent is an outline's own bounding rectangle, from the literal
// coordinates.
//
// Expressions are NOT evaluated here: this is the measurement path, which runs
// on a stored document without the parameter context, and a coordinate it cannot
// read must not become a zero that silently shrinks the part. A profile written
// entirely in expressions therefore contributes no measured extent, which is a
// miss rather than a wrong number.
func profileExtent(p Part) (min, max [2]float64, ok bool) {
	min = [2]float64{math.Inf(1), math.Inf(1)}
	max = [2]float64{math.Inf(-1), math.Inf(-1)}
	for _, pt := range p.Profile {
		if pt.XFrom != "" || pt.YFrom != "" {
			return min, max, false
		}
		min[0] = math.Min(min[0], pt.X)
		min[1] = math.Min(min[1], pt.Y)
		max[0] = math.Max(max[0], pt.X)
		max[1] = math.Max(max[1], pt.Y)
	}
	return min, max, len(p.Profile) >= minProfilePoints
}

// DrawableOverlays keeps the overlays that may be shown and reports what was
// removed, in words a reader can act on.
//
// # Why this exists alongside ValidateOverlays
//
// Two doors, and they must not behave the same way. Storing a variant is a
// deliberate act with a person behind it, so a bad overlay refuses the whole
// write — nothing is lost, and the caller is told exactly what to fix.
//
// A conversation is different. Geometry arrives mid-turn from a model, and
// refusing the turn over one malformed dimension throws away a shape somebody
// was waiting on and invites a retry that is no more likely to be right. So the
// offending overlay is DROPPED and the drop is stated, which is the same
// treatment the unrecognised-unit case gets one branch above it in converse.go:
// degrade the part that is wrong, tell the reader, keep the rest.
//
// What must never happen in either door is the third option — drawing it.
func DrawableOverlays(overlays []Overlay) (kept []Overlay, dropped []string) {
	seen := map[string]bool{}
	for i := range overlays {
		o := overlays[i]
		if err := o.Validate(); err != nil {
			dropped = append(dropped, describeDrop(o, err))
			continue
		}
		if o.ID != "" && seen[o.ID] {
			dropped = append(dropped, fmt.Sprintf(
				"An overlay labelled %q repeated the id %q and was not drawn.", o.Label, o.ID))
			continue
		}
		if o.ID != "" {
			seen[o.ID] = true
		}
		kept = append(kept, o)
	}
	return kept, dropped
}

// describeDrop says what was removed and why, for a reader rather than a log.
//
// A tolerance gets its own sentence. "An overlay was invalid" would leave
// somebody wondering what the render is missing; the point of removing an
// invented tolerance is lost unless the reader learns that FORGE tried to state
// one — that is the thing they most need to know about what they are looking at.
func describeDrop(o Overlay, err error) string {
	if strings.TrimSpace(o.Tolerance) != "" && !isToleranceLabel(o.How) {
		return fmt.Sprintf(
			"FORGE stated the tolerance %q on %q. Nothing about this geometry implies a tolerance — "+
				"it is a manufacturing decision, not a property of a shape — so it was removed rather "+
				"than drawn. A tolerance here must come from a drawing or a specification, and say which.",
			o.Tolerance, o.Label)
	}
	label := o.Label
	if label == "" {
		label = string(o.Kind)
	}
	return fmt.Sprintf("An overlay on %q was not drawn: %s", label, firstSentence(err))
}

func firstSentence(err error) string {
	var e *errs.Error
	if errors.As(err, &e) && e.Detail != "" {
		if i := strings.Index(e.Detail, ". "); i > 0 {
			return e.Detail[:i+1]
		}
		return e.Detail
	}
	return err.Error()
}
