package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Running the scripts a model wrote, in the turn that wrote them, and rewriting
// the ones that will not build until they do.
//
// # The gap this closes
//
// Every other check in a turn reads the DOCUMENT. Faults() walks outlines and
// features and reports what cannot be built from what is written down;
// turned.go measures; look.go renders and looks. A scripted part defeats all
// three, because its shape is not written down anywhere — it is whatever
// build123d makes when the script runs, and the script does not run until
// somebody asks for the solid.
//
// So a script that raises was invisible to the turn. Measured on the live
// deployment, 2026-09-09, asked for a gear:
//
//	the model chose shape "script"        ✅
//	the sandbox accepted the script       ✅
//	every builder name resolved           ✅
//	the script ran                        ✅
//	build123d refused it                  ❌  ValueError: A face or sketch must be provided
//
// Everything FORGE built to catch a bad answer was upstream of the only thing
// that went wrong. The reader was told the gear was made; the error reached them
// later as an inference note on an export, phrased for a person, with the turn
// long over — and it never reached the MODEL at all, which is the one party that
// could have fixed it.
//
// # Why the builder's own words, verbatim
//
// build123d's messages are unusually specific: "A face or sketch must be
// provided" names the mistake exactly — a solid operation was given a wire.
// Nothing here paraphrases them and nothing here diagnoses. A rule that tried to
// would be a second, worse copy of a CAD kernel's own knowledge, and it would go
// stale the first time build123d improved a message.
//
// # Why the script and not the document
//
// georepair.go hands the model the whole document, and has to defend against a
// model that "fixes" a fault by deleting the part that has it — removedSomething
// exists entirely for that. Here the model is handed one script and can return
// only a script, so that failure is not available to it: the part, its id, its
// position and every other part in the document are never in the request and
// cannot come back changed.
//
// # Why this one can VERIFY and the others cannot
//
// A repaired document is re-checked against the same static rules that faulted
// it, which is the best that can be done for coordinates. A repaired script is
// re-RUN. Acceptance is not "the model returned something different", it is "the
// kernel built it" — so this loop cannot report a fix that did not work, and it
// is the only check in the turn that can say that.

// ScriptRunner runs one model-written script and says whether it built.
//
// Narrow on purpose. The only thing this needs from the CAD kernel is a verdict
// and, when it is bad, the builder's own words — it deliberately does not want
// the STEP that came back. The document stays the single source of truth for
// what the model IS, and the solid is built from it downstream exactly as it was
// before this file existed; a shape cached here would be a second answer to the
// same question.
//
// nil means this deployment does not run scripts at all, which is also what the
// contract tells the model (scriptAvailability). One field, so the two cannot
// disagree — the earlier pair of them could, and a contract that offers a
// capability nothing verifies is how the gear above reached a reader.
type ScriptRunner interface {
	// RunScript builds one scripted part IN THE CONTEXT of its document.
	//
	// The document is passed rather than a resolved parameter map because
	// resolving it is geometry's job and is already done — walking the
	// dependency graph, converting lengths, reporting what does not add up.
	// Handing over a map would mean doing that here, in a package whose whole
	// point is not to know how a document resolves.
	RunScript(ctx context.Context, doc *Prototype, source string) error
}

const (
	// scriptRepairAttempts bounds the rewrites of ONE part. Each is a model call
	// plus a kernel run of up to 30 seconds.
	//
	// Four rather than the two georepair allows, because this loop has evidence
	// georepair does not: it knows whether the last attempt actually built. A
	// repair pass that cannot verify itself has to stop early, since a fifth
	// unverified rewrite is as likely to be worse as better. This one stops
	// when it is RIGHT, and the cap is only there for the case where it never is.
	scriptRepairAttempts = 4
	// scriptRepairBudget bounds the rewrites in one TURN, across every part. A
	// document with six broken scripts must not cost six times the worst case
	// while somebody waits.
	scriptRepairBudget = 8
	// scriptRepairWindow is the wall clock the whole phase gets. The caps above
	// bound CALLS; this bounds the wait, which is the thing a person actually
	// experiences, and it is the one that holds when a kernel is slow rather
	// than wrong.
	scriptRepairWindow = 4 * time.Minute
)

