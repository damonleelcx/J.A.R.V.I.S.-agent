package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// scriptStub answers each repair call with the next script in a list.
type scriptStub struct {
	replies []string
	calls   int
	seen    []string
}

func (s *scriptStub) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	for _, m := range req.Messages {
		if m.Role == llm.User {
			s.seen = append(s.seen, m.Content)
		}
	}
	if s.calls >= len(s.replies) {
		return nil, context.DeadlineExceeded
	}
	out := s.replies[s.calls]
	s.calls++
	return &llm.Response{Content: `{"script":` + quote(out) + `}`, FinishReason: "stop"}, nil
}

func (s *scriptStub) ModelFor(llm.Role) string { return "script-stub" }

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func gearDoc(script string) *Prototype {
	return &geometry.Document{
		Name: "Gearbox", Units: "mm",
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size: map[string]float64{"width": 100, "height": 10, "depth": 100}},
			{ID: "gear", Name: "Gear", Shape: "script", Script: script},
		},
	}
}

func refused(detail string) error {
	return errs.New("cad.Kernel.RunScript", errs.CodeExternalProtocol).WithDetail("%s", detail)
}

// A script that will not build is rewritten until it does, and the fix is
// accepted only because the kernel BUILT it.
//
// # What this closes
//
// Every other check in a turn reads the document. A scripted part's shape is not
// in the document — it is whatever build123d makes — so a script that raises was
// invisible to the turn and reached the reader as an inference note on an export
// long afterwards, having never reached the model that could fix it.
func TestScripts_RewrittenUntilTheyBuild(t *testing.T) {
	stub := &scriptStub{replies: []string{"result = second_try", "result = third_try"}}
	runs := 0
	runner := stubScriptRunner(func(source string) error {
		runs++
		if source == "result = third_try" {
			return nil
		}
		// A DIFFERENT message each time, or the loop stops on a repeat.
		return refused("ValueError: attempt " + string(rune('a'+runs)))
	})
	c := (&Conversation{client: stub}).WithScripts(runner)

	reply := &Reply{Prototype: gearDoc("result = extrude(profile, 10)")}
	if !c.repairIfScriptsFail(context.Background(), reply, nil, nil) {
		t.Fatal("a script that only builds on the third attempt was not repaired")
	}
	if got := reply.Prototype.Parts[1].Script; got != "result = third_try" {
		t.Errorf("the document did not keep the script that built; it has %q", got)
	}
	if runs != 3 {
		t.Errorf("the kernel ran %d times, want 3: a rewrite is accepted because it BUILT, "+
			"so every rewrite must be run", runs)
	}
	if !strings.Contains(reply.Repaired, "it builds now") {
		t.Errorf("a correction nobody is told about teaches the reader to trust the next "+
			"uncorrected draft. Repaired = %q", reply.Repaired)
	}
}

// The builder's own words reach the model, verbatim.
//
// build123d's messages are unusually specific — "A face or sketch must be
// provided" names the mistake exactly — and paraphrasing them here would be a
// worse copy of a CAD kernel's own knowledge.
func TestScripts_TheBuildersWordsReachTheModel(t *testing.T) {
	stub := &scriptStub{replies: []string{"result = fixed"}}
	runner := stubScriptRunner(func(source string) error {
		if source == "result = fixed" {
			return nil
		}
		return refused("A face or sketch must be provided")
	})
	c := (&Conversation{client: stub}).WithScripts(runner)

	c.repairIfScriptsFail(context.Background(), &Reply{Prototype: gearDoc("result = broken")}, nil, nil)
	if len(stub.seen) == 0 {
		t.Fatal("the model was never asked to fix anything")
	}
	if !strings.Contains(stub.seen[0], "A face or sketch must be provided") {
		t.Errorf("the builder's message never reached the model, so it is being asked to fix "+
			"a problem nobody named:\n%s", stub.seen[0])
	}
	if !strings.Contains(stub.seen[0], "result = broken") {
		t.Errorf("the script itself never reached the model:\n%s", stub.seen[0])
	}
}

