package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Checking a tool call against the schema its contract declares.
//
// # The gap this closes
//
// Contract.InputSchema was documented as "validated before the tool runs" and
// Invocation.Input as "already validated against InputSchema". Neither was true.
// The schema reached the model provider and went no further, so an argument the
// contract forbade arrived at Run and encoding/json discarded it in silence.
// Every tool was affected: the memory tools defended themselves with a strict
// decoder, workspace and shell did not.
//
// Two documented promises that nothing kept is worse than an undocumented gap,
// because the next tool gets written against the promise.
//
// # Why a subset, and why it REFUSES what it cannot check
//
// JSON Schema is large. The contracts in this build use eight keywords between
// them, and implementing the rest to a standard nobody exercises is how a
// validator acquires bugs in code no call site reaches.
//
// So this validates a bounded subset — and any schema using a keyword outside it
// is REFUSED AT REGISTRATION. That rule is the whole design. A validator that
// silently ignores `oneOf` accepts everything `oneOf` was written to reject,
// while the contract still says the arguments were validated: the exact failure
// this file exists to end, reintroduced one keyword lower. Refusing at
// registration means a tool whose schema outgrows the validator cannot start
// rather than cannot be trusted.
//
// Adding a keyword is deliberate work: implement it, add it to supportedKeywords,
// and the fence that enumerates the difference goes quiet.

// supportedKeywords is everything CompileSchema understands.
//
// Annotations are listed too. They constrain nothing, but a schema carrying one
// must not be refused — `description` is what the model reads to use the tool at
// all.
var supportedKeywords = map[string]bool{
	// Constraints.
	"type":                 true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"enum":                 true,
	"minimum":              true,
	"maximum":              true,
	"minLength":            true,
	"maxLength":            true,
	"items":                true,
	// Annotations, ignored when validating.
	"description": true,
	"title":       true,
	"$schema":     true,
	"examples":    true,
	"default":     true,
}

// Schema is a compiled input schema.
//
// Compiled once at registration rather than parsed per call: a tool is called in
// the hot path of a worker loop, and re-parsing JSON to discover the same shape
// every time is work done to reach a conclusion already known.
type Schema struct {
	raw  json.RawMessage
	node *node
}

type node struct {
	// types is the declared type(s). Empty means any.
	types []string
	// properties is nil for non-objects.
	properties map[string]*node
	required   []string
	// additionalAllowed is true unless the schema says otherwise.
	additionalAllowed bool
	enum              []json.RawMessage
	minimum, maximum  *float64
	minLen, maxLen    *int
	items             *node
}

// CompileSchema parses a contract's input schema, refusing anything it cannot
// check.
func CompileSchema(toolName string, raw json.RawMessage) (*Schema, error) {
	const op = "tools.CompileSchema"

	if len(raw) == 0 {
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("tool %q declares no input schema; arguments could not be validated", toolName)
	}
	n, err := compileNode(op, toolName, raw, "input")
	if err != nil {
		return nil, err
	}
	return &Schema{raw: raw, node: n}, nil
}

