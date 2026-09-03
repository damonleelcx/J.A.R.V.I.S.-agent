package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// The boundary, from outside the package.
//
// The schema fences check the validator. This checks the PROMISE the contract
// makes: that a tool's Run is never reached with arguments its schema forbids.
// Written against a tool that fails loudly if it is called, because the
// difference between "validated" and "the tool coped" is invisible from any
// assertion about the result.

// tripwire refuses to run. Any invocation that reaches it is a validation gap.
type tripwire struct {
	contract tools.Contract
	ran      bool
	sawInput json.RawMessage
}

func (tw *tripwire) Contract() tools.Contract { return tw.contract }

func (tw *tripwire) Run(_ context.Context, inv tools.Invocation) (*tools.Result, error) {
	tw.ran = true
	tw.sawInput = inv.Input
	return &tools.Result{Output: json.RawMessage(`{}`)}, nil
}

func newTripwire(schema string) *tripwire {
	return &tripwire{contract: tools.Contract{
		Name:          "tripwire",
		Description:   "fails the test if it is ever reached with arguments its schema forbids",
		InputSchema:   json.RawMessage(schema),
		Capabilities:  []tools.Capability{tools.CapRead},
		RiskTier:      engine.RiskR0,
		Reversibility: tools.ReversibleNone,
		Timeout:       time.Second,
		Idempotent:    true,
		Available:     true,
	}}
}

// Contract.InputSchema is documented as checked before the tool runs. It now is,
// and this is the fence that says so: the call is refused at the registry and
// the tool is never reached.
func TestBoundary_AMalformedCallNeverReachesTheTool(t *testing.T) {
	tw := newTripwire(`{
		"type":"object",
		"properties":{"path":{"type":"string"}},
		"required":["path"],
		"additionalProperties":false
	}`)
	r := tools.NewRegistry()
	if err := r.Register(tw); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		`{}`,                                // missing a required property
		`{"path":7}`,                        // wrong type
		`{"path":"a.txt","recursive":true}`, // a property the contract forbids
		`{"recursive":true}`,                // both at once
	} {
		if err := r.ValidateInput("tripwire", json.RawMessage(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
			continue
		}
		// The executor only calls Run after ValidateInput returns nil, so a
		// refusal here is the tool not being reached. Asserted directly as
		// well, so this fence does not depend on reading the executor.
		if tw.ran {
			t.Fatalf("the tool ran with %s", bad)
		}
	}

	if err := r.ValidateInput("tripwire", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatalf("a valid call was refused: %v", err)
	}
}

// The validator must not be the reason a legitimate call fails. A gate that
// refuses correct arguments is an outage, not a control.
func TestBoundary_EveryShippedToolAcceptsItsOwnDocumentedCall(t *testing.T) {
	r := tools.NewRegistry()
	r.MustRegister(tools.ListTool{})
	r.MustRegister(tools.ReadTool{})
	r.MustRegister(tools.WriteTool{})
	r.MustRegister(tools.ShellTool{})

	calls := map[string]string{
		"workspace_list":  `{"path":".","recursive":true}`,
		"workspace_read":  `{"path":"README.md"}`,
		"workspace_write": `{"path":"out.txt","content":"hello"}`,
		"shell_run":       `{"command":"echo hi","reason":"check the shell is reachable"}`,
	}
	for name, args := range calls {
		if err := r.ValidateInput(name, json.RawMessage(args)); err != nil {
			t.Errorf("%s refused its own documented call %s: %v", name, args, err)
		}
	}
}

// And the shipped tools must actually refuse what their contracts forbid — the
// two that had no strict decoder of their own are the point of this one.
func TestBoundary_ShippedToolsRefuseWhatTheirContractsForbid(t *testing.T) {
	r := tools.NewRegistry()
	r.MustRegister(tools.ListTool{})
	r.MustRegister(tools.ReadTool{})
	r.MustRegister(tools.WriteTool{})
	r.MustRegister(tools.ShellTool{})

	cases := []struct{ tool, args, expect string }{
		{"workspace_read", `{"path":"a","depth":3}`, "depth"},
		{"workspace_write", `{"path":"a"}`, "required"},
		{"workspace_list", `{"path":".","recursive":"yes"}`, "expected boolean"},
		// shell_run requires a REASON as well as a command, and until now
		// nothing enforced it: the model could run a command and the audit
		// trail would record an empty justification for it.
		{"shell_run", `{"command":"rm -rf /tmp/x"}`, `"reason" is required`},
	}
	for _, c := range cases {
		err := r.ValidateInput(c.tool, json.RawMessage(c.args))
		if err == nil {
			t.Errorf("%s accepted %s", c.tool, c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.expect) {
			t.Errorf("%s: error does not mention %q: %v", c.tool, c.expect, err)
		}
	}
}
