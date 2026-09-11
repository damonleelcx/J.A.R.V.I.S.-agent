package geometry

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Turning a proposal into triangles (PRD VIS-05).
//
// # What a mesh is, and what it is not
//
// The primitive set FORGE proposes in — box, cylinder, cone, sphere, tube,
// plane — is analytic. A cylinder has a radius, not a number of sides. Exporting
// it means choosing a number of sides, and the chosen surface is not the surface
// that was described: it is smaller, everywhere, by an amount that can be
// calculated exactly.
//
// VIS-05 says to LABEL that, so this file calculates the number rather than
// describing it. "Approximated" is a word; "0.034 mm smaller in radius, at every
// point between the facets" is a fact somebody can decide with.
//
// # Why these segment counts and not others
//
// Because they are the renderer's. internal/httpapi/assets/forge3d.js declares
// them in one object and this file mirrors it, so the file a person downloads is
// the surface they were looking at when they decided to download it. A finer
// export would be a better mesh and a worse answer: the deviation printed on the
// screen would no longer be the deviation of anything they had seen.
//
// TestTessellation_GoMatchesTheRenderer parses the renderer's declaration and
// fails when these drift apart.
const (
	// radialSegments is how many sides a cylinder, cone or tube is drawn with.
	radialSegments = 40
	// sphereRadialSegments is a sphere's segments around the equator.
	sphereRadialSegments = 32
)

// sphereRings is a sphere's divisions from pole to pole.
//
// Derived from the radial count exactly as the renderer derives it
// (max(8, floor(segments/2))), rather than written down as 16 — a second literal
// would keep agreeing with the renderer right up until somebody changed one of
// them.
func sphereRings() int {
	r := sphereRadialSegments / 2
	if r < 8 {
		r = 8
	}
	return r
}

// TessellationCounts reports the segment counts this build exports with.
//
// Exported so the fence can compare them against the renderer's own declaration
// in forge3d.js. That fence lives in internal/httpapi, where the asset is
// embedded — reading the file by relative path from here would pass on a
// developer's machine and prove nothing about the binary.
func TessellationCounts() (radial, sphereRadial, sphereRingCount int) {
	return radialSegments, sphereRadialSegments, sphereRings()
}

// Triangle is one facet in assembly coordinates, wound counter-clockwise seen
// from outside.
type Triangle struct {
	A, B, C [3]float64
	Normal  [3]float64
}

// Mesh is a tessellated assembly.
type Mesh struct {
	// Groups keeps part identity, in document order. Formats that can carry it
	// (OBJ) write one group per part; formats that cannot (STL) flatten them and
	// say so in the label.
	Groups []MeshGroup
	// Deviations is one entry per part whose surface was approximated, with the
	// error measured rather than described.
	Deviations []Deviation
	// Inferences is everything the tessellator had to decide because the
	// document did not say it. VIS-05 names inference as a thing to label, and
	// a defaulted dimension is the most consequential kind: it is a number
	// nobody chose that leaves the system looking exactly like one somebody did.
	Inferences []string
}

// labelOf names a part by id, for a message about a feature that refers to one.
//
// Falls back to the raw id rather than to "unknown": a feature naming a part
// that does not exist is a real document fault, and the id is what somebody
// needs in order to find it.
func labelOf(doc Document, id string) string {
	for _, p := range doc.Parts {
		if p.ID == id {
			return p.Label()
		}
	}
	return id
}

// toolNames joins a feature's tools into something readable.
func toolNames(doc Document, ids []string) string {
	if len(ids) == 0 {
		return "nothing"
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, labelOf(doc, id))
	}
	return strings.Join(out, ", ")
}

// Triangles returns every facet in the mesh, flattened.
func (m *Mesh) Triangles() []Triangle {
	var out []Triangle
	for _, g := range m.Groups {
		out = append(out, g.Triangles...)
	}
	return out
}

// MeshGroup is one part's facets.
type MeshGroup struct {
	PartID    string
	Label     string
	Shape     string
	Triangles []Triangle
}

// Deviation is how far a tessellated surface sits from the surface described.
type Deviation struct {
	PartID string
	Label  string
	Shape  string
	// Segments is how many flat faces replaced the curve.
	Segments int
	// Max is the greatest distance between the described surface and the
	// exported one, in the assembly's unit. Inward: the facets are chords, so
	// every exported point lies on or inside the described surface.
	Max Quantity
}

