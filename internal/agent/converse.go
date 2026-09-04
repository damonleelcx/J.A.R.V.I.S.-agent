package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/text"
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
//
// A var rather than a const because the finish list is spliced in from
// geometry.FinishGuide(). Repeating those names here would put the closed set in
// two places, and the copy in a prompt is the one that silently goes stale — the
// model would keep offering a finish the viewer stopped drawing.
var converseFraming = `You are in CONVERSATION at the workbench. The person is talking to you, probably
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
        "note": "what this part is for",
        "material": null or {"name": "aluminium 6061-T6", "finish": "metal",
                             "how": "observed|retrieved|inferred|assumed", "source": ""}
      }
    ],
    "states": [
      {
        "id": "stable-kebab-id",
        "name": "what this configuration is",
        "hidden": ["part-ids not shown in this state"],
        "offsets": {"part-id": [0, 10, 0]},
        "how": "proposed",
        "note": "what this state is for"
      }
    ],
    "assumptions": ["anything you chose that they did not specify"],
    "not_verified": ["what this render does NOT establish"],
    "overlays": [
      {
        "id": "stable-kebab-id",
        "kind": "dimension" | "datum",
        "label": "what this marks",
        "from": [0,0,0], "to": [0,0,0],
        "value": 42.0, "unit": "mm",
        "tolerance": "ONLY from a drawing or specification — see below",
        "how": "observed" | "retrieved" | "calculated" | "inferred" | "assumed" | "proposed",
        "source": "where it came from",
        "note": "what a reader needs alongside the number"
      }
    ]
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
- Part ids are STABLE ACROSS TURNS. When you revise an assembly, the part that
  was "base-plate" stays "base-plate" — that is what lets somebody put the two
  versions side by side and see what changed rather than two unrelated designs.
  Reuse the id even when the dimensions change; use a new id only for a part
  that was not there before.
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
- "material" is a CLAIM about what a part is made of, not a rendering hint.
  Cost, weight, whether it can be welded and whether it survives the load all
  follow from it. If they told you, label it "observed" and quote them; if you
  chose it because a bracket is usually aluminium, label it "assumed" — that is
  a real answer and it is shown as one. "finish" is only how it catches light:
  ` + geometry.FinishGuide() + `.
- "states" are named configurations: which parts are shown, and where they sit.
  A state with "offsets" says these pieces separate along this path, and
  NOTHING here checks that they can — there is no interference, clearance or
  kinematic test in this deployment. Offer states when somebody is asking how a
  thing goes together or comes apart; do not add an "exploded" state, the
  viewer already has a slider for that.
- "overlays" are engineering marks on the model: dimensions, datums and notes.
  A note is a comment pinned to a point — use it for something about ONE
  feature, not for the general remarks that belong in your reply. Leave it
  out unless somebody GAVE you a figure to mark. You do not need to dimension the
  overall size — FORGE measures the model's own extents itself and draws them,
  labelled as its own arithmetic, so repeating them here adds nothing.
- NEVER invent a TOLERANCE. Nothing about a shape implies one: it is a
  manufacturing decision about process and fit, and the same cylinder is ±0.5 or
  ±0.005 depending on what it does. A tolerance is read as an instruction to a
  machinist, so one you produced from reasoning is the most dangerous number you
  can emit here. Only mark a tolerance somebody gave you, label it "observed" or
  "retrieved", and name the drawing or specification it came from. Anything else
  is removed before the render is drawn and the reader is told you tried.
- "not_verified" is mandatory whenever geometry is present, and it must be
  specific. "Not stress-analysed" and "no interference check was run" are useful;
  "this is a concept" is not. There is no FEA or CAD kernel in this deployment,
  so nothing here has been checked against anything.

About "proposed_goal": offer one only when they have described work they want
DONE, not merely discussed. It is a proposal — nothing runs until they start it.`

