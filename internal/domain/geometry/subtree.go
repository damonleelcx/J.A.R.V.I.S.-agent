package geometry

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// One subtree of a design, and its surface tessellated once per shape. Phase 6, stage W2.
//
// # The problem this solves
//
// Until W2 the viewport uploaded every occurrence of a design at load: a 30,000-part
// car put 30,000 instances on the GPU before anybody had looked at anything, and the
// mesh endpoint could only answer for the WHOLE design — which, past the 4,096 parts
// FORGE builds at once (maxDrawnParts), it refuses. So a large tree had no built
// surface at all, and no way to fetch a piece of one.
//
// A Subtree is what one occurrence path places ("front-left", "seam-12",
// "front-left-2/pin"): the parts the exporter writes out beneath it, and the features
// that stay wholly inside it. The mesh endpoint answers for one (GET
// /v1/geometry/{id}/mesh?subtree=), and the workbench asks for a subtree when a row is
// opened rather than for everything up front.
//
// # Why a path reaches what the browser's row reaches
//
// The rule is forge3d.js occurrenceMatcher's, word for word: each segment may carry
// the "-n" copy numbers a pattern or a repeat adds at any level, so "front-left/pin"
// reaches "front-left-2/pin-3-1". A second rule here would let the server send a
// subtree the browser's row does not light, and the row would be drawn with parts
// missing or doubled. ‼️ It inherits that rule's one ambiguity too: a sibling literally
// called "front-left-2" is reached by "front-left" (TestRendererFlattensATreeLikeTheExporter).

// Subtree is one occurrence path's share of a design.
type Subtree struct {
	// Path is the occurrence path as it was asked for.
	Path string
	// Parts are the placed parts beneath it, expanded, in the exporter's order.
	Parts []Part
	// Features are the expanded features whose target and tools are all beneath it.
	Features []Feature
	// Outside names each feature left out because it reaches a part outside the
	// path. Named, not dropped: a cut whose tool lives in a sibling subtree is not
	// applied in this subtree's mesh, and a surface quietly missing a hole is wrong
	// in a way nobody notices.
	Outside []string

	expanded Document
}

// Subtree resolves an occurrence path. A path that is malformed is refused as
// invalid; a well-formed path that places nothing in this design is refused as not
// found, naming the path — never answered with an empty mesh, which a viewport would
// draw as "this subtree is empty".
func (d Document) Subtree(path string) (*Subtree, error) {
	const op = "geometry.Document.Subtree"
	p := strings.TrimSpace(path)
	if p == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a subtree is named by an occurrence path such as \"front-left\" or \"seam-12/rivet-3\", and none was given")
	}
	for _, seg := range strings.Split(p, PathSeparator) {
		if strings.TrimSpace(seg) == "" {
			return nil, errs.New(op, errs.CodeValidationFailed).
				WithDetail("%q is not an occurrence path: every segment between %q must name a child", p, PathSeparator)
		}
	}
	under := OccurrenceUnder(p)
	e := d.expandedWithin(p)
	s := &Subtree{Path: p, expanded: e}
	in := map[string]bool{}
	for _, part := range e.Parts {
		if under(part.ID) {
			s.Parts = append(s.Parts, part)
			in[part.ID] = true
		}
	}
	if len(s.Parts) == 0 {
		return nil, errs.New(op, errs.CodeNotFound).
			WithDetail("no occurrence %q in this design: nothing the design places is at or beneath that path", p)
	}
	for _, f := range e.Features {
		named := f.With
		if f.Of != "" {
			named = append([]string{f.Of}, f.With...)
		}
		touched := 0
		for _, id := range named {
			if in[id] {
				touched++
			}
		}
		// A feature on parts elsewhere in the design is none of this subtree's
		// business; one wholly inside is kept; one that straddles the path is named.
		if touched == 0 {
			continue
		}
		if touched == len(named) {
			s.Features = append(s.Features, f)
			continue
		}
		name := f.ID
		if name == "" {
			name = strings.ToLower(f.Op)
		}
		s.Outside = append(s.Outside, fmt.Sprintf("%s (%s of %s) reaches a part outside %s, so it is not "+
			"applied in this subtree's mesh; the whole design's export applies it", name,
			strings.ToLower(f.Op), labelOf(e, f.Of), p))
	}
	return s, nil
}

