package agent

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
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
	out, relocated, evaluated := repairDimensionsNoted(raw)
	return out, relocated || evaluated
}

// repairDimensionsNoted is repairDimensions saying which reading it made: an
// expression moved to its "_from" twin (relocated), or a placement's expression
// worked out to its number because a placement has no twin (evaluated).
func repairDimensionsNoted(raw []byte) ([]byte, bool, bool) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Not JSON at all. Nothing here can help, and pretending otherwise
		// would replace one failure with a more confusing one.
		return raw, false, false
	}
	// ‼️ Every list of parts a reply can carry, not only a whole prototype's "parts".
	// A tree's parts are its DEFINITIONS, and every step of a build after the first
	// arrives as an edit's patch. Measured live 2026-09-15 (car-quality run 1): a
	// chassis written as a tree, "size": {"width": "beam_width"} on a definition, and
	// the whole first step was lost because this read prototype.parts alone.
	// docs/bugfix/2026-09-15-an-expression-in-a-definition-lost-the-whole-reply.md
	// Fence: TestParseReply_ReadsAnExpressionInADefinitionsSize.
	var lists []any
	evaluated := false
	if proto, ok := doc["prototype"].(map[string]any); ok {
		lists = append(lists, proto["parts"], proto["definitions"])
		evaluated = repairPlacements(proto) || evaluated
	}
	if edit, ok := doc["prototype_edit"].(map[string]any); ok {
		if patch, ok := edit["patch"].(map[string]any); ok {
			lists = append(lists, patch["parts"], patch["definitions"])
			evaluated = repairPlacements(patch) || evaluated
		}
	}
	moved := false
	for _, list := range lists {
		parts, ok := list.([]any)
		if !ok {
			continue
		}
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
	}
	if !moved && !evaluated {
		return raw, false, false
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, false, false
	}
	return out, moved, evaluated
}

// childPositionNote tells the reader a placement's expression was read at its value
// and kept as its binding. It names a pattern's bindings from geometry's own table
// since 2026-09-17 (bound patterns): repairPattern reads the same fields.
var childPositionNote = `A position on a placed child or an interface, or a child's pattern step or angle, ` +
	`arrived as an expression. It was placed at what the expression works out to from the parameters now, and ` +
	`the expression was kept as its "position_from" (a pattern's ` + patternFroms() + `), so it follows ` +
	`those parameters when they change.`

// patternFroms is every pattern binding's key, quoted, from geometry's table.
func patternFroms() string {
	var froms []string
	for _, b := range geometry.PatternBindings() {
		froms = append(froms, strconv.Quote(b.From))
	}
	return strings.Join(froms, ", ")
}

// stepPatternFields is every pattern field that may carry a parameter's name, quoted,
// from geometry's table: what a build step is told (stepSystem).
func stepPatternFields() string {
	var fields []string
	for _, b := range geometry.PatternBindings() {
		fields = append(fields, strconv.Quote(b.Field))
	}
	if len(fields) < 2 {
		return strings.Join(fields, "")
	}
	return strings.Join(fields[:len(fields)-1], ", ") + " or " + fields[len(fields)-1]
}

