package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/eval"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The evaluation suite from a terminal (README phase 7).
//
// # Why this is not part of `make test`
//
// It calls a real model, which costs money and takes minutes, and its output is
// non-deterministic — the same prompt has produced a correct standards figure
// and a fabricated one four runs apart. A suite like that inside the per-commit
// test run would either be flaky or be quietly weakened until it stopped
// failing, and both are worse than not running it.
//
// So it is a command an operator runs deliberately and a scheduled CI job runs
// on its own cadence, with the model and the repeat count printed beside the
// numbers, because a rate means nothing without them.

// cmdEvalList prints the suite and what each case exists because of.
//
// No model and no database: the question it answers is about this build.
func cmdEvalList() error {
	for _, c := range eval.Cases() {
		fmt.Printf("%s\n", c.ID)
		fmt.Printf("    %s\n", wrap(c.Why, 74, "    "))
		fmt.Printf("    turns: %d\n", len(c.Turns))
		for _, s := range c.Scorers {
			// A tracked scorer printed as "floor 0%" reads as a requirement
			// somebody broke, and the next person deletes it. Same rule as the
			// report's rendering.
			bound := fmt.Sprintf("floor %.0f%%", s.Floor*100)
			if s.Tracked {
				bound = "tracked, not required"
			}
			fmt.Printf("      · %s  (%s)\n", s.Name, bound)
			fmt.Printf("        %s\n", wrap(s.Asserts, 70, "        "))
		}
		fmt.Println()
	}
	fmt.Println("Every scorer is deterministic Go over the reply. Nothing in this suite asks a model")
	fmt.Println("to grade a model, and where a scorer needs a fact about the world — the published")
	fmt.Println("dimensions of a NEMA 17 face — that fact is written down in the suite with its")
	fmt.Println("source named, rather than taken from the thing being measured.")
	return nil
}

// cmdEvalRun executes the suite against a real model.
//
// # Exit status
//
// 0 when every scorer reached its floor; 1 otherwise, including when a case
// could not be exercised at all. The floors are OBSERVATIONS rather than
// targets, so a red run asks "what changed — the model, its version, or the
// prompt?" and not "which number do we lower?".
func cmdEvalRun(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdEvalRun"

	fs := newFlagSet("eval run")
	only := fs.String("only", "", "comma-separated case ids; default is all of them")
	repeats := fs.Int("repeats", 3, "how many times to run each case; a single run measures nothing")
	verbose := fs.Bool("verbose", false, "print every run's detail, not only the scorers that fell short")
	jsonOut := fs.String("json", "", "also write the full report, with every reply, to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.LLM.APIKey == "" {
		return errs.New(op, errs.CodeConfigInvalid).
			WithDetail("evaluation measures how the MODEL behaves inside the harness, so it needs a real " +
				"one — a stub would measure the stub. Set FORGE_LLM_API_KEY and run this again.")
	}

	client := llm.NewOpenAICompatible(cfg.LLM, log, clock.System{})
	runner, err := eval.NewRunner(client, *repeats)
	if err != nil {
		return err
	}

	var names []string
	for _, n := range strings.Split(*only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	// Refused before the first request rather than after the last: an unknown
	// case id should not cost a model call to discover.
	if _, err := eval.Select(names); err != nil {
		return err
	}

	// Progress, because this takes minutes and a silent terminal is
	// indistinguishable from a hung one.
	runner.OnProgress = func(caseID string, run, of int, err error) {
		status := "ok"
		if err != nil {
			status = "FAILED: " + err.Error()
		}
		fmt.Fprintf(os.Stderr, "  %s  run %d/%d  %s\n", caseID, run, of, status)
	}
	fmt.Fprintf(os.Stderr, "Running the evaluation suite against %s. This calls a real model.\n\n",
		client.ModelFor(llm.RoleConverse))

	report, err := runner.Run(ctx, names)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr)
	fmt.Print(report.Render(*verbose))

	if *jsonOut != "" {
		// The raw replies, so the scoring can be re-judged rather than taken on
		// trust — the same rule the spikes follow: show the process data, not
		// only the conclusion.
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return errs.Wrap(op, errs.CodeSerializationFail, err)
		}
		if err := os.WriteFile(*jsonOut, body, 0o644); err != nil {
			return errs.Wrap(op, errs.CodeInternal, err).
				WithDetail("could not write %s", *jsonOut)
		}
		fmt.Printf("\nEvery reply, and every scorer's reasoning, written to %s\n", *jsonOut)
	}

	if !report.Met() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("at least one scorer fell below its floor. The floors are observations rather than " +
				"targets: the question is what changed — the model, its version, or the prompt.")
	}
	return nil
}
