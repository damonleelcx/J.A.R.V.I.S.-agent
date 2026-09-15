package geometry

import (
	"fmt"
	"strings"
)

// Editing a tree by where a part is placed, and saying what the edit reached.
//
// Phase 7, stage E1 of docs/plan-2026-09-13-millions-of-parts.md: "prototype_edit
// by path; editing a definition reports every occurrence it changes."
//
// # What a path means in an edit
//
// A PATH resolves to the definition or assembly it places, and the edit changes
// that design — every occurrence of it (decided 2026-09-15). The person points at
// "front-left/hub" because that is the hub they can see, and the viewport, faults
// and interference all name parts that way; the agent should not have to walk the
// tree back to the id "hub" before it can say what to change. So wherever an edit
// names a definition or an assembly by id, it may name a placement of one instead.
//
// # Why a path never forks one copy
//
// D1f's decision stands (edit.go, Removals): an edit patches the DESIGN by id.
// Forking "front-left/hub" away from the definition the other three hubs follow
// would make a document in which one hub silently stops following changes to the
// hub, and nothing on screen would show that it had. A path is only a way of
// NAMING a design, so the rule it could break is never in question.
//
// # Why the edit says what it reached
//
// Because a path reads like one part and changes many. "Make front-left/hub 25 mm"
// changes all four hubs, and a person who meant one of them must be told, in the
// turn, before they build on it. The same is true of an id: a definition patched
// by id is placed somewhere the agent may not have looked. So every entry an edit
// patches or removes reports the placed part ids — as Expanded names them — that
// show the change, in the document before the edit and after it.
//
// # What it does not report, on purpose
//
// Parameters, derived values and features are not traced to parts. A parameter
// can reach any number, and a feature changes the solid of parts whose own entry
// is untouched; a count that included them would be a guess dressed as a figure.

// Reached is one thing an edit patched or removed, and every placed part that
// shows the change.
type Reached struct {
	// Kind is "part", "definition", "assembly" or "child".
	Kind string `json:"kind"`
	// ID is what was changed: a part, definition or assembly id, or a child as
	// "assembly-id/child-id". Always the design's own id, even when it was named by
	// a path.
	ID string `json:"id"`
	// Path is the placed path the edit named it by; "" when it was named by id.
	Path    string `json:"path,omitempty"`
	Removed bool   `json:"removed,omitempty"`
	// Occurrences are the ids, as Expanded names them, of every placed part that
	// comes from it: those in the document before the edit, then any only the
	// edited document places. A patch that adds a copy reports the new copy; a
	// removal reports what is gone.
	Occurrences []string `json:"occurrences"`
}

// placements is a document's tree written out once, with the record of which
// placement wrote which part.
type placements struct {
	parts []Part
	spans []treeSpan
	// byPath is every placed path, to the span that names it. A path two
	// placements write out (a patterned "bolt" beside a child "bolt-2") is in
	// ambiguous instead, because which design it names would be a guess.
	byPath    map[string]treeSpan
	ambiguous map[string]bool
}

func placementsOf(d Document) placements {
	var spans []treeSpan
	e, _ := expandAssembliesTraced(d, &spans)
	p := placements{parts: e.Parts, spans: spans, byPath: map[string]treeSpan{}, ambiguous: map[string]bool{}}
	for _, s := range spans {
		if s.path == "" {
			continue
		}
		if have, ok := p.byPath[s.path]; ok && (have.ref != s.ref || have.assembly != s.assembly) {
			p.ambiguous[s.path] = true
			continue
		}
		p.byPath[s.path] = s
	}
	return p
}

// occurrences returns the ids of the parts written by every span match accepts,
// in the order the expansion wrote them. Aliases are skipped: they rename parts
// another span already counts.
func (p placements) occurrences(match func(treeSpan) bool) []string {
	var ids []string
	end := -1
	for _, s := range p.spans {
		if s.alias || !match(s) {
			continue
		}
		// Spans are recorded as each placement finishes, and two occurrences of one
		// design never nest (that would be a cycle), so they arrive in order. The
		// guard keeps a part from being counted twice if that ever stops being so.
		for i := max(s.start, end); i < s.end; i++ {
			ids = append(ids, p.parts[i].ID)
		}
		end = max(end, s.end)
	}
	return ids
}

