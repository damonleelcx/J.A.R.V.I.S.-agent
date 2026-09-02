package agent

import (
	"regexp"
	"sort"
	"strings"
)

// Recalled figures: the honesty gap this file exists to close.
//
// # What went wrong
//
// FORGE proposed a NEMA 17 motor mount and wrote, in its `assumptions` list:
//
//	"centered NEMA 17 bolt pattern (holes at ±20.5 mm on both axes)"
//
// That is a 41 mm pattern. NEMA 17 is 31 mm square. The figure is wrong, it is
// specific, and it appeared in the one field whose entire job is honesty. Across
// four runs of the identical prompt the same model produced 31 mm correctly
// twice and the fabricated figure once — so this is not a bad model, it is an
// unguarded one. Raw data in docs/spikes/2026-09-02-zoo-text-to-cad/.
//
// # Why the prompt alone cannot fix it
//
// The soul already forbids this. `no-fabrication` says, verbatim: "I do not
// invent measurements, citations, standards, file contents, test results, or the
// outcome of anything I did not observe." The instruction was there and was not
// followed, because a fabricated standard is indistinguishable — from the
// inside — from a remembered one. Adding a firmer sentence to the prompt would
// be asking the failing component to catch its own failure.
//
// # The fix, and why it is labelling rather than blocking
//
// Two categories were being laundered into one:
//
//   - a dimension FORGE CHOSE ("I picked 60 mm because you did not say") — an
//     assumption, and legitimately so;
//   - a figure FORGE RECALLED from a published standard ("NEMA 17 is 31 mm") —
//     not an assumption at all, but a claim about the world that this deployment
//     has no way to check.
//
// The second is detected here, server-side, from the reply's own text, and
// surfaced to the reader as recalled-and-unchecked. It is not rejected and not
// rewritten: refusing the turn would block the main flow over a sentence, and
// silently deleting a number would destroy information the person may need. The
// same shape of fix as "Drawn approximately" for shapes the renderer cannot
// draw — the system says what it did instead of hiding it.
//
// # What this does NOT claim
//
// It catches NAMED standards from the table below. A figure recalled from an
// unnamed source ("a typical stepper flange is 42 mm") is not caught, and the
// banner does not say it is. Partial coverage that says so beats total coverage
// that is asserted.

// StandardsClaim is one FRAGMENT of a reply that referred to a published
// standard.
//
// # Why it is grouped by fragment and not by standard
//
// The first version emitted one claim per standard and attached every figure in
// the sentence to each of them. On the first live run that produced
// "M3 — 42.3 mm, ±0.1 mm, 31 mm, 3.2 mm, 3.5 mm" from a sentence that mentioned
// both M3 screws and a NEMA 17 face: an M3 screw is not 42.3 mm, and the panel
// had invented a pairing in the course of warning about invented numbers.
//
// Attributing each figure to its nearest standard was the obvious alternative
// and was rejected: proximity in prose is a guess, and a guessed pairing is the
// same failure one step quieter. So a claim reports only what is actually known —
// this sentence names these standards and contains these figures — and carries
// the sentence, so the reader pairs them by reading it.
type StandardsClaim struct {
	// Standards are the references named in this fragment, e.g. "NEMA 17".
	Standards []string `json:"standards"`
	// Figures are the dimensioned numbers in the same fragment. Empty means
	// conformance was claimed without quoting anything, which is a claim too.
	Figures []string `json:"figures,omitempty"`
	// Where the claim appeared: "assumption", "part note", "detail", "spoken".
	Where string `json:"where"`
	// Text is the fragment itself, so the pairing is done by a reader looking at
	// the sentence rather than by this file guessing at it.
	Text string `json:"text"`
}

// standardsFamilies enumerates the reference systems worth catching.
//
// A table rather than a chain of conditions, so adding a family is one line and
// the set is inspectable. Each entry is a regexp fragment matching the family
// token AND its designation, because "ISO" on its own is prose and "ISO 4762" is
// a claim.
var standardsFamilies = []struct {
	Name    string
	Pattern string
}{
	// Motor and mechanical frames
	{"NEMA", `NEMA\s?-?\s?\d{1,3}[A-Z]?`},
	{"IEC frame", `IEC\s?\d{2,3}[A-Z]?`},
	// General engineering standards bodies
	{"ISO", `ISO\s?\d{3,5}(?:-\d+)?`},
	{"DIN", `DIN\s?\d{3,5}(?:-\d+)?`},
	{"ANSI", `ANSI\s?[A-Z]?\d{1,3}(?:\.\d+)*`},
	{"ASME", `ASME\s?[A-Z]?\d{1,3}(?:\.\d+)*`},
	{"ASTM", `ASTM\s?[A-Z]\d{1,4}`},
	{"JIS", `JIS\s?[A-Z]\s?\d{3,4}`},
	{"EN", `\bEN\s?\d{3,5}(?:-\d+)?`},
	{"SAE", `SAE\s?J?\d{3,4}`},
	{"BS", `\bBS\s?\d{3,5}`},
	{"GB", `\bGB/?T?\s?\d{3,5}`},
	{"UL", `\bUL\s?\d{2,4}`},
	// Fasteners and threads: a size IS a standard claim, and the commonest one.
	{"metric thread", `\bM(?:1\.6|2|2\.5|3|4|5|6|8|10|12|14|16|20|24)\b`},
	{"unified thread", `\b(?:#\d{1,2}|\d/\d{1,2}|\d)-(?:\d{2})\s?UN[CF]\b`},
	{"pipe thread", `\b(?:NPT|BSPP?|BSPT)\b`},
	// Ratings and gauges
	{"IP rating", `\bIP\s?[0-6][0-9K]\b`},
	{"AWG", `\b\d{1,2}\s?AWG\b`},
	// Bearings and common part series
	{"bearing series", `\b6[0-9]{3}(?:-2RS|ZZ)?\b`},
}

