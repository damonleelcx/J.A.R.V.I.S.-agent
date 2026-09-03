package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/secrets"
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
  it in "assumptions" instead.

UNTRUSTED CONTENT

` + UntrustedRule + "`"

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
	// secrets resolves the handles a model writes into tool arguments (PRD
	// SEC-03). Nil is a legal deployment — one that declares no secrets — and a
	// call referencing a handle is then refused rather than passed through.
	secrets *secrets.Broker
	// characters resolves the project's character (PRD RSN-04). Same shape and
	// same reason as secrets: nil executes every project with the character this
	// executor was constructed with.
	characters *CharacterStore
}

// NewExecutor wires an executor.
func NewExecutor(client llm.Client, registry *tools.Registry, repo *engine.Repository,
	budget *engine.BudgetGuard, char persona.Character, clk clock.Clock, log *logx.Logger, pool *db.Pool) *Executor {
	return &Executor{
		client: client, registry: registry, repo: repo, budget: budget,
		char: char, clock: clk, log: log, pool: pool,
	}
}

// WithSecrets attaches a secret broker.
//
// Separate from NewExecutor so that adding it does not change the signature
// every caller and test uses, and so that a deployment without one is an
// explicit shape rather than a nil someone forgot to pass.
func (e *Executor) WithSecrets(b *secrets.Broker) *Executor { e.secrets = b; return e }

// WithCharacters makes execution honour the project's critique intensity.
func (e *Executor) WithCharacters(s *CharacterStore) *Executor { e.characters = s; return e }

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
	framing := executorFraming
	// Tell the model which handles exist, by name and purpose only (PRD SEC-03).
	//
	// Without this the mechanism is unusable: a model that does not know
	// `secret://github_token` exists will either invent a credential, ask a
	// person for one, or give up. Only granted, unrevoked handles are listed —
	// a handle the model cannot use produces a refusal it could not have
	// predicted, which teaches it nothing.
	if note := e.secretsNote(ctx, tc); note != "" {
		framing += note
	}
	// Resolved once per cycle and used for both the fresh prompt and the rebuilt
	// one below, so a task cannot be planned under one character and resumed
	// under another because the row changed mid-flight.
	char := e.characters.For(ctx, tc.Goal.ProjectID, e.char)
	messages := tc.Messages(persona.SystemPrompt(char, framing))

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
			messages = append([]llm.Message{{Role: llm.System, Content: persona.SystemPrompt(char, executorFraming)}},
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

	// The arguments must match the schema the contract declares (PRD: the
	// contract documents InputSchema as checked before the tool runs).
	//
	// # Where this sits, and why
	//
	// After the permission check and BEFORE the idempotency key: a call the
	// grant forbids is refused whatever its shape, and a malformed call must
	// not claim a key — a later well-formed call with the same arguments would
	// then dedupe against a record of something that never ran.
	//
	// Refused rather than failed. The tool did not run, so `failed` would put a
	// row in the ledger asserting an execution that never happened. The model
	// gets the specific problem — which field, and what was wrong with it — so
	// its next attempt is a correction rather than a guess.
	if err := e.registry.ValidateInput(name, json.RawMessage(call.Function.Arguments)); err != nil {
		detail := err.Error()
		e.recordToolCall(ctx, tc, call, engine.ToolRefused, "",
			string(errs.CodeValidationFailed), detail, 0)
		e.log.Info(ctx, logx.EventToolRefusedSchema,
			"task_id", tc.Task.ID, "tool", name)
		return toolError(string(errs.CodeValidationFailed), detail)
	}

	// Idempotency key: stable across retries of the same logical call, so a
	// replayed attempt finds the completed record instead of acting again.
	key := idempotencyKey(tc.Task.ID, name, call.Function.Arguments)

	if prior, found := e.findCompletedCall(ctx, key); found {
		e.log.Info(ctx, logx.EventToolDeduplicated,
			"task_id", tc.Task.ID, "tool", name, "idempotency_key", key)
		return string(prior)
	}

	// Secret handles (PRD SEC-03).
	//
	// The model wrote `secret://name` somewhere in its arguments. Resolution
	// happens here, AFTER the idempotency key is computed from the raw
	// arguments — so the key is stable across a credential rotation, which is
	// what it should be: rotating a secret does not make a call a different
	// call.
	resolution, secErr := e.resolveSecrets(ctx, tc, name, call.Function.Arguments)
	if secErr != nil {
		// A refusal, not a silent pass-through. Leaving the literal handle in
		// place is how a request goes out with `Authorization: Bearer
		// secret://github_token` and fails for a reason that has nothing to do
		// with credentials.
		e.recordToolCall(ctx, tc, call, engine.ToolRefused, "",
			string(errs.CodeOf(secErr)), secErr.Error(), 0)
		e.log.Warn(ctx, logx.EventSecretRefused,
			"task_id", tc.Task.ID, "tool", name, "code", string(errs.CodeOf(secErr)))
		return toolError(string(errs.CodeOf(secErr)), secErr.Error())
	}

	inv := tools.Invocation{
		Tool:           name,
		Input:          json.RawMessage(call.Function.Arguments),
		IdempotencyKey: key,
		TaskID:         tc.Task.ID,
		GoalID:         tc.Goal.ID,
		Workspace:      workspace,
		Secrets:        resolution.Values,
	}

	callCtx, cancel := context.WithTimeout(ctx, contract.Timeout)
	defer cancel()

	start := e.clock.Now()
	res, runErr := tool.Run(callCtx, inv)
	elapsed := e.clock.Now().Sub(start)

	if runErr != nil {
		// The error text is redacted too. A failing HTTP client quoting the
		// header it choked on is one of the likeliest ways a value comes back.
		detail := resolution.Redactor.Redact(runErr.Error())
		e.recordToolCall(ctx, tc, call, engine.ToolFailed, "",
			string(errs.CodeOf(runErr)), detail, elapsed)
		e.log.Info(ctx, logx.EventToolFailed,
			"task_id", tc.Task.ID, "tool", name, "code", string(errs.CodeOf(runErr)))
		return toolError(string(errs.CodeOf(runErr)), detail)
	}

	// Redaction, before the result reaches EITHER the model or the ledger.
	//
	// This is the half of SEC-03 that the handle mechanism is worthless without:
	// the tool has the value now, and its output is about to become context.
	output := resolution.Redactor.RedactJSON(res.Output)
	raw := resolution.Redactor.Redact(res.Raw)

	// Defence in depth. If a value survives redaction, an encoding was missed —
	// and the right response is to lose the tool result rather than hand the
	// model a credential. Losing a result is recoverable; the other is not.
	if leaked := resolution.Redactor.Leaks(string(output)+raw, resolution.Values); len(leaked) > 0 {
		e.log.Warn(ctx, logx.EventSecretLeakBlocked,
			"task_id", tc.Task.ID, "tool", name, "handles", strings.Join(leaked, ","))
		blocked := fmt.Sprintf("This tool returned output containing the value of %s, and the "+
			"redactor could not remove it, so the whole result was discarded. The tool ran and "+
			"its effects stand; only the output was withheld. Do not retry expecting to see it.",
			handleList(leaked))
		e.recordToolCall(ctx, tc, call, engine.ToolSucceeded, blocked, "", "", elapsed)
		return toolError(string(errs.CodeSecretUnavailable), blocked)
	}

	e.recordToolCall(ctx, tc, call, engine.ToolSucceeded, raw, "", "", elapsed)
	e.log.Debug(ctx, logx.EventToolSucceeded,
		"task_id", tc.Task.ID, "tool", name, "duration_ms", elapsed.Milliseconds())

	// PRD SEC-04. Tool output is untrusted input: a file in the workspace, the
	// stdout of a command, a page somebody else wrote. Until now it reached the
	// model as a JSON string indistinguishable from anything the operator said.
	//
	// It is FRAMED rather than filtered — see untrusted.go for why nothing is
	// stripped — and what the scan found is recorded rather than swallowed. The
	// framing goes on last, after redaction, so the envelope wraps what the
	// model will actually see.
	framed, findings := Untrusted(name, string(output))
	if len(findings) > 0 {
		e.log.Warn(ctx, logx.EventInjectionSuspected,
			"task_id", tc.Task.ID, "goal_id", tc.Goal.ID, "tool", name,
			"patterns", Summarise(findings))
		// Into the timeline as well as the log. "Did anything try to steer the
		// agent through its own tool output?" is a question asked after the
		// fact, by somebody reading the goal rather than grepping a log.
		e.appendInjectionEvent(ctx, tc, name, findings)
	}
	return framed
}

