package httpapi

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Colours live in one place, and these two fences are what keeps them there.
//
// # Why this is worth a test
//
// Before light mode, the palette was a `:root` block in shell.css plus about a
// hundred and fifty hex and rgba literals written out by hand across the other
// five stylesheets — #141417 for a rail here, #17171b for an input there, a
// dozen copies of #0e0f1a for text on a filled button. That was survivable while
// there was exactly one theme, because a literal and a token render the same
// thing when there is nothing to switch between.
//
// It stops being survivable the moment there are two grounds. A literal is a
// colour that cannot change with the theme, so every one left behind is a patch
// of the dark design showing through the light one — and the failure is silent:
// the page renders, nothing errors, and the defect is a grey box somebody
// notices weeks later on a screen nobody tested. That is precisely the kind of
// rot these fences exist to catch on the commit that introduces it.

var (
	// A hex colour, or an rgb()/rgba() function.
	colourLiteral = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|\brgba?\([^)]*\)`)
	rootBlockOpen = regexp.MustCompile(`:root[^{]*\{`)
	customProp    = regexp.MustCompile(`^\s*(--[a-zA-Z0-9-]+)\s*:\s*(.*)$`)
)

// TestStylesheetsDefineColoursAsTokens fails when a colour is written anywhere
// but shell.css's :root blocks.
//
// shell.css's :root is the exception rather than an oversight: it is where the
// values themselves have to be stated, and it is the only file the fence lets
// state them. Everything else refers to them by name.
func TestStylesheetsDefineColoursAsTokens(t *testing.T) {
	sheets, err := fs.Glob(assetFS, "assets/*.css")
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) < 5 {
		t.Fatalf("found %d stylesheet(s); the glob is not working", len(sheets))
	}

	for _, path := range sheets {
		name := strings.TrimPrefix(path, "assets/")
		body, err := assetFS.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		src := stripCSSComments(string(body))
		if name == "shell.css" {
			src = stripRootBlocks(src)
		}
		for _, lit := range colourLiteral.FindAllString(src, -1) {
			t.Errorf("%s writes the colour %s directly. Colours are declared once, as custom "+
				"properties in shell.css's :root block, and referred to by name everywhere else — "+
				"a literal cannot change with the theme, so this one renders the dark value on a "+
				"light page. Add a token for it in shell.css if none of the existing ones fit.",
				name, lit)
		}
	}
}

// TestEveryLightDarkTokenHasAPlainFallback fails when a `light-dark()` value is
// declared without an ordinary declaration of the same property above it.
//
// The pair is what makes the whole scheme safe to ship. A browser too old for
// `light-dark()` drops that declaration as invalid and keeps whatever came
// before it — the dark value, which is the design that shipped before this file
// had a light mode. With no declaration before it there is nothing to keep: the
// token resolves to nothing and the page arrives with no background, no ink and
// no borders.
//
// These pages are opened from mail clients on other people's phones, which is
// exactly the population where "too old for light-dark()" is not hypothetical.
func TestEveryLightDarkTokenHasAPlainFallback(t *testing.T) {
	body, err := assetFS.ReadFile("assets/shell.css")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(stripCSSComments(string(body)), "\n")

	// The property declared by the previous line, if it declared one.
	prev := ""
	pairs := 0
	for i, line := range lines {
		m := customProp.FindStringSubmatch(line)
		if m == nil {
			prev = ""
			continue
		}
		prop, value := m[1], m[2]
		if strings.Contains(value, "light-dark(") {
			if prev != prop {
				t.Errorf("shell.css:%d declares %s with light-dark() but the line above it does not "+
					"declare %s. Without a plain declaration first, a browser that does not support "+
					"light-dark() drops this one and the token resolves to nothing — the page renders "+
					"with that colour missing entirely. State the dark value on its own line first.",
					i+1, prop, prop)
			} else {
				pairs++
			}
		}
		prev = prop
	}
	if pairs < 20 {
		t.Errorf("only %d light-dark() pair(s) found; the parse is not working, so this fence is "+
			"passing without checking anything", pairs)
	}
}

// stripRootBlocks removes every `:root … { … }` block, braces balanced.
func stripRootBlocks(s string) string {
	var b strings.Builder
	for {
		m := rootBlockOpen.FindStringIndex(s)
		if m == nil {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:m[0]])
		depth, i := 1, m[1]
		for i < len(s) && depth > 0 {
			switch s[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			i++
		}
		s = s[i:]
	}
}
