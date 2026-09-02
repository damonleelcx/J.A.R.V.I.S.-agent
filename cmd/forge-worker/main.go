// Command forge-worker runs FORGE's agent workers.
//
// It is a separate binary from forged deliberately. The API server and the
// workers have different failure modes, different resource profiles, and
// different scaling needs — and separating them means an agent loop that wedges
// or exhausts memory cannot take the API down with it. It also makes "stop doing
// work but stay reachable" a deployment action rather than a code path.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "\nforge-worker failed to start.")
		fmt.Fprintf(os.Stderr, "  error : %v\n", err)
		if d, ok := errs.Lookup(errs.CodeOf(err)); ok {
			fmt.Fprintf(os.Stderr, "  code  : %s (%s)\n", d.Code, d.Category)
			fmt.Fprintf(os.Stderr, "  fix   : %s\n", d.Remedy)
		}
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := config.LoadDotEnv(".env"); err != nil {
		return errs.Wrap("worker.run", errs.CodeConfigInvalid, err)
	}
	// A worker needs storage, models and engine settings. It does not serve
	// HTTP and does not send mail, so it is not asked for that configuration.
	cfg, warnings, err := config.Load(config.SectionDB, config.SectionLLM, config.SectionEngine)
	if err != nil {
		return err
	}

	log := logx.New(logx.Options{
		Level: parseLevel(cfg.Log.Level), Format: cfg.Log.Format, Service: "forge-worker",
	})
	log.Info(ctx, logx.EventWorkerStarting, "version", version, "commit", commit, "built", date)
	for _, w := range warnings {
		// The verifier-independence warning surfaces here. A deployment running
		// the verifier on the executor's own model family is not broken, but an
		// operator must be told rather than left to discover it in an audit.
		log.Warn(ctx, logx.EventConfigDefault, "warning", w)
	}

	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	clk := clock.System{}
	repo := engine.NewRepository()
	queue := engine.NewQueue()
	budget := engine.NewBudgetGuard(cfg.Engine)
	client := llm.NewOpenAICompatible(cfg.LLM, log, clk)
	character := persona.DefaultCharacter()

	registry := tools.NewRegistry()
	registry.MustRegister(tools.ListTool{})
	registry.MustRegister(tools.ReadTool{})
	registry.MustRegister(tools.WriteTool{})
	registry.MustRegister(tools.ShellTool{})
	// Memory as tools rather than as a silent context injection (PRD MEM-01).
	// Going through the registry means every recall and every write lands in the
	// tool-call ledger and the timeline, so "why did FORGE think that?" is
	// answerable from rows rather than from a prompt nobody kept.
	memorySvc := memory.NewService(pool, clk, log)
	registry.MustRegister(tools.NewMemoryRecallTool(memorySvc, pool))
	registry.MustRegister(tools.NewMemoryRememberTool(memorySvc, pool))
	// Declared-but-unavailable connectors are registered on purpose. See
	// tools/unavailable.go: omitting them is what invites the model to invent
	// the result they would have returned.
	for _, t := range tools.StandardUnavailableConnectors() {
		registry.MustRegister(t)
	}

	workspaceRoot := os.Getenv("FORGE_WORKSPACE_ROOT")
	if workspaceRoot == "" {
		workspaceRoot = "./.forge/workspaces"
	}
	if err := os.MkdirAll(workspaceRoot, 0o750); err != nil {
		return errs.Wrap("worker.run", errs.CodeConfigInvalid, err).
			WithDetail("cannot create the workspace root %q", workspaceRoot)
	}

	assembler := agent.NewAssembler(repo, queue)
	executor := agent.NewExecutor(client, registry, repo, budget, character, clk, log, pool)
	verifier := agent.NewVerifier(client, character)

	log.Info(ctx, logx.EventWorkerReady,
		"concurrency", cfg.Engine.WorkerConcurrency,
		"workspace_root", workspaceRoot,
		"planner_model", client.ModelFor(llm.RolePlanner),
		"executor_model", client.ModelFor(llm.RoleExecutor),
		"verifier_model", client.ModelFor(llm.RoleVerifier),
		"tools", len(registry.Contracts()))

	var wg sync.WaitGroup
	for i := 0; i < cfg.Engine.WorkerConcurrency; i++ {
		w := agent.NewWorker(agent.WorkerDeps{
			Pool: pool, Repo: repo, Queue: queue, Budget: budget,
			Assembler: assembler, Executor: executor, Verifier: verifier,
			Config: cfg.Engine, WorkspaceRoot: workspaceRoot, Clock: clk, Log: log,
		})
		wg.Add(1)
		go func(w *agent.Worker) {
			defer wg.Done()
			if err := w.Run(ctx); err != nil {
				log.ErrorWith(ctx, logx.EventWorkerStopped, err, "worker_id", w.ID)
			}
		}(w)
	}

	<-ctx.Done()
	log.Info(context.WithoutCancel(ctx), logx.EventWorkerStopping,
		"detail", "finishing in-flight tasks; they are released back to the queue rather than "+
			"abandoned to their lease timeout")

	// Bounded wait. A worker wedged inside a tool must not hold the process open
	// forever — its lease will expire and another worker will recover the task,
	// which is exactly the mechanism that exists for this.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		log.Info(context.WithoutCancel(ctx), logx.EventWorkerStopped)
	case <-time.After(30 * time.Second):
		log.Warn(context.WithoutCancel(ctx), logx.EventShutdownTimeout,
			"detail", "workers did not stop within 30s; exiting anyway. Their leases will expire "+
				"and another worker will recover the tasks")
	}
	return nil
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
