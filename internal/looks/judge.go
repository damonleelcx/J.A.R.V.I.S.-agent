// Package looks is the styling gate: the ONLY place in FORGE where a model is
// asked whether something looks better.
//
// # Why this is a package of its own, and not a prompt in internal/agent
//
// damon, 2026-09-18: "looks designed" is a FORGE goal — FORGE's output should
// read as a designed product, not boxes and sticks. The same decision kept the
// defect check CLOSED to styling: `internal/agent/look.go` and `sketch.go` ask
// closed questions with determinate answers, because every open styling
// complaint in testing was wrong and a repair driven by one damaged a good model
// (a car body 1900 wide, 800 tall and 4500 long was called "grossly too short").
// Those two files and the repair loop they feed must never see a styling
// verdict.
//
// A separate prompt inside `internal/agent` would be one careless edit away from
// reaching the repair loop. So the gate is a package `internal/agent` does not
// import at all — `TestTheRepairLoopCannotSeeAStylingVerdict` holds that as a
// structural fact rather than a convention — and it cannot return a repair, a
// document, or a problem the repair loop knows how to act on. The only thing it
// can say is: the new one looks better, worse, or no different.
//
// # Why not internal/eval
//
// `internal/eval` says, in its own package comment, "Nothing grades its own
// homework … there is no model judging a model anywhere in here, and there is no
// place to put one." That rule is right for what eval measures, and this gate
// breaks it on purpose: "does this look like a designed product" has no
// deterministic scorer, and the alternative to a judged verdict is no measurement
// at all. It is kept out of eval so that rule stays true where it was written,
// and everything here is built to make the judgement auditable rather than
// trusted: two asks with the images swapped, a verdict only when they agree, and
// every image kept beside the answer it produced.
package looks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Verdict is the whole vocabulary of this gate.
//
// Not a score, and not a list of problems: a number invites a threshold nobody
// measured, and a list invites a repair. Three words, about one pair of pictures
// of the SAME design change.
type Verdict string

const (
	// Better: the new render looks more like a designed product.
	Better Verdict = "better"
	// Worse: the old one did.
	Worse Verdict = "worse"
	// Same is the ordinary answer, and the one a disagreement collapses to.
	Same Verdict = "no difference"
)

// MaxImagesPerQuestion is the hard limit on how many pictures one question
// carries, and it is two because the question is a comparison of two renders.
//
// # Why this is a refusal and not a guideline
//
// A third picture turns "which of these two" into "here is a gallery, tell me
// what you think" — the open question look.go exists to avoid. It also makes the
// swapped-order re-ask meaningless: with three images there is no single swap,
// so position bias stops being measurable and the agreement rule below becomes
// theatre. askOnce refuses rather than truncating, because a silently dropped
// image is a question nobody asked being answered as if they had.
const MaxImagesPerQuestion = 2

// Side is one end of the change: the render, and what the DEFECT CHECK already
// said about it.
//
// The counts are read from the existing check's output (agent.builtSheet /
// cad.Build) and are never re-judged here — this package has no opinion about
// faults, and asking a vision model to count buried parts is exactly the mistake
// look.go documents.
type Side struct {
	// Shaded is the ordinary render, as a PNG data URI.
	Shaded string
	// Normals is the same view shaded by surface normal, as a PNG data URI, or
	// "" when there is none. See normalsHelp.
	Normals string
	// Faults is len(doc.Faults()): the document's own count.
	Faults int
	// Buried is the kernel's buried total (builtSheet.Buried).
	Buried int
	// Skipped is how many parts the kernel could not build (len(builtSheet.Skipped)).
	Skipped int
}

// Change is one design change to be judged: two renders of the SAME thing.
type Change struct {
	// ID names the change for the audit trail; it becomes a directory name.
	ID string
	// What describes the change in one sentence, for the record. It is NOT sent
	// to the model: told what changed, a judge agrees that it was an improvement.
	What string
	// Before is the old render, After the new one.
	Before, After Side
}

