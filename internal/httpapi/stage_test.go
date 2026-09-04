package httpapi

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

func workbenchHTML(t *testing.T) string {
	t.Helper()
	rr := httptest.NewRecorder()
	NewPageHandlers(testDeps()).Workbench(rr, httptest.NewRequest(http.MethodGet, "/workbench", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /workbench = %d", rr.Code)
	}
	return rr.Body.String()
}

// A declared panel that the page does not render is a panel with no producer.
//
// # Why this is the first fence and not an afterthought
//
// This repository's recurring failure is a declaration with nothing on the other
// end of it: a capability described in Go, documented, tested in isolation, and
// never actually mounted anywhere a person could reach. stagePanels() would fail
// exactly that way — silently, because a tab whose panel does not exist looks
// like a tab that has not been clicked yet.
func TestEveryDeclaredPanelIsOnThePage(t *testing.T) {
	html := workbenchHTML(t)
	panels := stagePanels()
	if len(panels) < 2 {
		t.Fatalf("only %d panel(s) declared; this fence is not looking at anything", len(panels))
	}
	for _, p := range panels {
		for _, want := range []string{
			`id="tab-` + p.ID + `"`,
			`id="panel-` + p.ID + `"`,
			`aria-controls="panel-` + p.ID + `"`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("panel %q is declared in stagePanels() but the workbench page has no %s — "+
					"the tab leads nowhere", p.ID, want)
			}
		}
		if !strings.Contains(html, ">"+p.Label+"</button>") {
			t.Errorf("panel %q renders no tab labelled %q", p.ID, p.Label)
		}
	}
}

// An empty panel must name connectors that are really declared and really refused.
//
// # What this catches, and when
//
// The day somebody links a SPICE engine or an FEA solver, the connector stops
// being unavailable and this test goes red — which is the point. The Simulation
// panel would otherwise go on telling every reader that no solver exists, in
// text nobody would think to look for, beside a solver that does.
//
// It also catches the cheaper mistake: a panel citing a connector name that was
// renamed or never existed, whose explanation would render as a heading over
// nothing.
func TestAnEmptyPanelNamesConnectorsThatAreActuallyRefused(t *testing.T) {
	declared := map[string]tools.Contract{}
	for _, tool := range tools.StandardUnavailableConnectors() {
		c := tool.Contract()
		declared[c.Name] = c
	}

	sawEmpty := false
	for _, p := range stagePanels() {
		if p.Available {
			// The converse, which is just as much a defect: an available panel
			// carrying a refusal would render an explanation for an emptiness
			// that is not there.
			if p.Reason != "" || len(p.Refused) != 0 {
				t.Errorf("panel %q is available and also carries a refusal (%d connector(s)); "+
					"one of the two is wrong", p.ID, len(p.Refused))
			}
			continue
		}
		sawEmpty = true
		if strings.TrimSpace(p.Reason) == "" {
			t.Errorf("panel %q is empty and says nothing about why; an unexplained blank panel is "+
				"the thing this whole mechanism exists to prevent", p.ID)
		}
		if len(p.Refused) == 0 {
			t.Errorf("panel %q is empty and names no connector; its explanation has no source and "+
				"cannot go stale visibly", p.ID)
		}
		for _, rc := range p.Refused {
			c, ok := declared[rc.Name]
			if !ok {
				t.Errorf("panel %q names connector %q, which is not in "+
					"tools.StandardUnavailableConnectors()", p.ID, rc.Name)
				continue
			}
			if c.Available {
				t.Errorf("panel %q says it is empty because %q has no backend, but %q is now "+
					"AVAILABLE. The panel can be filled and is still refusing to be.", p.ID, rc.Name, rc.Name)
			}
			if rc.Reason != c.UnavailableReason {
				t.Errorf("panel %q shows a reason for %q that is not the connector's own. "+
					"Two copies of one fact; they have already parted company.", p.ID, rc.Name)
			}
		}
	}
	if !sawEmpty {
		t.Fatal("no panel is declared empty, so nothing above was checked. If this build gained a " +
			"CAD kernel and a solver, delete this fence deliberately rather than leaving it vacuous.")
	}
}