// scriptRepairSystem asks for a script back and nothing else.
//
// It does NOT carry the conversation, for the reason repairSystem does not: what
// to build was settled, and re-reading the request invites a model to build
// something else it likes better. It carries the sandbox's rules because a
// rewrite that reaches for `import numpy` is refused before it runs, and the
// model has no other way to know that here.
const scriptRepairSystem = `You are fixing a build123d script that the CAD kernel refused to build.

You will be given the script and the exact message the builder produced. Return the
corrected script as {"script": "..."} and nothing else.

Rules:
- Build the SAME shape this script was trying to build. You are fixing how it is
  built, not choosing something simpler to build instead.
- Assign the finished solid to ` + "`result`" + `. It must be a solid with volume,
  not a sketch, a wire or an empty compound.
- This runs a DRAWING, not a program. build123d's builders, the maths functions,
  and this document's own PARAMETERS are already in scope by name — lengths in
  millimetres, whole numbers as integers. Only values that RESOLVE are there: a
  "derived" entry FORGE could not evaluate is absent, and "<name> is not
  available here" for something you declared means exactly that. Expressions have
  no sine or cosine, so compute anything trigonometric here in the script, where
  cos, sin, tan and radians all work. ` + "`import math`" + ` and
  ` + "`from build123d import *`" + ` are tolerated and do nothing; any other import,
  and any file, network or system access, is refused before the script runs.
- ` + "`def`" + `, ` + "`lambda`" + `, loops and comprehensions are all allowed, and so are
  build123d's own operators: ` + "`edge @ 0.5`" + ` is the point half way along a curve and
  ` + "`edge % 0.5`" + ` is the tangent there. Classes and try/except are not allowed, and
  neither is any attribute beginning with ` + "`__`" + `.
- Read the message. "A face or sketch must be provided" means a solid operation was
  handed a wire or an open curve — close the profile and make a face from it before
  extruding or revolving it.
- Return only the JSON object.`

// scriptedParts returns the parts whose shape is a script, in document order.
//
// Matched the way the builder matches them — cad.BuildDocument runs a part when
// geometry.Solids marked it "step", which happens for exactly the shape word
// "script" — so this cannot come to a different answer about which parts run
// than the thing that runs them. scriptedPartsAgreeWithTheBuilder fences it.
func scriptedParts(doc *Prototype) []*PrototypePart {
	if doc == nil {
		return nil
	}
	var out []*PrototypePart
	for i := range doc.Parts {
		p := &doc.Parts[i]
		if strings.ToLower(strings.TrimSpace(p.Shape)) != "script" {
			continue
		}
		if strings.TrimSpace(p.Script) == "" {
			// Reported by the builder as "a scripted part with no script", and
			// not something a rewrite can fix: there is nothing to rewrite.
			continue
		}
		out = append(out, p)
	}
	return out
}

// unverified drops the parts whose script this turn did not write.
//
// # Why a turn only runs its OWN scripts
//
// A script is up to thirty seconds of kernel. Without this, a twelve-pass build
// re-runs every script it has ever written on every subsequent pass — the same
// bytes, for the same verdict, eleven more times — because each pass carries the
// whole document forward and an edit leaves what it does not mention untouched.
//
// The rule is byte identity against the document this turn started from: if the
// script is unchanged, this turn did not write it, and the turn that DID write
// it ran it. What that gives up is deliberate and worth naming: a broken script
// stored before this check existed is not re-examined by a later turn that does
// not touch it. That part is still reported — at export, in the build's own
// notes, exactly as it was before — and the moment anybody edits it, it lands
// here.
func unverified(parts []*PrototypePart, previous *Prototype) []*PrototypePart {
	if previous == nil {
		return parts
	}
	was := make(map[string]string, len(previous.Parts))
	for _, p := range previous.Parts {
		was[p.ID] = p.Script
	}
	out := parts[:0]
	for _, p := range parts {
		if before, known := was[p.ID]; known && before == p.Script {
			continue
		}
		out = append(out, p)
	}
	return out
}

