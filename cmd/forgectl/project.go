package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"time"
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
//
// # Update (2026-09-04): one of these IS on the API now
//
// `project review-authority` has an HTTP equivalent —
// PUT/DELETE /v1/projects/{id}/review-authority — because it is the only control
// here that widens what may be done, and terminal-only meant it could be
// exercised by whoever had shell access and by nobody else, while the people
// accountable for the work are in the browser looking at the refusal.
//
// The reasoning above still holds for `project character`: it changes how FORGE
// talks, and nothing about it is urgent to somebody without a terminal. The two
// commands are not the same kind of act and do not have to make the same choice.

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

// The industry a project works in (PRD §"Domain packs").
//
// # Why this is a command rather than a field on project creation
//
// There is no `project new`. Projects are created as a side effect of the first
// goal — `forgectl goal new --industry ...` — because a project with no work in
// it is a row nobody asked for. So this command exists for the other two things
// a person needs: seeing which rules are in force, and changing them when the
// first goal filed the project under the wrong domain or under "Other".
//
// Changing it is deliberate and says what it changed, because the industry
// decides the ceiling on what may be done in the project. Silently re-filing
// somebody's work under different rules is the failure this whole area exists to
// prevent.

// industryChoices renders the selector's list for flag help and error text.
//
// Built from pack.Industries() rather than written out, because this is the same
// list the product offers and a second copy would be the one that goes stale.
func industryChoices() string {
	var out []string
	for _, d := range pack.Industries() {
		out = append(out, string(d.Pack))
	}
	return strings.Join(out, " | ")
}

// describeIndustry names an industry the way a person picked it, for output.
func describeIndustry(given string) string {
	d, ok := pack.Lookup(given)
	if !ok {
		// Unresolvable values never reach here — EnsureProject refuses them — so
		// this is the empty case: nothing was stated and `general` was used.
		d, _ = pack.Lookup(string(pack.General))
	}
	if d.Industry != "" {
		return fmt.Sprintf("%s (%s)", d.Industry, d.Pack)
	}
	return string(d.Pack)
}

// cmdProjectIndustry shows or changes the industry a project works in.
func cmdProjectIndustry(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdProjectIndustry"

	fs := newFlagSet("project industry")
	project := fs.String("project", "", "project id (required)")
	set := fs.String("set", "", "change the industry to: "+industryChoices())
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl project industry --project <id> [--set <industry>]\n"+
				"With no --set, this prints the industry in force and what it permits.\n"+
				"Industries: %s", industryChoices())
	}

	// Validated BEFORE the database is touched, so a typo costs nothing and the
	// error names the list. The value written is the canonical pack id for the
	// reason EnsureProject writes one: the column has no constraint, and a domain
	// spelled three ways is a source of truth that has stopped being single.
	var target pack.Definition
	if *set != "" {
		d, ok := pack.Lookup(*set)
		if !ok {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("%q is not an industry this build knows.\nIndustries: %s",
					*set, industryChoices())
		}
		if !d.Available() {
			return errs.New(op, errs.CodeForbidden).
				WithDetail("no project may be worked in the %s pack.\n\n%s\n\nIt would require %s.",
					d.Pack, d.Summary, d.Requires)
		}
		target = d
	}

	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if *set != "" {
		tag, err := pool.Exec(ctx,
			`update forge_projects set pack = $2, updated_at = now() where id = $1`,
			*project, string(target.Pack))
		if err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		if tag.RowsAffected() == 0 {
			return errs.New(op, errs.CodeNotFound).
				WithDetail("no project %s. Nothing was changed.", *project)
		}
	}

	var name, stored string
	if err := pool.QueryRow(ctx,
		`select name, pack from forge_projects where id = $1`, *project).Scan(&name, &stored); err != nil {
		return errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no project %s", *project)
	}
	d, known := pack.Lookup(stored)

	fmt.Printf("%s (%s)\n", name, *project)
	fmt.Printf("  industry   %s\n", describeIndustry(stored))
	if !known {
		// Pre-dates the closed set, or was written by something that bypassed
		// EnsureProject. Said plainly rather than rendered as though it selected
		// rules, because it selects none.
		fmt.Printf("\nThis build does not recognise %q, so NO pack rules are in force here.\n"+
			"Set one with --set <industry>. Industries: %s\n", stored, industryChoices())
		return nil
	}
	fmt.Printf("  ceiling    %s\n", d.MaxTier)
	if d.GeometryUnit != "" {
		fmt.Printf("  geometry   %s; %s\n", d.GeometryUnit, d.GeometryAxes)
	}
	fmt.Printf("\n%s\n", d.Summary)
	if d.DataRules != "" {
		fmt.Printf("\nHandling: %s\n", d.DataRules)
	}
	// What the domain needs and this build does not have, named together. The
	// two are separate facts and stating only the first would read as a promise.
	if len(d.Adapters) > 0 {
		fmt.Printf("\nWork in this domain typically needs: %s\n", strings.Join(d.Adapters, ", "))
		fmt.Println("None of them is available in this build. Results that would come from " +
			"them cannot be produced here, and will not be simulated.")
	}
	fmt.Printf("\nWork above %s here would require %s.\n", d.MaxTier, d.Requires)
	return nil
}

