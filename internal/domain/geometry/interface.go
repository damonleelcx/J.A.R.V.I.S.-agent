package geometry

import (
	"fmt"
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