// Tessellate converts a document into triangles.
//
// The unit is the assembly's, already resolved. Nothing here converts: the file
// is written in the unit the geometry was authored in, and the label says which.
func Tessellate(doc Document, unit Unit) *Mesh {
	// Same expansions the solid builder does, in the same order and for the same
	// reason: the viewport and the exported file must agree about how many
	// spokes there are, and what shape a tooth is.
	doc, gearProblems := expandGears(doc)
	doc, repeatProblems := expandRepeats(doc)

	m := &Mesh{}
	inferred := map[string]bool{}
	infer := func(format string, args ...any) {
		s := fmt.Sprintf(format, args...)
		if !inferred[s] {
			inferred[s] = true
			m.Inferences = append(m.Inferences, s)
		}
	}
	for _, p := range repeatProblems {
		infer("%s %s.", p.Name, p.Detail)
	}
	for _, p := range gearProblems {
		if p.Severity == Error {
			infer("%s %s, so it is not in this file.", p.Name, p.Detail)
			continue
		}
		infer("%s %s.", p.Name, p.Detail)
	}

	// What the features would have done, and did not.
	//
	// # Why this is here at all
	//
	// Tessellate draws PARTS. It has never performed a feature — a cut, a fuse,
	// a fillet — because those need a kernel and this is a triangle builder, and
	// only the CAD kernel does them (document.go says so).
	//
	// What it also did, until now, was say nothing. So an OBJ or an STL of a
	// bracket with four bolt holes contained four solid POSTS standing on the
	// plate, and the file carried no hint that the four cylinders in it are the
	// opposite of what they represent. That is the same failure this package
	// names in three other places — "a file quietly missing a part is worse than
	// one that says it is missing it", "the export asserting something the
	// system did not do" — and it was the one place nobody had said it.
	//
	// It cannot be fixed by cutting: that is a boolean operation and there is no
	// CSG here (see the implementation plan on what that would take). It CAN be
	// stopped from passing unnoticed, which is exactly the stance the viewport
	// already takes by drawing a cut tool as a ghost.
	tools := map[string]string{}
	for _, f := range doc.Features {
		switch strings.ToLower(f.Op) {
		case "cut":
			for _, id := range f.With {
				tools[id] = "cut"
			}
			infer("%s: the material %s removes is NOT removed in this file. This is a mesh; "+
				"the cut is performed by the CAD kernel and appears in the STEP export.",
				labelOf(doc, f.Of), toolNames(doc, f.With))
		case "fuse":
			for _, id := range f.With {
				tools[id] = "fuse"
			}
			infer("%s: %s is present as a separate solid rather than fused into it. This is a "+
				"mesh; the fuse is performed by the CAD kernel.",
				labelOf(doc, f.Of), toolNames(doc, f.With))
		case "fillet", "chamfer":
			infer("%s: its %s is not in this file — a mesh has no edges to round. The rounded "+
				"solid is what the STEP export contains.", labelOf(doc, f.Of),
				strings.ToLower(f.Op))
		}
	}

	for _, p := range doc.Parts {
		local, dev := partTriangles(p, unit, infer)
		if len(local) == 0 {
			continue
		}
		if op, isTool := tools[p.ID]; isTool {
			// Named per PART as well as per feature, because the parts list and
			// the group names in the file are where somebody looks when they are
			// wondering what a cylinder is doing there.
			infer("%s is a %s TOOL and is in this file as a solid. It is the shape of the "+
				"operation, not a part of the assembly.", p.Label(), op)
		}
		placed := place(local, p)
		m.Groups = append(m.Groups, MeshGroup{
			PartID: p.ID, Label: p.Label(), Shape: p.Shape, Triangles: placed,
		})
		if dev != nil {
			dev.PartID, dev.Label, dev.Shape = p.ID, p.Label(), p.Shape
			m.Deviations = append(m.Deviations, *dev)
		}
	}
	sort.SliceStable(m.Inferences, func(i, j int) bool { return m.Inferences[i] < m.Inferences[j] })
	return m
}

// sizeOr reads a dimension, reporting when it had to be invented.
//
// The renderer defaults a missing dimension silently (num(v, d)), which is
// defensible on a screen beside a provenance banner. It is not defensible in a
// file: a defaulted 1 leaves this system indistinguishable from a stated 1, and
// there is no banner attached to a download. So every default is recorded and
// travels with the export.
// sizeSynonyms are keys a model writes that can only mean a key this vocabulary
// already has, per shape.
//
// # Why this exists
//
// A cylinder is `radius` + `height`, and a model asked for a wheel wrote
//
//	"shape": "cylinder", "size": {"depth": 250, "radius": 350}
//
// A cylinder has NO "depth" in this contract, so the 250 could not mean anything
// else — and it was thrown away, `height` defaulted to 1 mm, and the note said
// "no height was given" while the model had plainly given one under another
// name. Observed on all four wheels of the sports car, 2026-09-09: ⌀700 discs a
// millimetre wide, which is why the car rendered as a slab.
//
// # Why a reading and not a stricter prompt
//
// The same reasoning dimensionrepair.go records: the model is not wrong in any
// way a person would recognise — it named a length and put a length there — and
// telling it more firmly which word to use makes the failure rarer without
// making it survivable, while the cost of the failure stays "a part with an
// invented dimension". A reading fixes the documents that already exist too.
//
// # Why a table and why it is per shape
//
// "depth" is a REAL key on a box, so this cannot be a global alias list. Only
// entries with an observed failure behind them belong here: an alias added on a
// hunch would silently accept a spelling nobody uses and hide the day the
// contract genuinely changed.
var sizeSynonyms = map[string]map[string]string{
	"cylinder": {"depth": "height"},
	"cone":     {"depth": "height"},
	// A gear's face width. "thickness" is the word the model reached for when it
	// had no gear shape and guessed one — `InvoluteGear(module=2, teeth=20,
	// thickness=6)`, on a live run (scriptrepair_live_test.go) — and "face_width"
	// is the gear standards' own name for it. Read by gear.go, and by gearSize in
	// forge3d.js, which holds the same list.
	"gear": {"thickness": "depth", "face_width": "depth"},
}

