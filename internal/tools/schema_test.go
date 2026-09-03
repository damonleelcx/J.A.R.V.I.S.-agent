package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

func compile(t *testing.T, schema string) *Schema {
	t.Helper()
	s, err := CompileSchema("fixture", json.RawMessage(schema))
	if err != nil {
		t.Fatalf("compiling %s: %v", schema, err)
	}
	return s
}

// The defect this file exists for: a property the contract forbids reached Run
// and encoding/json discarded it in silence, while the contract went on saying
// the arguments had been validated.
func TestSchema_RefusesAPropertyTheContractForbids(t *testing.T) {
	s := compile(t, `{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`)

	err := s.Validate(json.RawMessage(`{"path":"a.txt","recursive":true}`))
	if err == nil {
		t.Fatal("an undeclared property was accepted")
	}
	if !strings.Contains(err.Error(), "recursive") {
		t.Errorf("the error does not name the offending field: %v", err)
	}
	// The reader is a model composing its next attempt, so it must be told what
	// the tool DOES accept rather than only that it was wrong.
	if !strings.Contains(err.Error(), "path") {
		t.Errorf("the error does not say what the tool accepts: %v", err)
	}
}

// additionalProperties defaults to true in JSON Schema, and a validator that
// treated an absent keyword as false would reject calls the contract allows.
func TestSchema_AllowsExtrasWhenTheSchemaDoesNotForbidThem(t *testing.T) {
	s := compile(t, `{"type":"object","properties":{"path":{"type":"string"}}}`)
	if err := s.Validate(json.RawMessage(`{"path":"a.txt","note":"hello"}`)); err != nil {
		t.Fatalf("an extra property was rejected by a schema that permits them: %v", err)
	}
}

func TestSchema_EnforcesRequiredTypesEnumsAndBounds(t *testing.T) {
	s := compile(t, `{
		"type":"object",
		"properties":{
			"scope":{"type":"string","enum":["turn","session","project"]},
			"limit":{"type":"integer","minimum":1,"maximum":200},
			"key":{"type":"string"}
		},
		"required":["scope","key"],
		"additionalProperties":false
	}`)

	cases := []struct {
		name, input, expect string
	}{
		{"missing required", `{"scope":"turn"}`, `"key" is required`},
		{"wrong type", `{"scope":"turn","key":7}`, "expected string, got number"},
		{"outside enum", `{"scope":"forever","key":"k"}`, "not one of"},
		{"below minimum", `{"scope":"turn","key":"k","limit":0}`, "below the minimum"},
		{"above maximum", `{"scope":"turn","key":"k","limit":500}`, "above the maximum"},
		{"integer given a fraction", `{"scope":"turn","key":"k","limit":1.5}`, "expected integer"},
	}
	for _, c := range cases {
		err := s.Validate(json.RawMessage(c.input))
		if err == nil {
			t.Errorf("%s: accepted %s", c.name, c.input)
			continue
		}
		if !strings.Contains(err.Error(), c.expect) {
			t.Errorf("%s: error does not say %q: %v", c.name, c.expect, err)
		}
	}

	if err := s.Validate(json.RawMessage(`{"scope":"project","key":"k","limit":25}`)); err != nil {
		t.Fatalf("a valid call was rejected: %v", err)
	}
}

// 3.0 is an integer by JSON Schema's rule. Rejecting it would refuse arguments a
// provider is entitled to send.
func TestSchema_AWholeNumberWithADecimalPointIsAnInteger(t *testing.T) {
	s := compile(t, `{"type":"object","properties":{"n":{"type":"integer"}}}`)
	if err := s.Validate(json.RawMessage(`{"n":3.0}`)); err != nil {
		t.Fatalf("3.0 was rejected as a non-integer: %v", err)
	}
}

// Every problem, not the first. One error per round trip turns a two-field
// mistake into two model calls.
func TestSchema_ReportsEveryProblemAtOnce(t *testing.T) {
	s := compile(t, `{
		"type":"object",
		"properties":{"a":{"type":"string"},"b":{"type":"integer"}},
		"required":["a","b"],
		"additionalProperties":false
	}`)
	err := s.Validate(json.RawMessage(`{"c":1}`))
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{`"a" is required`, `"b" is required`, `"c" is not a property`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error is missing %q, so the model must discover it on a later attempt: %v", want, err)
		}
	}
}

// THE load-bearing rule. A schema whose constraints this build cannot enforce is
// refused outright, because accepting it would let the contract keep claiming
// its arguments were validated while the keyword that mattered was ignored —
// the original defect, reintroduced one level down.
func TestSchema_RefusesAKeywordItCannotEnforce(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","properties":{"a":{"type":"string","pattern":"^x"}}}`,
		`{"oneOf":[{"type":"string"},{"type":"integer"}]}`,
		`{"type":"object","properties":{"a":{"type":"string"}},"dependentRequired":{"a":["b"]}}`,
		`{"type":"object","$ref":"#/definitions/other"}`,
	} {
		_, err := CompileSchema("fixture", json.RawMessage(schema))
		if err == nil {
			t.Errorf("a schema this build cannot enforce was accepted: %s", schema)
			continue
		}
		if !strings.Contains(err.Error(), "cannot validate") {
			t.Errorf("the refusal does not say why: %v", err)
		}
		// And it must say what to do, or the next person deletes the check.
		if !strings.Contains(err.Error(), "supportedKeywords") {
			t.Errorf("the refusal does not say how to add the keyword: %v", err)
		}
	}
}

