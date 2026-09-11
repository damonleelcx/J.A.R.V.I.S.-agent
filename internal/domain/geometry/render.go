// Package geometry, rendering half: turning a built model into a picture.
//
// # Why a rasterizer here and not the renderer that already exists
//
// The renderer that exists is WebGL, in the reader's browser. The server cannot
// reach it, and the agent needs to see what it built BEFORE the turn is emitted
// — after that the geometry is already on somebody's screen and the damage is
// done. A headless browser would work and costs a second runtime, a container
// three times the size, and a class of failure (the browser did not start) that
// this deployment has no way to notice. The triangles are already in hand from
// Tessellate; drawing them is a page of arithmetic.
//
// This is deliberately NOT a good renderer. It is orthographic, flat-shaded and
// unlit beyond a single fixed direction, because its only reader is a vision
// model being asked structural questions: is the length along the length, is
// anything inside anything else, is a part missing. Shadows, perspective and
// materials would cost time and answer none of those.
package geometry

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
)

// View is one direction to look from.
type View struct {
	Name string
	// Eye is the direction the camera looks ALONG, in assembly space.
	Eye [3]float64
	// Up disambiguates the roll of the image.
	Up [3]float64
}

// StandardViews are the four a person would ask for, and the four that answer
// different questions: front shows width and height, side shows length and
// height, top shows the footprint, and iso shows whether the three agree.
func StandardViews() []View {
	return []View{
		{"front", [3]float64{0, 0, -1}, [3]float64{0, 1, 0}},
		{"side", [3]float64{-1, 0, 0}, [3]float64{0, 1, 0}},
		{"top", [3]float64{0, -1, 0}, [3]float64{0, 0, 1}},
		{"iso", [3]float64{-1, -0.8, -1}, [3]float64{0, 1, 0}},
	}
}

// ContactSheet draws every standard view of a document side by side, and returns
// it as a PNG data URI — the same shape an image uploaded by a reader arrives in,
// so it needs nothing new to reach the model.
//
// Empty string when there is nothing to draw. A model with no buildable part is
// not a picture of an empty room, it is the absence of a picture, and a caller
// must be able to tell those apart.
func ContactSheet(d Document, unit Unit, size int) string {
	m := Tessellate(d, unit)
	parts := make([]RenderPart, 0, len(m.Groups))
	for _, g := range m.Groups {
		parts = append(parts, RenderPart{ID: g.PartID, Triangles: g.Triangles})
	}
	return ContactSheetOf(d, parts, size)
}

// RenderPart is one part's surface, from wherever it was produced.
//
// The ID is what colours it: the part's own colour is looked up in the document,
// so a sheet drawn from the CAD kernel's mesh and one drawn from the local
// tessellation put the same part in the same colour, and neither differs from
// what the reader sees in the workbench.
type RenderPart struct {
	ID        string
	Triangles []Triangle
}

