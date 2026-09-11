package geometry

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Gear: an involute spur gear, said in the numbers a gear is specified by.
//
// # Why this is a shape and not a script
//
// Before this, a gear could only be reached through a model-written build123d
// script, and that path plateaued. Names, signatures, operators, usage and
// parameters were each answered in turn, every named failure went away, and the
// rate sat at roughly six builds in ten throughout (docs/plan-2026-09-09-complex-
// prototypes.md, Stages 8–10). What was left was the model's own involute
// geometry — `Standard_TypeMismatch`, `BRep_API: command not done` — which no
// error message can repair, because the model was being asked to DERIVE a curve
// every time somebody wanted a gear.
//
// A spur gear is specified by a handful of numbers. Asking for the numbers and
// drawing the curve here takes the derivation away from the model entirely, and
// it gives back everything a script costs: the panel can read it, a parameter can
// drive it, a revision can change its tooth count without rewriting anything,
// and every check in a turn can see it.
//
// # Why it becomes an extrusion rather than being built
//
// A spur gear IS an extrusion — a constant section carried along its axle. So
// the numbers are turned into that section (flanks, tip and root arcs, bore)
// before anything reads the part, exactly as a repeat is written out before
// anything reads it (repeat.go). The kernel, the mesh exporter, the fault check
// and the measurement path then see an ordinary extrusion, which they already
// build, fence and agree about. The involute is worked out in ONE place in Go;
// sidecar.py has no gear case and must never grow one, or the file and the
// picture would have two opinions about a tooth.
//
// The browser is the one reader that cannot call this, and holds a copy
// (gearOutline in forge3d.js) fenced point for point by
// TestRendererDrawsTheSameGearAsTheExporter. A copy rather than an outline
// written onto the stored part, because a turn's repairs install a replacement
// document without binding it (georepair.go, turned.go, look.go, sketch.go): an
// outline written at bind time would be missing on exactly the turns that were
// repaired, and the viewport would draw those gears as unit boxes.
//
// # What it deliberately does not do
//
// Spur gears with a standard full-depth tooth: addendum 1 module, dedendum 1.25,
// no profile shift, no backlash, a sharp root. Not helical, bevel or internal, not
// a rack, not a worm — those are still scripts. A hub, a keyway and spokes are
// ordinary parts cut from or fused to it. The root fillet a hob leaves is not
// drawn, and neither is the undercut it cuts on a gear with few teeth — which is
// said, because a fuller root than the cut gear has is a claim about strength.

// gearShape is the word, spelled once.
const gearShape = "gear"

const (
	// gearFlankSegments is how many straight segments stand in for each involute
	// flank. Eight keeps a module-2, 20-tooth flank within about 0.01 mm of the
	// true curve — reported per gear in the export notes, not assumed. The
	// browser holds the same count (GEAR in forge3d.js), and the gear fence
	// compares the two drawings point for point.
	gearFlankSegments = 8
	// The standard full-depth tooth: how far a tooth reaches above the pitch
	// circle and below it, in modules.
	gearAddendum = 1.0
	gearDedendum = 1.25
	// defaultPressureAngle is what an absent "pressure_angle" MEANS, in degrees,
	// and the contract says so. Not reported when it is used: the contract tells
	// the model to leave it out when it is 20, so an absence is a statement, and a
	// note that fired on every correctly written gear is a note people stop
	// reading.
	defaultPressureAngle = 20.0
	// maxGearTeeth bounds the drawing for the same reason maxRepeat bounds a
	// pattern — a gear is that many repeated tooth shapes — and reuses its number
	// so the two budgets cannot drift apart.
	maxGearTeeth = maxRepeat
)

// gearSpec is a gear's numbers, every one of them read and checked.
type gearSpec struct {
	Module        float64
	Teeth         int
	PressureAngle float64 // degrees
	Depth         float64 // the face width, along the part's local Z
	BoreRadius    float64 // zero for a solid blank
}

func (g gearSpec) pitchRadius() float64 { return g.Module * float64(g.Teeth) / 2 }
func (g gearSpec) tipRadius() float64   { return g.pitchRadius() + gearAddendum*g.Module }
func (g gearSpec) rootRadius() float64  { return g.pitchRadius() - gearDedendum*g.Module }
func (g gearSpec) baseRadius() float64 {
	return g.pitchRadius() * math.Cos(g.PressureAngle*math.Pi/180)
}

// halfBase is half a tooth's angular thickness where it leaves the base circle.
// At the pitch circle a tooth is exactly half the circular pitch (zero backlash),
// and the involute's own polar angle carries that down to the base circle.
func (g gearSpec) halfBase() float64 {
	return math.Pi/(2*float64(g.Teeth)) + involute(g.PressureAngle*math.Pi/180)
}

