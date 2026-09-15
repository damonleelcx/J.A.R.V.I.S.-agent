package agent

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The contract teaches "density", and what it says is what the code does. Phase 5,
// stage V3 added the field and the mass roll-up; the contract never named it, so no
// model could write one (closed 2026-09-15).
//
// Three agreements, each a way a faithful model gets let down:
//   - the material the contract SHOWS decodes, and its density is accepted;
//   - the ceiling the contract STATES is the one the validator enforces;
//   - the rule it states — mass only when every part has a density, otherwise
//     weighted by volume — is what MassProperties does.
func TestTheContractTeachesDensityAsTheValidatorReadsIt(t *testing.T) {
	example := regexp.MustCompile(`"material": null or (\{[^}]*\})`).FindStringSubmatch(converseFraming)
	if example == nil {
		t.Fatal("the contract shows no material")
	}
	var shown geometry.Material
	dec := json.NewDecoder(strings.NewReader(example[1]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&shown); err != nil {
		t.Fatalf("the contract's material is not one a model could copy: %v\n%s", err, example[1])
	}
	if shown.Density <= 0 {
		t.Errorf("the contract's material shows no density: %s", example[1])
	}
	if err := shown.Validate(); err != nil {
		t.Errorf("the material the contract shows is refused: %v", err)
	}

	start, end := strings.Index(converseFraming, `- "material" is a CLAIM`), strings.Index(converseFraming, `- "states" are named`)
	if start < 0 || end < start {
		t.Fatal("the contract has no material paragraph before the states one")
	}
	paragraph := converseFraming[start:end]
	for _, want := range []string{"KILOGRAMS PER CUBIC METRE", "MASS only when every part has a density", "weighted by volume"} {
		if !strings.Contains(paragraph, want) {
			t.Errorf("the material paragraph does not say %q:\n%s", want, paragraph)
		}
	}

	ceiling := regexp.MustCompile(`a density above (\d+) is refused`).FindStringSubmatch(paragraph)
	if ceiling == nil {
		t.Fatalf("the contract does not state the density ceiling:\n%s", paragraph)
	}
	stated, _ := strconv.ParseFloat(ceiling[1], 64)
	if stated != geometry.MaxDensity {
		t.Errorf("the contract states a ceiling of %v and the validator enforces %v", stated, float64(geometry.MaxDensity))
	}
	for _, c := range []struct {
		density float64
		ok      bool
	}{{stated, true}, {stated + 1, false}} {
		m := geometry.Material{Name: "x", Density: c.density}
		if err := m.Validate(); (err == nil) != c.ok {
			t.Errorf("a density of %v: validator error %v; the contract says it is %v", c.density, err,
				map[bool]string{true: "accepted", false: "refused"}[c.ok])
		}
	}
	for _, n := range regexp.MustCompile(`about (\d+)`).FindAllStringSubmatch(paragraph, -1) {
		v, _ := strconv.ParseFloat(n[1], 64)
		if err := (&geometry.Material{Name: "example", Density: v}).Validate(); err != nil {
			t.Errorf("the contract gives %v as a density and the validator refuses it: %v", v, err)
		}
	}

	// The rule, as MassProperties applies it.
	steel := &geometry.Material{Name: "steel", Density: 7850}
	box := map[string]float64{"width": 10, "height": 10, "depth": 10}
	doc := geometry.Document{Name: "pair", Units: "mm", Parts: []geometry.Part{
		{ID: "a", Shape: "box", Size: box, Material: steel}, {ID: "b", Shape: "box", Size: box}}}
	measures := []geometry.SolidMeasure{{ID: "a", Volume: 1000, Measured: true}, {ID: "b", Volume: 1000, Measured: true}}
	if r := geometry.MassProperties(doc, measures); r.Basis != geometry.MassByVolume || r.Groups[0].Mass != 0 || len(r.WithoutDensity) != 1 {
		t.Errorf("one part of two with a density: basis %q, mass %v, without %v; the contract says volume, no mass", r.Basis, r.Groups[0].Mass, r.WithoutDensity)
	}
	doc.Parts[1].Material = steel
	if r := geometry.MassProperties(doc, measures); r.Basis != geometry.MassByDensity || r.Groups[0].Mass <= 0 {
		t.Errorf("every part with a density: basis %q, mass %v; the contract says a mass", r.Basis, r.Groups[0].Mass)
	}
}
