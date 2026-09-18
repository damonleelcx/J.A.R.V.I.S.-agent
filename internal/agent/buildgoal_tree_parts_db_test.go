package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// ‼️ A build goal's step says how many parts the model it kept PLACES. The 2026-09-17
// live wheel goal's steps each read "0 part(s), kept as version …", because a tree has
// no top-level parts and the count was len(doc.Parts).
// docs/spikes/2026-09-17-live-verification
func TestBuildGoal_AStepOfATreeSaysThePartsItPlaces(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	stub := &goalStub{tokens: 100, replies: []string{
		`{"steps":[{"name":"suspension","what":"the suspension","assembly":"suspension"},` +
			`{"name":"brakes","what":"two brake discs","assembly":"brakes"}]}`,
		`{"speech":"suspension","prototype":` + mustJSONInner(t, carOnSuspension()) + `}`,
		brakesStep(`{"id":"left-disc","ref":"disc","position":[-800,0,1350]},`+
			`{"id":"right-disc","ref":"disc","position":[800,0,1350]}`, ``),
	}}
	h.plan(t, goal, stub)
	if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalSucceeded) {
		t.Fatalf("the build ended %s", status)
	}
	v := h.variants(t)
	if len(v) == 0 || len(v[0].Document.Parts) != 0 || v[0].Document.Occurrences() <= 2 {
		t.Fatalf("the fence needs a kept tree that places more than the suspension's two arms; kept %d version(s)", len(v))
	}
	want := v[0].Document.Occurrences()
	tasks := h.tasks(t, goal.ID)
	var last struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(tasks[len(tasks)-1].Result, &last); err != nil {
		t.Fatalf("the last step's result is not readable: %v (%s)", err, tasks[len(tasks)-1].Result)
	}
	if !strings.Contains(last.Summary, fmt.Sprintf(": %d part(s), kept as version", want)) {
		t.Errorf("the last step says %q; the model it kept places %d parts", last.Summary, want)
	}
}
