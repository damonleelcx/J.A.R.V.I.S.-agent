package geometry

import (
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Materials and assembly states (PRD VIS-02).
//
// VIS-02 asks for "orbit, pan, zoom, section, explode, assembly states,
// annotations, materials, scale". Six of those were built with the viewer. This
// file is the three that were not, and the two of them that carry a claim.
//
// # A material is a NAME, and naming it is a claim
//
// "Aluminium 6061" is not a rendering instruction. It is a statement about what
// the part is made of, and everything downstream — cost, weight, whether it can
// be welded, whether it survives the load — follows from it. FORGE picking one
// because a bracket is usually aluminium is a guess, and a guess rendered as
// brushed metal reads as a decision somebody made.
//
// So a material carries how it was arrived at, exactly like every other claim in
// this system, and the panel shows it. What it does NOT do is imply that any
// property of that material was used for anything: there is no solver here, and
// nothing has been checked against a datasheet. The render is a look.
//
// # Why the finish is declared rather than derived from the name
//
// The obvious design is a lookup — "aluminium" implies metal, "ABS" implies
// plastic — and it would need to exist in Go for the panel and in JavaScript for
// the shader. This codebase has that shape already and has recorded what it
// costs: AUD-04's readback rules live in two copies and the fence counts them
// rather than comparing them, so it cannot catch a rule neither copy
// implements.
//
// Declaring the finish alongside the name removes the table entirely. The
// document says "aluminium 6061, metal", both surfaces read the same field, and
// they cannot disagree.
//
// # An assembly state is a claim about how something comes apart
//
// A state that moves parts says these pieces separate along this path. Nothing
// here checks that they can: no interference test, no clearance, no kinematics.
// A state is therefore proposed unless somebody said otherwise, and a document
// carrying one that moves anything says so in what it does not establish.

// Finish is how a material catches light. A closed set, because the viewer
// switches on it and an unknown finish would render as nothing in particular
// while looking deliberate.
type Finish string

const (
	FinishMetal      Finish = "metal"
	FinishPlastic    Finish = "plastic"
	FinishRubber     Finish = "rubber"
	FinishGlass      Finish = "glass"
	FinishPainted    Finish = "painted"
	FinishUnfinished Finish = "unfinished"
)

// finishes is the closed set, with how each one reads.
//
// What each finish LOOKS like — the specular exponent and its strength — is
// deliberately not here. Those are rendering constants, they are needed only by
// the renderer, and putting them here would mean the same table existing in Go
// and in JavaScript. This codebase has that shape already and has recorded what
// it costs: AUD-04's readback rules live in two copies and its fence counts
// them rather than comparing them, so it cannot catch a rule neither copy
// implements.
//
// So the split is by who needs it. Go owns the closed set of NAMES, because it
// validates them and describes them to the model. The viewer owns what they
// look like. Neither can drift from the other, because neither holds the
// other's half.
var finishes = []struct {
	Finish Finish
	// Looks is how this finish reads, in the words the model contract uses.
	Looks string
}{
	{FinishMetal, "a hard, tight highlight"},
	{FinishPainted, "a soft sheen"},
	{FinishPlastic, "a broad, dull highlight"},
	{FinishGlass, "a sharp highlight; usually paired with opacity below 1"},
	{FinishRubber, "almost no highlight"},
	{FinishUnfinished, "the viewer's default"},
}

// Valid reports whether f is a finish the viewer can draw.
func (f Finish) Valid() bool {
	for _, d := range finishes {
		if d.Finish == f {
			return true
		}
	}
	return false
}

// FinishNames lists the finishes, for error text and the model contract.
func FinishNames() []string {
	out := make([]string, 0, len(finishes))
	for _, d := range finishes {
		out = append(out, string(d.Finish))
	}
	return out
}

// FinishGuide describes each finish, for the contract shown to the model. One
// producer, so the prompt cannot list a finish the viewer does not draw.
func FinishGuide() string {
	parts := make([]string, 0, len(finishes))
	for _, d := range finishes {
		parts = append(parts, string(d.Finish)+" ("+d.Looks+")")
	}
	return strings.Join(parts, ", ")
}

// Material is what a part is made of.
type Material struct {
	// Name is the material as an engineer would write it: "aluminium 6061-T6",
	// "ABS", "304 stainless". Free text, because the set is not closed and a
	// dropdown of twelve materials would be wrong more often than it was right.
	Name   string `json:"name"`
	Finish Finish `json:"finish"`
	// How and Source are the same discipline as everywhere else. A material
	// FORGE chose because a bracket is usually aluminium is `assumed`.
	How    claim.Epistemic `json:"how"`
	Source string          `json:"source,omitempty"`
}

// Validate checks a material before it is stored or drawn.
func (m *Material) Validate() error {
	const op = "geometry.Material.Validate"

	if strings.TrimSpace(m.Name) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a material must be named; a finish with no material is a look with nothing behind it")
	}
	if !m.Finish.Valid() {
		// Unknown finishes fall back rather than refusing the part: the name is
		// the claim and the finish is only how it catches light, so losing the
		// look is a smaller harm than losing the material.
		m.Finish = FinishUnfinished
	}
	if !m.How.Valid() {
		m.How = claim.Assumed
	}
	return nil
}

