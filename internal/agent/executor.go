package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

const executorFraming = `You are EXECUTING one task. Do that task and nothing else.

You have tools. Use them to find out what is true rather than assuming: read a
file before editing it, list a directory before claiming what is in it, run the
check rather than predicting its result.

When you are finished, reply with JSON only:

{
  "status": "completed" | "blocked" | "failed",
  "summary": "what you did, in one or two sentences",
  "result": { ... whatever this task was asked to produce ... },
  "evidence": ["what you actually observed that supports the result"],
  "assumptions": ["anything you had to assume rather than check"],
  "blocked_reason": "" or "what you need in order to continue"
}

- "completed" means the work is done and the evidence supports it. It does NOT
  mean verified — something else checks that.
- "blocked" is a legitimate outcome. A task you cannot do because of a missing
  permission, an unavailable connector, or an ambiguity is blocked, not failed,
  and saying so is better than producing something plausible.
- "evidence" is what you OBSERVED — a command's exit code, a file's contents, a
  tool's output. Not what you expect to be true. If a claim has no evidence, put
  it in "assumptions" instead.`

// Executor runs one task through a bounded tool loop.
type Executor struct {
	client   llm.Client
	registry *tools.Registry
	repo     *engine.Repository
	budget   *engine.BudgetGuard
	char     persona.Character
	clock    clock.Clock
	log      *logx.Logger
	pool     *db.Pool
}

// NewExecutor wires an executor.
func NewExecutor(client llm.Client, registry *tools.Registry, repo *engine.Repository,
	budget *engine.BudgetGuard, char persona.Character, clk clock.Clock, log *logx.Logger, pool *db.Pool) *Executor {
	return &Executor{
		client: client, registry: registry, repo: repo, budget: budget,
		char: char, clock: clk, log: log, pool: pool,
	}
}

// Outcome is what an execution cycle produced.
type Outcome struct {
	Status        string          `json:"status"`
	Summary       string          `json:"summary"`
	Result        json.RawMessage `json:"result"`
	Evidence      []string        `json:"evidence"`
	Assumptions   []string        `json:"assumptions"`
	BlockedReason string          `json:"blocked_reason"`
	Usage         llm.Usage       `json:"-"`
	Iterations    int             `json:"-"`
	ToolCallsMade int             `json:"-"`
}