// `additionalProperties: {schema}` is legal JSON Schema and is not implemented.
// Read as `true` it would silently widen every contract that used it.
func TestSchema_RefusesASchemaValuedAdditionalProperties(t *testing.T) {
	_, err := CompileSchema("fixture", json.RawMessage(
		`{"type":"object","additionalProperties":{"type":"string"}}`))
	if err == nil {
		t.Fatal("a schema-valued additionalProperties was accepted")
	}
	if !strings.Contains(err.Error(), "widen") {
		t.Errorf("the refusal does not say what the risk is: %v", err)
	}
}

// A contract that requires a property it never describes cannot be satisfied and
// cannot be checked. Caught at registration, where it is the tool author's
// mistake, rather than at the first call, where it looks like the model's.
func TestSchema_RefusesARequiredPropertyThatIsNotDeclared(t *testing.T) {
	_, err := CompileSchema("fixture", json.RawMessage(
		`{"type":"object","properties":{"a":{"type":"string"}},"required":["a","b"]}`))
	if err == nil {
		t.Fatal("a schema requiring an undeclared property was accepted")
	}
	if !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("the refusal does not name the property: %v", err)
	}
}

// An empty enum permits nothing, so every call would fail for a reason no
// reader could act on.
func TestSchema_RefusesAnEmptyEnum(t *testing.T) {
	if _, err := CompileSchema("fixture", json.RawMessage(`{"enum":[]}`)); err == nil {
		t.Fatal("an empty enum was accepted")
	}
}

// Absent arguments and `{}` mean the same thing to a tool with no required
// properties, and a provider may send either.
func TestSchema_AbsentArgumentsAreAnEmptyObject(t *testing.T) {
	s := compile(t, `{"type":"object","properties":{"a":{"type":"string"}}}`)
	if err := s.Validate(nil); err != nil {
		t.Fatalf("absent arguments were rejected: %v", err)
	}
	strict := compile(t, `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`)
	if err := strict.Validate(nil); err == nil {
		t.Fatal("absent arguments satisfied a required property")
	}
}

// Nested objects and arrays are checked too. A validator that stopped at the top
// level would pass exactly the calls whose shape is hardest to get right.
func TestSchema_ChecksNestedValues(t *testing.T) {
	s := compile(t, `{
		"type":"object",
		"properties":{
			"where":{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false},
			"tags":{"type":"array","items":{"type":"string"}}
		}
	}`)
	if err := s.Validate(json.RawMessage(`{"where":{"path":"a","depth":2}}`)); err == nil {
		t.Error("an undeclared nested property was accepted")
	} else if !strings.Contains(err.Error(), "where") {
		t.Errorf("the error does not locate the problem: %v", err)
	}
	if err := s.Validate(json.RawMessage(`{"tags":["a",7]}`)); err == nil {
		t.Error("a wrongly-typed array element was accepted")
	} else if !strings.Contains(err.Error(), "tags[1]") {
		t.Errorf("the error does not locate the element: %v", err)
	}
}

// Every tool this build registers must have an enforceable schema. This is the
// fence that would have caught the original gap: it holds against the real
// contracts rather than a fixture.
func TestRegistry_EveryRegisteredToolHasAnEnforceableSchema(t *testing.T) {
	r := NewRegistry()
	for _, tool := range StandardUnavailableConnectors() {
		if err := r.Register(tool); err != nil {
			t.Fatalf("%s: %v", tool.Contract().Name, err)
		}
	}
	for _, c := range r.Contracts() {
		if _, err := CompileSchema(c.Name, c.InputSchema); err != nil {
			t.Errorf("%s: %v", c.Name, err)
		}
	}
}

// An unregistered tool is NOT FOUND, never a pass. A validator that waved
// through what it could not find would be a hole in the one place that exists
// to close it.
func TestRegistry_ValidatingAnUnknownToolIsNotAPass(t *testing.T) {
	r := NewRegistry()
	err := r.ValidateInput("nothing", json.RawMessage(`{"anything":true}`))
	if errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("validating an unregistered tool returned %v", err)
	}
}

// Registration refuses a contract whose schema cannot be enforced, so a tool
// that outgrows the validator stops the process rather than running unchecked.
func TestRegistry_RefusesAToolWhoseSchemaCannotBeEnforced(t *testing.T) {
	r := NewRegistry()
	bad := NewUnavailable("fancy", "d", "r", []Capability{CapRead}, engine.RiskR0,
		json.RawMessage(`{"type":"object","properties":{"a":{"type":"string","pattern":"^x"}}}`))
	if err := r.Register(bad); err == nil {
		t.Fatal("a tool with an unenforceable schema was registered")
	}
}
