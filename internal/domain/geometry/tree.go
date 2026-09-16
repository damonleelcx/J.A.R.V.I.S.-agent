package geometry

import (
	"fmt"
	"strings"
)

// Designs placed inside assemblies.
//
// Phase 1, stage D1b of docs/plan-2026-09-13-millions-of-parts.md.
//
// # What this is
//
// A Document can describe an assembly two ways, and one document may use both:
//
//   - Parts: solids at absolute positions — every document before 2026-09-13.
//   - Definitions placed from Root through Assemblies: a design written ONCE and
//     placed by every child that names it, inside assemblies that are themselves
//     placed. A car's suspension corner is one assembly placed four times.
//
// # Why readers never see the tree
//
// Every reader — the kernel request, the mesh, faults, measurement, the browser —
// reads parts at absolute positions, and D1a made them all read one expansion. The
// tree is flattened by that expansion, first, into ordinary parts whose ids are the
// path of child ids from the root ("front-left/damper"). Nothing downstream has to
// learn a new shape, and what is STORED grows with designs and placements rather
// than with parts — which is the whole point at a million of them.
//
// # What it does not do yet, on purpose
//
// Patterns and mirror on a child (D1c), named interfaces to attach to (D1d) and
// features scoped to an assembly (D1e) are later stages, each with its own fences.
// A definition's own "repeat" IS honoured, in the definition's own frame, so the
// spokes of a wheel turn with the wheel wherever it is placed.

// Assembly is a group of placed children.
type Assembly struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Interfaces are named mounting frames a child may be attached at, here or
	// from a sibling ("front-left/hub"). See interface.go.
	Interfaces []Interface `json:"interfaces,omitempty"`
	Children   []Child     `json:"children"`
}

// Child places a definition, or another assembly, inside an assembly.
type Child struct {
	// ID names this placement within its assembly. It becomes one segment of the
	// id of every part beneath it, so it may not contain PathSeparator.
	ID string `json:"id"`
	// Ref is the id of a definition or of an assembly in the same document.
	Ref  string `json:"ref"`
	Name string `json:"name,omitempty"`
	// Position and Rotation place the child in its assembly's frame, in the
	// document's units and in DEGREES about x then y then z — the same convention
	// a part uses, so nothing about a child's placement reads differently.
	Position []float64 `json:"position,omitempty"`
	Rotation []float64 `json:"rotation,omitempty"`
	// Mirror reflects the child across the plane normal to "x", "y" or "z" in its
	// own frame, before it is rotated and placed — a whole sub-assembly included.
	// Two mirrors on the way down cancel. Empty means no reflection.
	Mirror string `json:"mirror,omitempty"`
	// Pattern writes the child out as several copies, each placed by the pattern
	// in this assembly's frame (pattern.go). Nil means one copy.
	Pattern *Pattern `json:"pattern,omitempty"`
	// At attaches the child to an interface: one on this assembly ("mount") or on
	// a sibling's placement ("front-left/hub", "bolt-3/seat"). The child's
	// position, rotation, mirror and pattern are then measured in that
	// interface's frame. Empty means this assembly's frame. See interface.go.
	At string `json:"at,omitempty"`
}

// PathSeparator joins child ids into the id of a flattened part.
const PathSeparator = "/"

// maxTreeDepth bounds nesting. A real product structure is a handful of levels
// deep; a document forty levels deep is a cycle somebody unrolled, and walking it
// is a way for one document to spend the whole turn.
const maxTreeDepth = 16

// maxTreeParts bounds what one tree may flatten to.
//
// ‼️ This is a PRE-INSTANCING ceiling, not a statement of what a tree can describe.
// Until Phase 4 (K1, one build per definition) and Phase 6 (W1, instanced drawing)
// land, every flattened part is an independent solid in the kernel request and an
// independent draw call in the browser, and the measured cost of checking one
// against another is already 0.66 s at 120 parts. It is raised on measured numbers
// when those stages land.
const maxTreeParts = 4096