// Execute runs the task to a conclusion or a bound.
//
// # Shape of the loop
//
// observe → the model decides → tools run → results are checkpointed → repeat.
// Every iteration writes a checkpoint BEFORE the next model call, so a crash at
// any point leaves the next attempt able to resume rather than restart. The
// conversation grows within one Execute call, but it is never the source of
// truth: the checkpoint is, and a fresh process rebuilds from it.
func (e *Executor) Execute(ctx context.Context, tc *TaskContext, workspace string) (*Outcome, error) {
	const op = "agent.Executor.Execute"

	definitions := e.registry.Definitions(tc.Grant)
	messages := tc.Messages(persona.SystemPrompt(e.char, executorFraming))

	// Resume the conversation from a checkpoint when one exists, so an
	// interrupted task does not start its reasoning over.
	if tc.Checkpoint != nil {
		var saved struct {
			Messages []llm.Message `json:"messages"`
		}
		if err := json.Unmarshal(tc.Checkpoint.State, &saved); err == nil && len(saved.Messages) > 0 {
			// Rebuild the system prompt from the persona package rather than
			// trusting the stored copy: identity is reconstructed every cycle,
			// and a checkpoint written under an older persona version must not
			// resurrect it.
			messages = append([]llm.Message{{Role: llm.System, Content: persona.SystemPrompt(e.char, executorFraming)}},
				saved.Messages...)
		} else if err != nil {
			e.log.WarnWith(ctx, logx.EventTaskResumeDegraded, err,
				"task_id", tc.Task.ID, "checkpoint", tc.Checkpoint.ID,
				"detail", "the checkpoint could not be decoded; the task restarts from its instruction "+
					"instead of resuming, so completed side effects may be repeated")
		}
	}

	out := &Outcome{}
	totalCalls := 0

	for iteration := 0; ; iteration++ {
		if breach := e.budget.CheckIteration(iteration); breach != nil {
			return nil, breach.Error()
		}

		resp, err := e.client.Complete(ctx, llm.Request{
			Role:      llm.RoleExecutor,
			Messages:  messages,
			Tools:     definitions,
			MaxTokens: 8192,
		})
		if err != nil {
			return nil, err
		}
		out.Usage.PromptTokens += resp.Usage.PromptTokens
		out.Usage.CompletionTokens += resp.Usage.CompletionTokens
		out.Usage.TotalTokens += resp.Usage.TotalTokens
		out.Iterations = iteration + 1

		// Record spend immediately, not at the end. A task that is killed
		// mid-loop has still spent the tokens, and a budget that only counts
		// completed work is not a budget.
		if err := e.budget.RecordSpend(ctx, e.pool, tc.Goal.ID, resp.Usage.TotalTokens, 0); err != nil {
			e.log.WarnWith(ctx, logx.EventBudgetRecordFailed, err, "goal_id", tc.Goal.ID)
		}

		messages = append(messages, llm.Message{
			Role: llm.Assistant, Content: resp.Content, ToolCalls: resp.ToolCalls,
		})

		if len(resp.ToolCalls) == 0 {
			// No tools requested: this is the model's final answer.
			if err := json.Unmarshal([]byte(extractJSON(resp.Content)), out); err != nil {
				return nil, errs.Wrap(op, errs.CodeExternalProtocol, err).
					WithDetail("the executor did not return usable JSON: %s", truncate(resp.Content, 400))
			}
			if err := out.validate(); err != nil {
				return nil, err
			}
			out.ToolCallsMade = totalCalls
			return out, nil
		}

		if breach := e.budget.CheckToolCalls(len(resp.ToolCalls)); breach != nil {
			return nil, breach.Error()
		}

		for _, call := range resp.ToolCalls {
			totalCalls++
			result := e.runTool(ctx, tc, call, workspace)
			messages = append(messages, llm.Message{
				Role:       llm.Tool,
				Name:       call.Function.Name,
				ToolCallID: call.ID,
				Content:    result,
			})
		}

		// Checkpoint after every iteration, before the next model call. This is
		// the line that makes the whole thing resumable: a crash here loses at
		// most one iteration, not the task.
		state, _ := json.Marshal(map[string]any{
			"messages":  messages[1:], // the system prompt is rebuilt, never stored
			"iteration": iteration,
		})
		if _, err := e.repo.SaveCheckpoint(ctx, e.pool, tc.Task.ID,
			engine.CheckpointIterationEnd, state, e.clock.Now()); err != nil {
			// Checkpointing is best-effort relative to the work itself: failing
			// the task because we could not save a resume point would discard
			// work that actually succeeded. But it is loud, because the next
			// crash now costs more.
			e.log.WarnWith(ctx, logx.EventCheckpointFailed, err,
				"task_id", tc.Task.ID, "iteration", iteration,
				"detail", "no resume point was saved; a crash before the next checkpoint will restart this task")
		}
	}
}

// runTool executes one tool call and returns the content for the tool message.
//
// Errors are returned to the MODEL as content rather than aborting the loop.
// A tool failing is information the model must handle — a missing file, a failed
// build, a refused permission. Aborting would throw away the loop's context and
// force a restart that hits the same wall.
func (e *Executor) runTool(ctx context.Context, tc *TaskContext, call llm.ToolCall, workspace string) string {
	name := call.Function.Name

	tool, err := e.registry.Get(name)
	if err != nil {
		return toolError("NO_SUCH_TOOL", fmt.Sprintf(
			"There is no tool named %q. Use only the tools you were given.", name))
	}
	contract := tool.Contract()

	if ok, why := tc.Grant.Permits(contract); !ok {
		e.recordToolCall(ctx, tc, call, engine.ToolRefused, "", string(errs.CodeToolRefused), why, 0)
		return toolError(string(errs.CodeToolRefused), why)
	}

	// Idempotency key: stable across retries of the same logical call, so a
	// replayed attempt finds the completed record instead of acting again.
	key := idempotencyKey(tc.Task.ID, name, call.Function.Arguments)

	if prior, found := e.findCompletedCall(ctx, key); found {
		e.log.Info(ctx, logx.EventToolDeduplicated,
			"task_id", tc.Task.ID, "tool", name, "idempotency_key", key)
		return string(prior)
	}

	inv := tools.Invocation{
		Tool:           name,
		Input:          json.RawMessage(call.Function.Arguments),
		IdempotencyKey: key,
		TaskID:         tc.Task.ID,
		GoalID:         tc.Goal.ID,
		Workspace:      workspace,
	}

	callCtx, cancel := context.WithTimeout(ctx, contract.Timeout)
	defer cancel()

	start := e.clock.Now()
	res, runErr := tool.Run(callCtx, inv)
	elapsed := e.clock.Now().Sub(start)

	if runErr != nil {
		e.recordToolCall(ctx, tc, call, engine.ToolFailed, "",
			string(errs.CodeOf(runErr)), runErr.Error(), elapsed)
		e.log.Info(ctx, logx.EventToolFailed,
			"task_id", tc.Task.ID, "tool", name, "code", string(errs.CodeOf(runErr)))
		return toolError(string(errs.CodeOf(runErr)), runErr.Error())
	}

	e.recordToolCall(ctx, tc, call, engine.ToolSucceeded, res.Raw, "", "", elapsed)
	e.log.Debug(ctx, logx.EventToolSucceeded,
		"task_id", tc.Task.ID, "tool", name, "duration_ms", elapsed.Milliseconds())

	return string(res.Output)
}

