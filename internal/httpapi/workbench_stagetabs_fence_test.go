package httpapi

import (
	"regexp"
	"strings"
	"testing"
)

// The stage's tab strip scrolls inside itself on a phone, rather than widening the page.
//
// Seen in the browser (2026-09-17, docs/spikes/2026-09-17-workbench-viewport): at 420 px
// the strip was 548 px wide and the page's layout 561 px, although workbench.css already
// gave `.stagetabs` min-width: 0 and overflow-x: auto for exactly this. Its first rule
// is `flex: 0 0 auto`, and a flex item that may not shrink never gets narrower than its
// content, so the overflow never happened inside the strip. This reads every top-level
// `.stagetabs` rule in cascade order, as the browser applies them, and holds the three
// declarations the strip needs to be in force together.
func TestWorkbenchStageTabsScrollInsideTheirStripOnAPhone(t *testing.T) {
	css, err := assetFS.ReadFile("assets/workbench.css")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	rules := 0
	depth := 0 // braces open before the current piece
	for _, block := range strings.Split(stripCSSComments(string(css)), "}") {
		open := strings.LastIndex(block, "{")
		if open < 0 {
			depth-- // this "}" closes an at-rule
			continue
		}
		outer := depth + strings.Count(block[:open], "{") // braces around the rule's own
		selector := strings.TrimSpace(block[strings.LastIndexAny(block[:open], ";{}")+1 : open])
		depth = outer // the rule's own brace is closed by the "}" this piece ended at
		if outer != 0 || selector != ".stagetabs" {
			continue
		}
		rules++
		for _, decl := range strings.Split(block[open+1:], ";") {
			name, value, ok := strings.Cut(decl, ":")
			if !ok {
				continue
			}
			name, value = strings.TrimSpace(name), strings.TrimSpace(value)
			switch name {
			case "flex":
				f := strings.Fields(value)
				switch {
				case value == "none":
					got["flex-shrink"] = "0"
				case len(f) >= 2:
					got["flex-shrink"] = f[1]
				default:
					got["flex-shrink"] = "1"
				}
			case "flex-shrink", "min-width", "overflow-x":
				got[name] = value
			}
		}
	}
	if rules < 2 {
		t.Fatalf("found %d top-level .stagetabs rules; the fixture expects the base rule and the narrow-width correction", rules)
	}
	if got["min-width"] != "0" || got["overflow-x"] != "auto" {
		t.Errorf(".stagetabs ends with min-width %q and overflow-x %q; the strip must be allowed to be narrower than its tabs and scroll them", got["min-width"], got["overflow-x"])
	}
	if got["flex-shrink"] == "0" || got["flex-shrink"] == "" {
		t.Errorf(".stagetabs ends with flex-shrink %q: it cannot get narrower than its tabs, so on a phone the page widens instead of the strip scrolling", got["flex-shrink"])
	}
}

// No page writes a colour literal in a style attribute.
//
// TestStylesheetsDefineColoursAsTokens keeps literals out of the stylesheets, and the
// section-cut picker's style was written on the element in pages.go instead, with the
// dark ground as #0f131b — on the light theme a black box with near-black text in it.
// Seen in the browser checking the build goal card on 2026-09-17
// (docs/spikes/2026-09-17-workbench-viewport).
func TestPagesWriteNoColourLiteralInAStyleAttribute(t *testing.T) {
	styles := regexp.MustCompile(`style="([^"]*)"`).FindAllStringSubmatch(pageTemplates, -1)
	for _, m := range styles {
		if lit := colourLiteral.FindString(m[1]); lit != "" {
			t.Errorf("pages.go writes the colour %s in style=%q; it cannot change with the theme. Put the rule in a "+
				"stylesheet with a token from shell.css", lit, m[1])
		}
	}
	css, err := assetFS.ReadFile("assets/workbench.css")
	if err != nil {
		t.Fatal(err)
	}
	if rule := ruleFor(stripCSSComments(string(css)), ".sliders select"); !strings.Contains(rule, "background: var(--") || !strings.Contains(rule, "color: var(--ink)") {
		t.Errorf("the section-cut picker has no themed rule in workbench.css (.sliders select): %q", rule)
	}
}
