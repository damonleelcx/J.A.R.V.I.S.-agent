package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The console's panels each have a producer.
//
// # What this guards
//
// A panel that renders and is never filled, and an endpoint that is written and
// never called, look identical from the outside: an empty card. That is exactly
// how this wave's bug presented — "the console is not showing past conversations
// or sessions, and past artifacts" — except that then the card did not exist at
// all and neither did the query behind it.
//
// So both halves are asserted together: the markup has the mount point, and the
// script fetches something to put in it. Either half alone passes while the
// panel stays blank.
//
// Deliberately string matching. The alternative is running the page, which needs
// a browser and a session; this catches the realistic regression — somebody
// tidying a card out of the template, or replacing a fetch during a refactor —
// at no cost. It cannot catch a fetch that runs and renders nothing, which the
// integration tests cover on the data side.
//
// See docs/bugfix/2026-09-08-history-was-unreachable.md
func TestEveryConsolePanelHasAProducer(t *testing.T) {
	js, err := assetFS.ReadFile("assets/console.js")
	if err != nil {
		t.Fatalf("reading console.js: %v", err)
	}
	script := string(js)

	for _, tc := range []struct{ panel, fetches, why string }{
		{"conversations", "/v1/conversations",
			"history is durable on the server and would be reachable only through a key in " +
				"one browser's localStorage again"},
		{"artifacts", "/v1/geometry?project_id=",
			"what was built would be visible only in the workbench's own Variants rail"},
		{"projects", "/v1/projects", "the console cannot say where any work lives"},
		{"goals", "/v1/goals", "running work is invisible"},
		{"approvals", "/v1/approvals", "nothing tells a person something is waiting on them"},
	} {
		t.Run(tc.panel, func(t *testing.T) {
			// The mount point the renderer writes into.
			if !strings.Contains(script, "$('"+tc.panel+"')") {
				t.Errorf("console.js never writes into #%s, so the card renders empty. %s",
					tc.panel, tc.why)
			}
			// The request that fills it.
			if !strings.Contains(script, tc.fetches) {
				t.Errorf("console.js never requests %s, so #%s has no producer. %s",
					tc.fetches, tc.panel, tc.why)
			}
		})
	}
}

// The console page carries the mount points the script writes into.
//
// The other half of the pair above: a script that fills #conversations is inert
// if no element has that id, and it fails silently — getElementById returns null
// and the panel is simply never there. Nothing else in the build would notice.
func TestTheConsolePageHasEveryMountPoint(t *testing.T) {
	pages := NewPageHandlers(testDeps())
	rr := httptest.NewRecorder()
	pages.Console(rr, httptest.NewRequest(http.MethodGet, "/console", nil))
	body := rr.Body.String()
	if len(body) < 200 {
		t.Fatalf("the console page rendered only %d bytes; this fence would pass vacuously", len(body))
	}

	for _, id := range []string{"projects", "approvals", "conversations", "artifacts", "goals"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("the console page has no element with id %q, so its panel cannot be filled "+
				"however well the script works", id)
		}
	}
}
