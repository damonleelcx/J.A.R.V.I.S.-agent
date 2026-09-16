package geometry

import (
	"fmt"
	"math"
	"strings"
)

// Interfaces: named mounting frames declared on an assembly.
//
// Phase 1, stage D1d of docs/plan-2026-09-13-millions-of-parts.md. Decided
// 2026-09-14:
//
//   - A child may be attached `at` an interface on its PARENT assembly ("mount")
//     or on a SIBLING's placement ("front-left/hub": a path of child ids ending in
//     an interface id, a pattern copy named by its copy id, "bolt-3/seat"). The
//     path always names ONE placement. An assembly id alone ("corner/hub") would
//     not: a car places its corner four times, and "which hub?" has no answer.
//   - An attached child's position, rotation, mirror and pattern are measured in
//     the interface's frame, exactly as an unattached child's are measured in its
//     assembly's frame. So four bolts "polar about z" at a hub turn about the
//     hub's own axis, wherever the hub is.
//   - Interfaces are declared on assemblies only. A part that needs a mounting
//     frame is wrapped in a one-child assembly, which keeps Part — and every flat
//     document, the contract and the browser's copy of it — unchanged.
//
// Why: a mate is written once. Move the hub on the corner assembly and every wheel
// attached to it follows, in every place the corner is used, with no coordinates
// copied between the corner and whatever mounts on it.
type Interface struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Position and Rotation place the frame in its assembly's frame, in the
	// document's units and in DEGREES about x then y then z, as a child's do.
	Position []float64 `json:"position,omitempty"`
	Rotation []float64 `json:"rotation,omitempty"`
}

// interfaceProblems checks the interfaces one assembly declares: an id a path can
// name, and one frame per id.
func interfaceProblems(a Assembly) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range a.Interfaces {
		switch {
		case strings.TrimSpace(f.ID) == "" || strings.Contains(f.ID, PathSeparator):
			out = append(out, fmt.Sprintf("declares an interface with the id %q; an interface id is the last "+
				"segment of an `at` path, so it must be non-empty and contain no %q", f.ID, PathSeparator))
		case seen[f.ID]:
			out = append(out, fmt.Sprintf("declares two interfaces with the id %q, so a child attached at it "+
				"could sit on either", f.ID))
		default:
			seen[f.ID] = true
		}
	}
	return out
}

// attachments resolves `at` paths against the assemblies of one document.
// resolving holds the children whose frame is being worked out right now, so
// attachments that lead back to themselves are refused instead of recursing
// forever.
type attachments struct {
	asms      map[string]Assembly
	resolving map[string]bool
}

func newAttachments(asms map[string]Assembly) *attachments {
	return &attachments{asms: asms, resolving: map[string]bool{}}
}

// reference is the frame a child of a is measured in, expressed in a's frame: a's
// own frame for an unattached child, the interface's for an attached one. A
// problem reads after the child's name. whole is the `at` path first asked about,
// quoted in every problem so the message names what the author wrote; "" means
// this child's own.
func (r *attachments) reference(a Assembly, c Child, whole string) (placement, string) {
	if c.At == "" {
		return placementOf(nil, nil, false), ""
	}
	if whole == "" {
		whole = c.At
	}
	key := a.ID + "\x00" + c.ID
	if r.resolving[key] {
		return placement{}, fmt.Sprintf("is attached at %q, and following it leads back to %q, so there is no "+
			"frame to measure it in", whole, c.ID)
	}
	r.resolving[key] = true
	defer delete(r.resolving, key)
	return r.interfaceIn(a, c.At, whole)
}

// interfaceIn is the frame an `at` path names, expressed in a's frame.
func (r *attachments) interfaceIn(a Assembly, at, whole string) (placement, string) {
	segs := strings.Split(at, PathSeparator)
	for _, s := range segs {
		if strings.TrimSpace(s) == "" {
			return placement{}, fmt.Sprintf("is attached at %q, which has an empty segment; write it as "+
				"\"interface\" or \"child/…/interface\"", whole)
		}
	}
	if len(segs) == 1 {
		for _, f := range a.Interfaces {
			if f.ID == at {
				return placementOf(f.Position, f.Rotation, false), ""
			}
		}
		return placement{}, fmt.Sprintf("is attached at %q, but assembly %q declares no interface %q%s",
			whole, a.ID, at, declaredInterfaces(a))
	}
	frame, sub, problem := r.childFrame(a, segs[0], whole)
	if problem != "" {
		return placement{}, problem
	}
	rest, problem := r.interfaceIn(sub, strings.Join(segs[1:], PathSeparator), whole)
	if problem != "" {
		return placement{}, problem
	}
	return frame.then(rest), ""
}