// resolveDesign reads name, written where an edit names a definition (assembly
// false) or an assembly, as the id of the design it means. ids are the base's own
// ids of that kind. path is name when it was read as a placed path, and "" when
// it was read as an id. ok is false when name was refused through fail.
//
// An id the design already has is an id, as it always was. Anything else that is
// a placed path names the design that path places. Anything else is still an id —
// a new one, for a patch — unless it contains PathSeparator, which no id placed
// by a child can, and is refused as a path that places nothing.
func (p placements) resolveDesign(name string, assembly bool, ids map[string]bool, verb string,
	fail func(format string, args ...any)) (id, path string, ok bool) {
	kind, other, otherList := "definition", "assembly", "assemblies"
	if assembly {
		kind, other, otherList = "assembly", "definition", "definitions"
	}
	s, placed := p.byPath[name]
	switch {
	case p.ambiguous[name]:
		fail("cannot %s %s %q: more than one placement is written out as %q, and they place different "+
			"designs, so which one it names is a guess; name the %s by its id", verb, kind, name, name, kind)
		return "", "", false
	case ids[name]:
		if placed && (s.ref != name || s.assembly != assembly) {
			fail("cannot %s %s %q: it is the id of a %s and also the path of a placement of %q, a different "+
				"design; name the one you mean by a longer path", verb, kind, name, kind, s.ref)
			return "", "", false
		}
		return name, "", true
	case !placed:
		if strings.Contains(name, PathSeparator) {
			fail("cannot %s %s %q: nothing is placed at that path in this design, and a %s id "+
				"cannot contain %q", verb, kind, name, kind, PathSeparator)
			return "", "", false
		}
		return name, "", true
	case s.assembly != assembly:
		fail("cannot %s %s %q: that path places the %s %q, which an edit names under %q", verb, kind, name,
			other, s.ref, otherList)
		return "", "", false
	}
	return s.ref, name, true
}

// fillReached works out every entry's occurrences from the document before the
// edit and after it, and folds entries that name the same thing into one.
func fillReached(reached []Reached, base, out Document, before, after placements) []Reached {
	merged := make([]Reached, 0, len(reached))
	at := map[string]int{}
	for _, r := range reached {
		var was, is []string
		switch r.Kind {
		case "part":
			was, is = topLevelOccurrences(base, r.ID), topLevelOccurrences(out, r.ID)
		case "child":
			child := func(s treeSpan) bool { return s.child == r.ID }
			was, is = before.occurrences(child), after.occurrences(child)
		default:
			design := func(s treeSpan) bool { return s.ref == r.ID && s.assembly == (r.Kind == "assembly") }
			was, is = before.occurrences(design), after.occurrences(design)
		}
		r.Occurrences = unionIDs(was, is)
		key := fmt.Sprintf("%s\x00%s\x00%t", r.Kind, r.ID, r.Removed)
		if i, ok := at[key]; ok {
			merged[i].Occurrences = unionIDs(merged[i].Occurrences, r.Occurrences)
			if merged[i].Path == "" {
				merged[i].Path = r.Path
			}
			continue
		}
		at[key] = len(merged)
		merged = append(merged, r)
	}
	return merged
}

// topLevelOccurrences is the ids a top-level part is written out as: itself, or
// its repeat copies. Read off the expansions rather than restating their naming.
func topLevelOccurrences(d Document, id string) []string {
	for _, p := range d.Parts {
		if p.ID != id {
			continue
		}
		e, _ := expandGears(Document{Parts: []Part{p}})
		e, _ = expandRepeats(e)
		ids := make([]string, 0, len(e.Parts))
		for _, q := range e.Parts {
			ids = append(ids, q.ID)
		}
		return ids
	}
	return nil
}

func unionIDs(a, b []string) []string {
	out := append([]string(nil), a...)
	seen := make(map[string]bool, len(a))
	for _, id := range a {
		seen[id] = true
	}
	for _, id := range b {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
