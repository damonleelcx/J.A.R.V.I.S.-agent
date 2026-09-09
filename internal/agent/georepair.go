package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// One chance to fix geometry that will not build.
//
// # The failure this answers
//
// A turn can produce a document with a part missing and say the opposite.
// Observed twice on real turns, 2026-09-09, both against qwen3.7-plus:
//
//   - "Add a rear spoiler" → the wing's outline had two points. An outline of
//     two points is a line, encloses nothing, and the part was left out. FORGE
//     said "I have added a rear spoiler".
//   - "Add wheel wells" → the arch loop repeated a point, so the loop had an
//     edge of zero length and the WHOLE BODY was dropped. FORGE said "I am
//     cutting wheel wells into the body". The car had no body.
//
// Both were reported honestly in the notes, and both still left the person with
// a model that did not contain what they had just been told it contained. Being
// honest about a hole is better than hiding it and worse than not having one.
//
// # Why a repair and not a stricter prompt
//
// The prompt fix for the first case was real and worth making — the sweep
// guidance genuinely asked for a line. It does not generalise: a repeated point
// in a loop is not a misunderstanding to be worded away, it is a slip, and a
// model will make it again. dimensionrepair.go takes the other route where one
// exists — read the intent — but a duplicated coordinate has no charitable
// reading. Nobody can tell which of the two points was meant.
//
// So the model is shown exactly what the builder said and asked to correct that
// and nothing else.
//
// # Why more than one attempt, and why it still cannot loop
//
// Faults CASCADE. The validator reports the first thing wrong with a loop and
// stops, so fixing it exposes the next. Measured against qwen3.7-plus on the
// real wheel-arch document: the model correctly removed the repeated point on
// the first pass, and the builder then said
//
//	hole 1 is not inside the outline — its point 1 is at (-1900, -50), which is
//	outside. A hole is a loop WITHIN the outline
//
// A single attempt that demanded a perfect result therefore threw away a real
// improvement and reported failure — which is how this was first written, and
// the live test is what caught it.
//
// So the rule is MONOTONE rather than all-or-nothing: a pass is accepted when it
// strictly reduces the number of faults, and the loop stops the moment one does
// not. That cannot run away — the count is a non-negative integer that must fall
// every round to continue — and it is bounded again by repairAttempts so a
// pathological document cannot cost more than a fixed number of calls while
// somebody waits.
const repairAttempts = 2

