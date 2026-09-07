package geometry

import "fmt"

// Retired shape words: spellings the contract no longer offers, and what a
// document that already uses one is read as.
//
// # Why a word gets retired rather than fixed
//
// `tube` was in the vocabulary from the beginning and never modelled a bore.
// It could not: a tube's size keys are `radius` and `height` and there has never
// been an inner one, so the document had no field in which to say how thick the
// wall is. Every consumer therefore drew and exported a SOLID cylinder and
// apologised — the renderer in a ghost note, the mesh exporter in an inference,
// the STEP file in its own — for a void that was never stated anywhere.
//
// That is not a defect to repair. Repairing it means adding a size key, and the
// document can already say a bored tube TWO ways, both of which model the void
// properly:
//
//   - a cylinder with a cylinder CUT from it, which is what a drilled bore is;
//   - an extrusion, revolve or sweep whose section carries a `holes` loop, which
//     is what a bore that FOLLOWS the part is — the only one of the two that can
//     turn a corner with a bent tube (see document.go on Holes).
//
// So the word carried no information the rest of the vocabulary did not already
// carry better, and it carried a claim — "this is hollow" — that nothing behind
// it could keep. A third spelling would have been a third thing to keep in step.
//
// # Why the word is still READ
//
// Retiring it from the CONTRACT stops new documents using it. It cannot stop the
// stored ones: every variant already saved with a `tube` in it must still open,
// still render and still export, because the ledger is append-only and a part
// that stops drawing is a revision history that lies about what was proposed.
//
// So the word resolves, in one table, to the shape it always actually was — and
// says so, out loud, in the same channel every other substitution uses. What
// changes is not the geometry: it is that the document no longer offers a person
// a word whose meaning it cannot keep.
//
// # Why this is not done at the document boundary
//
// Rewriting `tube` to `cylinder` on the way IN would be tidier and is refused
// for the reason document.go gives: what is persisted is exactly what the model
// said, so a replay cannot differ from what the person saw. A stored document
// that says `tube` goes on saying `tube`; every reader resolves it the same way,
// through here.
type retirement struct {
	// As is the shape word this one is read as.
	As string
	// Because is said to the reader, in the same channel as every other
	// substitution. Phrased as a fact about the document rather than an
	// apology: nothing is being approximated here — the geometry is exactly
	// what it always was, and only the word has changed.
	Because string
}

var retiredShapes = map[string]retirement{
	"tube": {
		As: "cylinder",
		Because: "%s is drawn as a solid cylinder, which is what a %q has always been in this " +
			"vocabulary: it has no inner dimension, so no bore was ever stated. To model a bore, " +
			"cut a cylinder from it, or give the section a `holes` loop — which is the only one " +
			"of the two that can follow a bend.",
	},
}

// resolveShape maps a shape word onto the one the builders handle, and the note
// a reader is owed when the two differ.
//
// One function for every consumer — the mesh, the exporter, the summary line and
// the renderer's own copy of this table — because a word that resolves one way
// in the viewport and another in the exported file is the exact defect the
// tessellation fences exist to prevent.
func resolveShape(shape, label string) (as, note string) {
	r, ok := retiredShapes[shape]
	if !ok {
		return shape, ""
	}
	return r.As, fmt.Sprintf(r.Because, label, shape)
}

// RetiredShapeWords is the closed list, for the fences that hold the model's
// contract and the renderer's copy of this table in step with it.
func RetiredShapeWords() []string {
	out := make([]string, 0, len(retiredShapes))
	for word := range retiredShapes {
		out = append(out, word)
	}
	return out
}
