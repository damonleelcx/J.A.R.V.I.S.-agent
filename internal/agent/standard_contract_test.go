package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Standard parts and patterns in the contract, and the warning that reaches the
// turn. Phase 2, stage A3.

func standardParagraph(t *testing.T) string {
	t.Helper()
	start := strings.Index(geometryContract, `- "standard" is a CATALOGUED part`)
	end := strings.Index(geometryContract, `- WRITE A DESIGN ONCE AND PLACE IT MANY TIMES`)
	if start < 0 || end < start {
		t.Fatal("the contract has no standard-part paragraph followed by the place-it-many-times one")
	}
	return geometryContract[start:end]
}

// The contract offers exactly the catalogue: every designation FORGE has is taught,
// and every designation taught is one FORGE builds. Both directions, because the
// failure on either side is a model faithfully writing something — a screw it was
// never offered, or one it was offered and gets refused.
func TestTheContractTeachesEveryStandardPartFORGEHas(t *testing.T) {
	paragraph := standardParagraph(t)
	if !strings.Contains(converseFraming[:strings.Index(converseFraming, "\n        \"shape_note\"")], `"standard"`) {
		t.Error(`the shape list does not offer "standard"`)
	}

	taught := map[string]bool{}
	for _, m := range regexp.MustCompile(`"((?:ISO|EN) \d+ [^"<]+)"`).FindAllStringSubmatch(paragraph, -1) {
		taught[m[1]] = true
	}
	lengths := regexp.MustCompile(`and the length one of ([\d, ]+)\.`).FindStringSubmatch(paragraph)
	if lengths == nil {
		t.Fatal("the contract does not say which lengths a cap screw comes in")
	}
	screws := regexp.MustCompile(`"ISO 4762 (M\d+)x<length>", a length from (\d+) to (\d+)`).FindAllStringSubmatch(paragraph, -1)
	if len(screws) == 0 {
		t.Fatal("the contract offers no cap screw sizes")
	}
	for _, s := range screws {
		lo, _ := strconv.ParseFloat(s[2], 64)
		hi, _ := strconv.ParseFloat(s[3], 64)
		for _, l := range strings.Split(lengths[1], ", ") {
			if v, _ := strconv.ParseFloat(l, 64); v >= lo && v <= hi {
				taught["ISO 4762 "+s[1]+"x"+l] = true
			}
		}
	}

	has := map[string]bool{}
	for _, d := range geometry.StandardDesignations() {
		has[d] = true
		if !taught[d] {
			t.Errorf("FORGE has %q and the contract never offers it", d)
		}
	}
	var names []string
	for d := range taught {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, d := range names {
		part := geometry.Part{ID: "p", Shape: "standard", Standard: d, Size: map[string]float64{"length": 600}}
		if _, ok := geometry.StandardPartForTest(part, "mm"); !ok || !has[d] {
			t.Errorf("the contract offers %q and FORGE refuses it", d)
		}
	}
	for _, family := range []string{"ISO 4762", "ISO 4032", "ISO 7089", "ISO 15", "EN 10219", "EN 10056"} {
		if !strings.Contains(paragraph, family) {
			t.Errorf("the contract does not name the %s family", family)
		}
	}
}

// The worked example builds: six standard screws placed by one polar pattern, with
// no fault and nothing flagged. And the same six written out as children ARE
// flagged, with the example's own pattern — so what the contract teaches and what
// the check suggests are one spelling.
func TestTheContractsPatternExampleBuildsAndIsWhatTheCheckSuggests(t *testing.T) {
	const marker = "Worked example — six cap screws round a flange:\n"
	i := strings.Index(geometryContract, marker)
	if i < 0 {
		t.Fatal("the contract has no worked pattern example")
	}
	rest := geometryContract[i+len(marker):]
	const last = `"root": "flange"`
	end := strings.Index(rest, last)
	if end < 0 {
		t.Fatal(`the pattern example does not end with "root"`)
	}
	body := `{"name": "flange", "units": "mm", ` + rest[:end+len(last)] + `}`
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var doc geometry.Document
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("the pattern example is not a document a model could copy: %v\n%s", err, body)
	}
	if faults := doc.Faults(); len(faults) != 0 {
		t.Errorf("the contract's own example has faults: %+v", faults)
	}
	expanded := doc.Expanded().Parts
	if len(expanded) != 6 {
		t.Fatalf("the example places %d parts; the paragraph describes six screws", len(expanded))
	}
	for _, p := range expanded {
		if p.Shape != "revolve" || p.Standard != "ISO 4762 M6x20" {
			t.Errorf("placed %s is a %s named %q; want the M6x20 screw drawn", p.ID, p.Shape, p.Standard)
		}
	}
	if found := doc.EnumeratedRepetition(); len(found) != 0 {
		t.Errorf("the contract's own example is flagged: %s", found[0].Warning())
	}

	written := doc
	written.Assemblies = []geometry.Assembly{{ID: "flange"}}
	for _, p := range doc.PlacedParts() {
		written.Assemblies[0].Children = append(written.Assemblies[0].Children,
			geometry.Child{ID: strings.ReplaceAll(p.ID, "screw", "bolt"), Ref: "screw", Position: p.Position, Rotation: p.Rotation})
	}
	found := written.EnumeratedRepetition()
	if len(found) != 1 {
		t.Fatalf("the example written out as six children is flagged %d times; want once", len(found))
	}
	suggested, _ := json.Marshal(found[0].Pattern)
	taught, _ := json.Marshal(doc.Assemblies[0].Children[0].Pattern)
	if string(suggested) != string(taught) {
		t.Errorf("the check suggests %s; the contract teaches %s", suggested, taught)
	}
}

