package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// The vision check looks at sub-assemblies on their own as well as the whole
// model. Phase 5, stage V4 of docs/plan-2026-09-13-millions-of-parts.md.

// lookStub is a vision model that records every picture it is shown and what it
// was told, and answers each prompt as the test decides.
type lookStub struct {
	prompts []string
	images  []string
	answer  func(prompt string) string
}

func (s *lookStub) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	prompt := ""
	for _, m := range req.Messages {
		if m.Role == llm.User {
			prompt = m.Content
			if len(m.Images) > 0 {
				s.prompts = append(s.prompts, m.Content)
				s.images = append(s.images, m.Images[0])
			}
		}
	}
	body := `{"problems":[]}`
	if s.answer != nil {
		body = s.answer(prompt)
	}
	return &llm.Response{Content: body, FinishReason: "stop"}, nil
}

func (s *lookStub) ModelFor(llm.Role) string { return "look-stub" }

func block(id, name string) geometry.Part {
	return geometry.Part{ID: id, Name: name, Shape: "box",
		Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}
}

// twoPartAssembly is an assembly of two blocks side by side.
func twoPartAssembly(id, first, second string) ([]geometry.Part, geometry.Assembly) {
	a, b := block(id+"-a", first), block(id+"-b", second)
	return []geometry.Part{a, b}, geometry.Assembly{ID: id, Children: []geometry.Child{
		{ID: "a", Ref: a.ID}, {ID: "b", Ref: b.ID, Position: []float64{20, 0, 0}},
	}}
}

// car is a body, a wheel placed four times, and an antenna that is a single part.
func car() *Prototype {
	wheelDefs, wheel := twoPartAssembly("wheel", "Tyre", "Hub")
	bodyDefs, body := twoPartAssembly("body", "Shell", "Seat")
	antenna := block("antenna", "Antenna")
	defs := append(append(wheelDefs, bodyDefs...), antenna)
	root := geometry.Assembly{ID: "car", Children: []geometry.Child{
		{ID: "body", Ref: "body"},
		{ID: "fl", Ref: "wheel", Position: []float64{0, 0, 100}},
		{ID: "fr", Ref: "wheel", Position: []float64{100, 0, 100}},
		{ID: "rl", Ref: "wheel", Position: []float64{0, 0, -100}},
		{ID: "rr", Ref: "wheel", Position: []float64{100, 0, -100}},
		{ID: "antenna", Ref: "antenna", Position: []float64{50, 50, 0}},
	}}
	return &geometry.Document{Name: "car", Units: "mm", Root: "car", Definitions: defs,
		Assemblies: []geometry.Assembly{body, wheel, root}}
}

// sixAssemblies places six different two-part assemblies, a to f.
func sixAssemblies() *Prototype {
	doc := &geometry.Document{Name: "rack", Units: "mm", Root: "rack"}
	root := geometry.Assembly{ID: "rack"}
	for i, id := range []string{"a", "b", "c", "d", "e", "f"} {
		defs, asm := twoPartAssembly(id, strings.ToUpper(id)+" left", strings.ToUpper(id)+" right")
		doc.Definitions = append(doc.Definitions, defs...)
		doc.Assemblies = append(doc.Assemblies, asm)
		root.Children = append(root.Children, geometry.Child{ID: "at-" + id, Ref: id, Position: []float64{float64(100 * i), 0, 0}})
	}
	doc.Assemblies = append(doc.Assemblies, root)
	return doc
}

func occurrencePrompts(prompts []string) []string {
	var out []string
	for _, p := range prompts {
		if strings.Contains(p, "an occurrence of") {
			out = append(out, p)
		}
	}
	return out
}

// Four wheels are one wheel to look at, and an antenna of one part is seen well
// enough in the whole picture: the whole model, the body, one wheel.
func TestLook_LooksAtEachDistinctSubAssemblyOnce(t *testing.T) {
	stub := &lookStub{}
	c := &Conversation{client: stub}
	doc := car()

	if _, _, err := c.look(context.Background(), doc, "a car", c.render(context.Background(), doc)); err != nil {
		t.Fatal(err)
	}
	subs := occurrencePrompts(stub.prompts)
	if len(stub.prompts) != 3 || len(subs) != 2 {
		t.Fatalf("%d picture(s), %d of them sub-assemblies; want the whole car, the body and one wheel\n%q",
			len(stub.prompts), len(subs), stub.prompts)
	}
	var wheel string
	for _, p := range subs {
		if strings.Contains(p, "an occurrence of wheel") {
			wheel = p
		}
	}
	if wheel == "" {
		t.Fatalf("no wheel was looked at on its own: %q", subs)
	}
	// One occurrence drawn, not all four stacked into one picture.
	if n := strings.Count(wheel, "Tyre"); n != 1 {
		t.Errorf("the wheel's picture names the tyre %d times; it is one wheel\n%s", n, wheel)
	}
	for i := 1; i < len(stub.images); i++ {
		if stub.images[i] == stub.images[0] {
			t.Errorf("picture %d is the whole model again, not a sub-assembly on its own", i)
		}
	}
}