// repairPlacements reads the positions of a tree's children and interfaces.
//
// # The problem this solves
//
// Measured live 2026-09-15 (car-quality run 2): after definitions were read, the
// chassis step was still lost, to "position": ["-half_wheelbase + 200", 0, 0] on a
// child. A child's place had no "position_from" to move the expression to, and the
// whole reply was discarded.
//
// # Why its value, and only when it can be worked out
//
// The model wrote the relationship it meant, over parameters it declared in the same
// reply; the number that relationship gives now is the one it was placing. So a
// quoted number is read as the number, and an expression is evaluated by FORGE's own
// binder against the reply's parameters and derived values. An expression that does
// not evaluate — an unknown name, a unit mismatch, bad grammar — is left as it was and
// fails exactly as before: nothing is guessed.
// docs/bugfix/2026-09-15-a-build-steps-edit-replaced-the-models-root.md
// Fence: TestParseReply_ReadsAnExpressionInAChildsPositionAtItsValue.
//
// # And why the expression is kept
//
// ‼️ #97 kept only the number, so a respec of half_wheelbase moved every definition
// bound to it and left the wheels placed at the old one. A child and an interface now
// have "position_from" (geometry/tree.go), and an expression that evaluates is written
// there beside its number: every reader still draws the number, and a respec moves it.
// A quoted number is a number and binds nothing. The model's own "position_from" for
// that axis, when it sent one, wins, as it does on a part.
// Fence: TestAssemble_AStepsChildPlacedByAParameterFollowsItAfterStorageAndRespec.
func repairPlacements(container map[string]any) bool {
	asms, ok := container["assemblies"].([]any)
	if !ok {
		return false
	}
	units, _ := container["units"].(string)
	moved := false
	for _, a := range asms {
		asm, ok := a.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"children", "interfaces"} {
			list, _ := asm[key].([]any)
			for _, item := range list {
				obj, ok := item.(map[string]any)
				if !ok {
					continue
				}
				// A child's pattern step and angle, by the same reading (2026-09-17, bound
				// patterns). Fence: TestAssemble_AStepsPatternWrittenWithAParameterFollowsIt.
				if pattern, isPattern := obj["pattern"].(map[string]any); isPattern && key == "children" {
					moved = repairPattern(container, pattern, units) || moved
				}
				pos, ok := obj["position"].([]any)
				if !ok {
					continue
				}
				for i, v := range pos {
					s, isString := v.(string)
					if !isString {
						continue
					}
					if f, isNum := asNumber(v); isNum {
						pos[i], moved = f, true
						continue
					}
					if f, ok := evaluateOver(container, s, units); ok {
						pos[i], moved = f, true
						bindPlacementAxis(obj, i, s)
					}
				}
			}
		}
	}
	return moved
}

// bindPlacementAxis keeps a placement's expression for axis i as its position_from,
// unless the placement already binds that axis.
func bindPlacementAxis(obj map[string]any, i int, expr string) {
	bindAxisAs(obj, "position_from", i, expr)
}

// bindAxisAs keeps expr for axis i under obj[fromKey], unless that axis is bound already.
func bindAxisAs(obj map[string]any, fromKey string, i int, expr string) {
	if i >= len(positionAxes) {
		return
	}
	from, _ := obj[fromKey].(map[string]any)
	if from == nil {
		from = map[string]any{}
		obj[fromKey] = from
	}
	if _, taken := from[positionAxes[i]]; !taken {
		from[positionAxes[i]] = expr
	}
}

// repairPattern reads a child's pattern fields that geometry.PatternBindings names, the
// way repairPlacements reads a position: a quoted number is its number, and an
// expression that evaluates over the reply's parameters is placed at its value and
// kept as the field's binding ("offset_from" per axis, "angle_from"), unless the model
// bound it itself. One that does not evaluate is left to fail as before.
func repairPattern(container, pattern map[string]any, units string) bool {
	moved := false
	for _, b := range geometry.PatternBindings() {
		if b.Vector {
			vec, _ := pattern[b.Field].([]any)
			for i, v := range vec {
				expr, isString := v.(string)
				if !isString {
					continue
				}
				if f, isNum := asNumber(v); isNum {
					vec[i], moved = f, true
					continue
				}
				if f, ok := evaluateOver(container, expr, units); ok {
					vec[i], moved = f, true
					bindAxisAs(pattern, b.From, i, expr)
				}
			}
			continue
		}
		expr, isString := pattern[b.Field].(string)
		if !isString {
			continue
		}
		if f, isNum := asNumber(expr); isNum {
			pattern[b.Field], moved = f, true
			continue
		}
		if f, ok := evaluateOver(container, expr, units); ok {
			pattern[b.Field], moved = f, true
			if _, taken := pattern[b.From]; !taken {
				pattern[b.From] = expr
			}
		}
	}
	return moved
}

