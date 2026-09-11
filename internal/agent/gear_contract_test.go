package agent

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The contract offers "gear", and the example it gives is a gear FORGE draws.
//
// # Why the example is BUILT rather than read
//
// The paragraph is prose that nothing compiles. A key renamed in gear.go and not
// in the contract would leave the model faithfully writing a size FORGE refuses —
// every gear a fault, repaired back into the same wrong key. Unmarshalling the
// contract's own example and drawing it is the one check that fails on that day.
func TestTheContractOffersAGearAndItsExampleBuilds(t *testing.T) {
	const marker = `"shape": `
	i := strings.Index(converseFraming, marker)
	if i < 0 {
		t.Fatal(`converseFraming no longer declares a "shape" field; point this fence at the shape words`)
	}
	enum := converseFraming[i:]
	if end := strings.Index(enum, "\n        \"shape_note\""); end > 0 {
		enum = enum[:end]
	}
	if !strings.Contains(enum, `"gear"`) {
		t.Errorf("the contract does not offer the gear shape:\n%s", enum)
	}

	m := regexp.MustCompile(`- "gear" is an involute SPUR gear[\s\S]*?"size": (\{[^}]*\})`).
		FindStringSubmatch(converseFraming)
	if m == nil {
		t.Fatal(`the contract has no "gear" paragraph with a "size" example`)
	}
	var size map[string]float64
	if err := json.Unmarshal([]byte(m[1]), &size); err != nil {
		t.Fatalf("the contract's gear example is not JSON a model could copy: %v\n%s", err, m[1])
	}
	if _, _, ok := geometry.GearOutlineForTest(size); !ok {
		t.Errorf("the contract's own gear example %s is refused by gear.go, so a model that "+
			"copies it gets a fault every time", m[1])
	}
	for _, key := range []string{"module", "teeth", "depth", "bore_radius"} {
		if _, ok := size[key]; !ok {
			t.Errorf("the gear example does not show %q", key)
		}
	}
}

// A spur gear is no longer a script's example.
//
// The script paragraph and the availability line both named "an involute gear
// tooth" as the canonical reason to write a script. Left in, they tell the model
// to do the one thing this shape exists so it does not have to.
func TestTheContractNoLongerSendsASpurGearToAScript(t *testing.T) {
	start := strings.Index(converseFraming, `- "script" is the last resort`)
	if start < 0 {
		t.Fatal("the contract has no script paragraph; point this fence at wherever scripts are offered")
	}
	paragraph := converseFraming[start:]
	if end := strings.Index(paragraph[1:], "\n- "); end > 0 {
		paragraph = paragraph[:end+1]
	}
	for name, text := range map[string]string{
		"the script paragraph":          paragraph,
		"the line when scripts are on":  scriptAvailability(true),
		"the line when scripts are off": scriptAvailability(false),
	} {
		if strings.Contains(strings.ToLower(text), "involute") {
			t.Errorf("%s still names an involute gear as a reason to script:\n%s", name, text)
		}
	}
}