func sizeOr(p Part, key string, fallback float64, unit Unit, infer func(string, ...any)) float64 {
	if v, ok := p.Size[key]; ok {
		return v
	}
	// A key the model used that can only mean this one. Reported, because a
	// dimension read from a different word is still a reading and the person is
	// entitled to know which word FORGE acted on.
	shape, _ := resolveShape(p.Shape, p.Label())
	for alias, means := range sizeSynonyms[shape] {
		if means != key {
			continue
		}
		if v, ok := p.Size[alias]; ok {
			infer("%s: %q was read as its %s (%s). A %s has no %q, so that is the only thing "+
				"it can mean — but it is a reading, not what was written.",
				p.Label(), alias, key, NewQuantity(v, unit), shape, alias)
			return v
		}
	}
	infer("%s: no %s was given, so %s was used. This is a number FORGE chose, not one it was told.",
		p.Label(), key, NewQuantity(fallback, unit))
	return fallback
}

// partTriangles builds one part in its own local frame, centred on the origin.
func partTriangles(p Part, unit Unit, infer func(string, ...any)) ([]Triangle, *Deviation) {
	// A retired shape word is read as the shape it always was, and the reader
	// is told which — through the same table the exporter, the summary line and
	// the renderer all consult (retired.go).
	shape, retiredNote := resolveShape(p.Shape, p.Label())
	if retiredNote != "" {
		infer("%s", retiredNote)
	}
	switch shape {
	case "box":
		return box(
			sizeOr(p, "width", 1, unit, infer),
			sizeOr(p, "height", 1, unit, infer),
			sizeOr(p, "depth", 1, unit, infer)), nil

	case "section":
		// A drawing with no thickness, so a mesh file has nothing to put in it.
		//
		// Named and skipped rather than exported as a bounding box like the
		// default below: a section is not a shape this exporter failed to
		// understand, it is one that correctly has no volume. STL and OBJ carry
		// solids, and a 1 mm cube standing in for a station would put a body in
		// the file that the design does not contain.
		//
		// The blended solid a loft makes IS exportable — through STEP, which the
		// kernel writes. That is where a lofted hull comes out.
		infer("%s: a section is an outline with no thickness, so it is not in this mesh file. "+
			"The solid its loft blends is in the parametric export.", p.Label())
		return nil, nil

	case "extrusion":
		return extrusion(p, sizeOr(p, "depth", 1, unit, infer), unit, infer)

	case "revolve":
		tris, dev := revolved(p, unit, infer)
		// Faceted twice over when the outline has rounded corners: once round
		// the turn and once round each corner. The worse of the two is what the
		// file actually is.
		return tris, worseDeviation(dev, chordDeviation(revolveRadius(p), radialSegments, unit))

	case "sweep":
		// A sweep with no rounded corner has NO deviation, and that is not an
		// omission: a polygon carried along a polyline has no curved surface
		// anywhere on it, so the triangles ARE the solid. A bend radius is the
		// one thing that makes a sweep an approximation.
		return swept(p, unit, infer)

	case "plane":
		// A plane has no thickness and is not a solid. Exported as the two
		// triangles the renderer draws, and named in the label as the one thing
		// in the file that will not print, machine, or hold a volume.
		return plane(
			sizeOr(p, "width", 1, unit, infer),
			sizeOr(p, "depth", 1, unit, infer)), nil

	case "cylinder":
		r := sizeOr(p, "radius", 0.5, unit, infer)
		h := sizeOr(p, "height", 1, unit, infer)
		rt := r
		if v, ok := p.Size["radius_top"]; ok {
			rt = v
		}
		return cylinder(r, rt, h, radialSegments), chordDeviation(math.Max(r, rt), radialSegments, unit)

	case "cone":
		r := sizeOr(p, "radius", 0.5, unit, infer)
		h := sizeOr(p, "height", 1, unit, infer)
		return cylinder(r, 0, h, radialSegments), chordDeviation(r, radialSegments, unit)

	case "sphere":
		r := sizeOr(p, "radius", 0.5, unit, infer)
		return sphere(r, sphereRadialSegments, sphereRings()), sphereDeviation(r, sphereRadialSegments, sphereRings(), unit)

	default:
		// Same substitution as the renderer's, and reported for the same reason:
		// a file whose group is called "fillet" and whose facets are a cuboid is
		// the export asserting something the system did not do.
		infer("%s: the shape %q is not one FORGE can build, so it is exported as a bounding box. "+
			"The file does not contain the shape its group is named after.", p.Label(), p.Shape)
		return box(
			sizeOr(p, "width", 1, unit, infer),
			sizeOr(p, "height", 1, unit, infer),
			sizeOr(p, "depth", 1, unit, infer)), nil
	}
}

