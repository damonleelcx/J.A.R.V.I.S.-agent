package geometry

import "math"

// RotationRadians is a part's rotation in the unit RotationMatrix wants.
//
// # Why the document is in DEGREES and the matrix is in radians
//
// RotationMatrix is this system's one rotation convention and takes radians, so
// that the CAD kernel and the renderer read the same nine numbers rather than
// each implementing the same paragraph of trigonometry. That is right and does
// not change.
//
// What was wrong is that the DOCUMENT was in those radians too, and nothing ever
// said so. The document is written by a model, and a model asked for a wheel
// writes 90 for a quarter turn — every time. Measured on 2026-09-09: every
// non-zero rotation ever stored in this deployment was [0, 0, 90] on a wheel,
// eight values across two documents, and not one of them meant 90 radians. Read
// as radians, 90 is 90 - 14*2*pi = 2.04 rad = 116.8 degrees, so the car's wheels
// sat tilted 27 degrees off vertical. It looked like a modelling mistake and was
// a units mistake, which is the same shape as "depth" on a cylinder — see
// sizeSynonyms in mesh.go — and is fixed the same way: read what was meant.
//
// Degrees is also the unit every CAD interface a person has used shows, so a
// stored document now says what a reader would say.
//
// # Why here and not at each caller
//
// There are three surfaces that place a part — the mesh builder, the solid
// builder that feeds the kernel, and the renderer. A conversion repeated in each
// is a conversion that will disagree in one of them after the next edit, and a
// part exported to a different orientation than the one on screen is the single
// failure a downloaded file cannot be labelled out of. The Go surfaces call
// this; the renderer calls Forge3D.rotationRadians, whose agreement with this is
// fenced by TestTheRendererTurnsDegreesLikeTheBuilderDoes.
func (p Part) RotationRadians() [3]float64 {
	var out [3]float64
	copy(out[:], padTo3(p.Rotation))
	for i := range out {
		out[i] = out[i] * math.Pi / 180
	}
	return out
}
