package agent

import (
	"fmt"
	"strings"
)

// Saying which children could be one pattern.
//
// Phase 2, stage A3 of docs/plan-2026-09-13-millions-of-parts.md. The finding is
// geometry's (Document.EnumeratedRepetition, repetition.go there); this is where a
// turn and a build step pass it on.
//
// # Why it is a note and not a repair
//
// Every repair loop in this package sends the model a FAULT — a part that is not in
// the model — and installs what comes back. Forty bolts written out one by one are
// all in the model. Rewriting a correct document to make it cheaper is not worth a
// model call on every turn, and a rewrite that went wrong would lose parts to save
// tokens. So a turn says it, through the channel every other correction uses
// (Reply.Repaired, emitted as a notice), and a build step tells the model building
// the next pass, which is extending that model anyway and can fold the run into a
// pattern while it is there.
//
// # Why it is bounded
//
// A model that writes one row out writes all of them. Five sentences say what is
// going on; five hundred would be a prompt nobody reads and a step over its ceiling.

// maxRepetitionNotes is how many runs are named before the rest are counted.
const maxRepetitionNotes = 5

// repetitionNotes is one sentence per run, at most maxRepetitionNotes, and a count
// of the rest. In the document's own order, so the same model says the same thing.
func repetitionNotes(d *Prototype) []string {
	if d == nil {
		return nil
	}
	found := d.EnumeratedRepetition()
	out := make([]string, 0, min(len(found), maxRepetitionNotes+1))
	for i, r := range found {
		if i == maxRepetitionNotes {
			out = append(out, fmt.Sprintf("%d more run(s) of children like these could each be one pattern.", len(found)-i))
			break
		}
		out = append(out, r.Warning())
	}
	return out
}

// noteRepetition tells the reader. Said once however many times it is called, as
// every other note is: a turn's paths may both reach it.
func noteRepetition(reply *Reply) {
	if reply == nil || reply.Prototype == nil {
		return
	}
	for _, line := range repetitionNotes(reply.Prototype) {
		if !strings.Contains(reply.Repaired, line) {
			reply.noteRepair(line)
		}
	}
}

// repetitionForStep is what a build step is told about the model so far, or "".
func repetitionForStep(d *Prototype) string {
	notes := repetitionNotes(d)
	if len(notes) == 0 {
		return ""
	}
	return "\n\nWritten out one child at a time in the model so far, where one child with a \"pattern\" " +
		"places the same copies. When this step touches that assembly, write them as the pattern:\n- " +
		strings.Join(notes, "\n- ")
}