// NotVerifiedFallback is what VIS-06's banner says when the model supplied
// nothing of its own.
//
// A named constant rather than a literal at its one call site, because a second
// reader needs it: the evaluation suite measures whether the MODEL wrote
// something specific, and it can only do that by telling the model's own words
// apart from this backstop (internal/eval/scorers.go). Two copies of this
// sentence would drift, and the drift would silently credit the backstop to the
// model — the property would stop being measured with nothing reporting it.
const NotVerifiedFallback = "Nothing here has been analysed or checked. There is no CAD kernel, " +
	"solver, or interference check in this deployment — this is a shape, not a result."

// Conversation is the workbench dialogue.
//
// It is deliberately separate from the planner and the executor. This surface
// reasons WITH someone; those two act. Keeping them apart means a conversation
// cannot accidentally start work, which is the property PRD AGT-04 is protecting
// when it says autonomy is never raised silently.
type Conversation struct {
	client llm.Client
	char   persona.Character
	// characters resolves the project's character (PRD RSN-04). Optional; nil
	// answers every project with the character this conversation was built with.
	characters *CharacterStore
	// domains resolves the project's industry, for its units and vocabulary
	// (PRD §"Domain packs"). Optional; nil answers every project with `general`,
	// which carries no conventions and so asserts nothing about a domain nobody
	// established.
	domains *DomainStore
}

// NewConversation returns the conversational surface.
func NewConversation(client llm.Client, char persona.Character) *Conversation {
	return &Conversation{client: client, char: char}
}

// WithCharacters makes conversation honour the project's critique intensity.
func (c *Conversation) WithCharacters(s *CharacterStore) *Conversation {
	c.characters = s
	return c
}

// WithDomains makes conversation answer in the project's industry — its units,
// its vocabulary, and what a first answer there is expected to establish.
func (c *Conversation) WithDomains(s *DomainStore) *Conversation {
	c.domains = s
	return c
}

// framingFor returns the conversation instruction for a domain.
//
// # Why the conventions go AFTER the framing rather than into it
//
// converseFraming is how FORGE talks — speech short, detail on screen, propose
// geometry for physical things. That is invariant. The pack adds what it must
// get right to be useful HERE: units, vocabulary, what a first answer has to
// establish. Two different kinds of instruction, so they are two blocks, and the
// domain one can be absent without leaving a hole in the other.
//
// `general` carries no conventions by definition — it is the unknown-domain
// pack — so an unstated industry gets exactly the framing this had before packs
// were read at all. That is the property that makes this safe to add: nothing
// changes for a project that never chose a domain.
func framingFor(domain domainpack.Definition) string {
	if strings.TrimSpace(domain.Conventions) == "" {
		return converseFraming
	}
	return converseFraming + "\n\nThis project is " + domain.Industry + " work. " +
		"What that requires of an answer here:\n\n" + domain.Conventions +
		"\n\nThe boundary of this domain: " + domain.Summary
}

// Turn is one exchange, as stored and replayed.
type Turn struct {
	Role    string `json:"role"` // "user" | "forge"
	Content string `json:"content"`
}

// HistoryContent renders one of FORGE's replies as a LATER turn should see it.
//
// # Why both halves reach the next turn
//
// A reply has two: the speech, kept short because it is spoken aloud, and the
// detail, which is where the reasoning goes because the screen can carry it
// (PRD §5.3). Only the speech used to travel, so FORGE could explain a choice at
// length and then, one question later, have no idea why it had chosen it. "Why
// did you say 3mm?" — the most ordinary follow-up there is — was the one thing
// it could not answer about its own answer.
//
// # Why they stay labelled instead of running together
//
// The label is load-bearing, not tidiness. What keeps FORGE's speech short is
// largely its sense of how it spoke last time, and a model whose own previous
// turns arrive as long paragraphs learns that long spoken replies are normal.
// The evaluation suite floors spoken replies at 70 words
// (internal/eval/scorers.go); saying which half was spoken is what keeps that
// floor measuring the same thing after the detail started travelling with it.
//
// ONE producer, called by the workbench (from its record) and by the evaluation
// harness (from the reply it just received). An eval that assembles a turn's
// history differently from the product is measuring a different system.
func HistoryContent(speech, detail string) string {
	speech = strings.TrimSpace(speech)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return speech
	}
	shown := "[Shown on screen with that reply, not spoken aloud: " +
		clipRunes(detail, historyDetailLimit) + "]"
	if speech == "" {
		return shown
	}
	return speech + "\n\n" + shown
}

