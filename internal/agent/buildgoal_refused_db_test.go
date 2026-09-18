package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// ‼️ A build step a gate refused, which kept nothing of its own, is said on the step, on
// the goal and on its timeline. Live run 3 of 2026-09-17: step 3 tried to put a new root
// over the old one, kept nothing, and the goal read "All 3 task(s) finished: 3
// succeeded, 0 skipped" (docs/spikes/2026-09-17-live-verification).
//
// The goal still SUCCEEDS when an earlier step kept a model: that model is valid, and
// failing the goal would call it lost. A build in which every step was refused kept no
// model at all, and fails.
func TestBuildGoal_ARefusedStepIsSaidOnTheStepAndTheGoal(t *testing.T) {
	const twoSteps = `{"steps":[{"name":"chassis","what":"the chassis"},{"name":"body","what":"the body"}]}`
	const nothing = `{"speech":"I would rather not change anything."}`

	t.Run("one step refused", func(t *testing.T) {
		h := newBuildHarness(t)
		goal := h.goal(t, nil)
		stub := &goalStub{tokens: 10, replies: []string{twoSteps, wholeDoc("chassis", "Chassis"), nothing, nothing}}
		h.plan(t, goal, stub)
		if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalSucceeded) {
			t.Fatalf("a build that kept step 1's valid model ended %s", status)
		}
		tasks := h.tasks(t, goal.ID)
		var last struct {
			Summary string          `json:"summary"`
			Result  buildStepResult `json:"result"`
		}
		if err := json.Unmarshal(tasks[1].Result, &last); err != nil {
			t.Fatal(err)
		}
		if !last.Result.Refused || !strings.HasPrefix(last.Summary, "Step 2 of 2 (body): refused, kept nothing; the model stays at version ") ||
			strings.Contains(last.Summary, "kept as version") {
			t.Errorf("the refused step reads %q (refused=%v); it must say it kept nothing, and never \"kept as version\"",
				last.Summary, last.Result.Refused)
		}
		var outcome string
		if err := h.pool.QueryRow(context.Background(), `select outcome_summary from forge_goals where id = $1`, goal.ID).Scan(&outcome); err != nil {
			t.Fatal(err)
		}
		want := "All 2 task(s) finished: 2 succeeded, 0 skipped. 1 build step(s) were refused, kept nothing: Step 2 of 2 — body."
		if outcome != want {
			t.Errorf("the goal's outcome reads %q;\nwant %q", outcome, want)
		}
		var ended string
		if err := h.pool.QueryRow(context.Background(), `select summary from forge_events where goal_id = $1 and kind = $2`,
			goal.ID, engine.EventGoalEnded).Scan(&ended); err != nil {
			t.Fatal(err)
		}
		if ended != want {
			t.Errorf("the timeline's goal.ended reads %q; want %q", ended, want)
		}
		var said int
		if err := h.pool.QueryRow(context.Background(), `select count(*) from forge_events where goal_id = $1 and kind = $2
			and summary like 'Step 2 of 2 (body): refused, kept nothing%'`, goal.ID, engine.EventTaskSucceeded).Scan(&said); err != nil {
			t.Fatal(err)
		}
		if said != 1 {
			t.Errorf("%d timeline entries say step 2 was refused; the card reads it there", said)
		}
	})

	t.Run("every step refused", func(t *testing.T) {
		h := newBuildHarness(t)
		goal := h.goal(t, nil)
		stub := &goalStub{tokens: 10, replies: []string{twoSteps, nothing, nothing, nothing, nothing}}
		h.plan(t, goal, stub)
		if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalFailed) {
			t.Fatalf("a build that kept no model ended %s", status)
		}
		var outcome, code string
		if err := h.pool.QueryRow(context.Background(), `select outcome_summary, failure_code from forge_goals where id = $1`,
			goal.ID).Scan(&outcome, &code); err != nil {
			t.Fatal(err)
		}
		if code != "BUILD_KEPT_NOTHING" || !strings.Contains(outcome, "All 2 build step(s) were refused, kept nothing, so the build kept no model") {
			t.Errorf("a build that kept nothing ended %s: %q", code, outcome)
		}
	})
}