// cmdProjectReviewAuthority names the person a raised ceiling rests on.
//
// # What this command can and cannot do
//
// Every engineering pack stops at r1 — reversible draft — because this build
// implements none of the qualified review that r2 and above require in those
// domains. Correct, and a dead end for a deployment that DOES have a licensed
// engineer: there was no way to say so.
//
// This is that way, and it is deliberately blunt about its limits. It cannot
// verify a licence: there is no registry to consult from in here and no
// credential to validate. What it writes is a CLAIM with an author, and every
// line it prints says so. What that buys is real — work above r1 becomes
// traceable to a named person who accepted responsibility, which is what PRD
// AGT-07 asks of any consequential transition — but it is not verification and
// must never be printed as though it were.
//
// Clearing is as easy as setting. A mechanism that raises a ceiling and cannot
// lower it is one nobody should switch on.
func cmdProjectReviewAuthority(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdProjectReviewAuthority"

	fs := newFlagSet("project review-authority")
	project := fs.String("project", "", "project id (required)")
	holder := fs.String("holder", "", "the named person accountable in this domain")
	note := fs.String("note", "", "what they hold: a registration number, a role, a scope")
	as := fs.String("as", "", "user id of whoever is recording this (required with --holder)")
	clear := fs.Bool("clear", false, "remove the recorded authority and lower the ceiling again")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Only the project is required. With neither --holder nor --clear this is a
	// READ, which is what somebody reaches for first: the usage text promises
	// that, and a guard demanding --holder would have made the promise false.
	if *project == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl project review-authority --project <id> \\\n" +
				"          --holder \"<name>\" [--note \"<registration or role>\"] --as <user-id>\n" +
				"       forgectl project review-authority --project <id> --clear\n\n" +
				"With neither, this prints what is recorded.\n\n" +
				"This does NOT verify anything. It records a claim, attributed to whoever " +
				"made it, and raises the domain's ceiling because a named person accepted " +
				"responsibility — not because a qualification was established.")
	}

	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	svc := workspace.NewService(pool, clock.System{}, log)

	switch {
	case *clear:
		if err := svc.RecordReviewAuthority(ctx, pool, *project, "", "", ""); err != nil {
			return err
		}
		fmt.Println("Cleared. This project's ceiling is the domain's ordinary one again.")
	case *holder != "":
		if *as == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("--as is required: a raised ceiling rests on an attributed statement. " +
					"An anonymous one would let work above the ordinary limit happen on the " +
					"strength of a value with no author.")
		}
		if err := svc.RecordReviewAuthority(ctx, pool, *project, *holder, *note, *as); err != nil {
			return err
		}
	}

	a, err := svc.ReviewAuthorityFor(ctx, pool, *project)
	if err != nil {
		return err
	}
	d, err := svc.PackFor(ctx, pool, *project)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", describeIndustry(string(d.Pack)))
	if !a.Recorded() {
		fmt.Printf("  authority  none recorded\n  ceiling    %s\n", d.MaxTier)
		if d.ReviewAuthority == "" {
			fmt.Printf("\nNothing raises the ceiling in this domain. %s\n", d.Requires)
			return nil
		}
		fmt.Printf("\nRecording %s would raise it to %s.\n", d.ReviewAuthority, d.ReviewCeiling)
		return nil
	}
	fmt.Printf("  authority  %s\n", a.Holder)
	if a.Note != "" {
		fmt.Printf("  holding    %s\n", a.Note)
	}
	fmt.Printf("  recorded   by %s at %s\n", a.RecordedBy, a.RecordedAt.UTC().Format(time.RFC3339))
	fmt.Printf("  ceiling    %s (raised from %s)\n", d.CeilingWith(true), d.MaxTier)
	// Said every time, in these words. A raised ceiling that does not state what
	// was NOT established is a way to launder authority nothing checked.
	fmt.Printf("\nRECORDED, NOT VERIFIED. This build cannot check a qualification: there is no\n" +
		"registry to consult and no credential to validate. What is stored is a claim,\n" +
		"attributed to whoever made it, and the ceiling rose because a named person\n" +
		"accepted responsibility for the work — not because a licence was established.\n")
	fmt.Printf("\nThe domain asks for %s.\n", d.ReviewAuthority)
	return nil
}
