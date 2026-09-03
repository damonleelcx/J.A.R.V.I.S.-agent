package drill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Run executes scenarios, each in a schema of its own.
//
// # Why a schema per scenario rather than one shared one
//
// A drill injects faults. Two of them sharing a schema would inject faults into
// each other's fixtures, and the resulting failure would be blamed on whichever
// ran second. Isolation also means a scenario can leave its wreckage behind for
// inspection without breaking the next one.
//
// The schemas are dropped afterwards unless keep is set, because a failed drill
// is exactly when somebody wants to go and look.
func Run(ctx context.Context, url string, names []string, keep bool, log *logx.Logger) (*Report, error) {
	const op = "drill.Run"

	selected := Scenarios()
	if len(names) > 0 {
		selected = nil
		for _, n := range names {
			s, err := Lookup(n)
			if err != nil {
				return nil, err
			}
			selected = append(selected, s)
		}
	}
	if len(selected) == 0 {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("no drills are registered; an empty run would report success having proved nothing")
	}

	report := &Report{}
	for _, s := range selected {
		start := time.Now()
		result := runOne(ctx, url, s, keep, log)
		result.Duration = time.Since(start)
		report.Results = append(report.Results, *result)
	}
	return report, nil
}

func runOne(ctx context.Context, url string, s Scenario, keep bool, log *logx.Logger) *Result {
	schema := "forge_drill_" + strings.NewReplacer("-", "_").Replace(s.Name)
	if len(schema) > 60 {
		schema = schema[:60]
	}
	h, cleanup, err := newHarness(ctx, url, schema, log)
	if err != nil {
		return &Result{Scenario: s.Name, Err: err}
	}
	if !keep {
		defer cleanup()
	} else {
		defer h.Pool.Close()
	}

	res, err := s.Run(ctx, h)
	if err != nil {
		return &Result{Scenario: s.Name, Err: err}
	}
	if res == nil {
		return &Result{Scenario: s.Name, Err: errs.New("drill.runOne", errs.CodeInvariantViolated).
			WithDetail("scenario %q returned no result and no error", s.Name)}
	}
	return res
}

func newHarness(ctx context.Context, url, schema string, log *logx.Logger) (*Harness, func(), error) {
	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 6, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	admin, err := db.Connect(ctx, cfg(url), logx.Discard())
	if err != nil {
		return nil, nil, err
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "drop schema if exists "+schema+" cascade"); err != nil {
		return nil, nil, errs.Wrap("drill.newHarness", errs.CodeDatabaseUnavail, err)
	}
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		return nil, nil, errs.Wrap("drill.newHarness", errs.CodeDatabaseUnavail, err)
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := db.Connect(ctx, cfg(url+sep+"search_path="+schema), logx.Discard())
	if err != nil {
		return nil, nil, err
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		pool.Close()
		return nil, nil, err
	}

	h := &Harness{Pool: pool, Now: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)}
	if err := h.seed(ctx); err != nil {
		pool.Close()
		return nil, nil, err
	}
	cleanup := func() {
		pool.Close()
		if c, err := db.Connect(context.Background(), cfg(url), logx.Discard()); err == nil {
			_, _ = c.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
			c.Close()
		}
	}
	return h, cleanup, nil
}