// chordDeviation is how far an n-sided prism sits inside the cylinder it
// replaces.
//
// The vertices lie ON the described circle and the flat faces are chords, so the
// greatest error is at the middle of each face: r − r·cos(π/n), the sagitta.
// Exact, not an estimate.
func chordDeviation(radius float64, segments int, unit Unit) *Deviation {
	if radius <= 0 || segments <= 0 {
		return nil
	}
	d := radius * (1 - math.Cos(math.Pi/float64(segments)))
	return &Deviation{Segments: segments, Max: NewQuantity(round(d, 6), unit)}
}

// sphereDeviation takes the worse of a sphere's two tessellation directions.
//
// A sphere is faceted around the equator AND from pole to pole, and the two are
// not the same fineness. Reporting the finer one would understate the error
// everywhere the coarser one dominates.
func sphereDeviation(radius float64, radial, rings int, unit Unit) *Deviation {
	if radius <= 0 || radial <= 0 || rings <= 0 {
		return nil
	}
	around := radius * (1 - math.Cos(math.Pi/float64(radial)))
	along := radius * (1 - math.Cos(math.Pi/float64(2*rings)))
	worst, segments := around, radial
	if along > worst {
		worst, segments = along, rings
	}
	return &Deviation{Segments: segments, Max: NewQuantity(round(worst, 6), unit)}
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// place applies the part's rotation and position, in the renderer's order:
// translate ∘ rotate. Anything else would put the exported part somewhere other
// than where it was drawn.
func place(ts []Triangle, p Part) []Triangle {
	rot := p.RotationRadians()
	pos := [3]float64{}
	copy(pos[:], padTo3(p.Position))

	out := make([]Triangle, 0, len(ts))
	for _, t := range ts {
		nt := Triangle{
			A: translate(rotate(t.A, rot), pos),
			B: translate(rotate(t.B, rot), pos),
			C: translate(rotate(t.C, rot), pos),
		}
		// The normal is rotated, not recomputed from the rotated corners: a
		// degenerate facet (a cylinder of zero radius, which the model does
		// emit) has no cross product, and a zero normal in an STL is a facet
		// some readers discard.
		nt.Normal = rotate(t.Normal, rot)
		out = append(out, nt)
	}
	return out
}

func padTo3(v []float64) []float64 {
	out := []float64{0, 0, 0}
	copy(out, v)
	return out
}

// rotate applies the renderer's XYZ rotation matrix (forge3d.js rotationXYZ),
// which takes RADIANS. Mirrored term by term rather than rederived, because a
// rotation convention that is merely equivalent-looking is one that rotates
// some parts the other way.
// RotationMatrix is this system's rotation convention, row-major.
//
// # Why it is exported, and why rotate now goes through it
//
// The convention lives in exactly one place. A CAD kernel placing the same part
// has to agree with the renderer about what a rotation MEANS — the Euler order,
// and that the angles are RADIANS and not degrees — and the only way to
// guarantee that is for both to read the same nine numbers rather than each
// implement the same paragraph of trigonometry. A kernel that disagreed would
// export a part rotated somewhere other than where it was drawn, which is the
// one failure a downloaded file cannot be labelled out of.
//
// Angles are radians, as they have always been here: nothing in this file has
// ever converted from degrees, and a caller passing 90 gets 90 radians.
func RotationMatrix(r [3]float64) [9]float64 {
	cx, sx := math.Cos(r[0]), math.Sin(r[0])
	cy, sy := math.Cos(r[1]), math.Sin(r[1])
	cz, sz := math.Cos(r[2]), math.Sin(r[2])

	return [9]float64{
		cy * cz, -cy * sz, sy,
		sx*sy*cz + cx*sz, -sx*sy*sz + cx*cz, -sx * cy,
		-cx*sy*cz + sx*sz, cx*sy*sz + sx*cz, cx * cy,
	}
}

func rotate(v [3]float64, r [3]float64) [3]float64 {
	m := RotationMatrix(r)
	return [3]float64{
		m[0]*v[0] + m[1]*v[1] + m[2]*v[2],
		m[3]*v[0] + m[4]*v[1] + m[5]*v[2],
		m[6]*v[0] + m[7]*v[1] + m[8]*v[2],
	}
}

func translate(v [3]float64, t [3]float64) [3]float64 {
	return [3]float64{v[0] + t[0], v[1] + t[1], v[2] + t[2]}
}

// ---------------------------------------------------------------------------
// The primitives, mirroring forge3d.js
// ---------------------------------------------------------------------------

func quad(a, b, c, d [3]float64, n [3]float64) []Triangle {
	var out []Triangle
	out = appendNonDegenerate(out, Triangle{A: a, B: b, C: c, Normal: n})
	out = appendNonDegenerate(out, Triangle{A: a, B: c, C: d, Normal: n})
	return out
}

func box(w, h, d float64) []Triangle {
	x, y, z := w/2, h/2, d/2
	var out []Triangle
	faces := []struct {
		n       [3]float64
		corners [4][3]float64
	}{
		{[3]float64{0, 0, 1}, [4][3]float64{{-x, -y, z}, {x, -y, z}, {x, y, z}, {-x, y, z}}},
		{[3]float64{0, 0, -1}, [4][3]float64{{x, -y, -z}, {-x, -y, -z}, {-x, y, -z}, {x, y, -z}}},
		{[3]float64{0, 1, 0}, [4][3]float64{{-x, y, z}, {x, y, z}, {x, y, -z}, {-x, y, -z}}},
		{[3]float64{0, -1, 0}, [4][3]float64{{-x, -y, -z}, {x, -y, -z}, {x, -y, z}, {-x, -y, z}}},
		{[3]float64{1, 0, 0}, [4][3]float64{{x, -y, z}, {x, -y, -z}, {x, y, -z}, {x, y, z}}},
		{[3]float64{-1, 0, 0}, [4][3]float64{{-x, -y, -z}, {-x, -y, z}, {-x, y, z}, {-x, y, -z}}},
	}
	for _, f := range faces {
		out = append(out, quad(f.corners[0], f.corners[1], f.corners[2], f.corners[3], f.n)...)
	}
	return out
}

func plane(w, d float64) []Triangle {
	x, z := w/2, d/2
	n := [3]float64{0, 1, 0}
	return quad([3]float64{-x, 0, -z}, [3]float64{x, 0, -z}, [3]float64{x, 0, z}, [3]float64{-x, 0, z}, n)
}

func cylinder(radius, radiusTop, height float64, segments int) []Triangle {
	half := height / 2
	var out []Triangle

	slope := 0.0
	if height != 0 {
		slope = (radius - radiusTop) / height
	}
	at := func(r, y float64, i int) [3]float64 {
		t := float64(i) / float64(segments) * 2 * math.Pi
		return [3]float64{r * math.Cos(t), y, r * math.Sin(t)}
	}
	for i := 0; i < segments; i++ {
		t := (float64(i) + 0.5) / float64(segments) * 2 * math.Pi
		n := normalise([3]float64{math.Cos(t), slope, math.Sin(t)})
		topA, topB := at(radiusTop, half, i), at(radiusTop, half, i+1)
		botA, botB := at(radius, -half, i), at(radius, -half, i+1)
		// Wound so the outward face is counter-clockwise from outside; a cone
		// collapses one edge to a point and the degenerate triangle is dropped
		// rather than written as a zero-area facet.
		out = appendNonDegenerate(out, Triangle{A: topA, B: botA, C: botB, Normal: n})
		out = appendNonDegenerate(out, Triangle{A: topA, B: botB, C: topB, Normal: n})
	}
	caps := []struct {
		y    float64
		r    float64
		n    [3]float64
		flip bool
	}{
		{half, radiusTop, [3]float64{0, 1, 0}, false},
		{-half, radius, [3]float64{0, -1, 0}, true},
	}
	for _, c := range caps {
		if c.r <= 0 {
			continue
		}
		centre := [3]float64{0, c.y, 0}
		for i := 0; i < segments; i++ {
			a, b := at(c.r, c.y, i), at(c.r, c.y, i+1)
			t := Triangle{A: centre, B: a, C: b, Normal: c.n}
			if c.flip {
				t = Triangle{A: centre, B: b, C: a, Normal: c.n}
			}
			out = appendNonDegenerate(out, t)
		}
	}
	return out
}

func sphere(radius float64, radial, rings int) []Triangle {
	at := func(ring, seg int) [3]float64 {
		phi := float64(ring) / float64(rings) * math.Pi
		theta := float64(seg) / float64(radial) * 2 * math.Pi
		return [3]float64{
			math.Sin(phi) * math.Cos(theta) * radius,
			math.Cos(phi) * radius,
			math.Sin(phi) * math.Sin(theta) * radius,
		}
	}
	var out []Triangle
	for y := 0; y < rings; y++ {
		for x := 0; x < radial; x++ {
			a, b := at(y, x), at(y+1, x)
			c, d := at(y+1, x+1), at(y, x+1)
			out = appendNonDegenerate(out, Triangle{A: a, B: b, C: c, Normal: normalise(a)})
			out = appendNonDegenerate(out, Triangle{A: a, B: c, C: d, Normal: normalise(a)})
		}
	}
	return out
}

// appendNonDegenerate drops facets with no area, and orients the rest.
//
// # Dropping
//
// A cone's apex ring and a sphere's poles both collapse to a point, and a
// zero-area triangle is not geometry: some readers ignore it, some report the
// file as invalid, and none of them benefit from it being there.
//
// # Orienting, and the bug that made it necessary
//
// Mesh consumers decide which side of a facet is OUTSIDE from its winding
// order. The renderer does not: it is handed each normal explicitly and draws
// with back-face culling off, so a face wound the wrong way looks perfectly
// correct on screen. The first bracket exported from this code carried a
// cylinder whose signed volume was NEGATIVE — an inside-out solid, in a file
// that opens and renders without complaint anywhere. A slicer reading it would
// have had to guess which side was material.
//
// Rather than fix the winding of each primitive by hand — the same mistake,
// available six more times — every facet is oriented against the normal it was
// built with, which is analytically outward in all of them. The property becomes
// structural instead of a detail somebody has to remember.
//
// Caught and held by TestExport_EverySolidIsWoundOutward, which computes each
// exported group's signed volume. Reading the code did not find this; measuring
// the artefact did.
//
// docs/bugfix/2026-09-02-exported-meshes-were-inside-out.md
func appendNonDegenerate(out []Triangle, t Triangle) []Triangle {
	if same(t.A, t.B) || same(t.B, t.C) || same(t.A, t.C) {
		return out
	}
	return append(out, orient(t))
}

// orient makes a facet's winding agree with its own normal.
//
// A facet already wound outward is returned untouched — including one whose
// normal is degenerate (a zero-radius cone wall), where there is nothing to
// disagree with and swapping would be a coin toss.
func orient(t Triangle) Triangle {
	ab := [3]float64{t.B[0] - t.A[0], t.B[1] - t.A[1], t.B[2] - t.A[2]}
	ac := [3]float64{t.C[0] - t.A[0], t.C[1] - t.A[1], t.C[2] - t.A[2]}
	cross := [3]float64{
		ab[1]*ac[2] - ab[2]*ac[1],
		ab[2]*ac[0] - ab[0]*ac[2],
		ab[0]*ac[1] - ab[1]*ac[0],
	}
	if cross[0]*t.Normal[0]+cross[1]*t.Normal[1]+cross[2]*t.Normal[2] >= 0 {
		return t
	}
	t.B, t.C = t.C, t.B
	return t
}

func same(a, b [3]float64) bool {
	const eps = 1e-12
	return math.Abs(a[0]-b[0]) < eps && math.Abs(a[1]-b[1]) < eps && math.Abs(a[2]-b[2]) < eps
}

func normalise(v [3]float64) [3]float64 {
	l := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	if l == 0 {
		return [3]float64{0, 0, 0}
	}
	return [3]float64{v[0] / l, v[1] / l, v[2] / l}
}

// extrusion tessellates a profile swept along local Z, centred on it.
//
// # Why the caps are triangulated and the walls are not
//
// The walls are quads between consecutive points and need no triangulation at
// all. The caps are the outline itself, which is where ear clipping earns its
// place: a fan across an L-bracket's inner corner puts triangles outside the
// part, and the exported file would be a different shape from the drawing.
//
// # Winding
//
// triangulate normalises the outline to counter-clockwise, so the +Z cap is used
// as it comes and the -Z cap is reversed. The walls take their outward normal
// from the edge direction, which is only well defined BECAUSE the winding was
// normalised — an inside-out solid is a defect this repository has shipped once
// already.
func extrusion(p Part, depth float64, unit Unit, infer func(string, ...any)) ([]Triangle, *Deviation) {
	outer, holes, dev, err := flattenSection(partOutline(p), partHoles(p), unit)
	if err != nil {
		// Reported and then not drawn. A drawing whose corners cannot be
		// resolved has no points to fall back to — unlike a self-crossing one,
		// which at least has the points it was given.
		infer("%s: %v, so it is not drawn.", p.Label(), err)
		return nil, nil
	}
	sec := triangulateLoops(outer, holes)
	if !sec.OK {
		// Reported and then drawn as far as it went. A part that vanishes from a
		// render is read as a design with a piece missing; a partial one with a
		// note beside it is read as what it is.
		infer("%s: this outline could not be closed into a surface — it crosses itself, "+
			"repeats a point, or has a hole that will not fit inside it — so it is drawn "+
			"only as far as FORGE could read it.", p.Label())
	}
	if len(sec.Tris) == 0 {
		return nil, nil
	}
	half := depth / 2
	pts := sec.Merged
	at := func(i int, z float64) [3]float64 { return [3]float64{pts[i][0], pts[i][1], z} }

	out := make([]Triangle, 0, len(sec.Tris)*2+len(pts)*2)
	for _, t := range sec.Tris {
		out = appendNonDegenerate(out, Triangle{
			A: at(t[0], half), B: at(t[1], half), C: at(t[2], half),
			Normal: [3]float64{0, 0, 1}})
		// Reversed, so the bottom cap faces away from the solid too.
		out = appendNonDegenerate(out, Triangle{
			A: at(t[2], -half), B: at(t[1], -half), C: at(t[0], -half),
			Normal: [3]float64{0, 0, -1}})
	}
	// The walls, loop by loop rather than over the merged ring: a wall along a
	// bridge would be a quad of zero width, drawn twice and facing both ways.
	for _, loop := range sec.Loops {
		for i := range loop {
			j := (i + 1) % len(loop)
			dx, dy := loop[j][0]-loop[i][0], loop[j][1]-loop[i][1]
			// Outward for a counter-clockwise outline. (dy, -dx) and not
			// (-dy, dx): on a square wound counter-clockwise the bottom edge
			// runs +x, and the outward direction is -y. A HOLE is wound the
			// other way, so the same formula points into the hole — which is out
			// of the material, which is what outward means there.
			n := normalise([3]float64{dy, -dx, 0})
			a := [3]float64{loop[i][0], loop[i][1], -half}
			b := [3]float64{loop[j][0], loop[j][1], -half}
			c := [3]float64{loop[j][0], loop[j][1], half}
			d := [3]float64{loop[i][0], loop[i][1], half}
			out = appendNonDegenerate(out, Triangle{A: a, B: b, C: c, Normal: n})
			out = appendNonDegenerate(out, Triangle{A: a, B: c, C: d, Normal: n})
		}
	}
	return out, dev
}

// revolved tessellates an outline turned a full circle about its own axis.
//
// # Why there is no triangulation here
//
// An extrusion needs its caps triangulated. A full revolve has none: the surface
// closes on itself, and every facet is a quad between two adjacent outline
// points at two adjacent angles. The outline's winding still matters, because it
// decides which way those quads face.
//
// The segment count is the renderer's own radial count, so a revolved boss and a
// cylinder beside it are tessellated to the same fineness — and the exported
// file is the surface that was on screen, which is what the tessellation fence
// exists to keep true.
func revolved(p Part, unit Unit, infer func(string, ...any)) ([]Triangle, *Deviation) {
	outer, holes, dev, err := flattenSection(partOutline(p), partHoles(p), unit)
	if err != nil {
		infer("%s: %v, so it is not drawn.", p.Label(), err)
		return nil, nil
	}
	if len(outer) < minProfilePoints {
		return nil, nil
	}
	// Normalised the same way the extrusion does, and for the same reason: the
	// facet winding below is only outward for a counter-clockwise outline, and an
	// inside-out solid is a defect this repository has shipped once.
	loops := sectionLoops(outer, holes)
	for _, loop := range loops {
		if selfIntersects(loop) {
			infer("%s: this outline crosses itself, so the shape it would sweep is not a "+
				"solid; it is drawn as FORGE read it.", p.Label())
			break
		}
	}

	aboutX := RevolveAxis(p) == "x"
	// at maps an outline point and an angle to a point on the swept surface.
	// Turning about Y, the outline's x is the radius and its y stays; turning
	// about X, the other way round.
	at := func(pts [][2]float64, i int, t float64) [3]float64 {
		if aboutX {
			r := pts[i][1]
			return [3]float64{pts[i][0], r * math.Cos(t), r * math.Sin(t)}
		}
		r := pts[i][0]
		return [3]float64{r * math.Cos(t), pts[i][1], r * math.Sin(t)}
	}

	out := make([]Triangle, 0, len(outer)*radialSegments*2)
	for seg := 0; seg < radialSegments; seg++ {
		t0 := float64(seg) / float64(radialSegments) * 2 * math.Pi
		t1 := float64(seg+1) / float64(radialSegments) * 2 * math.Pi
		// Every loop is turned: the outline sweeps the outside of the part and a
		// hole sweeps a surface inside it, wound the other way so its facets face
		// into the void.
		for _, pts := range loops {
			for i := range pts {
				j := (i + 1) % len(pts)
				a, b := at(pts, i, t0), at(pts, j, t0)
				c, d := at(pts, j, t1), at(pts, i, t1)
				// The normal comes from the facet itself rather than from a
				// formula per axis: the two axes have opposite handedness and a
				// formula written for one is silently inverted for the other.
				out = appendNonDegenerate(out, Triangle{A: a, B: b, C: c, Normal: faceNormal(a, b, c)})
				out = appendNonDegenerate(out, Triangle{A: a, B: c, C: d, Normal: faceNormal(a, c, d)})
			}
		}
	}
	return out, dev
}

// swept tessellates an outline carried along a path (see sweep.go).
//
// # Why this shares its geometry with the measurement path and the kernel
//
// Where each ring of points ends up is the whole of what a sweep IS, and it is
// decided by sweptSections. This function turns rings into triangles and does
// not decide anything else — the alternative is a viewport, an exporter and a
// bounding box that each frame the section their own way, and a part that is
// drawn one way, measured another and built a third.
//
// # Winding
//
// triangulate normalises the outline counter-clockwise, so the caps and the
// facet order below are outward for the same reason the extrusion's are. The
// facet normals are computed from the facets rather than from a formula, because
// a path may point anywhere and a formula written for one direction is silently
// inverted for another.
func swept(p Part, unit Unit, infer func(string, ...any)) ([]Triangle, *Deviation) {
	outer, holes, sectionDev, oerr := flattenSection(partOutline(p), partHoles(p), unit)
	flatPath, pathDev, perr := partPath(p).flatten("path", unit)
	if err := firstOf(oerr, perr); err != nil {
		infer("%s: %v, so it is not drawn.", p.Label(), err)
		return nil, nil
	}
	dev := worseDeviation(sectionDev, pathDev)
	sec := triangulateLoops(outer, holes)
	if !sec.OK {
		infer("%s: this outline could not be closed into a surface — it crosses itself, "+
			"repeats a point, or has a hole that will not fit inside it — so it is drawn "+
			"only as far as FORGE could read it.", p.Label())
	}
	if len(sec.Tris) == 0 {
		return nil, nil
	}

	// The merged ring is carried along the path as loop ZERO, so the caps can be
	// built from the rings like everything else rather than from a second
	// transformation that could disagree with them. At the two ends the mitre is
	// the identity, so its ring is exactly the section as drawn.
	loops := append([][][2]float64{sec.Merged}, sec.Loops...)
	rings, _, err := sweptSections(loops, flatPath, p.PathClosed)
	if err != nil {
		// Drawn anyway when there is anything to draw, and named. A part that
		// vanishes from a render reads as a design with a piece missing; the
		// export path refuses the same document, and the banner says which.
		infer("%s: %v. It is drawn as FORGE read it, and it is not a solid.", p.Label(), err)
	}
	if len(rings) == 0 || len(rings[0]) < minPathPoints {
		return nil, nil
	}
	caps, walls := rings[0], rings[1:]
	vertices := len(caps)
	last := vertices - 1

	out := make([]Triangle, 0, len(sec.Tris)*2+len(sec.Merged)*vertices*2)
	// The two ends — unless there are none. A CLOSED path has no ends: the
	// surface closes on itself at the seam, and a cap there would be a disc
	// standing in the middle of the material.
	if !p.PathClosed {
		startNormal := normalise(sub3(caps[0][0], caps[1][0]))
		endNormal := normalise(sub3(caps[last][0], caps[last-1][0]))
		for _, t := range sec.Tris {
			out = appendNonDegenerate(out, Triangle{
				A: caps[0][t[2]], B: caps[0][t[1]], C: caps[0][t[0]], Normal: startNormal})
			out = appendNonDegenerate(out, Triangle{
				A: caps[last][t[0]], B: caps[last][t[1]], C: caps[last][t[2]], Normal: endNormal})
		}
	}
	segments := last
	if p.PathClosed {
		segments = vertices
	}
	for l, loop := range sec.Loops {
		ring := walls[l]
		for i := 0; i < segments; i++ {
			for k := range loop {
				next := (k + 1) % len(loop)
				onward := (i + 1) % vertices
				a, b := ring[i][k], ring[i][next]
				c, d := ring[onward][next], ring[onward][k]
				out = appendNonDegenerate(out, Triangle{A: a, B: b, C: c, Normal: faceNormal(a, b, c)})
				out = appendNonDegenerate(out, Triangle{A: a, B: c, C: d, Normal: faceNormal(a, c, d)})
			}
		}
	}
	return out, dev
}

// revolveRadius is the largest radius the outline sweeps, which is what decides
// how far the tessellated surface departs from the true one.
func revolveRadius(p Part) float64 {
	// The FLATTENED outline, because a rounded corner can be the outermost thing
	// on it — the crown of a bead, the outer edge of a rounded flange — and the
	// vertex it was rounded from sits further out than any material does.
	// Measuring the vertex would overstate the deviation, which is the safe
	// direction but is still a number that is not true of the file.
	flat, _, err := partOutline(p).flatten("outline", Millimetre)
	if err != nil {
		return 0
	}
	aboutX := RevolveAxis(p) == "x"
	var r float64
	for _, pt := range flat {
		v := pt[0]
		if aboutX {
			v = pt[1]
		}
		r = math.Max(r, math.Abs(v))
	}
	return r
}

func faceNormal(a, b, c [3]float64) [3]float64 {
	u := [3]float64{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	v := [3]float64{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
	return normalise([3]float64{
		u[1]*v[2] - u[2]*v[1],
		u[2]*v[0] - u[0]*v[2],
		u[0]*v[1] - u[1]*v[0],
	})
}

// TrianglesFrom turns a flat vertex buffer and an index buffer into triangles.
//
// # Why this lives here
//
// It is the shape a mesh has on a wire — a []float64 of coordinates and an
// []int32 of indices — turned into the shape this package draws. Both ends
// already speak Triangle, so the translation belongs beside it rather than in
// whichever caller happens to need it first; it is now needed by the CAD
// kernel's surface and by the tests that stand in for one.
//
// ‼️ An index outside the buffer is SKIPPED, not trusted. This data crosses a
// process boundary from a Python sidecar, and an out-of-range index would panic
// the rasterizer — on the path that draws a picture for an automated checker,
// which is exactly where a crash would be least explicable and least expected.
//
// The normal is computed rather than read, because the kernel's mesh carries no
// per-facet normal and the renderer shades by it. A zero normal draws every
// facet in one flat colour, and a picture in one flat colour cannot answer the
// question the checks exist to ask.
func TrianglesFrom(verts []float64, idx []int32) []Triangle {
	at := func(i int32) ([3]float64, bool) {
		o := int(i) * 3
		if o < 0 || o+2 >= len(verts) {
			return [3]float64{}, false
		}
		return [3]float64{verts[o], verts[o+1], verts[o+2]}, true
	}
	out := make([]Triangle, 0, len(idx)/3)
	for i := 0; i+2 < len(idx); i += 3 {
		a, oka := at(idx[i])
		b, okb := at(idx[i+1])
		c, okc := at(idx[i+2])
		if !oka || !okb || !okc {
			continue
		}
		out = append(out, Triangle{A: a, B: b, C: c, Normal: faceNormalOf(a, b, c)})
	}
	return out
}

func faceNormalOf(a, b, c [3]float64) [3]float64 {
	u := [3]float64{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	v := [3]float64{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
	n := [3]float64{u[1]*v[2] - u[2]*v[1], u[2]*v[0] - u[0]*v[2], u[0]*v[1] - u[1]*v[0]}
	l := math.Sqrt(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])
	if l == 0 {
		return [3]float64{0, 0, 1}
	}
	return [3]float64{n[0] / l, n[1] / l, n[2] / l}
}
