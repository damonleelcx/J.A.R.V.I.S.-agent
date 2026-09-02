// Command portraitcrop cuts FORGE's expression portraits out of the character
// sheet and writes them to internal/httpapi/assets/portrait/.
//
// It lives in the repository rather than being a one-off shell invocation
// because the crop is a decision — which region of the sheet is "thoughtful" —
// and a decision that exists only in someone's terminal history is one nobody
// can check or redo when the sheet is redrawn.
//
// Usage:
//
//	go run ./tools/portraitcrop -sheet path/to/character-sheet.png
//	go run ./tools/portraitcrop -sheet ... -contact   # write a contact sheet only
package main

import (
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	xdraw "golang.org/x/image/draw"
)

// crop is one expression's region on the sheet, in source pixels.
//
// The sheet's bottom-left panel holds four head-and-shoulders studies in a row.
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

func main() {
	sheet := flag.String("sheet", "", "path to the character sheet PNG (required)")
	outDir := flag.String("out", "internal/httpapi/assets/portrait", "output directory")
	size := flag.Int("size", 512, "output edge length in pixels")
	contactOnly := flag.Bool("contact", false, "write only a contact sheet for review, not the assets")
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

	// The contact sheet exists so the crop can be JUDGED rather than assumed.
	// Coordinates picked by eye off a scaled preview are wrong about as often as
	// they are right, and a portrait cropped through the forehead is the kind of
	// thing that ships because nobody looked.
	contact := image.NewRGBA(image.Rect(0, 0, *size*len(expressionCrops), *size))

	for i, c := range expressionCrops {
		r := image.Rect(
			b.Min.X+int(c.X0*float64(b.Dx())),
			b.Min.Y+int(c.Y0*float64(b.Dy())),
			b.Min.X+int(c.X1*float64(b.Dx())),
			b.Min.Y+int(c.Y1*float64(b.Dy())),
		)
		fmt.Printf("  %-11s %v  (%dx%d)  %s\n", c.Name, r, r.Dx(), r.Dy(), c.Purpose)

		// Square off around the centre so the face is not distorted by the
		// resize: the sheet's cells are taller than they are wide.
		sq := squareAround(r, b)

		dst := image.NewRGBA(image.Rect(0, 0, *size, *size))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sq, draw.Over, nil)

		draw.Draw(contact, image.Rect(i**size, 0, (i+1)**size, *size), dst, image.Point{}, draw.Src)

		if *contactOnly {
			continue
		}
		out := filepath.Join(*outDir, c.Name+".png")
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
