package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// drawStub is an illustrator whose answers a test decides.
type drawStub struct {
	url    string
	model  string
	err    error
	calls  int
	prompt string
}

func (d *drawStub) Draw(_ context.Context, prompt string) (string, error) {
	d.calls++
	d.prompt = prompt
	return d.url, d.err
}
func (d *drawStub) IllustratorModel() string {
	if d.model == "" {
		return "draw-stub"
	}
	return d.model
}

// sketchStub answers each stage of the loop by which question it was asked.
type sketchStub struct {
	drawPrompt string // reply to "should this be drawn"
	form       string // reply to "read this drawing"
	problems   string // reply to "compare these two"
	repaired   string // reply to a geometry repair
	seen       []string
	visionOff  bool
}

func (s *sketchStub) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	var system string
	for _, m := range req.Messages {
		if m.Role == llm.System {
			system = m.Content
		}
		if m.Role == llm.User {
			s.seen = append(s.seen, m.Content)
		}
	}
	switch {
	case strings.Contains(system, "You write the prompt for a reference DRAWING"):
		return &llm.Response{Content: s.drawPrompt, FinishReason: "stop"}, nil
	case strings.Contains(system, "Describe ONLY ITS FORM"):
		return &llm.Response{Content: s.form, FinishReason: "stop"}, nil
	case strings.Contains(system, "The FIRST image is a reference drawing"):
		return &llm.Response{Content: s.problems, FinishReason: "stop"}, nil
	case strings.Contains(system, "correcting geometry"):
		return &llm.Response{Content: s.repaired, FinishReason: "stop"}, nil
	}
	return &llm.Response{Content: oneGearTurn, FinishReason: "stop"}, nil
}

func (s *sketchStub) ModelFor(r llm.Role) string {
	if r == llm.RoleVision && s.visionOff {
		return ""
	}
	return "sketch-stub"
}

func bracketDoc(width float64) *Prototype {
	return &geometry.Document{
		Name: "Bracket", Units: "mm",
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size: map[string]float64{"width": width, "height": 60, "depth": 5}},
		},
	}
}

// The drawing is made BEFORE the geometry, and its form reaches the prompt.
//
// Generating it after the geometry would make it a picture of what was already
// decided, and comparing a model's work against a picture drawn from that same
// work is a check that cannot fail.
func TestSketch_DrawnFirstAndItsFormReachesTheGeometryPrompt(t *testing.T) {
	stub := &sketchStub{
		drawPrompt: `{"draw":true,"prompt":"three panels of an L bracket"}`,
		form:       `{"form":"An L-shaped plate with a slot in the base.","parts":["upright","base"]}`,
	}
	draw := &drawStub{url: "https://example.invalid/ref.png"}
	c := (&Conversation{client: stub}).WithIllustrator(draw)

	_, err := c.Respond(context.Background(), "", nil, "an L bracket", "", nil, nil)
	if err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	if draw.calls != 1 {
		t.Fatalf("the reference was drawn %d times, want 1", draw.calls)
	}
	var geoPrompt string
	for _, m := range stub.seen {
		if strings.Contains(m, "an L bracket") && strings.Contains(m, "reference drawing") {
			geoPrompt = m
		}
	}
	if geoPrompt == "" {
		t.Fatalf("the form never reached the prompt that writes the geometry, so the drawing "+
			"was made and then ignored.\nprompts seen: %q", stub.seen)
	}
	if !strings.Contains(geoPrompt, "L-shaped plate with a slot") {
		t.Errorf("the prompt does not carry what was read out of the drawing:\n%s", geoPrompt)
	}
}

