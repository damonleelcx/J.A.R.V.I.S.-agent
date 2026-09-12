package httpapi

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
)

// The landing page's figure, and the three ways it goes wrong quietly.
//
// # What this is
//
// FORGE's full-length character art stands at the right of the landing page as
// its subject. It is cut from the character sheet by tools/portraitcrop, which
// keys the sheet's ground and the shadow she casts on it out of the image.
//
// # Why it needs fences at all
//
// Every failure mode here renders. Nothing errors, nothing logs, and the page
// still loads:
//
//   - Dropped from the template, or missing from the asset allowlist: the page
//     is simply the page it was before she existed, which looks deliberate.
//   - Announced to a screen reader: the masthead already says FORGE, so this
//     reads the product's name twice to the people who cannot see why.
//   - Shipped with the sheet's ground still on it: a pale rectangle around her.
//     On the light theme that is nearly invisible, which is exactly how it would
//     get through review and out to the dark theme, where it is a white box.
//
// The third is the one worth the machinery. It is a property of the BYTES, so
// no amount of reading the template or the stylesheet can catch it.

func TestTheLandingPageCarriesTheFigure(t *testing.T) {
	pages := NewPageHandlers(testDeps())
	rr := httptest.NewRecorder()
	pages.Index(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("the landing page returned %d", rr.Code)
	}
	body := rr.Body.String()

	i := strings.Index(body, `class="home-figure"`)
	if i < 0 {
		t.Fatal("the landing page no longer carries .home-figure. She is the page's subject, " +
			"and the composition is measured around her: the band function where portal-field.js " +
			"is mounted reads her layout box to decide where the field's mass goes and how big " +
			"it is. Without the element the mass silently takes the whole right side back.")
	}
	block := body[i:]
	if end := strings.Index(block, "</div>"); end > 0 {
		block = block[:end]
	}

	// Decorative, and it must stay that way: the accessible name lives on the
	// masthead avatar.
	if !strings.Contains(block, `aria-hidden="true"`) {
		t.Error("the figure is not aria-hidden. It is decoration — the masthead already names " +
			"FORGE — so announcing it repeats the product's name to a screen reader for an " +
			"image its user cannot see.")
	}
	if !strings.Contains(block, `alt=""`) {
		t.Error(`the figure needs an empty alt. Any other value is a second announcement of ` +
			`something the page has already said in text.`)
	}
	// Stated dimensions, so half a megabyte arriving does not reflow the page
	// under someone who has started reading.
	if !strings.Contains(block, "width=") || !strings.Contains(block, "height=") {
		t.Error("the figure carries no width/height, so the browser cannot reserve its box and " +
			"the composition shifts when the image lands")
	}

	// The URL in the page must be one the handler actually serves. Embedded,
	// versioned and allowlisted are three separate lists and this asserts the
	// end of that chain rather than any one of them.
	src := attrValue(block, "src")
	if src == "" {
		t.Fatal("the figure's img has no src")
	}
	if !strings.HasPrefix(src, "/assets/"+persona.FigureFile) {
		t.Errorf("the figure is served from %q, which is not persona.FigureFile (%q). The file "+
			"is named in one place so the allowlist and the page cannot disagree about it.",
			src, persona.FigureFile)
	}
	rr = httptest.NewRecorder()
	pages.Assets(rr, httptest.NewRequest(http.MethodGet, src, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("the landing page asks for %s and the handler returns %d — the page renders "+
			"with a broken image and nothing reports it", src, rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("the figure served as %q", ct)
	}
	// The hashed URL is the whole reason this asset is passed through pageData
	// rather than written into the template, so check it actually earns the
	// long cache rather than silently taking the revalidating branch.
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("the figure is served with Cache-Control %q. The page renders a content-hashed "+
			"URL precisely so this is `immutable`; if it is not, the version in the URL no "+
			"longer matches the one the handler computed.", cc)
	}
}

// The sheet's ground must not be in the asset.
//
// A figure with its ground still attached is a pale rectangle sitting on the
// page. This reads the bytes the server would actually send.
func TestTheFigureHasNoGroundLeftOnIt(t *testing.T) {
	raw, err := assetFS.ReadFile("assets/" + persona.FigureFile)
	if err != nil {
		t.Fatalf("the figure is not embedded: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the figure does not decode as a PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() < 300 || b.Dy() < 900 {
		t.Errorf("the figure is %dx%d. It is displayed up to 1280px tall, so anything much "+
			"smaller is being upscaled and arrives soft.", b.Dx(), b.Dy())
	}

	// Every corner must be fully transparent. A ground left on the image is a
	// rectangle, and a rectangle has corners.
	corners := map[string]image.Point{
		"top-left":     {X: b.Min.X, Y: b.Min.Y},
		"top-right":    {X: b.Max.X - 1, Y: b.Min.Y},
		"bottom-left":  {X: b.Min.X, Y: b.Max.Y - 1},
		"bottom-right": {X: b.Max.X - 1, Y: b.Max.Y - 1},
	}
	for name, p := range corners {
		if _, _, _, a := img.At(p.X, p.Y).RGBA(); a != 0 {
			t.Errorf("the figure's %s corner has alpha %d, not 0. The character sheet's ground "+
				"is still on the asset, which puts a pale rectangle behind her on the page — "+
				"invisible on the light theme and a white box on the dark one. Re-cut it with "+
				"tools/portraitcrop and look at the contact sheet it writes.", name, a>>8)
		}
	}

	// And a real matte, not a one-pixel bevel: she occupies a little over a
	// third of her own bounding box, so a correct cut clears roughly half of it.
	var clear int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a == 0 {
				clear++
			}
		}
	}
	pct := 100 * float64(clear) / float64(b.Dx()*b.Dy())
	if pct < 25 {
		t.Errorf("only %.1f%% of the figure is transparent. A correct cut of this pose clears "+
			"about half the frame; this much ground left on it will read as a panel behind her.", pct)
	}
	if pct > 80 {
		t.Errorf("%.1f%% of the figure is transparent — the key has eaten the subject. Her "+
			"uniform is within a few units of the sheet's ground, so a tolerance raised far "+
			"enough removes her jacket and her boots along with it.", pct)
	}
}