// childFrame is where the placement seg — a child id, or one pattern copy's id —
// sits in a's frame, and the assembly it places.
func (r *attachments) childFrame(a Assembly, seg, whole string) (placement, Assembly, string) {
	for _, c := range a.Children {
		slots, patternProblem := c.Pattern.copies()
		if patternProblem != nil && patternProblem.Severity == Error {
			continue
		}
		for _, slot := range slots {
			if c.ID+slot.suffix != seg {
				continue
			}
			sub, isAsm := r.asms[c.Ref]
			if !isAsm {
				return placement{}, Assembly{}, fmt.Sprintf("is attached at %q, but %q places %q, which is not an "+
					"assembly; interfaces are declared on assemblies only, so wrap the part in a one-child "+
					"assembly that declares the frame", whole, seg, c.Ref)
			}
			reflect, ok := reflectionAcross(c.Mirror)
			if !ok {
				return placement{}, Assembly{}, fmt.Sprintf("is attached at %q, but %q mirrors across %q, so it "+
					"has no frame", whole, seg, c.Mirror)
			}
			ref, problem := r.reference(a, c, whole)
			if problem != "" {
				return placement{}, Assembly{}, problem
			}
			local := placementOf(c.Position, c.Rotation, false)
			local.m = mulMat3(local.m, reflect)
			return ref.then(slot.at.then(local)), sub, ""
		}
		if c.ID == seg && len(slots) > 1 {
			return placement{}, Assembly{}, fmt.Sprintf("is attached at %q, but %q is a pattern of %d copies; "+
				"attach to one of them, %q to %q", whole, seg, len(slots), seg+slots[0].suffix,
				seg+slots[len(slots)-1].suffix)
		}
	}
	return placement{}, Assembly{}, fmt.Sprintf("is attached at %q, but assembly %q has no child %q", whole, a.ID, seg)
}

func declaredInterfaces(a Assembly) string {
	if len(a.Interfaces) == 0 {
		return " (it declares none)"
	}
	ids := make([]string, 0, len(a.Interfaces))
	for _, f := range a.Interfaces {
		ids = append(ids, f.ID)
	}
	return " (it declares " + strings.Join(ids, ", ") + ")"
}

// An `at` that leaves its assembly, refused with the fix.
//
// # The problem this solves
//
// Measured live 2026-09-15 (docs/spikes/2026-09-15-car-quality, run 2): the wheels
// step attached each wheel at "rear-suspension/left-hub/hub-face" from INSIDE its own
// "wheels" assembly. It was refused as `assembly "wheels" has no child
// "rear-suspension"`: true, and no use. The fault repair was sent that sentence, moved
// the path to "left-hub/hub-face" — still inside "wheels" — and back again, four
// times, and the step was lost with a correct wheel in it.
//
// # Why the root, and why FORGE does not move the child itself
//
// An assembly is written once and may be placed many times, so a path inside it can
// name only what it contains: its own interfaces and its own children's. Naming
// another subsystem from inside it would break define-once — which suspension would a
// wheels assembly placed twice be on? The one place both subsystems are placed is the
// assembly that places them, for a build the root, and a longer path from there
// ("suspension-left/hub") names exactly one frame. So the refusal says to attach from
// the root, with the path to write there, and what is still wrong with that path when
// it would fail too. FORGE does not rewrite it: which child belongs in which assembly
// is the design, and a moved wheel is one the author never placed.
// docs/bugfix/2026-09-15-an-attachment-into-another-assembly-was-refused-without-the-fix.md
// Fence: TestInterface_AnAttachmentThatLeavesItsAssemblyIsRefusedWithTheFix.

// leaves reports whether the `at` path on a child of a names something outside a: a
// first segment that is none of a's children or pattern copies, or a lone interface a
// does not declare and the root does. Never for a child of the root itself, whose
// paths are the ones this tells everyone else to write.
func leaves(a, root Assembly, at string) bool {
	if a.ID == root.ID {
		return false
	}
	first, _, nested := strings.Cut(at, PathSeparator)
	if strings.TrimSpace(first) == "" {
		return false
	}
	if !nested {
		return !declares(a, first) && declares(root, first)
	}
	for _, c := range a.Children {
		if c.ID == first {
			return false
		}
		slots, _ := c.Pattern.copies()
		for _, slot := range slots {
			if c.ID+slot.suffix == first {
				return false
			}
		}
	}
	return true
}

func declares(a Assembly, id string) bool {
	for _, f := range a.Interfaces {
		if f.ID == id {
			return true
		}
	}
	return false
}

