package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Setting how FORGE works on a project (PRD RSN-04).
//
// # Why this is here rather than in the HTTP API
//
// There is no project API. Projects are not created, renamed or configured over
// HTTP anywhere in this codebase, and adding a settings endpoint would mean
// inventing that surface for one pair of fields. Project roles are administered
// here — `forgectl access grant` — and this is the same kind of act by the same
// kind of person.
//
// If a project API ever exists, this belongs on it and this command should call
// it rather than the database.

// critiqueValues and verbosityValues mirror the CHECK constraints in
// 0014_project_character.sql.
//
// Duplicated deliberately, so the command can say what it accepts before it
// tries. The database is still the authority — a value that got past this list
// is refused by the constraint rather than written — and the fence
// TestProjectCharacterValuesMatchTheSchema asserts the two agree, because a list
// here that drifted from the schema would reject values that are legal or
// promise ones that are not.
var (
	critiqueValues  = []string{"low", "normal", "high"}
	verbosityValues = []string{"terse", "normal", "explanatory"}
)

func oneOf(v string, allowed []string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// cmdProjectCharacter shows or sets how FORGE argues and explains on a project.
func cmdProjectCharacter(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdProjectCharacter"

	fs := newFlagSet("project character")
	project := fs.String("project", "", "project id (required)")
	critique := fs.String("critique", "", "how hard FORGE argues: "+strings.Join(critiqueValues, "|"))
	verbosity := fs.String("verbosity", "", "how much FORGE explains: "+strings.Join(verbosityValues, "|"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl project character --project <id> " +
				"[--critique low|normal|high] [--verbosity terse|normal|explanatory]\n" +
				"With no setting given, this prints the project's current character.")
	}
	if *critique != "" && !oneOf(*critique, critiqueValues) {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("--critique must be one of: %s. Got %q.\n"+
				"Note that none of them disables safety: safety-relevant objections are an "+
				"immutable commitment in the soul and no setting here relaxes them.",
				strings.Join(critiqueValues, ", "), *critique)
	}
	if *verbosity != "" && !oneOf(*verbosity, verbosityValues) {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("--verbosity must be one of: %s. Got %q.",
				strings.Join(verbosityValues, ", "), *verbosity)
	}

	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Written with COALESCE so an omitted flag leaves that field alone. Setting
	// one of the two must not silently reset the other to its default, which is
	// what a plain UPDATE of both columns would do.
	if *critique != "" || *verbosity != "" {
		tag, err := pool.Exec(ctx, `
			update forge_projects
			   set critique_intensity = coalesce(nullif($2, ''), critique_intensity),
			       verbosity          = coalesce(nullif($3, ''), verbosity),
			       updated_at         = now()
			 where id = $1`, *project, *critique, *verbosity)
		if err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		if tag.RowsAffected() == 0 {
			return errs.New(op, errs.CodeNotFound).
				WithDetail("no project %s. Nothing was changed.", *project)
		}
	}

	var gotCritique, gotVerbosity, name string
	if err := pool.QueryRow(ctx,
		`select name, critique_intensity, verbosity from forge_projects where id = $1`, *project).
		Scan(&name, &gotCritique, &gotVerbosity); err != nil {
		return errs.Wrap(op, errs.CodeNotFound, err).
			WithDetail("no project %s", *project)
	}

	fmt.Printf("%s (%s)\n", name, *project)
	fmt.Printf("  critique   %s\n", gotCritique)
	fmt.Printf("  verbosity  %s\n", gotVerbosity)
	if gotCritique == "high" {
		fmt.Println("\nFORGE will argue against plans on this project, including yours.")
	}
	// Said every time rather than only when it is turned down, because the thing
	// worth knowing is that this setting cannot reach safety at all.
	fmt.Println("\nSafety-relevant objections are always raised in full; no value here changes that.")
	return nil
}
