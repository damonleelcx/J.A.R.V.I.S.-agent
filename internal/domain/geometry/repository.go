package geometry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Repository is the geometry table.
//
// Every read joins forge_geometry to its version and artifact rather than
// copying those columns down. VIS-04's six facts are spread across three tables
// on purpose — see migration 0011 — and denormalising the verification state
// into the geometry row would create a second answer to "has this been checked",
// which is the class of bug SAF-05 exists to prevent.
type Repository struct{}

// NewRepository returns the geometry repository.
func NewRepository() *Repository { return &Repository{} }

// selectVariant is the one projection every read uses.
//
// One string rather than three nearly-identical queries: the join is the part
// that is easy to get subtly wrong, and a second copy of it is a second place
// for a column to go missing without anything failing.
const selectVariant = `
select g.version_id, g.name, g.document, g.units, g.units_declared, g.frame,
       g.generator, g.created_at,
       v.artifact_id, v.version, v.initiator_id, v.agent, v.inputs,
       v.verification_state, v.verification_note,
       v.human_disposition, v.dispositioned_by,
       a.project_id, a.path
  from forge_geometry g
  join forge_artifact_versions v on v.id = g.version_id
  join forge_artifacts a         on a.id = v.artifact_id
`

func scanVariant(row pgx.Row) (*Variant, error) {
	const op = "geometry.scanVariant"

	var v Variant
	var doc []byte
	var units, frame, agent, verification, disposition string
	if err := row.Scan(
		&v.VersionID, &v.Name, &doc, &units, &v.UnitsDeclared, &frame,
		&v.Generator, &v.CreatedAt,
		&v.ArtifactID, &v.Version, &v.InitiatorID, &agent, &v.Inputs,
		&verification, &v.VerificationNote,
		&disposition, &v.DispositionedBy,
		&v.ProjectID, &v.Path,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.New(op, errs.CodeNotFound).WithDetail("no such geometry variant")
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := json.Unmarshal(doc, &v.Document); err != nil {
		// A document that will not parse is a variant that cannot be drawn,
		// compared, or exported. Reported as corrupt state rather than returned
		// half-populated: a Variant with no parts renders as an empty viewport,
		// which reads as "FORGE proposed nothing" instead of "this row is
		// damaged".
		return nil, errs.Wrap(op, errs.CodeStateCorrupt, err).
			WithDetail("the geometry stored for version %s is not readable as a document", v.VersionID)
	}
	v.Units = Unit(units)
	v.Frame = Frame(frame)
	v.Agent = workspace.Agent(agent)
	v.Verification = workspace.Verification(verification)
	v.Disposition = workspace.Disposition(disposition)
	return &v, nil
}

// Insert writes the geometry for a version that already exists in this
// transaction.
func (r *Repository) Insert(ctx context.Context, q db.Querier, v *Variant) error {
	const op = "geometry.Repository.Insert"

	doc, err := json.Marshal(v.Document)
	if err != nil {
		return errs.Wrap(op, errs.CodeSerializationFail, err).
			WithDetail("the geometry for %q cannot be encoded as JSON", v.Name)
	}
	_, err = q.Exec(ctx, `
		insert into forge_geometry
		    (version_id, name, document, units, units_declared, frame, generator, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`,
		v.VersionID, v.Name, doc, string(v.Units), v.UnitsDeclared,
		string(v.Frame), v.Generator, v.CreatedAt)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err).
			WithDetail("the geometry for version %s could not be written", v.VersionID)
	}
	return nil
}

// Find returns one variant by its version id.
func (r *Repository) Find(ctx context.Context, q db.Querier, versionID string) (*Variant, error) {
	return scanVariant(q.QueryRow(ctx, selectVariant+` where g.version_id = $1`, versionID))
}

// ListByProject returns a project's variants, newest first.
func (r *Repository) ListByProject(ctx context.Context, q db.Querier, projectID string, limit int) ([]Variant, error) {
	const op = "geometry.Repository.ListByProject"

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.Query(ctx, selectVariant+`
		 where a.project_id = $1
		 order by g.created_at desc, v.version desc
		 limit $2`, projectID, limit)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()
	return collect(rows)
}

// FindMany returns the named variants, in the order they were ASKED FOR.
//
// The order matters and is the caller's, not the database's: a side-by-side view
// puts variants in the columns the person named them in, and re-sorting them by
// creation time would silently rearrange the comparison they asked to see.
func (r *Repository) FindMany(ctx context.Context, q db.Querier, versionIDs []string) ([]Variant, error) {
	const op = "geometry.Repository.FindMany"

	if len(versionIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, selectVariant+` where g.version_id = any($1)`, versionIDs)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()
	found, err := collect(rows)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Variant, len(found))
	for _, v := range found {
		byID[v.VersionID] = v
	}
	out := make([]Variant, 0, len(versionIDs))
	var missing []string
	for _, want := range versionIDs {
		v, ok := byID[want]
		if !ok {
			missing = append(missing, want)
			continue
		}
		out = append(out, v)
	}
	if len(missing) > 0 {
		// Named and absent is an error, not a shorter list. A comparison that
		// quietly drops a column shows the person a side-by-side of two things
		// when they asked about three, and nothing on the screen says so.
		return nil, errs.New(op, errs.CodeNotFound).
			WithDetail("no geometry variant for %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func collect(rows pgx.Rows) ([]Variant, error) {
	var out []Variant
	for rows.Next() {
		v, err := scanVariant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap("geometry.collect", errs.CodeDatabaseUnavail, err)
	}
	return out, nil
}

// artifactPath is where a named assembly's versions accumulate.
//
// # Why the name decides the path
//
// Successive proposals of the same thing have to land on the same artifact, or
// "make it taller" produces an unrelated row instead of version 2 and there is
// no history to compare. Deriving the path from the assembly's name does that
// with no state to keep and nothing for a client to assert.
//
// The cost, stated: a model that renames slightly — "NEMA 17 bracket" then
// "NEMA-17 motor bracket" — starts a second history. That is visible and
// harmless, because comparison takes arbitrary version ids and can span
// artifacts. The alternative, threading a variant id through the conversation,
// puts a piece of state in the client that the client would then be trusted to
// report back honestly.
func artifactPath(name string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '-'
	}, name)
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		// Every artifact needs a path and the schema refuses a blank one. A name
		// of nothing but punctuation is rare and must not become an error the
		// person sees on a shape that is otherwise fine.
		slug = "unnamed"
	}
	if len(slug) > 80 {
		slug = strings.Trim(slug[:80], "-")
	}
	return fmt.Sprintf("geometry/%s.forge.json", slug)
}
