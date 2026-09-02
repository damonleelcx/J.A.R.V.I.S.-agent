package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// converseFraming is the role instruction for the workbench conversation.
//
// This is the surface the PRD calls the control plane: the user talks, FORGE
// reasons with them, and the visual workspace follows. Two things it must get
// right that a chat assistant does not have to:
//
//   - Speech and screen carry different loads. Dense technical content belongs
//     on the screen with a short spoken summary; reading a table aloud is worse
//     than useless (PRD §5.3).
//   - Proposing geometry is not building it. A render is a picture of a
//     proposal, and saying so is not hedging — PRD VIS-06 makes it a hard
//     requirement, because photorealism convinces people of things nobody
//     checked.
const converseFraming = `You are in CONVERSATION at the workbench. The person is talking to you, probably
by voice, while looking at a 3D viewport and a workspace panel beside it.

How to answer:

- SPEECH is short. Two or three sentences. Lead with the answer.
- The SCREEN carries the detail: dimensions, lists, comparisons, geometry.
  Never read a table or a list of numbers aloud — put it on screen and say what
  it means.
- When they describe something physical, propose GEOMETRY. A shape they can
  turn around is worth more than a paragraph describing it.
- Ask a question only when the answer changes what you would build. Otherwise
  choose, say what you chose, and continue.

Reply with JSON only:

{
  "speech": "what to say aloud — short, plain, no markdown",
  "detail": "optional longer text for the screen; markdown is fine",
  "prototype": null or {
    "name": "what this is",
    "units": "mm" | "cm" | "m",
    "parts": [
      {
        "id": "stable-kebab-id",
        "name": "human name",
        "shape": "box" | "cylinder" | "cone" | "sphere" | "tube" | "plane",
        "size": {"width":1,"height":1,"depth":1,"radius":0.5,"radius_top":0.5},
        "position": [0,0,0],
        "rotation": [0,0,0],
        "color": "#b8bcc4",
        "opacity": 1.0,
        "note": "what this part is for"
      }
    ],
    "assumptions": ["anything you chose that they did not specify"],
    "not_verified": ["what this render does NOT establish"]
  },
  "proposed_goal": null or {
    "title": "short title",
    "statement": "what you would actually do, in full",
    "risk_tier": "r1"
  }
}

About "prototype":

- Positions are in the stated units, Y is up, and the origin is the assembly's
  centre. Parts are centred on their own position.
- Only emit it when the shape is the point. Do not attach geometry to a
  conversation about scheduling.
- "assumptions" is where every dimension you CHOSE goes. If they said "a
  bracket" and you picked 60mm, that belongs there.
- A figure from a PUBLISHED STANDARD is not an assumption and does not belong in
  that list. "NEMA 17 is 42.3mm across the face" is a claim about the world, and
  you are recalling it, not reading it — there is no reference source in this
  deployment and nothing here can check you. Name the standard, say plainly that
  the figure is from memory, and prefer not to quote a number at all unless it
  changes what you would build. A wrong figure attached to a real standard is
  more dangerous than no figure, because it is specific enough to be acted on.
- "not_verified" is mandatory whenever geometry is present, and it must be
  specific. "Not stress-analysed" and "no interference check was run" are useful;
  "this is a concept" is not. There is no FEA or CAD kernel in this deployment,
  so nothing here has been checked against anything.

About "proposed_goal": offer one only when they have described work they want
DONE, not merely discussed. It is a proposal — nothing runs until they start it.`

// Conversation is the workbench dialogue.
//
// It is deliberately separate from the planner and the executor. This surface
// reasons WITH someone; those two act. Keeping them apart means a conversation
// cannot accidentally start work, which is the property PRD AGT-04 is protecting
// when it says autonomy is never raised silently.
type Conversation struct {
	client llm.Client
	char   persona.Character
}

// NewConversation returns the conversational surface.
func NewConversation(client llm.Client, char persona.Character) *Conversation {
	return &Conversation{client: client, char: char}
}

// Turn is one exchange, as stored and replayed.
type Turn struct {
	Role    string `json:"role"` // "user" | "forge"
	Content string `json:"content"`
}

// Prototype is a proposed 3D form.
type Prototype struct {
	Name  string          `json:"name"`
	Units string          `json:"units"`
	Parts []PrototypePart `json:"parts"`
	// Assumptions is every dimension FORGE chose rather than was given.
	Assumptions []string `json:"assumptions"`
	// NotVerified is what this render does NOT establish. Required whenever
	// geometry is present — PRD VIS-06: photorealism never implies
	// manufacturability, structural adequacy, or compliance.
	NotVerified []string `json:"not_verified"`
}

// PrototypePart is one solid.
type PrototypePart struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Shape    string             `json:"shape"`
	Size     map[string]float64 `json:"size"`
	Position []float64          `json:"position"`
	Rotation []float64          `json:"rotation"`
	Color    string             `json:"color"`
	Opacity  float64            `json:"opacity"`
	Note     string             `json:"note"`
}

// ProposedGoal is work FORGE offers to do. Nothing runs until a human starts it.
type ProposedGoal struct {
	Title     string `json:"title"`
	Statement string `json:"statement"`
	RiskTier  string `json:"risk_tier"`
}