// historyDetailLimit bounds ONE recorded detail inside a later request.
//
// HistoryWindow bounds how many turns come back; nothing bounded how LONG one of
// them could be, and the record is permanent now — so a single reply carrying a
// long table would ride along in every request for the rest of the conversation.
// Two thousand characters is longer than any detail observed here and short
// enough that a full window of them is a few thousand tokens rather than a
// request the provider refuses.
const historyDetailLimit = 2000

// clipRunes shortens a recorded detail and says it did.
//
// The rule — count characters, cut on a boundary, mark the cut — is
// platform/text.Clip, because it was written here and separately in the ledger
// and one of the two got it wrong. What is local is WHY this caller wants it: a
// detail that quietly loses its ending leaves FORGE reasoning from part of its
// own argument with nothing to indicate the rest existed.
func clipRunes(s string, limit int) string { return text.Clip(s, limit) }

// HistoryWindow is how many earlier turns a conversation carries into a request.
//
// # Why it is exported
//
// The workbench reads a conversation's history out of its own record and hands
// it here, so two places now decide how far back a turn can see. If they
// disagreed, the caller would read a hundred turns for buildMessages to throw
// most of them away — and, worse, would believe it had passed a complete context
// while the model saw a fraction of it. One constant, read by both.
//
// Sixteen is a working figure rather than a measured one. It is enough to hold a
// design discussion together and short enough that a long session does not spend
// its budget re-reading itself.
const HistoryWindow = 16

// Prototype is a proposed 3D form.
//
// An ALIAS rather than a struct of its own: the shape the model emits and the
// shape that gets stored as a variant (PRD VIS-04) must be the same type, or the
// two definitions drift and a replayed render stops matching what was saved.
// It lives in internal/domain/geometry because a domain package cannot import
// the agent to find out what a part is.
type Prototype = geometry.Document

