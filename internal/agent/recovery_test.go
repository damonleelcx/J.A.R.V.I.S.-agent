package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// A truthful recovery plan after partial failure (PRD AGT-05).
//
// Against a real database because the whole claim is "this is derived from the
// record". A test with a fake store would be asserting my beliefs about what the
// record contains.

type recoveryFixture struct {
	pool   *db.Pool
	goalID string
	planID string
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	pool := characterHarness(t) // same schema-per-test harness; skips without a database
	ctx := context.Background()

	owner := id.New(id.PrefixUser)
	if _, err := pool.Exec(ctx, `
		insert into forge_users (id, email, display_name, status, password_hash, password_algo,
			password_changed_at, created_at, updated_at)
		values ($1,$2,'Owner','active','x','argon2id',now(),now(),now())`,
		owner, owner+"@example.com"); err != nil {
		t.Fatal(err)
	}
	project := id.New(id.PrefixProject)
	if _, err := pool.Exec(ctx, `
		insert into forge_projects (id, owner_id, name, created_at, updated_at)
		values ($1,$2,'P',now(),now())`, project, owner); err != nil {
		t.Fatal(err)
	}
	goalID := id.New(id.PrefixGoal)
	if _, err := pool.Exec(ctx, `
		insert into forge_goals (id, project_id, created_by, title, statement, status, autonomy,
			risk_tier, created_at, updated_at, ended_at)
		values ($1,$2,$3,'Ship the bracket','ship it','failed','approval_gated','r3',
			now(),now(),now())`,
		goalID, project, owner); err != nil {
		t.Fatal(err)
	}
	// A task belongs to a plan in the real schema, so the fixture builds one
	// rather than working around the foreign key. A fixture that diverges from
	// the schema tests a database this product does not have.
	planID := id.New(id.PrefixPlan)
	if _, err := pool.Exec(ctx, `
		insert into forge_plans (id, goal_id, version, created_at)
		values ($1,$2,1,now())`, planID, goalID); err != nil {
		t.Fatal(err)
	}
	return &recoveryFixture{pool: pool, goalID: goalID, planID: planID}
}

// goalLike adds another goal to the same schema, for tests that need two.
//
// Building a second fixture would not work: the harness drops and recreates a
// schema of a fixed name, so the second one deletes the first one's rows.
func (f *recoveryFixture) goalLike(t *testing.T, title string) string {
	t.Helper()
	goalID := id.New(id.PrefixGoal)
	var project, owner string
	if err := f.pool.QueryRow(context.Background(),
		`select project_id, created_by from forge_goals where id = $1`, f.goalID).
		Scan(&project, &owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `
		insert into forge_goals (id, project_id, created_by, title, statement, status, autonomy,
			risk_tier, created_at, updated_at)
		values ($1,$2,$3,$4,'x','draft','approval_gated','r3',now(),now())`,
		goalID, project, owner, title); err != nil {
		t.Fatal(err)
	}
	return goalID
}

func (f *recoveryFixture) task(t *testing.T, title string, status engine.TaskStatus) string {
	t.Helper()
	taskID := id.New(id.PrefixTask)
	// The schema requires a terminal task to carry an end time and a failed one
	// to carry an error code. Satisfied rather than worked around: a fixture that
	// side-steps a constraint is testing a database this product does not have.
	terminal := status == engine.StatusSucceeded || status == engine.StatusFailed ||
		status == engine.StatusCancelled || status == engine.StatusSkipped
	var endedAt *time.Time
	if terminal {
		now := time.Now().UTC()
		endedAt = &now
	}
	var errCode *string
	if status == engine.StatusFailed {
		code := "TOOL_FAILED"
		errCode = &code
	}
	if _, err := f.pool.Exec(context.Background(), `
		insert into forge_tasks (id, goal_id, plan_id, title, instruction, status, risk_tier,
			idempotency_key, ended_at, error_code, created_at, updated_at)
		values ($1,$2,$3,$4,'do it',$5,'r1',$6,$7,$8,now(),now())`,
		taskID, f.goalID, f.planID, title, string(status), taskID, endedAt, errCode); err != nil {
		t.Fatal(err)
	}
	return taskID
}

func (f *recoveryFixture) call(t *testing.T, taskID, tool string, status engine.ToolCallStatus,
	undo tools.Reversibility, errDetail string) {
	t.Helper()
	// A call that did not succeed carries a code, as the schema requires.
	var errCode *string
	if status != engine.ToolSucceeded {
		code := "TOOL_FAILED"
		errCode = &code
	}
	if _, err := f.pool.Exec(context.Background(), `
		insert into forge_tool_calls (id, task_id, idempotency_key, tool_name, input, status,
			error_code, error_detail, risk_tier, reversibility, started_at, ended_at, created_at)
		values ($1,$2,$3,$4,'{}'::jsonb,$5,$6,nullif($7,''),'r2',nullif($8,''),now(),now(),now())`,
		id.New(id.PrefixToolCall), taskID, id.New(id.PrefixToolCall), tool, string(status),
		errCode, errDetail, string(undo)); err != nil {
		t.Fatal(err)
	}
}

