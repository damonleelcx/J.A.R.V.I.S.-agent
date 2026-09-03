package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The operator's view of geometry (PRD VIS-04, VIS-05).
//
// The workbench is where variants are LOOKED at. This is where they are
// accounted for: which shapes a project has accumulated, what each one rests on,
// and what leaves the building when somebody exports one.
//
// There is deliberately no `geometry save` here. Geometry is written by the
// server at the moment it is produced, because that is the only place that knows
// the prompt, the model and the shape together — a command-line save would be
// somebody typing in their own provenance.

func geometryService(ctx context.Context, cfg *config.Config, log *logx.Logger) (*geometry.Service, *db.Pool, error) {
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return nil, nil, err
	}
	return geometry.NewService(pool, clock.System{}, log), pool, nil
}

// cmdGeometryFormats prints what this build can and cannot write.
//
// No database: the question is about this deployment. The unavailable formats
// are printed with the available ones because "STEP is missing from the list" and
// "STEP cannot be produced here and this is why" are read very differently.
func cmdGeometryFormats() error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FORMAT\tKIND\tAVAILABLE\tWHAT IT IS")
	for _, f := range geometry.Formats() {
		available := "yes"
		if !f.Available {
			available = "NO"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", f.Name, f.Kind, available, f.Gloss)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, f := range geometry.Formats() {
		if f.Available {
			continue
		}
		fmt.Printf("\n%s is refused:\n  %s\n", f.Name, wrapAt(f.Reason, 76, "  "))
	}
	return nil
}

