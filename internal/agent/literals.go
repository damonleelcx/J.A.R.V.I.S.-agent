package agent

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Positions a build step typed as the number a parameter already holds.
//
// # The problem this solves
//
// Every live car of 2026-09-15 placed every part by literal arithmetic: 36, 11 and 6
// literal positions, 0 bound, in documents that declared track, wheelbase and wheel
// size as parameters (docs/spikes/2026-09-15-car-quality). A position typed as 1350
// where half_wheelbase is 1350 is the same number today and a wheel left behind the
// first time somebody changes the wheelbase — which is the one thing parameters are for.
//
// # Why a note and never a refusal
//
// A literal that equals a parameter can be a coincidence (a 100 mm offset beside a
// 100 mm arm), and the model is right to place by number where nothing follows. So
// this never refuses and never rewrites; it says which parameter each repeated number
// is, by name, once a step has done it often enough to be a habit rather than a
// coincidence. It reads only what the step introduced, so a model carrying a literal
// from an earlier step is told once, not on every step after.
//
// # Why deterministic
//
// The note is in the step's result, the stream a person watches and the live
// measurement; it is built in document order and never from map order, so the same
// step says the same thing.
// Fence: TestAssemble_AStepThatRetypesAParametersValueIsToldWhichParameter.

// duplicatedLiteralsToNote is how many repeated coordinates make a habit: one or two
// can be chance.
const duplicatedLiteralsToNote = 3

// maxLiteralValuesNamed and maxLiteralPlacesNamed bound the note.
const (
	maxLiteralValuesNamed = 3
	maxLiteralPlacesNamed = 3
)

// maxParametersShown bounds the parameter line a step is shown.
const maxParametersShown = 40

type namedValue struct {
	name   string
	number float64
	unit   string
	expr   string
}

// resolvedValues is the document's parameters then its derived values, in the
// order they are declared and resolved, with what each works out to.
func resolvedValues(d *Prototype) []namedValue {
	if d == nil || (len(d.Parameters) == 0 && len(d.Derived) == 0) {
		return nil
	}
	res := d.Resolve()
	var out []namedValue
	seen := map[string]bool{}
	add := func(name string) {
		v, ok := res.Values[name]
		if !ok || seen[name] || math.IsNaN(v.Number) || math.IsInf(v.Number, 0) {
			return
		}
		seen[name] = true
		out = append(out, namedValue{name: name, number: v.Number, unit: v.Unit, expr: v.Expression})
	}
	for _, p := range d.Parameters {
		add(strings.ToLower(strings.TrimSpace(p.Name)))
	}
	for _, name := range res.Order {
		add(name)
	}
	return out
}

// parametersForStep is the line that shows a step the parameters it can write
// positions and sizes with, as they stand, or "" when the model has none.
//
// The step's view already carries the parameters and derived EXPRESSIONS; a derived
// value's number is not in it, and a model that cannot see 1350 beside
// half_wheelbase cannot notice it is about to type it.
// Fence: TestAssemble_AStepIsShownTheParametersItCanBindTo.
func parametersForStep(d *Prototype) string {
	values := resolvedValues(d)
	if len(values) == 0 {
		return ""
	}
	var items []string
	for i, v := range values {
		if i == maxParametersShown {
			items = append(items, fmt.Sprintf("and %d more", len(values)-i))
			break
		}
		item := v.name + " = " + formatNumber(v.number)
		if v.unit != "" {
			item += " " + v.unit
		}
		if v.expr != "" {
			item += " (" + v.expr + ")"
		}
		items = append(items, item)
	}
	return "\n\nParameters to write positions and sizes with, as they stand now: " + strings.Join(items, ", ") +
		". Write the name, not the number."
}

// literalUse is one coordinate a step typed that a parameter already holds.
type literalUse struct {
	place    string
	value    float64
	negative bool
}

