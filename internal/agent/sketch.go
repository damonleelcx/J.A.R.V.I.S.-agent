package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Drawing the thing first, and building against the drawing.
//
// # The order, and why it is this way round
//
// A reference picture is generated from what the person asked for, BEFORE any
// geometry is written. It is read by the vision model into a description of the
// form, that description goes into the prompt that writes the document, and when
// the solid exists it is compared back against the same picture. A mismatch is a
// repair, and the loop runs until they agree or the budget is spent.
//
// Generating it AFTER the geometry would make it a picture of what was already
// decided, and comparing a model's work against a picture drawn from that same
// work is a check that cannot fail. Drawing first is what makes the comparison
// mean anything: two independent renderings of one request, and a disagreement
// between them is information.
//
// # ‼️ What the picture is allowed to decide, and what it must not
//
// This is the whole safety property of this file, and it comes from measurement,
// not caution. Spiked against wan2.7-image on 2026-09-09
// (docs/spikes/2026-09-09-generated-reference-images/README.md), asked for a
// 20-tooth involute spur gear:
//
//	{"teeth": 28, "tooth_form": "rounded petal-like lobes",
//	 "has_bore": true, "dimensions_shown": false}
//
// Counted independently in pixels: 30 tooth tips. So the generator draws a good
// IMPRESSION of a gear and a wrong SPECIFICATION of one — 28-30 teeth against
// the 20 asked for, petal lobes rather than involute flanks, and no dimension
// anywhere.
//
// Taken literally, "keep working until the 3D matches the 2D" would therefore
// take a correct 20-tooth involute gear — one the script loop has run and the
// kernel has built — and repair it into a 28-tooth petal shape. The loop would
// work exactly as specified and make the part worse. That is the same trap
// look.go was rescued from twice: a repair driven by a wrong complaint damages a
// good model.
//
// So the division is fixed here and enforced by the prompts:
//
//	the PICTURE is authoritative for FORM     — silhouette, arrangement, what
//	                                            kind of thing this is
//	the REQUEST is authoritative for NUMBERS  — counts, dimensions, units
//
// Every question this file asks is closed and about form. None of them can be
// answered with a number, so no number can be changed by an answer.
//
// # Why one picture with several panels, and not one call per angle
//
// Measured in the same spike, on an L-bracket. Three separate generations asked
// for front, side and top:
//
//	{"distinct_viewing_directions": 1, "second_image_is": "front view",
//	 "third_image_is": "front view", "usable_as_orthographic_set": false}
//
// Asking for an angle does not get that angle — the generator draws the
// recognisable picture of the object every time, so three calls buy three times
// the cost and latency, one viewing direction, and three subtly different
// objects to reconcile. One generation carrying several panels instead:
//
//	{"same_object_in_all_panels": true, "distinct_viewing_directions": 2,
//	 "middle_panel_is": "a foreshortened/rotated front (face) view, not a true
//	  side view", "usable_as_orthographic_set": false}
//
// Consistency is solved, because one generation cannot contradict itself. It is
// still not a projection and never will be — the model draws pictures, not
// projections — which is the second, independent reason no depth or thickness
// may be read out of it.

// sketchAttempts bounds the repairs driven by the picture.
//
// Two, not the four the script loop allows, and for the opposite reason: that
// loop VERIFIES — it re-runs the kernel and knows whether it succeeded — while
// this one is a vision model's opinion about two pictures. An opinion that has
// been acted on twice and still disagrees is more likely to be wrong than the
// model is.
const sketchAttempts = 2

// drawingPromptSystem turns a request into something worth drawing, or says the
// request is not about an object at all.
//
// The refusal half is what keeps this off the critical path of every other kind
// of turn: a question about scheduling costs one cheap call and no picture.
const drawingPromptSystem = `You write the prompt for a reference DRAWING of a physical object.

Given what somebody asked an engineering assistant for, return JSON:
{"draw": true|false, "prompt": "..."}

Set "draw" to false, with an empty prompt, unless they are asking for a physical
object to be designed or changed. A question, a discussion, a scheduling matter
or anything with no shape in it is false.

When true, the prompt must ask for ONE image containing THREE panels side by
side showing the SAME object from different directions, as a clean
black-and-white technical line drawing on a plain white background, no shading,
no text and no dimension numbers. Name the object and its important features —
what it is, what sticks out, what is cut into it, how the parts sit relative to
each other. Do NOT put counts or sizes in the prompt: they are not what the
drawing is for.

Say nothing else. Return only the JSON object.`

