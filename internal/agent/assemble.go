package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Building a model across several passes instead of all at once.
//
// # The wall this is for
//
// One reply has to contain the entire model, so complexity is capped by what
// fits in a single generation — and it frays well before that cap. Measured on
// this deployment: a 13-part car lost its spoiler in 2 runs of 5; a 6-part car
// came back with 5 parts in 3 runs of 10. Asked for a sports car outright, the
// model does not even try — it answers "a sports car is too large and complex
// for a single parametric prototype here" and returns six boxes.
//
// That sentence is the hook. The model already recognises the situation and
// gives up in it. There is nothing wrong with its judgement: a car IS too large
// for one document. What was missing is the option to build it in pieces.
//
// # Why edits and not documents
//
// Each pass sends a "prototype_edit", which merges by id into what is already
// there. An edit CANNOT lose what it does not mention — that is the whole reason
// the form exists (geometry/edit.go) — so a pass that adds the suspension cannot
// drop the body while it is thinking about wishbones. Passing a whole document
// each time would put every finished part back through the model's typing on
// every pass, which is precisely the failure this guards against, multiplied by
// the number of passes.
//
// # Why the same checks run on every pass
//
// A fault caught on pass two is a fault the next eight passes build on top of.
// So each pass goes through the identical gauntlet a single turn does — it must
// build, it must not resize what it did not touch, and it must look right — and
// a pass that cannot be fixed is reported and skipped rather than abandoning the
// work already done.

// assemblyBudget bounds a build. Each step is a model call plus up to three
// repair calls plus a visual check, so a runaway plan is expensive in a way the
// person is waiting through.
const assemblyBudget = 12

// BuildStep is one pass, reported as it happens so a person watching sees
// progress rather than a spinner.
type BuildStep struct {
	N     int    `json:"n"`
	Of    int    `json:"of"`
	Name  string `json:"name"`
	Note  string `json:"note,omitempty"`
	Parts int    `json:"parts"`
}

const planSystem = `You are planning how to BUILD a physical model in several passes,
not describing one. Break the object into subsystems that can be added one after
another, each one a step that makes sense on its own and depends only on steps
before it.

Rules:
- Start with the part everything else attaches to.
- One subsystem per step. "Wheels and suspension and brakes" is three steps.
- Between 2 and 10 steps. Fewer than 2 does not need a plan; more than 10 is a
  level of detail nobody asked for.
- Each step's "what" is one sentence naming what is added and where it goes,
  written for whoever builds it next. It is not a description of the finished
  object.

Return JSON: {"steps": [{"name": "short name", "what": "one sentence"}]}`