// A script that never builds leaves the ORIGINAL in place and says so loudly.
//
// An unverified rewrite is not an improvement on an unverified original: neither
// builds, and replacing one with the other discards what the model meant while
// changing nothing a reader can see.
func TestScripts_UnfixableKeepsTheOriginalAndIsSaidOutLoud(t *testing.T) {
	stub := &scriptStub{replies: []string{"result = a", "result = b", "result = c", "result = d"}}
	n := 0
	runner := stubScriptRunner(func(string) error {
		n++
		return refused("ValueError: still wrong " + string(rune('a'+n)))
	})
	c := (&Conversation{client: stub}).WithScripts(runner)

	reply := &Reply{Prototype: gearDoc("result = original")}
	if c.repairIfScriptsFail(context.Background(), reply, nil, nil) {
		t.Error("a script that never built was reported as repaired")
	}
	if got := reply.Prototype.Parts[1].Script; got != "result = original" {
		t.Errorf("an unverified rewrite replaced the original; script is %q", got)
	}
	if stub.calls > scriptRepairAttempts {
		t.Errorf("the model was asked %d times, more than the cap of %d: somebody is waiting",
			stub.calls, scriptRepairAttempts)
	}
	if !strings.Contains(reply.Repaired, "missing from the model") {
		t.Errorf("the reader is not told the part will be absent. Repaired = %q", reply.Repaired)
	}
	if !strings.Contains(reply.Repaired, "still wrong") {
		t.Errorf("the builder's last word is not passed on, so nobody can act on it. "+
			"Repaired = %q", reply.Repaired)
	}
}

// The same complaint twice stops the loop.
//
// A script is rebuilt from scratch every run, so a repeated message is the same
// thing still wrong rather than the next thing wrong — unlike a document's
// faults, which cascade. Without this a stuck model spends the whole turn budget
// on one part.
func TestScripts_StopsWhenTheModelIsNotMoving(t *testing.T) {
	stub := &scriptStub{replies: []string{"result = a", "result = b", "result = c", "result = d"}}
	runner := stubScriptRunner(func(string) error { return refused("the same complaint") })
	c := (&Conversation{client: stub}).WithScripts(runner)

	c.repairIfScriptsFail(context.Background(), &Reply{Prototype: gearDoc("result = x")}, nil, nil)
	if stub.calls != 1 {
		t.Errorf("the model was asked %d times for the same unchanging complaint, want 1", stub.calls)
	}
}

// A deployment with no runner does not run scripts and does not pretend to.
func TestScripts_NoRunnerIsNotAQuietPass(t *testing.T) {
	c := &Conversation{client: &scriptStub{}}
	reply := &Reply{Prototype: gearDoc("result = anything")}
	if c.repairIfScriptsFail(context.Background(), reply, nil, nil) {
		t.Error("a deployment that cannot run scripts reported running one")
	}
	if c.scriptsAvailable() {
		t.Error("the contract offers scripts while nothing can run them, which is the " +
			"disagreement WithScripts(runner) exists to make impossible")
	}
}

// A turn only runs the scripts it wrote.
//
// A twelve-pass build carries the whole document forward on every pass. Without
// this, every script it has ever written is re-run on every later pass — the
// same bytes, for the same verdict, up to eleven more times, at thirty seconds
// of kernel each.
func TestScripts_UnchangedScriptsAreNotRerun(t *testing.T) {
	runs := 0
	runner := stubScriptRunner(func(string) error { runs++; return nil })
	c := (&Conversation{client: &scriptStub{}}).WithScripts(runner)

	previous := gearDoc("result = settled")
	reply := &Reply{Prototype: gearDoc("result = settled")}
	c.repairIfScriptsFail(context.Background(), reply, previous, nil)
	if runs != 0 {
		t.Errorf("an untouched script was run %d times; the turn that wrote it already ran it", runs)
	}

	reply.Prototype.Parts[1].Script = "result = changed"
	c.repairIfScriptsFail(context.Background(), reply, previous, nil)
	if runs != 1 {
		t.Errorf("a script this turn CHANGED was run %d times, want 1", runs)
	}
}

