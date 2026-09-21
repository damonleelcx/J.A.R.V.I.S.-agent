package geometry

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The curved-outline vocabulary, taught from the table it is enforced from.
//
// # Why this file exists (B3, 2026-09-18)
//
// damon, 2026-09-18: "looks designed" is now a FORGE goal — output should look
// like a designed product with flowing surfaces, not boxes and sticks. A body
// that bulges is the first thing that needs, and the contract said the opposite:
// the "radius" paragraph ended "a crescent, a lens, a bulged edge. There is no
// vocabulary for those here." — two paragraphs after the "via" paragraph that is
// exactly that vocabulary (wave 29, curve.go). A model reading the prompt top to
// bottom was told both, and the last word was "no".
//
// So the via paragraph moved here, next to the rules, and the contract prints it
// with CurveGuide(). Every refusal the validator writes about a bowed edge is a
// row of curveRules below; resolveVia, resolveArcs and the flattened
// self-intersection check write the row's refusal, and the guide teaches the
// row's sentence. A rule added here is taught the day it is enforced, and a fence
// (TestCurveGuide_TeachesEveryRuleTheValidatorEnforces) triggers each row and
// finds its refusal in the notes and its sentence in the guide.
//
// # Why arcs and not splines
//
// The brief allowed a DXF bulge or a spline through control points. Neither is
// added. An arc through three points is what OCCT builds exactly (Geom_Circle),
// what STEP carries exactly (CIRCLE on an extrusion, a true surface on a loft),
// what Go measures exactly (overlay.go, exactProfileExtent) and what the browser
// already draws from the same arithmetic (TestRendererBowsTheSameOutlineAsTheExporter).
// A spline would need an interpolation both sides agree on and a Go measurement
// that is only ever approximate; a sagitta "bulge" is the same arc spelled a
// second way. A model already writes "the middle of the bulge is here" reliably,
// and chained arcs are enough to shape a fender station, which is what a loft
// needs to bulge.

// curveRule is one thing the validator says about a bowed edge, and the sentence
// the contract says it with.
type curveRule struct {
	// refusal is written into the part's notes by the validator. It is a
	// substring of the whole message, so the fence can look for it.
	refusal string
	// teach is the contract's sentence about the same rule.
	teach string
}

var (
	ruleViaOfVia = curveRule{
		refusal: "its via carries a via of its own; an arc passes through one point, and a via " +
			"is that point rather than another edge",
		teach: "A via is one point. It must not carry a via of its own — refused.",
	}
	ruleViaRadius = curveRule{
		refusal: "its via carries a corner radius; a via is a point the edge passes THROUGH, " +
			"not a corner, so there is nothing there to round",
		teach: "A via must not carry a radius: it is a point the edge passes through, not a " +
			"corner — refused.",
	}
	ruleViaZ = curveRule{
		refusal: "its via carries a z; the arc lies in the same plane as the drawing it bends",
		teach: "A via on an outline or a hole must not carry a z: the arc lies in the drawing's " +
			"plane — refused. (A via on a sweep PATH may.)",
	}
	ruleViaNoArc = curveRule{
		refusal: "which names no arc — it is in line with the two ends, or on top of one of them",
		teach: "A via in line with the two ends of its edge, or on top of one of them, names no " +
			"circle — the edge is drawn straight and that is reported.",
	}
	ruleBulgeCrosses = curveRule{
		refusal: "crosses itself once its arcs are drawn",
		teach: "A via that bows its edge ACROSS another edge makes an outline that crosses " +
			"itself, even when the points do not — refused. Move the via closer to the line " +
			"between the two ends.",
	}
	ruleBulgeEmpty = curveRule{
		refusal: "encloses no area once its arcs are drawn",
		teach:   "Two points whose two arcs coincide enclose nothing — refused.",
	}
)

// curveRules is every row, in the order the guide teaches them.
var curveRules = []curveRule{
	ruleViaOfVia, ruleViaRadius, ruleViaZ, ruleViaNoArc, ruleBulgeCrosses, ruleBulgeEmpty,
}