// Round is one ask: two images, in the order they were sent, and what came back.
type Round struct {
	// View is "shaded" or "normals".
	View string
	// NewIsFirst says which position the AFTER render was in. Both values are
	// always asked, and that is the point of this field.
	NewIsFirst bool
	// Images are exactly what was sent, in order. Kept so the audit shows the
	// question, not a reconstruction of it.
	Images []string
	// Raw is the model's answer verbatim, before any reading of it.
	Raw string
	// Said is Raw read as a statement about the NEW render, with the position
	// swap undone.
	Said Verdict
	// Why is the one sentence the model gave, kept for the reader.
	Why string
	// Tokens is what this ask cost.
	Tokens int64
}

// Decision is the gate's answer, and the whole record of how it got there.
type Decision struct {
	Change   Change
	Rounds   []Round
	Verdict  Verdict
	Accepted bool
	// Reason says, in a sentence a person can check against Rounds, why.
	Reason string
	// Agreed is false when the two asks of a view disagreed — position bias,
	// which is a fact about the judge and is reported as one.
	Agreed bool
	// Guard is what the defect check said, and whether it allows an accept.
	Guard GuardResult
	// Tokens is every ask in this decision.
	Tokens int64
}

// GuardResult is the non-negotiable half of the gate.
type GuardResult struct {
	FaultsBefore, FaultsAfter   int
	BuriedBefore, BuriedAfter   int
	SkippedBefore, SkippedAfter int
	// OK is true when none of the three rose.
	OK bool
	// Rose names the ones that did, for the reason line.
	Rose []string
}

// guard reads the defect check's counts. It never asks anything.
//
// # Why "did not increase" and not "is zero"
//
// A styling change is judged against the model it started from, not against
// perfection. Most of the designs this gate will see already have skipped parts
// and buried pairs; requiring zero would refuse every change ever made to a real
// model, and a gate that always says no is a gate nobody runs. What it must
// never do is let a prettier picture pay for a worse model — so equal passes and
// any rise refuses, on the same reasoning as the repair rule in
// agent/interference.go (a repair is accepted only when the buried total falls
// and the found total does not rise).
func guard(c Change) GuardResult {
	g := GuardResult{
		FaultsBefore: c.Before.Faults, FaultsAfter: c.After.Faults,
		BuriedBefore: c.Before.Buried, BuriedAfter: c.After.Buried,
		SkippedBefore: c.Before.Skipped, SkippedAfter: c.After.Skipped,
	}
	if g.FaultsAfter > g.FaultsBefore {
		g.Rose = append(g.Rose, fmt.Sprintf("faults %d→%d", g.FaultsBefore, g.FaultsAfter))
	}
	if g.BuriedAfter > g.BuriedBefore {
		g.Rose = append(g.Rose, fmt.Sprintf("buried pairs %d→%d", g.BuriedBefore, g.BuriedAfter))
	}
	if g.SkippedAfter > g.SkippedBefore {
		g.Rose = append(g.Rose, fmt.Sprintf("skipped parts %d→%d", g.SkippedBefore, g.SkippedAfter))
	}
	g.OK = len(g.Rose) == 0
	return g
}

// judgeSystem asks ONE closed question about two pictures.
//
// It says nothing about what the object is, and nothing about what changed:
// a judge told "the fillets were added" reports that the fillets improved it,
// which measures the sentence rather than the picture. It is also told that
// "same" is an ordinary answer, for the reason look.go gives — a model asked to
// pick a winner will pick one out of two identical images.
const judgeSystem = `You are shown two renders of the SAME object. One is how it was drawn before a
change, the other after. They are labelled only "image 1" and "image 2", and the
order tells you nothing.

Answer ONE question: which of the two looks more like a finished, designed product?

By that we mean: surfaces that flow rather than meeting in raw square corners;
edges that are eased; a material and a light that read as a photograph of a real
object; parts that sit together as one thing rather than as separate blocks.

Do NOT say what should be changed. Do NOT guess what the object is for or whether
it is the right size, the right shape, or well engineered — you cannot see that
and it is checked elsewhere. Do NOT comment on the background, the image size or
the watermark.

If the two look the same, or the difference is too small to matter, answer "same".
That is an ordinary answer and you should not feel obliged to pick a winner.

Return JSON: {"verdict": "image1" | "image2" | "same", "why": "one sentence about what you can see"}`

