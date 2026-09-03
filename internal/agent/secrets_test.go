package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/secrets"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// The executor's half of SEC-03.
//
// The domain package holds the redactor and the broker. This holds the thing
// they exist for: that a value reaching a tool does NOT come back through
// runTool into the model's context or the ledger.
//
// These are in-package because runTool is where the mechanism lives, and testing
// it through the public surface would mean standing up a model.

// echoTool returns whatever it was given, which is what a shell command, an HTTP
// client logging its own request, and a library error quoting a header all
// effectively do.
type echoTool struct{ name string }

func (e echoTool) Contract() tools.Contract {
	return tools.Contract{
		Name: e.name, Description: "echoes its input",
		InputSchema:   json.RawMessage(`{"type":"object"}`),
		Capabilities:  []tools.Capability{tools.CapRead},
		RiskTier:      engine.RiskR0,
		Reversibility: tools.ReversibleNone,
		Timeout:       time.Second, Idempotent: true, Available: true,
	}
}

func (e echoTool) Run(_ context.Context, inv tools.Invocation) (*tools.Result, error) {
	// Reveal is what a tool that genuinely needs the value calls. Echoing the
	// result is the leak this whole mechanism exists to stop.
	revealed := inv.Reveal(string(inv.Input))
	out, _ := json.Marshal(map[string]any{"echoed": revealed})
	return &tools.Result{Output: out, Raw: "ran with: " + revealed}, nil
}

// failingTool fails with the value in its error message, which is how an HTTP
// client usually leaks one.
type failingTool struct{}

func (failingTool) Contract() tools.Contract {
	return tools.Contract{
		Name: "failing", Description: "fails loudly",
		InputSchema:   json.RawMessage(`{"type":"object"}`),
		Capabilities:  []tools.Capability{tools.CapRead},
		RiskTier:      engine.RiskR0,
		Reversibility: tools.ReversibleNone,
		Timeout:       time.Second, Idempotent: true, Available: true,
	}
}

func (failingTool) Run(_ context.Context, inv tools.Invocation) (*tools.Result, error) {
	return nil, errs.New("tools.failing", errs.CodeExternalProtocol).
		WithDetail("upstream rejected the request %q", inv.Reveal(string(inv.Input)))
}

func resolutionFor(t *testing.T, values map[string]string) *secrets.Resolution {
	t.Helper()
	return &secrets.Resolution{
		Values: values, Redactor: secrets.NewRedactor(values), TooShort: secrets.SkippedShort(values),
	}
}

