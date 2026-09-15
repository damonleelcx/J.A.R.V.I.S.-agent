package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
- When the object is made of sub-assemblies, give each step an "assembly": a
  short id for the sub-assembly that step builds ("chassis", "front-left-wheel").
  The step is shown that sub-assembly and what it attaches to, not the whole model,
  so a step that adds a door does not have to read the gearbox.

Return JSON: {"steps": [{"name": "short name", "what": "one sentence", "assembly": "optional sub-assembly id"}]}`

// planBuild asks for the order to build something in.
func (c *Conversation) planBuild(ctx context.Context, asked string) ([]buildTask, error) {
	steps, _, err := c.planBuildNoted(ctx, asked)
	return steps, err
}

// planBuildNoted is planBuild, and also says what was done to the plan to keep
// to a limit the person stated — empty when nothing was.
//
// # A stated step limit (2026-09-15)
//
// Asked from the workbench for a desk lamp "three steps at most", the live
// planner planned five. The prompt said "between 2 and 10 steps" and nothing
// else, so the person's own limit never reached the model — and a model told it
// would still be free to ignore it. So both: the limit is said in the request,
// and a plan that comes back over it is brought within it here, deterministically,
// and the caller is told so it can say so.
//
// ‼️ Combined, not cut. The steps past the limit are folded into the last step
// allowed rather than dropped: dropping the tail of a lamp's plan drops its shade,
// which the person asked for as surely as the limit. A step with more in it is
// still one step.
func (c *Conversation) planBuildNoted(ctx context.Context, asked string) ([]buildTask, string, error) {
	limit, stated := statedStepLimit(asked)
	request := "Plan the build of: " + asked
	if stated {
		request += fmt.Sprintf("\n\nThe person asked for at most %d steps. Plan no more than %d: "+
			"combine subsystems into one step where you must.", limit, limit)
	}
	resp, err := c.client.Complete(ctx, llm.Request{
		Role: llm.RoleConverse,
		Messages: []llm.Message{
			{Role: llm.System, Content: planSystem},
			{Role: llm.User, Content: request},
		},
		JSONMode:  true,
		MaxTokens: 1500,
	})
	if err != nil {
		return nil, "", err
	}
	var out struct {
		Steps []buildTask `json:"steps"`
	}
	if err := json.Unmarshal([]byte(repairJSON(resp.Content)), &out); err != nil {
		return nil, "", err
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
	note := ""
	if stated && len(steps) > limit {
		planned := len(steps)
		steps = combineSteps(steps, limit)
		note = fmt.Sprintf("You asked for at most %d steps and the plan came back with %d, so its "+
			"steps %d to %d were combined into step %d.", limit, planned, limit, planned, limit)
	}
	if len(steps) < 2 {
		// One step is not a build, it is a turn. Saying so lets the caller fall
		// back to the ordinary path rather than paying for a plan it cannot use.
		// A stated limit of one lands here too, and that is the same answer.
		return nil, "", errNotWorthPlanning
	}
	return steps, note, nil
}

// stepLimitNumber is a count as a person writes one, in digits or in words.
const stepLimitNumber = `(\d{1,3}|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)`

var (
	// "at most three steps", "no more than 3 passes", "a maximum of 4 stages".
	stepLimitBefore = regexp.MustCompile(`\b(at most|no more than|not more than|up to|a maximum of|maximum of|maximum|max\.?)\s+` +
		stepLimitNumber + `\s+(?:build\s+)?(?:steps?|passes|stages)\b`)
	// "three steps at most", "3 steps max", "four steps or fewer".
	stepLimitAfter = regexp.MustCompile(`\b` + stepLimitNumber + `\s+(?:build\s+)?(?:steps?|passes|stages)\s*,?\s*` +
		`(at most|at the most|max\b|maximum\b|or fewer|or less|tops\b)`)
	// "fewer than five steps", "less than 4 steps", "under 6 steps": one fewer.
	stepLimitBelow = regexp.MustCompile(`\b(fewer than|less than|under)\s+` + stepLimitNumber +
		`\s+(?:build\s+)?(?:steps?|passes|stages)\b`)
)

var stepLimitWords = map[string]int{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12}

// statedStepLimit reads a maximum number of steps the request states, if any.
//
// Deliberately narrow: it reads a MAXIMUM said about steps (or passes, or
// stages), and nothing else. "In three steps" is a description, not a limit, and
// "three parts" is not about steps at all. When more than one limit is stated,
// the smallest wins — a person who said both meant the stricter.
func statedStepLimit(asked string) (int, bool) {
	s := strings.ToLower(asked)
	best, found := 0, false
	take := func(word string, less int) {
		n, ok := stepLimitWords[word]
		if !ok {
			if _, err := fmt.Sscanf(word, "%d", &n); err != nil {
				return
			}
		}
		n -= less
		if n < 1 {
			n = 1
		}
		if !found || n < best {
			best, found = n, true
		}
	}
	for _, m := range stepLimitBefore.FindAllStringSubmatch(s, -1) {
		take(m[2], 0)
	}
	for _, m := range stepLimitAfter.FindAllStringSubmatch(s, -1) {
		take(m[1], 0)
	}
	for _, m := range stepLimitBelow.FindAllStringSubmatch(s, -1) {
		take(m[2], 1)
	}
	return best, found
}

// combineSteps folds every step from the limit-th on into the limit-th, in order.
//
// The combined step keeps each name and each sentence, so whoever builds it is
// asked for all of it. Its assembly is kept only when every folded step named the
// same one: a step shown one sub-assembly while asked to build three would be
// building blind, and with no assembly it is shown the model so far.
func combineSteps(steps []buildTask, limit int) []buildTask {
	if limit < 1 || len(steps) <= limit {
		return steps
	}
	out := append([]buildTask(nil), steps[:limit-1]...)
	tail := steps[limit-1:]
	merged := buildTask{Assembly: tail[0].Assembly}
	var names, whats []string
	for _, s := range tail {
		if name := strings.TrimSpace(s.Name); name != "" {
			names = append(names, name)
		}
		what := strings.TrimSpace(s.What)
		if !strings.HasSuffix(what, ".") {
			what += "."
		}
		whats = append(whats, what)
		if s.Assembly != merged.Assembly {
			merged.Assembly = ""
		}
	}
	merged.Name = strings.Join(names, " + ")
	merged.What = strings.Join(whats, " ")
	return append(out, merged)
}

type buildTask struct {
	Name string `json:"name"`
	What string `json:"what"`
	// Assembly is the sub-assembly this step builds, when the plan names one. On a
	// model written as a tree the step is shown only that (see subtree.go).
	Assembly string `json:"assembly,omitempty"`
}

// maxStepContextBytes is how much of a tree one build step is shown (Phase 2,
// stage A2; decided 2026-09-15). A step over it is refused by name rather than
// shown a truncated model, which would be a model with parts missing and nothing
// to say so.
const maxStepContextBytes = 64 << 10

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
- When the step names an assembly, you are shown that assembly and what it
  attaches to rather than the whole model. Build inside that assembly: patch it by
  id, or create it and place it as a child of the root if it is new. What you are
  not shown is still there, and an edit keeps it.

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

	steps, limited, err := c.planBuildNoted(ctx, asked)
	if err != nil {
		return nil, nil, err
	}

	doc := base
	if doc == nil {
		doc = &geometry.Document{Name: asked, Units: "mm"}
	}
	var notes []string
	if limited != "" {
		// Said, because the plan the person watches is not the one the model
		// returned. See planBuildNoted.
		notes = append(notes, limited)
	}

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
	// ‼️ A step on a tree is shown the assembly it builds, not the whole model
	// (Phase 2, stage A2; see subtree.go). The whole document grows with every
	// subsystem already built; the view grows only with the one being built.
	shown := "The model so far"
	if view := SubtreeModel(doc, step.Assembly); view != "" {
		body = []byte(view)
		shown = "The assembly this step builds, and what it attaches to (the rest of the model is not shown, and an edit keeps it)"
	}
	// And bounded, for a tree: refused by name over the ceiling, never truncated.
	if doc.Root != "" && len(body) > maxStepContextBytes {
		return nil, fmt.Sprintf("Step %d (%s) was not built: what it would be shown is %d KiB, over the %d KiB "+
			"one step is given. A step on a model built from sub-assemblies is shown only the one it names, "+
			"so name a smaller one.", n, step.Name, len(body)>>10, maxStepContextBytes>>10)
	}
	// The CONTRACT goes with every step, not just the conversational turns. A
	// prompt that asks for geometry without saying what geometry looks like gets
	// a schema the model invented — measured on the first live build, which came
	// back {"type":"box_beam","dimensions":{...}} on every step and produced
	// nothing seven times out of eight.
	system, sofar := stepSystem+"\n\n"+geometryContract, fmt.Sprintf("\n\n%s:\n%s", shown, body)
	if !doc.HasGeometry() {
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
	// ‼️ Settled like a turn's own document. A pass never goes through validate(),
	// so a build used to install what each pass typed — never bound, defaulted or
	// noted. An edit is already settled by resolveEdit; a whole document is not,
	// and settling is idempotent, so it is done for both.
	// docs/bugfix/2026-09-11-edited-and-repaired-documents-were-never-settled.md
	// Fence: TestAssemble_APassIsSettled.
	reply.Prototype = settleDocument(reply.Prototype)
	if reply.Prototype == nil {
		return nil, fmt.Sprintf("Step %d (%s) produced no geometry.", n, step.Name)
	}

	// The same gauntlet, and in the same order, for the same reasons. A fault
	// admitted on pass 2 is a fault the next ten passes build on top of.
	c.repairIfFaulty(ctx, &reply)
	c.repairIfTurned(ctx, &reply, doc)
	sheet := c.render(ctx, reply.Prototype)
	c.repairIfItLooksWrong(ctx, &reply, step.What, &sheet)
	// And whether any two parts are in the same place. After the picture checks
	// and before the scripts, for the reason they are ordered that way: this one
	// reads the kernel numbers the render already produced, and the script check
	// keeps the last word. See interference.go.
	c.repairIfPartsOverlap(ctx, &reply, &sheet)
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
	if err != nil || doc == nil || !doc.HasGeometry() {
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