// The parts this runs are exactly the parts the builder runs.
//
// cad.BuildDocument runs a part when geometry.Solids marked it "step". If the
// two ever disagree, a part is either verified and not built or built and never
// verified — and the second is the whole bug this file exists for.
func TestScripts_SelectionAgreesWithTheBuilder(t *testing.T) {
	doc := &geometry.Document{
		Name: "Mixed", Units: "mm",
		Parts: []geometry.Part{
			{ID: "a", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}},
			{ID: "b", Shape: "script", Script: "result = something"},
			{ID: "c", Shape: "tube", Size: map[string]float64{"radius": 1, "height": 1}},
			{ID: "d", Shape: "SCRIPT", Script: "result = shouty"},
			{ID: "e", Shape: "script"}, // no script: nothing to run and nothing to rewrite
		},
	}
	mine := map[string]bool{}
	for _, p := range scriptedParts(doc) {
		mine[p.ID] = true
	}

	solids, _ := geometry.Solids(*doc, geometry.Millimetre)
	theirs := map[string]bool{}
	for _, s := range solids {
		if s.Shape == "step" {
			theirs[s.ID] = true
		}
	}
	// "e" is the one deliberate difference: the builder marks it and then reports
	// "a scripted part with no script", which no rewrite can fix.
	delete(theirs, "e")

	// Not vacuous: two parts really are scripted, and a fence over two empty
	// sets is the shape this repository has already been caught by twice.
	if len(theirs) != 2 {
		t.Fatalf("the builder marked %d parts to run, want 2 — this fence would be measuring "+
			"nothing", len(theirs))
	}
	if len(mine) != len(theirs) {
		t.Fatalf("this file verifies %v; the builder runs %v", keysOf(mine), keysOf(theirs))
	}
	for id := range theirs {
		if !mine[id] {
			t.Errorf("part %q is RUN by the builder and never verified here, which is exactly "+
				"the gap scriptrepair.go closes", id)
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A refusal always reaches the model as words.
//
// An empty string would ask it to fix an unnamed problem, and it would answer.
func TestScripts_RefusalIsNeverEmpty(t *testing.T) {
	if got := scriptRefusal(errors.New("bare error")); got != "bare error" {
		t.Errorf("an error with no detail arrived as %q", got)
	}
	if got := scriptRefusal(refused("named")); got != "named" {
		t.Errorf("the runner's own detail arrived as %q", got)
	}
}

// turnStub answers a whole turn with one prepared reply, then answers every
// repair call with the script the test wants.
type turnStub struct {
	first  string
	script string
	calls  int
}

func (s *turnStub) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	s.calls++
	if s.calls == 1 {
		return &llm.Response{Content: s.first, FinishReason: "stop"}, nil
	}
	return &llm.Response{Content: `{"script":` + quote(s.script) + `}`, FinishReason: "stop"}, nil
}

// Stream makes this a real Streamer, so the streaming subtest exercises the
// STREAMING path's own call site rather than falling back into the buffered one.
// Without it both subtests would prove the same thing and deleting the call from
// converse_stream.go — the path people actually use — would leave them green.
func (s *turnStub) Stream(_ context.Context, _ llm.Request, onChunk func(llm.Chunk) error) error {
	s.calls++
	if err := onChunk(llm.Chunk{Delta: s.first}); err != nil {
		return err
	}
	return onChunk(llm.Chunk{Done: true, FinishReason: "stop", Model: "turn-stub"})
}

func (s *turnStub) ModelFor(r llm.Role) string {
	if r == llm.RoleVision {
		return "" // no vision here: this fence is about scripts, not looking
	}
	return "turn-stub"
}

const oneGearTurn = `{"speech":"Here is the gear.","prototype":{"name":"Gearbox","units":"mm",
"parts":[{"id":"gear","name":"Gear","shape":"script","script":"result = broken"}]}}`

// The script check is REACHED by a real turn, on both reply paths.
//
// # Why a wiring fence and not only the unit fences above
//
// Every fence in this file drives repairIfScriptsFail directly, so all of them
// stay green if the call is deleted from the reply path. That is not a
// hypothetical: the same drill on scriptAvailability found exactly that hole —
// perfect wording, never sent. A check nothing calls is a check nobody has.
func TestScripts_TheTurnActuallyRunsThem(t *testing.T) {
	for _, path := range []string{"buffered", "streamed"} {
		t.Run(path, func(t *testing.T) {
			stub := &turnStub{first: oneGearTurn, script: "result = fixed"}
			ran := 0
			runner := stubScriptRunner(func(source string) error {
				ran++
				if source == "result = fixed" {
					return nil
				}
				return refused("A face or sketch must be provided")
			})
			c := (&Conversation{client: stub}).WithScripts(runner)

			var reply *Reply
			var err error
			if path == "buffered" {
				reply, err = c.Respond(context.Background(), "", nil, "make me a gear", "", nil, nil)
			} else {
				err = c.RespondStream(context.Background(), "", nil, "make me a gear", "", nil, nil,
					func(ev StreamEvent) error {
						if ev.Kind == "prototype" {
							reply = &Reply{Prototype: ev.Prototype}
						}
						return nil
					})
			}
			if err != nil {
				t.Fatalf("the turn failed: %v", err)
			}
			if ran == 0 {
				t.Fatal("the turn never ran the script it wrote. Every other check reads the " +
					"document, where a scripted part's shape does not appear, so nothing else " +
					"in this turn could have noticed.")
			}
			if reply == nil || len(reply.Prototype.Parts) == 0 {
				t.Fatal("the turn produced no geometry")
			}
			if got := reply.Prototype.Parts[0].Script; got != "result = fixed" {
				t.Errorf("the turn kept a script that does not build: %q", got)
			}
		})
	}
}

// buildStub answers by WHICH question it was asked, not by call order.
//
// A multi-pass build interleaves a plan, a step, a script repair and a look, and
// a stub keyed on call count silently answers the wrong one the moment the order
// changes — which is a green test measuring nothing.
type buildStub struct{ repairs int }

func (b *buildStub) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	var system string
	for _, m := range req.Messages {
		if m.Role == llm.System {
			system = m.Content
		}
	}
	switch {
	case strings.Contains(system, "planning how to BUILD"):
		return &llm.Response{Content: `{"steps":[{"name":"Plate","what":"the base plate"},
			{"name":"Gear","what":"the gear on the plate"}]}`, FinishReason: "stop"}, nil
	case strings.Contains(system, "fixing a build123d script"):
		b.repairs++
		return &llm.Response{Content: `{"script":"result = fixed"}`, FinishReason: "stop"}, nil
	default:
		return &llm.Response{Content: oneGearTurn, FinishReason: "stop"}, nil
	}
}

