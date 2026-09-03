package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// goalCommands drive the engine from a terminal.
//
// These exist before the web console because a durable agent has to be operable
// without one. When the console is down, or the goal is stuck, or somebody is
// debugging at 3am, the question is always "what is it doing and why" — and that
// has to be answerable from a shell.

// cmdGoalNew plans a goal and writes it, without starting it.
//
// Planning and activation are separate steps on purpose: the plan is the thing
// worth reading before any work happens, and a command that plans-and-runs gives
// nobody the chance.
func cmdGoalNew(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdGoalNew"

	fs := newFlagSet("goal new")
	title := fs.String("title", "", "short title for the goal (required)")
	statement := fs.String("statement", "", "what you want, in your own words (required)")
	autonomy := fs.String("autonomy", "sandbox_execute", "discuss | draft | sandbox_execute | approval_gated")
	risk := fs.String("risk", "r1", "r0 | r1 | r2 | r3 | r4")
	email := fs.String("owner", "", "email of an existing account to own this goal (required)")
	project := fs.String("project", "", "existing project id, or empty to create one")
	start := fs.Bool("start", false, "activate the goal immediately after planning")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *title == "" || *statement == "" || *email == "" {
		fs.Usage()
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("--title, --statement and --owner are required")
	}

	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	var ownerID string
	if err := pool.QueryRow(ctx,
		`select id from forge_users where lower(email) = lower($1)`, *email).Scan(&ownerID); err != nil {
		return errs.Wrap(op, errs.CodeNotFound, err).
			WithDetail("no account with email %q. Create one first:\n"+
				"    curl -sX POST $FORGE_PUBLIC_URL/v1/auth/sign-up -H 'Content-Type: application/json' \\\n"+
				"      -d '{\"email\":\"%s\",\"password\":\"<at least 12 characters>\"}'", *email, *email)
	}

	// The same intake the workbench's "Start this" button uses. One
	// implementation of "draft, plan, activate" rather than two that drift —
	// see agent.Intake.
	client := llm.NewOpenAICompatible(cfg.LLM, log, clock.System{})
	intake := agent.NewIntake(client, persona.DefaultCharacter(), cfg.Engine, clock.System{}).
		WithCharacters(agent.NewCharacterStore(pool, log))

	goal, err := intake.Draft(ctx, pool, agent.DraftRequest{
		OwnerID:   ownerID,
		ProjectID: *project,
		Title:     *title,
		Statement: *statement,
		Autonomy:  engine.Autonomy(*autonomy),
		RiskTier:  engine.RiskTier(*risk),
	})
	if err != nil {
		return err
	}
	if *project == "" {
		fmt.Printf("created project %s\n", goal.ProjectID)
	}
	fmt.Printf("created goal %s\n\n", goal.ID)

	// Planning can take minutes on a large model, especially when the goal is
	// ambiguous enough that the planner works at it. Silence for that long reads
	// as a hang, and PRD NFR-02 asks for meaningful progress at least every 10s.
	// A ticker is the least this can be and still be honest: it reports elapsed
	// time, which is all that is actually known.
	fmt.Printf("planning with %s …\n", intake.PlannerModel())
	stopTicker := startElapsedTicker("  still planning")
	outcome, err := intake.Plan(ctx, pool, goal)
	stopTicker()
	if err != nil {
		return err
	}
	if outcome.ClarificationNeeded != "" {
		// A question is a legitimate outcome. A plan built on a wrong assumption
		// costs far more than an answered question, so this is not an error.
		fmt.Printf("\nFORGE needs an answer before it can plan this:\n\n  %s\n\n", outcome.ClarificationNeeded)
		fmt.Printf("Goal %s is saved as a draft. Re-run with a statement that answers this.\n", goal.ID)
		return nil
	}

	fmt.Printf("\n%s\n\n", outcome.Rationale)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TASK\tRISK\tGATE\tDEPENDS ON")
	for _, pt := range outcome.Result.Tasks {
		gate := "-"
		if engine.RiskTier(pt.RiskTier).RequiresApproval() {
			gate = "approval"
		}
		deps := strings.Join(pt.DependsOn, ", ")
		if deps == "" {
			deps = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", truncateCLI(pt.Title, 46), pt.RiskTier, gate, deps)
	}
	w.Flush()
	fmt.Printf("\nplan v%d · %d task(s) written\n", outcome.Plan.Version, len(outcome.Tasks))

	if *start {
		if err := intake.Start(ctx, pool, goal, nil); err != nil {
			return err
		}
		fmt.Printf("\ngoal is ACTIVE. Run `make work` (or ./bin/forge-worker) to execute it.\n")
	} else {
		fmt.Printf("\ngoal is a DRAFT. Review the plan above, then:\n  forgectl goal start %s\n", goal.ID)
	}
	fmt.Printf("Follow it with:\n  forgectl goal show %s\n", goal.ID)
	return nil
}

// cmdGoalReplan plans a draft goal whose plan never landed.
//
// # Why this command exists
//
// Planning is a model call of one to three minutes. When it trips, Draft has
// already committed the goal and Apply has written nothing, so what survives is
// a draft with no tasks — and until now there was no way forward from there.
// Starting it is refused (it would be a goal running with nothing to run), so
// the only recovery was to write the goal again under a new id.
func cmdGoalReplan(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdGoalReplan"

	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl goal replan <goal-id>")
	}
	goalID := args[0]

	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	goal, err := loadGoalCLI(ctx, pool, goalID)
	if err != nil {
		return err
	}
	// The same intake every other planning path uses, so a replan produces the
	// plan the first attempt would have.
	client := llm.NewOpenAICompatible(cfg.LLM, log, clock.System{})
	intake := agent.NewIntake(client, persona.DefaultCharacter(), cfg.Engine, clock.System{}).
		WithCharacters(agent.NewCharacterStore(pool, log))

	fmt.Printf("planning %q with %s …\n", goal.Title, intake.PlannerModel())
	stopTicker := startElapsedTicker("  still planning")
	outcome, err := intake.Replan(ctx, pool, goal)
	stopTicker()
	if err != nil {
		return err
	}
	if outcome.ClarificationNeeded != "" {
		// A question is the planner working correctly, and the goal stays a
		// draft. Printed as the outcome rather than an error, for the same
		// reason Plan returns it rather than raising it.
		fmt.Printf("\nThe planner needs an answer before it can plan this:\n  %s\n\n",
			wrap(outcome.ClarificationNeeded, 74, "  "))
		fmt.Println("The goal is still a draft. Answer the question in its statement and replan.")
		return nil
	}
	fmt.Printf("\nPlanned: %d task(s).\n", len(outcome.Tasks))
	for _, task := range outcome.Tasks {
		fmt.Printf("  %-8s %s\n", task.RiskTier, task.Title)
	}
	fmt.Printf("\nStart it with:\n  forgectl goal start %s\n", goal.ID)
	return nil
}