// flankStart is where the involute begins: the base circle, or the root circle
// when the roots are outside it. Below the base circle there is no involute, and
// the flank runs straight in to the root.
func (g gearSpec) flankStart() float64 { return math.Max(g.rootRadius(), g.baseRadius()) }

// involute is the polar angle an involute has turned through where its pressure
// angle is a, in radians: inv(a) = tan(a) - a.
func involute(a float64) float64 { return math.Tan(a) - a }

// rollAngle is the involute's parameter at radius r — the angle the string has
// unwound from a base circle of radius base. Its polar angle there is t - atan(t).
func rollAngle(r, base float64) float64 {
	if r <= base {
		return 0
	}
	return math.Sqrt((r/base)*(r/base) - 1)
}

// flankRoll is the roll angle of flank sample i of gearFlankSegments.
func (g gearSpec) flankRoll(i int) float64 {
	base := g.baseRadius()
	tStart := rollAngle(g.flankStart(), base)
	tTip := rollAngle(g.tipRadius(), base)
	return tStart + (tTip-tStart)*float64(i)/gearFlankSegments
}

// isGear reports whether a part is a gear, read the way outlineShapes reads a
// shape word.
func isGear(p Part) bool {
	return strings.EqualFold(strings.TrimSpace(p.Shape), gearShape)
}

// gearSize reads one of a gear's size keys, through the synonyms a model writes
// for it (sizeSynonyms in mesh.go), and says which word it read.
func gearSize(p Part, key string) (value float64, readFrom string, ok bool) {
	if v, has := p.Size[key]; has {
		return v, key, true
	}
	var aliases []string
	for alias, means := range sizeSynonyms[gearShape] {
		if means == key {
			aliases = append(aliases, alias)
		}
	}
	// Sorted, so a part carrying two synonyms is read the same way every time
	// and the same way the browser reads it.
	sort.Strings(aliases)
	for _, alias := range aliases {
		if v, has := p.Size[alias]; has {
			return v, alias, true
		}
	}
	return 0, "", false
}