// expandedWithin is Expanded, placing only the part of the tree that can lead to path.
//
// ‼️ Found by the 2026-09-17 workbench check: asking for ONE rivet of a stored
// 1,020,782-part design took 5.9 s, because every subtree request expanded the whole
// design to pick out what was under the path. The parts under the path are placed
// exactly as the whole expansion places them — a placement's frame depends only on the
// placements above it — and Subtree keeps only those.
//
// Not pruned when any assembly has features of its own: an assembly's feature names parts
// by where they were placed (tree_features.go), and a feature that straddles the path
// must still be found and NAMED in Outside, which needs the parts outside the path placed.
// Fence: TestSubtree_PlacesOnlyWhatLeadsToItsPathAndTheSameParts.
func (d Document) expandedWithin(path string) Document {
	for _, a := range d.Assemblies {
		if len(a.Features) > 0 {
			return d.Expanded()
		}
	}
	e, _ := expandTree(d, nil, occurrencePrefix(path))
	e, _ = expandStandards(e)
	e, _ = expandGears(e)
	e, _ = expandRepeats(e)
	return e
}

// occurrencePrefix accepts a placement's path when each of its segments matches the
// asked path's segment at that depth by OccurrenceUnder's rule (copy numbers allowed):
// a placement that can be at, above or under the path. forge3d.js has the same.
func occurrencePrefix(path string) func([]string) bool {
	parts := strings.Split(path, PathSeparator)
	segs := make([]*regexp.Regexp, len(parts))
	for i, s := range parts {
		segs[i] = regexp.MustCompile("^" + regexp.QuoteMeta(s) + `(-\d+)*$`)
	}
	return func(childPath []string) bool {
		for i := 0; i < len(childPath) && i < len(segs); i++ {
			if !segs[i].MatchString(childPath[i]) {
				return false
			}
		}
		return true
	}
}

// OccurrenceUnder reports whether a placed part's id is at or beneath an occurrence
// path, by forge3d.js occurrenceMatcher's rule (see the top of this file).
func OccurrenceUnder(path string) func(id string) bool {
	segs := strings.Split(path, PathSeparator)
	for i, s := range segs {
		segs[i] = regexp.QuoteMeta(s) + `(-\d+)*`
	}
	re := regexp.MustCompile("^" + strings.Join(segs, PathSeparator) + "(/|$)")
	return re.MatchString
}

// Document is the subtree as a flat document of its own: the design with only these
// parts and features, carrying its units, parameters and name. What a builder is sent.
func (s *Subtree) Document() Document {
	out := s.expanded
	out.Parts = s.Parts
	out.Features = s.Features
	return out
}

// Refusal is why the viewport is not sent this subtree, or "".
//
// ‼️ Measured on the SUBTREE, not the design (Phase 6, stage W2): the point of asking
// for one subtree is that it is smaller than the whole. The ceiling is the viewport's
// own (maxViewportParts) and is unchanged by this; what is new is only what it counts.
func (s *Subtree) Refusal() string {
	if len(s.Parts) <= maxViewportParts {
		return ""
	}
	return fmt.Sprintf("The subtree %s places %d parts, more than %d, which is the most the FORGE viewport "+
		"draws at once. Open a row beneath it instead; nothing was sent.", s.Path, len(s.Parts), maxViewportParts)
}

// Buildable reports whether the kernel may be asked to build this subtree: whether it
// places no more than the parts the kernel builds at once for a view (maxBuiltParts).
func (s *Subtree) Buildable() bool { return len(s.Parts) <= maxBuiltParts }

// MaxBuiltParts is the kernel's ceiling, exported so the mesh endpoint can say in its
// reply why a subtree was not built by the kernel.
func MaxBuiltParts() int { return maxBuiltParts }

// InstanceMesh is a design's surface as the K4 reply carries it, made by the Go
// tessellator instead of the kernel: each distinct shape's triangles once, in its own
// frame, and every placed part as a 4×4 column-major matrix of one of them. All in
// MILLIMETRES, as the kernel's reply is, so the browser draws both the same way.
type InstanceMesh struct {
	Definitions []InstanceDefinition
	Instances   []PlacedInstance
	// Triangles counts each definition once, however many copies place it.
	Triangles int
	// Inferences is everything the tessellator decided that the document did not
	// say, and every feature it did not perform — in Tessellate's words.
	Inferences []string
}

// InstanceDefinition is one shape's surface in its own frame, in millimetres.
type InstanceDefinition struct {
	Vertices  []float64
	Triangles []int32
}

// PlacedInstance is one placed copy of a definition. A mirrored part's reflection is
// in its DEFINITION, not its matrix — the convention the kernel's reply follows (K1
// builds the mirrored shape as a shape of its own), so the two replies are read alike.
type PlacedInstance struct {
	ID, Label  string
	Definition int
	Matrix     [16]float64
}

