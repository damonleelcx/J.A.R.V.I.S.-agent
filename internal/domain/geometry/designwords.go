package geometry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// What a design word means as an operation on the car template.
//
// Stage C3 of the looks-designed work, 2026-09-18. "Flowing fender", "aggressive
// stance", "soft edges" are what people say, and the car template's numbers are
// what FORGE builds from. The table that joins them is ONE file
// (templates/design_words.json), rendered into the prompt by DesignWordGuide — so
// the prompt and the table cannot say two different things, and changing what a
// word means is one edit.
//
// # What a row may not do
//
// Set a count. A count (wheel nuts, fins) comes from the request, never from a
// word: the same rule sketch.go holds a generated picture to, after it drew 28-30
// teeth for a gear asked for with 20. TestDesignWords_NeverSetACountAndStayInsideTheTemplate
// fences it.

//go:embed templates/design_words.json
var designWordsJSON []byte

type designSet struct {
	Key    string    `json:"key"`
	Band   []float64 `json:"band,omitempty"`
	Toward string    `json:"toward,omitempty"`
}

type designRow struct {
	Words     []string    `json:"words"`
	Operation string      `json:"operation"`
	Meaning   string      `json:"meaning"`
	Set       []designSet `json:"set"`
	Later     string      `json:"later,omitempty"`
}

var designWords = mustReadDesignWords(designWordsJSON)

func mustReadDesignWords(raw []byte) []designRow {
	var t struct {
		Rows []designRow `json:"rows"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		panic("geometry: templates/design_words.json does not parse: " + err.Error())
	}
	return t.Rows
}

// designOperations are the operations a row may name: what the template does with
// the numbers it moves.
var designOperations = map[string]string{
	"loft":   "the body's loft through its stations",
	"stance": "where the wheels sit",
	"fillet": "how round the body's edges are",
	"cabin":  "where the cabin sits",
}

// DesignWordGuide is the design-word table as the prompt says it, one row a line.
func DesignWordGuide() string {
	var b strings.Builder
	for _, r := range designWords {
		quoted := make([]string, len(r.Words))
		for i, w := range r.Words {
			quoted[i] = fmt.Sprintf("%q", w)
		}
		sets := make([]string, 0, len(r.Set))
		for _, s := range r.Set {
			switch {
			case len(s.Band) == 2:
				sets = append(sets, fmt.Sprintf("%s %g to %g", s.Key, s.Band[0], s.Band[1]))
			case s.Toward != "":
				sets = append(sets, fmt.Sprintf("%s toward the %s end of its class's range", s.Key, s.Toward))
			}
		}
		fmt.Fprintf(&b, "      %s -> %s (%s): %s; %s\n", strings.Join(quoted, ", "), r.Operation,
			designOperations[r.Operation], r.Meaning, strings.Join(sets, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}