// curveExample is a drawing the guide shows, which a fence builds exactly as
// printed: an example the validator refuses would teach a model to be refused.
type curveExample struct {
	name, use string
	profile   []Point
}

func viaPt(x, y, vx, vy float64) Point { return Point{X: x, Y: y, Via: &Point{X: vx, Y: vy}} }

// curveExamples are the three shapes the old contract said it could not say.
var curveExamples = []curveExample{
	{
		name: "A LENS",
		use: "two arcs bowing opposite ways between the same two points — a lens, an eye, " +
			"a leaf, an aerofoil's thickness",
		profile: []Point{viaPt(-20, 0, 0, -8), viaPt(20, 0, 0, 8)},
	},
	{
		name:    "A CRESCENT",
		use:     "two arcs bowing the SAME way, the outer deeper than the inner",
		profile: []Point{viaPt(-20, 0, 0, 6), viaPt(20, 0, 0, 14)},
	},
	{
		name: "A BULGED FENDER STATION",
		use: "a \"section\" for a car body's loft: flat underneath, the flanks bowing out " +
			"over the wheels and the top crowned. Loft a plain rectangle into it and the " +
			"body swells",
		profile: []Point{
			viaPt(-800, 0, -880, 250), {X: 800, Y: 0},
			viaPt(800, 500, 880, 250), viaPt(-800, 500, 0, 620),
		},
	},
}

// CurveGuide is the contract's paragraph on bowed edges.
//
// Printed into the prompt by internal/agent (converse.go) rather than typed
// there, so the rules it states are the rows the validator refuses with.
func CurveGuide() string {
	var b strings.Builder
	b.WriteString(`- "via" on a point BENDS THE EDGE ARRIVING AT IT into a circular arc that passes
  through the via on the way. Use it for an edge that BOWS: a crescent, a lens, a
  bulged flank, a crowned roof line, a cam lobe, a hook, a D-shaped shaft, the
  belly of a bracket that clears something. It works on an outline point, a hole
  point and a path point, and on a path it curves the run itself rather than only
  its corner.
  It is a POINT ON THE ARC, not a centre and not a direction. Three points fix a
  circle completely, so put the via where the middle of the bulge should be and
  the arc follows. How far it sits off the straight line between the two ends is
  how far the edge bows: a chord c bowed by s is an arc of radius (c²/4 + s²)/2s.
  The arc is EXACT: the solid, the exported STEP and every measurement carry a
  true circle, not facets.
  A "via" and a "radius" are different things and are not alternatives. A radius
  ROUNDS A CORNER between two straight edges; a via CURVES AN EDGE. A corner
  where an arc meets is left sharp — the radius there is ignored and reported —
  so do not put one on either end of a bowed edge.
  Two arcs between the same two points is a legitimate outline of TWO points: an
  outline needs three points only when every edge is straight.
  A "section" may bow like any outline, and that is how a LOFT BULGES: the blend
  passes through each station's arcs exactly. Stations may have different numbers
  of points — the kernel pairs their edges up — but a body reads best when every
  station is drawn with the same points, in the same order, starting at the same
  place.
`)
	b.WriteString("  Each of these is accepted exactly as written:\n")
	for _, ex := range curveExamples {
		raw, err := json.Marshal(ex.profile)
		if err != nil {
			// A Point always marshals; a panic here is a programming error
			// caught by every test that builds the contract.
			panic(fmt.Sprintf("curve example %q: %v", ex.name, err))
		}
		fmt.Fprintf(&b, "    %s, %s:\n      \"profile\": %s\n", ex.name, ex.use, raw)
	}
	b.WriteString("  What is refused or ignored, by name:\n")
	for _, r := range curveRules {
		fmt.Fprintf(&b, "    %s\n", r.teach)
	}
	return strings.TrimRight(b.String(), "\n")
}