// TessellateInstances is Tessellate, written out once per distinct shape.
//
// # Why this exists beside Tessellate
//
// Tessellate places every part's triangles in assembly coordinates, which is what a
// file needs and what 30,000 copies of a rivet cannot afford: it is bounded by
// maxDrawnParts for that reason. The viewport needs each shape once and a matrix per
// copy, so what grows per copy here is sixteen numbers.
//
// ‼️ Bounded by the VIEWPORT's ceiling, not the builder's: this serves only the
// viewport, and the viewport's ceiling is the one measured for drawing a copy
// (limits.go). Exports still go through Tessellate and stay at maxDrawnParts.
//
// Features are not performed — there are no booleans here, as in Tessellate — and
// are named in Tessellate's own words: the notes come from running Tessellate over
// just the parts the features name, so there is one copy of that wording.
func TessellateInstances(doc Document, unit Unit) *InstanceMesh {
	if occurrences(doc, maxViewportParts) > maxViewportParts {
		return &InstanceMesh{Inferences: []string{fmt.Sprintf("This design places more than %d parts, which "+
			"is the most the FORGE viewport draws at once; nothing was tessellated.", maxViewportParts)}}
	}
	scale, ok := unit.toMM()
	if !ok {
		return &InstanceMesh{Inferences: []string{"This design has no unit FORGE can convert, so its surface " +
			"cannot be stated in millimetres; nothing was tessellated."}}
	}
	doc, treeProblems := expandAssemblies(doc)
	doc, standardProblems := expandStandards(doc)
	doc, gearProblems := expandGears(doc)
	doc, repeatProblems := expandRepeats(doc)

	m := &InstanceMesh{}
	seen := map[string]bool{}
	infer := func(format string, args ...any) {
		s := fmt.Sprintf(format, args...)
		if !seen[s] {
			seen[s] = true
			m.Inferences = append(m.Inferences, s)
		}
	}
	for _, p := range append(append(append(treeProblems, standardProblems...), gearProblems...), repeatProblems...) {
		infer("%s %s.", p.Name, p.Detail)
	}

	// The feature notes, in Tessellate's words, from the parts the features name.
	if len(doc.Features) > 0 {
		named := map[string]bool{}
		for _, f := range doc.Features {
			named[f.Of] = true
			for _, id := range f.With {
				named[id] = true
			}
		}
		few := Document{Name: doc.Name, Units: doc.Units, Parameters: doc.Parameters, Derived: doc.Derived,
			Features: doc.Features}
		for _, p := range doc.Parts {
			if named[p.ID] {
				few.Parts = append(few.Parts, p)
			}
		}
		for _, s := range Tessellate(few, unit).Inferences {
			infer("%s", s)
		}
	}

	byKey := map[string]int{}
	for _, p := range doc.Parts {
		key := shapeKey(p)
		def, known := byKey[key]
		if !known {
			local, _ := partTriangles(p, unit, infer)
			def = -1
			if len(local) > 0 {
				def = len(m.Definitions)
				m.Definitions = append(m.Definitions, definitionOf(local, p.Mirrored, scale))
				m.Triangles += len(local)
			}
			byKey[key] = def
		}
		if def < 0 {
			continue
		}
		m.Instances = append(m.Instances, PlacedInstance{ID: p.ID, Label: p.Label(), Definition: def,
			Matrix: instanceMatrix(p, scale)})
	}
	sort.SliceStable(m.Inferences, func(i, j int) bool { return m.Inferences[i] < m.Inferences[j] })
	return m
}

// shapeKey is every field partTriangles reads, and the reflection: two parts with one
// key are the same triangles in their own frames.
func shapeKey(p Part) string {
	b, _ := json.Marshal([]any{p.Shape, p.Standard, p.Lattice, p.Size, p.Profile, p.Holes, p.Path, p.PathClosed, p.Axis, p.Mirrored})
	return string(b)
}

// definitionOf flattens a part's local triangles into shared vertices, reflected when
// the part is mirrored (mirrorTriangle, which keeps the winding facing out) and scaled
// to millimetres. A vertex is shared only by triangles that also share its normal, so a
// box's edges stay sharp when the browser averages normals at shared vertices.
func definitionOf(local []Triangle, mirrored bool, scale float64) InstanceDefinition {
	type key struct{ p, n [3]float64 }
	index := map[key]int32{}
	var out InstanceDefinition
	for _, t := range local {
		if mirrored {
			t = mirrorTriangle(t)
		}
		for _, v := range [3][3]float64{t.A, t.B, t.C} {
			k := key{v, t.Normal}
			i, ok := index[k]
			if !ok {
				i = int32(len(out.Vertices) / 3)
				index[k] = i
				out.Vertices = append(out.Vertices, v[0]*scale, v[1]*scale, v[2]*scale)
			}
			out.Triangles = append(out.Triangles, i)
		}
	}
	return out
}

// instanceMatrix is where a part's own frame goes: T · R, column-major, in millimetres.
// The reflection is already in the definition (definitionOf).
func instanceMatrix(p Part, scale float64) [16]float64 {
	r := RotationMatrix(p.RotationRadians())
	pos := padTo3(p.Position)
	return [16]float64{r[0], r[3], r[6], 0, r[1], r[4], r[7], 0, r[2], r[5], r[8], 0,
		pos[0] * scale, pos[1] * scale, pos[2] * scale, 1}
}
