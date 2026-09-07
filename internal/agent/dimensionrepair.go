package agent

import (
	"encoding/json"
	"strconv"
)

// An expression written where a number was expected.
//
// # The defect, and why it cost whole documents
//
// The contract offers TWO spellings for every dimension: a literal (`x`,
// `size.width`) and an expression (`x_from`, `size_from`), and the framing then
// says, in bold and more than once, to PREFER the expression. A model that takes
// that instruction and writes the expression into the literal slot produces:
//
//	"profile": [{"x": "-enclosure_width / 2 + wall_thickness", "y": 0}]
//	"size":    {"depth": "enclosure_height / 2"}
//
// which is valid JSON, complete, and unmarshals into nothing: Go rejects a
// string in a float64 field, and one such field throws away the ENTIRE reply —
// the speech, the detail, the parameters and every other part. The person is
// told "that reply came back in a shape FORGE could not read".
//
// # This is the defect the implementation plan has carried unexplained
//
// converseMaxTokens' comment records that a V-belt pulley failed to parse on
// 2026-09-05, that truncation was investigated and ruled out, and that "that
// reply was simply malformed, intermittently". It was not malformed. It was this,
// and the pulley is the shape most likely to provoke it because its outline is
// the one place a model has several dimensions to relate to each other.
//
// Measured 2026-09-06 against qwen3.7-plus at --repeats 3: draws-a-turned-part
// scored 0 of 3 and covers-product-design 0 of 3, both entirely from this. The
// suite reported a capability that had gone dead; what had actually happened is
// that the reply was being discarded on arrival.
//
// # Why the fix is a reading and not a stricter prompt
//
// The model is not wrong in any way a person would recognise. It was asked for an
// expression, it wrote one, and it put it beside the name of the thing it
// describes. The contract's two slots are the awkward part, and telling the model
// more firmly which to use makes the failure rarer without making it survivable —
// while the cost of the failure stays "the whole document".
//
// So a string in a numeric slot is READ. There are exactly two honest readings
// and this does both:
//
//   - it is a number in quotes ("65.0") — the same value, said differently.
//   - it is an expression, and the contract has a field for the expression of
//     that very dimension. It is moved there, which is where the model meant it.
//
// Anything else is left exactly as it was, and fails exactly as it did.
//
// # Why it runs only after a strict parse has already failed
//
// So that a reply which already parses is never touched. This walks the document
// as generic JSON and writes it out again, and while that is value-preserving
// for everything the contract carries, "we did not touch it" is a stronger
// property than "we believe our rewrite was faithful" — and it costs nothing to
// have, because this path is reached only when the alternative is losing the
// reply.

// dimensionFields are the numeric fields of a point that have an expression
// twin, mapped to the twin's name.
//
// A table, because the alternative is one `if` per field in three places, and
// the field somebody forgot to add would silently keep costing whole documents.
var dimensionFields = map[string]string{
	"x":      "x_from",
	"y":      "y_from",
	"z":      "z_from",
	"radius": "radius_from",
}

// positionAxes names the position array's elements, which the contract addresses
// by name in position_from and by index in position.
var positionAxes = []string{"x", "y", "z"}

// repairDimensions reads expressions out of numeric slots, returning the
// document to parse and whether anything was moved.
//
// The bool is returned so the caller can say what it did. A repair nobody
// mentions is a document silently different from the one the model sent, which
// is the same class of thing as a render that does not match its file.
func repairDimensions(raw []byte) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Not JSON at all. Nothing here can help, and pretending otherwise
		// would replace one failure with a more confusing one.
		return raw, false
	}
	proto, ok := doc["prototype"].(map[string]any)
	if !ok {
		return raw, false
	}
	parts, ok := proto["parts"].([]any)
	if !ok {
		return raw, false
	}
	moved := false
	for _, p := range parts {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		moved = repairSize(part) || moved
		moved = repairPosition(part) || moved
		moved = repairPoints(part["profile"]) || moved
		moved = repairPath(part["path"]) || moved
		if holes, ok := part["holes"].([]any); ok {
			for _, hole := range holes {
				moved = repairPoints(hole) || moved
			}
		}
	}
	if !moved {
		return raw, false
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, false
	}
	return out, true
}

// asNumber reads a quoted number. Returns ok=false for anything else, including
// an expression — which is a different repair.
func asNumber(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// relocate moves an expression from a numeric key to its expression twin.
//
// The numeric key is DELETED rather than set to zero. Zero is a position, a
// length and a radius that all mean something, and a part left at zero because
// its expression could not be read would be drawn — wrongly and silently — where
// an absent key gets the documented default and is reported as an inference.
func relocate(obj map[string]any, key, twin string, expr string) {
	if _, taken := obj[twin]; taken {
		// The model wrote both. The expression field is the one the contract
		// says wins, so the stray copy is simply dropped.
		delete(obj, key)
		return
	}
	obj[twin] = expr
	delete(obj, key)
}

func repairSize(part map[string]any) bool {
	size, ok := part["size"].(map[string]any)
	if !ok {
		return false
	}
	from, _ := part["size_from"].(map[string]any)
	moved := false
	for k, v := range size {
		s, isString := v.(string)
		if !isString {
			continue
		}
		if f, isNum := asNumber(v); isNum {
			size[k] = f
			moved = true
			continue
		}
		if from == nil {
			from = map[string]any{}
			part["size_from"] = from
		}
		if _, taken := from[k]; !taken {
			from[k] = s
		}
		delete(size, k)
		moved = true
	}
	return moved
}

func repairPosition(part map[string]any) bool {
	pos, ok := part["position"].([]any)
	if !ok {
		return false
	}
	from, _ := part["position_from"].(map[string]any)
	moved := false
	for i, v := range pos {
		s, isString := v.(string)
		if !isString || i >= len(positionAxes) {
			continue
		}
		if f, isNum := asNumber(v); isNum {
			pos[i] = f
			moved = true
			continue
		}
		if from == nil {
			from = map[string]any{}
			part["position_from"] = from
		}
		if _, taken := from[positionAxes[i]]; !taken {
			from[positionAxes[i]] = s
		}
		// Zero rather than deleted, because position is an ARRAY: deleting an
		// element would shift the axes after it, so a bad x would become a bad
		// y as well.
		pos[i] = float64(0)
		moved = true
	}
	return moved
}

// repairPoints handles a profile or one hole loop.
func repairPoints(v any) bool {
	pts, ok := v.([]any)
	if !ok {
		return false
	}
	moved := false
	for _, p := range pts {
		pt, ok := p.(map[string]any)
		if !ok {
			continue
		}
		for key, twin := range dimensionFields {
			raw, present := pt[key]
			if !present {
				continue
			}
			s, isString := raw.(string)
			if !isString {
				continue
			}
			if f, isNum := asNumber(raw); isNum {
				pt[key] = f
				moved = true
				continue
			}
			relocate(pt, key, twin, s)
			moved = true
		}
	}
	return moved
}

// repairPath is the same shape as a profile and is spelled separately only
// because a reader looking for "what happens to a path" should find it.
func repairPath(v any) bool { return repairPoints(v) }