// ‼️ The drawing may never change a number.
//
// # What this closes
//
// Measured against wan2.7-image: asked for a 20-tooth involute spur gear it drew
// 28-30 petal-shaped teeth and no dimensions at all. So "keep working until the
// 3D matches the 2D", taken literally, would repair a correct kernel-verified
// gear into a 28-tooth petal shape.
//
// Two rules stand between that and the model, and this fences both: the
// comparison is told not to report counts or dimensions, and a correction that
// resizes anything is refused whatever it claims to be fixing.
func TestSketch_APictureCannotChangeANumber(t *testing.T) {
	t.Run("the comparison is told not to count or measure", func(t *testing.T) {
		for _, forbidden := range []string{"Do NOT count", "Do NOT comment on dimensions"} {
			if !strings.Contains(sketchMatchSystem, forbidden) {
				t.Errorf("the comparison prompt no longer says %q, so the vision model may "+
					"report a generated picture's tooth count as a defect", forbidden)
			}
		}
		if !strings.Contains(sketchReadSystem, "Do NOT report any number") {
			t.Error("the reading prompt no longer forbids numbers, so a generated picture's " +
				"counts can be written into the geometry prompt as if they were the request")
		}
	})

	t.Run("a resizing correction is refused", func(t *testing.T) {
		// The vision model complains; the repair comes back with the part
		// resized. It must be refused however plausible the complaint.
		resized := bracketDoc(500)
		body := mustJSON(t, resized)
		stub := &sketchStub{
			problems: `{"problems":[{"part":"Plate","detail":"the plate is missing its slot"}]}`,
			repaired: body,
		}
		c := &Conversation{client: stub}

		reply := &Reply{Prototype: bracketDoc(100)}
		sheet := c.render(context.Background(), reply.Prototype)
		c.repairAgainstSketch(context.Background(), reply,
			&Sketch{Image: "https://example.invalid/ref.png", Form: "an L plate"}, &sheet, nil)

		if got := reply.Prototype.Parts[0].Size["width"]; got != 100 {
			t.Errorf("the drawing resized the plate from 100 to %v. It carries no dimensions "+
				"at all, so it can never be the reason a size changed", got)
		}
		if !strings.Contains(reply.Repaired, "could not correct") {
			t.Errorf("a refused correction was not reported. Repaired = %q", reply.Repaired)
		}
	})
}

// A turn that is not about an object costs one cheap call and no picture.
func TestSketch_NotEveryTurnIsDrawn(t *testing.T) {
	stub := &sketchStub{drawPrompt: `{"draw":false,"prompt":""}`}
	draw := &drawStub{url: "https://example.invalid/ref.png"}
	c := (&Conversation{client: stub}).WithIllustrator(draw)

	if s := c.sketchFirst(context.Background(), "can we move the review to Thursday?", nil); s != nil {
		t.Errorf("a scheduling question was drawn: %+v", s)
	}
	if draw.calls != 0 {
		t.Errorf("the image model was called %d times for a turn with no shape in it", draw.calls)
	}
}

// A deployment that cannot READ a drawing does not pay to make one.
//
// Drawing without vision is a picture nobody uses: the form never reaches the
// prompt and the comparison cannot run. Skipped as a pair, so the cost is never
// paid for half a feature.
func TestSketch_NoVisionMeansNoDrawing(t *testing.T) {
	stub := &sketchStub{drawPrompt: `{"draw":true,"prompt":"a bracket"}`, visionOff: true}
	draw := &drawStub{url: "https://example.invalid/ref.png"}
	c := (&Conversation{client: stub}).WithIllustrator(draw)

	if s := c.sketchFirst(context.Background(), "an L bracket", nil); s != nil {
		t.Errorf("a drawing was made that nothing could read: %+v", s)
	}
	if draw.calls != 0 {
		t.Errorf("the image model was called %d times with no vision model to read the result",
			draw.calls)
	}
}

// A drawing that cannot be made leaves the turn exactly as it was.
func TestSketch_AFailedDrawingIsNotAFailedTurn(t *testing.T) {
	stub := &sketchStub{drawPrompt: `{"draw":true,"prompt":"a bracket"}`}
	draw := &drawStub{err: errors.New("the image model could not be reached")}
	c := (&Conversation{client: stub}).WithIllustrator(draw)

	reply, err := c.Respond(context.Background(), "", nil, "an L bracket", "", nil, nil)
	if err != nil {
		t.Fatalf("a drawing that could not be made cost the person their answer: %v", err)
	}
	if reply.Prototype == nil {
		t.Error("the turn produced no geometry when the reference failed")
	}
}

// The comparison sends BOTH pictures, reference first.
//
// The prompt says "the FIRST image is a reference" and "the SECOND is what was
// built". Sending them in the other order, or sending one, would have the vision
// model answer confidently about the wrong pair.
func TestSketch_BothPicturesAreSentInOrder(t *testing.T) {
	var sent []string
	spy := &imageOrderSpy{onImages: func(imgs []string) { sent = imgs }}
	c := &Conversation{client: spy}

	doc100 := bracketDoc(100)
	c.matchesSketch(context.Background(), doc100, &Sketch{Image: "https://example.invalid/ref.png"},
		c.render(context.Background(), doc100))

	if len(sent) != 2 {
		t.Fatalf("the comparison sent %d image(s), want 2 — the reference and the build", len(sent))
	}
	if sent[0] != "https://example.invalid/ref.png" {
		t.Errorf("the reference is not the FIRST image, and the prompt says it is: %q", sent[0])
	}
	if !strings.HasPrefix(sent[1], "data:image/png;base64,") {
		t.Errorf("the second image is not the rendered build: %q", firstLine(sent[1]))
	}
}

type imageOrderSpy struct{ onImages func([]string) }

