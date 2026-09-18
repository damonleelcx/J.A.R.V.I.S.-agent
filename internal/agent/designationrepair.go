package agent

import (
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A standard's designation written as a parameter's value.
//
// # The defect
//
// The live re-check of 2026-09-17 (docs/spikes/2026-09-17-live-findings-fixed) lost
// its first build step to one line of an otherwise complete reply:
//
//	"parameters": [..., {"name": "lug_nut_standard", "value": "ISO 4032 M12", "unit": ""}, ...]
//
// A parameter's value is a number (geometry.Parameter.Value is a float64), so the
// whole reply failed to parse and the step kept nothing.
//
// # Where a designation belongs, by the contract's own teaching
//
// The contract teaches exactly one place for a designation: a catalogued part is
// {"shape": "standard", "standard": "ISO 4762 M8x30"}, and FORGE draws it at the
// published figures. A parameter "how": "standard" is a NUMBER taken from a standard,
// with its source. So a designation in a parameter is never read as a number — there
// is no number to guess — and it is never a parameter at all:
//
//   - when a part or definition in the same reply already carries that designation in
//     its "standard", the parameter only restated it. It is left out, and the turn
//     says so. Nothing the model drew changes.
//   - when none does, nothing here can say which part it belongs to. The reply stays
//     unreadable, as it was, and the step's note names the field the designation
//     goes in, so the next attempt can write it there.
//
// Only a designation is read this way: a catalogued one, or text that opens with a
// standards body's prefix. Any other string in a parameter's value is left exactly as
// it was, and fails exactly as it did.

// standardsBodies are the prefixes a designation opens with.
var standardsBodies = []string{"ISO ", "DIN ", "EN ", "BS ", "ANSI ", "ASME ", "JIS ", "GB ", "SAE ", "ASTM "}

// looksLikeDesignation reports whether s is a standard's designation rather than a
// number or an expression.
func looksLikeDesignation(s string) bool {
	if geometry.IsStandardDesignation(s) {
		return true
	}
	u := strings.ToUpper(strings.TrimSpace(s))
	for _, p := range standardsBodies {
		if strings.HasPrefix(u, p) && len(u) > len(p) {
			return true
		}
	}
	return false
}

// repairDesignationParameters leaves out every parameter whose value restates a
// designation a part in the same document carries, and returns one sentence for
// each, and one remedy for each designation no part carries (left in place).
func repairDesignationParameters(doc map[string]any) (dropped, refused []string) {
	var docs []map[string]any
	if proto, ok := doc["prototype"].(map[string]any); ok {
		docs = append(docs, proto)
	}
	if edit, ok := doc["prototype_edit"].(map[string]any); ok {
		if patch, ok := edit["patch"].(map[string]any); ok {
			docs = append(docs, patch)
		}
	}
	for _, d := range docs {
		params, ok := d["parameters"].([]any)
		if !ok {
			continue
		}
		carried := map[string]string{} // normalised designation -> part id
		for _, key := range []string{"parts", "definitions"} {
			list, _ := d[key].([]any)
			for _, p := range list {
				part, _ := p.(map[string]any)
				if s, ok := part["standard"].(string); ok && s != "" {
					if _, seen := carried[normalisedDesignation(s)]; !seen {
						id, _ := part["id"].(string)
						carried[normalisedDesignation(s)] = id
					}
				}
			}
		}
		kept := params[:0:0]
		for _, p := range params {
			param, _ := p.(map[string]any)
			value, isString := param["value"].(string)
			if !isString || !looksLikeDesignation(value) {
				kept = append(kept, p)
				continue
			}
			name, _ := param["name"].(string)
			if id, ok := carried[normalisedDesignation(value)]; ok {
				dropped = append(dropped, fmt.Sprintf("Parameter %q held the designation %q, which is not a number: "+
					"a designation belongs on the part, as its \"standard\", and %q already carries it there. The "+
					"parameter was left out; nothing drawn changed.", name, value, id))
				continue
			}
			refused = append(refused, fmt.Sprintf("Parameter %q holds the designation %q, and a parameter's "+
				"\"value\" is a number. A designation belongs on the part itself: \"shape\": \"standard\", "+
				"\"standard\": %q. Write it there and leave the parameter out.", name, value, value))
			kept = append(kept, p)
		}
		if len(kept) != len(params) {
			d["parameters"] = kept
		}
	}
	return dropped, refused
}

// normalisedDesignation compares designations the way the catalogue does.
func normalisedDesignation(s string) string {
	return strings.Join(strings.Fields(strings.ToUpper(strings.ReplaceAll(s, "×", "x"))), " ")
}
