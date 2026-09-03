package media

import (
	"regexp"
	"strings"
)

// Technical readback for FORGE's spoken voice (PRD AUD-04).
//
// # What this is for
//
// AUD-04 asks that numbers, units, tolerances and identifiers come back in a
// form a listener can write down and get right. "2.5mm" is unambiguous on
// screen and ambiguous out loud; "v0.2.0" read as "vee zero point twenty" is a
// version number that no longer exists. The fix is to rewrite the text before
// it is synthesised, leaving what is displayed and recorded untouched.
//
// # Why there are two copies of this
//
// The workbench speaks through the browser's own speech synthesis, so it
// normalises in `internal/httpapi/assets/voice.js`. A room speaks through the
// server, because everybody in it has to hear the same voice at the same
// instant (see speech.go). Those are two different runtimes, and the same
// requirement applies to both.
//
// The rules are therefore duplicated here rather than shared, the same trade
// speech.go makes for the sample rate, and for the same reason: the alternative
// is a network round trip per utterance in the browser, or generated JavaScript,
// and neither is worth it for twelve substitutions. What makes the duplication
// safe is that it is fenced — TestTheReadbackRulesHaveNotDrifted compares this
// table against the JavaScript one and fails when a rule is added to one side
// only. If you change either copy, change both.
//
// # Why the order is fixed
//
// The rules are applied in sequence and the sequence carries meaning. The
// identifier rule must run before markdown stripping, because stripping removes
// the `_` that identifies an identifier. Reordering this table changes what
// FORGE says.

// readbackRule is one substitution. Most are a pattern and a replacement; the
// ones that spell a match out character by character need a function.
type readbackRule struct {
	re  *regexp.Regexp
	rep string
	fn  func(match []string) string
}

// readbackRules mirrors readable() in internal/httpapi/assets/voice.js, rule for
// rule and in the same order. These are the substitutions that actually change
// meaning when a listener writes down what they heard; ordinary prose is left
// alone.
var readbackRules = []readbackRule{
	// Dotted-number strings — versions, IP addresses, build numbers — segment by
	// segment.
	//
	// Two or more dots, any number of segments. It was written as exactly three
	// segments, which read "1.2.3.4" as "1 point 2 point 3.4": the first three
	// spoken and the fourth left as a decimal, which is worse than either
	// treatment on its own. Segment count is not something the rule should have
	// an opinion about.
	//
	// Two dots minimum, so a plain decimal is left alone. "2.5" and "$2.50" are
	// already read correctly and rewriting them buys nothing.
	//
	// No \b at the front, deliberately. There is no word boundary between the
	// "v" and the "0" of "v0.2.0", so a leading \b skips the most common way a
	// version is written — which it did on both copies of this rule until it was
	// noticed. Omitting it also covers "rev1.2.3" and "V2.0.1" without naming
	// them, and a lookbehind (the precise alternative) does not exist in RE2, so
	// the two copies could not have stayed identical with one.
	//
	// The \b at the end stays: it keeps "1.2.3mm" out of here and leaves it to
	// the unit rules below, which is where a measurement belongs.
	{
		re: regexp.MustCompile(`\d+(?:\.\d+){2,}\b`),
		fn: func(m []string) string {
			return strings.Join(strings.Split(m[0], "."), " point ")
		},
	},

	// Coordinates, spoken with their axes and their signs.
	//
	// A position leaves the geometry model as "(12.5 mm, 0 mm, -40 mm)" — see
	// position() in internal/domain/geometry/compare.go. Said as written that is
	// three numbers in a row: parentheses and commas are silent, so a listener
	// hears "12.5 millimetres, 0 millimetres, 40 millimetres" with nothing to say
	// which number is which axis. The minus is silent too, which puts the part
	// 80mm from where it is. Both are the AUD-04 failure exactly: correct on
	// screen, impossible to write down from the voice.
	//
	// Naming the axes is the whole fix. The frame those axes belong to is
	// deliberately NOT added here. It is not in the text, and this layer rewrites
	// what it is handed rather than inventing premises (RSN-06); a coordinate
	// that needs to carry "assembly origin, Y up" has to carry it from the
	// producer, which is what Frame in internal/domain/geometry/units.go already
	// says. A readback rule that supplied a frame would be stating a datum nobody
	// measured.
	//
	// Three segments only. A pair in prose ("(1, 2)") is far more likely to be a
	// list than a position, and no position in this domain has two axes.
	//
	// Why this rule exists, and the drill that proves the fence still fires:
	// docs/bugfix/2026-09-03-coordinates-read-back-without-axes-or-sign.md
	//
	// Placed ahead of the unit rules so that a unit is still in its written form
	// when the triple is split; spelling units stays the unit rules' job, in one
	// place, rather than being repeated inside this pattern.
	{
		re: regexp.MustCompile(coordinateExpr),
		fn: func(m []string) string {
			return "X " + spokenSign(m[1]) +
				", Y " + spokenSign(m[2]) +
				", Z " + spokenSign(m[3])
		},
	},

	// Units, spelled out. "5mm" heard as "five em em" is not a measurement.
	{re: regexp.MustCompile(`(\d)\s?mm\b`), rep: "${1} millimetres"},
	{re: regexp.MustCompile(`(\d)\s?cm\b`), rep: "${1} centimetres"},
	{re: regexp.MustCompile(`(\d)\s?kg\b`), rep: "${1} kilograms"},
	{re: regexp.MustCompile(`(\d)\s?ms\b`), rep: "${1} milliseconds"},
	{re: regexp.MustCompile(`(\d)\s?Nm\b`), rep: "${1} newton metres"},
	{re: regexp.MustCompile(`(\d)\s?°C\b`), rep: "${1} degrees Celsius"},

	// ± is silent in most voices, which turns a tolerance into a bare number.
	{re: regexp.MustCompile(`±`), rep: " plus or minus "},

	// Identifiers are read as letters and digits rather than as a word. Must
	// precede the markdown rule, which removes the underscore this matches on.
	{
		re: regexp.MustCompile(`\b([a-z]{3})_([0-9A-Z]{6})[0-9A-Z]*\b`),
		fn: func(m []string) string {
			return spellOut(m[1]) + " " + spellOut(m[2]) + " and so on"
		},
	},

	// Markdown that would otherwise be read aloud as punctuation.
	{re: regexp.MustCompile("[*_`#]"), rep: ""},

	// Collapse whatever the rules above left behind.
	{re: regexp.MustCompile(`\s+`), rep: " "},
}