// sketchReadSystem reads the picture into words the geometry contract can use.
const sketchReadSystem = `You are looking at a generated reference drawing of an object that is about to
be modelled in CAD. Describe ONLY ITS FORM, for whoever builds it.

Return JSON: {"form": "...", "parts": ["...", "..."]}

- "form" is at most four sentences: the overall shape, what is attached to what,
  what is cut into what, and how the pieces are arranged.
- "parts" names the distinct pieces you can see.

Do NOT report any number: not a count of teeth, holes, slots, spokes or ribs, and
not a dimension. This drawing was generated and its counts are unreliable — they
have been measured wrong before — while the sizes and counts come from what the
person actually asked for. A number you report here would overwrite one they
gave.`

// sketchMatchSystem compares the built solid against the drawing.
//
// Closed questions, for the reason lookSystem gives at length: an open question
// to a vision model produces criticism whether or not anything is wrong, and a
// repair driven by that redesigns a model nobody asked it to redesign. Each
// question here is about FORM and has an answer the two pictures can settle.
const sketchMatchSystem = `The FIRST image is a reference drawing of what was asked for. The SECOND image is
four orthographic views of the 3D model that was actually built.

Answer ONLY these questions, comparing the two:

1. Is a feature clearly present in the drawing — a hole, a slot, a flange, an arm,
   a cut — entirely absent from the built model?
2. Is a piece attached in a clearly different PLACE than the drawing shows, or
   pointing in a clearly different direction?
3. Is the overall arrangement clearly different — something that stands upright in
   the drawing lying flat in the model, or the wrong way round?

Do NOT answer any other question. In particular:

- Do NOT count anything, and do NOT report a count as wrong. The drawing was
  generated and its counts are unreliable; the counts come from what was asked.
- Do NOT comment on dimensions, proportions, size or thickness. The drawing
  carries no dimensions at all, so it cannot settle one.
- Do NOT comment on smoothness, detail, faceting, style, colour or realism. The
  second image is a plain render and is meant to look like one.
- A scripted part is drawn in the second image as a plain block and its real
  shape is not visible there. Say nothing about the shape of a plain block.

Return JSON: {"problems": [{"part": "which piece", "detail": "what differs, in one sentence"}]}
Return {"problems": []} when none of the three is true. That is the ordinary
answer and you should not feel obliged to find something.`

// Sketch is the reference a turn was built against, kept so a reader can see it.
type Sketch struct {
	// Image is the generated drawing's URL. Not persisted anywhere: the provider
	// hands back a temporary object-store link, and a stored one would rot into
	// a broken image beside a model that is still correct.
	Image string `json:"image,omitempty"`
	// Form is what the vision model read out of it, and is what actually reached
	// the prompt that wrote the geometry.
	Form string `json:"form,omitempty"`
}

// drawingPrompt asks whether this request is about an object, and what to draw.
// Returns "" when there is nothing to draw, which is the ordinary answer for
// most turns.
func (c *Conversation) drawingPrompt(ctx context.Context, message string) string {
	resp, err := c.client.Complete(ctx, llm.Request{
		Role: llm.RoleConverse,
		Messages: []llm.Message{
			{Role: llm.System, Content: drawingPromptSystem},
			{Role: llm.User, Content: message},
		},
		JSONMode:  true,
		MaxTokens: 600,
	})
	if err != nil {
		return ""
	}
	var out struct {
		Draw   bool   `json:"draw"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); err != nil || !out.Draw {
		return ""
	}
	return strings.TrimSpace(out.Prompt)
}

// readSketch turns the drawing into a description of its form.
func (c *Conversation) readSketch(ctx context.Context, image string) string {
	resp, err := c.client.Complete(ctx, llm.Request{
		Role: llm.RoleVision,
		Messages: []llm.Message{
			{Role: llm.System, Content: sketchReadSystem},
			{Role: llm.User, Content: "Describe the form of this object.", Images: []string{image}},
		},
		JSONMode:  true,
		MaxTokens: 1500,
	})
	if err != nil || resp == nil {
		return ""
	}
	var out struct {
		Form  string   `json:"form"`
		Parts []string `json:"parts"`
	}
	if err := json.Unmarshal([]byte(repairJSON(resp.Content)), &out); err != nil {
		return ""
	}
	form := strings.TrimSpace(out.Form)
	if len(out.Parts) > 0 {
		form += " The pieces are: " + strings.Join(out.Parts, ", ") + "."
	}
	return withoutNumbers(strings.TrimSpace(form))
}

// numberWords are the counts a description reaches for when it is not using
// digits. Not exhaustive and does not need to be: this removes the ones a
// picture is actually described with.
var numberWords = strings.Fields(`one two three four five six seven eight nine ten
	eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty
	thirty forty fifty sixty seventy eighty ninety hundred
	single pair dozen`)

// withoutNumbers strips counts and dimensions out of what was read from the
// drawing, replacing each with "several".
//
// # Why this is enforced and not asked for
//
// sketchReadSystem already says "Do NOT report any number" in as many words. The
// FIRST live run of this loop came back with:
//
//	"Two circular holes are drilled through the vertical leg…"
//
// — obeying the letter (no digits) and not the rule. It happened to agree with
// the request that time. It will not always: the same generator draws 28-30
// teeth when asked for 20, and a description saying "twenty-eight teeth around
// the rim" would put that count into the prompt that writes the geometry,
// competing with the 20 the person actually asked for.
//
// An instruction a model may ignore is not a guard. This is deterministic, it
// runs on every reading, and the sentence stays readable — "several circular
// holes drilled through the vertical leg" is exactly as useful a description of
// FORM, which is all this text is for. The counts come from the request, which
// is the only place they were ever going to be right.
func withoutNumbers(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	word := func(w string) string {
		trimmed := strings.Trim(strings.ToLower(w), `.,;:()[]"'`)
		// Split on hyphens: the first live run produced "twenty-eight", which a
		// whole-word match walks straight past. "L-shaped" is unaffected because
		// neither half is a count.
		for _, seg := range strings.Split(trimmed, "-") {
			for _, n := range numberWords {
				if seg == n {
					return "several"
				}
			}
		}
		// Any word carrying a digit: "28", "8mm", "20-tooth", "M6".
		if strings.ContainsAny(w, "0123456789") {
			return "several"
		}
		return w
	}
	for i, f := range strings.Fields(s) {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(word(f))
	}
	return b.String()
}

