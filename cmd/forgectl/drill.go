package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/drill"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// cmdDrillList prints the registered drills and what each one proves.
func cmdDrillList() error {
	for _, s := range drill.Scenarios() {
		fmt.Printf("%s\n    %s\n\n", s.Name, wrap(s.Describes, 76, "    "))
	}
	fmt.Println("Each drill runs in a schema of its own, injects a real fault, and PROVES the fault")
	fmt.Println("landed before asserting anything. A drill that cannot prove it is reported VACUOUS")
	fmt.Println("and fails the run — a scenario that disturbed nothing has demonstrated nothing.")
	return nil
}

// cmdDrillRun executes recovery drills (PRD NFR-07).
//
// # Exit status is the point
//
// 0 when every drill injected its fault AND every invariant held; 1 otherwise —
// including when a drill was vacuous. It belongs in the release checklist beside
// `audit verify` and `graph review`.
func cmdDrillRun(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	fs := newFlagSet("drill run")
	only := fs.String("only", "", "comma-separated drill names; default is all of them")
	keep := fs.Bool("keep", false, "leave each drill's schema behind for inspection")
	verbose := fs.Bool("verbose", false, "print every invariant, not only the ones that did not hold")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var names []string
	if strings.TrimSpace(*only) != "" {
		for _, n := range strings.Split(*only, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}

	report, err := drill.Run(ctx, cfg.DB.URL, names, *keep, log)
	if err != nil {
		return err
	}

	for i := range report.Results {
		r := &report.Results[i]
		fmt.Println(r.Summary())
		for _, c := range r.Checks {
			if c.Held && !*verbose {
				continue
			}
			mark := "  ✓"
			if !c.Held {
				mark = "  ✗"
			}
			fmt.Printf("%s %-34s %s\n", mark, c.Name, c.Detail)
		}
		if !r.FaultInjected() && r.Err == nil {
			fmt.Println("     The scenario ran without error and disturbed nothing. Read it: either the " +
				"injection no longer works, or the system changed under it.")
		}
	}
	fmt.Printf("\n%s\n", report.Summary())
	if *keep {
		fmt.Println("Schemas were kept: look for forge_drill_* in the database.")
	}
	if !report.Passed() {
		os.Exit(1)
	}
	return nil
}

// wrap re-flows a description for a terminal.
func wrap(s string, width int, indent string) string {
	var out strings.Builder
	line := 0
	for _, word := range strings.Fields(s) {
		if line > 0 && line+len(word)+1 > width {
			out.WriteString("\n" + indent)
			line = 0
		} else if line > 0 {
			out.WriteString(" ")
			line++
		}
		out.WriteString(word)
		line += len(word)
	}
	return out.String()
}
