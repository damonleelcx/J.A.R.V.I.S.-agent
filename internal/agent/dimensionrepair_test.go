package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The fences over reading an expression out of a numeric slot.
//
// # The fixtures are real replies
//
// Both are verbatim qwen3.7-plus output from 2026-09-06, captured while
// investigating why draws-a-turned-part scored 0 of 3 and covers-product-design
// 0 of 3 in an evaluation run. Neither was truncated; both are complete, valid
// JSON; both unmarshalled into nothing, because the model wrote its expressions
// into the fields the contract keeps for numbers. A hand-written fixture would
// have been a guess at the failure, and the guess this repository already made
// was "truncation", which was investigated and ruled out and left the real cause
// unfound for a day.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The headline: a reply that was thrown away now arrives.
func TestARealReplyThatWasLostNowParses(t *testing.T) {
	for _, name := range []string{
		"expression-in-a-number-slot-pulley.json",
		"expression-in-a-number-slot-enclosure.json",
	} {
		t.Run(name, func(t *testing.T) {
			raw := fixture(t, name)

			var before Reply
			if err := json.Unmarshal(raw, &before); err == nil {
				t.Fatal("this fixture parses as it stands, so it is not the failure it was " +
					"collected for and this test is measuring nothing")
			}

			repaired, moved := repairDimensions(raw)
			if !moved {
				t.Fatal("nothing was read out of a numeric slot")
			}
			var after Reply
			if err := json.Unmarshal(repaired, &after); err != nil {
				t.Fatalf("the repaired reply still does not parse: %v", err)
			}
			if after.Prototype == nil || len(after.Prototype.Parts) == 0 {
				t.Fatal("the reply parsed but carried no geometry")
			}
			if strings.TrimSpace(after.Speech) == "" {
				t.Error("the speech was lost. One string in one float field used to discard the " +
					"whole reply — the speech, the detail, the parameters and every part.")
			}
		})
	}
}

// The expression goes to the field the contract keeps for it, so binding can
// still evaluate it. Moving it anywhere else would parse and then draw a part
// at whatever the default is, which is worse than the refusal it replaced.
func TestAnExpressionLandsInItsOwnField(t *testing.T) {
	raw := fixture(t, "expression-in-a-number-slot-enclosure.json")
	repaired, _ := repairDimensions(raw)

	var reply Reply
	if err := json.Unmarshal(repaired, &reply); err != nil {
		t.Fatal(err)
	}
	var sawSize, sawPoint bool
	for _, p := range reply.Prototype.Parts {
		if p.SizeFrom["depth"] == "enclosure_height / 2" {
			sawSize = true
		}
		for _, pt := range p.Profile {
			if strings.Contains(pt.XFrom, "enclosure_width") {
				sawPoint = true
			}
		}
	}
	if !sawSize {
		t.Error(`size.depth's expression did not reach size_from["depth"]`)
	}
	if !sawPoint {
		t.Error("a profile point's x expression did not reach x_from")
	}
}

// The numeric key is DELETED, not zeroed.
//
// Zero is a length, a position and a radius that all mean something. A part left
// at zero because its expression could not be read afterwards would be drawn,
// wrongly and silently, where an absent key takes the documented default and is
// reported as an inference.
func TestTheNumberIsRemovedRatherThanSetToZero(t *testing.T) {
	raw := []byte(`{"prototype":{"parts":[
		{"id":"a","shape":"box","size":{"width":"plate_size","height":6}}]}}`)
	repaired, moved := repairDimensions(raw)
	if !moved {
		t.Fatal("nothing was moved")
	}
	var doc struct {
		Prototype struct {
			Parts []struct {
				Size     map[string]any    `json:"size"`
				SizeFrom map[string]string `json:"size_from"`
			} `json:"parts"`
		} `json:"prototype"`
	}
	if err := json.Unmarshal(repaired, &doc); err != nil {
		t.Fatal(err)
	}
	part := doc.Prototype.Parts[0]
	if _, present := part.Size["width"]; present {
		t.Errorf("size.width is still present as %v; a zero here draws a part with no width "+
			"and says nothing", part.Size["width"])
	}
	if part.SizeFrom["width"] != "plate_size" {
		t.Errorf("size_from.width = %q", part.SizeFrom["width"])
	}
	if part.Size["height"] != float64(6) {
		t.Errorf("the number beside it was disturbed: %v", part.Size["height"])
	}
}