// literalPositionNote says which parameters a step's new positions retyped as
// numbers, or "" when it did so fewer than duplicatedLiteralsToNote times.
func literalPositionNote(before, after *Prototype) string {
	var lengths []namedValue
	for _, v := range resolvedValues(after) {
		if _, isLength := geometry.ParseUnit(v.unit); isLength && v.number != 0 {
			lengths = append(lengths, v)
		}
	}
	if len(lengths) == 0 {
		return ""
	}
	var keys []string
	uses := map[string][]literalUse{}
	total := 0
	scan := func(place string, pos []float64, from map[string]string) {
		for i, lit := range pos {
			if i > 2 || lit == 0 || from[positionAxes[i]] != "" {
				continue
			}
			var names []string
			for _, v := range lengths {
				if math.Abs(math.Abs(lit)-math.Abs(v.number)) <= 1e-9*math.Max(1, math.Abs(v.number)) {
					names = append(names, v.name)
				}
			}
			if len(names) == 0 {
				continue
			}
			key := strings.Join(names, " or ")
			if _, seen := uses[key]; !seen {
				keys = append(keys, key)
			}
			// The axis a name goes under, and the sign the parameter needs: a literal is
			// half_wheelbase or -half_wheelbase, never the other way round.
			uses[key] = append(uses[key], literalUse{place: place + " " + positionAxes[i], value: lit,
				negative: (lit < 0) != (firstValue(lengths, names[0]) < 0)})
			total++
		}
	}
	oldParts := positionsByID(before, func(d *Prototype) []geometry.Part { return d.Parts })
	for _, p := range after.Parts {
		if !samePosition(oldParts, p.ID, p.Position) {
			scan(p.ID, p.Position, p.PositionFrom)
		}
	}
	oldDefs := positionsByID(before, func(d *Prototype) []geometry.Part { return d.Definitions })
	for _, p := range after.Definitions {
		if !samePosition(oldDefs, p.ID, p.Position) {
			scan("definition "+p.ID, p.Position, p.PositionFrom)
		}
	}
	oldPlacements := map[string][]float64{}
	if before != nil {
		for _, a := range before.Assemblies {
			for _, c := range a.Children {
				oldPlacements[a.ID+"/"+c.ID] = c.Position
			}
			for _, f := range a.Interfaces {
				oldPlacements[a.ID+" interface "+f.ID] = f.Position
			}
		}
	}
	for _, a := range after.Assemblies {
		for _, c := range a.Children {
			if key := a.ID + "/" + c.ID; !samePosition(oldPlacements, key, c.Position) {
				scan(key, c.Position, nil)
			}
		}
		for _, f := range a.Interfaces {
			if key := a.ID + " interface " + f.ID; !samePosition(oldPlacements, key, f.Position) {
				scan(key, f.Position, nil)
			}
		}
	}
	if total < duplicatedLiteralsToNote {
		return ""
	}

	var groups []string
	for i, key := range keys {
		if i == maxLiteralValuesNamed {
			groups = append(groups, fmt.Sprintf("and %d more", len(keys)-i))
			break
		}
		list := uses[key]
		var places []string
		for j, u := range list {
			if j == maxLiteralPlacesNamed {
				places = append(places, fmt.Sprintf("%d more", len(list)-j))
				break
			}
			places = append(places, u.place+" = "+formatNumber(u.value))
		}
		groups = append(groups, fmt.Sprintf("%s is %s (%s)", formatNumber(math.Abs(list[0].value)), key, strings.Join(places, ", ")))
	}
	first := uses[keys[0]][0]
	name := strings.SplitN(keys[0], " or ", 2)[0]
	if first.negative {
		name = "-" + name
	}
	axis := first.place[strings.LastIndex(first.place, " ")+1:]
	return fmt.Sprintf("This step typed %d position(s) as the number a parameter already holds: %s. Write the "+
		"parameter's name so the position follows it: \"position_from\": {%q: %q} on a part or a definition, and "+
		"%q in a child's or an interface's \"position\", which FORGE works out from the parameters.",
		total, strings.Join(groups, "; "), axis, name, name)
}

func firstValue(values []namedValue, name string) float64 {
	for _, v := range values {
		if v.name == name {
			return v.number
		}
	}
	return 0
}

func positionsByID(d *Prototype, list func(*Prototype) []geometry.Part) map[string][]float64 {
	out := map[string][]float64{}
	if d == nil {
		return out
	}
	for _, p := range list(d) {
		out[p.ID] = p.Position
	}
	return out
}

// samePosition reports whether key was already at pos before the step.
func samePosition(before map[string][]float64, key string, pos []float64) bool {
	old, ok := before[key]
	if !ok || len(old) != len(pos) {
		return false
	}
	for i := range old {
		if old[i] != pos[i] {
			return false
		}
	}
	return true
}

func formatNumber(v float64) string {
	return strconv.FormatFloat(math.Round(v*1e6)/1e6, 'f', -1, 64)
}