// ContactSheetOf draws already-built surfaces, whatever built them.
//
// # Why this exists separately from ContactSheet
//
// ContactSheet draws what the document DESCRIBES. That is a triangle builder,
// and it performs no boolean and runs no script — so a bolt hole is a solid post
// and a scripted part is a bounding box, and it says so in its own inferences.
// Both were live defects in the checks that read this picture: the vision model
// reported "a solid cylinder protruding from the plate surface rather than a
// hole" about a correct plate, and "a rectangular block rather than a circular
// gear" about a gear whose script had just built a correct 12565.7 mm³ solid.
//
// The CAD kernel builds the real surface — features applied, scripts run — and
// hands back triangles. This is the door that lets those be drawn by the same
// rasterizer, in the same colours, from the same viewpoints, so the two pictures
// are comparable and only their SOURCE differs.
//
// Empty string when there is nothing to draw. A model with no buildable part is
// not a picture of an empty room, it is the absence of a picture, and a caller
// must be able to tell those apart.
func ContactSheetOf(d Document, parts []RenderPart, size int) string {
	total := 0
	for _, p := range parts {
		total += len(p.Triangles)
	}
	if total == 0 || size <= 0 {
		return ""
	}
	// Grouped by part and coloured by the part's OWN colour — the same one the
	// reader sees in the workbench. A picture in one flat colour cannot answer
	// the question this loop exists to ask ("is the cabin inside the body?"),
	// because every surface looks like the same surface. And a colour invented
	// here would put the agent and the reader in front of different pictures.
	var groups []shadedGroup
	for _, p := range parts {
		groups = append(groups, shadedGroup{tris: p.Triangles, col: partColour(d, p.ID)})
	}
	views := StandardViews()
	sheet := image.NewRGBA(image.Rect(0, 0, size*len(views), size))
	for i, v := range views {
		drawInto(sheet, i*size, size, groups, v)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, sheet); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// shadedGroup is one part's facets and the colour to draw them in.
type shadedGroup struct {
	tris []Triangle
	col  [3]float64
}

// partColour reads the part's declared "#rrggbb". Parts without one get a hue
// from their position in the document rather than a single default: two
// uncoloured parts touching must not read as one part, which is precisely the
// judgement the picture is being used for.
func partColour(d Document, id string) [3]float64 {
	for i, p := range d.Parts {
		if p.ID != id {
			continue
		}
		if c, ok := parseHexColour(p.Color); ok {
			return c
		}
		// Golden-ratio hue stepping: adjacent parts land far apart on the wheel.
		return hueToRGB(math.Mod(float64(i)*0.618033988749895, 1))
	}
	return [3]float64{0.7, 0.7, 0.72}
}

func parseHexColour(s string) ([3]float64, bool) {
	if len(s) != 7 || s[0] != '#' {
		return [3]float64{}, false
	}
	var v [3]float64
	for i := 0; i < 3; i++ {
		n, err := strconv.ParseUint(s[1+i*2:3+i*2], 16, 8)
		if err != nil {
			return [3]float64{}, false
		}
		v[i] = float64(n) / 255
	}
	return v, true
}

// hueToRGB at full saturation and a value that keeps every hue legible against
// the light background.
func hueToRGB(h float64) [3]float64 {
	const s, val = 0.55, 0.85
	i := math.Floor(h * 6)
	f := h*6 - i
	p := val * (1 - s)
	q := val * (1 - s*f)
	t := val * (1 - s*(1-f))
	switch int(i) % 6 {
	case 0:
		return [3]float64{val, t, p}
	case 1:
		return [3]float64{q, val, p}
	case 2:
		return [3]float64{p, val, t}
	case 3:
		return [3]float64{p, q, val}
	case 4:
		return [3]float64{t, p, val}
	default:
		return [3]float64{val, p, q}
	}
}

// drawInto rasterises one view into a square panel at xoff.
func drawInto(dst *image.RGBA, xoff, size int, groups []shadedGroup, v View) {
	fwd := normalise(v.Eye)
	right := normalise(cross3(v.Up, fwd))
	up := cross3(fwd, right)

	// Fit: project everything, then scale so the widest axis fills the panel
	// with a margin. Fitting per view rather than once for all of them is
	// deliberate — a top view of a long car would otherwise be a thin stripe,
	// and the reader of this picture is being asked about proportion.
	type pt struct{ x, y, z float64 }
	proj := make([][][3]pt, len(groups))
	lo := [2]float64{math.Inf(1), math.Inf(1)}
	hi := [2]float64{math.Inf(-1), math.Inf(-1)}
	for gi, g := range groups {
		proj[gi] = make([][3]pt, len(g.tris))
		for i, t := range g.tris {
			for j, w := range [3][3]float64{t.A, t.B, t.C} {
				p := pt{dot3(w, right), dot3(w, up), dot3(w, fwd)}
				proj[gi][i][j] = p
				lo[0], hi[0] = math.Min(lo[0], p.x), math.Max(hi[0], p.x)
				lo[1], hi[1] = math.Min(lo[1], p.y), math.Max(hi[1], p.y)
			}
		}
	}
	span := math.Max(hi[0]-lo[0], hi[1]-lo[1])
	if span <= 0 {
		span = 1
	}
	margin := float64(size) * 0.08
	scale := (float64(size) - 2*margin) / span
	cx := (lo[0] + hi[0]) / 2
	cy := (lo[1] + hi[1]) / 2
	toScreen := func(p pt) (float64, float64) {
		return float64(size)/2 + (p.x-cx)*scale, float64(size)/2 - (p.y-cy)*scale
	}

	// Painter's algorithm would put a far triangle over a near one wherever they
	// overlap without crossing, which is exactly the case that matters here: a
	// wheel buried INSIDE a body must look buried. So: a z buffer.
	zbuf := make([]float64, size*size)
	for i := range zbuf {
		zbuf[i] = math.Inf(1)
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dst.Set(xoff+x, y, color.RGBA{0xf4, 0xf5, 0xf7, 0xff})
		}
	}

	light := normalise([3]float64{-0.4, 0.8, -0.5})
	for gi, g := range groups {
		for i, t := range g.tris {
			x0, y0 := toScreen(proj[gi][i][0])
			x1, y1 := toScreen(proj[gi][i][1])
			x2, y2 := toScreen(proj[gi][i][2])
			// Flat shading from the facet's own normal: enough to see where one
			// surface ends and the next begins, which is the whole job.
			shade := 0.35 + 0.65*math.Abs(dot3(t.Normal, light))
			c := color.RGBA{
				uint8(clamp01(shade*g.col[0]) * 255),
				uint8(clamp01(shade*g.col[1]) * 255),
				uint8(clamp01(shade*g.col[2]) * 255), 0xff}
			fillTriangle(dst, zbuf, xoff, size,
				x0, y0, proj[gi][i][0].z, x1, y1, proj[gi][i][1].z, x2, y2, proj[gi][i][2].z, c)
		}
	}
}

func fillTriangle(dst *image.RGBA, zbuf []float64, xoff, size int,
	x0, y0, z0, x1, y1, z1, x2, y2, z2 float64, c color.RGBA) {

	minx := int(math.Floor(math.Min(x0, math.Min(x1, x2))))
	maxx := int(math.Ceil(math.Max(x0, math.Max(x1, x2))))
	miny := int(math.Floor(math.Min(y0, math.Min(y1, y2))))
	maxy := int(math.Ceil(math.Max(y0, math.Max(y1, y2))))
	if minx < 0 {
		minx = 0
	}
	if miny < 0 {
		miny = 0
	}
	if maxx > size-1 {
		maxx = size - 1
	}
	if maxy > size-1 {
		maxy = size - 1
	}
	area := (x1-x0)*(y2-y0) - (x2-x0)*(y1-y0)
	if math.Abs(area) < 1e-12 {
		return // edge-on: no pixels, and dividing by it would produce NaNs
	}
	for y := miny; y <= maxy; y++ {
		for x := minx; x <= maxx; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			w0 := ((x1-px)*(y2-py) - (x2-px)*(y1-py)) / area
			w1 := ((x2-px)*(y0-py) - (x0-px)*(y2-py)) / area
			w2 := 1 - w0 - w1
			if w0 < 0 || w1 < 0 || w2 < 0 {
				continue
			}
			z := w0*z0 + w1*z1 + w2*z2
			idx := y*size + x
			if z >= zbuf[idx] {
				continue
			}
			zbuf[idx] = z
			dst.Set(xoff+x, y, c)
		}
	}
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }
