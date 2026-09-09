package httpapi

import (
	"io/fs"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// FORGE's mark must be inlined into the page, never referenced as an image.
//
// # Why this is worth a test
//
// persona.AvatarSVG carries no paint of its own, on purpose. Every colour is a
// class resolved by avatar.css against the theme's custom properties, and every
// one of the six states is an animation in that same stylesheet. That is what
// lets one server-side implementation of the mark render correctly in both
// themes without the server knowing which theme is on screen.
//
// The cost of that design is a hard constraint on how the markup may be
// consumed: an SVG loaded through <img> is an isolated document that cannot see
// the host page's stylesheet. Behind an <img>, every class goes unresolved — the
// gradient stops fall back to black, .fa-ring loses its stroke entirely and is
// not drawn at all, and none of the state animations run. The mark becomes a
// black silhouette that reports nothing.
//
// The failure is silent in the worst way: the request succeeds, the SVG is
// valid, the element is the right size, and nothing errors. On the dark
// workbench header it read as "the logo is hard to see in dark mode" — a
// styling nit — when in fact the status indicator had stopped indicating
// status in every theme. See
// docs/bugfix/2026-09-09-the-sigil-was-black-behind-an-img.md.
var (
	sigilEndpoint = regexp.MustCompile(`/v1/meta/sigil`)
	imgTag        = regexp.MustCompile(`<img\b`)
)

// TestSigilIsNeverConsumedAsAnImage fails when a script builds an <img> around
// the sigil endpoint.
//
// Scoped to <img> rather than to the endpoint itself: fetching the endpoint is
// exactly what assets/sigil.js is supposed to do. What must not come back is a
// caller wrapping it in an element that severs it from the stylesheet.
func TestSigilIsNeverConsumedAsAnImage(t *testing.T) {
	scripts, err := fs.Glob(assetFS, "assets/*.js")
	if err != nil {
		t.Fatal(err)
	}
	// The glob going blind would make this fence pass by finding nothing to
	// check, which is the one way a fence fails that nobody notices.
	if len(scripts) < 10 {
		t.Fatalf("found %d script(s); the glob is not working", len(scripts))
	}
	sawSigilJS := false

	for _, path := range scripts {
		name := strings.TrimPrefix(path, "assets/")
		if name == "sigil.js" {
			sawSigilJS = true
		}
		body, err := assetFS.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Comments are stripped first. This file's own header explains the rule
		// by quoting the thing it forbids, and a fence that cannot be described
		// without tripping itself is one somebody will delete rather than obey.
		for i, line := range strings.Split(stripJSComments(string(body)), "\n") {
			if !sigilEndpoint.MatchString(line) || !imgTag.MatchString(line) {
				continue
			}
			t.Errorf("%s:%d builds an <img> around /v1/meta/sigil. The mark takes its "+
				"colours and its state animation from avatar.css, and an SVG behind an <img> "+
				"is an isolated document that cannot see this page's stylesheet — every class "+
				"goes unresolved, the gradients render black and the ring is not drawn at all. "+
				"Use window.ForgeSigil.place() or .slot() + .hydrate() from assets/sigil.js, "+
				"which put the same markup into the document where the stylesheet reaches it.",
				name, i+1)
		}
	}

	if !sawSigilJS {
		t.Error("assets/sigil.js is missing. It is the only supported way to place the " +
			"mark; without it callers will go back to <img> and the mark will render black.")
	}
}

// TestSigilMarkupHasNoPaintOfItsOwn pins the premise the fence above rests on.
//
// If AvatarSVG ever grows real fill/stroke/stop-color attributes, the <img>
// route stops being wrong and the constraint above becomes cargo. This test is
// what tells a future reader that the premise changed, rather than leaving them
// to work out why an apparently arbitrary rule exists.
func TestSigilMarkupHasNoPaintOfItsOwn(t *testing.T) {
	svg := sigilBody(t)
	for _, attr := range []string{"stop-color=", "stroke=\"#", "fill=\"#"} {
		if strings.Contains(svg, attr) {
			t.Errorf("the served sigil now sets %s directly. It used to take every colour "+
				"from avatar.css, which is why it may not be consumed through an <img> "+
				"(see TestSigilIsNeverConsumedAsAnImage). If the mark is now self-painting, "+
				"revisit that fence rather than deleting this one.", attr)
		}
	}
	// The classes the stylesheet paints. Their absence would mean the mark is
	// getting its colour from somewhere this test does not know about.
	for _, cls := range []string{"fa-wing-lit", "fa-core-hot", "fa-ring"} {
		if !strings.Contains(svg, cls) {
			t.Errorf("the served sigil no longer carries the class %q, which avatar.css "+
				"paints. Either the mark changed shape or it stopped being themeable.", cls)
		}
	}
}

// sigilBody serves the mark through the real handler, so what is asserted on is
// what a browser receives rather than a re-derivation of it.
func sigilBody(t *testing.T) string {
	t.Helper()
	pages := NewPageHandlers(testDeps())
	rec := httptest.NewRecorder()
	pages.Sigil(rec, httptest.NewRequest("GET", "/v1/meta/sigil?state=idle&size=64", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /v1/meta/sigil returned %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// stripJSComments blanks out // and /* */ comments, leaving line numbering
// intact so a failure still points at the right line.
//
// String literals are tracked so that a "//" inside one is not mistaken for a
// comment. Regular-expression literals are not tracked, which is safe only
// because no asset contains one holding a quote — TestStripJSCommentsKeepsCode
// is what notices if that stops being true.
func stripJSComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))

	const (
		code = iota
		lineComment
		blockComment
		str
	)
	mode, quote := code, byte(0)

	for i := 0; i < len(src); i++ {
		c := src[i]
		switch mode {
		case code:
			switch {
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				mode = lineComment
				i++
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				mode = blockComment
				i++
			case c == '\'' || c == '"' || c == '`':
				mode, quote = str, c
				out.WriteByte(c)
			default:
				out.WriteByte(c)
			}
		case str:
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
			} else if c == quote {
				mode = code
			}
		case lineComment:
			if c == '\n' {
				mode = code
				out.WriteByte(c)
			}
		case blockComment:
			if c == '\n' {
				out.WriteByte(c) // keep line numbers honest
			} else if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				mode = code
				i++
			}
		}
	}
	return out.String()
}