// Judge is the gate.
type Judge struct {
	// Client places the asks. Only llm.RoleVision is used.
	Client llm.Client
}

// ErrNoJudge is the deliberate absence: a deployment with no vision model has no
// styling gate, and says so rather than reporting every change as acceptable.
var ErrNoJudge = errors.New("this deployment has no vision model, so nothing can judge how it looks")

// ErrTooManyImages is the refusal behind MaxImagesPerQuestion.
var ErrTooManyImages = errors.New("a looks question carries at most two images")

// ErrNothingToCompare is a change with no pair to look at.
var ErrNothingToCompare = errors.New("a looks judgement needs a render of both the old and the new")

// Judge decides one change.
//
// # The order of the asks, and why the normals pair comes second
//
// The shaded pair is asked twice, swapped. If the two answers disagree the
// judgement is a position bias and collapses to Same — no amount of further
// asking makes a biased judge reliable, and paying for two more calls to break a
// tie would be buying the answer we wanted.
//
// The normals pair is asked ONLY when the shaded pair agreed on Better, and only
// when both sides have one. It is a confirmation, so it can turn an accept into
// a refusal but never the other way round: a normals view shows the surface
// itself — a fillet that is really a chamfer, a "smooth" panel that is eight
// flats — which is exactly what a flattering light hides, and it is the one view
// where this gate can catch a change that only LOOKS smoother. Asking it after a
// Worse or a disagreement would spend tokens on an answer that cannot change the
// outcome.
func (j Judge) Judge(ctx context.Context, c Change) (Decision, error) {
	d := Decision{Change: c, Verdict: Same, Guard: guard(c)}
	if j.Client == nil || j.Client.ModelFor(llm.RoleVision) == "" {
		return d, ErrNoJudge
	}
	if c.Before.Shaded == "" || c.After.Shaded == "" {
		return d, ErrNothingToCompare
	}

	agreed, verdict, err := j.askBothWays(ctx, &d, "shaded", c.Before.Shaded, c.After.Shaded)
	if err != nil {
		return d, err
	}
	d.Agreed, d.Verdict = agreed, verdict
	if !agreed {
		d.Reason = "the judge answered differently when the two images were swapped, so it read the " +
			"positions rather than the pictures; that is not a verdict"
		return d, nil
	}
	if verdict == Better && normalsHelp(c) {
		nAgreed, nVerdict, nErr := j.askBothWays(ctx, &d, "normals", c.Before.Normals, c.After.Normals)
		if nErr != nil {
			return d, nErr
		}
		switch {
		case !nAgreed:
			d.Agreed, d.Verdict = false, Same
			d.Reason = "the shaded pair agreed, but the same question about the surface normals " +
				"answered differently when swapped"
			return d, nil
		case nVerdict != Better:
			d.Verdict = Same
			d.Reason = "the shading looks better but the surfaces underneath do not: " +
				"the normals view answered " + string(nVerdict)
			return d, nil
		}
	}

	switch {
	case verdict != Better:
		d.Reason = "the judge preferred the old render both ways round"
		if verdict == Same {
			d.Reason = "the judge saw no difference it could name, both ways round"
		}
	case !d.Guard.OK:
		d.Reason = "the judge preferred the new render both ways round, but the check got worse: " +
			strings.Join(d.Guard.Rose, ", ")
	default:
		d.Accepted = true
		d.Reason = "the judge preferred the new render both ways round, and no fault, buried pair or " +
			"skipped part was added"
	}
	return d, nil
}

// normalsHelp says when the normals pair is worth asking.
//
// Only when BOTH sides have one: comparing a normals view against a shaded one
// asks which picture is prettier, not which surface is better, and it would be
// answered confidently every time.
func normalsHelp(c Change) bool { return c.Before.Normals != "" && c.After.Normals != "" }