func compileNode(op, toolName string, raw json.RawMessage, path string) (*node, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, errs.Wrap(op, errs.CodeInvariantViolated, err).
			WithDetail("tool %q has a schema at %s that is not a JSON object", toolName, path)
	}

	var unsupported []string
	for k := range doc {
		if !supportedKeywords[k] {
			unsupported = append(unsupported, k)
		}
	}
	if len(unsupported) > 0 {
		sort.Strings(unsupported)
		// The load-bearing refusal. See the file comment: accepting a schema
		// whose constraints this cannot enforce would let the contract keep
		// claiming its arguments were validated while the keyword that mattered
		// was ignored.
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("tool %q uses schema keyword(s) %s at %s, which this build cannot validate. "+
				"A schema is refused rather than partly enforced: ignoring a keyword would let the "+
				"contract go on claiming its arguments were checked. Either express the constraint with "+
				"%s, or implement the keyword in tools/schema.go and add it to supportedKeywords.",
				toolName, strings.Join(quoteAll(unsupported), ", "), path,
				strings.Join(quoteAll(constraintKeywords()), ", "))
	}

	n := &node{additionalAllowed: true}

	if v, ok := doc["type"]; ok {
		types, err := stringOrStringArray(v)
		if err != nil {
			return nil, errs.New(op, errs.CodeInvariantViolated).
				WithDetail("tool %q has a %s type that is neither a string nor an array of strings", toolName, path)
		}
		for _, t := range types {
			switch t {
			case "object", "array", "string", "number", "integer", "boolean", "null":
			default:
				return nil, errs.New(op, errs.CodeInvariantViolated).
					WithDetail("tool %q declares unknown type %q at %s", toolName, t, path)
			}
		}
		n.types = types
	}

	if v, ok := doc["properties"]; ok {
		var props map[string]json.RawMessage
		if err := json.Unmarshal(v, &props); err != nil {
			return nil, errs.Wrap(op, errs.CodeInvariantViolated, err).
				WithDetail("tool %q has non-object properties at %s", toolName, path)
		}
		n.properties = map[string]*node{}
		for name, sub := range props {
			child, err := compileNode(op, toolName, sub, path+"."+name)
			if err != nil {
				return nil, err
			}
			n.properties[name] = child
		}
	}

	if v, ok := doc["required"]; ok {
		if err := json.Unmarshal(v, &n.required); err != nil {
			return nil, errs.Wrap(op, errs.CodeInvariantViolated, err).
				WithDetail("tool %q has a non-array `required` at %s", toolName, path)
		}
		// A required property the schema does not describe is a contract that
		// cannot be satisfied and cannot be checked. Caught here rather than at
		// the first call, where it would look like the model's mistake.
		for _, r := range n.required {
			if n.properties != nil {
				if _, ok := n.properties[r]; !ok {
					return nil, errs.New(op, errs.CodeInvariantViolated).
						WithDetail("tool %q requires %q at %s but does not declare it in properties", toolName, r, path)
				}
			}
		}
	}

	if v, ok := doc["additionalProperties"]; ok {
		var allowed bool
		if err := json.Unmarshal(v, &allowed); err != nil {
			// `additionalProperties: {schema}` is the other legal form and this
			// build does not implement it. Refused rather than read as `true`,
			// which would silently widen the contract.
			return nil, errs.New(op, errs.CodeInvariantViolated).
				WithDetail("tool %q sets additionalProperties to a schema at %s; this build supports only "+
					"true or false, and reading a schema as `true` would silently widen the contract", toolName, path)
		}
		n.additionalAllowed = allowed
	}

	if v, ok := doc["enum"]; ok {
		if err := json.Unmarshal(v, &n.enum); err != nil {
			return nil, errs.Wrap(op, errs.CodeInvariantViolated, err).
				WithDetail("tool %q has a non-array enum at %s", toolName, path)
		}
		if len(n.enum) == 0 {
			return nil, errs.New(op, errs.CodeInvariantViolated).
				WithDetail("tool %q declares an empty enum at %s, which permits nothing", toolName, path)
		}
	}

	if err := numberBound(doc, "minimum", &n.minimum); err != nil {
		return nil, errs.Wrap(op, errs.CodeInvariantViolated, err).
			WithDetail("tool %q has a non-numeric minimum at %s", toolName, path)
	}
	if err := numberBound(doc, "maximum", &n.maximum); err != nil {
		return nil, errs.Wrap(op, errs.CodeInvariantViolated, err).
			WithDetail("tool %q has a non-numeric maximum at %s", toolName, path)
	}
	if err := intBound(doc, "minLength", &n.minLen); err != nil {
		return nil, errs.Wrap(op, errs.CodeInvariantViolated, err).
			WithDetail("tool %q has a non-integer minLength at %s", toolName, path)
	}
	if err := intBound(doc, "maxLength", &n.maxLen); err != nil {
		return nil, errs.Wrap(op, errs.CodeInvariantViolated, err).
			WithDetail("tool %q has a non-integer maxLength at %s", toolName, path)
	}

	if v, ok := doc["items"]; ok {
		child, err := compileNode(op, toolName, v, path+"[]")
		if err != nil {
			return nil, err
		}
		n.items = child
	}
	return n, nil
}