// evaluateOver works out one expression over a reply's parameters and derived
// values, with the binder every bound dimension goes through: a probe part whose x
// is bound to it. False when the binder reports an error.
func evaluateOver(container map[string]any, expr, units string) (float64, bool) {
	if units == "" {
		units = "mm"
	}
	probe := map[string]any{"name": "probe", "units": units,
		"parts": []any{map[string]any{"id": "probe", "shape": "box",
			"size":     map[string]any{"width": 1, "height": 1, "depth": 1},
			"position": []any{0, 0, 0}, "position_from": map[string]any{"x": expr}}}}
	if p, ok := container["parameters"]; ok {
		probe["parameters"] = p
	}
	if d, ok := container["derived"]; ok {
		probe["derived"] = d
	}
	raw, err := json.Marshal(probe)
	if err != nil {
		return 0, false
	}
	var d geometry.Document
	if json.Unmarshal(raw, &d) != nil {
		return 0, false
	}
	for _, p := range d.Bind() {
		if p.Severity == geometry.Error {
			return 0, false
		}
	}
	if len(d.Parts) != 1 || len(d.Parts[0].Position) != 3 {
		return 0, false
	}
	return d.Parts[0].Position[0], true
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

// withBaseParameters runs run with container's "parameters", "derived" and "units"
// widened by the model a build step or an edit adds to, then puts them back.
//
// ‼️ A step's patch names the model's parameters without restating them (2026-09-15,
// attach and bind). A child written at ["-half_wheelbase", 0, 0] in a patch that does
// not itself declare half_wheelbase was worked out over the patch's parameters alone,
// failed, and lost the whole step as unreadable — so teaching steps to write a
// parameter's name where they place a child would have taught them to lose the step.
// The container's own entries win a name clash, as a patch's win when it is merged.
// Put back afterwards, so what is parsed is exactly what the model sent, read.
// Fence: TestAssemble_AStepsPlacementExpressionReadsTheModelsParameters.
func withBaseParameters(container map[string]any, base *Prototype, run func() bool) bool {
	if base == nil || (len(base.Parameters) == 0 && len(base.Derived) == 0) {
		return run()
	}
	keys := []string{"parameters", "derived", "units"}
	saved, had := map[string]any{}, map[string]bool{}
	for _, k := range keys {
		saved[k], had[k] = container[k]
	}
	own := map[string]bool{}
	for _, k := range []string{"parameters", "derived"} {
		list, _ := container[k].([]any)
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				if name, ok := m["name"].(string); ok {
					own[strings.ToLower(strings.TrimSpace(name))] = true
				}
			}
		}
	}
	widen := func(key string, fromBase any) {
		var inherited []any
		if raw, err := json.Marshal(fromBase); err == nil {
			var list []any
			if json.Unmarshal(raw, &list) == nil {
				for _, item := range list {
					if m, ok := item.(map[string]any); ok {
						if name, ok := m["name"].(string); ok && own[strings.ToLower(strings.TrimSpace(name))] {
							continue
						}
					}
					inherited = append(inherited, item)
				}
			}
		}
		if list, ok := saved[key].([]any); ok {
			inherited = append(inherited, list...)
		}
		container[key] = inherited
	}
	widen("parameters", base.Parameters)
	widen("derived", base.Derived)
	if u, _ := container["units"].(string); u == "" && base.Units != "" {
		container["units"] = base.Units
	}
	defer func() {
		for _, k := range keys {
			if had[k] {
				container[k] = saved[k]
			} else {
				delete(container, k)
			}
		}
	}()
	return run()
}

// placementsOverModel rewrites a reply whose edit places a child or an interface by
// an expression over the model's parameters into the reply with those positions
// read, and says whether it did. A reply that parses as sent, a reply that is not an
// edit, and a model with no parameters are returned untouched.
func placementsOverModel(resp *llm.Response, base *Prototype) (*llm.Response, bool) {
	if resp == nil || base == nil || (len(base.Parameters) == 0 && len(base.Derived) == 0) {
		return resp, false
	}
	body := []byte(extractJSON(resp.Content))
	var strict Reply
	if json.Unmarshal(body, &strict) == nil {
		return resp, false
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return resp, false
	}
	edit, _ := doc["prototype_edit"].(map[string]any)
	patch, _ := edit["patch"].(map[string]any)
	if patch == nil || !withBaseParameters(patch, base, func() bool { return repairPlacements(patch) }) {
		return resp, false
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return resp, false
	}
	read := *resp
	read.Content = string(out)
	return &read, true
}
