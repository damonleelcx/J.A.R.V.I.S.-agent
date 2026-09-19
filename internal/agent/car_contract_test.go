package agent

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The contract offers "car" the way it offers "gear": the shape word, a paragraph
// whose example FORGE builds, every number the template reads, every class the
// proportion table has, and the design-word table row for row — rendered from the
// tables themselves, so the prompt cannot offer a key or a word the template does
// not read. Stage C1 and C3 of the looks-designed work, 2026-09-18.
func TestTheContractCarriesTheCarTemplateAndEveryDesignWord(t *testing.T) {
	i := strings.Index(converseFraming, `"shape": `)
	if i < 0 {
		t.Fatal(`converseFraming no longer declares a "shape" field`)
	}
	enum := converseFraming[i:]
	if end := strings.Index(enum, "\n        \"shape_note\""); end > 0 {
		enum = enum[:end]
	}
	if !strings.Contains(enum, `"car"`) {
		t.Errorf("the contract does not offer the car shape:\n%s", enum)
	}
	start := strings.Index(converseFraming, `- "car" is a whole road car`)
	if start < 0 {
		t.Fatal(`the contract has no "car" paragraph`)
	}
	paragraph := converseFraming[start:]
	if end := strings.Index(paragraph[1:], "\n- "); end > 0 {
		paragraph = paragraph[:end+1]
	}
	if !strings.Contains(paragraph, geometry.CarGuide()) {
		t.Error("the car paragraph does not carry the template's own key list (geometry.CarGuide)")
	}
	if !strings.Contains(paragraph, geometry.DesignWordGuide()) {
		t.Error("the car paragraph does not carry the design-word table (geometry.DesignWordGuide)")
	}
	for _, class := range geometry.CarClasses() {
		if !strings.Contains(paragraph, class) {
			t.Errorf("the car paragraph does not name the class %q", class)
		}
	}
	for _, word := range []string{`"flowing fender"`, `"aggressive stance"`, `"soft edges"`} {
		if !strings.Contains(paragraph, word) {
			t.Errorf("the rendered prompt does not carry the design word %s", word)
		}
	}
	if !strings.Contains(paragraph, "never sets a count") {
		t.Error("the car paragraph does not say a design word never sets a count")
	}

	// The example builds: copied as written, it is a car with no fault.
	m := regexp.MustCompile(`(\{"id": "car", "name": "GT coupe", "shape": "car", "class": "sports",\s*"size": \{[^}]*\}\})`).
		FindStringSubmatch(paragraph)
	if m == nil {
		t.Fatal("the car paragraph has no example part")
	}
	var part geometry.Part
	if err := json.Unmarshal([]byte(m[1]), &part); err != nil {
		t.Fatalf("the car example is not JSON a model could copy: %v\n%s", err, m[1])
	}
	doc := settleDocument(&Prototype{Name: "car", Units: "mm", Parts: []geometry.Part{part}})
	if doc == nil || len(doc.Faults()) != 0 || doc.Root == "" {
		t.Fatalf("the contract's own car example does not build: %+v", doc)
	}
}

// The reference drawing guides a car's silhouette only through WORDS (stage C4):
// sketch.go reads the picture into a number-free description of its form, which
// reaches the prompt beside the design-word table, and the model maps "wedge" or
// "fastback" to the template's style knobs through that table. So every design
// word must survive the reading's number filter unchanged — a word it rewrote
// ("several-door") could never reach its row — and no number from the picture
// can reach a count or a dimension. sketch.go and look.go are not changed: they
// stay closed to styling (damon, 2026-09-18).
func TestDesignWordsSurviveTheSketchReadingsNumberFilter(t *testing.T) {
	guide := geometry.DesignWordGuide()
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(guide, -1) {
		if got := withoutNumbers(m[1]); got != m[1] {
			t.Errorf("the design word %q reads as %q after the sketch's number filter, so a drawing "+
				"described with it could never reach its row", m[1], got)
		}
	}
	// And the filter still does its job on what a drawing of a car says.
	said := withoutNumbers("a wedge-shaped fastback with five-spoke wheels and 4 exhausts")
	if strings.Contains(said, "five") || strings.Contains(said, "4") || !strings.Contains(said, "wedge-shaped fastback") {
		t.Errorf("the sketch reading kept a count or lost the form: %q", said)
	}
}

// Settling a reply writes a car out as its tree, with every proportion outside its
// class told to the reader and the car still built.
func TestSettle_WritesACarOutAsItsTreeAndSaysWhatIsOutsideItsClass(t *testing.T) {
	doc := &Prototype{Name: "car", Units: "mm", Parts: []geometry.Part{{ID: "gt", Name: "GT", Shape: "car",
		Class: "sports", Size: map[string]float64{"length": 4500, "width": 1900, "height": 1250,
			"wheelbase": 3400, "front_overhang": 560}}}}
	got := settleDocument(doc)
	if got == nil {
		t.Fatal("the car was dropped")
	}
	for _, p := range got.Parts {
		if strings.EqualFold(p.Shape, "car") {
			t.Fatal("settling left the car as a car part")
		}
	}
	if len(got.Faults()) != 0 || got.Root == "" || len(got.Parameters) == 0 {
		t.Fatalf("faults %v, root %q, %d parameter(s)", got.Faults(), got.Root, len(got.Parameters))
	}
	said := strings.Join(got.NotVerified, "\n")
	if !strings.Contains(said, "wheelbase as a share of length") || !strings.Contains(said, "built as asked") {
		t.Fatalf("the reader was not told the wheelbase is outside the sports cars:\n%s", said)
	}
	// Settling what was settled changes nothing: a repair sends the settled one back.
	again := settleDocument(got)
	if len(again.Assemblies) != len(got.Assemblies) || len(again.Parameters) != len(got.Parameters) {
		t.Fatal("settling a written-out car wrote it out again")
	}
}
