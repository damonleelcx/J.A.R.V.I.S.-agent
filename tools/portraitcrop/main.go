// Command portraitcrop cuts FORGE's character art out of the character sheet
// and writes it to internal/httpapi/assets/portrait/.
//
// It lives in the repository rather than being a one-off shell invocation
// because the crop is a decision — which region of the sheet is "thoughtful" —
// and a decision that exists only in someone's terminal history is one nobody
// can check or redo when the sheet is redrawn.
//
// Two kinds of art come off the same sheet and they are cut differently:
//
//   - The expression portraits are square, opaque, and shown inside a circle.
//     Whatever ground the sheet has is hidden by the frame.
//   - The figure is the full-length study on the landing page. It stands on the
//     page itself, so its ground has to be keyed out — see keyGround.
//
// Usage:
//
//	go run ./tools/portraitcrop -sheet path/to/character-sheet.png
//	go run ./tools/portraitcrop -sheet ... -contact   # write contact sheets only
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"

	xdraw "golang.org/x/image/draw"
)

// crop is one piece of art's region on the sheet, in source pixels.
//
// These are expressed as fractions of the sheet rather than absolute pixels so
// that a re-render at a different resolution still lands correctly.
type crop struct {
	Name string
	// x0, y0, x1, y1 as fractions of width/height.
	X0, Y0, X1, Y1 float64
	Purpose        string
}

// expressionCrops maps the character sheet's expression row onto FORGE's
// avatar expressions. Order on the sheet, left to right:
// calm · bright · thoughtful · focused.
// The vertical range is biased toward the FACE rather than the whole cell. The
// portrait is displayed inside a circle, and a square taken from the full cell
// puts the face in its upper third — so the circle crops the chin and fills its
// lower half with jacket. These bounds centre the head instead.
//
// The left edge of the first cell is inset past the panel border, which the
// sheet draws as a thin line and which reads as a stray scratch once scaled.
var expressionCrops = []crop{
	{"calm", 0.020, 0.716, 0.172, 0.952, "level and unhurried — the default presence"},
	{"bright", 0.180, 0.716, 0.332, 0.952, "open smile — shown when a goal completes"},
	{"thoughtful", 0.342, 0.716, 0.494, 0.952, "considering, hand near the chin — thinking, and waiting on a human"},
	{"focused", 0.504, 0.716, 0.656, 0.952, "narrowed and deliberate — a tool is running"},
}

// figureCrop is the full-length study, taken from the turnaround row.
//
// # Why the second figure and not the first
//
// The row is front · three-quarter · profile · back. The figure stands at the
// RIGHT edge of the landing page with the copy to its left, and the
// three-quarter view is the only one that faces into the page — the front view
// looks past the reader and the profile turns its back on half the composition.
//
// # Why the margin is wider than the figure
//
// keyGround models the ground from the crop's own left and right edge columns.
// Those columns therefore have to BE ground on every row: a crop drawn tight to
// the hair would take the hair as its reference for that row and key nothing
// out there. The figure's own bounds on this sheet are x 468..694, y 24..715 of
// 1536x1024; the crop below leaves roughly thirty pixels of ground on each side
// and stops short of the panel's border line, which is not ground and would
// survive the key as a hairline.
var figureCrop = crop{
	"figure", 0.283854, 0.011719, 0.466146, 0.707031,
	"full length, three-quarter view, facing into the page — the landing page's subject",
}