// The explanation must be on the PAGE, not merely in the struct.
//
// Without this, everything above can pass while the panel renders blank: the
// declaration would be correct, well tested, and unreachable. That is the same
// defect as a panel with no producer, one level down.
func TestAnEmptyPanelSaysWhyOnThePage(t *testing.T) {
	html := workbenchHTML(t)
	for _, p := range stagePanels() {
		if p.Available {
			continue
		}
		// The first sentence of each paragraph, escaped the way the template
		// escapes it. Whole paragraphs are not compared: html/template rewrites
		// quotes and ampersands, and a fence that fails on punctuation gets
		// deleted rather than fixed.
		for _, para := range paragraphs(p.Reason) {
			probe := firstWords(para, 8)
			if !strings.Contains(html, probe) {
				t.Errorf("panel %q does not render its reason on the page; looked for %q", p.ID, probe)
			}
		}
		for _, rc := range p.Refused {
			if !strings.Contains(html, rc.Name) {
				t.Errorf("panel %q does not name connector %q on the page", p.ID, rc.Name)
			}
			if probe := firstWords(rc.Reason, 8); !strings.Contains(html, probe) {
				t.Errorf("panel %q names %q without its refusal; looked for %q", p.ID, rc.Name, probe)
			}
		}
	}
}

func firstWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}

// Every state the ledger can hold has a chip to render it in.
//
// # The defect this is for
//
// The Checks panel builds a class name from the value it is showing —
// `wbchip-v-<verification>`, `wbchip-d-<disposition>`. A vocabulary that grows
// on the Go side therefore renders as an UNSTYLED chip on the browser side, and
// it does so quietly: the text is right, the colour is missing, and the one
// state anybody would add next is a new kind of failure. A grey "failed" that
// should have been red is the worst possible version of this bug.
//
// Held against the domain's own lists rather than a copy, so adding a state in
// workspace/artifact.go is what turns this red.
func TestEveryVerificationAndDispositionHasAChip(t *testing.T) {
	css, err := assetFS.ReadFile("assets/workbench.css")
	if err != nil {
		t.Fatal(err)
	}
	sheet := string(css)

	if len(workspace.Verifications()) == 0 || len(workspace.Dispositions()) == 0 {
		t.Fatal("the domain reports no states; this fence is looking at nothing")
	}
	for _, v := range workspace.Verifications() {
		if !definesClass(sheet, "wbchip-v-"+string(v)) {
			t.Errorf("verification state %q renders as an unstyled chip: workbench.css has no "+
				".wbchip-v-%s", v, v)
		}
	}
	for _, d := range workspace.Dispositions() {
		if !definesClass(sheet, "wbchip-d-"+string(d)) {
			t.Errorf("disposition %q renders as an unstyled chip: workbench.css has no "+
				".wbchip-d-%s", d, d)
		}
	}
}

// Classes a SCRIPT writes must be styled too.
//
// # Why the existing fence does not cover this
//
// TestNoPageUsesAClassStyledOnlyInAStylesheetItDoesNotLoad reads the rendered
// HTML, which is exactly the half of the interface that is easiest to get right.
// Most of what a person sees on the workbench is written by a script, and a
// class that is styled NOWHERE — a typo, a rename that missed one call site, a
// panel shipped before its stylesheet — renders as unformatted text in the
// middle of the page and is invisible to every test that checks content.
//
// The rule for a class name built by concatenation (`'wbchip wbchip-' + state`)
// is that the FAMILY must exist: at least one class in the stylesheets starts
// with that prefix. The per-value check for the one family that matters is
// TestEveryVerificationAndDispositionHasAChip above.
func TestScriptsOnlyWriteClassesTheStylesheetsDefine(t *testing.T) {
	sheets, err := fs.Glob(assetFS, "assets/*.css")
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]bool{}
	classRe := regexp.MustCompile(`\.([a-zA-Z][\w-]*)`)
	for _, path := range sheets {
		body, err := assetFS.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range strings.Split(string(body), "{") {
			if i := strings.LastIndex(block, "}"); i >= 0 {
				block = block[i+1:]
			}
			for _, m := range classRe.FindAllStringSubmatch(stripCSSComments(block), -1) {
				defined[m[1]] = true
			}
		}
	}
	if len(defined) < 40 {
		t.Fatalf("only %d class(es) parsed from %d stylesheet(s); the parse is not working",
			len(defined), len(sheets))
	}
	hasFamily := func(prefix string) bool {
		for c := range defined {
			if strings.HasPrefix(c, prefix) {
				return true
			}
		}
		return false
	}

	// The value of a class attribute inside a JavaScript string, up to whichever
	// comes first: the closing double quote of the attribute, or the single
	// quote that ends the JS literal. The second case means the last token is a
	// prefix that the script completes at runtime.
	attrRe := regexp.MustCompile(`class="([^"']*)(["'])`)
	scripts, err := fs.Glob(assetFS, "assets/*.js")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, path := range scripts {
		body, err := assetFS.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimPrefix(path, "assets/")
		for _, m := range attrRe.FindAllStringSubmatch(string(body), -1) {
			tokens := strings.Fields(m[1])
			partial := m[2] == "'"
			for i, class := range tokens {
				checked++
				if partial && i == len(tokens)-1 {
					if !hasFamily(strings.TrimSuffix(class, "-")) {
						t.Errorf("%s writes a class beginning %q and no stylesheet defines "+
							"anything in that family — every value it can produce renders unstyled",
							name, class)
					}
					continue
				}
				if !defined[class] {
					t.Errorf("%s writes class %q, which no stylesheet defines. It will render "+
						"as unformatted text.", name, class)
				}
			}
		}
	}
	if checked < 40 {
		t.Fatalf("only %d class(es) found across %d script(s); the scan is not working",
			checked, len(scripts))
	}
}