// outsideProblem is the refusal for a path that leaves assembly a, with the path to
// write from the root instead. A path that already starts at the root's id loses it:
// from the root, "rear-suspension/left-hub/hub-face" is "left-hub/hub-face".
//
// ‼️ The fix comes FIRST and the reason after it: a build step's note clips each fault
// at 200 characters, and the repair and the person need the instruction, not only why.
func (r *attachments) outsideProblem(a, root Assembly, at string) string {
	fromRoot := at
	if first, rest, ok := strings.Cut(at, PathSeparator); ok && first == root.ID && rest != "" {
		fromRoot = rest
	}
	out := fmt.Sprintf("is attached at %q, outside its assembly %q; attach it from the root %q instead, as a "+
		"child there with \"at\": %q. An assembly is written once and may be placed anywhere, so a child in it "+
		"attaches only at the assembly's own interfaces or its own children's", at, a.ID, root.ID, fromRoot)
	if _, problem := r.interfaceIn(root, fromRoot, fromRoot); problem != "" {
		if _, why, ok := strings.Cut(problem, ", but "); ok {
			problem = why
		}
		out += "; from the root that path fails too: " + problem
	}
	return out
}

// RootInterface is one mounting frame a child of the root can be attached at: the
// `at` path to write on that child, and where the frame sits in the root's frame.
type RootInterface struct {
	At       string    `json:"at"`
	Position []float64 `json:"position"`
	Rotation []float64 `json:"rotation,omitempty"`
	// Mirrored says the frame is reflected: something above it mirrors its placement.
	Mirrored bool `json:"mirrored,omitempty"`
}

// rootInterfaceDepth is how many placements deep a listed path may reach from the
// root: "suspension-left/hub" is 1, "suspension-left/knuckle/hub" 2.
const rootInterfaceDepth = 3

// rootInterfaceBudget bounds how many candidate frames one listing looks at, so a
// pattern of a thousand copies costs a bounded walk rather than a thousand of them.
const rootInterfaceBudget = 4096

// InterfacesFromRoot lists the interfaces a child of the root can attach at, on every
// assembly the root already places except those placing except, in document order.
// Each path is resolved by the same code a placement is, so a listed path is one
// that attaches: one refused (a mirror across an unknown axis, a cycle) is not
// listed. At most limit are returned; more counts those left out.
//
// Phase 2, stage A2 (2026-09-15): a step that mounts its subsystem on another is
// shown these instead of the other subsystem's contents.
// Fence: TestInterfacesFromRoot_ListsWhereAChildOfTheRootCanAttach.
func (d Document) InterfacesFromRoot(except string, limit int) (list []RootInterface, more int) {
	asms := map[string]Assembly{}
	for _, a := range d.Assemblies {
		if _, dup := asms[a.ID]; !dup {
			asms[a.ID] = a
		}
	}
	root, ok := asms[d.Root]
	if !ok {
		return nil, 0
	}
	attach := newAttachments(asms)
	budget := rootInterfaceBudget
	var visit func(a Assembly, prefix string, depth int, onPath map[string]bool)
	visit = func(a Assembly, prefix string, depth int, onPath map[string]bool) {
		for _, f := range a.Interfaces {
			if budget == 0 {
				return
			}
			budget--
			if len(list) >= limit {
				more++
				continue
			}
			path := prefix + PathSeparator + f.ID
			frame, problem := attach.interfaceIn(root, path, path)
			if problem != "" {
				continue
			}
			pos, rot, mirrored := frame.stored()
			list = append(list, RootInterface{At: path, Position: tidyCoordinates(pos), Rotation: tidyRotation(rot), Mirrored: mirrored})
		}
		if depth >= rootInterfaceDepth {
			return
		}
		for _, c := range a.Children {
			sub, isAsm := asms[c.Ref]
			if !isAsm || onPath[sub.ID] {
				continue
			}
			slots, problem := c.Pattern.copies()
			if problem != nil && problem.Severity == Error {
				continue
			}
			onPath[sub.ID] = true
			for _, slot := range slots {
				visit(sub, prefix+PathSeparator+c.ID+slot.suffix, depth+1, onPath)
			}
			delete(onPath, sub.ID)
		}
	}
	for _, c := range root.Children {
		sub, isAsm := asms[c.Ref]
		if !isAsm || c.Ref == except || sub.ID == root.ID {
			continue
		}
		slots, problem := c.Pattern.copies()
		if problem != nil && problem.Severity == Error {
			continue
		}
		for _, slot := range slots {
			visit(sub, c.ID+slot.suffix, 1, map[string]bool{root.ID: true, sub.ID: true})
		}
	}
	return list, more
}

// tidyCoordinates rounds away the float noise composing frames leaves (1e-13 for a
// zero), so a listing reads as the numbers the author wrote.
func tidyCoordinates(v []float64) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = math.Round(x*1e6) / 1e6
		if out[i] == 0 {
			out[i] = 0 // not -0
		}
	}
	return out
}

func tidyRotation(v []float64) []float64 {
	out := tidyCoordinates(v)
	for _, x := range out {
		if x != 0 {
			return out
		}
	}
	return nil
}
