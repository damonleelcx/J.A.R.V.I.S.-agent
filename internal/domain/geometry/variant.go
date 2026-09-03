package geometry

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// A variant, and the six things PRD VIS-04 makes it carry.
//
// # The requirement, read literally
//
// "Variants side by side; each render links to geometry version, inputs, units,
// assumptions, generator, verification status."
//
// So a render that cannot state one of the six is REFUSED at write time rather
// than stored with a hole. This is WRK-04's rule one level up, and for the same
// reason: a provenance panel with a blank field looks like provenance and
// answers none of the questions one is displayed for. The reader cannot tell
// "there were no assumptions" from "nobody wrote them down", and those are
// opposite facts.
//
// # A variant is a version
//
// There is no separate variant identity. The geometry hangs off an artifact
// version (kind `model`), which already carries five of the six as columns. See
// migration 0011 for why that is the whole design rather than a shortcut.

// Variant is one stored geometry, with its provenance resolved.
type Variant struct {
	// --- 1. geometry version ---
	VersionID  string
	ArtifactID string
	ProjectID  string
	// Path is the artifact this variant is a version OF. Successive proposals
	// of the same assembly share it, which is what makes "v1 and v3 of the
	// bracket" a comparison rather than a coincidence.
	Path    string
	Version int

	Name     string
	Document Document

	// --- 2. inputs: what it was made from ---
	Inputs json.RawMessage

	// --- 3. units, resolved and as declared ---
	//
	// Both, because "no unit was stated" and "a unit was stated that FORGE
	// cannot convert" are different failures and one field collapses them.
	Units         Unit
	UnitsDeclared string
	// Frame is the coordinate frame the positions are in (PRD WRK-05).
	Frame Frame

	// --- 4. assumptions live in Document.Assumptions ---
	//     Not copied out. One truth: the list a reader sees is the list that was
	//     stored, not a projection of it that can fall behind.

	// --- 5. generator ---
	//
	// Generator is WHAT DREW IT — the model, by id. Agent is which part of
	// FORGE acted. They answer different questions and diverge the moment the
	// model behind the workbench changes, so both are kept.
	Generator string
	Agent     workspace.Agent

	// --- 6. verification status ---
	//
	// Two fields, never merged: Verification is what a machine found and
	// Disposition is what a person decided (PRD SAF-05). A single "approved"
	// column would eventually let a passing test suite assert that somebody
	// signed off on something nobody looked at.
	Verification     workspace.Verification
	VerificationNote string
	Disposition      workspace.Disposition
	DispositionedBy  *string

	// InitiatorID is the person whose intent this serves — WRK-04's first fact,
	// carried through so a variant list can say whose proposal it was.
	InitiatorID string
	CreatedAt   time.Time
}

// Assumptions is what FORGE chose rather than was told, never nil.
//
// A nil slice and an empty one mean different things to a JSON encoder and the
// same thing to a reader, so the boundary settles it once: an empty list is the
// statement "nothing was assumed", and it is rendered as one.
func (v *Variant) Assumptions() []string {
	if v.Document.Assumptions == nil {
		return []string{}
	}
	return v.Document.Assumptions
}

// NotVerified is what this render does not establish, never nil.
func (v *Variant) NotVerified() []string {
	if v.Document.NotVerified == nil {
		return []string{}
	}
	return v.Document.NotVerified
}

// UnitsNote explains the unit situation in one sentence a reader can act on.
//
// Rendered here rather than in each of the three surfaces that show it — the
// API, forgectl, and the workbench — because three call sites would eventually
// give three different explanations of the same state.
func (v *Variant) UnitsNote() string {
	if v.Units.Known() {
		return ""
	}
	if strings.TrimSpace(v.UnitsDeclared) == "" {
		return "No unit was stated, so every dimension in this variant is a bare number."
	}
	return fmt.Sprintf("The declared unit %q is not one FORGE can convert, "+
		"so every dimension in this variant is a bare number.", v.UnitsDeclared)
}