// Readable rewrites text so a listener can transcribe it correctly (PRD AUD-04).
//
// It is applied to what FORGE says, not to what it writes: the transcript keeps
// the original, so the room shows "2.5mm ±0.1" while the voice says "2.5
// millimetres plus or minus 0.1". Those are the same fact at two different
// resolutions, and a person reading the record afterwards wants the first one.
func Readable(text string) string {
	for _, r := range readbackRules {
		if r.fn == nil {
			text = r.re.ReplaceAllString(text, r.rep)
			continue
		}
		text = r.re.ReplaceAllStringFunc(text, func(match string) string {
			return r.fn(r.re.FindStringSubmatch(match))
		})
	}
	return strings.TrimSpace(text)
}

// coordinateSegment matches one axis value: a signed number and, optionally, the
// unit written after it. Composed into coordinateExpr three times rather than
// typed out three times, because the three have to stay identical.
const coordinateSegment = `(-?\d+(?:\.\d+)?(?:\s?[A-Za-z°]+)?)`

// coordinateExpr matches a parenthesised triple. Mirrors COORD in voice.js.
var coordinateExpr = `\(\s*` + coordinateSegment +
	`\s*,\s*` + coordinateSegment +
	`\s*,\s*` + coordinateSegment + `\s*\)`

// spokenSign makes a leading minus audible. "-" is silent in the voices this is
// synthesised through, so "-40 mm" is heard as "40 millimetres": a coordinate on
// the wrong side of the datum, the same class of failure as the silent ± above.
//
// Scoped to coordinates on purpose. A general rule would also rewrite dates
// ("2026-09-03") and ranges ("5-10mm"), where the hyphen is not a sign.
func spokenSign(seg string) string {
	if strings.HasPrefix(seg, "-") {
		return "minus " + strings.TrimPrefix(seg, "-")
	}
	return seg
}

// spellOut separates characters so they are heard individually: "req" becomes
// "r e q", which a listener writes down as three letters rather than a word.
func spellOut(s string) string {
	return strings.Join(strings.Split(s, ""), " ")
}
