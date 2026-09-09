package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Parts that were in the model and are not any more.
//
// # The failure this answers
//
// A revision deletes by OMISSION: a document that does not mention a part no
// longer has one. That is unavoidable for a whole prototype — there is no way
// for a rewrite to say "I meant to drop this" — and it means a turn can destroy
// something nobody discussed.
//
// Measured 2026-09-09 against qwen3.7-plus. Asked to make a car body less boxy,
// two runs in three returned a document with no spoiler in it. The reply said
// the body had been reshaped, said nothing about the spoiler, and the spoiler
// was gone. The person is looking at the shape they asked about, so the thing
// that vanished is the thing they are least likely to notice.
//
// # Why this reports rather than refuses
//
// Sometimes the removal is exactly right: "take the cabin off" produces a
// document without a cabin, and refusing it would break the feature this
// conversation is for. Nothing here can tell an intended removal from an
// accidental one — only the person can, and only if they are told. So the turn
// proceeds and says what is no longer there.
//
// The edit form does not need this: an edit that does not mention a part cannot
// remove it, which is the whole reason it exists. This is the safety net under
// the whole-prototype path, where the guarantee is not available.
func vanishedParts(before, after *Prototype) []string {
	if before == nil || after == nil {
		return nil
	}
	kept := make(map[string]bool, len(after.Parts))
	for _, p := range after.Parts {
		kept[p.ID] = true
	}
	// A part consumed as a tool is not something the reader ever saw. A tool
	// "does not also appear as a solid of its own" (geometry.Feature.With) — it
	// became the void it was drawn to make. So it is skipped in both directions:
	// still referenced by a feature AFTER (it did not go anywhere), or already a
	// tool BEFORE (there was no visible part to lose). Only the second case can
	// actually occur — a document that drops a tool while keeping the feature
	// that names it is already faulty and is reported as a fault, not as a loss —
	// but a revision that reworks an arch and drops the cutter it no longer needs
	// is ordinary and must stay quiet. Guarding only AFTER made this test vacuous
	// (drilled 2026-09-09: the mutation stayed green), which is how the wrong
	// direction was found.
	consumed := map[string]bool{}
	for _, f := range append(append([]geometry.Feature{}, before.Features...), after.Features...) {
		for _, id := range f.With {
			consumed[id] = true
		}
	}
	var gone []string
	for _, p := range before.Parts {
		if !kept[p.ID] && !consumed[p.ID] {
			gone = append(gone, p.Label())
		}
	}
	sort.Strings(gone)
	return gone
}

// noteVanished tells the reader what a revision removed, when it removed
// anything. Silent when nothing went, which is the ordinary case.
func noteVanished(reply *Reply, before *Prototype) {
	if reply == nil || reply.Prototype == nil || before == nil {
		return
	}
	gone := vanishedParts(before, reply.Prototype)
	if len(gone) == 0 {
		return
	}
	reply.noteRepair(fmt.Sprintf(
		"No longer in the model: %s. A part left out of a revision is deleted — "+
			"say so if that was not what you wanted.", strings.Join(gone, ", ")))
}