// AssemblyState is a named configuration of the assembly.
type AssemblyState struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Hidden lists parts not shown in this state.
	Hidden []string `json:"hidden,omitempty"`
	// Offsets moves parts, by part id, in the document's units. This is what
	// makes a state a claim rather than a filter.
	Offsets map[string][]float64 `json:"offsets,omitempty"`

	How  claim.Epistemic `json:"how"`
	Note string          `json:"note,omitempty"`
}

// Moves reports whether this state repositions anything, which is what turns it
// from a visibility filter into a statement about how the assembly comes apart.
func (s *AssemblyState) Moves() bool {
	for _, off := range s.Offsets {
		for _, v := range off {
			if v != 0 {
				return true
			}
		}
	}
	return false
}

// ValidateStates checks a document's states against its parts.
//
// Every id must resolve. A state that hides or moves a part that does not exist
// is not a harmless typo: the viewer would show the assembly unchanged, and
// somebody would read that as "this state makes no difference" rather than as a
// broken reference.
func ValidateStates(states []AssemblyState, parts []Part) error {
	const op = "geometry.ValidateStates"

	known := map[string]bool{}
	for _, p := range parts {
		known[p.ID] = true
	}
	seen := map[string]bool{}

	for i := range states {
		s := &states[i]
		if strings.TrimSpace(s.Name) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("assembly state %d has no name; a state nobody can pick is one nobody can see", i+1)
		}
		if s.ID == "" {
			s.ID = "state-" + strings.ToLower(strings.ReplaceAll(s.Name, " ", "-"))
		}
		if seen[s.ID] {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("two assembly states share the id %q", s.ID)
		}
		seen[s.ID] = true

		if !s.How.Valid() {
			// A state FORGE composed is a guess at how the thing goes together.
			s.How = claim.Proposed
		}
		for _, id := range s.Hidden {
			if !known[id] {
				return errs.New(op, errs.CodeValidationFailed).
					WithDetail("assembly state %q hides %q, which is not a part of this assembly. The "+
						"viewer would show no change and a reader would take that for the state "+
						"making no difference", s.Name, id)
			}
		}
		for id, off := range s.Offsets {
			if !known[id] {
				return errs.New(op, errs.CodeValidationFailed).
					WithDetail("assembly state %q moves %q, which is not a part of this assembly", s.Name, id)
			}
			if len(off) != 3 {
				return errs.New(op, errs.CodeValidationFailed).
					WithDetail("assembly state %q moves %q by %d components; a translation has three",
						s.Name, id, len(off))
			}
		}
	}
	return nil
}

// StatesNotVerified is what a document with moving states does not establish.
//
// Returned rather than appended in place, so both doors — the storage one and
// the conversation one — add it the same way and neither has to remember the
// wording.
func StatesNotVerified(states []AssemblyState) string {
	for i := range states {
		if states[i].Moves() {
			return "Assembly states show proposed positions only. No interference, clearance or " +
				"kinematic check exists in this deployment, so nothing here establishes that these " +
				"parts can actually move along these paths or come apart in this order."
		}
	}
	return ""
}
