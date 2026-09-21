package agent

import (
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// settleDocument holds a document to the rules every document a turn produces
// is held to, whichever path produced it: the unit, the tolerance rule on
// overlays, materials, states, the not-verified fallback, part defaults, the
// bindings and the notes about outlines, parameters and features.
//
// Returns nil for a document with no parts, which renders as a blank viewport and
// reads as a failure.
//
// # Why this is not inside validate()
//
// It was, and validate() runs ONCE, early. Three things install a document into
// the reply after it: resolveEdit (an edit), repairGeometry (all four repairs)
// and a build pass (assemble.go). Each installed what the model typed — a body
// bound to body_width = 2000 drew the 1900 typed beside the expression, and the
// note that says so is written by the step that was skipped. So every producer
// calls this instead, and a later check reads bound numbers rather than stale
// ones. See docs/bugfix/2026-09-11-edited-and-repaired-documents-were-never-settled.md.
//
// # Why it must be idempotent
//
// A repair is sent the whole document, not_verified included, and sends it back;
// settling what comes back would repeat every note. So notes are made distinct,
// and the unitless note is not added twice. Fence: TestSettle_IsIdempotent.
func settleDocument(d *Prototype) *Prototype {
	if d == nil {
		return nil
	}
	// HasGeometry, not len(Parts): a design written as definitions and assemblies
	// has no top-level parts and is not empty (stage D1f). Dropping it here threw
	// away every tree a model wrote.
	if !d.HasGeometry() {
		// An empty prototype renders as a blank viewport, which reads as a
		// failure. Dropping it is more honest than showing nothing.
		return nil
	}
	/* A "car" part is written out here as the tree it is (geometry/car.go, stage C1
	 * of the looks-designed work, 2026-09-18): FIRST, because everything below —
	 * the defaults, the binding, the notes about outlines and features — reads the
	 * tree it becomes, and here because this is the door every producer of a
	 * document goes through. A car it could not build stays a "car" part, which
	 * Faults reports, and its reasons are told like every other note; so is every
	 * proportion outside the car's class, which never stops it being built. */
	for _, problem := range geometry.ExpandTemplates(d) {
		d.NotVerified = append(d.NotVerified, templateNote(problem))
	}
	/* PRD WRK-05: a dimension without its unit will eventually be read in
	 * the wrong one.
	 *
	 * The units field is free text from a model, so it can be missing,
	 * misspelled, or something we cannot convert. An unrecognised unit is NOT
	 * quietly treated as millimetres — a wrong guess about scale is the
	 * difference between a bracket and a building. It is recorded as
	 * unspecified, every dimension then renders as "60 (unit not stated)",
	 * and the reader is told in the one place they are already looking. */
	if _, known := geometry.ParseUnit(d.Units); !known && len(d.Parts) > 0 {
		declared := strings.TrimSpace(d.Units)
		note := "No unit was stated for these dimensions, " + unitlessSuffix
		if declared != "" {
			note = fmt.Sprintf("The unit %q is not one FORGE can convert, ", declared) + unitlessSuffix
		}
		d.Units = ""
		// Said once. Settling clears the unit, so a document settled again — a
		// repair is sent the settled one and sends it back — reads as having NO
		// unit, and would gain a second, differently-worded note about the same
		// fact. Fence: TestSettle_IsIdempotent.
		if !saysUnitless(d.NotVerified) {
			d.NotVerified = append(d.NotVerified, note)
		}
	}
	// PRD VIS-03. Overlays arrive from the model like everything else here,
	// and a dimension line with a tolerance on it is the most authoritative
	// mark that can appear on a render. The storage door refuses a bad one
	// outright; this door drops it and says so, because refusing the whole
	// turn would throw away the shape somebody is waiting on — the same
	// treatment the unrecognised unit gets above.
	//
	// Appended to NotVerified rather than logged, because that is the one
	// place the reader is already looking, and "FORGE tried to state a
	// tolerance and it was removed" is exactly what they need to know about
	// what is in front of them.
	if len(d.Overlays) > 0 {
		kept, dropped := geometry.DrawableOverlays(d.Overlays)
		d.Overlays = kept
		d.NotVerified = append(d.NotVerified, dropped...)
	}
	// PRD VIS-02. Materials and states arrive from the model like everything
	// else here. A material with an unusable finish keeps its NAME and loses
	// its look — the name is the claim — and a state referring to a part
	// that does not exist is dropped, because the viewer would show the
	// assembly unchanged and a reader would take that for the state making
	// no difference.
	for i := range d.Parts {
		if m := d.Parts[i].Material; m != nil {
			if err := m.Validate(); err != nil {
				d.Parts[i].Material = nil
				d.NotVerified = append(d.NotVerified,
					"A material FORGE named could not be read and was dropped: "+err.Error())
			}
		}
		// A process is what turns the manufacturability check on for a part, and it
		// is checked against the same table the contract is written from and the
		// findings are judged by (geometry.Profiles; addresses issue 6). A name that
		// is not in it is DROPPED and said, never guessed at: a part checked against
		// the wrong process gets numbers that look exactly like the right ones.
		if p := d.Parts[i].Process; p != "" && !geometry.ValidProcess(p) {
			d.Parts[i].Process = ""
			d.NotVerified = append(d.NotVerified, fmt.Sprintf(
				"%s says it is made by %q, which FORGE does not know, so it was not checked for "+
					"manufacturability. The processes FORGE checks are: %s.",
				d.Parts[i].Label(), p, geometry.ProcessGuide()))
		}
	}
	// A section that cannot be cut is dropped here, in the document's own words,
	// rather than coming back from the kernel as a refusal per section.
	if len(d.Sections) > 0 {
		kept, dropped := geometry.ValidateSections(d.Sections, d.Parts)
		d.Sections = kept
		d.NotVerified = append(d.NotVerified, dropped...)
	}
	if err := geometry.ValidateStates(d.States, d.Parts); err != nil {
		d.States = nil
		d.NotVerified = append(d.NotVerified,
			"The assembly states FORGE proposed referred to parts that are not in this "+
				"assembly, so none of them is shown. "+err.Error())
	}
	if note := geometry.StatesNotVerified(d.States); note != "" {
		d.NotVerified = append(d.NotVerified, note)
	}
	// PRD VIS-06 as an invariant rather than an instruction: geometry
	// without a statement of what it does not establish is exactly the
	// render that gets mistaken for an analysis.
	if len(d.NotVerified) == 0 {
		d.NotVerified = []string{NotVerifiedFallback}
	}
	for i := range d.Parts {
		p := &d.Parts[i]
		if p.ID == "" {
			p.ID = fmt.Sprintf("part-%d", i+1)
		}
		if len(p.Position) != 3 {
			p.Position = []float64{0, 0, 0}
		}
		if len(p.Rotation) != 3 {
			p.Rotation = []float64{0, 0, 0}
		}
		if p.Opacity <= 0 || p.Opacity > 1 {
			p.Opacity = 1
		}
		if p.Color == "" {
			p.Color = "#b8bcc4"
		}
	}
	/* The parametric model, resolved and APPLIED (waves 10 and 11).
	 *
	 * Bind evaluates the document's expressions and writes the results into
	 * the numbers the renderer reads, so a part whose width follows
	 * plate_size actually follows it. It returns everything Resolve would
	 * have reported plus anything wrong with the bindings themselves, which
	 * is why there is one call here and not two.
	 *
	 * It runs LAST in this block because it needs what the loop above
	 * guarantees: every part has an id (Bind names parts by their label) and
	 * a three-element position to write an axis into.
	 *
	 * None of what it reports changes a pixel — a document whose parameters
	 * do not resolve renders exactly like one whose parameters do — which is
	 * precisely why it has to be said. Appended to NotVerified for the same
	 * reason as the dropped tolerances and the unconvertible unit above: it
	 * is the one place the reader is already looking. */
	for _, problem := range d.Bind() {
		d.NotVerified = append(d.NotVerified, parameterNote(problem))
	}
	/* And what relationship checking could NOT check, and why (issues 9, 10
	 * and 11; geometry/relationships.go).
	 *
	 * Wave 13 checked a distance between two bound positions and said nothing
	 * about anything else, so a document whose only "relationships" were a
	 * parameter nothing reads and four holes at typed coordinates came back
	 * indistinguishable from one that had been checked. Silence there is not
	 * neutral: it is read as a clean result.
	 *
	 * Here for the same reason Bind's problems are here — none of it changes a
	 * pixel, and this is the one place the reader is already looking. */
	for _, problem := range d.RelationshipProblems() {
		d.NotVerified = append(d.NotVerified, relationshipNote(problem))
	}
	/* Features, and the one place the picture and the file disagree.
	 *
	 * A feature that does not check out is dropped by the kernel rather than
	 * approximated, so the reader has to be told which — an assembly missing
	 * a hole somebody asked for is not something the render will show.
	 *
	 * And the viewport has no boolean operations, so it cannot make the
	 * void. It draws the tool as a faint ghost rather than as a solid post
	 * — which is the opposite of what a hole is — and says so here. A real
	 * divergence between two things this product shows the same person,
	 * stated for the same reason "Drawn approximately" is. */
	/* An outline nothing could read is a part that is simply NOT THERE, and
	 * the render looks like a design with a piece missing rather than like
	 * an error. Its own voice, because "a number is missing" and "a whole
	 * part is absent" are different things to be told. */
	for _, problem := range d.ProfileProblems() {
		d.NotVerified = append(d.NotVerified, profileNote(problem))
	}
	if _, featureProblems := d.Operations(); len(featureProblems) > 0 {
		for _, problem := range featureProblems {
			d.NotVerified = append(d.NotVerified, featureNote(problem))
		}
	}
	d.NotVerified = append(d.NotVerified, d.FeatureNotes()...)

	d.NotVerified = distinctNotes(d.NotVerified)
	return d
}

// unitlessSuffix ends both wordings of the note about a unit FORGE cannot use,
// so one test can tell whether a document has been told already.
const unitlessSuffix = "so every number here is unitless."

func saysUnitless(notes []string) bool {
	for _, n := range notes {
		if strings.HasSuffix(n, unitlessSuffix) {
			return true
		}
	}
	return false
}

// distinctNotes keeps the first of each identical note, in order.
func distinctNotes(notes []string) []string {
	seen := make(map[string]bool, len(notes))
	out := notes[:0]
	for _, n := range notes {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}
