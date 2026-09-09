package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// sheetSize is the pixel size of one panel of the contact sheet. Four of these
// go side by side. Large enough that a wheel reads as a wheel; small enough that
// four of them are one ordinary image rather than an expensive one.
const sheetSize = 420

// lookSystem asks CLOSED questions.
//
// # Why not "what is wrong with this"
//
// An open question to a vision model produces criticism whether or not anything
// is wrong — it will find the render "low-poly" and the proportions "not
// aggressive enough", and a repair driven by that redesigns a model nobody
// asked it to redesign. Every question below has a determinate answer that the
// picture can actually settle, and the model is told to return an empty list
// when the answer is no. Styling is explicitly not its business: nothing here
// can tell a deliberate wedge from a mistake, and guessing costs the reader the
// design they asked for.
const lookSystem = `You are looking at four orthographic views of a 3D model that was just built:
front, side, top, then an isometric view. Each part is drawn in its own colour.

Answer ONLY these questions, from what you can SEE:

1. Is a part completely hidden inside another part, so it does not appear at all?
2. Is a part floating clear of everything else, touching nothing, when it should be attached?
3. Is a part obviously the wrong way round or lying on the wrong axis for what it is —
   a wheel lying flat like a plate, a body wider than it is long?

Do NOT answer any other question. In particular do NOT comment on PROPORTIONS —
whether something is too tall, too short, too thin, or not the shape you would
expect for the thing described. You do not know what was asked for beyond one
sentence, a concept model is meant to be plain, and every judgement of that kind
in testing was wrong: a car body 1900 wide, 800 tall and 4500 long was called
"grossly too short". Sizes are checked by measurement elsewhere and do not need
an opinion.

Do NOT comment on: how detailed it is, how smooth or faceted it is, colours,
materials, styling, realism, or whether you would have designed it differently.
Those are not defects and a change made for them is a change nobody asked for.

Return JSON: {"problems": [{"part": "the name shown in the model", "detail": "what you see, in one sentence"}]}
Return {"problems": []} when nothing above is true. That is the ordinary answer and
you should not feel obliged to find something.`

