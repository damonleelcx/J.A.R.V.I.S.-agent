package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Technical readback (PRD AUD-04).
//
// These are the cases where saying the text as written produces something a
// listener cannot write down correctly. Ordinary prose is deliberately absent
// from the table except as a check that it survives untouched.

func TestReadbackMakesTextTranscribable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a version is read digit by digit",
			// "0.2.0" said as "zero point twenty" is a version that does not
			// exist. This is the case AUD-04 exists for.
			in:   "Tagged 0.2.0 this morning.",
			want: "Tagged 0 point 2 point 0 this morning.",
		},
		{
			name: "a v-prefixed version is read digit by digit",
			// The form versions are actually written in, and the one the rule
			// used to miss: a leading \b cannot match between "v" and "0"
			// because both are word characters. It went unnoticed for the life
			// of the browser copy because the doc comment above readable() uses
			// this exact string as its worked example.
			//
			// Guarding the front of the pattern precisely would need a
			// lookbehind, which RE2 does not have, so the two copies could not
			// have stayed identical that way. The front is simply unanchored.
			in:   "Tagged v0.2.0 this morning.",
			want: "Tagged v0 point 2 point 0 this morning.",
		},
		{
			name: "a four-segment number is spoken all the way through",
			// The rule used to match exactly three segments, so this came out
			// as "1 point 2 point 3.4" — the first three spoken and the fourth
			// left as a decimal, which is worse than either treatment alone.
			in:   "Build 1.2.3.4 is on the bench.",
			want: "Build 1 point 2 point 3 point 4 is on the bench.",
		},
		{
			name: "an address is a dotted number like any other",
			in:   "The hub is at 10.0.0.1 now.",
			want: "The hub is at 10 point 0 point 0 point 1 now.",
		},
		{
			name: "a plain decimal is left alone",
			// Two dots minimum. "2.5" and "$2.50" are already read correctly,
			// and a rule that rewrote every decimal in every sentence would be
			// touching far more text than AUD-04 asks it to.
			in:   "It costs $2.50 and weighs 2.5 kg.",
			want: "It costs $2.50 and weighs 2.5 kilograms.",
		},
		{
			name: "a dotted number glued to a unit stays a measurement",
			// The trailing \b keeps this out of the dotted-number rule so the
			// unit rules can have it. Asserted because the two rules compete
			// for this string and the order of that competition is invisible.
			in:   "Bore 1.2.3mm at 10.20.30",
			want: "Bore 1.2.3 millimetres at 10 point 20 point 30",
		},
		{
			name: "a four-segment number glued to a unit is still half spoken",
			// The residue of the trailing \b, recorded rather than chased. The
			// greedy match backs off until the boundary holds, which lands it
			// on the first three segments and leaves ".4mm" to the unit rule.
			//
			// Left alone because no measurement is written with three dots in
			// it — this input was constructed to find the edge, not observed.
			// Removing the trailing \b would fix it and would change what
			// "1.2.3mm" does, which is the case that occurs. Both engines agree
			// on this output, so at worst it is consistently odd.
			in:   "Bore 1.2.3.4mm",
			want: "Bore 1 point 2 point 3.4 millimetres",
		},
		{
			name: "the prefix is not required to be a v",
			// Consequence of leaving the front unanchored rather than special-
			// casing "v", and the reason that was the better of the two: nobody
			// has to enumerate the ways people write a version.
			in:   "Shipped in rev1.2.3.",
			want: "Shipped in rev1 point 2 point 3.",
		},
		{
			name: "a measurement keeps its unit",
			in:   "The wall is 2.5mm thick.",
			want: "The wall is 2.5 millimetres thick.",
		},
		{
			name: "a unit written with a space is still spelled out",
			in:   "Clearance is 3 cm.",
			want: "Clearance is 3 centimetres.",
		},
		{
			name: "a tolerance does not lose its sign",
			// ± is silent in most voices, which turns a tolerance into a bare
			// number — the failure that loses a part on a shop floor.
			in:   "Bore 12mm ±0.1",
			want: "Bore 12 millimetres plus or minus 0.1",
		},
		{
			name: "torque and temperature are spoken, not lettered",
			in:   "Torque to 40Nm, cure at 80°C.",
			want: "Torque to 40 newton metres, cure at 80 degrees Celsius.",
		},
		{
			name: "a latency figure is not a unit collision",
			in:   "First audio in 700ms.",
			want: "First audio in 700 milliseconds.",
		},
		{
			name: "an identifier is spelled out",
			in:   "See req_A1B2C3D4E5.",
			want: "See r e q A 1 B 2 C 3 and so on.",
		},
		{
			name: "a position names its axes",
			// The case the coordinate rule exists for. position() in
			// internal/domain/geometry/compare.go emits exactly this shape, and
			// spoken as written it is three numbers in a row with nothing to say
			// which is which — a coordinate nobody can write down.
			in:   "Bracket origin is at (12.5 mm, 0 mm, -40 mm).",
			want: "Bracket origin is at X 12.5 millimetres, Y 0 millimetres, Z minus 40 millimetres.",
		},
		{
			name: "a coordinate does not lose its minus",
			// "-" is silent, so this used to be heard as "Y 8" — the part on the
			// wrong side of the datum, which is the ± failure again in a different
			// place. Asserted separately from the case above because the sign is
			// the half that is silently wrong rather than merely ambiguous.
			in:   "Move it to (0, -8, 0).",
			want: "Move it to X 0, Y minus 8, Z 0.",
		},
		{
			name: "a coordinate with no units is still read by axis",
			in:   "Origin (1.75, -0.5, 33) on the fixture.",
			want: "Origin X 1.75, Y minus 0.5, Z 33 on the fixture.",
		},
		{
			name: "a coordinate and a tolerance survive each other",
			// The two rules touch the same sentence and run in a fixed order. If
			// they ever compete, this is where it shows.
			in:   "Datum at (0 mm, 8 mm, 0 mm) ±0.1",
			want: "Datum at X 0 millimetres, Y 8 millimetres, Z 0 millimetres plus or minus 0.1",
		},
		{
			name: "a pair in prose is not a position",
			// Why the rule takes three segments and not two or more. Nothing in
			// this domain has a two-axis position, and "(1, 2)" in a sentence is a
			// list. Matching pairs would put axis names into ordinary prose.
			in:   "Check items (1, 2) before you start.",
			want: "Check items (1, 2) before you start.",
		},
		{
			name: "a parenthesis around words is left alone",
			in:   "Torque it (see the datum) first.",
			want: "Torque it (see the datum) first.",
		},
		{
			name: "markdown is not read as punctuation",
			in:   "**Important**: check the `bore` diameter.",
			want: "Important: check the bore diameter.",
		},
		{
			name: "an identifier survives the markdown rule",
			// The underscore that identifies an identifier is also a markdown
			// character. If the rules were reordered, this reads as "reqA1B2C3".
			in:   "Logged against req_A1B2C3.",
			want: "Logged against r e q A 1 B 2 C 3 and so on.",
		},
		{
			name: "prose is left alone",
			in:   "That depends on how the bracket is loaded.",
			want: "That depends on how the bracket is loaded.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Readable(tc.in); got != tc.want {
				t.Errorf("readback of %q\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}
}

// The two copies of the readback rules have not drifted apart.
//
// # Why this fence exists
//
// AUD-04 applies to both places FORGE speaks, and they are different runtimes:
// the workbench synthesises in the browser (assets/voice.js), a room synthesises
// on the server (readback.go). The rules are therefore written twice, which is
// safe exactly as long as somebody notices when only one copy changes — the
// failure otherwise is silent and surface-specific, which is how the server side
// came to be missing this in the first place.
//
// Counting rules is coarse on purpose. It survives reformatting and renaming,
// and it fires on the thing that actually goes wrong: a rule added to one side
// only. It cannot check that matching rules mean the same thing; the table above
// is what checks that, and a new rule should arrive with a case in it.
func TestTheReadbackRulesHaveNotDrifted(t *testing.T) {
	path := filepath.Join("..", "httpapi", "assets", "voice.js")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the browser's copy of the readback rules: %v", err)
	}

	body, err := readableBody(string(src))
	if err != nil {
		t.Fatalf("%v in %s; this fence locates readable() by name, so renaming it "+
			"means updating the fence rather than deleting it", err, path)
	}

	inJS := strings.Count(body, ".replace(")
	if inJS != len(readbackRules) {
		t.Fatalf("the browser applies %d readback substitutions and the server applies %d.\n"+
			"Both speak for PRD AUD-04 and they must agree: a rule added to one copy only "+
			"means a measurement is read back correctly in the workbench and wrongly in a "+
			"room, or the reverse.\n"+
			"Fix by making the change in both %s and internal/media/readback.go, and adding "+
			"a case to TestReadbackMakesTextTranscribable.",
			inJS, len(readbackRules), path)
	}

	// Both copies finish by trimming. Cheap to assert, and its absence would show
	// up as a pause before FORGE starts speaking rather than as a wrong word.
	if !strings.Contains(body, ".trim()") {
		t.Error("the browser's readable() no longer trims; the server's Readable still does")
	}
}

// readableBody returns the source of readable() in voice.js.
func readableBody(src string) (string, error) {
	const marker = "function readable(text)"
	start := strings.Index(src, marker)
	if start < 0 {
		return "", errNoReadable{"no function readable(text) found"}
	}
	rest := src[start:]
	// The function is declared at two-space indent, so its closing brace is the
	// first "\n  }" after it.
	end := strings.Index(rest, "\n  }")
	if end < 0 {
		return "", errNoReadable{"readable() has no closing brace at the expected indent"}
	}
	return rest[:end], nil
}

type errNoReadable struct{ msg string }

func (e errNoReadable) Error() string { return e.msg }
