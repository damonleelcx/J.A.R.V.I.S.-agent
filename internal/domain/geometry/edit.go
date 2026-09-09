package geometry

import (
	"fmt"
	"strings"
)

// Editing a model that already exists, rather than restating it.
//
// # The problem
//
// A revision was a full rewrite. To add one part the agent re-emitted every
// other part, and — before the model on screen was handed to it — every number
// came from recall. Two failures followed from that one shape:
//
//   - Drift. Parts nobody asked about were retyped, so asking for a spoiler
//     could change the wheelbase, and nothing compared the two.
//   - Ambiguity. Removal was expressed by OMISSION, so a part left out by
//     accident and a part removed on purpose produced the same document. There
//     was no way to tell "remove the cabin" from "I forgot the cabin".
//
// # Why an edit rather than a better-behaved rewrite
//
// Showing the agent the document makes drift less likely. An edit makes it
// impossible: a part the edit does not mention is not in the payload at all, so
// there is nothing to retype and nothing to get wrong. And removal becomes a
// statement — it appears in the record as an intent rather than as an absence.
//
// # Why the result is a whole document
//
// Everything downstream — the viewport, the store, compare, export, the STEP
// kernel — already consumes a Document, and none of them should learn a second
// shape. The edit exists on the way IN only: it is applied here and what leaves
// is an ordinary document. That is what keeps the blast radius of this feature
// to the two functions that resolve it.
type Edit struct {
	Remove Removals `json:"remove,omitempty"`
	// Patch is a Document fragment merged into the base BY ID. An entry whose id
	// already exists replaces that entry wholesale; a new id is appended.
	//
	// Wholesale and not field-by-field: a field-level merge has no way to say
	// "clear this", so a part could never lose its profile or its holes once it
	// had them. Replacing whole entries keeps one rule that is true everywhere.
	Patch *Document `json:"patch,omitempty"`
}

// Removals names what should cease to exist. Ids, never indexes — an index picks
// a different thing the moment anything else changes, which is the same reason
// a fillet selects edges by rule.
type Removals struct {
	Parts    []string `json:"parts,omitempty"`
	Features []string `json:"features,omitempty"`
}

// Empty reports whether this edit would change nothing.
//
// Worth its own answer: an agent that means "I changed nothing" should say so in
// words, and an edit that silently produces an identical document would append a
// version recording a change nobody made.
func (e Edit) Empty() bool {
	return len(e.Remove.Parts) == 0 && len(e.Remove.Features) == 0 &&
		(e.Patch == nil || (len(e.Patch.Parts) == 0 && len(e.Patch.Features) == 0 &&
			len(e.Patch.Parameters) == 0 && len(e.Patch.Derived) == 0 &&
			len(e.Patch.Assumptions) == 0 && len(e.Patch.NotVerified) == 0 &&
			len(e.Patch.Overlays) == 0 && len(e.Patch.States) == 0 &&
			strings.TrimSpace(e.Patch.Name) == "" && strings.TrimSpace(e.Patch.Units) == ""))
}

// Apply returns base with this edit made, and what could not be done.
//
// Never mutates base: the stored variant must stay exactly what it was, or a
// failed edit would corrupt the thing it failed to change.
//
// A removal naming something absent is a PROBLEM and not silence. "Remove the
// cabin" when there is no cabin means the agent and the person disagree about
// what is on screen, and continuing quietly hides that disagreement behind a
// version that looks successful.
func (e Edit) Apply(base Document) (Document, []Problem) {
	var problems []Problem
	fail := func(format string, args ...any) {
		problems = append(problems, Problem{Severity: Error, Name: "edit",
			Detail: fmt.Sprintf(format, args...)})
	}

	out := base
	out.Parts = append([]Part(nil), base.Parts...)
	out.Features = append([]Feature(nil), base.Features...)
	out.Parameters = append([]Parameter(nil), base.Parameters...)
	out.Derived = append([]Derived(nil), base.Derived...)
	out.Assumptions = append([]string(nil), base.Assumptions...)
	out.NotVerified = append([]string(nil), base.NotVerified...)
	out.Overlays = append([]Overlay(nil), base.Overlays...)
	out.States = append([]AssemblyState(nil), base.States...)

	for _, id := range e.Remove.Parts {
		kept := out.Parts[:0]
		found := false
		for _, p := range out.Parts {
			if p.ID == id {
				found = true
				continue
			}
			kept = append(kept, p)
		}
		out.Parts = kept
		if !found {
			fail("cannot remove part %q, which is not in this assembly", id)
		}
	}
	for _, id := range e.Remove.Features {
		kept := out.Features[:0]
		found := false
		for _, f := range out.Features {
			if f.ID == id {
				found = true
				continue
			}
			kept = append(kept, f)
		}
		out.Features = kept
		if !found {
			fail("cannot remove feature %q, which is not in this assembly", id)
		}
	}

	if e.Patch != nil {
		p := e.Patch
		if strings.TrimSpace(p.Name) != "" {
			out.Name = p.Name
		}
		if strings.TrimSpace(p.Units) != "" {
			out.Units = p.Units
		}
		for _, in := range p.Parts {
			out.Parts = upsertPart(out.Parts, in)
		}
		for _, in := range p.Features {
			out.Features = upsertFeature(out.Features, in)
		}
		for _, in := range p.Parameters {
			out.Parameters = upsertParameter(out.Parameters, in)
		}
		for _, in := range p.Derived {
			out.Derived = upsertDerived(out.Derived, in)
		}
		for _, in := range p.Overlays {
			out.Overlays = upsertOverlay(out.Overlays, in)
		}
		for _, in := range p.States {
			out.States = upsertState(out.States, in)
		}
		// Prose is APPENDED, not replaced: an assumption made two turns ago is
		// still an assumption this design rests on, and a revision that restated
		// only its own would quietly drop the earlier ones. Exact duplicates are
		// skipped so a repeated note does not accumulate.
		out.Assumptions = appendNew(out.Assumptions, p.Assumptions)
		out.NotVerified = appendNew(out.NotVerified, p.NotVerified)
	}
	return out, problems
}

func upsertPart(list []Part, in Part) []Part {
	for i := range list {
		if list[i].ID == in.ID {
			list[i] = in
			return list
		}
	}
	return append(list, in)
}

func upsertFeature(list []Feature, in Feature) []Feature {
	for i := range list {
		if list[i].ID == in.ID {
			list[i] = in
			return list
		}
	}
	return append(list, in)
}

func upsertParameter(list []Parameter, in Parameter) []Parameter {
	for i := range list {
		if list[i].Name == in.Name {
			list[i] = in
			return list
		}
	}
	return append(list, in)
}

func upsertDerived(list []Derived, in Derived) []Derived {
	for i := range list {
		if list[i].Name == in.Name {
			list[i] = in
			return list
		}
	}
	return append(list, in)
}

func upsertOverlay(list []Overlay, in Overlay) []Overlay {
	for i := range list {
		if list[i].ID == in.ID {
			list[i] = in
			return list
		}
	}
	return append(list, in)
}

func upsertState(list []AssemblyState, in AssemblyState) []AssemblyState {
	for i := range list {
		if list[i].ID == in.ID {
			list[i] = in
			return list
		}
	}
	return append(list, in)
}

func appendNew(have, add []string) []string {
	for _, s := range add {
		dup := false
		for _, existing := range have {
			if existing == s {
				dup = true
				break
			}
		}
		if !dup {
			have = append(have, s)
		}
	}
	return have
}