// askBothWays puts one view to the judge twice, swapped, and reports whether the
// two answers agree.
func (j Judge) askBothWays(ctx context.Context, d *Decision, view, before, after string) (bool, Verdict, error) {
	var said []Verdict
	for _, newFirst := range []bool{true, false} {
		r, err := j.askOnce(ctx, view, newFirst, before, after)
		if err != nil {
			return false, Same, err
		}
		d.Rounds = append(d.Rounds, r)
		d.Tokens += r.Tokens
		said = append(said, r.Said)
	}
	if said[0] != said[1] {
		return false, Same, nil
	}
	return true, said[0], nil
}

// askOnce is one question: two images and nothing else.
func (j Judge) askOnce(ctx context.Context, view string, newFirst bool, before, after string) (Round, error) {
	first, second := after, before
	if !newFirst {
		first, second = before, after
	}
	r := Round{View: view, NewIsFirst: newFirst, Images: []string{first, second}}
	if len(r.Images) > MaxImagesPerQuestion {
		return r, ErrTooManyImages
	}

	prompt := "Image 1 and image 2, in that order."
	if view == "normals" {
		prompt = "Image 1 and image 2, in that order. These two are shaded by surface direction " +
			"rather than by light: the colour is the way the surface faces, so a smoothly curved " +
			"surface shades smoothly and a row of flats shows as bands."
	}
	resp, err := j.Client.Complete(ctx, llm.Request{
		Role: llm.RoleVision,
		Messages: []llm.Message{
			{Role: llm.System, Content: judgeSystem},
			{Role: llm.User, Content: prompt, Images: r.Images},
		},
		JSONMode: true,
		// Generous for the reason look.go gives: a reasoning model that spends
		// its budget thinking and has nothing left to answer with returns an
		// EMPTY answer, which here would read as "no difference" — a silent
		// miss wearing a successful call's clothes.
		MaxTokens: 1000,
	})
	if err != nil {
		return r, err
	}
	if resp == nil {
		return r, ErrNothingToCompare
	}
	r.Raw = resp.Content
	r.Tokens = resp.Usage.TotalTokens
	if r.Tokens == 0 {
		r.Tokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	}
	said, why := readVerdict(resp.Content)
	r.Why = why
	// Undo the swap: every Said in a Decision is a statement about the NEW render.
	switch {
	case said == firstIsBetter:
		r.Said = betterIf(newFirst)
	case said == secondIsBetter:
		r.Said = betterIf(!newFirst)
	default:
		r.Said = Same
	}
	return r, nil
}

func betterIf(newWasThatOne bool) Verdict {
	if newWasThatOne {
		return Better
	}
	return Worse
}

// The three answers the contract offers, before the swap is undone.
type sideSaid int

const (
	neitherIsBetter sideSaid = iota
	firstIsBetter
	secondIsBetter
)

// readVerdict reads the answer, and treats anything it cannot read as "same".
//
// # Why an unreadable answer is Same and not an error
//
// parseLook in look.go accepts both shapes the provider produces because being
// strict about the shape of an answer buys nothing when the sentence is the
// whole value. Here the opposite risk applies: this gate's job is to REFUSE, and
// the safe reading of "I could not tell what it said" is "no preference", which
// refuses. An error would abandon the second ask and lose the position-bias
// check with it.
func readVerdict(body string) (sideSaid, string) {
	var out struct {
		Verdict string `json:"verdict"`
		Why     string `json:"why"`
	}
	body = strings.TrimSpace(body)
	if i := strings.Index(body, "{"); i > 0 {
		body = body[i:]
	}
	if j := strings.LastIndex(body, "}"); j >= 0 {
		body = body[:j+1]
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return neitherIsBetter, ""
	}
	why := strings.TrimSpace(out.Why)
	switch strings.ToLower(strings.TrimSpace(out.Verdict)) {
	case "image1", "image 1", "1", "first":
		return firstIsBetter, why
	case "image2", "image 2", "2", "second":
		return secondIsBetter, why
	}
	return neitherIsBetter, why
}
