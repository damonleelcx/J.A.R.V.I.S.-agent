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
	// Features are cuts, fuses and fillets between the parts this assembly places,
	// named by path from here and applied in every occurrence (tree_features.go).
	Features []Feature `json:"features,omitempty"`
	Children []Child   `json:"children"`
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
	// PositionFrom binds the child's position to expressions over the document's
	// parameters, keyed "x", "y" and "z", exactly as a part's position_from is
	// (binding.go). Bind writes what they work out to into Position, so the expansion,
	// the kernel and the browser read numbers and nothing else changes for them.
	//
	// # Why a child needs one
	//
	// #97 and #111 read a child written at ["-half_wheelbase", 0, 0] and stored the
	// NUMBER: a respec of half_wheelbase then moved every definition bound to it and
	// left the wheels where they were. A placement is where most of a tree's
	// dimensions live (a car's track and wheelbase are where its corners are placed,
	// not how big a corner is), so a binding only definitions can carry follows the
	// wrong half of the design. Optional: a child with none stores byte-identically.
	// Fence: TestBind_AChildsAndAnInterfacesPositionFollowTheirParameters.
	PositionFrom map[string]string `json:"position_from,omitempty"`
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

// NameSeparator joins the display names along a part's occurrence path, the way
// PathSeparator joins its ids: a part a tree places is named by every child above
// it (the child's name, or its id when it has none, with its pattern number) and
// then by its own name, e.g. "Front wheel / Hub" or "left / Ell 1".
//
// Why: names used to come from a child only when the child was named, and only
// one level down, so an unnamed child's parts, the copies of an unnamed pattern,
// and every occurrence of a sub-assembly repeated the definition's names: two
// "Ell 1"s, two "Hub"s, in the Parts panel and in STEP files alike, while their
// ids were distinct. Built from the same path as the id, a name is unique
// whenever sibling names are, and does not change when an unrelated part is added.
// See docs/bugfix/2026-09-14-tree-copies-shared-display-names.md.
// Fence: TestTree_EveryPartItPlacesIsNamedByItsOccurrence; mirrored in forge3d.js.
const NameSeparator = " / "

// maxTreeDepth bounds nesting. A real product structure is a handful of levels
// deep; a document forty levels deep is a cycle somebody unrolled, and walking it
// is a way for one document to spend the whole turn.
const maxTreeDepth = 16

// The ceiling on how many parts are drawn or built at once lives in limits.go
// (maxDrawnParts): since Phase 3, stage S0 it bounds drawing and building, not the tree.

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
	return expandAssembliesTraced(d, nil)
}

// treeSpan is one placement the walk made: what it placed, and where the parts
// it wrote landed in the expansion. Recorded only when an edit asks
// (edit_paths.go), which reads the spans to resolve a placed path to the design
// it places and to name every occurrence of a design an edit changed.
//
// # Why the walk records it rather than a reader working it out afterwards
//
// A placed id could be parsed back into child ids and pattern suffixes, but a
// child id may itself end in "-3", and a definition's repeat copy ends in "-3"
// too. The walk is the only party that knows which placement wrote which part,
// so it says so, the way a definition's copy suffix is read off the expansion
// below rather than restated.
type treeSpan struct {
	// path is the placement's path from the root ("front-left/hub", "bolt-3"),
	// which is also the id of the part it wrote when it placed a definition; ""
	// for the root itself.
	path string
	// ref is the definition or assembly placed there; assembly says which.
	ref      string
	assembly bool
	// child is "assembly-id/child-id", the child that made this placement; "" for
	// the root.
	child string
	// alias marks a second name for parts another span already covers: a
	// patterned child's bare id, which is every copy, and a definition's repeat
	// copy. Read for paths, never counted as an occurrence.
	alias      bool
	start, end int
}

