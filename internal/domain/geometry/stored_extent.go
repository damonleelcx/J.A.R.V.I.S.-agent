package geometry

import "math"

// A design's extent, worked out once when it is stored.
//
// # The problem this solves
//
// Measure finds a design's overall dimensions from bounds, which places every
// occurrence the design has: a stored 1,020,782-part fleet took 2.1-3.1 s to answer
// GET /v1/geometry/{id}, on every open of it, all of it placing parts to find six
// numbers (docs/spikes/2026-09-17-large-designs, "Found, not changed"). A version is
// immutable, so those six numbers never change after it is written: they are worked
// out when it is stored (Repository.Insert) and kept beside the document
// (forge_geometry.extent, migration 0025), and a read measures FROM them.
//
// # Why the extent and not the overlays
//
// The overlays' words — the note naming how many dimensions were assumed, the labels,
// the rounding — are Measure's, and they change with the code. Only what costs a
// placement is kept; MeasureFrom writes the rest on every read, exactly as Measure
// does, so the two answer identically by construction.
//
// # Why not bound the tree from definitions × placements
//
// Considered and rejected: bounds ignores a part's own rotation but not a
// placement's, so a rotated placement moves each part's centre and not its box, and
// the box of a rotated sub-assembly is not the rotated box of its parts. Any shortcut
// that composed boxes up the tree would answer differently from today for every
// rotated child. Storing what bounds found is identical by construction.
//
// # When the kept extent is not used
//
// A row written before migration 0025 has none, and one written by an earlier
// ExtentRev was worked out by different arithmetic: both are measured the old way,
// once, and the answer kept (Service.Measured). A design whose extent is not finite
// (nothing it places has a size bounds can read) keeps none and is measured each time.
// Fences: TestExtent_MeasuresExactlyAsMeasureDoes,
// TestGetMeasuresALargeDesignWithoutPlacingIt.

// ExtentRev is the revision of the arithmetic an Extent was worked out by. Raise it
// whenever bounds (or localBox, or what they expand) answers differently, and every
// kept extent is worked out again on its next read rather than trusted.
const ExtentRev = 1

// Extent is the corners bounds finds, as stored.
type Extent struct {
	Rev int `json:"rev"`
	// Empty is a design whose tree places nothing bounds could size: no corner on
	// any axis. Stored as a fact rather than as infinities, which JSON cannot carry.
	Empty bool       `json:"empty,omitempty"`
	Min   [3]float64 `json:"min"`
	Max   [3]float64 `json:"max"`
}

// ExtentOf works out a document's extent, placing every occurrence: the cost the
// kept extent exists to pay once. Nil when there is nothing to keep — a document
// that places nothing (Measure answers nil for it without bounding) or corners that
// are not finite numbers.
func ExtentOf(doc Document) *Extent {
	if !doc.HasGeometry() {
		return nil
	}
	min, max := bounds(doc)
	e := &Extent{Rev: ExtentRev, Min: min, Max: max}
	if math.IsInf(min[0], 1) && math.IsInf(min[1], 1) && math.IsInf(min[2], 1) &&
		math.IsInf(max[0], -1) && math.IsInf(max[1], -1) && math.IsInf(max[2], -1) {
		return &Extent{Rev: ExtentRev, Empty: true}
	}
	for i := 0; i < 3; i++ {
		for _, v := range []float64{min[i], max[i]} {
			if math.IsInf(v, 0) || math.IsNaN(v) {
				return nil
			}
		}
	}
	return e
}

// Current reports whether a kept extent can be measured from.
func (e *Extent) Current() bool { return e != nil && e.Rev == ExtentRev }

// corners are the extent as bounds returned it.
func (e *Extent) corners() (min, max [3]float64) {
	if e.Empty {
		inf := math.Inf(1)
		return [3]float64{inf, inf, inf}, [3]float64{-inf, -inf, -inf}
	}
	return e.Min, e.Max
}

// MeasureFrom is Measure over a kept extent: the same overlays, without placing a
// part. An extent that is not Current is not trusted and the document is measured.
func MeasureFrom(doc Document, unit Unit, e *Extent) []Overlay {
	if !doc.HasGeometry() {
		return nil
	}
	if !e.Current() {
		return Measure(doc, unit)
	}
	min, max := e.corners()
	return measureCorners(doc, unit, min, max)
}