// Reply is one response.
type Reply struct {
	Speech       string        `json:"speech"`
	Detail       string        `json:"detail"`
	Prototype    *Prototype    `json:"prototype"`
	ProposedGoal *ProposedGoal `json:"proposed_goal"`
	// Claims is the epistemic ledger (PRD RSN-05): every statement in this reply
	// together with how FORGE came to hold it. Derived from the reply, never
	// asked of the model — see ClaimLedger.
	Claims []Claim `json:"claims,omitempty"`
	// Recalled is computed from the reply's own text, never asked of the model.
	// A component cannot be its own guard: the failure being caught here is the
	// model stating a standard's figure it has no way to check, and asking it to
	// self-report that is asking it to notice the thing it just failed to
	// notice. See standards.go.
	Recalled []StandardsClaim `json:"recalled,omitempty"`
	Model    string           `json:"model"`
	Usage    llm.Usage        `json:"-"`
	// LatencyMS is measured client-of-the-provider side and reported to the UI,
	// which displays the REAL figure rather than claiming the PRD's ≤700ms
	// target. A target asserted without measurement is a marketing claim.
	LatencyMS int64 `json:"latency_ms"`
}

// Respond produces one turn of conversation.
func (c *Conversation) Respond(ctx context.Context, history []Turn, message string, workspaceNote string) (*Reply, error) {
	const op = "agent.Conversation.Respond"

	if strings.TrimSpace(message) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).WithDetail("empty message")
	}

	messages := []llm.Message{
		{Role: llm.System, Content: persona.SystemPrompt(c.char, converseFraming)},
	}
	// Bounded history. A workbench session runs for hours; replaying all of it
	// on every turn spends the budget on repetition and eventually exceeds the
	// window. The most recent exchanges are what bear on the next one.
	const keep = 16
	if len(history) > keep {
		history = history[len(history)-keep:]
	}
	for _, t := range history {
		role := llm.User
		if t.Role == "forge" {
			role = llm.Assistant
		}
		messages = append(messages, llm.Message{Role: role, Content: t.Content})
	}

	user := message
	if workspaceNote != "" {
		// What is currently on screen, so "make that taller" resolves against
		// what they are looking at rather than against the transcript.
		user = "[What is on screen right now: " + workspaceNote + "]\n\n" + message
	}
	messages = append(messages, llm.Message{Role: llm.User, Content: user})

	resp, err := c.client.Complete(ctx, llm.Request{
		Role:      llm.RoleConverse,
		Messages:  messages,
		JSONMode:  true,
		MaxTokens: 6000,
	})
	if err != nil {
		return nil, err
	}

	var reply Reply
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &reply); err != nil {
		// Rather than fail the turn, fall back to speaking the raw text. A
		// conversation that dies on a formatting slip is worse than one that
		// occasionally speaks something unstructured — the person is mid-sentence.
		reply = Reply{Speech: strings.TrimSpace(resp.Content)}
		if reply.Speech == "" {
			return nil, errs.Wrap(op, errs.CodeExternalProtocol, err).
				WithDetail("the model returned neither usable JSON nor any text")
		}
	}
	reply.Model = resp.Model
	reply.Usage = resp.Usage
	reply.LatencyMS = resp.Latency.Milliseconds()

	if err := reply.validate(); err != nil {
		return nil, err
	}
	return &reply, nil
}

// validate enforces the honesty rules on a reply.
//
// It is the single choke point both the buffered and the streamed path go
// through, which is why the standards scan lives here rather than in either
// caller: a rule enforced in one of two paths is a rule that holds until someone
// uses the other one.
func (r *Reply) validate() error {
	const op = "agent.Reply.validate"

	if strings.TrimSpace(r.Speech) == "" && strings.TrimSpace(r.Detail) == "" {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the reply carried nothing to say or show")
	}

	// Computed before the prototype fix-ups below, so a claim is found in the
	// text the model actually produced.
	r.Recalled = FindStandardsClaims(r)
	r.Claims = r.ClaimLedger()
	if r.Prototype != nil {
		if len(r.Prototype.Parts) == 0 {
			// An empty prototype renders as a blank viewport, which reads as a
			// failure. Dropping it is more honest than showing nothing.
			r.Prototype = nil
			return nil
		}
		// PRD VIS-06 as an invariant rather than an instruction: geometry
		// without a statement of what it does not establish is exactly the
		// render that gets mistaken for an analysis.
		if len(r.Prototype.NotVerified) == 0 {
			r.Prototype.NotVerified = []string{
				"Nothing here has been analysed or checked. There is no CAD kernel, " +
					"solver, or interference check in this deployment — this is a shape, not a result.",
			}
		}
		for i := range r.Prototype.Parts {
			p := &r.Prototype.Parts[i]
			if p.ID == "" {
				p.ID = fmt.Sprintf("part-%d", i+1)
			}
			if len(p.Position) != 3 {
				p.Position = []float64{0, 0, 0}
			}
			if len(p.Rotation) != 3 {
				p.Rotation = []float64{0, 0, 0}
			}
			if p.Opacity <= 0 || p.Opacity > 1 {
				p.Opacity = 1
			}
			if p.Color == "" {
				p.Color = "#b8bcc4"
			}
		}
	}
	return nil
}

// elapsedMS reports milliseconds since the turn's start instant, which is
// carried on the context by the HTTP layer.
//
// On the context rather than a field because a Conversation is shared across
// concurrent turns; a start time stored on the struct would be whichever
// request wrote it last.
func elapsedMS(ctx context.Context) int64 {
	if start, ok := ctx.Value(turnStartKey{}).(time.Time); ok {
		return time.Since(start).Milliseconds()
	}
	return 0
}

type turnStartKey struct{}

// WithTurnStart marks when a conversational turn began, so latency is measured
// from the moment the person finished speaking rather than from the model call.
func WithTurnStart(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, turnStartKey{}, at)
}
