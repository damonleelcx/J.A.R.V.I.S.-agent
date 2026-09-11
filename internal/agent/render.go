package agent

import (
	"context"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Drawing the solid that was actually BUILT, not the one that was described.
//
// # The defect this closes, twice over
//
// Every check that looks at a picture — look.go and sketch.go — was reading a
// render of the DOCUMENT. geometry.Tessellate is a triangle builder: it performs
// no boolean and runs no script, and says so in its own inferences ("the material
// X removes is NOT removed in this file", "four solid POSTS standing on the
// plate"). So two whole classes of part arrived at the vision model as something
// they are not:
//
//   - a cut tool is drawn as a SOLID. Measured live: "Bolt Hole: The part is a
//     solid cylinder protruding from the plate surface rather than a hole passing
//     through it" — about a correct plate. It fired on every part with a hole.
//   - a scripted part is drawn as a BOUNDING BOX. Measured live: "the part is a
//     rectangular block rather than a circular gear with teeth and a bore" —
//     about a gear whose script had just built a correct 12565.7 mm³ solid.
//
// Both were patched by TELLING the vision model what it was really looking at.
// That worked and it does not scale: it is one apology per blind spot, each one a
// sentence the model may ignore, and a third blind spot would have needed a third.
//
// This removes the blind spots instead. The kernel builds the real surface —
// features applied, scripts run — and hands back triangles, which go through the
// same rasterizer, in the same colours, from the same four viewpoints. What the
// checks look at is then what the person will export.
//
// # Why the fallback is not silent
//
// A deployment with no kernel, or a kernel that cannot build this document, still
// gets a picture: the described one, which is better than no check at all. But
// the two apologies above are only TRUE of that picture — on a kernel render a
// hole really is a hole and a gear really is a gear, and repeating them would
// teach the model to ignore a real defect. So a render says where it came from,
// and the callers ask.
type builtSheet struct {
	// Image is the contact sheet as a PNG data URI, or "" when there is nothing
	// to draw.
	Image string
	// FromKernel is true when this is the solid the kernel built. False means
	// the document's own tessellation, with the holes unfilled and the scripts
	// unrun — which is what the notes in look.go and sketch.go describe.
	FromKernel bool
}

// SolidBuilder builds the real surface of a document.
//
// Narrow on purpose, like ScriptRunner beside it: the agent wants triangles and
// nothing else — not the STEP, not the volume, not the bounds. Those are built
// downstream from the document, which stays the single source of truth, and a
// second copy held here would eventually disagree with it.
//
// nil is a deployment with no kernel, and then every render is the described one.
type SolidBuilder interface {
	// BuildSurface returns one entry per part the kernel actually built. A part
	// it could not build is absent rather than approximated — the caller is
	// drawing a picture of what exists, and inventing a shape for a part that
	// failed would put it in front of a checker as though it were fine.
	BuildSurface(ctx context.Context, doc *Prototype) ([]geometry.RenderPart, error)
}

// render draws the built solid, falling back to the described one.
//
// ‼️ It does NOT return an error. A picture that could not be built is not a
// failed turn: every caller degrades to the described render, which is exactly
// the check that shipped before this existed. Losing the turn over a kernel that
// is busy would be strictly worse than looking at a slightly wrong picture and
// saying which one it is.
func (c *Conversation) render(ctx context.Context, doc *Prototype) builtSheet {
	if doc == nil {
		return builtSheet{}
	}
	if c != nil && c.solids != nil {
		if parts, err := c.solids.BuildSurface(ctx, doc); err == nil && len(parts) > 0 {
			if img := geometry.ContactSheetOf(*doc, parts, sheetSize); img != "" {
				return builtSheet{Image: img, FromKernel: true}
			}
		}
	}
	// The unit is Millimetre here for the reason it always has been: the views
	// fit whatever they are given, so a uniform scale changes nothing anyone can
	// see, and a document with no unit still has to produce a picture.
	return builtSheet{Image: geometry.ContactSheet(*doc, geometry.Millimetre, sheetSize)}
}

// describedRenderNote is what a caller must tell the vision model when the
// picture is the DESCRIBED one — and must not when it is the kernel's.
//
// One function, called from both checks, because these two sentences were
// written separately in look.go and sketch.go and immediately drifted: the same
// blind spot explained twice, in two wordings, either of which could be fixed
// without the other. A render that lies about holes lies about them identically
// wherever it is read.
//
// Empty when there is nothing to apologise for, which is the whole point of the
// kernel path.
func describedRenderNote(doc *Prototype, sheet builtSheet) string {
	if sheet.FromKernel || doc == nil {
		return ""
	}
	note := ""
	if tools := cuttingTools(doc); len(tools) > 0 {
		note += "\n\nThese parts are the TOOLS that cut material away: " + joinLabels(tools) +
			". This picture is drawn from the description rather than built, so it cannot " +
			"perform a cut and draws them as SOLIDS — a bolt hole appears as a solid " +
			"cylinder sitting inside, or poking out of, the part it cuts. That is what a " +
			"correct hole looks like here. Do not report them as hidden inside another " +
			"part, as solid where a hole should be, or as extra material. DO still say if " +
			"one is floating clear of the part it is meant to cut, or lying on an axis that " +
			"would not pass through it: a tool that misses removes nothing, and then the " +
			"hole really is absent."
	}
	if scripted := scriptedLabels(doc); len(scripted) > 0 {
		note += "\n\nThese parts are built by a script and this picture shows them as a PLAIN " +
			"BLOCK that is not their real shape: " + joinLabels(scripted) +
			". Do not report that they are the wrong shape, or blocky, or not what was " +
			"asked for — you cannot see their shape at all. You may still say whether one " +
			"is buried inside another part or floating clear of everything."
	}
	return note
}

// scriptedLabels names the parts whose shape is a script.
func scriptedLabels(doc *Prototype) []string {
	var out []string
	for _, p := range scriptedParts(doc) {
		out = append(out, p.Label())
	}
	return out
}

func joinLabels(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// RenderForTest exposes the render to the live test in agent_test, and reports
// which picture it is. Two returns rather than the struct, because a test that
// cannot tell a kernel render from a described one cannot check this at all.
func RenderForTest(ctx context.Context, c *Conversation, doc *Prototype) (string, bool) {
	s := c.render(ctx, doc)
	return s.Image, s.FromKernel
}
