package agent

import (
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The feature vocabulary in the contract comes from the tables the validator and
// the kernel read (looks designed, stages B2 and B4): every operation, every edge
// rule and every face a shell may leave open is taught, and nothing is taught that
// the validator refuses. The first edge rules were a sentence in this prompt, a Go
// map and a Python if-chain, and a rule added to one was a rule the others refused.
func TestTheContractTeachesEveryFeatureRuleFORGEHas(t *testing.T) {
	for name, text := range map[string]string{"build": buildContract, "converse": converseFraming} {
		if strings.Contains(text, "%!") {
			t.Fatalf("%s: the contract has a formatting error: %s", name,
				text[strings.Index(text, "%!"):][:60])
		}
		schema := regexp.MustCompile(`"op": ([^,\n]+(?: \| [^,\n]+)*),`).FindStringSubmatch(text)
		if schema == nil {
			t.Fatalf("%s: the features schema offers no op", name)
		}
		for _, op := range []string{"cut", "fuse", "fillet", "chamfer", "loft", "shell", "thicken"} {
			if !strings.Contains(schema[1], `"`+op+`"`) {
				t.Errorf("%s: the features schema does not offer %q: %s", name, op, schema[1])
			}
		}
		edges := regexp.MustCompile(`"edges": ([^\n]+),\n`).FindStringSubmatch(text)
		if edges == nil {
			t.Fatalf("%s: the features schema offers no edges", name)
		}
		offered := regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(edges[1], -1)
		if len(offered) != len(geometry.EdgeRules) {
			t.Errorf("%s: the schema offers %d edge rules and FORGE has %d: %s", name,
				len(offered), len(geometry.EdgeRules), edges[1])
		}
		for _, m := range offered {
			d := &geometry.Document{Name: "x", Units: "mm", Parts: []geometry.Part{
				{ID: "p", Shape: "box", Size: map[string]float64{"width": 10, "height": 10, "depth": 10}},
				{ID: "q", Shape: "box", Size: map[string]float64{"width": 2, "height": 2, "depth": 2}}},
				Features: []geometry.Feature{{ID: "f", Op: "fuse", Of: "p", With: []string{"q"}},
					{ID: "r", Op: "fillet", Of: "p", Radius: 1, Edges: m[1], EdgeLength: 5}}}
			if m[1] != "longer" {
				d.Features[1].EdgeLength = 0
			}
			if _, problems := d.Operations(); len(problems) != 0 {
				t.Errorf("%s: the contract offers edges %q and the validator refuses it: %v", name, m[1], problems)
			}
		}
		for _, r := range geometry.EdgeRules {
			if !strings.Contains(text, `"`+r.Name+`" is `+r.Says) {
				t.Errorf("%s: the contract does not say what edges %q selects", name, r.Name)
			}
		}
		for _, f := range geometry.OpenFaceRules {
			if !strings.Contains(text, `"`+f.Name+`" is `+f.Says) {
				t.Errorf("%s: the contract does not say which face %q leaves open", name, f.Name)
			}
		}
		for _, field := range []string{`"edge_length"`, `"thickness"`, `"thickness_from"`, `"open"`} {
			if !strings.Contains(text, field) {
				t.Errorf("%s: the contract never shows the field %s", name, field)
			}
		}
	}
}