// cmdGoalStart activates a goal so its tasks become claimable.
func cmdGoalStart(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdGoalStart"
	if len(args) != 1 {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("usage: forgectl goal start <goal-id>")
	}
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	goal, err := loadGoalCLI(ctx, pool, args[0])
	if err != nil {
		return err
	}
	applier := agent.NewPlanApplier(engine.NewRepository(), engine.NewQueue(),
		engine.NewBudgetGuard(cfg.Engine), clock.System{})
	if err := applier.Activate(ctx, pool, goal, engine.ActorHuman, nil); err != nil {
		return err
	}
	fmt.Printf("goal %s is now active\n", goal.ID)
	return nil
}

// cmdGoalShow renders a goal's current state and its execution timeline.
//
// This is the "what happened, why, when, and what next" surface. It reads only
// persisted state, so it tells the truth about a goal whose workers are all dead.
func cmdGoalShow(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdGoalShow"
	if len(args) < 1 {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("usage: forgectl goal show <goal-id>")
	}
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	goal, err := loadGoalCLI(ctx, pool, args[0])
	if err != nil {
		return err
	}
	repo := engine.NewRepository()
	queue := engine.NewQueue()

	tasks, err := repo.ListTasks(ctx, pool, goal.ID)
	if err != nil {
		return err
	}
	depth, err := queue.Depth(ctx, pool, goal.ID)
	if err != nil {
		return err
	}

	var pendingApprovals int
	_ = pool.QueryRow(ctx,
		`select count(*) from forge_approvals where goal_id = $1 and decision = 'pending'`,
		goal.ID).Scan(&pendingApprovals)

	running := depth[engine.StatusRunning]+depth[engine.StatusClaimed] > 0
	state := persona.AvatarStateForGoal(string(goal.Status), pendingApprovals > 0, running, false)

	fmt.Printf("\n  %s  %s\n", state.Label(), goal.Title)
	fmt.Printf("  %s\n\n", goal.ID)
	fmt.Printf("  status     %s (%s, ceiling %s)\n", goal.Status, goal.Autonomy, goal.RiskTier)
	fmt.Printf("  spend      %d tokens · %d task(s) created\n", goal.Spend.Tokens, goal.Spend.TasksCreated)
	if goal.StartedAt != nil {
		fmt.Printf("  running    %s\n", time.Since(*goal.StartedAt).Round(time.Second))
	}
	if pendingApprovals > 0 {
		fmt.Printf("\n  %d action(s) are waiting for you:\n", pendingApprovals)
		rows, _ := pool.Query(ctx,
			`select id, task_id, risk_tier, summary from forge_approvals
			  where goal_id = $1 and decision = 'pending' order by requested_at`, goal.ID)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var aid, tid, tier, summary string
				if err := rows.Scan(&aid, &tid, &tier, &summary); err == nil {
					fmt.Printf("    %s  [%s]  %s\n", aid, tier, truncateCLI(firstLine(summary), 60))
					fmt.Printf("      forgectl approve %s --as %s\n", aid, "<your-email>")
				}
			}
		}
	}

	fmt.Printf("\n  TASKS\n")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  \tSTATUS\tVERIFIED\tATT\tTITLE")
	for _, t := range tasks {
		verified := "-"
		if t.Verified() {
			verified = "yes"
		} else if t.Status == engine.StatusSucceeded {
			// Completion and verification are different facts; a low-tier task
			// that nobody checked must not read as one that was checked.
			verified = "not required"
		}
		fmt.Fprintf(w, "  \t%s\t%s\t%d\t%s\n", t.Status, verified, t.AttemptCount, truncateCLI(t.Title, 46))
		if t.ErrorCode != "" {
			fmt.Fprintf(w, "  \t\t\t\t  ↳ %s: %s\n", t.ErrorCode, truncateCLI(t.ErrorDetail, 60))
		}
	}
	w.Flush()

	timeline, err := repo.Timeline(ctx, pool, goal.ID, 25, 0)
	if err != nil {
		return err
	}
	fmt.Printf("\n  TIMELINE (newest first)\n")
	for _, e := range timeline {
		fmt.Printf("  %4d  %-9s  %-24s  %s\n",
			e.Seq, e.Actor, e.Kind, truncateCLI(firstLine(e.Summary), 72))
	}
	fmt.Println()
	return nil
}