// look asks the vision model what it can see wrong with the built model.
//
// # Why the agent needs to see its own work
//
// Everything measured on this deployment says the model writes coordinates
// blind and nobody checks until a person looks at the screen: the car's length
// and width were swapped in 2 runs of 10; a quarter turn was written as 90 and
// read as 116.8 degrees, leaving the wheels tilted; wheels ended up inside a
// body that had become 4.5m wide. A person catches every one of those in a
// glance. Until now FORGE had no glance — its vision model read only images the
// READER uploaded.
//
// # Why it returns an error rather than an empty list
//
// "Could not look" and "looked and saw nothing" are different facts and must
// never collapse into each other. They did once, on the first run of this code:
// the configured vision model did not exist on this endpoint, every call failed
// with model_not_found, and the swallowed error read as a clean bill of health
// on a car with a wheel buried inside its body. A silent miss that reports
// "fine" is worse than no check at all, because it is trusted.
//
// errNoVision is the one absence that is NOT a problem: this deployment ships
// with vision deliberately unconfigured, and the caller stays quiet about that.
func (c *Conversation) look(ctx context.Context, doc *Prototype, asked string) ([]geometry.Problem, error) {
	if c == nil || c.client == nil || doc == nil {
		return nil, errNoVision
	}
	if c.client.ModelFor(llm.RoleVision) == "" {
		return nil, errNoVision
	}
	sheet := geometry.ContactSheet(*doc, geometry.Millimetre, sheetSize)
	if sheet == "" {
		return nil, errNothingToSee
	}

	parts := make([]string, 0, len(doc.Parts))
	for _, p := range doc.Parts {
		parts = append(parts, p.Label())
	}
	// Told what it is looking at and what was asked for: questions 1-3 are much
	// easier to answer about named parts than about coloured blobs.
	prompt := "This was built in answer to: " + asked +
		"\n\nThe parts are: " + strings.Join(parts, ", ")

	resp, err := c.client.Complete(ctx, llm.Request{
		Role:     llm.RoleVision,
		Messages: []llm.Message{{Role: llm.System, Content: lookSystem}, {Role: llm.User, Content: prompt, Images: []string{sheet}}},
		JSONMode: true,
		// Generous, because a reasoning model spends its budget thinking and
		// then has nothing left to answer with. Measured on qwen3.8-max, a
		// 40-token cap produced a correct reading of the picture in
		// reasoning_content and an EMPTY answer — a silent miss wearing a
		// successful call's clothes. RoleVision is now latency-bound
		// (llm/deliberation.go) so it does not deliberate at all, but the cap
		// stays generous: the failure it guards against is silent.
		MaxTokens: 4000,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errNothingToSee
	}
	return parseLook(resp.Content), nil
}

// parseLook reads the model's answer into problems, accepting BOTH shapes it
// produces.
//
// # What this closes
//
// The contract asks for {"part":…,"detail":…}. Measured 2026-09-09,
// qwen3.8-max answered {"problems":["Yes, the wheels are partially hidden
// inside the red body…"]} — plain strings. Read strictly that is an EMPTY
// problem list: a correct observation about a real defect, silently discarded,
// and reported to the reader as "nothing wrong". Being strict about the shape
// of an answer buys nothing here; the sentence is the whole value.
//
// Split out from look so both shapes are fenced without a live call: a fix only
// exercised against a real endpoint is one nobody notices breaking.
func parseLook(body string) []geometry.Problem {
	var out struct {
		Problems []json.RawMessage `json:"problems"`
	}
	if err := json.Unmarshal([]byte(repairJSON(body)), &out); err != nil {
		return nil
	}
	var problems []geometry.Problem
	for _, raw := range out.Problems {
		var obj struct {
			Part   string `json:"part"`
			Detail string `json:"detail"`
		}
		var text string
		switch {
		case json.Unmarshal(raw, &obj) == nil && strings.TrimSpace(obj.Detail) != "":
			text = strings.TrimSpace(obj.Part + ": " + obj.Detail)
		case json.Unmarshal(raw, &text) == nil:
			text = strings.TrimSpace(text)
		}
		if text == "" || text == ":" {
			continue
		}
		problems = append(problems, geometry.Problem{
			Severity: geometry.Error, Name: obj.Part, Detail: text,
		})
	}
	return problems
}

var (
	errNoVision     = errors.New("this deployment has no vision model configured")
	errNothingToSee = errors.New("there was nothing to draw")
)

// repairIfItLooksWrong gives a model that LOOKS wrong one chance to be fixed,
// and says so either way.
//
// # The acceptance rule, and why it is this strict
//
// A vision model can be wrong, and a repair driven by a wrong complaint makes a
// good model worse. So a correction is taken only when it neither introduces a
// fault nor changes the size of anything — the two failures this repository has
// already had to fix — and the reader is told a correction happened, because a
// silent one teaches people to trust the next first draft.
//
// Runs LAST of the three checks: after repairIfFaulty, because a document that
// will not build has a worse problem than one that looks wrong; and after
// repairIfTurned, because a part turned on its side is measurable and does not
// need an opinion.
func (c *Conversation) repairIfItLooksWrong(ctx context.Context, reply *Reply, asked string) {
	if reply == nil || reply.Prototype == nil {
		return
	}
	seen, err := c.look(ctx, reply.Prototype, asked)
	if errors.Is(err, errNoVision) {
		return // deliberate absence: see the config note in converse.go
	}
	if err != nil {
		// A check the reader might assume ran did not. Said, because "FORGE
		// looked and it is fine" and "FORGE could not look" are different
		// things to be told.
		reply.noteRepair("FORGE could not look at the model it built to check it: " + err.Error())
		return
	}
	if len(seen) == 0 {
		return
	}
	before := reply.Prototype
	if fixed := c.repairGeometry(ctx, before, seen); fixed != nil {
		if len(fixed.Faults()) <= len(before.Faults()) && len(turnedOnItsSide(before, fixed)) == 0 {
			reply.Prototype = fixed
			reply.noteRepair("Looking at the model it had just built, FORGE found " +
				problemWord(len(seen)) + " and corrected it: " + seen[0].Detail)
			return
		}
	}
	// Not fixed. Said anyway: the picture is about to be on somebody's screen
	// and this is the one moment when what is wrong with it is known.
	reply.noteRepair("Looking at the model it had just built, FORGE saw something it could " +
		"not correct: " + seen[0].Detail)
}

func problemWord(n int) string {
	if n == 1 {
		return "a problem"
	}
	return "problems"
}

// LookForTest exposes the visual inspection to the live test in agent_test, for
// the same reason RepairForTest exists: only a real vision model can answer
// whether this premise holds, and it lives in the external test package.
func LookForTest(ctx context.Context, c *Conversation, doc *Prototype, asked string) ([]geometry.Problem, error) {
	return c.look(ctx, doc, asked)
}