// Every node kind the graph can hold has a column to be drawn in.
//
// # The defect this exists for
//
// The Diagram places each node by KIND. A kind that matches no column is a node
// that is drawn NOWHERE — and the picture that results looks complete, because
// nothing on it says a node is missing. That is the partial listing this
// codebase keeps finding, in the one form where it is hardest to notice: an
// absence in a drawing.
//
// Held against workspace.Kinds() rather than a copy, so adding a kind in the
// domain is what turns this red. The panel also has a belt to this brace — a
// kind matching no column lands in a visibly odd "not in any column" column
// rather than vanishing — but a bug somebody has to notice is not a fence.
func TestEveryNodeKindHasAColumn(t *testing.T) {
	roles := stageNodeRoles()
	if len(roles) < 2 {
		t.Fatalf("only %d column(s) declared; this fence is looking at nothing", len(roles))
	}

	placed := map[string][]string{} // kind -> columns naming it
	known := map[string]bool{}
	for _, k := range workspace.Kinds() {
		known[string(k.Kind)] = true
	}
	if len(known) < 5 {
		t.Fatalf("the domain reports %d kind(s); this fence is looking at nothing", len(known))
	}

	for _, r := range roles {
		for _, kind := range strings.Fields(r.Kinds) {
			if !known[kind] {
				t.Errorf("column %q names %q, which is not a node kind. Nothing will ever be "+
					"drawn there, and whatever it was meant to hold is drawn nowhere.", r.ID, kind)
			}
			placed[kind] = append(placed[kind], r.ID)
		}
	}
	for kind := range known {
		switch len(placed[kind]) {
		case 1:
		case 0:
			t.Errorf("node kind %q has no column: every %s in a project would be drawn nowhere, "+
				"in a diagram that would still look complete", kind, kind)
		default:
			t.Errorf("node kind %q is in %d columns (%v); it would be drawn twice and the two "+
				"copies would have different relations", kind, len(placed[kind]), placed[kind])
		}
	}
}

// Every column has a colour, and the page carries the columns at all.
//
// The node's colour is built as wbn-<column>, so a column added in Go without a
// rule here renders as an unmarked box — legible, but carrying none of the
// information the colour exists to carry. `other` is checked too: it is the
// column a kind lands in when it matches none, which is exactly when somebody
// needs to see that something is wrong.
func TestEveryDiagramColumnHasANodeColour(t *testing.T) {
	css, err := assetFS.ReadFile("assets/workbench.css")
	if err != nil {
		t.Fatal(err)
	}
	sheet := string(css)

	ids := []string{"other"}
	for _, r := range stageNodeRoles() {
		ids = append(ids, r.ID)
	}
	for _, id := range ids {
		if !definesClass(sheet, "wbn-"+id) {
			t.Errorf("diagram column %q draws unmarked boxes: workbench.css has no .wbn-%s", id, id)
		}
	}

	// And the columns must reach the page, or the script reads an empty list and
	// draws every node into the fallback column.
	html := workbenchHTML(t)
	for _, r := range stageNodeRoles() {
		if !strings.Contains(html, `data-role="`+r.ID+`"`) {
			t.Errorf("column %q is declared in Go and is not on the page; the diagram cannot "+
				"read it", r.ID)
		}
		if !strings.Contains(html, `data-kinds="`+r.Kinds+`"`) {
			t.Errorf("column %q reaches the page without its kinds (%q)", r.ID, r.Kinds)
		}
	}
}

// definesClass reports whether the stylesheet defines exactly this class.
//
// # Why this is not strings.Contains
//
// It was, and the drill caught it: renaming .wbn-account to .wbn-accountable
// left the fence GREEN, because the old name is a prefix of the new one. A
// substring match answers "does some class start with this" and reports a
// missing rule as a present one — which is the direction a fence must never
// fail in. The class has to END here: the next character must be one that
// cannot continue a class name.
func definesClass(sheet, name string) bool {
	return regexp.MustCompile(`\.` + regexp.QuoteMeta(name) + `(?:[^\w-]|$)`).MatchString(sheet)
}