// planBuild asks for the order to build something in.
func (c *Conversation) planBuild(ctx context.Context, asked string) ([]buildTask, error) {
	resp, err := c.client.Complete(ctx, llm.Request{
		Role: llm.RoleConverse,
		Messages: []llm.Message{
			{Role: llm.System, Content: planSystem},
			{Role: llm.User, Content: "Plan the build of: " + asked},
		},
		JSONMode:  true,
		MaxTokens: 1500,
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		Steps []buildTask `json:"steps"`
	}
	if err := json.Unmarshal([]byte(repairJSON(resp.Content)), &out); err != nil {
		return nil, err
	}
	var steps []buildTask
	for _, s := range out.Steps {
		if strings.TrimSpace(s.What) == "" {
			continue
		}
		steps = append(steps, s)
		if len(steps) == assemblyBudget {
			break
		}
	}
	if len(steps) < 2 {
		// One step is not a build, it is a turn. Saying so lets the caller fall
		// back to the ordinary path rather than paying for a plan it cannot use.
		return nil, errNotWorthPlanning
	}
	return steps, nil
}

type buildTask struct {
	Name string `json:"name"`
	What string `json:"what"`
}

var errNotWorthPlanning = fmt.Errorf("this does not need building in passes")

const stepSystem = `You are adding ONE subsystem to a model that already exists.

You are given the model so far and the step to do. Return a "prototype_edit"
that ADDS what this step asks for, and nothing else.

- Do not restate parts that are already there. An edit merges by id: what you do
  not mention is kept exactly as it is, which is the point of this form.
- Reuse an existing id only when you mean to REPLACE that part wholesale.
- Position what you add relative to what is already there. Read the dimensions
  you were given; do not assume them.
- Do this step only. The later steps are somebody else's, including yours in a
  moment.

Return JSON: {"speech": "one sentence on what you added", "prototype_edit": {"patch": {"parts": [...], "features": [...]}}}`

// firstStepSystem is for the pass that has nothing to add TO.
//
// An edit cannot be applied to an empty model, and resolveEdit refuses one for
// good reason: on the conversational path "edit the model on screen" with no
// model on screen is a mistake worth naming, not one to paper over. So the first
// pass sends a whole document — there is nothing yet for it to lose — and every
// pass after it sends an edit, which is where the guarantee starts mattering.
const firstStepSystem = `You are building the FIRST part of a model that will be added to in later passes.

Return the whole prototype for this step only. Do not build the later steps: they
are somebody else's, including yours in a moment. Build the part everything else
will attach to, at a size the rest can be positioned against.

Return JSON: {"speech": "one sentence on what you built", "prototype": {"name": "...", "units": "mm", "parts": [...]}}`

// assemble builds a model in passes and returns what it managed to build.
//
// Reports every step through emit as it lands, because a person watching a
// twelve-pass build needs to see it moving. Never returns nil with no error: a
// build that fails on pass two still hands back pass one, which is a model the
// person can work with rather than a wasted minute.
func (c *Conversation) assemble(ctx context.Context, asked string, base *Prototype,
	emit func(BuildStep) error) (*Prototype, []string, error) {

	steps, err := c.planBuild(ctx, asked)
	if err != nil {
		return nil, nil, err
	}

	doc := base
	if doc == nil {
		doc = &geometry.Document{Name: asked, Units: "mm"}
	}
	var notes []string

	for i, step := range steps {
		next, note := c.buildOneStep(ctx, doc, asked, step, i+1, len(steps))
		if note != "" {
			notes = append(notes, note)
		}
		if next != nil {
			doc = next
		}
		if emit != nil {
			if err := emit(BuildStep{N: i + 1, Of: len(steps), Name: step.Name,
				Note: note, Parts: len(doc.Parts)}); err != nil {
				// The caller went away — the connection closed, the reader left.
				// The work so far is still worth returning.
				return doc, notes, nil
			}
		}
	}
	return doc, notes, nil
}

// buildOneStep runs one pass through the same gauntlet a single turn faces.
// Returns the new document, or nil when the pass produced nothing usable, plus
// a note for the reader when something went wrong.
func (c *Conversation) buildOneStep(ctx context.Context, doc *Prototype, asked string,
	step buildTask, n, of int) (*Prototype, string) {

	body, err := json.Marshal(doc)
	if err != nil {
		return nil, ""
	}
	// The CONTRACT goes with every step, not just the conversational turns. A
	// prompt that asks for geometry without saying what geometry looks like gets
	// a schema the model invented — measured on the first live build, which came
	// back {"type":"box_beam","dimensions":{...}} on every step and produced
	// nothing seven times out of eight.
	system, sofar := stepSystem+"\n\n"+geometryContract, fmt.Sprintf("\n\nThe model so far:\n%s", body)
	if len(doc.Parts) == 0 {
		system, sofar = firstStepSystem+"\n\n"+geometryContract, ""
	}
	resp, err := c.client.Complete(ctx, llm.Request{
		Role: llm.RoleConverse,
		Messages: []llm.Message{
			{Role: llm.System, Content: system},
			{Role: llm.User, Content: fmt.Sprintf(
				"Building: %s\nStep %d of %d — %s: %s%s",
				asked, n, of, step.Name, step.What, sofar)},
		},
		JSONMode:  true,
		MaxTokens: converseMaxTokens,
	})
	if err != nil {
		return nil, fmt.Sprintf("Step %d (%s) could not be built: %v.", n, step.Name, err)
	}

	// The SAME parser a turn uses. A second copy here used a different extractor
	// and skipped the dimension-repair fallback, and reported "came back
	// unreadable" on every step of a live eight-step build.
	reply, err := parseReply(resp)
	if err != nil {
		return nil, fmt.Sprintf("Step %d (%s) came back unreadable.", n, step.Name)
	}
	// The edit becomes a document here, exactly as it does on a normal turn.
	if err := reply.resolveEdit(doc); err != nil || reply.Prototype == nil {
		return nil, fmt.Sprintf("Step %d (%s) produced no geometry.", n, step.Name)
	}

	// The same gauntlet, and in the same order, for the same reasons. A fault
	// admitted on pass 2 is a fault the next ten passes build on top of.
	c.repairIfFaulty(ctx, &reply)
	c.repairIfTurned(ctx, &reply, doc)
	c.repairIfItLooksWrong(ctx, &reply, step.What)
	// Last, for the reason the turn paths run it last: it is the only check that
	// verifies itself, and a rewrite after it would leave an unrun script behind.
	c.repairIfScriptsFail(ctx, &reply, doc, nil)

	// A pass that BREAKS the model is refused and the previous state kept: ten
	// good passes must not be lost to an eleventh bad one.
	if len(reply.Prototype.Faults()) > len(doc.Faults()) {
		return nil, fmt.Sprintf("Step %d (%s) was left out: it would have broken the model.", n, step.Name)
	}
	// And a pass that DROPPED something is reported, never silently accepted.
	if gone := vanishedParts(doc, reply.Prototype); len(gone) > 0 {
		return reply.Prototype, fmt.Sprintf("Step %d (%s) removed %s.",
			n, step.Name, strings.Join(gone, ", "))
	}
	return reply.Prototype, reply.Repaired
}

// AssembleForTest exposes the build loop to the live test in agent_test, for
// the same reason RepairForTest and LookForTest do.
func AssembleForTest(ctx context.Context, c *Conversation, asked string, base *Prototype,
	emit func(BuildStep) error) (*Prototype, []string, error) {
	return c.assemble(ctx, asked, base, emit)
}

// buildInPasses runs a multi-pass build when the model asked for one.
//
// # Why the model decides and not a heuristic here
//
// Whether something fits in one document is a judgement about the thing being
// built, and the model already makes it well: asked for a sports car it answered
// "a sports car is too large and complex for a single parametric prototype here"
// and returned six boxes. It was right. All that was missing was somewhere for
// that judgement to go other than an apology.
//
// # Why it degrades rather than fails
//
// A build that cannot be planned, or that produces nothing, leaves the reply
// exactly as it was — the model's own words, which already say what it meant to
// do. Turning a slow path's failure into a lost turn would be strictly worse
// than the six boxes this replaces.
func (c *Conversation) buildInPasses(ctx context.Context, reply *Reply, asked string,
	current *Prototype, onStep func(BuildStep) error) {

	if reply == nil || !reply.BuildInPasses {
		return
	}
	reply.BuildInPasses = false // consumed, whatever happens next

	// A reply that carried geometry AND asked for a build has already answered:
	// the geometry it sent is what it could do in one pass, and building over
	// the top of it would throw that away for something nobody compared.
	if reply.Prototype != nil || reply.PrototypeEdit != nil {
		return
	}

	doc, notes, err := c.assemble(ctx, asked, current, onStep)
	if err != nil || doc == nil || len(doc.Parts) == 0 {
		if err != nil && !errors.Is(err, errNotWorthPlanning) {
			reply.noteRepair("FORGE tried to build this a piece at a time and could not: " + err.Error())
		}
		return
	}
	reply.Prototype = doc
	for _, n := range notes {
		reply.noteRepair(n)
	}
}

// describeStep is one line a person can read while a build runs.
func describeStep(s BuildStep) string {
	line := fmt.Sprintf("Step %d of %d — %s. %d part(s) so far.", s.N, s.Of, s.Name, s.Parts)
	if s.Note != "" {
		line += " " + s.Note
	}
	return line
}