// The rule everything else rests on: a call that FAILED still ran.
//
// Filing a failed call under "did not happen" is the most dangerous thing this
// type could do, because it is the answer somebody acts on — they re-run the
// task and the half-applied effect happens twice.
func TestAFailedCallIsReportedAsUncertainNotAsNotHavingHappened(t *testing.T) {
	f := newRecoveryFixture(t)
	task := f.task(t, "publish the drawing", engine.StatusFailed)
	f.call(t, task, "connector.publish", engine.ToolFailed, tools.Irreversible,
		"upstream returned 500 after the upload began")

	plan, err := Recover(context.Background(), f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Uncertain) != 1 {
		t.Fatalf("uncertain effects = %d, want 1 (%+v)", len(plan.Uncertain), plan.Uncertain)
	}
	if len(plan.Standing) != 0 {
		t.Errorf("a failed call was reported as a standing effect: %+v", plan.Standing)
	}

	out := plan.Render()
	// The wording is the feature. "Failed" on its own reads as "did not happen".
	for _, want := range []string{
		"ran and then failed",
		"does NOT say the effect did not happen",
		"check before re-running",
		"connector.publish",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
}

// Effects are grouped by what it would actually take to reverse them, and the
// irreversible ones are stated as such rather than listed alongside the rest.
func TestStandingEffectsAreSeparatedByWhatItTakesToUndoThem(t *testing.T) {
	f := newRecoveryFixture(t)
	task := f.task(t, "prepare the release", engine.StatusSucceeded)
	f.call(t, task, "workspace.write", engine.ToolSucceeded, tools.ReversibleAutomatic, "")
	f.call(t, task, "connector.deploy", engine.ToolSucceeded, tools.Irreversible, "")
	f.call(t, task, "connector.ticket", engine.ToolSucceeded, tools.ReversibleManual, "")
	// A read changes nothing, so it is not something to recover from.
	f.call(t, task, "workspace.read", engine.ToolSucceeded, tools.ReversibleNone, "")

	plan, err := Recover(context.Background(), f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Standing) != 3 {
		t.Fatalf("standing effects = %d, want 3 — a call that changed nothing should not "+
			"appear (%+v)", len(plan.Standing), plan.Standing)
	}
	irr, manual, auto := plan.partition()
	if len(irr) != 1 || len(manual) != 1 || len(auto) != 1 {
		t.Fatalf("partition = %d irreversible, %d manual, %d automatic; want 1/1/1",
			len(irr), len(manual), len(auto))
	}
	out := plan.Render()
	if !strings.Contains(out, "cannot be undone") {
		t.Error("the plan does not say that an irreversible effect cannot be undone")
	}
	// FORGE does not perform the rollback, and says so rather than implying it will.
	if !strings.Contains(out, "does not perform this rollback") {
		t.Error("the plan lists undoable effects without saying who undoes them")
	}
}

// A call recorded before reversibility was captured is described as unknown, and
// grouped with the ones a person must look at.
//
// The tempting default is to treat an unrecorded value as harmless. That is the
// direction this type must never round in: it would tell somebody a machine can
// clean up something nobody knows the shape of.
func TestAnUnrecordedReversibilityIsNotTreatedAsReversible(t *testing.T) {
	f := newRecoveryFixture(t)
	task := f.task(t, "old work", engine.StatusSucceeded)
	f.call(t, task, "legacy.tool", engine.ToolSucceeded, "", "")

	plan, err := Recover(context.Background(), f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Standing) != 1 {
		t.Fatalf("standing = %d, want 1", len(plan.Standing))
	}
	if plan.Standing[0].Undoable() {
		t.Error("a call with no recorded reversibility was reported as automatically undoable")
	}
	_, manual, auto := plan.partition()
	if len(auto) != 0 || len(manual) != 1 {
		t.Errorf("an unrecorded reversibility was grouped as automatic (%d auto, %d manual)",
			len(auto), len(manual))
	}
	if !strings.Contains(plan.Render(), "reversibility was not recorded") {
		t.Error("the plan does not say the reversibility is unknown")
	}
}

// Refused calls never ran, so there is nothing to recover from and nothing
// uncertain about them.
func TestARefusedCallIsNotAnEffect(t *testing.T) {
	f := newRecoveryFixture(t)
	task := f.task(t, "blocked work", engine.StatusFailed)
	f.call(t, task, "connector.deploy", engine.ToolRefused, tools.Irreversible, "above the ceiling")

	plan, err := Recover(context.Background(), f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Standing)+len(plan.Uncertain) != 0 {
		t.Errorf("a refused call appeared in the plan: standing=%+v uncertain=%+v",
			plan.Standing, plan.Uncertain)
	}
	if !strings.Contains(plan.Render(), "nothing to undo and nothing uncertain") {
		t.Error("a goal that changed nothing does not say so")
	}
}

// Tasks are split by whether they finished, and the unfinished ones carry the
// status that explains why.
func TestTasksAreSplitByWhetherTheyActuallyFinished(t *testing.T) {
	f := newRecoveryFixture(t)
	f.task(t, "draw the bracket", engine.StatusSucceeded)
	f.task(t, "publish it", engine.StatusFailed)
	f.task(t, "announce it", engine.StatusPending)

	plan, err := Recover(context.Background(), f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Completed) != 1 || len(plan.Unfinished) != 2 {
		t.Fatalf("completed=%v unfinished=%v", plan.Completed, plan.Unfinished)
	}
	joined := strings.Join(plan.Unfinished, " | ")
	for _, want := range []string{"failed", "pending"} {
		if !strings.Contains(joined, want) {
			t.Errorf("an unfinished task does not carry its status (%q): %s", want, joined)
		}
	}
	if plan.TakenAt.IsZero() || time.Since(plan.TakenAt) > time.Minute {
		t.Error("the plan has no usable timestamp; a recovery plan without one is a " +
			"photograph somebody will treat as live")
	}
}
