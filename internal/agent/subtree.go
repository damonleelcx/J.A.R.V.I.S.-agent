package agent

import (
	"encoding/json"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// What one build step on a tree is shown: its own assembly, not the whole model.
//
// # The problem this solves (Phase 2, stage A2)
//
// Every pass of a build was handed json.Marshal of the whole document. For a car
// of a few dozen parts that is a few kilobytes; for a design of hundreds of
// sub-assemblies it is a prompt that grows with every pass, and a step that adds
// a door is paying to read the gearbox. A model that is built out of assemblies
// already says which part of it a step is about, so a step on a tree is shown
// that assembly and what it rests on, and a prompt stays the same size however
// many other subsystems the model has.
//
// # What the step still sees
//
//   - the assembly it builds (Focus), and every assembly and definition beneath it;
//   - how the root places it (Placements), so it knows where it sits;
//   - the interfaces those placements attach AT, whether on the root or on a
//     sibling — only the frames, never what the sibling contains;
//   - the document's parameters and derived values, which a dimension may name;
//   - every interface on what the root already places, by the path a child of the
//     root attaches at ("suspension-left/knuckle/hub"), and where it sits: a step
//     mounts its subsystem on another from the root, never from inside its own
//     assembly (2026-09-15, attach and bind; stepdeclared.go);
//   - how many other placements there are, so it knows it is not the whole model.
//
// An edit merges by id, so what the step does not see it does not lose.

// subtreeModel is the JSON a step on a tree is given.
type subtreeModel struct {
	Name       string               `json:"name"`
	Units      string               `json:"units"`
	Parameters []geometry.Parameter `json:"parameters,omitempty"`
	Derived    []geometry.Derived   `json:"derived,omitempty"`
	Root       string               `json:"root"`
	// Focus is the assembly this step builds. New says it does not exist yet: the
	// step creates it and places it from the root.
	Focus       string              `json:"focus"`
	New         bool                `json:"new,omitempty"`
	Placements  []geometry.Child    `json:"placements,omitempty"`
	Definitions []geometry.Part     `json:"definitions,omitempty"`
	Assemblies  []geometry.Assembly `json:"assemblies,omitempty"`
	AttachesTo  []attachment        `json:"attaches_to,omitempty"`
	// RootChildren is what the root already places, shown to a step that creates
	// its assembly: it places it by patching the root, which replaces the root whole.
	RootChildren []geometry.Child `json:"root_children,omitempty"`
	// OtherPlacements is how many placements the root holds besides the focus's.
	OtherPlacements int `json:"other_placements"`
	// RootInterfaces is where a child of the root can attach, on everything the root
	// places except the focus: the paths a step writes in "placements". Bounded;
	// RootInterfacesMore counts what was left out.
	// Fence: TestAssemble_AStepIsShownTheInterfacesItCanAttachAtFromTheRoot.
	RootInterfaces     []geometry.RootInterface `json:"root_interfaces,omitempty"`
	RootInterfacesMore int                      `json:"root_interfaces_more,omitempty"`
}

// maxRootInterfacesShown bounds root_interfaces, so the view stays the size of the
// subsystem it shows however many mounting points the rest of the model has.
const maxRootInterfacesShown = 48

// attachment is the interfaces one assembly outside the subtree offers.
type attachment struct {
	Assembly   string               `json:"assembly"`
	Interfaces []geometry.Interface `json:"interfaces"`
}

// SubtreeModel renders what a build step on a tree is shown, or "" when the
// document is not a tree or no assembly is named — then the step is shown the
// whole document, as every step was before.
func SubtreeModel(d *Prototype, focus string) string {
	focus = strings.TrimSpace(focus)
	if d == nil || d.Root == "" || focus == "" {
		return ""
	}
	assemblies := map[string]geometry.Assembly{}
	for _, a := range d.Assemblies {
		assemblies[a.ID] = a
	}
	definitions := map[string]geometry.Part{}
	for _, p := range d.Definitions {
		definitions[p.ID] = p
	}
	root, ok := assemblies[d.Root]
	if !ok {
		return ""
	}

	view := subtreeModel{Name: d.Name, Units: d.Units, Parameters: d.Parameters, Derived: d.Derived,
		Root: d.Root, Focus: focus}

	if _, exists := assemblies[focus]; !exists {
		// A step that creates the assembly: shown only where it can attach.
		view.New = true
		if len(root.Interfaces) > 0 {
			view.AttachesTo = []attachment{{Assembly: root.ID, Interfaces: root.Interfaces}}
		}
		// ‼️ And what the root already places. The step places its new assembly by
		// patching the root, a patched assembly is replaced whole, and a step shown only
		// a count of the root's children can send back only its own (2026-09-15, live car
		// findings). The placements themselves, never what they contain.
		// Fence: TestSubtreeModel_ANewAssemblyIsShownWhatTheRootAlreadyPlaces.
		view.RootChildren = root.Children
		view.OtherPlacements = len(root.Children)
		view.RootInterfaces, view.RootInterfacesMore = d.InterfacesFromRoot(focus, maxRootInterfacesShown)
		return marshalView(view)
	}

	// Everything beneath the focus, in the document's own order.
	wantAsm, wantDef := map[string]bool{}, map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		if wantAsm[id] {
			return
		}
		wantAsm[id] = true
		for _, c := range assemblies[id].Children {
			if _, isAsm := assemblies[c.Ref]; isAsm {
				walk(c.Ref)
			} else if _, isDef := definitions[c.Ref]; isDef {
				wantDef[c.Ref] = true
			}
		}
	}
	walk(focus)
	for _, a := range d.Assemblies {
		if wantAsm[a.ID] {
			view.Assemblies = append(view.Assemblies, a)
		}
	}
	for _, p := range d.Definitions {
		if wantDef[p.ID] {
			view.Definitions = append(view.Definitions, p)
		}
	}

	// How the root places it, and what those placements attach at.
	siblings := map[string]string{}
	for _, c := range root.Children {
		siblings[c.ID] = c.Ref
	}
	attached := map[string]bool{}
	attach := func(a geometry.Assembly) {
		if attached[a.ID] || len(a.Interfaces) == 0 {
			return
		}
		attached[a.ID] = true
		view.AttachesTo = append(view.AttachesTo, attachment{Assembly: a.ID, Interfaces: a.Interfaces})
	}
	for _, c := range root.Children {
		if c.Ref != focus {
			view.OtherPlacements++
			continue
		}
		view.Placements = append(view.Placements, c)
		if c.At == "" {
			continue
		}
		// "mount" is the root's own interface; "frame/axle-left" is on the sibling
		// placed as "frame" (a pattern copy "bolt-3" by its base id).
		if sibling, _, onSibling := strings.Cut(c.At, geometry.PathSeparator); onSibling {
			ref, ok := siblings[sibling]
			if !ok {
				if i := strings.LastIndex(sibling, "-"); i > 0 && allDigits(sibling[i+1:]) {
					ref = siblings[sibling[:i]]
				}
			}
			if a, ok := assemblies[ref]; ok {
				attach(a)
			}
		} else {
			attach(root)
		}
	}
	view.RootInterfaces, view.RootInterfacesMore = d.InterfacesFromRoot(focus, maxRootInterfacesShown)
	return marshalView(view)
}

func marshalView(v subtreeModel) string {
	body, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(body)
}