// expandAssembliesTraced is expandAssemblies, recording every placement in spans
// when spans is not nil. Nil costs one comparison per placement.
func expandAssembliesTraced(d Document, spans *[]treeSpan) (Document, []Problem) {
	record := func(s treeSpan) {
		if spans != nil {
			*spans = append(*spans, s)
		}
	}
	if !d.hasTree() {
		return d, nil
	}
	out := d
	out.Parts = append([]Part(nil), d.Parts...)
	out.Definitions, out.Assemblies, out.Root = nil, nil, ""
	// A copy, not the caller's slice: the tree's features are appended to it below,
	// and appending to a slice with spare capacity writes into the caller's array.
	out.Features = append([]Feature(nil), d.Features...)

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
	// Counted before anything is placed (limits.go, Phase 3 stage S0): nested
	// patterns can describe more occurrences than any machine can hold, and every
	// reader of a tree comes through here.
	if p := occurrenceProblem(d); p != nil {
		problems = append(problems, *p)
		return out, problems
	}

	attach := newAttachments(asms, d.Root)
	// The frame is a placement (frame.go), not a position and three angles, so a
	// reflection anywhere above a part reaches the part.
	//
	// It also returns where each placement's parts landed in out.Parts, by path from
	// the assembly walked, so that assembly's features can name them
	// (tree_features.go). Depth first, so everything one placement writes is contiguous.
	var walk func(a Assembly, path, names []string, onPath map[string]bool, frame placement) (index map[string]partRange, stop bool)
	walk = func(a Assembly, path, names []string, onPath map[string]bool, frame placement) (map[string]partRange, bool) {
		index := map[string]partRange{}
		if len(path) >= maxTreeDepth {
			fail(strings.Join(path, PathSeparator), "nests more than %d assemblies deep", maxTreeDepth)
			return index, true
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
				// ‼️ A path that leaves the assembly it is written in is refused with the
				// fix, not with "has no child": run 2's wheels were repaired four times
				// against that sentence and lost (interface.go, leaves).
				if leaves(a, root, c.At) {
					attachProblem = attach.outsideProblem(a, root, c.At)
				} else {
					// ‼️ And every other attachment refusal carries what to write instead:
					// the paths that attach here. Without one, run 4's repair was told what
					// had failed and nothing it could do (interface.go, attachRemedy).
					attachProblem += "; " + attach.attachRemedy(a, c)
				}
				// The child's own name before it, so the sentence says WHICH child even
				// where it travels without the fault's Name (the repair's prompt did).
				fail(name, "%s", namedChild(c)+attachProblem)
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
			childStart := len(out.Parts)
			for _, slot := range slots {
				slotStart := len(out.Parts)
				childPath := append(append([]string(nil), path...), c.ID+slot.suffix)
				slotName := strings.Join(childPath, PathSeparator)
				// The occurrence's display path, a label per level (see NameSeparator).
				childLabel := c.Name
				if childLabel == "" {
					childLabel = c.ID
				}
				if slot.number != "" {
					childLabel += " " + slot.number
				}
				childNames := append(append([]string(nil), names...), childLabel)
				childFrame := frame.then(reference.then(slot.at.then(local)))
				if isAsm {
					onPath[sub.ID] = true
					subIndex, stop := walk(sub, childPath, childNames, onPath, childFrame)
					delete(onPath, sub.ID)
					for rel, r := range subIndex {
						index[c.ID+slot.suffix+PathSeparator+rel] = r
					}
					index[c.ID+slot.suffix] = partRange{slotStart, len(out.Parts)}
					record(treeSpan{path: slotName, ref: sub.ID, assembly: true, child: a.ID + PathSeparator + c.ID,
						start: slotStart, end: len(out.Parts)})
					if stop {
						return index, true
					}
					continue
				}
				for _, lp := range defCopies {
					partStart := len(out.Parts)
					q := lp
					// "" for the definition itself, "-k" for its k-th copy: read off the
					// expansion's own answer rather than restating its naming rule.
					suffix := strings.TrimPrefix(lp.ID, def.ID)
					q.ID = slotName + suffix
					// The definition copy's own name ("Ell 1") after the path above it.
					q.Name = strings.Join(append(append([]string(nil), childNames...), lp.Label()), NameSeparator)
					q.Position, q.Rotation, q.Mirrored = childFrame.then(placementOf(lp.Position, lp.Rotation, lp.Mirrored)).stored()
					q.Size = cloneSize(lp.Size)
					out.Parts = append(out.Parts, q)
					// A definition's own repeat copy, by its copy id ("rivet-2").
					if suffix != "" {
						index[c.ID+slot.suffix+suffix] = partRange{partStart, len(out.Parts)}
						record(treeSpan{path: q.ID, ref: def.ID, child: a.ID + PathSeparator + c.ID, alias: true,
							start: partStart, end: len(out.Parts)})
					}
				}
				index[c.ID+slot.suffix] = partRange{slotStart, len(out.Parts)}
				record(treeSpan{path: slotName, ref: def.ID, child: a.ID + PathSeparator + c.ID,
					start: slotStart, end: len(out.Parts)})
			}
			// A patterned child named by its own id is every copy, as a repeated part's is.
			if len(slots) > 1 {
				index[c.ID] = partRange{childStart, len(out.Parts)}
				record(treeSpan{path: name, ref: c.Ref, assembly: isAsm, child: a.ID + PathSeparator + c.ID, alias: true,
					start: childStart, end: len(out.Parts)})
			}
		}
		// This assembly's own features, in THIS occurrence, after its children's:
		// the inner assembly's welds are made before the outer one's (tree_features.go).
		out.Features = append(out.Features, occurrenceFeatures(a, path, index, out.Parts, fail)...)
		return index, false
	}
	treeStart := len(out.Parts)
	walk(root, nil, nil, map[string]bool{root.ID: true}, placementOf(nil, nil, false))
	record(treeSpan{ref: root.ID, assembly: true, start: treeStart, end: len(out.Parts)})
	return out, problems
}
