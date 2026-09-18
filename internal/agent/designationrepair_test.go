package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// The two shapes the live re-check of 2026-09-17 lost whole build steps to
// (docs/spikes/2026-09-17-live-findings-fixed/data/tasks.json), each written here
// around the exact fragment the step's refusal quoted.

// Step 1: …"name": "lug_nut_standard", "value": "ISO 4032 M12", "unit": ""…
func liveStep1(lugNut string) string {
	return `{"speech": "The hub with five lug nuts.", "prototype": {"name": "Road Car Wheel", "units": "mm", "root": "hub",
	  "parameters": [
	    {"name": "hub_radius", "value": 35, "unit": "mm", "how": "chosen"},
	    {"name": "lug_nut_standard", "value": "ISO 4032 M12", "unit": ""},
	    {"name": "bolt_circle_diameter", "value": 100, "unit": "mm", "how": "standard", "source": "Common passenger car PCD"}],
	  "definitions": [
	    {"id": "hub-body", "name": "Hub Body", "shape": "cylinder", "size": {"radius": 35, "height": 40}, "size_from": {"radius": "hub_radius"}},
	    ` + lugNut + `],
	  "assemblies": [{"id": "hub", "children": [
	    {"id": "body", "ref": "hub-body"},
	    {"id": "nut", "ref": "lug-nut", "position": [50, 20, 0], "pattern": {"kind": "polar", "count": 5, "about": "y"}}]}],
	  "not_verified": ["a shape, not a result"]}}`
}

const carriedNut = `{"id": "lug-nut", "name": "Lug Nut", "shape": "standard", "standard": "ISO 4032 M12"}`

// ‼️ A designation written as a parameter's value: left out when a part already carries
// it as its "standard" (where the contract teaches a designation goes), with a note;
// refused with the field it belongs in when no part does. Never read as a number.
func TestParseReply_ADesignationAsAParametersValue(t *testing.T) {
	reply, err := parseReply(&llm.Response{Content: liveStep1(carriedNut)})
	if err != nil || reply.Prototype == nil {
		t.Fatalf("the live step-1 reply is still lost: %v", err)
	}
	for _, p := range reply.Prototype.Parameters {
		if p.Name == "lug_nut_standard" {
			t.Errorf("the designation was kept as a parameter with the value %v", p.Value)
		}
	}
	if len(reply.Prototype.Parameters) != 2 {
		t.Errorf("the other parameters were not kept: %+v", reply.Prototype.Parameters)
	}
	want := `Parameter "lug_nut_standard" held the designation "ISO 4032 M12", which is not a number: a designation ` +
		`belongs on the part, as its "standard", and "lug-nut" already carries it there.`
	if !strings.Contains(reply.Repaired, want) {
		t.Errorf("the turn does not say the parameter was left out:\n%s", reply.Repaired)
	}

	// No part carries it: which part it belongs to is not known, so it is refused, and
	// the refusal names the field.
	orphan := liveStep1(`{"id": "lug-nut", "name": "Lug Nut", "shape": "cylinder", "size": {"radius": 9, "height": 10}}`)
	if r, _ := parseReply(&llm.Response{Content: orphan}); r.Prototype != nil {
		t.Errorf("a designation no part carries was read into a model: %+v", r.Prototype.Parameters)
	}
	why := unreadableDetail(&llm.Response{Content: orphan})
	for _, w := range []string{`"prototype.parameters.value" is a JSON string`,
		`Parameter "lug_nut_standard" holds the designation "ISO 4032 M12", and a parameter's "value" is a number. ` +
			`A designation belongs on the part itself: "shape": "standard", "standard": "ISO 4032 M12".`} {
		if !strings.Contains(why, w) {
			t.Errorf("the refusal does not say %q:\n%s", w, why)
		}
	}

	// Any other string stays exactly as unreadable as it was.
	other := strings.Replace(liveStep1(carriedNut), `"ISO 4032 M12", "unit": ""`, `"twelve", "unit": ""`, 1)
	if r, _ := parseReply(&llm.Response{Content: other}); r.Prototype != nil {
		t.Error("a parameter whose value is ordinary text was read into a model")
	}
}

// Step 2: …"size": { "radius_from": "hub_center_diameter / 2", "height"…
const liveStep2 = `{"speech": "The rim and the tyre.", "prototype_edit": {"patch": {
  "parameters": [{"name": "hub_center_diameter", "value": 70, "unit": "mm", "how": "chosen"}],
  "definitions": [{"id": "hub-cap", "name": "Hub Cap", "shape": "cylinder",
    "size": { "radius_from": "hub_center_diameter / 2", "height": 12 }}],
  "assemblies": [{"id": "rim-tyre", "children": [{"id": "cap", "ref": "hub-cap"}]}]}}}`

// ‼️ A binding written inside "size" under its own name is lifted to the binding the
// contract teaches, "size_from", under the dimension's name.
func TestParseReply_ReadsABindingWrittenInsideSize(t *testing.T) {
	reply, err := parseReply(&llm.Response{Content: liveStep2})
	if err != nil || reply.PrototypeEdit == nil || reply.PrototypeEdit.Patch == nil || len(reply.PrototypeEdit.Patch.Definitions) != 1 {
		t.Fatalf("the live step-2 reply is still lost: %v", err)
	}
	d := reply.PrototypeEdit.Patch.Definitions[0]
	if d.SizeFrom["radius"] != "hub_center_diameter / 2" || d.SizeFrom["radius_from"] != "" {
		t.Errorf("the binding reads %v; want size_from.radius = hub_center_diameter / 2", d.SizeFrom)
	}
	if _, kept := d.Size["radius_from"]; kept || d.Size["height"] != 12 {
		t.Errorf("the size reads %v; want the binding lifted out and the height kept", d.Size)
	}
	if !strings.Contains(reply.Repaired, "read as the expressions they are") {
		t.Errorf("the turn does not say a dimension was read: %q", reply.Repaired)
	}
}

// And as build steps: both live replies are kept, the hub's radius follows its binding.
func TestAssemble_TheLiveRecheckStepsAreReadNotRefused(t *testing.T) {
	c := &Conversation{client: &scriptedStub{replies: []string{liveStep1(carriedNut)}}}
	first, note := c.buildOneStep(context.Background(), &Prototype{Name: "a wheel", Units: "mm"}, "a wheel",
		buildTask{Name: "Hub Assembly", What: "the hub", Assembly: "hub"}, 1, 3)
	if first == nil {
		t.Fatalf("step 1 was refused again: %s", note)
	}
	c = &Conversation{client: &scriptedStub{replies: []string{liveStep2}}}
	second, note := c.buildOneStep(context.Background(), first, "a wheel",
		buildTask{Name: "Rim and Tyre", What: "the rim", Assembly: "rim-tyre"}, 2, 3)
	if second == nil {
		t.Fatalf("step 2 was refused again: %s", note)
	}
	caps := 0
	for _, p := range second.PlacedParts() {
		if strings.HasSuffix(p.ID, "cap") {
			caps++
			if p.Size["radius"] != 35 {
				t.Errorf("the hub cap's radius is %v; its binding hub_center_diameter / 2 is 35", p.Size["radius"])
			}
		}
	}
	if caps != 1 {
		t.Errorf("%d hub cap(s) placed; want the one step 2 built", caps)
	}
}
