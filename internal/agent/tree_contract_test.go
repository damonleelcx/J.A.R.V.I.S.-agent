package agent

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The contract offers trees, and the example it gives is a tree FORGE builds.
//
// Stage D1f of docs/plan-2026-09-13-millions-of-parts.md. Built rather than read,
// for the reason the gear fence gives: the paragraph is prose nothing compiles, and
// a field renamed in geometry and not here would leave a model faithfully writing a
// tree FORGE refuses. Unknown fields are refused while decoding, so a key the
// contract spells differently from the Go tag fails here.
func TestTheContractOffersATreeAndItsExampleBuilds(t *testing.T) {
	const marker = "Worked example — two welded wheels on an axle:\n"
	i := strings.Index(geometryContract, marker)
	if i < 0 {
		t.Fatal("the contract has no worked tree example")
	}
	rest := geometryContract[i+len(marker):]
	const last = `"root": "axle"`
	end := strings.Index(rest, last)
	if end < 0 {
		t.Fatal(`the contract's tree example does not end with "root"`)
	}
	body := `{"name": "axle", "units": "mm", ` + rest[:end+len(last)] + `}`

	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var doc geometry.Document
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("the contract's tree example is not a document a model could copy: %v\n%s", err, body)
	}
	for _, p := range doc.TreeProblems() {
		if p.Severity == geometry.Error {
			t.Errorf("the contract's own tree is refused: %s %s", p.Name, p.Detail)
		}
	}
	if faults := doc.Faults(); len(faults) != 0 {
		t.Errorf("the contract's own tree has faults: %+v", faults)
	}
	// One axle bar, and two wheels of a hub and six spokes each.
	if placed := doc.PlacedParts(); len(placed) != 15 {
		t.Errorf("the example places %d parts; the paragraph describes 15", len(placed))
	}
	var welds []string
	for _, f := range doc.Expanded().Features {
		welds = append(welds, f.ID)
	}
	if strings.Join(welds, ",") != "left-wheel/weld,right-wheel/weld" {
		t.Errorf("the wheel's weld is applied as %q; want it in both wheels", welds)
	}
	if _, _, problems, _ := geometry.SolidsAndOperations(doc, geometry.Millimetre); len(problems) != 0 {
		t.Errorf("the kernel request for the example has problems: %+v", problems)
	}
}

// The edit the contract describes names the fields an edit really has.
func TestTheContractNamesTheTreeEditFieldsAnEditHas(t *testing.T) {
	m := regexp.MustCompile(`"remove": (\{[^\n]*\}),\n`).FindStringSubmatch(geometryContract)
	if m == nil {
		t.Fatal(`the contract has no "remove" example`)
	}
	dec := json.NewDecoder(strings.NewReader(m[1]))
	dec.DisallowUnknownFields()
	var removals geometry.Removals
	if err := dec.Decode(&removals); err != nil {
		t.Fatalf("the contract's remove example names a field an edit does not have: %v\n%s", err, m[1])
	}
	if len(removals.Definitions) == 0 || len(removals.Assemblies) == 0 || len(removals.Children) == 0 {
		t.Errorf("the contract's remove example does not show how to remove definitions, assemblies and children: %s", m[1])
	}
	for _, want := range []string{`"definitions": [ ...whole definitions, by id... ]`, `"assemblies": [ ...whole assemblies, by id... ]`} {
		if !strings.Contains(geometryContract, want) {
			t.Errorf("the contract's patch does not show %s", want)
		}
	}
}
