package geometry_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The sheet is a picture of the model, not a blank page.
//
// # Why this is asserted at the pixel level
//
// Everything built on top of this asks a vision model structural questions about
// the image. A rasterizer that silently produced an empty panel would send a
// blank page and get back a confident answer about nothing — the failure would
// look like the vision model being wrong, and would be unfalsifiable from the
// logs. So the pixels are counted.
//
// This repository has met the blank-image trap once already: canvas.toDataURL()
// on a WebGL context with no preserved drawing buffer returns a constant, and a
// camera-movement measurement built on it was meaningless.
func TestContactSheet_DrawsTheModel(t *testing.T) {
	doc := geometry.Document{Name: "car", Units: "mm", Parts: []geometry.Part{
		{ID: "body", Name: "Body", Shape: "box",
			Size: map[string]float64{"width": 1900, "height": 800, "depth": 4500}},
		{ID: "wheel", Name: "Wheel", Shape: "cylinder",
			Size:     map[string]float64{"radius": 350, "depth": 250},
			Position: []float64{-950, 350, 1600}, Rotation: []float64{0, 0, 90}},
	}}

	uri := geometry.ContactSheet(doc, geometry.Millimetre, 200)
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("not a PNG data URI: %.40s", uri)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	views := len(geometry.StandardViews())
	if w := img.Bounds().Dx(); w != 200*views {
		t.Errorf("the sheet is %dpx wide; %d views at 200px should be %d", w, views, 200*views)
	}

	// EVERY panel must contain the model. One blank panel is a whole view the
	// reader silently cannot see, and it is the view that would have shown the
	// thing the others hide.
	for i, v := range geometry.StandardViews() {
		if painted := paintedIn(img, i*200, 200); painted < 200*200/50 {
			t.Errorf("the %q panel is %d painted pixels — effectively blank. A blank panel "+
				"sends the vision model a picture of nothing and gets back a confident "+
				"answer about nothing", v.Name, painted)
		}
	}
}

// A model with nothing buildable is the ABSENCE of a picture, not a picture of
// an empty room — a caller has to be able to tell those apart.
func TestContactSheet_SaysNothingWhenThereIsNothing(t *testing.T) {
	if got := geometry.ContactSheet(geometry.Document{Name: "empty", Units: "mm"},
		geometry.Millimetre, 200); got != "" {
		t.Errorf("an empty document produced a %d-byte image", len(got))
	}
}

// The z buffer must hide what is behind. A wheel buried INSIDE the body is the
// failure this whole loop exists to catch, and painter's-algorithm ordering
// would draw it on top of the body it is inside.
func TestContactSheet_HidesWhatIsBehind(t *testing.T) {
	// A small dark part entirely inside a large one: nothing of it should show.
	hidden := geometry.Document{Name: "h", Units: "mm", Parts: []geometry.Part{
		{ID: "shell", Name: "Shell", Shape: "box", Size: map[string]float64{"width": 1000, "height": 1000, "depth": 1000}},
		{ID: "core", Name: "Core", Shape: "box", Size: map[string]float64{"width": 100, "height": 100, "depth": 100}},
	}}
	alone := geometry.Document{Name: "a", Units: "mm", Parts: hidden.Parts[:1]}

	a := decode(t, geometry.ContactSheet(hidden, geometry.Millimetre, 120))
	b := decode(t, geometry.ContactSheet(alone, geometry.Millimetre, 120))
	if !sameImage(a, b) {
		t.Error("a part entirely inside another changed the picture, so something is being " +
			"drawn through a surface it is behind. Every judgement made from these images " +
			"about what is inside what would be unreliable")
	}
}

func decode(t *testing.T, uri string) image.Image {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func paintedIn(img image.Image, xoff, size int) int {
	n := 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, b, _ := img.At(xoff+x, y).RGBA()
			// Anything that is not the background.
			if r>>8 != 0xf4 || g>>8 != 0xf5 || b>>8 != 0xf7 {
				n++
			}
		}
	}
	return n
}

func sameImage(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			if a.At(x, y) != b.At(x, y) {
				return false
			}
		}
	}
	return true
}
