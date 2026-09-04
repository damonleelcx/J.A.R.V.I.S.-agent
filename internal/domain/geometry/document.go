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
}

// Part is one solid.
type Part struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Shape    string             `json:"shape"`
	Size     map[string]float64 `json:"size"`
	Position []float64          `json:"position"`
	Rotation []float64          `json:"rotation"`
	Color    string             `json:"color"`
	Opacity  float64            `json:"opacity"`
	Note     string             `json:"note"`
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