// Ground-keying constants. Named here rather than inlined because they are the
// whole judgement of the key, and because the numbers that work are not obvious:
// on this sheet the white uniform is (244,235,229) and the ground behind it is
// (246,240,237). Those are six units apart. A key that removes everything
// "near-white" removes her jacket and her boots with it.
//
// What makes the key possible anyway is that the ground is CONNECTED to the
// border and locally flat, and the uniform is neither — so the flood starts at
// the frame edge and is stopped both by distance from the modelled ground and
// by any real edge it meets.
const (
	// groundTol: a pixel this close to the modelled ground (Euclidean, 0-255 per
	// channel) can be flooded into. Above the uniform's own distance from the
	// ground, and below the softest part of the silhouette's shadow.
	groundTol = 16
	// groundOpaque: a fringe pixel this far from the ground is fully the
	// subject. Between groundTol and this, alpha ramps.
	groundOpaque = 40
	// edgeGate: the flood will not cross a pixel whose local luminance gradient
	// exceeds this. It is what keeps a soft boundary between two nearly equal
	// whites from leaking.
	edgeGate = 36
	// fringe: how many pixels deep from the keyed ground the alpha ramp is
	// allowed to reach. Beyond it the subject is fully opaque regardless of
	// colour, so a pale highlight in the middle of the jacket cannot go
	// translucent.
	fringe = 6
	// shadowFloor and shadowChroma key out the figure's CAST SHADOW.
	//
	// The sheet lights her standing on a floor, and the floor is not part of
	// her. The shadow is too dark to be within groundTol and too smooth to be
	// stopped by edgeGate, so without this it survives as a grey smear pooling
	// at her boots — invisible on the light theme and obvious on the dark one.
	//
	// A shadowed ground is the ground SCALED DOWN: same hue, less light. So a
	// pixel is shadow when it is a multiple of the modelled ground between
	// shadowFloor and 1, with almost nothing left over. The white boot standing
	// in it is the nearest thing to a false positive — it scales at about 1.00
	// with a residual near 7 — which is why the residual bound is well under
	// that and not merely under the uniform's distance from the ground.
	shadowFloor  = 0.55
	shadowChroma = 5.0
)

func main() {
	sheet := flag.String("sheet", "", "path to the character sheet PNG (required)")
	outDir := flag.String("out", "internal/httpapi/assets/portrait", "output directory")
	size := flag.Int("size", 512, "expression portrait edge length in pixels")
	figureHeight := flag.Int("figure-height", 1280, "full-length figure height in pixels")
	contactOnly := flag.Bool("contact", false, "write only contact sheets for review, not the assets")
	flag.Parse()

	if *sheet == "" {
		fmt.Fprintln(os.Stderr, "portraitcrop: -sheet is required")
		flag.Usage()
		os.Exit(2)
	}

	src, err := loadPNG(*sheet)
	if err != nil {
		fmt.Fprintf(os.Stderr, "portraitcrop: cannot read the sheet: %v\n", err)
		os.Exit(1)
	}
	b := src.Bounds()
	fmt.Printf("sheet %s (%dx%d)\n", *sheet, b.Dx(), b.Dy())

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "portraitcrop: %v\n", err)
		os.Exit(1)
	}

	writeExpressions(src, *outDir, *size, *contactOnly)
	writeFigure(src, *outDir, *figureHeight, *contactOnly)
}