// sketchFirst draws the reference and reads it, before any geometry exists.
//
// Returns nil when this deployment does not draw, when the request is not about
// an object, or when the drawing could not be made or read. Every one of those
// leaves the turn exactly as it was before this file existed — a reference that
// could not be produced must not cost somebody their answer.
func (c *Conversation) sketchFirst(ctx context.Context, message string, notify func(string)) *Sketch {
	if c == nil || c.client == nil || c.illustrator == nil {
		return nil
	}
	if c.illustrator.IllustratorModel() == "" || c.client.ModelFor(llm.RoleVision) == "" {
		// Drawing without being able to READ the drawing is a picture nobody
		// uses: the form never reaches the prompt and the comparison cannot run.
		// Skipped as a pair, so the cost is never paid for half a feature.
		return nil
	}
	prompt := c.drawingPrompt(ctx, message)
	if prompt == "" {
		return nil
	}
	if notify != nil {
		notify("Drawing a reference of what you asked for, before building it.")
	}
	image, err := c.illustrator.Draw(ctx, prompt)
	if err != nil || image == "" {
		return nil
	}
	form := c.readSketch(ctx, image)
	if form == "" {
		// The picture exists and nothing can read it. Returned anyway so the
		// reader still SEES what was drawn, and the comparison still has
		// something to compare against — but with no form, nothing was injected
		// into the geometry prompt, and saying so is left to the caller.
		return &Sketch{Image: image}
	}
	if notify != nil {
		notify("Reference drawn. Building to it: " + firstLine(form))
	}
	return &Sketch{Image: image, Form: form}
}

// matchesSketch asks what the built model has that the drawing does not, and
// the other way round.
func (c *Conversation) matchesSketch(ctx context.Context, doc *Prototype, s *Sketch) []geometry.Problem {
	if s == nil || s.Image == "" || doc == nil {
		return nil
	}
	built := geometry.ContactSheet(*doc, geometry.Millimetre, sheetSize)
	if built == "" {
		return nil
	}
	said := "The first image is the reference. The second is what was built."
	if tools := cuttingTools(doc); len(tools) > 0 {
		said += toolsNote(tools)
	}
	resp, err := c.client.Complete(ctx, llm.Request{
		Role: llm.RoleVision,
		Messages: []llm.Message{
			{Role: llm.System, Content: sketchMatchSystem},
			{Role: llm.User, Content: said, Images: []string{s.Image, built}},
		},
		JSONMode: true,
		// Generous for the reason look.go's is: a reasoning model that spends
		// its budget thinking returns an EMPTY answer, which is a silent miss
		// wearing a successful call's clothes.
		MaxTokens: 4000,
	})
	if err != nil || resp == nil {
		return nil
	}
	return formOnly(parseLook(resp.Content))
}

// formOnly drops the complaints that are about a NUMBER.
//
// # Why this is enforced and not asked for
//
// sketchMatchSystem already says "Do NOT count anything, and do NOT report a
// count as wrong" and "Do NOT comment on dimensions". The live run said:
//
//	"The drawing shows three bolt holes arranged vertically, but the built model
//	 has only two."
//
// The drawing had drawn three; the person had asked for two; the built model was
// right. Acted on, that complaint adds a hole nobody asked for — which is
// precisely the failure this whole file exists to prevent, arriving through the
// one door left open.
//
// This is the FOURTH time in this feature that a rule stated in a prompt was not
// obeyed: the reading reported "Two circular holes", the count survived as a
// spelled-out word, the comparison reported pegs, and now this. The pattern is
// worth naming — a prompt states an intention and a filter enforces it, and only
// one of the two is a guarantee.
//
// # What it costs
//
// A real complaint that happens to carry a number is dropped with the rest: "two
// arms are missing" goes, where "the second arm is missing" stays. That is the
// safe direction and it is chosen deliberately. A wrong repair damages a model
// that was correct; a missed one leaves it exactly as the model wrote it, where
// every other check in the turn can still see it.
func formOnly(problems []geometry.Problem) []geometry.Problem {
	var out []geometry.Problem
	for _, p := range problems {
		if withoutNumbers(p.Detail) != p.Detail {
			continue
		}
		out = append(out, p)
	}
	return out
}