// Validate checks one call's arguments.
//
// The error names the field and what was wrong with it, because the reader is a
// model deciding what to send next. "invalid input" is a dead end for it in
// exactly the way it would be for a person.
func (s *Schema) Validate(input json.RawMessage) error {
	const op = "tools.Schema.Validate"

	if len(input) == 0 {
		// An absent argument object is not the same as `{}`, and tools with no
		// required properties accept the latter. Normalised here so a provider
		// that omits the field entirely behaves like one that sends an empty
		// object.
		input = json.RawMessage(`{}`)
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(string(input)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return errs.Wrap(op, errs.CodeValidationFailed, err).
			WithDetail("the arguments are not valid JSON")
	}
	if problems := s.node.check(v, ""); len(problems) > 0 {
		sort.Strings(problems)
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("the arguments do not match this tool's schema: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Raw returns the schema as declared, for the model request.
func (s *Schema) Raw() json.RawMessage { return s.raw }

// check returns every problem, not the first.
//
// All of them, because the reader is a model composing its next attempt: one
// error at a time turns a two-field mistake into two round trips, each costing a
// model call.
func (n *node) check(v any, path string) []string {
	var problems []string
	at := func(format string, args ...any) string {
		where := path
		if where == "" {
			where = "the arguments"
		}
		return where + ": " + fmt.Sprintf(format, args...)
	}

	if len(n.types) > 0 && !matchesAnyType(v, n.types) {
		return []string{at("expected %s, got %s", strings.Join(n.types, " or "), jsonTypeOf(v))}
	}
	if len(n.enum) > 0 && !inEnum(v, n.enum) {
		problems = append(problems, at("%s is not one of %s", render(v), enumList(n.enum)))
	}

	switch typed := v.(type) {
	case map[string]any:
		for _, r := range n.required {
			if _, ok := typed[r]; !ok {
				problems = append(problems, at("%q is required", r))
			}
		}
		for name, value := range typed {
			sub, declared := n.properties[name]
			if !declared {
				if !n.additionalAllowed {
					problems = append(problems, at("%q is not a property this tool accepts%s", name, near(name, n.properties)))
				}
				continue
			}
			problems = append(problems, sub.check(value, join(path, name))...)
		}
	case []any:
		if n.items != nil {
			for i, item := range typed {
				problems = append(problems, n.items.check(item, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case json.Number:
		f, err := typed.Float64()
		if err == nil {
			if n.minimum != nil && f < *n.minimum {
				problems = append(problems, at("%s is below the minimum of %s", typed, trimFloat(*n.minimum)))
			}
			if n.maximum != nil && f > *n.maximum {
				problems = append(problems, at("%s is above the maximum of %s", typed, trimFloat(*n.maximum)))
			}
		}
	case string:
		if n.minLen != nil && len([]rune(typed)) < *n.minLen {
			problems = append(problems, at("is shorter than the minimum of %d characters", *n.minLen))
		}
		if n.maxLen != nil && len([]rune(typed)) > *n.maxLen {
			problems = append(problems, at("is longer than the maximum of %d characters", *n.maxLen))
		}
	}
	return problems
}

// near suggests a declared property when the one given looks like a typo.
//
// A model that sent `paths` for `path` gets told which one exists rather than
// only that its field was rejected — the difference between one retry and
// several.
func near(given string, properties map[string]*node) string {
	lower := strings.ToLower(given)
	for name := range properties {
		l := strings.ToLower(name)
		if l == lower || strings.HasPrefix(l, lower) || strings.HasPrefix(lower, l) {
			return fmt.Sprintf(" (did you mean %q?)", name)
		}
	}
	if len(properties) == 0 {
		return " (this tool accepts no properties)"
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return " (it accepts " + strings.Join(quoteAll(names), ", ") + ")"
}

func matchesAnyType(v any, types []string) bool {
	for _, t := range types {
		if matchesType(v, t) {
			return true
		}
	}
	return false
}

func matchesType(v any, t string) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		// A JSON number with no fractional part. 3.0 IS an integer by JSON
		// Schema's rule, and rejecting it would refuse arguments a provider is
		// entitled to send.
		num, ok := v.(json.Number)
		if !ok {
			return false
		}
		if _, err := num.Int64(); err == nil {
			return true
		}
		f, err := num.Float64()
		return err == nil && f == math.Trunc(f)
	case "null":
		return v == nil
	}
	return false
}

func jsonTypeOf(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case nil:
		return "null"
	}
	return "something unrecognised"
}

func inEnum(v any, enum []json.RawMessage) bool {
	// Compared by re-encoding: enum members are raw JSON, and comparing decoded
	// values would need a deep-equality rule of its own with its own corners.
	got, err := json.Marshal(v)
	if err != nil {
		return false
	}
	for _, want := range enum {
		var normalised any
		if json.Unmarshal(want, &normalised) != nil {
			continue
		}
		if canonical, err := json.Marshal(normalised); err == nil && string(canonical) == string(got) {
			return true
		}
	}
	return false
}

func enumList(enum []json.RawMessage) string {
	out := make([]string, 0, len(enum))
	for _, e := range enum {
		out = append(out, string(e))
	}
	return strings.Join(out, ", ")
}

func render(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "that value"
	}
	return string(b)
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func stringOrStringArray(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	return many, nil
}

func numberBound(doc map[string]json.RawMessage, key string, dst **float64) error {
	v, ok := doc[key]
	if !ok {
		return nil
	}
	var f float64
	if err := json.Unmarshal(v, &f); err != nil {
		return err
	}
	*dst = &f
	return nil
}

func intBound(doc map[string]json.RawMessage, key string, dst **int) error {
	v, ok := doc[key]
	if !ok {
		return nil
	}
	var i int
	if err := json.Unmarshal(v, &i); err != nil {
		return err
	}
	*dst = &i
	return nil
}

func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}

// constraintKeywords is the supported set minus the annotations, for the message
// a refused schema produces. Derived rather than written twice: a second list
// would go stale the moment a keyword was added.
func constraintKeywords() []string {
	annotations := map[string]bool{"description": true, "title": true, "$schema": true, "examples": true, "default": true}
	out := []string{}
	for k := range supportedKeywords {
		if !annotations[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func trimFloat(f float64) string {
	if f == math.Trunc(f) {
		return fmt.Sprintf("%.0f", f)
	}
	return fmt.Sprintf("%g", f)
}