// repairGeometry returns a corrected document, or nil if it could not be fixed.
//
// Never returns an error: a repair that fails is not a failed turn. The speech
// is already true, the notes already say what is absent, and taking the turn
// away from the person because the fix did not work would be a strictly worse
// outcome than the one this exists to improve.
func (c *Conversation) repairGeometry(ctx context.Context, proto *Prototype, faults []geometry.Problem) *Prototype {
	if c == nil || c.client == nil || proto == nil || len(faults) == 0 {
		return nil
	}
	lines := make([]string, 0, len(faults))
	for _, f := range faults {
		lines = append(lines, "- "+f.Detail)
	}
	body, err := json.Marshal(proto)
	if err != nil {
		return nil
	}

	// Deliberately not the conversation. The model is not being asked what to
	// build — that was settled — it is being asked to correct coordinates in a
	// document it already wrote. Sending the history back would invite it to
	// reconsider the design, which is how a repair turns into a redesign the
	// person did not ask for.
	messages := []llm.Message{
		{Role: llm.System, Content: repairSystem},
		{Role: llm.User, Content: "This document has parts that cannot be built:\n" +
			strings.Join(lines, "\n") +
			"\n\nCorrect ONLY those faults and return the whole document as " +
			`{"prototype": …}` +
			"\n\n" + string(body)},
	}

	resp, err := c.client.Complete(ctx, llm.Request{
		Role:      llm.RoleConverse,
		Messages:  messages,
		JSONMode:  true,
		MaxTokens: converseMaxTokens,
	})
	if err != nil {
		// Returned quietly on purpose, and this is the one place in this file
		// where that is not a silent failure: the FAULT is already on screen.
		// Solids() reports every missing part in the document's own notes, the
		// banner shows them under "Not in the built solid", and a repair that
		// did not happen leaves exactly the state that existed before it was
		// tried. There is no new invisible failure to announce — only an
		// improvement that did not land.
		return nil
	}

	var out struct {
		Prototype *Prototype `json:"prototype"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); err != nil || out.Prototype == nil {
		return nil
	}
	// It must not be WORSE, and it must be different. Faults cascade one at a
	// time — the validator reports the first thing wrong with a loop and stops —
	// so a pass that removes the repeated point and reveals a containment
	// problem leaves the count unchanged while having done real work.
	//
	// Measured against qwen3.7-plus on the real wheel-arch document: one fault
	// in, one fault out, entirely different. A rule that demanded a lower count
	// refused that and reported failure, and a rule that demanded zero refused it
	// twice over. So the bar is "no worse, and something moved", and termination
	// comes from the attempt cap rather than from the count.
	after := out.Prototype.Faults()
	if len(after) > len(faults) || sameFaults(faults, after) {
		return nil
	}
	// And it must still be the same design.
	//
	// ‼️ A model asked to fix a fault will sometimes fix it by DELETING the
	// thing that has it. Measured against qwen3.7-plus on the wheel-arch
	// document: the repair reached zero faults by removing both arches — a
	// perfectly valid document that no longer contains the wheel wells somebody
	// had just asked for, and FORGE would have said the wells were made.
	//
	// A part count alone does not catch that, because the arches live INSIDE a
	// part. So nothing may be removed: no part, no feature, and no loop within a
	// part. Points inside a loop are exempt on purpose — removing a repeated
	// point is a legitimate fix and reduces that count by one, which is exactly
	// what a repair is supposed to do.
	if removedSomething(proto, out.Prototype) {
		return nil
	}
	return out.Prototype
}

// removedSomething reports whether the repair deleted content rather than fixing it.
func removedSomething(before, after *Prototype) bool {
	if len(after.Parts) < len(before.Parts) || len(after.Features) < len(before.Features) {
		return true
	}
	holes := make(map[string]int, len(before.Parts))
	for _, p := range before.Parts {
		holes[p.ID] = len(p.Holes)
	}
	for _, p := range after.Parts {
		was, known := holes[p.ID]
		if known && len(p.Holes) < was {
			return true
		}
		delete(holes, p.ID)
	}
	// A part that was there and is not any more, under whatever id.
	return len(holes) > 0
}

const repairSystem = `You are correcting geometry that a CAD builder refused.

You will be given a list of faults and a JSON document. Return the SAME document
with only those faults fixed, as {"prototype": ...}.

Rules:
- Change nothing except what the faults name. Every other part, dimension,
  parameter and feature must come back exactly as it was.
- Keep every part id.
- An outline is a CLOSED loop of at least three points, listed once — do not
  repeat the first point at the end, and never repeat a point.
- Say nothing else. Return only the JSON object.`

// repairIfFaulty replaces a reply's geometry with a corrected version when the
// one it carries has parts that cannot be built.
//
// Called from both reply paths at the same point, for the reason validate()
// lives at a choke point: a rule enforced in one of two paths holds until
// somebody uses the other one.
//
// Reports whether it changed anything, so a caller can say so.
func (c *Conversation) repairIfFaulty(ctx context.Context, reply *Reply) bool {
	if reply == nil || reply.Prototype == nil {
		return false
	}
	improved := false
	for i := 0; i < repairAttempts; i++ {
		faults := reply.Prototype.Faults()
		if len(faults) == 0 {
			break
		}
		fixed := c.repairGeometry(ctx, reply.Prototype, faults)
		if fixed == nil {
			// No improvement this pass, so another will not help: the model has
			// been given the same document and the same complaint and produced
			// nothing better. Stopping here is what bounds the cost.
			break
		}
		reply.Prototype = fixed
		improved = true
	}
	return improved
}

// RepairForTest exposes the repair to the live test in agent_test.
//
// The live test is the only thing that can answer whether a MODEL can do this
// job when handed the builder's own words — every other fence uses a stub that
// returns whatever the test wants, which proves the plumbing and nothing else.
// It lives in the external test package, so the unexported method needs a door.
func RepairForTest(ctx context.Context, c *Conversation, doc *Prototype) (*Prototype, bool) {
	reply := &Reply{Prototype: doc}
	changed := c.repairIfFaulty(ctx, reply)
	return reply.Prototype, changed
}

// sameFaults reports whether two fault lists say the same things.
//
// Compared by their sentences, because that is what the model was shown and what
// it was asked to act on: a pass that returns the identical complaint has not
// moved, however it rearranged the document around it. Order is not significant
// — the validator walks parts in document order and a repair may reorder nothing,
// but relying on that would make this fragile for no benefit.
func sameFaults(before, after []geometry.Problem) bool {
	if len(before) != len(after) {
		return false
	}
	seen := make(map[string]int, len(before))
	for _, p := range before {
		seen[p.Detail]++
	}
	for _, p := range after {
		if seen[p.Detail] == 0 {
			return false
		}
		seen[p.Detail]--
	}
	return true
}