// TestStripJSCommentsKeepsCode guards the fence above against going blind.
//
// If the stripper ever ate real code, TestSigilIsNeverConsumedAsAnImage would
// keep passing while checking nothing — the failure mode that makes a fence
// worse than no fence at all.
func TestStripJSCommentsKeepsCode(t *testing.T) {
	cases := []struct{ name, must string }{
		{"sigil.js", "window.ForgeSigil"},
		{"console.js", "window.ForgeSigil.slot("},
		{"workbench.js", "window.ForgeSigil.place("},
	}
	for _, c := range cases {
		body, err := assetFS.ReadFile("assets/" + c.name)
		if err != nil {
			t.Fatal(err)
		}
		if got := stripJSComments(string(body)); !strings.Contains(got, c.must) {
			t.Errorf("stripping comments from %s lost %q. The <img> fence reads the "+
				"stripped source, so anything the stripper eats is code that fence no "+
				"longer checks.", c.name, c.must)
		}
	}

	// A comment quoting the forbidden shape must be removed; the same shape in
	// real code must survive.
	src := "/* <img src=\"/v1/meta/sigil\"> */\nvar a = '<img src=\"/v1/meta/sigil\">'; // <img>\n"
	got := stripJSComments(src)
	if strings.Count(got, "<img") != 1 {
		t.Errorf("stripJSComments(%q) = %q; want exactly the one <img> that is real code", src, got)
	}
	if lines := strings.Count(got, "\n"); lines != 2 {
		t.Errorf("stripJSComments collapsed line numbering: got %d newlines, want 2", lines)
	}
}