func (b *buildStub) ModelFor(r llm.Role) string {
	if r == llm.RoleVision {
		return ""
	}
	return "build-stub"
}

// A multi-pass build runs the scripts each pass writes.
//
// # Why this needs its own fence
//
// A fault admitted on pass two is a fault the next ten passes build on top of,
// which is why every pass runs the same gauntlet a single turn does. A drill
// proved the turn fences could not see this call site removed: both reply paths
// went red and the build loop stayed green, so a twelve-pass build would have
// gone back to shipping scripts nobody ran.
func TestScripts_MultiPassBuildRunsThemToo(t *testing.T) {
	stub := &buildStub{}
	ran := 0
	runner := stubScriptRunner(func(source string) error {
		ran++
		if source == "result = fixed" {
			return nil
		}
		return refused("A face or sketch must be provided")
	})
	c := (&Conversation{client: stub}).WithScripts(runner)

	doc, _, err := c.assemble(context.Background(), "a gearbox", nil, nil)
	if err != nil {
		t.Fatalf("the build failed: %v", err)
	}
	if ran == 0 {
		t.Fatal("a multi-pass build never ran a script it wrote. Every pass runs the same " +
			"gauntlet a turn does precisely so a defect cannot be built on top of.")
	}
	if stub.repairs == 0 {
		t.Fatal("the script that would not build was never sent back to be rewritten")
	}
	for _, p := range doc.Parts {
		if p.Script != "" && p.Script != "result = fixed" {
			t.Errorf("the build kept a script that does not build: %q", p.Script)
		}
	}
}

// laterRepairsCannotUndoTheVerification: the script check has the last word.
//
// # What this closes
//
// It was placed beside repairIfFaulty, which reads well and is wrong: two more
// repairs run after that point, and BOTH hand back a whole new document from the
// model. Either can carry a script that has never been run, and the turn would
// still say the script builds — the verification would be about a document that
// no longer exists.
//
// Observed on the live deployment the day it shipped. The visual check reported
// "the part is a rectangular block rather than a circular gear" about a gear
// whose script had just built a correct 12565.7 mm³ solid, because the renderer
// draws a scripted part as a bounding box. It then "corrected" it.
func TestScripts_TheScriptCheckHasTheLastWord(t *testing.T) {
	for _, path := range []string{"buffered", "streamed"} {
		t.Run(path, func(t *testing.T) {
			stub := &lastWordStub{}
			var order []string
			runner := stubScriptRunner(func(source string) error {
				order = append(order, "ran:"+source)
				if source == "result = fixed" {
					return nil
				}
				return refused("A face or sketch must be provided")
			})
			c := (&Conversation{client: stub}).WithScripts(runner)

			var reply *Reply
			var err error
			if path == "buffered" {
				reply, err = c.Respond(context.Background(), "", nil, "make me a gear", "", nil, nil)
			} else {
				err = c.RespondStream(context.Background(), "", nil, "make me a gear", "", nil, nil,
					func(ev StreamEvent) error {
						if ev.Kind == "prototype" {
							reply = &Reply{Prototype: ev.Prototype}
						}
						return nil
					})
			}
			if err != nil {
				t.Fatalf("the turn failed: %v", err)
			}
			if reply == nil || len(reply.Prototype.Parts) == 0 {
				t.Fatal("the turn produced no geometry")
			}
			// The vision model replaced the whole document with one carrying an
			// unrun script. Whatever else happened, the turn must not keep it.
			if got := reply.Prototype.Parts[0].Script; got == "result = from the vision repair" {
				t.Errorf("the turn kept a script that no check ever ran. A repair that runs "+
					"AFTER the verification undoes it, and the reply still claims the script "+
					"builds.\nran, in order: %v", order)
			}
			if len(order) == 0 {
				t.Fatal("no script was run at all")
			}
			if last := order[len(order)-1]; last != "ran:result = fixed" {
				t.Errorf("the last script the kernel saw was %q, not the one the turn kept. "+
					"The verification is only worth something if nothing rewrites the document "+
					"after it.\nran, in order: %v", last, order)
			}
		})
	}
}