// enumeratedPlate is five standard screws written out as five children in a row.
func enumeratedPlate() *Prototype {
	d := &geometry.Document{Name: "Plate", Units: "mm", Root: "plate",
		Definitions: []geometry.Part{{ID: "screw", Name: "Cap screw", Shape: "standard", Standard: "ISO 4762 M6x20"}},
		NotVerified: []string{"a concept, not an analysis"}}
	a := geometry.Assembly{ID: "plate"}
	for i := 0; i < 5; i++ {
		a.Children = append(a.Children, geometry.Child{ID: fmt.Sprintf("screw-%c", 'a'+i), Ref: "screw",
			Position: []float64{float64(i) * 30, 0, 0}})
	}
	d.Assemblies = []geometry.Assembly{a}
	return d
}

const plateSuggestion = `"pattern": {"kind":"linear","count":5,"offset":[30,0,0]}`

// The turn says what could be one pattern, on both paths, and the standard part it
// carries is built without a fault.
func TestRepetition_TheTurnSaysWhatCouldBeOnePattern(t *testing.T) {
	reply, err := (&Conversation{client: &repairStub{reply: `{"speech":"Here is the plate.","prototype":` +
		mustJSONInner(t, enumeratedPlate()) + `}`}}).Respond(context.Background(), "proj", nil, "a plate with five screws", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Repaired, plateSuggestion) || !strings.Contains(reply.Repaired, `Assembly "plate"`) {
		t.Errorf("the turn wrote five screws out one by one and the notice does not say which pattern would do:\n%q", reply.Repaired)
	}
	if faults := reply.Prototype.Faults(); len(faults) != 0 {
		t.Errorf("a standard screw through a real turn has faults: %+v", faults)
	}
	if strings.Count(reply.Repaired, plateSuggestion) != 1 {
		t.Errorf("the note is said more than once: %q", reply.Repaired)
	}

	var notices []string
	stub := &streamingStub{repairStub{reply: `{"speech":"Here is the plate.","prototype":` + mustJSONInner(t, enumeratedPlate()) + `}`}}
	if err := (&Conversation{client: stub}).RespondStream(context.Background(), "proj", nil, "a plate with five screws", "", nil, nil,
		func(e StreamEvent) error {
			if e.Kind == "notice" {
				notices = append(notices, e.Text)
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(notices, " "), plateSuggestion) {
		t.Errorf("the streaming turn emitted no notice naming the pattern: %q", notices)
	}
}

// A build step is shown what the model so far writes out one by one — the step's
// model sees its prompt and nothing else — and a model with nothing to say is told
// nothing.
func TestAssemble_AStepIsToldWhatCouldBeOnePattern(t *testing.T) {
	stub := &scriptedStub{replies: []string{`{"speech":"ok"}`, `{"speech":"ok"}`}}
	c := &Conversation{client: stub}
	c.buildOneStep(context.Background(), enumeratedPlate(), "a plate", buildTask{Name: "nuts", What: "add nuts"}, 2, 3)

	patterned := enumeratedPlate()
	patterned.Assemblies[0].Children = []geometry.Child{{ID: "screw", Ref: "screw",
		Pattern: &geometry.Pattern{Kind: "linear", Count: 5, Offset: []float64{30, 0, 0}}}}
	c.buildOneStep(context.Background(), patterned, "a plate", buildTask{Name: "nuts", What: "add nuts"}, 2, 3)

	if len(stub.asked) != 2 {
		t.Fatalf("the steps asked %d time(s)", len(stub.asked))
	}
	if !strings.Contains(stub.asked[0], plateSuggestion) || !strings.Contains(stub.asked[0], "Written out one child at a time") {
		t.Errorf("the step was not told the five screws could be one pattern:\n%.1200s", stub.asked[0])
	}
	if strings.Contains(stub.asked[1], "Written out one child at a time") {
		t.Errorf("a step on a model with nothing written out was told there was:\n%.1200s", stub.asked[1])
	}
}