// PrototypePart is one solid.
type PrototypePart = geometry.Part

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
//
// projectID selects the character to answer in (PRD RSN-04). Empty is legal and
// means the constructed default — the evaluation harness has no project, and a
// deployment that never sets a character never needs one.
//
// images are data URIs attached to this turn — a sketch, a photograph of a
// part, a screenshot of a drawing (PRD VIS-01). They route the turn to the
// vision model, and a deployment that has not configured one refuses rather
// than sending a picture to a model that cannot see.
func (c *Conversation) Respond(ctx context.Context, projectID string, history []Turn, message string, workspaceNote string, images []string) (*Reply, error) {
	const op = "agent.Conversation.Respond"

	if strings.TrimSpace(message) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).WithDetail("empty message")
	}
	role := llm.RoleConverse
	if len(images) > 0 {
		// The same discipline as a missing CAD kernel (tools/unavailable.go).
		// Most text models do not fail on an image — they describe their own
		// confusion in confident prose, which is exactly the answer this product
		// exists not to produce. So the absence is reported, never worked around.
		if c.client.ModelFor(llm.RoleVision) == "" {
			return nil, errs.New(op, errs.CodeConnectorUnavailable).
				WithDetail("this deployment has no vision model configured, so FORGE cannot look at " +
					"the image. Set FORGE_LLM_VISION_MODEL to a model that reads images. It is " +
					"deliberately unset by default: a text model sent a picture does not refuse, it " +
					"describes what it imagines, and that answer is indistinguishable from one it saw.")
		}
		role = llm.RoleVision
	}

	// One builder, not two. buildMessages has always said it was "shared by both
	// paths" and this function had its own copy of it — so the streaming path
	// and the buffered path assembled the same request separately, and an image
	// added to one would simply not exist in the other.
	messages := c.buildMessages(c.characters.For(ctx, projectID, c.char),
		c.domains.For(ctx, projectID), history, message, workspaceNote, images)

	resp, err := c.client.Complete(ctx, llm.Request{
		Role:      role,
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
		/* PRD WRK-05: a dimension without its unit will eventually be read in
		 * the wrong one.
		 *
		 * The units field is free text from a model, so it can be missing,
		 * misspelled, or something we cannot convert. An unrecognised unit is NOT
		 * quietly treated as millimetres — a wrong guess about scale is the
		 * difference between a bracket and a building. It is recorded as
		 * unspecified, every dimension then renders as "60 (unit not stated)",
		 * and the reader is told in the one place they are already looking. */
		if _, known := geometry.ParseUnit(r.Prototype.Units); !known && len(r.Prototype.Parts) > 0 {
			declared := strings.TrimSpace(r.Prototype.Units)
			note := "No unit was stated for these dimensions, so every number here is unitless."
			if declared != "" {
				note = fmt.Sprintf("The unit %q is not one FORGE can convert, so every number here is unitless.", declared)
			}
			r.Prototype.Units = ""
			r.Prototype.NotVerified = append(r.Prototype.NotVerified, note)
		}
		// PRD VIS-03. Overlays arrive from the model like everything else here,
		// and a dimension line with a tolerance on it is the most authoritative
		// mark that can appear on a render. The storage door refuses a bad one
		// outright; this door drops it and says so, because refusing the whole
		// turn would throw away the shape somebody is waiting on — the same
		// treatment the unrecognised unit gets above.
		//
		// Appended to NotVerified rather than logged, because that is the one
		// place the reader is already looking, and "FORGE tried to state a
		// tolerance and it was removed" is exactly what they need to know about
		// what is in front of them.
		if len(r.Prototype.Overlays) > 0 {
			kept, dropped := geometry.DrawableOverlays(r.Prototype.Overlays)
			r.Prototype.Overlays = kept
			r.Prototype.NotVerified = append(r.Prototype.NotVerified, dropped...)
		}
		// PRD VIS-02. Materials and states arrive from the model like everything
		// else here. A material with an unusable finish keeps its NAME and loses
		// its look — the name is the claim — and a state referring to a part
		// that does not exist is dropped, because the viewer would show the
		// assembly unchanged and a reader would take that for the state making
		// no difference.
		for i := range r.Prototype.Parts {
			if m := r.Prototype.Parts[i].Material; m != nil {
				if err := m.Validate(); err != nil {
					r.Prototype.Parts[i].Material = nil
					r.Prototype.NotVerified = append(r.Prototype.NotVerified,
						"A material FORGE named could not be read and was dropped: "+err.Error())
				}
			}
		}
		if err := geometry.ValidateStates(r.Prototype.States, r.Prototype.Parts); err != nil {
			r.Prototype.States = nil
			r.Prototype.NotVerified = append(r.Prototype.NotVerified,
				"The assembly states FORGE proposed referred to parts that are not in this "+
					"assembly, so none of them is shown. "+err.Error())
		}
		if note := geometry.StatesNotVerified(r.Prototype.States); note != "" {
			r.Prototype.NotVerified = append(r.Prototype.NotVerified, note)
		}
		// PRD VIS-06 as an invariant rather than an instruction: geometry
		// without a statement of what it does not establish is exactly the
		// render that gets mistaken for an analysis.
		if len(r.Prototype.NotVerified) == 0 {
			r.Prototype.NotVerified = []string{NotVerifiedFallback}
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