// cmdApprove records a human decision on a gate.
//
// The decider is named and required. PRD SAF-05: "the AI approved it" is never
// acceptable authority, and an approval with no attributable human is exactly
// that with extra steps.
func cmdApprove(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string, approve bool) error {
	const op = "forgectl.cmdApprove"

	fs := newFlagSet("approve")
	as := fs.String("as", "", "email of the person making this decision (required)")
	reason := fs.String("reason", "", "why")
	if len(args) < 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl approve <approval-id> --as you@example.com")
	}
	approvalID := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *as == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("--as is required: an approval must name the person who made it")
	}

	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	var deciderID string
	if err := pool.QueryRow(ctx,
		`select id from forge_users where lower(email) = lower($1)`, *as).Scan(&deciderID); err != nil {
		return errs.Wrap(op, errs.CodeNotFound, err).
			WithDetail("no account with email %q; an approval must be attributable to a real account", *as)
	}

	decision := engine.ApprovalRejected
	if approve {
		decision = engine.ApprovalApproved
	}
	now := clock.System{}.Now()

	var taskID, goalID string
	err = pool.QueryRow(ctx, `
		update forge_approvals
		   set decision = $2, decided_by = $3, decided_at = $4, decision_reason = $5
		 where id = $1 and decision = 'pending'
		returning task_id, goal_id`,
		approvalID, string(decision), deciderID, now, *reason).Scan(&taskID, &goalID)
	if err != nil {
		return errs.Wrap(op, errs.CodeNotFound, err).
			WithDetail("approval %s does not exist or has already been decided", approvalID)
	}

	// An approved task returns to the QUEUE rather than continuing in place, so
	// a fresh worker resumes it under a new lease. The worker that opened the
	// gate is long gone.
	target := engine.StatusReady
	mut := engine.TaskMutation{}
	if !approve {
		target = engine.StatusFailed
		mut.ErrorCode = string(errs.CodeForbidden)
		mut.ErrorDetail = "a human rejected this action: " + *reason
	}
	repo := engine.NewRepository()
	task, err := repo.GetTask(ctx, pool, taskID)
	if err != nil {
		return err
	}
	if err := repo.TransitionTask(ctx, pool, task, target, now, mut); err != nil {
		return err
	}

	kind := engine.EventApprovalGranted
	if !approve {
		kind = engine.EventApprovalRejected
	}
	payload, _ := json.Marshal(map[string]any{"approval_id": approvalID, "reason": *reason})
	_ = repo.AppendEvent(ctx, pool, &engine.Event{
		GoalID: goalID, TaskID: &taskID, Kind: kind,
		// Attributed to the HUMAN, with their account id. This is the record an
		// auditor reads to answer "who authorised this".
		Actor: engine.ActorHuman, ActorID: &deciderID,
		Summary: fmt.Sprintf("%s by %s. %s", decision, *as, *reason),
		Payload: payload,
	}, now)

	fmt.Printf("%s recorded as %s by %s\n", approvalID, decision, *as)
	if approve {
		fmt.Println("the task has been returned to the queue")
	}
	return nil
}

