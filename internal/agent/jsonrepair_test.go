package agent

import (
	"encoding/json"
	"testing"
)

// TestRepairJSONFixesTheMalformationModelsActuallyProduce is grounded in
// measurement, not speculation.
//
// Four models were asked for a JSON object containing prose fields. Three
// returned a literal newline inside a string value, which RFC 8259 forbids and
// encoding/json rejects — so an otherwise correct plan was discarded over a line
// break. This is the repair for exactly that.
func TestRepairJSONFixesTheMalformationModelsActuallyProduce(t *testing.T) {
	cases := map[string]struct {
		in    string
		field string
		want  string
	}{
		"raw newline in a string": {
			in:    "{\"rationale\":\"first line\nsecond line\"}",
			field: "rationale",
			want:  "first line\nsecond line",
		},
		"raw tab": {
			in:    "{\"rationale\":\"a\tb\"}",
			field: "rationale",
			want:  "a\tb",
		},
		"carriage return": {
			in:    "{\"rationale\":\"a\r\nb\"}",
			field: "rationale",
			want:  "a\r\nb",
		},
		"newline inside an escaped-quote string": {
			in:    "{\"rationale\":\"he said \\\"go\\\"\nthen left\"}",
			field: "rationale",
			want:  "he said \"go\"\nthen left",
		},
		"already valid is unchanged": {
			in:    "{\"rationale\":\"one line\"}",
			field: "rationale",
			want:  "one line",
		},
		"escaped newline is left alone": {
			in:    `{"rationale":"one\ntwo"}`,
			field: "rationale",
			want:  "one\ntwo",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repaired := repairJSON(tc.in)

			var out map[string]string
			if err := json.Unmarshal([]byte(repaired), &out); err != nil {
				t.Fatalf("still unparseable after repair: %v\ninput:    %q\nrepaired: %q",
					err, tc.in, repaired)
			}
			if out[tc.field] != tc.want {
				t.Errorf("field %q = %q, want %q — the repair changed the content, not just the encoding",
					tc.field, out[tc.field], tc.want)
			}
		})
	}
}

// TestRepairJSONLeavesValidDocumentsAlone is what makes it safe to run
// unconditionally. Structural whitespace between tokens is not inside a string
// and must not be touched.
func TestRepairJSONLeavesValidDocumentsAlone(t *testing.T) {
	valid := []string{
		`{"a":1,"b":[1,2,3]}`,
		"{\n  \"a\": 1,\n  \"b\": \"text\"\n}",
		`{"nested":{"deep":{"value":"x"}}}`,
		`{"escaped":"a\\nb","real":"c"}`,
		`{"unicode":"é"}`,
		`[]`,
		`{}`,
	}
	for _, in := range valid {
		repaired := repairJSON(in)

		var a, b any
		if err := json.Unmarshal([]byte(in), &a); err != nil {
			t.Fatalf("fixture is not valid JSON: %v", err)
		}
		if err := json.Unmarshal([]byte(repaired), &b); err != nil {
			t.Fatalf("repair broke a valid document: %v\nin:  %s\nout: %s", err, in, repaired)
		}
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		if string(x) != string(y) {
			t.Errorf("repair changed a valid document\n  in:  %s\n  out: %s", x, y)
		}
	}
}

// TestExtractJSONHandlesTheWholeMess covers the realistic case: a fenced block
// with prose around it AND a raw newline inside a string.
func TestExtractJSONHandlesTheWholeMess(t *testing.T) {
	raw := "Here is the plan:\n\n```json\n{\"rationale\":\"line one\nline two\",\"tasks\":[]}\n```\n\nLet me know."

	var out struct {
		Rationale string `json:"rationale"`
		Tasks     []any  `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		t.Fatalf("a fenced object with a raw newline did not survive extraction: %v", err)
	}
	if out.Rationale != "line one\nline two" {
		t.Errorf("rationale = %q", out.Rationale)
	}
}