// NewVariant is a geometry version about to be written.
//
// Deliberately not the same struct as Variant: the write side names what a
// caller must supply, and the read side names what a reader is guaranteed. A
// single struct would have half its fields ignored on the way in, which is how
// a caller comes to believe it set something it did not.
type NewVariant struct {
	// ProjectID is an existing project, or empty to create one named after the
	// assembly and owned by the initiator — the same rule DraftRequest follows
	// for goals, and for the same reason: a person talking at the workbench has
	// not chosen a project yet, and refusing to keep their first variant until
	// they do would make VIS-04 reachable only after starting work.
	ProjectID string
	// InitiatorID is the person the proposal was made for. Never a service
	// account: a variant traces to somebody who asked for it.
	InitiatorID string
	Agent       workspace.Agent
	Generator   string
	Document    Document
	// Inputs is what the geometry was made from — for a workbench proposal, the
	// message that produced it. Required, because "made from nothing" and
	// "nobody recorded what it was made from" are different and only one of
	// them is ever true.
	Inputs any
	// GoalID links the change to the timeline, and through it to the audit
	// chain, when the proposal happened inside a goal. Empty is legal and
	// common: the workbench conversation runs outside one.
	GoalID string
}

// Validate refuses a variant that cannot state one of VIS-04's six.
//
// Every branch here exists because the requirement names the field. Like
// Version.Validate, this is the one place where a long list of required-field
// checks is the design rather than a smell — the requirement IS the list.
func (n *NewVariant) Validate() error {
	const op = "geometry.NewVariant.Validate"

	if strings.TrimSpace(n.InitiatorID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("VIS-04: a variant must name whose proposal it is. A render nobody asked for " +
				"cannot be traced back to the conversation that produced it.")
	}
	if !n.Agent.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("%q is not a recognised agent, so this variant could not say which part of FORGE drew it", n.Agent)
	}
	// 5. generator
	if strings.TrimSpace(n.Generator) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("VIS-04: a variant must name its generator — the model that produced the shape. " +
				"Two variants drawn by different models are not comparable on their dimensions alone, " +
				"and a reader cannot know that unless the render says what drew it.")
	}
	if strings.TrimSpace(n.Document.Name) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a variant must be named; an unnamed row is one a person cannot pick out of a list")
	}
	if len(n.Document.Parts) == 0 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("this geometry has no parts, so there is nothing to draw, compare, or export")
	}
	// 2. inputs
	if n.Inputs == nil {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("VIS-04: a variant must record what it was made from. Pass an empty object if it " +
				"genuinely had no inputs — that is a statement, whereas an absent field is a gap.")
	}
	// PRD VIS-06, enforced here as well as at the conversation boundary. The
	// boundary guarantees it for the path that exists today; this guarantees it
	// for every path, including ones written later by somebody who has not read
	// converse.go.
	if len(n.Document.NotVerified) == 0 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("VIS-06: geometry cannot be stored without stating what it does NOT establish. " +
				"A render with nothing in that list is the one that gets mistaken for an analysis.")
	}
	for i, p := range n.Document.Parts {
		if strings.TrimSpace(p.ID) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("part %d has no id, so it cannot be matched against the same part in another variant", i+1)
		}
		if strings.TrimSpace(p.Shape) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("part %q has no shape", p.Label())
		}
	}
	seen := map[string]bool{}
	for _, p := range n.Document.Parts {
		if seen[p.ID] {
			// Comparison matches parts across variants BY ID. Two parts sharing
			// one inside a single variant makes that matching ambiguous, and a
			// side-by-side view that silently picks one of them would report a
			// difference that is an artefact of the duplicate.
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("part id %q appears twice; comparison matches parts across variants by id, "+
					"so a duplicate would make this variant compare wrongly against every other one", p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}