// startElapsedTicker prints elapsed time until the returned function is called.
//
// It reports only what is known — how long this has been running. A fake
// progress bar would be worse than silence: it would imply the command can see
// how far along the model is, which it cannot.
func startElapsedTicker(label string) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	start := time.Now()

	go func() {
		defer close(finished)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				fmt.Printf("%s … %s elapsed\n", label, time.Since(start).Round(time.Second))
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func loadGoalCLI(ctx context.Context, pool *db.Pool, goalID string) (*engine.Goal, error) {
	var g engine.Goal
	var status, autonomy, risk string
	err := pool.QueryRow(ctx, `
		select id, project_id, created_by, title, statement, status, autonomy, risk_tier,
		       tokens_spent, cost_cents_spent, tasks_created, started_at, created_at
		  from forge_goals where id = $1`, goalID).
		Scan(&g.ID, &g.ProjectID, &g.CreatedBy, &g.Title, &g.Statement, &status, &autonomy, &risk,
			&g.Spend.Tokens, &g.Spend.CostCents, &g.Spend.TasksCreated, &g.StartedAt, &g.CreatedAt)
	if err != nil {
		return nil, errs.Wrap("forgectl.loadGoal", errs.CodeNotFound, err).
			WithDetail("no goal %s", goalID)
	}
	g.Status = engine.GoalStatus(status)
	g.Autonomy = engine.Autonomy(autonomy)
	g.RiskTier = engine.RiskTier(risk)
	return &g, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncateCLI(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