// readGear reads a gear's numbers and says what is wrong with them.
//
// An ERROR means there is no gear to draw and the part is left out, which is what
// puts it in front of the repair loop (faults.go). A WARNING is something the
// reader is owed and the gear is still drawn.
func readGear(p Part) (gearSpec, []Problem) {
	label := p.Label()
	var problems []Problem
	fail := func(format string, args ...any) {
		problems = append(problems, Problem{Severity: Error, Name: label,
			Detail: fmt.Sprintf(format, args...)})
	}
	note := func(format string, args ...any) {
		problems = append(problems, Problem{Severity: Warning, Name: label,
			Detail: fmt.Sprintf(format, args...)})
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

	var g gearSpec
	module, _, hasModule := gearSize(p, "module")
	teeth, _, hasTeeth := gearSize(p, "teeth")
	switch {
	case !hasModule:
		// Refused rather than defaulted. Every other dimension of a gear follows
		// from its module and its tooth count, so a module FORGE chose is not a
		// guess about one dimension — it is a different gear.
		fail("is a gear with no \"module\"; a gear's whole size follows from its module and its " +
			"number of teeth, so FORGE will not choose one")
	case !finite(module) || module <= 0:
		fail("is a gear with module %g; the module is the size of a tooth and must be a positive length", module)
	}
	switch {
	case !hasTeeth:
		fail("is a gear with no \"teeth\"; say how many it has")
	case !finite(teeth) || math.Abs(teeth-math.Round(teeth)) > 1e-9:
		fail("is a gear with %g teeth; a gear has a whole number of them", teeth)
	case math.Round(teeth) > maxGearTeeth:
		fail("is a gear with %g teeth, and %d is the most this build will draw", teeth, maxGearTeeth)
	}
	if len(problems) > 0 {
		return g, problems
	}
	g.Module, g.Teeth = module, int(math.Round(teeth))

	g.PressureAngle = defaultPressureAngle
	if v, _, has := gearSize(p, "pressure_angle"); has {
		g.PressureAngle = v
	}
	if !finite(g.PressureAngle) || g.PressureAngle <= 0 || g.PressureAngle >= 45 {
		fail("has a pressure angle of %g°; it is in degrees and between 0 and 45 — 20 is the "+
			"common standard, 14.5 and 25 the others in use", g.PressureAngle)
		return g, problems
	}

	g.Depth = 1
	if v, from, has := gearSize(p, "depth"); has {
		g.Depth = v
		if from != "depth" {
			note("gives its face width as %q, which was read as \"depth\" — the only thing it can "+
				"mean on a gear, but a reading rather than what was written", from)
		}
	} else {
		note("has no \"depth\" (its face width), so it is drawn 1 thick. This is a number FORGE " +
			"chose, not one it was told")
	}
	if !finite(g.Depth) || g.Depth <= 0 {
		fail("has a face width (\"depth\") of %g; it must be a positive length", g.Depth)
		return g, problems
	}

	if v, _, has := gearSize(p, "bore_radius"); has {
		g.BoreRadius = v
	}

	// The geometry has to close. Each of these is a gear that cannot exist, named
	// by the number that makes it impossible rather than reported as an outline
	// that crosses itself — which is what the outline check would say, about
	// points the model never wrote.
	root := g.rootRadius()
	switch {
	case root <= 0:
		fail("has %d teeth; with so few, the roots of its teeth reach the centre (the root circle "+
			"is %g tooth depths below the pitch circle), so a gear needs at least 3", g.Teeth, gearDedendum)
	case g.halfBase()-involute(math.Acos(g.baseRadius()/g.tipRadius())) <= 0:
		fail("has teeth that come to a point before they reach the tip circle at a %g° pressure "+
			"angle; use more teeth or a smaller pressure angle", g.PressureAngle)
	case math.Pi/float64(g.Teeth)-(g.halfBase()-flankTurn(g.flankRoll(0))) <= 0:
		fail("has teeth wider than the gaps between them at the root, so neighbouring teeth would " +
			"run into each other")
	case !finite(g.BoreRadius) || g.BoreRadius < 0:
		fail("has a bore radius of %g; leave \"bore_radius\" out for a solid blank", g.BoreRadius)
	case g.BoreRadius >= root:
		fail("has a bore (radius %g) that reaches the roots of its teeth (radius %g), so nothing "+
			"would hold the teeth on", g.BoreRadius, root)
	}
	if len(problems) > 0 && anyError(problems) {
		return g, problems
	}

	// Below this many teeth a cutter removes material from the root that the
	// mating gear's tip would otherwise hit: 2 / sin²(pressure angle), 17.1 at 20°.
	a := g.PressureAngle * math.Pi / 180
	if minTeeth := 2 / (math.Sin(a) * math.Sin(a)); float64(g.Teeth) < minTeeth {
		note("has %d teeth, and a gear with fewer than %.0f at %g° is undercut at the root when it "+
			"is cut. The undercut is not drawn, so these roots are fuller — and look stronger — than "+
			"a cut gear's", g.Teeth, math.Ceil(minTeeth), g.PressureAngle)
	}
	if len(p.Profile) > 0 || len(p.Holes) > 0 || len(p.Path) > 0 {
		note("carries an outline, holes or a path, which a gear does not read: its teeth are " +
			"drawn from its module and tooth count, so what was drawn was ignored")
	}
	return g, problems
}

// flankTurn is how far an involute has turned at roll angle t: its polar angle.
func flankTurn(t float64) float64 { return t - math.Atan(t) }

func anyError(problems []Problem) bool {
	for _, p := range problems {
		if p.Severity == Error {
			return true
		}
	}
	return false
}

// outline is the gear's section in its own XY plane, centred on its axle, with
// tooth 0 pointing up +Y — so a gear drawn facing you, as every outline is, has a
// tooth at the top.
//
// Counter-clockwise, one tooth at a time: in along the root, up the leading
// flank, across the tip, down the trailing flank. The tip and the root are TRUE
// arcs (a via on the point that ends them), so the kernel builds cylindrical
// faces there and only the flanks are faceted. The bore is a circle as two arcs.
//
// ‼️ Mirrored step for step by gearOutline in forge3d.js, down to the order of
// the arithmetic. TestRendererDrawsTheSameGearAsTheExporter compares the two.
func (g gearSpec) outline() (profile []Point, holes [][]Point) {
	base, tip, root := g.baseRadius(), g.tipRadius(), g.rootRadius()
	halfBase := g.halfBase()
	pitch := 2 * math.Pi / float64(g.Teeth)
	at := func(r, angle float64) Point {
		return Point{X: r * math.Cos(angle), Y: r * math.Sin(angle)}
	}
	// Below the base circle there is no involute: the flank runs straight in to
	// the root, along the radius the involute starts from.
	radial := root < base

	profile = make([]Point, 0, g.Teeth*(2*gearFlankSegments+4))
	for k := 0; k < g.Teeth; k++ {
		centre := math.Pi/2 + pitch*float64(k)
		first := len(profile)
		if radial {
			profile = append(profile, at(root, centre-halfBase))
		}
		for i := 0; i <= gearFlankSegments; i++ {
			t := g.flankRoll(i)
			profile = append(profile, at(base*math.Sqrt(1+t*t), centre-(halfBase-flankTurn(t))))
		}
		for i := gearFlankSegments; i >= 0; i-- {
			t := g.flankRoll(i)
			pt := at(base*math.Sqrt(1+t*t), centre+(halfBase-flankTurn(t)))
			if i == gearFlankSegments {
				// The tip land: the edge arriving here, from the other flank's tip.
				via := at(tip, centre)
				pt.Via = &via
			}
			profile = append(profile, pt)
		}
		if radial {
			profile = append(profile, at(root, centre+halfBase))
		}
		// The root land ARRIVING at this tooth, from the one before. On tooth 0
		// that is the closing edge, which is what entry 0 means (curve.go).
		via := at(root, centre-pitch/2)
		profile[first].Via = &via
	}

	if g.BoreRadius > 0 {
		b := g.BoreRadius
		holes = [][]Point{{
			{X: b, Y: 0, Via: &Point{X: 0, Y: -b}},
			{X: -b, Y: 0, Via: &Point{X: 0, Y: b}},
		}}
	}
	return profile, holes
}

// flankDeviation is about how far the straight segments stand off the involute
// they replace, in the gear's own units: the worst of several samples along each
// segment.
func (g gearSpec) flankDeviation() float64 {
	base := g.baseRadius()
	curve := func(t float64) [2]float64 {
		return [2]float64{base * (math.Cos(t) + t*math.Sin(t)), base * (math.Sin(t) - t*math.Cos(t))}
	}
	const samples = 8
	worst := 0.0
	for i := 0; i < gearFlankSegments; i++ {
		t0, t1 := g.flankRoll(i), g.flankRoll(i+1)
		a, b := curve(t0), curve(t1)
		for j := 1; j < samples; j++ {
			worst = math.Max(worst, distanceToSegment(curve(t0+(t1-t0)*float64(j)/samples), a, b))
		}
	}
	return worst
}

func distanceToSegment(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	length := dx*dx + dy*dy
	if length == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	s := math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/length))
	return math.Hypot(p[0]-a[0]-s*dx, p[1]-a[1]-s*dy)
}