func (s *imageOrderSpy) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	for _, m := range req.Messages {
		if len(m.Images) > 0 && s.onImages != nil {
			s.onImages(m.Images)
		}
	}
	return &llm.Response{Content: `{"problems":[]}`, FinishReason: "stop"}, nil
}
func (s *imageOrderSpy) ModelFor(llm.Role) string { return "order-spy" }

// Numbers are STRIPPED from what the drawing is read to say.
//
// # What this closes
//
// sketchReadSystem says "Do NOT report any number" in as many words, and the
// FIRST live run of this loop came back with "Two circular holes are drilled
// through the vertical leg" — obeying the letter, ignoring the rule. It agreed
// with the request that time. The same generator draws 28-30 teeth when asked
// for 20, and "twenty-eight teeth around the rim" in the geometry prompt would
// compete with the count the person actually gave.
//
// An instruction a model may ignore is not a guard.
func TestSketch_NumbersAreStrippedFromWhatTheDrawingSays(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Two circular holes are drilled through the leg.",
			"several circular holes are drilled through the leg."},
		{"There are twenty-eight teeth around the rim.",
			"There are several teeth around the rim."},
		{"A plate 8mm thick with 4 ribs.", "A plate several thick with several ribs."},
		{"An L-shaped bracket with a slot in the base.",
			"An L-shaped bracket with a slot in the base."},
	}
	for _, tc := range cases {
		if got := withoutNumbers(tc.in); got != tc.want {
			t.Errorf("withoutNumbers(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
		}
	}
}

// The comparison is told that a cut tool is drawn as a solid.
//
// # What this closes
//
// geometry.Tessellate does not perform a cut — its own inferences say so: "the
// material X removes is NOT removed in this file", "four solid POSTS standing on
// the plate". So in the rendered build every bolt hole is a peg.
//
// The live test found this unprompted on the first fixture that used a cut: "the
// drawing shows recessed holes (concentric circles) on the vertical arm, but the
// built model has protruding pins or pegs sticking out instead." Right about the
// picture, wrong about the model, and it would fire on every mechanical part
// with a hole in it.
func TestSketch_TheComparisonIsToldACutIsDrawnAsASolid(t *testing.T) {
	var said string
	spy := &promptTextSpy{onText: func(s string) { said = s }}
	c := &Conversation{client: spy}

	doc := bracketDoc(100)
	doc.Parts = append(doc.Parts, geometry.Part{ID: "hole", Name: "Bolt hole", Shape: "cylinder",
		Size: map[string]float64{"radius": 4, "height": 20}})
	doc.Features = []geometry.Feature{{ID: "drill", Op: "cut", Of: "plate", With: []string{"hole"}}}

	c.matchesSketch(context.Background(), doc, &Sketch{Image: "https://example.invalid/ref.png"}, c.render(context.Background(), doc))

	if !strings.Contains(said, "Bolt hole") || !strings.Contains(said, "TOOLS that cut") {
		t.Errorf("the comparison was not told that the bolt hole is drawn as a solid, so it "+
			"will report a peg on every part with a hole in it:\n%s", said)
	}

	// And a document with no cut says nothing about tools — a note about a
	// feature that is not there is noise that costs attention on every turn.
	said = ""
	c.matchesSketch(context.Background(), bracketDoc(100), &Sketch{Image: "https://example.invalid/ref.png"}, c.render(context.Background(), bracketDoc(100)))
	if strings.Contains(said, "TOOLS that cut") {
		t.Errorf("a document with no cut was told about cutting tools:\n%s", said)
	}
}

type promptTextSpy struct{ onText func(string) }

func (s *promptTextSpy) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	for _, m := range req.Messages {
		if m.Role == llm.User && s.onText != nil {
			s.onText(m.Content)
		}
	}
	return &llm.Response{Content: `{"problems":[]}`, FinishReason: "stop"}, nil
}
func (s *promptTextSpy) ModelFor(llm.Role) string { return "prompt-spy" }