// A position is an ARRAY, so its bad element is zeroed rather than removed:
// deleting one would shift every axis after it, and a bad x would become a bad y.
func TestAPositionKeepsItsShape(t *testing.T) {
	raw := []byte(`{"prototype":{"parts":[
		{"id":"a","shape":"box","position":[0,"plate_size / 4",3]}]}}`)
	repaired, moved := repairDimensions(raw)
	if !moved {
		t.Fatal("nothing was moved")
	}
	var doc struct {
		Prototype struct {
			Parts []struct {
				Position     []float64         `json:"position"`
				PositionFrom map[string]string `json:"position_from"`
			} `json:"parts"`
		} `json:"prototype"`
	}
	if err := json.Unmarshal(repaired, &doc); err != nil {
		t.Fatal(err)
	}
	p := doc.Prototype.Parts[0]
	if len(p.Position) != 3 || p.Position[2] != 3 {
		t.Errorf("position = %v; the axes after the repaired one moved", p.Position)
	}
	if p.PositionFrom["y"] != "plate_size / 4" {
		t.Errorf("position_from.y = %q", p.PositionFrom["y"])
	}
}

// A number in quotes is the same number.
//
// The other honest reading of a string in a numeric slot, and a different one:
// nothing is relocated, because "65.0" is not an expression and has no
// expression field to go to.
func TestAQuotedNumberIsThatNumber(t *testing.T) {
	raw := []byte(`{"prototype":{"parts":[
		{"id":"a","shape":"box","size":{"width":"65.0"},"position":["0","-3.5",0],
		 "profile":[{"x":"12.5","y":0}]}]}}`)
	repaired, moved := repairDimensions(raw)
	if !moved {
		t.Fatal("nothing was read")
	}
	var doc struct {
		Prototype struct {
			Parts []struct {
				Size         map[string]float64 `json:"size"`
				SizeFrom     map[string]string  `json:"size_from"`
				Position     []float64          `json:"position"`
				PositionFrom map[string]string  `json:"position_from"`
				Profile      []struct {
					X     float64 `json:"x"`
					XFrom string  `json:"x_from"`
				} `json:"profile"`
			} `json:"parts"`
		} `json:"prototype"`
	}
	if err := json.Unmarshal(repaired, &doc); err != nil {
		t.Fatalf("a quoted number was not read as one: %v", err)
	}
	p := doc.Prototype.Parts[0]
	if p.Size["width"] != 65 || p.Position[1] != -3.5 || p.Profile[0].X != 12.5 {
		t.Errorf("a quoted number came back wrong: %+v", p)
	}
	if len(p.SizeFrom) != 0 || len(p.PositionFrom) != 0 || p.Profile[0].XFrom != "" {
		t.Error("a quoted number was filed as an expression. It is not one, and an expression " +
			"field holding \"65.0\" would be evaluated as a parameter name that does not exist.")
	}
}

// A reply that already parses is returned untouched.
//
// The property that makes this safe: the repair is a rewrite of the document,
// and "we did not touch it" is stronger than "we believe our rewrite was
// faithful". It costs nothing to have, because this path is only ever reached
// when the alternative is losing the reply.
func TestAValidReplyIsNotRewritten(t *testing.T) {
	raw := []byte(`{"speech":"here","prototype":{"parts":[
		{"id":"a","shape":"box","size":{"width":60},"size_from":{"width":"plate_size"}}]}}`)
	out, moved := repairDimensions(raw)
	if moved {
		t.Error("a document with nothing wrong with it was rewritten")
	}
	if string(out) != string(raw) {
		t.Errorf("the document came back changed:\n%s", out)
	}
}

// An expression written in BOTH slots keeps the one the contract says wins.
func TestTheExpressionFieldWinsWhenBothAreWritten(t *testing.T) {
	raw := []byte(`{"prototype":{"parts":[
		{"id":"a","shape":"box","size":{"width":"wrong_one"},"size_from":{"width":"right_one"}}]}}`)
	repaired, moved := repairDimensions(raw)
	if !moved {
		t.Fatal("nothing was moved")
	}
	if !strings.Contains(string(repaired), "right_one") || strings.Contains(string(repaired), "wrong_one") {
		t.Errorf("the stray copy replaced the expression field: %s", repaired)
	}
}

// What is NOT repairable stays unrepaired, and fails as it did.
//
// A string where the contract has no expression twin — a parameter's value, say,
// written as prose — is not something this can read, and inventing a reading for
// it would be the guessing the whole system refuses.
func TestSomethingWithNoHonestReadingIsLeftAlone(t *testing.T) {
	raw := []byte(`{"prototype":{"parameters":[{"name":"n","value":"about sixty"}],"parts":[]}}`)
	_, moved := repairDimensions(raw)
	if moved {
		t.Error("prose in a parameter's value was 'repaired'. There is no honest reading of it, " +
			"and one invented here would be a number nobody chose.")
	}
}
