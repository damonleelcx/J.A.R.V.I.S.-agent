package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// TestEveryEventIsRegistered parses this package's source rather than trusting
// allEvents to be complete. Enumerating what you check makes the check vacuous:
// forgetting to add an event to allEvents would simply shorten the loop.
func TestEveryEventIsRegistered(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "event.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing event.go: %v", err)
	}
	declared := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Values) == 0 {
			return true
		}
		id, ok := vs.Type.(*ast.Ident)
		if !ok || id.Name != "Event" {
			return true
		}
		for i, name := range vs.Names {
			if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
				declared[name.Name] = strings.Trim(lit.Value, `"`)
			}
		}
		return true
	})
	if len(declared) < 20 {
		t.Fatalf("parsed only %d events; the parser is broken and this fence is vacuous", len(declared))
	}

	registered := map[Event]bool{}
	for _, e := range AllEvents() {
		registered[e] = true
	}
	for name, value := range declared {
		if !registered[Event(value)] {
			t.Errorf("event %s (%q) is declared but missing from allEvents", name, value)
		}
	}
}

// TestEventNamingConvention enforces <service>.<area>.<state>, lowercase.
// Dashboards and alert rules bind to these strings; drift breaks them silently.
func TestEventNamingConvention(t *testing.T) {
	for _, e := range AllEvents() {
		s := string(e)
		if s != strings.ToLower(s) {
			t.Errorf("event %q must be lowercase", s)
		}
		parts := strings.Split(s, ".")
		if len(parts) != 3 {
			t.Errorf("event %q must have exactly 3 dot-separated segments (<service>.<area>.<state>), got %d", s, len(parts))
		}
		if parts[0] != "forge" {
			t.Errorf("event %q must begin with the service segment 'forge'", s)
		}
		for _, p := range parts {
			if p == "" {
				t.Errorf("event %q has an empty segment", s)
			}
		}
	}
}

func TestNoDuplicateEvents(t *testing.T) {
	seen := map[Event]bool{}
	for _, e := range AllEvents() {
		if seen[e] {
			t.Errorf("event %q is registered twice", e)
		}
		seen[e] = true
	}
}

// TestErrorRecordCarriesRemedy is the practical payoff of the error registry:
// an ERROR line must be actionable on its own, without the reader opening the
// source to find out what to do.
func TestErrorRecordCarriesRemedy(t *testing.T) {
	var buf bytes.Buffer
	log := New(Options{Output: &buf, Format: "json", Level: slog.LevelDebug, Service: "test"})

	ctx := WithRequestID(context.Background(), "req_abc")
	ctx = WithTrace(ctx, "trc_1", "spn_1")
	err := errs.Wrap("db.Connect", errs.CodeDatabaseUnavail, errors.New("dial tcp: refused"))
	log.ErrorWith(ctx, EventDBConnectFailed, err)

	var rec map[string]any
	if e := json.Unmarshal(buf.Bytes(), &rec); e != nil {
		t.Fatalf("record is not valid JSON: %v\n%s", e, buf.String())
	}
	if rec["msg"] != string(EventDBConnectFailed) {
		t.Errorf("msg = %v, want the event name", rec["msg"])
	}
	for _, field := range []string{FieldRequestID, FieldTraceID, FieldSpanID, FieldErrorCode, FieldCategory, FieldRemedy} {
		if _, ok := rec[field]; !ok {
			t.Errorf("record is missing required field %q; a warning nobody can correlate or act on is a warning nobody can use", field)
		}
	}
	if rec[FieldErrorCode] != string(errs.CodeDatabaseUnavail) {
		t.Errorf("error_code = %v, want %v", rec[FieldErrorCode], errs.CodeDatabaseUnavail)
	}
	if rec[FieldRetryable] != true {
		t.Errorf("retryable = %v, want true", rec[FieldRetryable])
	}
}

func TestCorrelationIsOptional(t *testing.T) {
	// A log call outside any request must still emit, not panic or drop.
	var buf bytes.Buffer
	log := New(Options{Output: &buf, Format: "json", Level: slog.LevelDebug})
	log.Info(context.Background(), EventServerReady)
	if buf.Len() == 0 {
		t.Fatal("no record emitted without correlation ids")
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, present := rec[FieldRequestID]; present {
		t.Error("request_id should be omitted rather than emitted empty")
	}
}

func TestDiscardEmitsNothing(t *testing.T) {
	log := Discard()
	log.Error(context.Background(), EventHTTPPanic, "k", "v")
	// No assertion beyond "does not panic": Discard exists so tests that assert
	// behaviour are not drowned in output.
}