// findCompletedCall looks for a prior successful call under this key.
func (e *Executor) findCompletedCall(ctx context.Context, key string) (json.RawMessage, bool) {
	var output json.RawMessage
	err := e.pool.QueryRow(ctx,
		`select output from forge_tool_calls where idempotency_key = $1 and status = 'succeeded'`,
		key).Scan(&output)
	if err != nil || len(output) == 0 {
		return nil, false
	}
	return output, true
}

// recordToolCall appends to the idempotency ledger and the timeline.
//
// Best-effort: a ledger write that fails must not discard a tool result that
// actually happened. It is warned loudly because the ledger is what makes a
// retry safe, and a gap in it means the next retry may repeat a side effect.
func (e *Executor) recordToolCall(ctx context.Context, tc *TaskContext, call llm.ToolCall,
	status engine.ToolCallStatus, raw, errCode, errDetail string, elapsed time.Duration) {

	now := e.clock.Now()
	err := db.InTx(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			insert into forge_tool_calls
				(id, task_id, idempotency_key, tool_name, input, status, output,
				 raw_output, error_code, error_detail, started_at, ended_at, duration_ms, created_at)
			values ($1,$2,$3,$4,$5,$6,$7,$8,nullif($9,''),nullif($10,''),$11,$11,$12,$11)
			on conflict (idempotency_key) do nothing`,
			id.New(id.PrefixToolCall), tc.Task.ID,
			idempotencyKey(tc.Task.ID, call.Function.Name, call.Function.Arguments),
			call.Function.Name, jsonOrObject(call.Function.Arguments), string(status),
			nil, raw, errCode, errDetail, now, elapsed.Milliseconds())
		return err
	})
	if err != nil {
		e.log.WarnWith(ctx, logx.EventToolLedgerFailed, err,
			"task_id", tc.Task.ID, "tool", call.Function.Name,
			"detail", "the tool ran but was not recorded in the idempotency ledger; a retry could repeat its effect")
	}
}

// validate checks an outcome is usable and internally honest.
func (o *Outcome) validate() error {
	const op = "agent.Outcome.validate"

	switch o.Status {
	case "completed", "blocked", "failed":
	default:
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the executor returned status %q; expected completed, blocked or failed", o.Status)
	}
	if strings.TrimSpace(o.Summary) == "" {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the executor returned no summary")
	}
	if o.Status == "blocked" && strings.TrimSpace(o.BlockedReason) == "" {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the executor reported 'blocked' without saying what it needs, " +
				"which leaves nobody able to unblock it")
	}
	return nil
}

// Completed reports whether the executor believes the work is done. It is
// deliberately not called Succeeded: completion is the executor's claim, and
// verification is a separate step performed by something else.
func (o *Outcome) Completed() bool { return o.Status == "completed" }

// idempotencyKey derives a stable key for a logical tool call.
//
// Task id plus tool plus exact arguments. Including the task means the same call
// in two different tasks is two calls, which is correct: they are two decisions.
// Excluding the attempt number is the whole point — a retry produces the SAME
// key, which is what makes the ledger able to short-circuit it.
func idempotencyKey(taskID, tool, args string) string {
	return fmt.Sprintf("%s:%s:%x", taskID, tool, hashString(args))
}

func hashString(s string) uint64 {
	// FNV-1a. Not cryptographic — this is a deduplication key, not a secret, and
	// the task id and tool name already scope it.
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

func toolError(code, detail string) string {
	b, _ := json.Marshal(map[string]any{"error": code, "detail": detail})
	return string(b)
}

func jsonOrObject(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage(`{}`)
	}
	if !json.Valid([]byte(s)) {
		b, _ := json.Marshal(map[string]string{"raw": s})
		return b
	}
	return json.RawMessage(s)
}