// cmdGeometryList prints a project's variants, newest first.
func cmdGeometryList(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	fs := newFlagSet("geometry list")
	project := fs.String("project", "", "project id (required)")
	limit := fs.Int("limit", 50, "how many variants to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errs.New("forgectl.geometry", errs.CodeValidationFailed).
			WithDetail("--project is required; geometry is project-scoped")
	}
	svc, pool, err := geometryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	variants, err := svc.List(ctx, *project, *limit)
	if err != nil {
		return err
	}
	if len(variants) == 0 {
		fmt.Println("No geometry in this project yet. Variants are kept when the workbench proposes a shape.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VERSION ID\tASSEMBLY\tV\tPARTS\tUNITS\tVERIFIED\tDECIDED\tGENERATOR")
	for _, v := range variants {
		units := string(v.Units)
		if units == "" {
			// Never blank. A blank cell reads as "millimetres, obviously" to
			// anyone scanning a column of millimetres.
			units = "NOT STATED"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n",
			v.VersionID, v.Name, v.Version, len(v.Document.Parts), units,
			v.Verification, v.Disposition, v.Generator)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println("\nVERIFIED is what a machine found; DECIDED is what a person decided. They are never the same fact.")
	fmt.Printf("Compare any two:  forgectl geometry compare <version-id> <version-id>\n")
	return nil
}

// cmdGeometryShow prints one variant with VIS-04's six facts.
func cmdGeometryShow(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	if len(args) == 0 {
		return errs.New("forgectl.geometry", errs.CodeValidationFailed).
			WithDetail("geometry show needs a version id. List them with `forgectl geometry list --project <id>`.")
	}
	svc, pool, err := geometryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	v, err := svc.Find(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Printf("%s — %s v%d\n\n", v.Name, v.Path, v.Version)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	// The six VIS-04 requires every render to link to, named as the requirement
	// names them so somebody holding the PRD can tick them off.
	fmt.Fprintf(w, "geometry version\t%s (%s)\n", v.VersionID, v.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"))
	fmt.Fprintf(w, "inputs\t%s\n", string(v.Inputs))
	units := string(v.Units)
	if note := v.UnitsNote(); note != "" {
		units = "NOT STATED — " + note
	}
	fmt.Fprintf(w, "units\t%s\n", units)
	fmt.Fprintf(w, "frame\t%s\n", v.Frame)
	fmt.Fprintf(w, "generator\t%s (agent: %s)\n", v.Generator, v.Agent)
	fmt.Fprintf(w, "verification\t%s — what a machine found\n", v.Verification)
	fmt.Fprintf(w, "disposition\t%s — what a person decided\n", v.Disposition)
	fmt.Fprintf(w, "initiator\t%s\n", v.InitiatorID)
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println("\nPARTS")
	pw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(pw, "  ID\tNAME\tSHAPE\tDIMENSIONS")
	for _, p := range v.Document.Parts {
		fmt.Fprintf(pw, "  %s\t%s\t%s\t%s\n", p.ID, p.Label(), p.Shape, geometry.Dimensions(p, v.Units))
	}
	if err := pw.Flush(); err != nil {
		return err
	}

	printList("ASSUMED, NOT SPECIFIED", v.Assumptions(),
		"Nothing was recorded as assumed.")
	printList("THIS RENDER DOES NOT ESTABLISH", v.NotVerified(),
		"Nothing was recorded, which should be impossible — VIS-06 refuses geometry without it.")
	return nil
}

// cmdGeometryCompare prints several variants side by side.
func cmdGeometryCompare(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	if len(args) < 2 {
		return errs.New("forgectl.geometry", errs.CodeValidationFailed).
			WithDetail("geometry compare needs at least two version ids")
	}
	svc, pool, err := geometryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	cmp, err := svc.Compare(ctx, args)
	if err != nil {
		return err
	}
	columns := make([]string, 0, len(cmp.Variants))
	for i, v := range cmp.Variants {
		columns = append(columns, fmt.Sprintf("%d: %s v%d", i+1, v.Name, v.Version))
	}
	fmt.Printf("Comparing %s\n\n", strings.Join(columns, "   "))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	header := "FACT"
	for i := range cmp.Variants {
		header += fmt.Sprintf("\t%d", i+1)
	}
	fmt.Fprintln(w, header+"\t")
	for _, row := range cmp.Provenance {
		// A leading marker rather than colour: this output goes into terminals,
		// pipes and pasted incident notes, and colour survives none of them.
		mark := "  "
		if row.Differs {
			mark = "≠ "
		}
		line := mark + row.Field
		for _, val := range row.Values {
			line += "\t" + val
		}
		fmt.Fprintln(w, line+"\t")
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println("\nPARTS")
	pw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, p := range cmp.Parts {
		mark := "  "
		if p.Differs() {
			mark = "≠ "
		}
		label := p.Label
		if p.MatchedBy == geometry.MatchByName {
			// A guess, marked. Nothing keeps a part's id stable between turns,
			// so a row joined by name may not be one part.
			label += " (matched by name)"
		}
		line := mark + label
		for _, c := range p.Cells {
			if !c.Present {
				line += "\tABSENT"
				continue
			}
			line += "\t" + c.Shape + " " + c.Dimensions
		}
		fmt.Fprintln(pw, line+"\t")
	}
	if err := pw.Flush(); err != nil {
		return err
	}

	for _, p := range cmp.Parts {
		if len(p.Differences) == 0 {
			continue
		}
		fmt.Printf("\n%s:\n", p.Label)
		for _, d := range p.Differences {
			fmt.Printf("  - %s\n", d)
		}
	}
	// Three lists, never folded together. "These differ" is a finding; "these
	// could not be compared" is a judgement withheld; "these were matched by
	// name" is a judgement qualified.
	printList("MATCHED BY NAME, NOT BY IDENTITY", cmp.MatchNotes, "")
	printList("NOT COMPARED", cmp.NotComparable, "")
	return nil
}

// exportArgs is what `geometry export` was asked to do.
type exportArgs struct {
	VersionID string
	Format    string
	Out       string
	DryRun    bool
}

// parseExportArgs reads `<version-id> [flags]`.
//
// # Why the id is taken before parsing
//
// Go's flag package stops at the first non-flag argument. Parsing the whole
// slice would silently ignore every flag AFTER the id, and the command would
// then refuse for want of an option that was on the command line — the worst
// version of the failure, because it sends the reader to inspect their own
// typing. This is the third command in this binary to have that shape; the first
// two shipped the bug (docs/bugfix/2026-09-02-forgectl-memory-forget-ignored-its-flags.md).
//
// Split out from the command so it can be tested. Until now nothing in the suite
// invoked the CLI's argument parsing at all, which is why the same mistake was
// available a third time.
func parseExportArgs(args []string) (exportArgs, error) {
	const op = "forgectl.cmdGeometryExport"

	fs := newFlagSet("geometry export")
	format := fs.String("format", "obj", "obj or stl; `forgectl geometry formats` lists them all")
	out := fs.String("out", "", "file to write (default: the variant's own name in the current directory)")
	dryRun := fs.Bool("dry-run", false, "print the conversion label and write nothing")

	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return exportArgs{}, errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl geometry export <version-id> [--format obj|stl] [--out file] [--dry-run]. " +
				"The version id comes first. List them with `forgectl geometry list --project <id>`.")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return exportArgs{}, err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return exportArgs{}, errs.New(op, errs.CodeValidationFailed).
			WithDetail("export takes one version id; %q was also given. Export each variant separately, "+
				"or compare them with `forgectl geometry compare`.", rest[0])
	}
	return exportArgs{VersionID: args[0], Format: *format, Out: *out, DryRun: *dryRun}, nil
}

// cmdGeometryAdopt brings an earlier variant forward so it can be ruled on.
//
// The id comes first and the flags after it — Go's flag package stops at the
// first non-flag argument, so parsing the whole slice would silently ignore the
// --as this command then demands. Same shape as `approve` and `memory forget`,
// and fenced by TestParseAdoptArgs_FlagsAfterTheIDAreStillRead.
func cmdGeometryAdopt(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	opts, err := parseAdoptArgs(args)
	if err != nil {
		return err
	}
	svc, pool, err := geometryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	adopted, err := svc.Adopt(ctx, opts.VersionID, opts.As, opts.Reason)
	if err != nil {
		return err
	}
	fmt.Printf("%s v%d now carries the geometry from %s.\n", adopted.Path, adopted.Version, opts.VersionID)
	fmt.Printf("  version id  %s\n", adopted.VersionID)
	fmt.Println()
	fmt.Println("Nothing has been accepted. Adopting PROPOSES the earlier shape again; ruling on it")
	fmt.Println("is the separate act, and it is what the comparison was for:")
	fmt.Printf("  POST /v1/workspace/versions/%s/disposition\n", adopted.VersionID)
	return nil
}

// adoptArgs is what `geometry adopt` was asked to do.
type adoptArgs struct {
	VersionID string
	As        string
	Reason    string
}

func parseAdoptArgs(args []string) (adoptArgs, error) {
	const op = "forgectl.cmdGeometryAdopt"

	fs := newFlagSet("geometry adopt")
	as := fs.String("as", "", "the user id choosing this variant (required)")
	reason := fs.String("reason", "", "why this one")

	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return adoptArgs{}, errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl geometry adopt <version-id> --as <user-id> [--reason ...]. " +
				"The version id comes first.")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return adoptArgs{}, err
	}
	if strings.TrimSpace(*as) == "" {
		return adoptArgs{}, errs.New(op, errs.CodeValidationFailed).
			WithDetail("--as is required: a design nobody chose has no authority behind it")
	}
	return adoptArgs{VersionID: args[0], As: *as, Reason: *reason}, nil
}

// cmdGeometryExport writes a variant to a file, printing what the conversion
// lost before it says where the file went.
//
// The order is deliberate: a label printed after the path is a label somebody
// scrolls past on their way to copying the filename.
func cmdGeometryExport(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	opts, err := parseExportArgs(args)
	if err != nil {
		return err
	}
	svc, pool, err := geometryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	v, err := svc.Find(ctx, opts.VersionID)
	if err != nil {
		return err
	}
	res, err := geometry.Export(v, opts.Format)
	if err != nil {
		return err
	}
	printExportLabel(res)

	if opts.DryRun {
		fmt.Printf("\n--dry-run: nothing written. %d triangles, %d bytes would go to %s\n",
			res.Triangles, len(res.Content), res.Filename)
		return nil
	}
	path := opts.Out
	if path == "" {
		path = res.Filename
	}
	if err := os.WriteFile(path, res.Content, 0o644); err != nil {
		return errs.Wrap("forgectl.geometry", errs.CodeInternal, err).
			WithDetail("could not write %s", path)
	}
	abs, _ := filepath.Abs(path)
	fmt.Printf("\nWrote %d triangles to %s (%d bytes).\n", res.Triangles, abs, len(res.Content))
	return nil
}

func printExportLabel(res *geometry.Result) {
	l := res.Label
	fmt.Printf("%s\n\n", l.Headline())
	fmt.Printf("Format:       %s (%s)\n", l.Format, l.FormatKind)
	fmt.Printf("Units:        %s\n", l.Units)
	fmt.Printf("Generator:    %s\n", l.Generator)
	fmt.Printf("Verification: %s (machine)   Disposition: %s (person)\n", l.Verification, l.Disposition)

	if len(l.Tessellation) > 0 {
		fmt.Println("\nTESSELLATION — every curve below is flat faces in the file:")
		for _, d := range l.Tessellation {
			fmt.Printf("  - %s (%s): %d faces; the exported surface lies up to %s inside the one described.\n",
				d.Label, d.Shape, d.Segments, d.Max)
		}
	}
	printList("INFERRED — decided by FORGE because the description did not say", l.Inference, "")
	printList("LOST IN THIS CONVERSION", l.Lossy, "")
	printList("ASSUMED, NOT SPECIFIED", l.Assumptions, "")
	printList("THIS FILE DOES NOT ESTABLISH", l.NotVerified, "")
}

// printList prints a titled list, or a stated absence.
//
// The empty case is a sentence rather than nothing: a heading with no bullets
// reads as a rendering bug, and silence about assumptions reads as "there were
// none" whether or not that is true.
func printList(title string, items []string, whenEmpty string) {
	if len(items) == 0 {
		if whenEmpty != "" {
			fmt.Printf("\n%s\n  %s\n", title, whenEmpty)
		}
		return
	}
	fmt.Printf("\n%s\n", title)
	for _, i := range items {
		fmt.Printf("  - %s\n", i)
	}
}

// wrapAt breaks a paragraph so a terminal does not have to.
func wrapAt(s string, width int, indent string) string {
	var lines []string
	var line string
	for _, word := range strings.Fields(s) {
		if line == "" {
			line = word
			continue
		}
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"+indent)
}