// A complaint about a COUNT is dropped, whatever the prompt asked for.
//
// # What this closes
//
// sketchMatchSystem says "Do NOT count anything, and do NOT report a count as
// wrong". The live run said: "The drawing shows three bolt holes arranged
// vertically, but the built model has only two." The drawing had drawn three,
// the person had asked for two, and the built model was right. Acted on, that
// adds a hole nobody asked for.
//
// Fourth time in this feature that a rule stated in a prompt was not obeyed. A
// prompt states an intention; a filter enforces it.
func TestSketch_ComplaintsAboutCountsAreDropped(t *testing.T) {
	in := []geometry.Problem{
		{Severity: geometry.Error, Name: "arm",
			Detail: "The drawing shows three bolt holes, but the built model has only two."},
		{Severity: geometry.Error, Name: "base",
			Detail: "The elongated slot shown in the drawing is entirely absent from the built model."},
		{Severity: geometry.Error, Name: "plate",
			Detail: "The plate is 40mm wide in the drawing and much wider in the model."},
		{Severity: geometry.Error, Name: "upright",
			Detail: "The upright is lying flat instead of standing vertically."},
	}
	got := formOnly(in)
	if len(got) != 2 {
		t.Fatalf("kept %d complaint(s), want 2 — the count and the dimension must both go:\n%+v",
			len(got), got)
	}
	for _, p := range got {
		if strings.Contains(p.Detail, "three") || strings.Contains(p.Detail, "40mm") {
			t.Errorf("a complaint about a number survived: %q", p.Detail)
		}
	}
	if !strings.Contains(got[0].Detail, "slot") || !strings.Contains(got[1].Detail, "lying flat") {
		t.Errorf("a complaint about FORM was dropped, and those are the only actionable "+
			"ones this check produces:\n%+v", got)
	}
}

// The filter is actually WIRED into the comparison.
//
// TestSketch_ComplaintsAboutCountsAreDropped calls formOnly directly, so it
// stays green if the call site is deleted — a drill proved exactly that. A
// filter nothing calls is a filter nobody has.
func TestSketch_TheComparisonActuallyFiltersCounts(t *testing.T) {
	stub := &sketchStub{
		problems: `{"problems":[{"part":"arm","detail":"The drawing shows three bolt holes, but the built model has only two."}]}`,
	}
	c := &Conversation{client: stub}
	doc := bracketDoc(100)
	got := c.matchesSketch(context.Background(), doc,
		&Sketch{Image: "https://example.invalid/ref.png"}, c.render(context.Background(), doc))
	if len(got) != 0 {
		t.Errorf("the comparison passed a complaint about a COUNT back to the repair loop. "+
			"The drawing drew three, the person asked for two, and acting on it adds a hole "+
			"nobody asked for:\n%+v", got)
	}
}

// look() is told which parts are cutting tools, for the reason the comparison is.
//
// # What this closes
//
// geometry.Tessellate does not perform a cut, so a bolt hole is drawn as a solid
// cylinder inside the plate — which is question 1 of lookSystem word for word,
// "a part completely hidden inside another part". Confirmed live before it was
// fixed, on a plate with one bolt hole: "Bolt Hole: The part is a solid cylinder
// protruding from the plate surface rather than a hole passing through it." True
// about the picture, false about the model, and it fires on every mechanical
// part with a hole in it.
//
// This is a UNIT fence on the wiring. Whether the vision model then behaves is
// TestLiveLook's job, and it carries both cases: the hole that is cut correctly
// and the tool that misses.
func TestLook_IsToldWhichPartsAreCuttingTools(t *testing.T) {
	spy := &visionPromptSpy{}
	c := &Conversation{client: spy}

	doc := bracketDoc(100)
	doc.Parts = append(doc.Parts, geometry.Part{ID: "hole", Name: "Bolt Hole", Shape: "cylinder",
		Size: map[string]float64{"radius": 5, "height": 30}})
	doc.Features = []geometry.Feature{{ID: "drill", Op: "cut", Of: "plate", With: []string{"hole"}}}

	if _, err := c.look(context.Background(), doc, "a plate with a bolt hole", c.render(context.Background(), doc)); err != nil {
		t.Fatalf("look failed: %v", err)
	}
	if !strings.Contains(spy.prompt, "Bolt Hole") || !strings.Contains(spy.prompt, "TOOLS that cut") {
		t.Errorf("look was not told the bolt hole is a cutting tool drawn as a solid, so it "+
			"will report a hole as a defect on every part that has one:\n%s", spy.prompt)
	}
	// ‼️ And it must NOT have been silenced wholesale. A tool floating clear of
	// what it cuts removes nothing, and that is a real defect only this check
	// would notice — it found exactly that on the live deployment.
	if !strings.Contains(spy.prompt, "floating clear") {
		t.Errorf("look was told to ignore cutting tools entirely. A tool that misses cuts "+
			"nothing, and suppressing the false positive must not take that with it:\n%s",
			spy.prompt)
	}

	// A document with no cut says nothing about tools.
	spy.prompt = ""
	plain := bracketDoc(100)
	if _, err := c.look(context.Background(), plain, "a plate",
		c.render(context.Background(), plain)); err != nil {
		t.Fatalf("look failed: %v", err)
	}
	if strings.Contains(spy.prompt, "TOOLS that cut") {
		t.Errorf("a document with no cut was told about cutting tools:\n%s", spy.prompt)
	}
}