// seed builds the owner, project, goal and plan a scenario hangs work off.
func (h *Harness) seed(ctx context.Context) error {
	h.UserID = id.New(id.PrefixUser)
	if _, err := h.Pool.Exec(ctx, `
		insert into forge_users (id, email, display_name, status, password_hash, password_algo,
			password_changed_at, created_at, updated_at)
		values ($1,$2,'Drill','active','x','argon2id',$3,$3,$3)`,
		h.UserID, "drill-"+h.UserID+"@example.com", h.Now); err != nil {
		return errs.Wrap("drill.seed", errs.CodeDatabaseUnavail, err)
	}
	h.ProjectID = id.New(id.PrefixProject)
	if _, err := h.Pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, created_at, updated_at) values ($1,$2,'Drill',$3,$3)`,
		h.ProjectID, h.UserID, h.Now); err != nil {
		return errs.Wrap("drill.seed", errs.CodeDatabaseUnavail, err)
	}
	h.GoalID = id.New(id.PrefixGoal)
	if _, err := h.Pool.Exec(ctx, `
		insert into forge_goals (id, project_id, created_by, title, statement, status,
			started_at, created_at, updated_at)
		values ($1,$2,$3,'Drill goal','Do the thing','active',$4,$4,$4)`,
		h.GoalID, h.ProjectID, h.UserID, h.Now); err != nil {
		return errs.Wrap("drill.seed", errs.CodeDatabaseUnavail, err)
	}
	h.PlanID = id.New(id.PrefixPlan)
	if _, err := h.Pool.Exec(ctx,
		`insert into forge_plans (id, goal_id, version, created_at) values ($1,$2,1,$3)`,
		h.PlanID, h.GoalID, h.Now); err != nil {
		return errs.Wrap("drill.seed", errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// addTask creates a task through the real repository, so the fixture meets the
// same constraints production does.
func (h *Harness) addTask(ctx context.Context, title string, dependsOn []string) (*engine.Task, error) {
	return h.addTaskWithAttempts(ctx, title, dependsOn, 1)
}

// addTaskWithAttempts creates a task with a chosen retry budget.
//
// The budget matters to a drill in a way it does not to ordinary code: a task
// with one attempt whose worker dies is TERMINALLY failed, correctly, and a
// scenario about recovery that uses one attempt is asking whether the system
// recovers from something it is not supposed to recover from.
func (h *Harness) addTaskWithAttempts(ctx context.Context, title string, dependsOn []string, attempts int) (*engine.Task, error) {
	task := &engine.Task{
		ID: id.New(id.PrefixTask), GoalID: h.GoalID, PlanID: h.PlanID,
		Title: title, Instruction: "do " + title,
		Status: engine.StatusPending, IdempotencyKey: id.New(id.PrefixTask),
		MaxAttempts: attempts, NotBefore: h.Now, Priority: 100,
		RiskTier: engine.RiskR1, CreatedAt: h.Now, UpdatedAt: h.Now,
	}
	if err := engine.NewRepository().CreateTask(ctx, h.Pool, task, dependsOn); err != nil {
		return nil, err
	}
	return task, nil
}

// finish drives a task to a terminal state through the real transition rules,
// so a drill cannot reach a state the engine would refuse.
func (h *Harness) finish(ctx context.Context, repo *engine.Repository, queue *engine.Queue,
	taskID string, to engine.TaskStatus) error {
	task, err := repo.GetTask(ctx, h.Pool, taskID)
	if err != nil {
		return err
	}
	if task.Status == engine.StatusPending {
		if _, err := queue.PromoteReadyTasks(ctx, h.Pool, h.GoalID, h.Now); err != nil {
			return err
		}
		if task, err = repo.GetTask(ctx, h.Pool, taskID); err != nil {
			return err
		}
	}
	for _, step := range []engine.TaskStatus{engine.StatusClaimed, engine.StatusRunning, to} {
		if task.Status == step {
			continue
		}
		mut := engine.TaskMutation{}
		if step == engine.StatusFailed {
			mut.ErrorCode, mut.ErrorDetail = "DRILL_INJECTED", "the drill made this fail on purpose"
		}
		if step == engine.StatusSucceeded {
			mut.Result = json.RawMessage(`{"drill":"finished"}`)
		}
		if err := repo.TransitionTask(ctx, h.Pool, task, step, h.Now, mut); err != nil {
			return fmt.Errorf("moving %s to %s: %w", taskID, step, err)
		}
		if task, err = repo.GetTask(ctx, h.Pool, taskID); err != nil {
			return err
		}
	}
	return nil
}

// goalRow is the slice of a goal a drill reads back.
type goalRow struct {
	Status         engine.GoalStatus
	OutcomeSummary string
	FailureCode    string
}

func (h *Harness) goal(ctx context.Context) (*goalRow, error) {
	var g goalRow
	var status string
	err := h.Pool.QueryRow(ctx,
		`select status, coalesce(outcome_summary, ''), coalesce(failure_code, '')
		   from forge_goals where id = $1`, h.GoalID).
		Scan(&status, &g.OutcomeSummary, &g.FailureCode)
	if err != nil {
		return nil, errs.Wrap("drill.goal", errs.CodeDatabaseUnavail, err)
	}
	g.Status = engine.GoalStatus(status)
	return &g, nil
}

// settle applies the same outcome rule the worker's settlement uses.
//
// Deliberately NOT a copy of that rule: it calls agent.GoalCompletion's logic
// indirectly by writing what the worker writes. If the worker's rule changes and
// this does not, the drill's own check ("completion is not implied") is what
// catches the divergence — which is the right way round, since the drill exists
// to test the rule rather than to restate it.
func (h *Harness) settle(ctx context.Context, depth map[engine.TaskStatus]int) error {
	total, succeeded, failed, skipped := 0, 0, 0, 0
	for status, n := range depth {
		total += n
		switch status {
		case engine.StatusSucceeded:
			succeeded += n
		case engine.StatusFailed, engine.StatusCancelled:
			failed += n
		case engine.StatusSkipped:
			skipped += n
		}
	}
	final, code := engine.GoalSucceeded, ""
	summary := fmt.Sprintf("All %d task(s) finished: %d succeeded, %d skipped.", total, succeeded, skipped)
	if failed > 0 {
		final, code = engine.GoalFailed, "TASKS_FAILED"
		summary = fmt.Sprintf("%d of %d task(s) failed or were cancelled; %d succeeded, %d were skipped as unreachable.",
			failed, total, succeeded, skipped)
	}
	_, err := h.Pool.Exec(ctx, `
		update forge_goals set status = $2, ended_at = $3, outcome_summary = $4, failure_code = nullif($5,'')
		 where id = $1 and status = 'active'`, h.GoalID, string(final), h.Now, summary, code)
	if err != nil {
		return errs.Wrap("drill.settle", errs.CodeDatabaseUnavail, err)
	}
	return nil
}
