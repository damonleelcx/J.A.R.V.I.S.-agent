package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The hash is a pure function of the event, which is the property that makes the
// chain checkable without a database — point the verifier at a dump and it still
// works. These tests hold that shape.

func ev(seq int64, kind, summary string) *Event {
	return &Event{
		ID: "evt_1", GoalID: "gol_1", Seq: seq, Kind: kind, Actor: ActorHuman,
		Summary: summary, Payload: json.RawMessage(`{"a":1}`),
		CreatedAt: time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
	}
}

func TestEventHash_IsDeterministic(t *testing.T) {
	a := EventHash("prev", ev(1, "task.started", "x"), "dig")
	b := EventHash("prev", ev(1, "task.started", "x"), "dig")
	if a != b {
		t.Fatalf("same event hashed differently: %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("hash is %d chars, want 64 (sha256 hex)", len(a))
	}
}

// Every field must be covered. A field outside the hash is a field an editor can
// change without breaking anything — which is the whole failure this prevents.
func TestEventHash_CoversEveryField(t *testing.T) {
	base := ev(1, "task.started", "the original summary")
	baseHash := EventHash("prev", base, "dig")

	actorA, actorB := "usr_a", "usr_b"
	taskA, taskB := "tsk_a", "tsk_b"
	base.ActorID, base.TaskID = &actorA, &taskA
	baseHash = EventHash("prev", base, "dig")

	for name, mutate := range map[string]func(*Event){
		"id":         func(e *Event) { e.ID = "evt_2" },
		"goal id":    func(e *Event) { e.GoalID = "gol_2" },
		"task id":    func(e *Event) { e.TaskID = &taskB },
		"seq":        func(e *Event) { e.Seq = 2 },
		"kind":       func(e *Event) { e.Kind = "task.failed" },
		"actor":      func(e *Event) { e.Actor = ActorExecutor },
		"actor id":   func(e *Event) { e.ActorID = &actorB },
		"summary":    func(e *Event) { e.Summary = "an edited summary" },
		"created at": func(e *Event) { e.CreatedAt = e.CreatedAt.Add(time.Second) },
	} {
		altered := *base
		mutate(&altered)
		if EventHash("prev", &altered, "dig") == baseHash {
			t.Errorf("changing %s did not change the hash — that field is outside the chain", name)
		}
	}
	if EventHash("prev", base, "other-digest") == baseHash {
		t.Error("changing the payload digest did not change the hash")
	}
	if EventHash("other-prev", base, "dig") == baseHash {
		t.Error("changing the previous hash did not change the hash — there is no chain")
	}
}

// Concatenating fields without a separator lets content move across a boundary
// without changing the digest. ("ab","c") must not hash like ("a","bc").
func TestEventHash_FieldBoundariesAreNotAmbiguous(t *testing.T) {
	a := ev(1, "kind", "ab")
	b := ev(1, "kind", "a")
	b.ID = a.ID + "" // keep ids equal
	a.Kind, b.Kind = "x", "x"
	a.Summary, b.Summary = "ab", "a"
	// Move one character from summary into the following field's territory by
	// making the digest start with it.
	if EventHash("p", a, "dig") == EventHash("p", b, "bdig") {
		t.Fatal("field boundaries are ambiguous: content moved between fields without changing the hash")
	}
}

// A payload is hashed through a canonical form, because jsonb decides key order
// and number formatting for us. Two spellings of the same object must agree.
func TestPayloadDigest_CanonicalisesKeyOrder(t *testing.T) {
	a, err := PayloadDigest(json.RawMessage(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := PayloadDigest(json.RawMessage(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("key order changed the digest: %s vs %s — verification would fail on a jsonb round-trip", a, b)
	}
}

// "no payload" and "{}" are the same fact and must not be interchangeable with
// anything else.
func TestPayloadDigest_EmptyFormsAgree(t *testing.T) {
	want, _ := PayloadDigest(json.RawMessage(`{}`))
	for _, raw := range []string{``, `null`, `  `, `{}`} {
		got, err := PayloadDigest(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if got != want {
			t.Errorf("%q digested as %s, want the canonical empty object %s", raw, got, want)
		}
	}
	other, _ := PayloadDigest(json.RawMessage(`{"a":1}`))
	if other == want {
		t.Fatal("a non-empty payload digests the same as an empty one")
	}
}

// A payload that is not JSON must be refused rather than digested as its own
// text: an audit chain that accepts anything attests to nothing.
func TestPayloadDigest_RefusesInvalidJSON(t *testing.T) {
	if _, err := PayloadDigest(json.RawMessage(`{not json`)); err == nil {
		t.Fatal("invalid JSON was accepted into the audit chain")
	}
}

func TestChainReport_SummaryNamesTheFirstBreak(t *testing.T) {
	r := ChainReport{Events: 5, Chained: 5, Findings: []ChainFinding{
		{Seq: 3, Problem: "broken-link"}, {Seq: 4, Problem: "broken-link"},
	}}
	if r.Intact() {
		t.Fatal("a report with findings claimed to be intact")
	}
	if !strings.Contains(r.Summary(), "seq 3") {
		t.Fatalf("summary does not point at the first break: %q", r.Summary())
	}
}