// At most maxSubAssemblyLooks are looked at closely, and the turn says how many of
// how many.
func TestLook_CapsSubAssemblyLooksAndSaysHowMany(t *testing.T) {
	stub := &lookStub{}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: sixAssemblies()}
	sheet := c.render(context.Background(), reply.Prototype)

	c.repairIfItLooksWrong(context.Background(), reply, "a rack", &sheet)

	if subs := occurrencePrompts(stub.prompts); len(subs) != maxSubAssemblyLooks {
		t.Errorf("%d sub-assemblies looked at; the cap is %d", len(subs), maxSubAssemblyLooks)
	}
	if !strings.Contains(reply.Repaired, "looked closely at 4 of 6 sub-assemblies") {
		t.Errorf("the turn does not say how closely it looked: %q", reply.Repaired)
	}
}

// A sub-assembly the kernel found sharing material is one of those looked at,
// wherever it falls in the document.
func TestLook_AClashingSubAssemblyIsLookedAtFirst(t *testing.T) {
	stub := &lookStub{}
	c := &Conversation{client: stub}
	doc := sixAssemblies()
	sheet := c.render(context.Background(), doc)
	sheet.Interferences = []geometry.Interference{{A: "at-f/a", B: "at-f/b", Volume: 10, Fraction: 0.1}}

	if _, _, err := c.look(context.Background(), doc, "a rack", sheet); err != nil {
		t.Fatal(err)
	}
	subs := occurrencePrompts(stub.prompts)
	if len(subs) == 0 || !strings.Contains(subs[0], "an occurrence of f") {
		t.Errorf("the sub-assembly with a clash was not looked at first: %q", subs)
	}
}

// A flat document has no sub-assemblies: one look, as before.
func TestLook_AFlatDocumentGetsOneLook(t *testing.T) {
	stub := &lookStub{}
	c := &Conversation{client: stub}
	doc := twoBoxes()

	_, covered, err := c.look(context.Background(), doc, "an engine", c.render(context.Background(), doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.prompts) != 1 || covered.Of != 0 {
		t.Errorf("a flat document drew %d picture(s) and counted %d sub-assemblies; want 1 and 0",
			len(stub.prompts), covered.Of)
	}
}

// What was seen in a sub-assembly on its own says which one.
func TestLook_ASubAssemblyProblemSaysWhereItWasSeen(t *testing.T) {
	stub := &lookStub{answer: func(prompt string) string {
		if strings.Contains(prompt, "an occurrence of body") {
			return `{"problems":[{"part":"Seat","detail":"is hidden inside the shell"}]}`
		}
		return `{"problems":[]}`
	}}
	c := &Conversation{client: stub}
	doc := car()

	problems, _, err := c.look(context.Background(), doc, "a car", c.render(context.Background(), doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.HasPrefix(problems[0].Detail, "in body: ") {
		t.Errorf("problems %+v; want one, saying it was seen in body", problems)
	}
}

// A tree keeps its parts in definitions, so the prompt names the placed parts.
// Before stage V4 it named doc.Parts, which a tree-only design leaves empty.
func TestLook_ATreesPartsAreNamedInThePrompt(t *testing.T) {
	stub := &lookStub{}
	c := &Conversation{client: stub}
	doc := car()

	if _, _, err := c.look(context.Background(), doc, "a car", c.render(context.Background(), doc)); err != nil {
		t.Fatal(err)
	}
	if len(stub.prompts) == 0 || !strings.Contains(stub.prompts[0], "Tyre") || !strings.Contains(stub.prompts[0], "Antenna") {
		t.Errorf("the whole model's prompt does not name the tree's parts: %q", stub.prompts)
	}
}
