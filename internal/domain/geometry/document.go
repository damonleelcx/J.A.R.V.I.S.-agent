// Package geometry is the shape FORGE proposes, and everything that has to
// travel with it.
//
// # Why this is a domain package and not part of the agent
//
// The document below started life inside internal/agent as the JSON contract
// with the model, which is where it looked like it belonged: the model emits it
// and the workbench draws it, and nothing else touched it. That stopped being
// true at PRD VIS-04, which asks for variants side by side — a variant has to be
// STORED to be compared with another one, and a domain package cannot import the
// agent to find out what a part is.
//
// So the vocabulary moved down: units, frames, quantities and the document
// itself live here, and internal/agent keeps a type alias so the model contract
// and the stored shape are literally the same type. Two structs with the same
// fields would drift, and the day they drift is the day a render shows something
// different from what was saved.
//
// # What is deliberately NOT here
//
// A CAD kernel. Nothing in this package computes a volume, checks an
// interference, or produces a manufacturable artefact, and the export path says
// so rather than approximating one (PRD VIS-05, VIS-06).
package geometry

// Document is a proposed assembly: the shape, what was assumed to draw it, and
// what it does not establish.
//
// The JSON tags are the model's contract, unchanged from when this lived in
// internal/agent — they are also the stored shape, which is the point. What is
// persisted is exactly what the model said, so a replay cannot differ from what
// the person saw.
type Document struct {
	Name  string `json:"name"`
	Units string `json:"units"`
	Parts []Part `json:"parts"`
	// Parameters are the numbers a person could change, and Derived are the
	// values that must follow when they do (see parameters.go).
	//
	// Optional, and additive on purpose. A document that carries neither is
	// exactly the document this package has always held — the fields were added
	// by the 2026-09-05 parametric phase and every existing stored variant
	// predates them, so absence has to keep meaning "not parametric" rather
	// than "parametric and empty".
	//
	// What they do NOT yet do is drive Parts. A part's Size is still the
	// authored number, and the link between the two is the next phase's work;
	// until it exists, Resolve reports what the parameters say and nothing
	// silently rewrites geometry from them.
	Parameters []Parameter `json:"parameters,omitempty"`
	Derived    []Derived   `json:"derived,omitempty"`
	// Features are the operations that make an assembly a PART rather than a
	// pile of solids: holes cut through a plate, edges rounded, bodies fused
	// (see feature.go). Optional, and only the CAD kernel performs them — the
	// renderer draws primitives and says what it could not show.
	Features []Feature `json:"features,omitempty"`
	// Assumptions is every dimension FORGE chose rather than was given. One of
	// the six things PRD VIS-04 requires a render to link to.
	Assumptions []string `json:"assumptions"`
	// NotVerified is what this render does NOT establish. Required whenever
	// geometry is present — PRD VIS-06: photorealism never implies
	// manufacturability, structural adequacy, or compliance.
	NotVerified []string `json:"not_verified"`
	// Overlays are engineering annotations somebody authored on this model:
	// dimensions taken from a drawing, tolerances, datums (PRD VIS-03). Only
	// what was AUTHORED lives here. Dimensions FORGE derives from the parts are
	// computed at read time by Measure and travel separately, because a value
	// somebody stated and a value FORGE worked out are different claims and must
	// not become indistinguishable by sharing a field.
	Overlays []Overlay `json:"overlays,omitempty"`
	// States are named configurations of this assembly (PRD VIS-02): which parts
	// are shown, and where they sit. A state that moves anything is a claim
	// about how the thing comes apart, and nothing here checks it.
	States []AssemblyState `json:"states,omitempty"`
}

// Part is one solid.
type Part struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Shape    string             `json:"shape"`
	Size     map[string]float64 `json:"size"`
	Position []float64          `json:"position"`
	// SizeFrom and PositionFrom bind a dimension to an EXPRESSION over the
	// document's parameters, so that changing a parameter moves the part
	// (see binding.go). Keys are the same size keys Size uses, and "x", "y",
	// "z" for the position.
	//
	// Both are optional, and a dimension that appears in neither keeps the
	// number the model typed. Bind writes the evaluated result into Size and
	// Position, so nothing downstream — the mesh, the comparison, the exporter —
	// ever sees an expression.
	SizeFrom     map[string]string `json:"size_from,omitempty"`
	PositionFrom map[string]string `json:"position_from,omitempty"`
	// Profile is a closed outline in the part's own XY plane (see profile.go).
	// An "extrusion" sweeps it along local Z by Size["depth"]; a "revolve"
	// turns it about Axis. Read for no other shape.
	Profile []Point `json:"profile,omitempty"`
	// Axis is which way a "revolve" turns: "y" (the default, and up) or "x".
	Axis     string    `json:"axis,omitempty"`
	Rotation []float64 `json:"rotation"`
	Color    string    `json:"color"`
	Opacity  float64   `json:"opacity"`
	Note     string    `json:"note"`
	// Material is what this part is made of (PRD VIS-02). Optional, and a claim
	// when present: naming a material is a statement everything downstream
	// depends on, so it carries how it was arrived at. Nil means nobody said,
	// which is different from "unspecified material" and is left as nothing
	// rather than filled in.
	Material *Material `json:"material,omitempty"`
}

// Label returns the part's human name, falling back to its id.
//
// Never empty, because every surface that lists parts — the panel, the
// comparison, the exported file's group names — needs something to print, and
// each inventing its own fallback is how the same part ends up with three names.
func (p Part) Label() string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}