// cuttingTools names the pieces that REMOVE material, in document order.
//
// ‼️ The renderer does not perform a cut. geometry.Tessellate draws parts and
// says so in its own inferences — "the material X removes is NOT removed in this
// file", "four solid POSTS standing on the plate" — because a boolean is the CAD
// kernel's job and this is a triangle builder.
//
// So in the second picture every bolt hole is a peg. Found by the live test on
// the first fixture that used a cut: unprompted, the vision model reported "the
// drawing shows recessed holes (concentric circles) on the vertical arm, but the
// built model has protruding pins or pegs sticking out instead." It was exactly
// right about the picture and exactly wrong about the model, which is the worst
// shape a complaint can have — and it would fire on every mechanical part with a
// hole in it, which is most of them.
func cuttingTools(doc *Prototype) []string {
	if doc == nil {
		return nil
	}
	tool := map[string]bool{}
	for _, f := range doc.Features {
		if strings.EqualFold(strings.TrimSpace(f.Op), "cut") {
			for _, id := range f.With {
				tool[id] = true
			}
		}
	}
	var out []string
	for _, p := range doc.Parts {
		if tool[p.ID] {
			out = append(out, p.Label())
		}
	}
	return out
}

// toolsNote tells the comparison what it is really looking at.
func toolsNote(tools []string) string {
	return "\n\nIn the second image these pieces are the TOOLS that cut material away: " +
		strings.Join(tools, ", ") + ". This renderer cannot perform a cut, so it draws them as " +
		"SOLIDS — a bolt hole appears as a peg standing on the plate. The holes are really " +
		"there in the built model. Do not report them as extra material, as pegs or pins, or " +
		"as a feature the drawing does not show."
}

// repairAgainstSketch brings the model closer to the drawing, and says what it
// could not fix.
//
// Runs after look.go and before the script check, which is where it belongs:
// after, because "does it look like the reference" is a weaker question than
// "does anything float or disappear" and should not pre-empt it; before, because
// the script check must have the last word over anything that rewrites the
// document (see scriptrepair.go).
func (c *Conversation) repairAgainstSketch(ctx context.Context, reply *Reply, s *Sketch, notify func(string)) {
	if reply == nil || reply.Prototype == nil || s == nil || s.Image == "" {
		return
	}
	for i := 0; i < sketchAttempts; i++ {
		seen := c.matchesSketch(ctx, reply.Prototype, s)
		if len(seen) == 0 {
			if i > 0 {
				reply.noteRepair("The model now matches the reference drawing.")
			}
			return
		}
		if notify != nil {
			notify(fmt.Sprintf("The build differs from the reference (%s). Correcting it — "+
				"attempt %d of %d.", firstLine(seen[0].Detail), i+1, sketchAttempts))
		}
		before := reply.Prototype
		fixed := c.repairGeometry(ctx, before, seen)
		// The SAME acceptance rule the visual check uses, and for the same
		// reason: a vision model can be wrong, and a correction driven by a wrong
		// complaint makes a good model worse. It must not introduce a fault and
		// must not resize anything — the picture carries no dimensions, so it can
		// never be the reason a size changed.
		if fixed == nil || len(fixed.Faults()) > len(before.Faults()) ||
			len(turnedOnItsSide(before, fixed)) != 0 {
			reply.noteRepair("Comparing the model with the reference drawing, FORGE saw " +
				"something it could not correct: " + seen[0].Detail)
			return
		}
		reply.Prototype = fixed
		reply.noteRepair("Comparing the model with the reference drawing, FORGE found a " +
			"difference and corrected it: " + seen[0].Detail)
	}
}

// SketchForTest exposes the loop to the live test in agent_test, for the reason
// RepairForTest and LookForTest exist: only a real image model and a real vision
// model can answer whether the premise holds.
func SketchForTest(ctx context.Context, c *Conversation, message string) *Sketch {
	return c.sketchFirst(ctx, message, nil)
}

// MatchForTest exposes the comparison for the same reason.
func MatchForTest(ctx context.Context, c *Conversation, doc *Prototype, s *Sketch) []geometry.Problem {
	return c.matchesSketch(ctx, doc, s)
}
