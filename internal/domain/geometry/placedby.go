package geometry

import (
	"strconv"
	"strings"
)

// Which child places a flattened part.
//
// # The problem this solves
//
// A tree is flattened into parts named by path ("left-wheel/lug-nut-3"), and every
// finding about a built part — interference above all — names that path. But a
// path is where a part IS, not something anybody can edit: the contract says so
// ("change the DESIGN, not one placement"). The 2026-09-15 live car buried each lug
// nut of a pattern inside its hub, rim and tyre, and the repair it was sent named
// only the paths, so the one thing it could have moved — the child and its pattern
// — was never mentioned.
//
// This answers "which child of which assembly placed this, and was it one copy of a
// pattern", read from the document rather than guessed from the id's spelling.

// ChildPlacement is the child that places one flattened part.
type ChildPlacement struct {
	// Assembly is the assembly whose child places the part.
	Assembly string
	// Child is that child's id, without a pattern copy's suffix.
	Child string
	// Ref is what the child places.
	Ref string
	// Copy is the pattern copy the part belongs to, from 1, or 0 when the child has
	// no pattern.
	Copy int
	// Pattern is the child's pattern, nil when it has none.
	Pattern *Pattern
}

// PlacedBy returns the child that places the part with this flattened id: for a
// part inside a sub-assembly, the innermost child on its path. False for a
// top-level part or an id the tree does not place.
func (d Document) PlacedBy(id string) (ChildPlacement, bool) {
	if d.Root == "" || !strings.Contains(id, PathSeparator) {
		return ChildPlacement{}, false
	}
	asms := make(map[string]Assembly, len(d.Assemblies))
	for _, a := range d.Assemblies {
		asms[a.ID] = a
	}
	asm, ok := asms[d.Root]
	if !ok {
		return ChildPlacement{}, false
	}
	segs := strings.Split(id, PathSeparator)
	for i, seg := range segs {
		last := i == len(segs)-1
		c, copyN, ok := childOnPath(asm, seg, last)
		if !ok {
			return ChildPlacement{}, false
		}
		if last {
			return ChildPlacement{Assembly: asm.ID, Child: c.ID, Ref: c.Ref, Copy: copyN, Pattern: c.Pattern}, true
		}
		next, isAsm := asms[c.Ref]
		if !isAsm {
			return ChildPlacement{}, false
		}
		asm = next
	}
	return ChildPlacement{}, false
}

// childOnPath finds the child of a that one path segment names: its own id, a
// pattern copy "id-n", or — on the last segment — a definition's repeat copy
// "id-k" or "id-n-k". The expansion's own naming, read backwards.
func childOnPath(a Assembly, seg string, last bool) (Child, int, bool) {
	for _, c := range a.Children {
		if c.ID == seg {
			return c, 0, true
		}
	}
	for _, c := range a.Children {
		rest, ok := strings.CutPrefix(seg, c.ID+"-")
		if !ok || rest == "" {
			continue
		}
		first, more, hasMore := strings.Cut(rest, "-")
		n, err := strconv.Atoi(first)
		if err != nil || n < 1 {
			continue
		}
		switch {
		case c.Pattern != nil && !hasMore:
			return c, n, true
		case c.Pattern != nil && last:
			if _, err := strconv.Atoi(more); err == nil {
				return c, n, true
			}
		case c.Pattern == nil && last && !hasMore:
			return c, 0, true
		}
	}
	return Child{}, 0, false
}