// The property the mechanism rests on. A tool that echoes its input must not put
// the value back into the model's context.
func TestSecrets_AToolThatEchoesItsInputDoesNotLeakTheValue(t *testing.T) {
	// Deliberately NOT shaped like a real provider's token.
	//
	// It used to read "ghp_…", which is a GitHub personal-access-token prefix,
	// and secret scanners flagged this file on every push. Nothing in the
	// redactor cares about the shape — it replaces the literal string it was
	// given — so the realism bought nothing and cost a permanently red security
	// check. A scanner that cries wolf on your own fixtures is one nobody
	// believes the day it is right.
	//
	// Still long and distinctive, so a pass cannot happen by coincidence.
	const value = "FIXTURE-not-a-real-credential-8f21c7d4e9"
	res := resolutionFor(t, map[string]string{"gh": value})

	tool := echoTool{name: "echo"}
	inv := tools.Invocation{
		Tool: "echo", Input: json.RawMessage(`{"header":"Authorization: Bearer secret://gh"}`),
		Secrets: res.Values,
	}
	out, err := tool.Run(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	// Unredacted, the leak is present — otherwise this test would pass without
	// the redactor doing anything, which is the vacuous version of it.
	if !strings.Contains(string(out.Output), value) {
		t.Fatal("the tool did not leak the value, so this test cannot show that redaction stops it")
	}

	redacted := res.Redactor.RedactJSON(out.Output)
	if strings.Contains(string(redacted), value) {
		t.Fatalf("the value survived into what the model would read: %s", redacted)
	}
	if !strings.Contains(string(redacted), "secret://gh") {
		t.Fatalf("the mask does not name the handle: %s", redacted)
	}
	if strings.Contains(res.Redactor.Redact(out.Raw), value) {
		t.Fatal("the value survived into the raw output, which is what the ledger stores")
	}
}

// An error message is one of the likeliest ways a value comes back.
func TestSecrets_AFailingToolsErrorIsRedactedToo(t *testing.T) {
	const value = "FIXTURE-not-a-real-credential-8f21c7d4e9"
	res := resolutionFor(t, map[string]string{"gh": value})

	_, err := failingTool{}.Run(context.Background(), tools.Invocation{
		Input: json.RawMessage(`{"token":"secret://gh"}`), Secrets: res.Values,
	})
	if err == nil {
		t.Fatal("the tool did not fail")
	}
	if !strings.Contains(err.Error(), value) {
		t.Fatal("the error did not quote the value, so this test cannot show that redaction removes it")
	}
	if got := res.Redactor.Redact(err.Error()); strings.Contains(got, value) {
		t.Fatalf("the value survived into the error the model reads: %s", got)
	}
}

// A tool that never calls Reveal never handles a value, whatever the model wrote
// in its arguments. Input keeps the handles on purpose: several tools log their
// own arguments, and substituting into Input would put the value straight back
// into the record.
func TestSecrets_InputKeepsTheHandleAndOnlyRevealSubstitutes(t *testing.T) {
	const value = "a-real-looking-value"
	inv := tools.Invocation{
		Input:   json.RawMessage(`{"token":"secret://gh"}`),
		Secrets: map[string]string{"gh": value},
	}
	if strings.Contains(string(inv.Input), value) {
		t.Fatal("the value was substituted into Input; a tool logging its arguments would record it")
	}
	if !strings.Contains(string(inv.Input), "secret://gh") {
		t.Fatal("the handle is not in Input, so a tool that logs its arguments records nothing useful")
	}
	if got := inv.Reveal(string(inv.Input)); !strings.Contains(got, value) {
		t.Fatalf("Reveal did not substitute: %s", got)
	}
}

// Defence in depth: a value that survives redaction means an encoding was missed,
// and the result must be discarded rather than handed over.
func TestSecrets_ASurvivingValueIsCaughtByTheLeakCheck(t *testing.T) {
	const value = "a-real-looking-value"
	values := map[string]string{"gh": value}
	r := secrets.NewRedactor(values)

	// Stand in for an encoding the redactor does not cover, by checking the
	// literal that it does: if this ever stops being caught, the check below is
	// the only thing between a missed encoding and the context window.
	if leaks := r.Leaks("out: "+value, values); len(leaks) != 1 || leaks[0] != "gh" {
		t.Fatalf("a surviving value was not detected: %v", leaks)
	}
	if leaks := r.Leaks(r.Redact("out: "+value), values); len(leaks) != 0 {
		t.Fatalf("the leak check fires on properly redacted text: %v", leaks)
	}
}

// The model is told what handles exist, by name and purpose. A note that leaked
// a value would be the shortest possible path from the broker to the context.
func TestSecrets_TheNoteToTheModelCarriesNoValues(t *testing.T) {
	e := &Executor{}
	if note := e.secretsNote(context.Background(), &TaskContext{Goal: &engine.Goal{}}); note != "" {
		t.Fatalf("a deployment with no broker described handles anyway: %q", note)
	}

	available := []secrets.Available{{
		Handle: "secret://gh", Description: "for the GitHub API", Tools: []string{"http_get"},
	}}
	blob, err := json.Marshal(available)
	if err != nil {
		t.Fatal(err)
	}
	// Whatever the note renders, it renders from this — and this has nowhere to
	// put a value. Asserted at the type level because the failure mode is
	// somebody adding a field later for a good-sounding reason.
	for _, banned := range []string{"value", "env", "password", "token\":"} {
		if strings.Contains(string(blob), banned) {
			t.Fatalf("the description handed to the model contains %q: %s", banned, blob)
		}
	}
}

// An unresolvable handle is refused rather than passed through. A request that
// goes out with `Bearer secret://gh` fails for a reason that has nothing to do
// with credentials, and the model debugs the wrong thing for the rest of the run.
func TestSecrets_AnUnresolvableHandleIsRefusedNotPassedThrough(t *testing.T) {
	e := &Executor{} // no broker configured
	tc := &TaskContext{Goal: &engine.Goal{ID: "gol_1", ProjectID: "prj_1"}}

	_, err := e.resolveSecrets(context.Background(), tc, "http_get", `{"token":"secret://gh"}`)
	if err == nil {
		t.Fatal("a handle was accepted with no broker to resolve it")
	}
	if !errs.Is(err, errs.CodeSecretUnavailable) {
		t.Fatalf("got %s", errs.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "secret://gh") {
		t.Fatalf("the refusal does not name the handle: %v", err)
	}

	// And a call with no handles resolves cleanly even with no broker, so a
	// deployment that declares no secrets is not broken by this being here.
	res, err := e.resolveSecrets(context.Background(), tc, "http_get", `{"path":"README.md"}`)
	if err != nil {
		t.Fatalf("an ordinary call was refused in a deployment with no broker: %v", err)
	}
	if len(res.Values) != 0 {
		t.Fatal("values were resolved for a call that referenced none")
	}
}
