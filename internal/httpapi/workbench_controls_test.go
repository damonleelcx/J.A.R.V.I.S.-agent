package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The camera buttons must be selected by their container, never by data-view alone.
//
// # What went wrong
//
// `data-view` means two unrelated things in this page. It names a CAMERA on the
// four buttons in .viewctl, and it names a PANE on the narrow layout's
// Talk/Stage/Work tabs — and, critically, on `.wb-body` itself, where mobileNav
// writes the current pane so the stylesheet can act on it.
//
// initControls bound its click handler with a document-wide
// querySelectorAll('[data-view]'), so it attached to the ENTIRE WORKBENCH BODY.
// Every click anywhere in the workbench then bubbled to that handler and called
// studio.viewFrom('stage') — a pane name, not a camera — while clearing
// aria-pressed on all four camera buttons. Iso/Front/Top/Side did nothing: each
// button's own handler ran, then the body's ran after it and undid the result.
//
// mobileNav's own comment states the intent this broke: the attribute is held on
// .wb-body "so the CSS is the only thing that acts on it".
//
// # Why a fence rather than a comment
//
// The mistake is invisible at the call site — `[data-view]` reads like "the view
// buttons" and is only wrong because of a second, distant use of the same
// attribute. The next person to touch this file has no reason to suspect it.
//
// See docs/bugfix/2026-09-08-view-buttons-bound-to-the-whole-body.md
func TestCameraButtonsAreScopedToTheirContainer(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatalf("reading workbench.js: %v", err)
	}
	js := codeOnly(string(b))

	if strings.Contains(js, "querySelectorAll('[data-view]')") {
		t.Error("workbench.js selects [data-view] document-wide. That attribute is also on " +
			".wb-body and on the Talk/Stage/Work tabs, so this binds the camera handler to the " +
			"whole workbench body: every click anywhere calls studio.viewFrom() with a PANE name " +
			"and clears the camera selection, and Iso/Front/Top/Side stop working entirely. " +
			"Scope it to '.viewctl [data-view]'.")
	}
	if !strings.Contains(js, "querySelectorAll('.viewctl [data-view]')") {
		t.Error("the camera buttons are no longer selected by their container. If they are found " +
			"some other way, make sure it cannot also match .wb-body — mobileNav writes the " +
			"current pane there, and matching it binds this handler to the entire body.")
	}
}

// The markup must keep the container that scoping depends on.
//
// The fence above is only meaningful while .viewctl exists and still holds the
// four buttons. Renaming the container without updating the selector would leave
// the handler bound to nothing — the buttons would go quiet again, this time
// silently rather than by being overridden.
func TestTheViewBarStillHoldsTheCameraButtons(t *testing.T) {
	pages := NewPageHandlers(testDeps())
	rr := httptest.NewRecorder()
	pages.Workbench(rr, httptest.NewRequest(http.MethodGet, "/workbench", nil))
	body := rr.Body.String()
	if len(body) < 200 {
		t.Fatalf("the workbench rendered only %d bytes; this fence would pass vacuously", len(body))
	}

	if !strings.Contains(body, `class="viewctl"`) {
		t.Fatal("the workbench has no .viewctl container. The camera handler is scoped to it, " +
			"so the buttons are now bound to nothing and Iso/Front/Top/Side do nothing at all.")
	}
	for _, view := range []string{"iso", "front", "top", "side"} {
		if !strings.Contains(body, `data-view="`+view+`"`) {
			t.Errorf("the %q camera button is gone from the workbench", view)
		}
	}
}

// codeOnly drops whole-line comments before scanning.
//
// Necessary because the comment explaining this very bug has to QUOTE the
// forbidden selector to explain it, and a fence that cannot tell code from the
// prose describing it fails on its own documentation — which is how fences get
// deleted. Whole-line only: enough for the block comments this file scans, and
// it does not pretend to parse JavaScript.
func codeOnly(js string) string {
	var out strings.Builder
	for _, line := range strings.Split(js, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "//") {
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}