// appendInjectionEvent records a suspected injection on the goal's timeline.
//
// Best-effort, and loudly so: failing the tool call because the record could not
// be written would turn a detection into an outage. The log line above has
// already happened, so nothing is lost silently.
func (e *Executor) appendInjectionEvent(ctx context.Context, tc *TaskContext, tool string, findings []Finding) {
	payload, _ := json.Marshal(map[string]any{
		"tool":     tool,
		"patterns": findings,
		"note": "Content returned by this tool matched known prompt-injection shapes. It was framed as " +
			"untrusted data and passed to the model unaltered — nothing was removed, because rewriting " +
			"untrusted content cannot be done correctly and would leave it looking trustworthy.",
	})
	ev := &engine.Event{
		GoalID: tc.Goal.ID, TaskID: &tc.Task.ID,
		Kind: engine.EventInjectionSuspected, Actor: engine.ActorSystem,
		Summary: fmt.Sprintf("%s returned content matching %d prompt-injection pattern(s)", tool, len(findings)),
		Payload: payload,
	}
	if err := e.repo.AppendEvent(ctx, e.pool, ev, e.clock.Now()); err != nil {
		e.log.WarnWith(ctx, logx.EventInjectionSuspected, err,
			"task_id", tc.Task.ID, "detail", "the suspected injection was logged but not recorded on the timeline")
	}
}

// secretsNote describes the available handles for the system prompt.
//
// Names and purposes, never values — this string goes straight into the context
// window. The struct it is built from has no field that could hold a value,
// which is how that stays true when somebody edits this later.
func (e *Executor) secretsNote(ctx context.Context, tc *TaskContext) string {
	if e.secrets == nil {
		return ""
	}
	available, err := e.secrets.Describe(ctx, tc.Goal.ProjectID)
	if err != nil {
		// Not fatal. A task that cannot list handles can still do work that
		// needs none, and failing the whole cycle over it would make an
		// unrelated database hiccup look like a task failure.
		e.log.WarnWith(ctx, logx.EventSecretRefused, err, "goal_id", tc.Goal.ID,
			"detail", "the available secret handles could not be listed; this task runs without them")
		return ""
	}
	if len(available) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nCREDENTIALS\n" +
		"You never see a credential's value. Write the handle where the value belongs and it is " +
		"substituted at the tool boundary, only for the tools listed beside it. A handle used " +
		"anywhere else is refused, and tool output containing a resolved value is redacted before " +
		"you see it — so do not try to read one back.\n")
	for _, a := range available {
		fmt.Fprintf(&b, "  %s — %s (usable by: %s)\n",
			a.Handle, orNoPurpose(a.Description), strings.Join(a.Tools, ", "))
	}
	return b.String()
}

func orNoPurpose(s string) string {
	if strings.TrimSpace(s) == "" {
		return "no purpose recorded"
	}
	return s
}

// resolveSecrets finds the handles a tool call references and resolves them.
//
// A deployment with no broker configured resolves nothing, and a call that
// references a handle in that deployment is refused rather than passed through:
// "there is no secret broker here" is a better message than a request that goes
// out with a handle where a token should be.
func (e *Executor) resolveSecrets(ctx context.Context, tc *TaskContext, toolName, args string) (*secrets.Resolution, error) {
	handles := secrets.FindHandles(args)
	if len(handles) == 0 {
		return &secrets.Resolution{Values: map[string]string{}, Redactor: secrets.NewRedactor(nil)}, nil
	}
	if e.secrets == nil {
		return nil, errs.New("agent.Executor.resolveSecrets", errs.CodeSecretUnavailable).
			WithDetail("this call references %s and no secret broker is configured in this deployment",
				handleList(handles))
	}
	return e.secrets.Resolve(ctx, tc.Goal.ProjectID, toolName, handles)
}

func handleList(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, secrets.HandlePrefix+n)
	}
	return strings.Join(out, ", ")
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