var standardsRE = func() *regexp.Regexp {
	parts := make([]string, 0, len(standardsFamilies))
	for _, f := range standardsFamilies {
		parts = append(parts, "(?:"+f.Pattern+")")
	}
	return regexp.MustCompile(`(?i)` + strings.Join(parts, "|"))
}()

// figureRE matches a dimensioned number: an optional ± or sign, digits, and a
// unit. A bare number is deliberately NOT matched — "NEMA 17" contains one, and
// flagging the designation as a figure would make every claim look quantitative.
var figureRE = regexp.MustCompile(
	`(?i)[±+\-]?\s?\d+(?:\.\d+)?\s?(?:mm|cm|m\b|µm|um|in\b|inch(?:es)?|"|°|deg|degrees?|` +
		`nm\b|Nm|N\b|kg|g\b|lb|A\b|mA|V\b|W\b|Hz|kHz|rpm|bar|psi|kPa|MPa)`)

// FindStandardsClaims scans a reply for references to published standards.
//
// Order is deterministic — by where, then by the text — so two runs over the
// same reply produce the same list and a test can assert on it.
func FindStandardsClaims(r *Reply) []StandardsClaim {
	if r == nil {
		return nil
	}
	var out []StandardsClaim

	// Speech and detail are prose; the reply may cite a standard with no
	// geometry attached at all, and that is exactly as unverifiable.
	for _, frag := range splitSentences(r.Speech) {
		out = append(out, claimsIn(frag, "spoken")...)
	}
	for _, frag := range splitSentences(r.Detail) {
		out = append(out, claimsIn(frag, "detail")...)
	}

	if r.Prototype != nil {
		// Assumptions first: this is the field the fabricated figure appeared
		// in, and the one a reader trusts most.
		for _, a := range r.Prototype.Assumptions {
			out = append(out, claimsIn(a, "assumption")...)
		}
		for _, n := range r.Prototype.NotVerified {
			out = append(out, claimsIn(n, "not-verified note")...)
		}
		for _, p := range r.Prototype.Parts {
			for _, frag := range splitSentences(p.Note) {
				out = append(out, claimsIn(frag, "part note")...)
			}
		}
	}

	out = dedupeClaims(out)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Where != out[j].Where {
			return out[i].Where < out[j].Where
		}
		return out[i].Text < out[j].Text
	})
	return out
}

func claimsIn(fragment, where string) []StandardsClaim {
	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return nil
	}
	names := dedupeFold(normaliseAll(standardsRE.FindAllString(fragment, -1)))
	if len(names) == 0 {
		return nil
	}
	return []StandardsClaim{{
		Standards: names,
		Figures:   dedupeFold(normaliseAll(figureRE.FindAllString(fragment, -1))),
		Where:     where,
		Text:      fragment,
	}}
}

func normaliseAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.Join(strings.Fields(s), " "))
	}
	return out
}

func dedupeFold(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		k := strings.ToUpper(strings.ReplaceAll(s, " ", ""))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

func dedupeClaims(in []StandardsClaim) []StandardsClaim {
	seen := map[string]bool{}
	out := in[:0]
	for _, c := range in {
		k := c.Where + "|" + c.Text
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

// splitSentences breaks prose into fragments so a figure is attributed to the
// sentence that actually contains it, rather than to the whole paragraph.
//
// A full stop between two digits is a DECIMAL POINT, not a sentence end. The
// first version of this used strings.FieldsFunc on '.', which split
// "42.3 mm" into "42" and "3 mm" — and since a bare "42" carries no unit, the
// figure regexp then matched nothing and the claim was reported with no number
// attached. Caught by TestStandardsClaim_CatchesAFigureWithNoGeometry, which is
// why that test uses a real decimal dimension rather than a round one.
func splitSentences(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	r := []rune(s)
	var out []string
	var cur strings.Builder
	flush := func() {
		if f := strings.TrimSpace(cur.String()); f != "" {
			out = append(out, f)
		}
		cur.Reset()
	}
	for i, ch := range r {
		switch ch {
		case ';', '\n', '•':
			flush()
		case '.':
			prevDigit := i > 0 && r[i-1] >= '0' && r[i-1] <= '9'
			nextDigit := i+1 < len(r) && r[i+1] >= '0' && r[i+1] <= '9'
			if prevDigit && nextDigit {
				cur.WriteRune(ch) // decimal point
				continue
			}
			flush()
		default:
			cur.WriteRune(ch)
		}
	}
	flush()
	return out
}