// lastWordStub plays a turn that produces a broken script, a vision model that
// finds a problem, and a repair that hands back a DIFFERENT unrun script.
type lastWordStub struct{ scriptFixes int }

func (s *lastWordStub) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	var system string
	for _, m := range req.Messages {
		if m.Role == llm.System {
			system = m.Content
		}
	}
	switch {
	case strings.Contains(system, "fixing a build123d script"):
		s.scriptFixes++
		return &llm.Response{Content: `{"script":"result = fixed"}`, FinishReason: "stop"}, nil
	case strings.Contains(system, "four orthographic views"):
		// The false positive this whole ordering problem was found through.
		return &llm.Response{Content: `{"problems":[{"part":"Gear","detail":"it is a rectangular block, not a gear"}]}`,
			FinishReason: "stop"}, nil
	case strings.Contains(system, "correcting geometry"):
		// A whole new document, with a script nothing has run.
		return &llm.Response{Content: `{"prototype":{"name":"Gearbox","units":"mm","parts":[
			{"id":"gear","name":"Gear","shape":"script","script":"result = from the vision repair"}]}}`,
			FinishReason: "stop"}, nil
	default:
		return &llm.Response{Content: oneGearTurn, FinishReason: "stop"}, nil
	}
}

func (s *lastWordStub) ModelFor(llm.Role) string { return "last-word-stub" }

// The vision check is told which parts it cannot see.
//
// The contact sheet is drawn from the DOCUMENT. A scripted part's shape is not
// in the document, so it is drawn as a bounding box and looks like a plain
// block whatever it really is. Asked about it, the model answers the only way it
// can — and this file's own doc says what a repair driven by a wrong complaint
// does to a good model.
func TestScripts_TheVisionCheckIsToldItCannotSeeThem(t *testing.T) {
	seen := &visionPromptSpy{}
	c := &Conversation{client: seen}
	doc := &geometry.Document{
		Name: "Mixed", Units: "mm",
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size: map[string]float64{"width": 100, "height": 10, "depth": 100}},
			{ID: "gear", Name: "Gear Body", Shape: "script", Script: "result = something"},
		},
	}
	if _, err := c.look(context.Background(), doc, "a gearbox"); err != nil {
		t.Fatalf("look failed: %v", err)
	}
	if !strings.Contains(seen.prompt, "Gear Body") ||
		!strings.Contains(seen.prompt, "PLAIN BLOCK") {
		t.Errorf("the vision model was not told that the scripted part is drawn as a block, "+
			"so it will report every scripted part as the wrong shape, on every turn:\n%s",
			seen.prompt)
	}
	if strings.Contains(seen.prompt, "PLAIN BLOCK") && strings.Contains(seen.prompt, "Plate,") &&
		strings.Count(seen.prompt, "Plate") > 1 {
		t.Errorf("an ordinary part was named as one the model cannot see:\n%s", seen.prompt)
	}
}

type visionPromptSpy struct{ prompt string }

func (s *visionPromptSpy) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	for _, m := range req.Messages {
		if m.Role == llm.User {
			s.prompt = m.Content
		}
	}
	return &llm.Response{Content: `{"problems":[]}`, FinishReason: "stop"}, nil
}

func (s *visionPromptSpy) ModelFor(llm.Role) string { return "vision-spy" }
