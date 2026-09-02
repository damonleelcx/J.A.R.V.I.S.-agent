// Command forged is FORGE's HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/httpapi"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/mail"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "\nforged failed to start.")
		fmt.Fprintf(os.Stderr, "  error : %v\n", err)
		if d, ok := errs.Lookup(errs.CodeOf(err)); ok {
			fmt.Fprintf(os.Stderr, "  code  : %s (%s)\n", d.Code, d.Category)
			fmt.Fprintf(os.Stderr, "  fix   : %s\n", d.Remedy)
		}
		os.Exit(1)
	}
}

func run() error {
	// SIGINT/SIGTERM cancel this context, which begins graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := config.LoadDotEnv(".env"); err != nil {
		return errs.Wrap("forged.run", errs.CodeConfigInvalid, err).
			WithDetail(".env exists but could not be read")
	}
	// The server needs everything except the engine, which is the worker's
	// concern. Declaring the difference means an operator running only the API
	// is not asked for configuration it will never use.
	cfg, warnings, err := config.Load(
		config.SectionDB, config.SectionHTTP, config.SectionMail,
		config.SectionAuth, config.SectionLLM,
	)
	if err != nil {
		return err
	}

	log := logx.New(logx.Options{
		Level:   parseLevel(cfg.Log.Level),
		Format:  cfg.Log.Format,
		Service: "forged",
	})
	log.Info(ctx, logx.EventServerStarting,
		"version", version, "commit", commit, "built", date)
	for _, w := range warnings {
		log.Warn(ctx, logx.EventConfigDefault, "warning", w)
	}
	log.Info(ctx, logx.EventConfigLoaded, "config", cfg.Redacted())

	clk := clock.System{}

	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Migrating at startup is deliberate for this deployment shape: FORGE ships
	// as a private, self-hosted system, and requiring a separate migration step
	// is a step that gets skipped. The chain is idempotent and serialised by an
	// advisory lock, so several instances starting at once is safe.
	results, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, log)
	if err != nil {
		return err
	}
	applied := 0
	for _, r := range results {
		if r.FirstApply {
			applied++
		}
	}
	log.Info(ctx, logx.EventMigrationApplied,
		"total", len(results), "first_apply", applied)

	mailer, err := mail.NewSender(cfg.Mail, log, clk)
	if err != nil {
		return err
	}
	if fs, ok := mailer.(*mail.FileSender); ok {
		log.Warn(ctx, logx.EventMailFileDrop,
			"outbox", fs.OutboxDir(),
			"detail", "the file mail transport is active: mail is written to disk and NOT delivered. Read verification links from that directory.")
	}

	identitySvc := identity.NewService(
		pool, identity.NewRepository(), mailer, cfg.Auth, cfg.HTTP.PublicURL, clk, log)

	// The workbench conversation needs a model. Nil is legal — the API and the
	// operations console work without one, and the workbench says so rather than
	// failing to load.
	var modelClient llm.Client
	if cfg.LLM.APIKey != "" {
		modelClient = llm.NewOpenAICompatible(cfg.LLM, log, clk)
	} else {
		log.Warn(ctx, logx.EventConfigDefault,
			"detail", "no FORGE_LLM_API_KEY: the workbench will load but cannot hold a conversation")
	}

	handler := httpapi.NewRouter(httpapi.Deps{
		Config:   cfg,
		Pool:     pool,
		Identity: identitySvc,
		LLM:      modelClient,
		Clock:    clk,
		Log:      log,
		Version:  version,
		Commit:   commit,
	})

	srv := &http.Server{
		Addr:         cfg.HTTP.Addr,
		Handler:      handler,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
		IdleTimeout:  2 * time.Minute,
		// ReadHeaderTimeout bounds a slowloris that dribbles headers forever.
		// ReadTimeout alone does not cover it on connections that never finish
		// sending a request line.
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info(ctx, logx.EventServerReady,
			"addr", cfg.HTTP.Addr, "public_url", cfg.HTTP.PublicURL, "env", string(cfg.Env))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return errs.Wrap("forged.run", errs.CodeInternal, err).
				WithDetail("the HTTP listener failed; is %s already in use?", cfg.HTTP.Addr)
		}
		return nil
	case <-ctx.Done():
		log.Info(context.WithoutCancel(ctx), logx.EventServerStopping,
			"grace", cfg.HTTP.ShutdownGrace.String())
	}

	// Shutdown runs on a fresh context: the signal already cancelled the old
	// one, and passing a cancelled context to Shutdown would abandon in-flight
	// requests immediately rather than draining them.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTP.ShutdownGrace)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn(context.WithoutCancel(ctx), logx.EventShutdownTimeout,
			"error", err.Error(),
			"detail", "in-flight requests did not finish within the grace period and were dropped")
	}
	log.Info(context.WithoutCancel(ctx), logx.EventServerStopped)
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