// hasTree reports whether the document uses definitions and assemblies at all.
func (d Document) hasTree() bool {
	return d.Root != "" || len(d.Assemblies) > 0 || len(d.Definitions) > 0
}

// HasGeometry reports whether the document places anything: parts at the top
// level, or a root that places a tree. Replaces "len(d.Parts) > 0" everywhere a
// document that is only a tree would otherwise read as empty.
func (d Document) HasGeometry() bool { return len(d.Parts) > 0 || d.Root != "" }

// PlacedParts is every part the document places, BEFORE repeats and gears are
// written out: the top-level parts, then the tree's. Readers that ask which parts
// exist (validation, states) read this; readers that draw or build read Expanded.
func (d Document) PlacedParts() []Part {
	e, _ := expandAssemblies(d)
	return e.Parts
}

// TreeProblems is everything wrong with how the document's tree is put together.
// Empty for a document with no tree.
func (d Document) TreeProblems() []Problem {
	_, problems := expandAssemblies(d)
	return problems
}

// expandAssemblies returns the document with its tree written out as parts after
// any top-level parts, and says what it could not place.
//
// Idempotent, like the other expansions: the result carries no tree, so expanding
// it again changes nothing — which is what lets every reader call it without
// coordinating.
func expandAssemblies(d Document) (Document, []Problem) {
	if !d.hasTree() {
		return d, nil
	}
	out := d
	out.Parts = append([]Part(nil), d.Parts...)
	out.Definitions, out.Assemblies, out.Root = nil, nil, ""

	var problems []Problem
	fail := func(name, format string, args ...any) {
		problems = append(problems, Problem{Severity: Error, Name: name, Detail: fmt.Sprintf(format, args...)})
	}

	if d.Root == "" {
		fail("assembly", "has %d definition(s) and %d assembly(ies) but names no root, so none of them "+
			"is placed; set \"root\" to the assembly that holds everything", len(d.Definitions), len(d.Assemblies))
		return out, problems
	}

	defs := map[string]Part{}
	for _, p := range d.Definitions {
		switch {
		case strings.TrimSpace(p.ID) == "":
			fail("definition", "has no id, so no child can place it")
		case defs[p.ID].ID != "":
			fail(p.ID, "is defined twice, so a child that places it is ambiguous")
		default:
			defs[p.ID] = p
		}
	}
	asms := map[string]Assembly{}
	for _, a := range d.Assemblies {
		_, isDef := defs[a.ID]
		switch {
		case strings.TrimSpace(a.ID) == "":
			fail("assembly", "has no id, so nothing can place it")
		case asms[a.ID].ID != "":
			fail(a.ID, "is declared twice as an assembly")
		case isDef:
			fail(a.ID, "names both a definition and an assembly, so a child that refers to it is ambiguous")
		default:
			asms[a.ID] = a
		}
	}
	for _, a := range d.Assemblies {
		for _, detail := range interfaceProblems(a) {
			fail(a.ID, "%s", detail)
		}
	}
	root, ok := asms[d.Root]
	if !ok {
		fail(d.Root, "is named as the root but is not an assembly in this document")
		return out, problems
	}

	placed := 0
	attach := newAttachments(asms)
	// The frame is a placement (frame.go), not a position and three angles, so a
	// reflection anywhere above a part reaches the part.
	var walk func(a Assembly, path []string, onPath map[string]bool, frame placement) (stop bool)
	walk = func(a Assembly, path []string, onPath map[string]bool, frame placement) bool {
		if len(path) >= maxTreeDepth {
			fail(strings.Join(path, PathSeparator), "nests more than %d assemblies deep", maxTreeDepth)
			return true
		}
		ids := map[string]bool{}
		for _, c := range a.Children {
			if strings.TrimSpace(c.ID) == "" || strings.Contains(c.ID, PathSeparator) {
				fail(a.ID, "places a child with the id %q; a child id is one segment of every part id "+
					"beneath it, so it must be non-empty and contain no %q", c.ID, PathSeparator)
				continue
			}
			if ids[c.ID] {
				fail(a.ID, "places two children with the id %q, so the parts beneath them would share ids", c.ID)
				continue
			}
			ids[c.ID] = true
			name := strings.Join(append(append([]string(nil), path...), c.ID), PathSeparator)
			reflect, ok := reflectionAcross(c.Mirror)
			if !ok {
				fail(name, "mirrors across %q; a mirror is across \"x\", \"y\" or \"z\"", c.Mirror)
				continue
			}
			local := placementOf(c.Position, c.Rotation, false)
			local.m = mulMat3(local.m, reflect)

			// What the child places is resolved ONCE, before its copies: an unknown
			// ref or a cycle is one problem about the child, not one per copy.
			sub, isAsm := asms[c.Ref]
			def, isDef := defs[c.Ref]
			if isAsm && onPath[sub.ID] {
				fail(name, "places assembly %q inside itself", sub.ID)
				continue
			}
			if !isAsm && !isDef {
				fail(name, "places %q, which is neither a definition nor an assembly in this document", c.Ref)
				continue
			}
			// A pattern writes the child out as copies, each placed by the pattern in
			// the frame the child is measured in (pattern.go). No pattern is one copy,
			// unnamed.
			slots, patternProblem := c.Pattern.copies()
			if patternProblem != nil {
				patternProblem.Name = name
				problems = append(problems, *patternProblem)
				if patternProblem.Severity == Error {
					continue
				}
			}
			// Attached at an interface, the child is measured in that interface's frame
			// -- its position, rotation, mirror and pattern alike (interface.go).
			reference, attachProblem := attach.reference(a, c, "")
			if attachProblem != "" {
				fail(name, "%s", attachProblem)
				continue
			}
			// The definition's own repeat, once, in the DEFINITION's frame, so a pattern
			// "about the origin" turns about the part's own origin wherever it is placed.
			var defCopies []Part
			if isDef {
				expanded, repeatProblems := expandRepeats(Document{Parts: []Part{def}})
				for _, rp := range repeatProblems {
					rp.Name = name
					problems = append(problems, rp)
				}
				defCopies = expanded.Parts
			}
			for _, slot := range slots {
				childPath := append(append([]string(nil), path...), c.ID+slot.suffix)
				slotName := strings.Join(childPath, PathSeparator)
				childName := c.Name
				if childName != "" && slot.number != "" {
					childName = c.Name + " " + slot.number
				}
				childFrame := frame.then(reference.then(slot.at.then(local)))
				if isAsm {
					onPath[sub.ID] = true
					stop := walk(sub, childPath, onPath, childFrame)
					delete(onPath, sub.ID)
					if stop {
						return true
					}
					continue
				}
				for _, lp := range defCopies {
					if placed >= maxTreeParts {
						fail(d.Root, "places more than %d parts, which is the most one tree may place until "+
							"instanced drawing and one build per design land", maxTreeParts)
						return true
					}
					q := lp
					// "" for the definition itself, "-k" for its k-th copy: read off the
					// expansion's own answer rather than restating its naming rule.
					suffix := strings.TrimPrefix(lp.ID, def.ID)
					q.ID = slotName + suffix
					if childName != "" {
						q.Name = childName
						if suffix != "" {
							q.Name = childName + " " + strings.TrimPrefix(suffix, "-")
						}
					}
					q.Position, q.Rotation, q.Mirrored = childFrame.then(placementOf(lp.Position, lp.Rotation, lp.Mirrored)).stored()
					q.Size = cloneSize(lp.Size)
					out.Parts = append(out.Parts, q)
					placed++
				}
			}
		}
		return false
	}
	walk(root, nil, map[string]bool{root.ID: true}, placementOf(nil, nil, false))
	return out, problems
}
