package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Where a build step says the root places what it built.
//
// # The problem this solves
//
// Measured live 2026-09-15 (docs/spikes/2026-09-15-car-quality, run 2): the wheels
// step built upright wheels with a ring of ISO lug nuts, exactly as the contract
// teaches, and attached each one at "rear-suspension/left-hub/hub-face" from INSIDE
// its own "wheels" assembly. An assembly is written once and may be placed many
// times, so a path inside it can name only what it contains, and the step was refused
// and lost. Mounting one subsystem on another is only expressible where both are
// placed: the root. But a step places something from the root by patching the root,
// which replaces it whole, and run 1 measured what that asks of a model: of five steps
// that built a new assembly, none restated the root's children to place it. #97 then
// had FORGE place such an assembly "from the root at its origin", which attaches it to
// nothing.
//
// # What a step can say instead
//
// "placements" beside "patch": each entry is one child of the root, placing the
// assembly this step builds ("ref" defaults to it), written exactly as a child of the
// root is — "at" a path from the root, "position", "rotation", "mirror", "pattern".
// FORGE appends them to the root, so the step never restates what the root already
// places, and an attachment across subsystems is written in the one place it is
// define-once-correct.
//
// # What it does not do
//
// It never places an assembly something already places (the step's own placement
// wins, and the declaration is reported unused), never places the root, and never
// guesses: a path that does not attach is placed as written and refused by name by
// the fault gate, which is where the repair can see it.
// docs/bugfix/2026-09-15-an-attachment-into-another-assembly-was-refused-without-the-fix.md
// Fence: TestAssemble_AStepsDeclaredPlacementAttachesItsAssemblyFromTheRoot.

// stepPlacements reads the placements a step declared, and a note when it declared
// some that could not be read.
//
// Read from "prototype_edit.placements" (what the prompt teaches), else the top of
// the reply, else "prototype_edit.patch.placements": all three are one unambiguous
// reading of one list, and a model that nests a field one level off otherwise loses
// the attachment it wrote. An expression in a position is worked out over the
// reply's parameters and the model's, as a child's is (dimensionrepair.go).
func stepPlacements(resp *llm.Response, base *Prototype) ([]geometry.Child, string) {
	if resp == nil {
		return nil, ""
	}
	var raw map[string]any
	if json.Unmarshal([]byte(extractJSON(resp.Content)), &raw) != nil {
		return nil, ""
	}
	edit, _ := raw["prototype_edit"].(map[string]any)
	patch, _ := edit["patch"].(map[string]any)
	var list []any
	for _, candidate := range []any{edit["placements"], raw["placements"], patch["placements"]} {
		if l, ok := candidate.([]any); ok && len(l) > 0 {
			list = l
			break
		}
	}
	if len(list) == 0 {
		return nil, ""
	}
	scope := map[string]any{"assemblies": []any{map[string]any{"children": list}}}
	for _, key := range []string{"parameters", "derived", "units"} {
		if v, ok := patch[key]; ok {
			scope[key] = v
		}
	}
	withBaseParameters(scope, base, func() bool { return repairPlacements(scope) })
	body, err := json.Marshal(list)
	if err != nil {
		return nil, ""
	}
	var out []geometry.Child
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Sprintf("The \"placements\" this step declared could not be read (%s), so they were not used.",
			clipRunes(err.Error(), 160))
	}
	return out, ""
}

// placeDeclared adds the step's declared placements to the root, for each assembly
// nothing places yet, and returns the sentence saying where, or "".
//
// Before placeStepAssembly, so an assembly placed here is one that call leaves alone.
func placeDeclared(d *Prototype, focus string, declared []geometry.Child) string {
	focus = strings.TrimSpace(focus)
	if d == nil || d.Root == "" || len(declared) == 0 {
		return ""
	}
	rootIndex := -1
	isAsm, placed := map[string]bool{}, map[string]bool{}
	for i, a := range d.Assemblies {
		isAsm[a.ID] = true
		if a.ID == d.Root && rootIndex < 0 {
			rootIndex = i
		}
		for _, c := range a.Children {
			placed[c.Ref] = true
		}
	}
	if rootIndex < 0 {
		return ""
	}
	// Grouped by what they place, in the order the step declared them.
	var refs []string
	byRef := map[string][]geometry.Child{}
	for _, p := range declared {
		ref := strings.TrimSpace(p.Ref)
		if ref == "" {
			ref = focus
		}
		p.Ref = ref
		if _, seen := byRef[ref]; !seen {
			refs = append(refs, ref)
		}
		byRef[ref] = append(byRef[ref], p)
	}
	children := append([]geometry.Child(nil), d.Assemblies[rootIndex].Children...)
	var notes []string
	for _, ref := range refs {
		switch {
		case ref == "":
			notes = append(notes, "This step declared placements with no \"ref\", and its plan names no assembly, so they were not used.")
		case ref == d.Root:
			notes = append(notes, fmt.Sprintf("This step declared placements of the root %q, which cannot place itself, so they were not used.", ref))
		case !isAsm[ref]:
			notes = append(notes, fmt.Sprintf("This step declared placements of %q, which is not an assembly in the model, so they were not used.", ref))
		case placed[ref]:
			notes = append(notes, fmt.Sprintf("This step placed the assembly %q itself, so the placements it declared for it were not used.", ref))
		default:
			var where []string
			for _, p := range byRef[ref] {
				id := strings.TrimSpace(p.ID)
				if id == "" || strings.Contains(id, geometry.PathSeparator) {
					id = ref
				}
				for childTaken(children, id) {
					id += "-placed"
				}
				p.ID = id
				children = append(children, p)
				where = append(where, describePlacement(p))
			}
			placed[ref] = true
			notes = append(notes, fmt.Sprintf("FORGE placed the assembly %q from the root %q where this step declared it: %s.",
				ref, d.Root, strings.Join(where, "; ")))
		}
	}
	if len(children) != len(d.Assemblies[rootIndex].Children) {
		// New slices: the document's assemblies may still share their children with the
		// model before this step, which must not change.
		assemblies := append([]geometry.Assembly(nil), d.Assemblies...)
		assemblies[rootIndex].Children = children
		d.Assemblies = assemblies
	}
	return strings.Join(notes, " ")
}

// describePlacement says where one placement from the root puts its assembly.
func describePlacement(c geometry.Child) string {
	out := fmt.Sprintf("%q", c.ID)
	switch {
	case c.At != "":
		out += fmt.Sprintf(" at %q", c.At)
	case len(c.Position) > 0:
		out += fmt.Sprintf(" at %v in the root's frame", c.Position)
	default:
		out += " at the root's origin"
	}
	if c.Mirror != "" {
		out += fmt.Sprintf(", mirrored across %s", c.Mirror)
	}
	return out
}