// The edge that keeps her off the field.
//
// She is white, and the field's wavefronts cross behind her at full brightness
// on both themes. A zero-offset shadow darkens the pixels immediately around
// her outline, which is what stops a violet ribbon — or, on the light theme,
// the paper itself — from meeting her silhouette with nothing in between. It
// looks like a rendering fault rather than a stylesheet that lost a line, so
// nobody would think to look in the stylesheet for it.
func TestTheFigureKeepsItsEdgeAgainstTheField(t *testing.T) {
	css, err := assetFS.ReadFile("assets/home.css")
	if err != nil {
		t.Fatal(err)
	}
	rule := ruleFor(string(css), ".home-figure img")
	if rule == "" {
		t.Fatal("home.css has no `.home-figure img` rule at all")
	}
	if !strings.Contains(rule, "var(--figure-halo)") {
		t.Error("`.home-figure img` no longer draws the --figure-halo shadow. That is the edge " +
			"between her and whatever the field is doing behind her — a wavefront at full " +
			"brightness, or on the light theme the paper itself. Without it her outline " +
			"dissolves into the brighter frames of an animation, which no screenshot catches.")
	}
	if !strings.Contains(rule, "var(--figure-shadow)") {
		t.Error("`.home-figure img` no longer draws the --figure-shadow. On the light theme she " +
			"is white on near-white and the shadow is what puts her in the page.")
	}
}

// The mass is fitted to room that was MEASURED, not to a tuned offset.
//
// # The requirement
//
// The landing page reads copy, then the field's white mass, then the figure,
// and none of the three may overlap another.
//
// # Why a constant cannot satisfy it
//
// The figure's width comes from her height, so the gap between the copy column
// and her depends on the window's proportions and not only its width. An offset
// that clears her on a 1440x900 laptop puts the mass through her on a 1280x1024
// display, and nothing about that failure is visible to anyone who only opened
// the page on the machine they tuned it on. Below a breakpoint she is not drawn
// at all and the whole right side is free again — a third case the same constant
// would have to be right about.
//
// Nor is the room always in the same DIRECTION. On a wide window it is the gap
// beside her; on a phone the copy is full-bleed, she stands at the foot of the
// first screen, and the room is the space ABOVE her — so the band is a
// rectangle and both of its axes constrain the mass, its size and its orbit.
// Treating it as a horizontal interval is what left a phone showing neither of
// them: the mass fell back to the full width where the scrim hid it, and the
// figure was not drawn at all.
//
// So the page measures its own layout and the shader is told the answer. This
// asserts the couplings that carry it, because the behaviour itself needs a
// browser: there is no JavaScript test harness in this repository, and the
// arrangement was verified in one at 1920, 1440, 1100 and 375 wide, on both
// themes, when it landed.
func TestTheFieldsMassIsFittedToMeasuredRoom(t *testing.T) {
	src, err := assetFS.ReadFile("assets/portal-field.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)

	for _, c := range []struct{ needle, why string }{
		{"uniform vec4  uBand;",
			"the shader no longer declares uBand as a rectangle, so it cannot be told where " +
				"the free room is or which way it runs"},
		{"sp -= uBand.xy;",
			"the mass is no longer placed at the measured band's centre — it is back to a " +
				"fixed offset, which is right at one window size and wrong at the next"},
		{"SPAN / max(room",
			"the mass is no longer shrunk to fit the measured band, so on a narrower window it " +
				"keeps its size and grows through whatever is beside it"},
		{"min(uBand.z, uBand.w)",
			"the fit considers only one axis. On a phone the room is the space ABOVE the " +
				"figure, and a mass sized by width alone hangs straight down through her"},
		{"min(RISE, uBand.w",
			"the scroll orbit is no longer clipped to the band's height, so on a phone the " +
				"mass swings down through the figure a quarter of the way through the page — " +
				"a collision nobody sees in a screenshot of the top of the page"},
		{"if (room > 0.10)",
			"the mass is drawn even when the layout leaves no room for it"},
		{"gl.uniform4f(uBand",
			"the measurement is never handed to the shader, so uBand stays at its default and " +
				"the mass sits in the middle of the page on top of the copy"},
		{"document.querySelector('.home-figure')",
			"the band is computed without looking at the figure, which is the one thing the " +
				"mass is not allowed to touch"},
	} {
		if !strings.Contains(js, c.needle) {
			t.Errorf("portal-field.js no longer contains %q: %s.\n"+
				"The composition is one decision spread over portal-field.js (placement and fit), "+
				"home.css (.home-figure's size) and the page's own layout. Any of them can be "+
				"changed alone and the page still renders — it just renders the mass through "+
				"the figure at some window size nobody has open.", c.needle, c.why)
		}
	}
}

// attrValue reads a quoted HTML attribute out of a fragment.
func attrValue(fragment, name string) string {
	i := strings.Index(fragment, name+`="`)
	if i < 0 {
		return ""
	}
	rest := fragment[i+len(name)+2:]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// ruleFor returns the declaration block of the first rule whose selector list
// ends with the given selector.
func ruleFor(css, selector string) string {
	for _, block := range strings.Split(css, "}") {
		open := strings.Index(block, "{")
		if open < 0 {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(stripCSSComments(block[:open])), selector) {
			return block[open+1:]
		}
	}
	return ""
}
