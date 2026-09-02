package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// cmdAuditVerify checks a goal's timeline against its hash chain.
//
// # Why this is a terminal command and not a console page
//
// It is the thing you run when you have stopped trusting the system. A console
// that renders "audit: OK" is asking you to trust the same deployment you came
// to doubt — it reads the same rows through the same code and could be lying by
// exactly the mechanism you are checking for. From a shell, against the database
// directly, with the binary you chose, the answer means something.
//
// # Exit status is the point
//
// 0 intact, 1 broken. It is meant to be run from cron and from a release
// checklist, where nobody reads the output until the exit code is non-zero.
func cmdAuditVerify(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdAuditVerify"

	fs := newFlagSet("audit verify")
	all := fs.Bool("all", false, "verify every goal rather than one")
	quiet := fs.Bool("quiet", false, "print nothing on success; exit status carries the answer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if !*all && len(rest) != 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl audit verify <goal-id>\n   or: forgectl audit verify --all")
	}

	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	goalIDs := rest
	if *all {
		goalIDs = nil
		rows, err := pool.Query(ctx, `select id from forge_goals order by created_at asc`)
		if err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		defer rows.Close()
		for rows.Next() {
			var gid string
			if err := rows.Scan(&gid); err != nil {
				return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
			}
			goalIDs = append(goalIDs, gid)
		}
		if err := rows.Err(); err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
	}

	repo := engine.NewRepository()
	var broken int
	var events, chained, unchained int

	for _, gid := range goalIDs {
		report, err := repo.VerifyChain(ctx, pool, gid)
		if err != nil {
			return err
		}
		events += report.Events
		chained += report.Chained
		unchained += report.Unchained

		if report.Intact() {
			if !*quiet && !*all {
				fmt.Printf("%s  %s\n", gid, report.Summary())
			}
			continue
		}
		broken++
		fmt.Printf("\n%s  %s\n", gid, report.Summary())
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  SEQ\tPROBLEM\tKIND\tDETAIL")
		for _, f := range report.Findings {
			fmt.Fprintf(w, "  %d\t%s\t%s\t%s\n", f.Seq, f.Problem, f.Kind, f.Detail)
		}
		w.Flush()
	}

	if broken == 0 {
		if !*quiet {
			fmt.Printf("\n%d goal(s), %d event(s): chain intact over %d",
				len(goalIDs), events, chained)
			if unchained > 0 {
				// Said every time rather than only when asked. An audit that
				// quietly excludes rows it cannot attest to is reporting a
				// stronger result than it has.
				fmt.Printf(", %d written before the chain existed and cannot be attested", unchained)
			}
			fmt.Println()
		}
		return nil
	}

	// A non-error return with a message would exit 0, and this command exists to
	// be believed by a script.
	//
	// STATE_CORRUPT, not INVARIANT_VIOLATED. The latter's registry remedy is
	// "report this, it indicates a logic defect, not a user error" — which is
	// false here and actively misleading: the code did not misbehave, a stored
	// record did not survive being read back. STATE_CORRUPT's remedy is
	// "quarantine the named record and inspect it; do not let the agent resume
	// against corrupt state", which is the correct instruction.
	return errs.New(op, errs.CodeStateCorrupt).
		WithDetail("%d of %d goal(s) have a timeline that does not verify. "+
			"Findings are printed above; the first one names the earliest altered row. "+
			"Do not repair it — preserve it, and see PRD SAF-07.", broken, len(goalIDs))
}