// expandGears returns the document with every gear written out as the extrusion
// it is, and says what it could not draw.
//
// Idempotent, like expandRepeats: a document with no gear in it comes back as it
// went in, which is what lets every entry point call it without coordinating.
// Called BEFORE expandRepeats, so a repeated gear is a repeated extrusion.
func expandGears(d Document) (Document, []Problem) {
	need := false
	for _, p := range d.Parts {
		if isGear(p) {
			need = true
			break
		}
	}
	if !need {
		return d, nil
	}

	var problems []Problem
	out := d
	out.Parts = make([]Part, 0, len(d.Parts))
	for _, p := range d.Parts {
		if !isGear(p) {
			out.Parts = append(out.Parts, p)
			continue
		}
		g, found := readGear(p)
		problems = append(problems, found...)
		if anyError(found) {
			// Left out rather than drawn as a box, the same bargain an unreadable
			// outline has: a part missing for a stated reason is something a
			// reader can act on, and a block labelled "gear" is not.
			continue
		}
		q := p
		q.Shape = "extrusion"
		q.Profile, q.Holes = g.outline()
		q.Path, q.PathClosed, q.Axis = nil, false, ""
		q.Size = map[string]float64{"depth": g.Depth}
		// The bindings already did their work (Bind wrote their values into
		// Size), and none of them names a key an extrusion has.
		q.SizeFrom = nil
		out.Parts = append(out.Parts, q)
	}
	return out, problems
}

// gearFacetNotes says, for an exported file, how closely each gear's flanks
// follow the involute.
//
// Only in the export's notes, not among the document's problems: it is true of
// every gear, so as a problem it would fire on every correct document — and a
// STEP file is the one place the number is worth a machinist reading.
func gearFacetNotes(d Document, unit Unit) []string {
	var out []string
	for _, p := range d.Parts {
		if !isGear(p) {
			continue
		}
		g, problems := readGear(p)
		if anyError(problems) {
			continue
		}
		out = append(out, fmt.Sprintf("%s: each tooth flank is %d straight segments standing in "+
			"for the involute, within about %s of it; the tip, the root and the bore are true arcs.",
			p.Label(), gearFlankSegments, NewQuantity(round(g.flankDeviation(), 4), unit)))
	}
	return out
}

// GearOutlineForTest draws a gear from its size keys, for the fence that holds
// the browser's copy of this to the same answer. ok is false when the numbers do
// not describe a gear.
//
// Named for what it is, like FlattenOutlineForTest: nothing in a product path
// should reach for it.
func GearOutlineForTest(size map[string]float64) (profile []Point, holes [][]Point, ok bool) {
	g, problems := readGear(Part{ID: "gear", Shape: gearShape, Size: size})
	if anyError(problems) {
		return nil, nil, false
	}
	profile, holes = g.outline()
	return profile, holes, true
}