// repairScript asks for one corrected script. Returns "" when nothing usable
// came back.
func (c *Conversation) repairScript(ctx context.Context, label, source, refusal string) string {
	resp, err := c.client.Complete(ctx, llm.Request{
		Role: llm.RoleConverse,
		Messages: []llm.Message{
			{Role: llm.System, Content: scriptRepairSystem},
			{Role: llm.User, Content: "The part is: " + label +
				"\n\nThe builder refused it:\n" + refusal +
				"\n\nThe script:\n" + source},
		},
		JSONMode:  true,
		MaxTokens: converseMaxTokens,
	})
	if err != nil {
		return ""
	}
	var out struct {
		Script string `json:"script"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); err != nil {
		return ""
	}
	return strings.TrimSpace(out.Script)
}

// scriptOutcome is what happened to one scripted part.
type scriptOutcome struct {
	Label string
	// Attempts is how many rewrites were tried. Zero means it built first time.
	Attempts int
	// Refusal is the builder's last word, empty when it ended up building.
	Refusal string
}

// repairIfScriptsFail runs every scripted part and rewrites the ones that will
// not build, until they do or the budget is spent.
//
// Reports whether it changed anything. Every outcome — fixed, still broken, and
// the fact that it looked at all — is said out loud through the reply, for the
// reason every other repair says so: a correction nobody hears about teaches a
// reader to trust the next uncorrected draft.
//
// notify is called with a line per attempt when the caller can show progress.
// A script gets up to 30 seconds of kernel and a rewrite is a model call, so a
// document with three bad scripts is minutes of silence otherwise — and a
// silence is indistinguishable from a hang.
func (c *Conversation) repairIfScriptsFail(ctx context.Context, reply *Reply,
	previous *Prototype, notify func(string)) bool {

	if c == nil || c.client == nil || c.runner == nil || reply == nil || reply.Prototype == nil {
		return false
	}
	parts := unverified(scriptedParts(reply.Prototype), previous)
	if len(parts) == 0 {
		return false
	}

	// The wall clock for the whole phase, independent of the turn's own. A turn
	// budget large enough to let a build run for many minutes (see the note on
	// FORGE_TURN_BUDGET) must not also mean one stubborn script can eat all of
	// it while five other parts wait.
	ctx, cancel := context.WithTimeout(ctx, scriptRepairWindow)
	defer cancel()

	budget := scriptRepairBudget
	changed := false
	var outcomes []scriptOutcome

	for _, part := range parts {
		label := part.Label()
		source := part.Script
		var last string
		attempts := 0

		for {
			err := c.runner.RunScript(ctx, reply.Prototype, source)
			if err == nil {
				if attempts > 0 {
					// Accepted because it BUILT, not because the model said so.
					part.Script = source
					changed = true
				}
				outcomes = append(outcomes, scriptOutcome{Label: label, Attempts: attempts})
				break
			}
			refusal := scriptRefusal(err)
			if attempts >= scriptRepairAttempts || budget <= 0 {
				outcomes = append(outcomes, scriptOutcome{Label: label, Attempts: attempts, Refusal: refusal})
				break
			}
			// The same complaint twice means the model is not moving. Faults in
			// a script do not cascade the way a document's do — a script is
			// rebuilt from scratch each time, so the next error is the next
			// thing wrong and a REPEATED error is the same thing still wrong.
			// Stopping here is what keeps a stuck model from spending the whole
			// budget on one part.
			if refusal == last {
				outcomes = append(outcomes, scriptOutcome{Label: label, Attempts: attempts, Refusal: refusal})
				break
			}
			last = refusal
			attempts++
			budget--
			if notify != nil {
				notify(fmt.Sprintf("%s did not build (%s). Rewriting it — attempt %d of %d.",
					label, firstLine(refusal), attempts, scriptRepairAttempts))
			}
			next := c.repairScript(ctx, label, source, refusal)
			if next == "" || next == source {
				// Nothing came back, or the same script came back. Either way
				// running it again would produce the identical refusal.
				outcomes = append(outcomes, scriptOutcome{Label: label, Attempts: attempts, Refusal: refusal})
				break
			}
			source = next
		}
	}
	noteScriptOutcomes(reply, outcomes)
	return changed
}

// noteScriptOutcomes says what running the scripts found.
//
// Silent ONLY when every script built first time, which is the ordinary case and
// carries no news: nothing was corrected and nothing is wrong. Anything else is
// stated, including a part that could not be fixed — that sentence used to reach
// the reader as an inference note on an export, hours later and with the turn it
// belonged to long gone.
func noteScriptOutcomes(reply *Reply, outcomes []scriptOutcome) {
	var fixed, broken []scriptOutcome
	for _, o := range outcomes {
		switch {
		case o.Refusal != "":
			broken = append(broken, o)
		case o.Attempts > 0:
			fixed = append(fixed, o)
		}
	}
	for _, o := range fixed {
		reply.noteRepair(fmt.Sprintf(
			"%s is built by a script that did not build as first written. FORGE rewrote it %s "+
				"and ran it again, and it builds now.", o.Label, timesWord(o.Attempts)))
	}
	for _, o := range broken {
		if o.Attempts == 0 {
			reply.noteRepair(fmt.Sprintf(
				"‼️ %s is built by a script the CAD kernel will not run, so that part will be "+
					"missing from the model: %s", o.Label, o.Refusal))
			continue
		}
		reply.noteRepair(fmt.Sprintf(
			"‼️ %s is built by a script the CAD kernel will not run. FORGE rewrote it %s and it "+
				"still does not build, so that part will be missing from the model. The builder "+
				"says: %s", o.Label, timesWord(o.Attempts), o.Refusal))
	}
}

func timesWord(n int) string {
	if n == 1 {
		return "once"
	}
	return fmt.Sprintf("%d times", n)
}

// scriptRefusal is the sentence the builder produced, as the model should see it.
//
// errs.DetailOf carries the runner's own message; the error's own text is the
// fallback for anything that arrives without one, so a refusal can never reach
// the model as an empty string — which would ask it to fix an unnamed problem
// and get back a rewrite based on nothing.
func scriptRefusal(err error) string {
	if err == nil {
		return ""
	}
	if d := strings.TrimSpace(errs.DetailOf(err)); d != "" {
		return d
	}
	return err.Error()
}

// firstLine is one line of a refusal, for a progress notice that shares a line
// with other text.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 120
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// ScriptRepairForTest exposes the script loop to the live test in agent_test,
// for the reason RepairForTest and LookForTest exist: only a real model and a
// real kernel can answer whether the premise holds — that a model handed
// build123d's own words can fix the script it just wrote — and every other fence
// controls both ends of that exchange.
func ScriptRepairForTest(ctx context.Context, c *Conversation, doc *Prototype) (*Reply, bool) {
	reply := &Reply{Prototype: doc}
	changed := c.repairIfScriptsFail(ctx, reply, nil, func(line string) {})
	return reply, changed
}
