package geometry

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
)

// namedAssembly is one box per name, so the exported file has one group per name
// in the order given.
//
// The names are what a person can actually type into the panel: a slash, a
// space, an accent, a script with no Latin letters in it, the same name twice,
// and a newline pasted in from a spreadsheet.
func namedAssembly(name string, parts ...string) *Variant {
	v := &Variant{
		VersionID: "ver_1", ProjectID: "prj_1", Path: "geometry/named.forge.json", Version: 3,
		Name:  name,
		Units: Millimetre, Frame: FrameAssembly, Generator: "claude-opus-5",
		Verification: workspace.Unverified, Disposition: workspace.Pending,
		Document: Document{Name: name, Units: "mm"},
	}
	for i, p := range parts {
		v.Document.Parts = append(v.Document.Parts, Part{
			ID: fmt.Sprintf("p%d", i+1), Name: p, Shape: "box",
			Size:     map[string]float64{"width": 10, "height": 10, "depth": 10},
			Position: []float64{float64(i) * 20, 0, 0}, Rotation: []float64{0, 0, 0},
		})
	}
	return v
}

// An OBJ name is one whitespace-delimited token, so a part called "left / Ell 1"
// cannot be written down as it was typed. This fences the rule that makes one:
// the token is readable, it is ASCII, no two groups in a file share one, and the
// name as the person typed it is on the comment line above the group — including
// when that name carries a newline, which would otherwise end the comment and
// leave the rest of the sentence in the file as geometry.
func TestExport_PartNamesBecomeOneReadableTokenAndStayDistinct(t *testing.T) {
	v := namedAssembly("Ω bracket / v2",
		"left / Ell 1", // a slash and spaces: the reported mangling
		"left Ell 1",   // transliterates onto the same token as the one above
		"Boîtier",      // an accent folds to the letter it is drawn on
		"左パネル",         // nothing Latin to carry over at all
		"M6 washer",    // an ordinary repeated name
		"M6 washer",
		"  spaced  out  ", // leading, trailing and doubled separators
		"rev.2-a",         // '.' and '-' are names people rely on, and are kept
		"✓✓",              // punctuation only: no token can be made from it
		"bad\nname v 1",   // a newline, which must not escape its comment
	)

	res, err := Export(v, "obj")
	if err != nil {
		t.Fatalf("exporting: %v", err)
	}
	obj := string(res.Content)

	// 1. Every name line is exactly two tokens. One space inside a name is what
	//    turns a part into two names, and a reader that survives it shows the
	//    part under the first word.
	var tokens []string
	for _, line := range strings.Split(obj, "\n") {
		if !strings.HasPrefix(line, "o ") && !strings.HasPrefix(line, "g ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			t.Errorf("%q is %d tokens; OBJ reads a name up to the first space", line, len(f))
			continue
		}
		for _, r := range f[1] {
			ok := r == '_' || r == '-' || r == '.' ||
				('0' <= r && r <= '9') || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')
			if !ok {
				t.Errorf("%q carries %q, which is not a letter, digit, '_', '-' or '.'", line, r)
				break
			}
		}
		if f[0] == "g" {
			tokens = append(tokens, f[1])
		} else if f[1] != "bracket_v2" {
			t.Errorf("the object is named %q, want %q", f[1], "bracket_v2")
		}
	}

	// 2. The rule, spelled out per name, in file order.
	want := []string{
		"left_Ell_1",   // the slash and the spaces become one '_' each, collapsed
		"left_Ell_1_2", // the same token: numbered, not silently merged
		"Boitier",
		"part",      // nothing carried over, so it says so rather than inventing
		"M6_washer", //
		"M6_washer_2",
		"spaced_out",
		"rev.2-a",
		"part_2", // "part" is taken; a distinct group still gets a distinct name
		"bad_name_v_1",
	}
	if len(tokens) != len(want) {
		t.Fatalf("the file has %d groups, want %d: %v", len(tokens), len(want), tokens)
	}
	for i := range want {
		if tokens[i] != want[i] {
			t.Errorf("group %d is %q, want %q", i+1, tokens[i], want[i])
		}
	}

	// 3. Distinct, said separately: two groups of one name is geometry a reader
	//    cannot attribute, and some readers keep only the last of them.
	seen := map[string]int{}
	for i, tok := range tokens {
		if first, dup := seen[tok]; dup {
			t.Errorf("groups %d and %d are both called %q", first+1, i+1, tok)
		}
		seen[tok] = i
	}

	// 4. The original name travels, beside the token made from it. This is the
	//    only place the characters the rule dropped still exist.
	for i, p := range v.Document.Parts {
		lines := strings.Split(p.Name, "\n")
		for j := range lines {
			lines[j] = "# " + lines[j]
		}
		block := strings.Join(lines, "\n")
		block = strings.Replace(block, "# ", "# FORGE part id="+p.ID+" name=", 1)
		block += "\ng " + tokens[i] + "\n"
		if !strings.Contains(obj, block) {
			t.Errorf("part %s (%q) is not named above its group; the file has no\n%s", p.ID, p.Name, block)
		}
	}
	if !strings.Contains(obj, "# FORGE object name=Ω bracket / v2\no bracket_v2\n") {
		t.Error("the assembly's own name as it was typed is not in the file above the object")
	}

	// 5. Nothing a person typed ended up outside a comment. A newline in a name
	//    would otherwise put the rest of their sentence in the file as data.
	for n, line := range strings.Split(obj, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch strings.Fields(line)[0] {
		case "o", "g", "v", "vn", "f":
		default:
			t.Errorf("line %d is neither a comment nor geometry: %q", n+1, line)
		}
	}

	// 6. The lossy list says the rule is in force, because the file is the only
	//    thing the person downloading it still has.
	if !strings.Contains(strings.Join(res.Label.Lossy, " "), "one token") {
		t.Errorf("the OBJ label does not say part names were rewritten: %v", res.Label.Lossy)
	}
}

// STL's solid line takes a name the same way, and STL has no comment to carry
// the original — so the token has to be right there too, and the label has to
// say the name did not travel.
func TestExport_TheSTLSolidNameIsOneTokenAndSaysTheNameDidNotTravel(t *testing.T) {
	res, err := Export(namedAssembly("左 bracket / Ell 2", "left / Ell 1"), "stl")
	if err != nil {
		t.Fatalf("exporting: %v", err)
	}
	stl := string(res.Content)
	first, _, _ := strings.Cut(stl, "\n")
	if f := strings.Fields(first); len(f) != 2 {
		t.Errorf("%q is %d tokens; STL reads the solid's name up to the first space", first, len(f))
	}
	if !strings.HasPrefix(first, "solid FORGE_bracket_Ell_2_v3_units_mm") {
		t.Errorf("the solid line is %q; the name was not made into one readable token", first)
	}
	if !strings.HasPrefix(strings.TrimSpace(strings.Split(stl, "\n")[len(strings.Split(stl, "\n"))-2]),
		"endsolid FORGE_bracket_Ell_2") {
		t.Error("endsolid names the solid differently from solid, so the file does not close its own name")
	}
	if !strings.Contains(strings.Join(res.Label.Lossy, " "), "one token") {
		t.Errorf("the STL label does not say the name was rewritten: %v", res.Label.Lossy)
	}
}