func writeExpressions(src image.Image, outDir string, size int, contactOnly bool) {
	b := src.Bounds()

	// The contact sheet exists so the crop can be JUDGED rather than assumed.
	// Coordinates picked by eye off a scaled preview are wrong about as often as
	// they are right, and a portrait cropped through the forehead is the kind of
	// thing that ships because nobody looked.
	contact := image.NewRGBA(image.Rect(0, 0, size*len(expressionCrops), size))

	for i, c := range expressionCrops {
		r := regionOf(c, b)
		fmt.Printf("  %-11s %v  (%dx%d)  %s\n", c.Name, r, r.Dx(), r.Dy(), c.Purpose)

		// Square off around the centre so the face is not distorted by the
		// resize: the sheet's cells are taller than they are wide.
		sq := squareAround(r, b)

		dst := image.NewRGBA(image.Rect(0, 0, size, size))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sq, draw.Over, nil)

		draw.Draw(contact, image.Rect(i*size, 0, (i+1)*size, size), dst, image.Point{}, draw.Src)

		if contactOnly {
			continue
		}
		out := filepath.Join(outDir, c.Name+".png")
		if err := writePNG(out, dst); err != nil {
			fmt.Fprintf(os.Stderr, "portraitcrop: writing %s: %v\n", out, err)
			os.Exit(1)
		}
		fmt.Printf("               -> %s\n", out)
	}

	contactPath := filepath.Join(os.TempDir(), "forge-portrait-contact.png")
	if err := writePNG(contactPath, contact); err != nil {
		fmt.Fprintf(os.Stderr, "portraitcrop: writing the contact sheet: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("contact sheet -> %s\n", contactPath)
}

func writeFigure(src image.Image, outDir string, height int, contactOnly bool) {
	b := src.Bounds()
	r := regionOf(figureCrop, b)
	fmt.Printf("  %-11s %v  (%dx%d)  %s\n", figureCrop.Name, r, r.Dx(), r.Dy(), figureCrop.Purpose)

	cut, keyed := keyGround(src, r)
	fmt.Printf("               ground keyed out of %.1f%% of the crop\n", keyed)
	// A key that removes almost nothing means the ground model missed and the
	// figure would ship inside a pale rectangle; one that removes almost
	// everything means it ate the subject. Neither is recoverable by looking at
	// the asset later, because by then it is just a PNG.
	if keyed < 25 || keyed > 80 {
		fmt.Fprintf(os.Stderr, "portraitcrop: the ground key removed %.1f%% of the figure crop, "+
			"which is outside the 25-80%% a correct key produces on this sheet. "+
			"The crop bounds or the sheet's ground have changed; check the contact sheet "+
			"before trusting this asset.\n", keyed)
		os.Exit(1)
	}

	scaled := scaleAlpha(cut, height)

	// The figure's contact sheet shows it over BOTH grounds it will be seen on.
	// A key that left a pale fringe is invisible on the light theme and obvious
	// on the dark one, so a single-ground proof proves nothing.
	contact := contactOverBoth(scaled)
	contactPath := filepath.Join(os.TempDir(), "forge-figure-contact.png")
	if err := writePNG(contactPath, contact); err != nil {
		fmt.Fprintf(os.Stderr, "portraitcrop: writing the figure contact sheet: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("figure contact -> %s\n", contactPath)

	if contactOnly {
		return
	}
	out := filepath.Join(outDir, figureCrop.Name+".png")
	if err := writePNG(out, scaled); err != nil {
		fmt.Fprintf(os.Stderr, "portraitcrop: writing %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Printf("               -> %s (%dx%d)\n", out, scaled.Bounds().Dx(), scaled.Bounds().Dy())
}

func regionOf(c crop, b image.Rectangle) image.Rectangle {
	return image.Rect(
		b.Min.X+int(c.X0*float64(b.Dx())),
		b.Min.Y+int(c.Y0*float64(b.Dy())),
		b.Min.X+int(c.X1*float64(b.Dx())),
		b.Min.Y+int(c.Y1*float64(b.Dy())),
	)
}

// keyGround cuts the sheet's ground away from a region and returns the subject
// with an alpha channel, plus the percentage of the region that was removed.
//
// # Why a flood and not a colour threshold
//
// The sheet's ground and the character's white uniform are a few units apart
// (see the constants above), so no threshold separates them. What does separate
// them is reachability: the ground is one connected region touching the crop's
// border. The flood starts there and is stopped by distance from the modelled
// ground and by any real edge, so an enclosed white — a sleeve, a boot — is
// never reached even though its colour would have qualified.
//
// # Why the ground is modelled per row rather than sampled once
//
// It is a soft vignette, not a flat colour: it runs from about 233 to 248 across
// this sheet, which is wider than the gap between the ground and the uniform. A
// single reference colour mis-reads the corners by more than the thing it is
// trying to distinguish. Each row takes its reference from its own left and
// right edge columns and interpolates between them.
//
// # Why the fringe is un-mixed rather than just faded
//
// A half-transparent edge pixel still carries the sheet's warm white in its RGB.
// Faded onto a dark page that reads as a pale halo tracing the silhouette —
// which is exactly the "pasted sticker" look the portrait README warns about.
// Removing the ground's contribution from the fringe's colour (the standard
// alpha un-mix) is what makes the same asset sit correctly on both themes.
func keyGround(src image.Image, r image.Rectangle) (*image.RGBA, float64) {
	w, h := r.Dx(), r.Dy()
	at := func(x, y int) [3]float64 {
		cr, cg, cb, _ := src.At(r.Min.X+x, r.Min.Y+y).RGBA()
		return [3]float64{float64(cr >> 8), float64(cg >> 8), float64(cb >> 8)}
	}

	// The ground model: per row, the median of the five leftmost and five
	// rightmost columns, interpolated across. Medians rather than means so a
	// stray dark pixel at the frame edge cannot drag the reference.
	left := make([][3]float64, h)
	right := make([][3]float64, h)
	for y := 0; y < h; y++ {
		for i := 0; i < 3; i++ {
			l := make([]float64, 0, 5)
			rr := make([]float64, 0, 5)
			for k := 0; k < 5; k++ {
				l = append(l, at(k, y)[i])
				rr = append(rr, at(w-1-k, y)[i])
			}
			sort.Float64s(l)
			sort.Float64s(rr)
			left[y][i], right[y][i] = l[len(l)/2], rr[len(rr)/2]
		}
	}
	ground := func(x, y int) [3]float64 {
		f := float64(x) / float64(w-1)
		var o [3]float64
		for i := 0; i < 3; i++ {
			o[i] = left[y][i]*(1-f) + right[y][i]*f
		}
		return o
	}
	lum := func(x, y int) float64 { c := at(x, y); return 0.299*c[0] + 0.587*c[1] + 0.114*c[2] }
	gradient := func(x, y int) float64 {
		if x < 1 || y < 1 || x >= w-1 || y >= h-1 {
			return 0
		}
		return math.Hypot(lum(x+1, y)-lum(x-1, y), lum(x, y+1)-lum(x, y-1))
	}
	distance := func(x, y int) float64 {
		c, g := at(x, y), ground(x, y)
		return math.Sqrt((c[0]-g[0])*(c[0]-g[0]) + (c[1]-g[1])*(c[1]-g[1]) + (c[2]-g[2])*(c[2]-g[2]))
	}
	// shadowed reports whether this pixel is the ground with the light taken out
	// of it: the ground colour scaled by k, with a small residual. k is the
	// projection of the pixel onto the ground colour, so it answers "how much of
	// this pixel IS the ground" without caring how dark it got.
	shadowed := func(x, y int) bool {
		c, g := at(x, y), ground(x, y)
		gg := g[0]*g[0] + g[1]*g[1] + g[2]*g[2]
		if gg == 0 {
			return false
		}
		k := (c[0]*g[0] + c[1]*g[1] + c[2]*g[2]) / gg
		if k < shadowFloor || k > 1 {
			return false
		}
		rx, ry, rz := c[0]-k*g[0], c[1]-k*g[1], c[2]-k*g[2]
		return math.Sqrt(rx*rx+ry*ry+rz*rz) < shadowChroma
	}
	isGroundColour := func(x, y int) bool { return distance(x, y) <= groundTol || shadowed(x, y) }

	isGround := make([]bool, w*h)
	queue := make([][2]int, 0, w*h)
	flood := func(x, y int) {
		if x < 0 || y < 0 || x >= w || y >= h || isGround[y*w+x] {
			return
		}
		if !isGroundColour(x, y) || gradient(x, y) > edgeGate {
			return
		}
		isGround[y*w+x] = true
		queue = append(queue, [2]int{x, y})
	}
	for x := 0; x < w; x++ {
		flood(x, 0)
		flood(x, h-1)
	}
	for y := 0; y < h; y++ {
		flood(0, y)
		flood(w-1, y)
	}
	for i := 0; i < len(queue); i++ {
		x, y := queue[i][0], queue[i][1]
		flood(x+1, y)
		flood(x-1, y)
		flood(x, y+1)
		flood(x, y-1)
	}

	// How many pixels each remaining pixel is from the keyed ground, so the
	// alpha ramp applies to the silhouette's fringe and nothing else.
	depth := make([]int, w*h)
	for i := range depth {
		depth[i] = fringe + 1
	}
	ring := make([][2]int, 0, w*h)
	for i, g := range isGround {
		if g {
			depth[i] = 0
			ring = append(ring, [2]int{i % w, i / w})
		}
	}
	for i := 0; i < len(ring); i++ {
		x, y := ring[i][0], ring[i][1]
		d := depth[y*w+x]
		if d >= fringe {
			continue
		}
		for _, n := range [4][2]int{{x + 1, y}, {x - 1, y}, {x, y + 1}, {x, y - 1}} {
			nx, ny := n[0], n[1]
			if nx < 0 || ny < 0 || nx >= w || ny >= h || depth[ny*w+nx] <= d+1 {
				continue
			}
			depth[ny*w+nx] = d + 1
			ring = append(ring, [2]int{nx, ny})
		}
	}

	// Premultiplied, because the result is about to be resampled. Scaling
	// straight (non-premultiplied) colour blends the RGB of fully transparent
	// pixels into their neighbours, which draws a dark outline around everything.
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	removed := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c, g := at(x, y), ground(x, y)
			a := 1.0
			switch {
			case isGround[y*w+x]:
				a = 0
				removed++
			case depth[y*w+x] <= fringe:
				a = (distance(x, y) - groundTol) / (groundOpaque - groundTol)
				a = math.Max(0, math.Min(1, a))
			}
			var rr, gg, bb float64
			if a > 0 {
				// Un-mix the ground back out, then premultiply.
				rr = clamp255((c[0]-(1-a)*g[0])/a) * a
				gg = clamp255((c[1]-(1-a)*g[1])/a) * a
				bb = clamp255((c[2]-(1-a)*g[2])/a) * a
			}
			out.SetRGBA(x, y, color.RGBA{byte(rr + 0.5), byte(gg + 0.5), byte(bb + 0.5), byte(a*255 + 0.5)})
		}
	}
	return out, 100 * float64(removed) / float64(w*h)
}

func clamp255(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// scaleAlpha resizes a keyed cut to a target height, keeping its aspect.
func scaleAlpha(src *image.RGBA, height int) *image.RGBA {
	b := src.Bounds()
	width := int(float64(b.Dx()) * float64(height) / float64(b.Dy()))
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// contactOverBoth composites a keyed cut over the dark and the light ground,
// side by side, so the matte can be judged on both themes at once.
func contactOverBoth(cut *image.RGBA) *image.RGBA {
	b := cut.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, w*2, h))
	for i, bg := range []color.RGBA{{16, 18, 24, 255}, {247, 247, 249, 255}} {
		panel := image.Rect(i*w, 0, (i+1)*w, h)
		draw.Draw(out, panel, &image.Uniform{bg}, image.Point{}, draw.Src)
		draw.Draw(out, panel, cut, b.Min, draw.Over)
	}
	return out
}

// squareAround expands or trims a rectangle to a square about its centre,
// clamped to the source bounds.
func squareAround(r, bounds image.Rectangle) image.Rectangle {
	side := r.Dy()
	if r.Dx() > side {
		side = r.Dx()
	}
	cx, cy := (r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2
	sq := image.Rect(cx-side/2, cy-side/2, cx+side/2, cy+side/2)

	// Clamp by translating rather than cropping, so the square stays square and
	// the face stays centred instead of being sliced at the sheet's edge.
	if sq.Min.X < bounds.Min.X {
		sq = sq.Add(image.Pt(bounds.Min.X-sq.Min.X, 0))
	}
	if sq.Min.Y < bounds.Min.Y {
		sq = sq.Add(image.Pt(0, bounds.Min.Y-sq.Min.Y))
	}
	if sq.Max.X > bounds.Max.X {
		sq = sq.Add(image.Pt(bounds.Max.X-sq.Max.X, 0))
	}
	if sq.Max.Y > bounds.Max.Y {
		sq = sq.Add(image.Pt(0, bounds.Max.Y-sq.Max.Y))
	}
	return sq.Intersect(bounds)
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	return enc.Encode(f, img)
}
