package geometry

import (
	"fmt"
	"strings"
)

// Features declared on an assembly.
//
// Phase 1, stage D1e of docs/plan-2026-09-13-millions-of-parts.md. Decided
// 2026-09-14:
//
//   - A cut, fuse, fillet, chamfer or loft between parts an assembly places is
//     declared ON THAT ASSEMBLY, and its `of` and `with` are PATHS from it, in the
//     syntax `at` uses: a child ("hub"), a pattern copy ("spoke-3"), a whole
//     pattern ("spoke": every copy), a definition's repeat copy ("rivet-2"), a
//     sub-assembly (every part it places) or a deeper path ("rim/weld-ring").
//   - It is written once and applied in EVERY occurrence of the assembly: a wheel
//     defined with its weld is welded in all four corners, with nothing to copy
//     and nothing to drift.
//   - Each occurrence builds its own exact result. Building the welded wheel ONCE
//     and placing it four times is stage K1's definition cache; this stage is about
//     what a feature means, K1 about what it costs.
//
// How: the tree walk writes every occurrence out as ordinary parts, depth first,
// so everything one placement writes out is contiguous in the part list. The walk
// records that range by path, and this file turns each of the assembly's features
// into an ordinary document feature naming the flattened ids, with its id scoped
// to the occurrence ("fl/weld"). Everything downstream — Operations' checks, the
// kernel, the mesh — then reads a feature it already understands.
//
// A group follows the rule a repeated part's id already has (retargetFeatures):
// `of` takes its FIRST part, because an operation applies to one solid; `with`
// takes EVERY part, because a tool list is a list.

// partRange is the parts one placement wrote out: parts[start:end].
type partRange struct{ start, end int }

// occurrenceFeatures writes an assembly's features out for one occurrence of it,
// at path, whose placements landed at index in parts. A feature naming a path
// that places nothing is refused through fail and left out; the rest are kept.
func occurrenceFeatures(a Assembly, path []string, index map[string]partRange, parts []Part,
	fail func(name, format string, args ...any)) []Feature {
	if len(a.Features) == 0 {
		return nil
	}
	prefix := strings.Join(path, PathSeparator)
	placedIDs := func(p string) []string {
		r, ok := index[p]
		if !ok || r.end <= r.start {
			return nil
		}
		ids := make([]string, 0, r.end-r.start)
		for _, q := range parts[r.start:r.end] {
			ids = append(ids, q.ID)
		}
		return ids
	}

	var out []Feature
	for i, f := range a.Features {
		id := strings.TrimSpace(f.ID)
		if id == "" {
			id = fmt.Sprintf("feature-%d", i+1)
		}
		name := id
		if prefix != "" {
			name = prefix + PathSeparator + id
		}
		of := placedIDs(f.Of)
		if len(of) == 0 {
			fail(name, "applies to %q, which names nothing assembly %q places; a feature on an assembly "+
				"names its parts by path from it (\"hub\", \"spoke-3\", \"rim/weld-ring\")", f.Of, a.ID)
			continue
		}
		q := f
		q.ID = name
		q.Of = of[0]
		q.With = nil
		refused := false
		for _, w := range f.With {
			tools := placedIDs(w)
			if len(tools) == 0 {
				fail(name, "uses %q as a tool, which names nothing assembly %q places", w, a.ID)
				refused = true
				continue
			}
			q.With = append(q.With, tools...)
		}
		if refused {
			continue
		}
		out = append(out, q)
	}
	return out
}
