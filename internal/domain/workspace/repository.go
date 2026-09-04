package workspace

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Repository is the persistence port for the workspace model.
//
// Every method takes a db.Querier rather than holding a pool, so the same code
// runs inside or outside a transaction. Creating an edge needs that: it reads
// both endpoints to check the pairing and then writes, and those must not
// interleave with a concurrent delete of an endpoint.
type Repository struct{}

// NewRepository returns the Postgres implementation.
func NewRepository() *Repository { return &Repository{} }

const nodeColumns = `id, project_id, kind, title, body, how, source, status,
	goal_id, decision_id, owner_id, artifact_id, created_by, created_at, updated_at`

func scanNode(row pgx.Row) (*Node, error) {
	var n Node
	var kind, how, status string
	err := row.Scan(&n.ID, &n.ProjectID, &kind, &n.Title, &n.Body, &how, &n.Source, &status,
		&n.GoalID, &n.DecisionID, &n.OwnerID, &n.ArtifactID,
		&n.CreatedBy, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, err
	}
	n.Kind, n.How, n.Status = Kind(kind), claim.Epistemic(how), Status(status)

	// All three columns carry check constraints, so an unrecognised value means
	// the constraint and this build have drifted apart. Reported rather than
	// coerced: a node silently treated as some other kind would inherit that
	// kind's epistemic permissions.
	if !n.Kind.Valid() {
		return nil, errs.New("workspace.scanNode", errs.CodeStateCorrupt).
			WithDetail("node %s is of kind %q, which this build does not recognise", n.ID, kind)
	}
	if !n.How.Valid() {
		return nil, errs.New("workspace.scanNode", errs.CodeStateCorrupt).
			WithDetail("node %s carries epistemic label %q, which this build does not recognise", n.ID, how)
	}
	if !n.Status.Valid() {
		return nil, errs.New("workspace.scanNode", errs.CodeStateCorrupt).
			WithDetail("node %s has status %q, which this build does not recognise", n.ID, status)
	}
	return &n, nil
}

// CreateNode inserts a node.
//
// The kind is fixed here and nowhere else changes it: there is deliberately no
// UpdateKind. See UpdateNode.
func (r *Repository) CreateNode(ctx context.Context, q db.Querier, n *Node) error {
	const op = "workspace.Repository.CreateNode"

	if err := n.Validate(); err != nil {
		return err
	}
	if n.CreatedAt.IsZero() || n.UpdatedAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("node %q has no timestamp; the application clock owns every timestamp in this system", n.Title)
	}
	_, err := q.Exec(ctx, `
		insert into forge_nodes
			(id, project_id, kind, title, body, how, source, status,
			 goal_id, decision_id, owner_id, artifact_id, created_by, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)`,
		n.ID, n.ProjectID, string(n.Kind), n.Title, n.Body, string(n.How), n.Source, string(n.Status),
		n.GoalID, n.DecisionID, n.OwnerID, n.ArtifactID, n.CreatedBy, n.CreatedAt)
	if err != nil {
		if isUnique(err, "forge_nodes_goal_anchor", "forge_nodes_decision_anchor",
			"forge_nodes_owner_anchor", "forge_nodes_artifact_anchor") {
			return errs.Wrap(op, errs.CodeConflict, err).
				WithDetail("this project already has a %s anchor for that row; use FindOrCreateAnchor rather than creating a second one", n.Kind)
		}
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// FindNode returns one node.
func (r *Repository) FindNode(ctx context.Context, q db.Querier, nodeID string) (*Node, error) {
	const op = "workspace.Repository.FindNode"

	n, err := scanNode(q.QueryRow(ctx, `select `+nodeColumns+` from forge_nodes where id = $1`, nodeID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no node %s", nodeID)
		}
		return nil, passThrough(op, err)
	}
	return n, nil
}

// FindAnchor returns the anchor node for an external row, or NOT_FOUND.
func (r *Repository) FindAnchor(ctx context.Context, q db.Querier, projectID string, kind Kind, refID string) (*Node, error) {
	const op = "workspace.Repository.FindAnchor"

	def, err := KindOf(kind)
	if err != nil {
		return nil, err
	}
	if def.Anchor == AnchorNone {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("%s nodes own their content; there is nothing for them to anchor", kind)
	}
	n, err := scanNode(q.QueryRow(ctx,
		`select `+nodeColumns+` from forge_nodes
		  where project_id = $1 and kind = $2 and `+string(def.Anchor)+` = $3`,
		projectID, string(kind), refID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).
				WithDetail("no %s anchor for %s in this project", kind, refID)
		}
		return nil, passThrough(op, err)
	}
	return n, nil
}

// EnsureAnchor creates an anchor node unless the project already has one, and
// returns whichever node holds that anchor.
//
// # Why this is not CreateNode with the conflict recovered
//
// CreateNode inserts and, on a unique violation, reads the winner's row. That
// is correct outside a transaction and wrong inside one: in Postgres a failed
// statement aborts the whole transaction, so the recovery read would itself
// fail with "current transaction is aborted" — and the caller would lose the
// artifact version it was in the middle of writing.
//
// ON CONFLICT DO NOTHING never fails, so it is safe in a transaction somebody
// else owns. This runs inside the one RecordChange holds.
func (r *Repository) EnsureAnchor(ctx context.Context, q db.Querier, n *Node) (*Node, error) {
	const op = "workspace.Repository.EnsureAnchor"

	if err := n.Validate(); err != nil {
		return nil, err
	}
	def, err := KindOf(n.Kind)
	if err != nil {
		return nil, err
	}
	if def.Anchor == AnchorNone {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("%s nodes own their content; there is nothing to anchor", n.Kind)
	}
	ref, _ := n.AnchorRef()

	if _, err := q.Exec(ctx, `
		insert into forge_nodes
			(id, project_id, kind, title, body, how, source, status,
			 goal_id, decision_id, owner_id, artifact_id, created_by, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)
		on conflict do nothing`,
		n.ID, n.ProjectID, string(n.Kind), n.Title, n.Body, string(n.How), n.Source, string(n.Status),
		n.GoalID, n.DecisionID, n.OwnerID, n.ArtifactID, n.CreatedBy, n.CreatedAt); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	// Read back rather than trusting the insert: on conflict nothing happened,
	// and the node that exists is the one every edge already points at.
	return r.FindAnchor(ctx, q, n.ProjectID, n.Kind, ref)
}

// UpdateNode rewrites a node's content, status and epistemic label.
//
// # Why there is no way to change a node's kind
//
// An assumption that turns out to be true does not become a requirement. The
// entire value of having labelled it an assumption is that somebody can later
// ask "what did we build on top of a guess?", and answering that means the
// assumption is still there to be found. Promotion is a NEW node with a
// derives_from edge back to the old one, which keeps both.
//
// This is the same rule as decision supersession in the memory package, and the
// same rule as the audit chain: history is added to, never rewritten.
func (r *Repository) UpdateNode(ctx context.Context, q db.Querier, n *Node) error {
	const op = "workspace.Repository.UpdateNode"

	// The kind is checked BEFORE the content, and against the stored row.
	//
	// Not merely tidier: a caller changing an assumption into a requirement
	// usually changes the epistemic label in the same breath, and validating
	// first meant they were told "a requirement cannot be assumed" — a true
	// statement about the wrong problem, which sends them to fix the label and
	// meet the real refusal on the second attempt. The first thing wrong with
	// that call is that it changes a kind, so that is what it is told.
	//
	// Found by running the thing rather than by a test: the fence for this
	// happened to change the label too, so it took the path that already worked.
	existing, err := r.FindNode(ctx, q, n.ID)
	if err != nil {
		return err
	}
	if existing.Kind != n.Kind {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("node %s is %s %s and this update would make it %s %s. A node's kind never changes: "+
				"create %s %s and draw a derives_from edge to this one, so both remain readable.",
				n.ID, articleFor(string(existing.Kind)), existing.Kind,
				articleFor(string(n.Kind)), n.Kind, articleFor(string(n.Kind)), n.Kind)
	}

	if err := n.Validate(); err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `
		update forge_nodes
		   set title = $2, body = $3, how = $4, source = $5, status = $6, updated_at = $7
		 where id = $1 and kind = $8`,
		n.ID, n.Title, n.Body, string(n.How), n.Source, string(n.Status), n.UpdatedAt, string(n.Kind))
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		// The kind was already compared above, so reaching here means the row
		// went away between the read and the write. The `and kind = $8` guard
		// stays as a second line: it is what makes a concurrent kind change
		// impossible rather than merely unlikely.
		return errs.New(op, errs.CodeNotFound).
			WithDetail("node %s was removed while it was being edited", n.ID)
	}
	return nil
}

// NodeFilter narrows a listing.
type NodeFilter struct {
	ProjectID string
	Kinds     []Kind
	Statuses  []Status
	// IDs narrows to specific nodes, and is how a caller ASKS WHICH OF THESE
	// EXIST HERE. Nodes named by a client cannot be linked or acted on until
	// they are known to be in this project, and answering that with one query
	// is what keeps the check from being skipped.
	IDs   []string
	Limit int
}

// ListNodes returns a project's nodes, newest first.
func (r *Repository) ListNodes(ctx context.Context, q db.Querier, f NodeFilter) ([]Node, error) {
	const op = "workspace.Repository.ListNodes"

	if strings.TrimSpace(f.ProjectID) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("listing nodes needs a project id; the graph is project-scoped")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
		// A lookup of named ids is answering "which of these exist", and the
		// answer must cover all of them. Left at 500 a longer list would come
		// back silently short, and the caller would read the missing rows as
		// nodes that do not exist.
		if n := len(f.IDs); n > limit {
			limit = n
		}
	}
	sql := `select ` + nodeColumns + ` from forge_nodes where project_id = $1`
	args := []any{f.ProjectID}
	if len(f.IDs) > 0 {
		args = append(args, f.IDs)
		sql += ` and id = any($` + strconv.Itoa(len(args)) + `)`
	}
	if len(f.Kinds) > 0 {
		args = append(args, kindStrings(f.Kinds))
		sql += ` and kind = any($` + strconv.Itoa(len(args)) + `)`
	}
	if len(f.Statuses) > 0 {
		args = append(args, statusStrings(f.Statuses))
		sql += ` and status = any($` + strconv.Itoa(len(args)) + `)`
	}
	args = append(args, limit)
	sql += ` order by created_at desc limit $` + strconv.Itoa(len(args))

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	out := []Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Edges
// ---------------------------------------------------------------------------

const edgeColumns = `id, project_id, kind, from_id, to_id, note, created_by, created_at`

func scanEdge(row pgx.Row) (*Edge, error) {
	var e Edge
	var kind string
	if err := row.Scan(&e.ID, &e.ProjectID, &kind, &e.FromID, &e.ToID, &e.Note,
		&e.CreatedBy, &e.CreatedAt); err != nil {
		return nil, err
	}
	e.Kind = EdgeKind(kind)
	if !e.Kind.Valid() {
		return nil, errs.New("workspace.scanEdge", errs.CodeStateCorrupt).
			WithDetail("edge %s is of kind %q, which this build does not recognise", e.ID, kind)
	}
	return &e, nil
}

// CreateEdge draws a typed relation, checking the pairing against both endpoints.
//
// The endpoints are READ rather than taken from the caller, because the pairing
// rule is about what the nodes actually are. A caller that told us the kinds
// would be telling us what it believes, and the belief is exactly the thing
// being checked.
// checkEdge resolves an edge's endpoints and refuses one the vocabulary does not
// allow. Returns the two nodes so a caller can name them.
//
// Every check here is a READ or a comparison in Go. Nothing in it can leave a
// failed statement behind, which is what lets EnsureEdge run inside a
// transaction the caller owns — see the comment there.
func (r *Repository) checkEdge(ctx context.Context, q db.Querier, e *Edge) (*Node, *Node, error) {
	const op = "workspace.Repository.checkEdge"

	def, err := EdgeKindOf(e.Kind)
	if err != nil {
		return nil, nil, err
	}
	if e.FromID == e.ToID {
		return nil, nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a node cannot %s itself", e.Kind)
	}
	from, err := r.FindNode(ctx, q, e.FromID)
	if err != nil {
		return nil, nil, err
	}
	to, err := r.FindNode(ctx, q, e.ToID)
	if err != nil {
		return nil, nil, err
	}
	if from.ProjectID != to.ProjectID {
		return nil, nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("those nodes are in different projects; the graph does not span projects")
	}
	if e.ProjectID == "" {
		e.ProjectID = from.ProjectID
	}
	if e.ProjectID != from.ProjectID {
		return nil, nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("this edge claims project %s but its endpoints are in %s", e.ProjectID, from.ProjectID)
	}
	if err := def.Permits(from.Kind, to.Kind); err != nil {
		return nil, nil, err
	}
	return from, to, nil
}

// EnsureEdge draws a relation that may already hold, inside a transaction the
// caller owns.
//
// # Why this is not CreateEdge with the conflict ignored
//
// CreateEdge inserts and recovers from the unique violation by reporting a
// conflict. That is correct on its own connection and WRONG inside a
// transaction: in Postgres a failed statement aborts the whole transaction, so
// by the time the caller decides the conflict was harmless, everything else it
// was writing is already lost. RecordChangeIn writes a version, an event and an
// anchor beside this; a second turn built from the same requirement would draw
// an edge that already exists, and would have destroyed the version it was
// recording. This is the same trap EnsureAnchor exists for, one edge over.
//
// ON CONFLICT DO NOTHING, so the statement cannot fail for a reason the caller
// would have forgiven.
func (r *Repository) EnsureEdge(ctx context.Context, q db.Querier, e *Edge) error {
	const op = "workspace.Repository.EnsureEdge"

	if _, _, err := r.checkEdge(ctx, q, e); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		insert into forge_edges (id, project_id, kind, from_id, to_id, note, created_by, created_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8)
		on conflict do nothing`,
		e.ID, e.ProjectID, string(e.Kind), e.FromID, e.ToID, e.Note, e.CreatedBy, e.CreatedAt); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

func (r *Repository) CreateEdge(ctx context.Context, q db.Querier, e *Edge) error {
	const op = "workspace.Repository.CreateEdge"

	from, to, err := r.checkEdge(ctx, q, e)
	if err != nil {
		return err
	}

	_, err = q.Exec(ctx, `
		insert into forge_edges (id, project_id, kind, from_id, to_id, note, created_by, created_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8)`,
		e.ID, e.ProjectID, string(e.Kind), e.FromID, e.ToID, e.Note, e.CreatedBy, e.CreatedAt)
	if err != nil {
		if isUnique(err, "forge_edges_unique") {
			// Not an error worth raising to a user: the relation they asked for
			// already holds. Reported as a conflict so a caller that cares can
			// tell, and harmless to ignore for one that does not.
			return errs.Wrap(op, errs.CodeConflict, err).
				WithDetail("%s already %s %s", from.Title, e.Kind, to.Title)
		}
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// DeleteEdge removes a relation. Edges are assertions about the world and the
// world changes, so unlike nodes they may be withdrawn.
func (r *Repository) DeleteEdge(ctx context.Context, q db.Querier, edgeID string) error {
	const op = "workspace.Repository.DeleteEdge"

	tag, err := q.Exec(ctx, `delete from forge_edges where id = $1`, edgeID)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeNotFound).WithDetail("no edge %s", edgeID)
	}
	return nil
}

// ListEdges returns a project's edges.
func (r *Repository) ListEdges(ctx context.Context, q db.Querier, projectID string, kinds ...EdgeKind) ([]Edge, error) {
	const op = "workspace.Repository.ListEdges"

	sql := `select ` + edgeColumns + ` from forge_edges where project_id = $1`
	args := []any{projectID}
	if len(kinds) > 0 {
		names := make([]string, 0, len(kinds))
		for _, k := range kinds {
			names = append(names, string(k))
		}
		args = append(args, names)
		sql += ` and kind = any($` + strconv.Itoa(len(args)) + `)`
	}
	sql += ` order by created_at asc`

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	out := []Edge{}
	for rows.Next() {
		e, err := scanEdge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Artifacts
// ---------------------------------------------------------------------------

const versionColumns = `id, artifact_id, version, initiator_id, agent, tool_call_id,
	inputs, diff, verification_state, verification_note,
	human_disposition, dispositioned_by, dispositioned_at, disposition_reason,
	event_id, created_at`

func scanVersion(row pgx.Row) (*Version, error) {
	var v Version
	var agent, verification, disposition string
	err := row.Scan(&v.ID, &v.ArtifactID, &v.Version, &v.InitiatorID, &agent, &v.ToolCallID,
		&v.Inputs, &v.Diff, &verification, &v.VerificationNote,
		&disposition, &v.DispositionedBy, &v.DispositionedAt, &v.DispositionReason,
		&v.EventID, &v.CreatedAt)
	if err != nil {
		return nil, err
	}
	v.Agent, v.Verification, v.Disposition = Agent(agent), Verification(verification), Disposition(disposition)
	if !v.Agent.Valid() || !v.Verification.Valid() || !v.Disposition.Valid() {
		return nil, errs.New("workspace.scanVersion", errs.CodeStateCorrupt).
			WithDetail("version %s carries agent=%q verification=%q disposition=%q; at least one is unrecognised by this build",
				v.ID, agent, verification, disposition)
	}
	return &v, nil
}

// FindOrCreateArtifact returns the artifact at a path, creating it if absent.
//
// Find-or-create rather than create, because an artifact's identity is its path
// and the second change to a file must land on the same history as the first.
// The unique index makes the race safe: the loser re-reads.
func (r *Repository) FindOrCreateArtifact(ctx context.Context, q db.Querier, a *Artifact) (*Artifact, error) {
	const op = "workspace.Repository.FindOrCreateArtifact"

	if !a.Kind.Valid() {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("artifact kind %q is not recognised", a.Kind)
	}
	if strings.TrimSpace(a.Path) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("an artifact needs a path; it is how the next change finds the same history")
	}

	existing, err := r.FindArtifactByPath(ctx, q, a.ProjectID, a.Path)
	if err == nil {
		return existing, nil
	}
	if !errs.Is(err, errs.CodeNotFound) {
		return nil, err
	}
	_, err = q.Exec(ctx, `
		insert into forge_artifacts (id, project_id, path, kind, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$5)`, a.ID, a.ProjectID, a.Path, string(a.Kind), a.CreatedAt)
	if err != nil {
		if isUnique(err, "forge_artifacts_project_id_path_key") {
			return r.FindArtifactByPath(ctx, q, a.ProjectID, a.Path)
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return a, nil
}

// FindArtifactByPath returns the artifact at a path, or NOT_FOUND.
func (r *Repository) FindArtifactByPath(ctx context.Context, q db.Querier, projectID, path string) (*Artifact, error) {
	const op = "workspace.Repository.FindArtifactByPath"

	var a Artifact
	var kind string
	err := q.QueryRow(ctx,
		`select id, project_id, path, kind, created_at, updated_at
		   from forge_artifacts where project_id = $1 and path = $2`, projectID, path).
		Scan(&a.ID, &a.ProjectID, &a.Path, &kind, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no artifact at %q", path)
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	a.Kind = ArtifactKind(kind)
	return &a, nil
}

// FindArtifact returns an artifact by id.
func (r *Repository) FindArtifact(ctx context.Context, q db.Querier, artifactID string) (*Artifact, error) {
	const op = "workspace.Repository.FindArtifact"

	var a Artifact
	var kind string
	err := q.QueryRow(ctx,
		`select id, project_id, path, kind, created_at, updated_at
		   from forge_artifacts where id = $1`, artifactID).
		Scan(&a.ID, &a.ProjectID, &a.Path, &kind, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no artifact %s", artifactID)
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	a.Kind = ArtifactKind(kind)
	return &a, nil
}

// AppendVersion records a change.
//
// The version number is allocated from the current maximum inside the caller's
// transaction, and unique (artifact_id, version) makes a race lose rather than
// produce two version 4s. Same shape as the event sequence in the engine.
//
// A previous PENDING version becomes SUPERSEDED, because a version nothing uses
// is not a decision anybody still owes. A previous ACCEPTED or REJECTED version
// is left alone: somebody ruled on it, and overwriting that would erase a human
// decision to tidy a queue.
func (r *Repository) AppendVersion(ctx context.Context, q db.Querier, v *Version) error {
	const op = "workspace.Repository.AppendVersion"

	if err := v.Validate(); err != nil {
		return err
	}
	if v.CreatedAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("version has no timestamp; the application clock owns every timestamp in this system")
	}

	var next int
	if err := q.QueryRow(ctx,
		`select coalesce(max(version), 0) + 1 from forge_artifact_versions where artifact_id = $1`,
		v.ArtifactID).Scan(&next); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	v.Version = next

	if _, err := q.Exec(ctx, `
		update forge_artifact_versions
		   set human_disposition = 'superseded'
		 where artifact_id = $1 and human_disposition = 'pending'`, v.ArtifactID); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}

	_, err := q.Exec(ctx, `
		insert into forge_artifact_versions
			(id, artifact_id, version, initiator_id, agent, tool_call_id, inputs, diff,
			 verification_state, verification_note,
			 human_disposition, dispositioned_by, dispositioned_at, disposition_reason,
			 event_id, created_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		v.ID, v.ArtifactID, v.Version, v.InitiatorID, string(v.Agent), v.ToolCallID,
		v.Inputs, v.Diff, string(v.Verification), v.VerificationNote,
		string(v.Disposition), v.DispositionedBy, v.DispositionedAt, v.DispositionReason,
		v.EventID, v.CreatedAt)
	if err != nil {
		if isUnique(err, "forge_artifact_versions_artifact_id_version_key") {
			return errs.Wrap(op, errs.CodeConflict, err).
				WithDetail("another change to this artifact landed first; re-read the current version and append again")
		}
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// CurrentVersion returns the highest version of an artifact.
//
// Derived from the version numbers rather than read from an is_current flag. A
// stored flag is a second source of truth about the same fact, and the day it
// disagrees nobody can tell which one is lying.
func (r *Repository) CurrentVersion(ctx context.Context, q db.Querier, artifactID string) (*Version, error) {
	const op = "workspace.Repository.CurrentVersion"

	v, err := scanVersion(q.QueryRow(ctx,
		`select `+versionColumns+` from forge_artifact_versions
		  where artifact_id = $1 order by version desc limit 1`, artifactID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).
				WithDetail("artifact %s has no versions", artifactID)
		}
		return nil, passThrough(op, err)
	}
	return v, nil
}

// ListVersions returns an artifact's history, newest first.
func (r *Repository) ListVersions(ctx context.Context, q db.Querier, artifactID string) ([]Version, error) {
	const op = "workspace.Repository.ListVersions"

	rows, err := q.Query(ctx,
		`select `+versionColumns+` from forge_artifact_versions
		  where artifact_id = $1 order by version desc`, artifactID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	out := []Version{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return out, nil
}

// FindVersion returns one version by id.
func (r *Repository) FindVersion(ctx context.Context, q db.Querier, versionID string) (*Version, error) {
	const op = "workspace.Repository.FindVersion"

	v, err := scanVersion(q.QueryRow(ctx,
		`select `+versionColumns+` from forge_artifact_versions where id = $1`, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no version %s", versionID)
		}
		return nil, passThrough(op, err)
	}
	return v, nil
}

// SetVerification records what a machine found.
//
// It deliberately does NOT touch the disposition. A passing test does not accept
// a change; that is the distinction the two columns exist to keep.
func (r *Repository) SetVerification(ctx context.Context, q db.Querier, versionID string, state Verification, note string) error {
	const op = "workspace.Repository.SetVerification"

	if !state.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("verification state %q is not one of unverified, passed, failed", state)
	}
	tag, err := q.Exec(ctx, `
		update forge_artifact_versions set verification_state = $2, verification_note = $3
		 where id = $1`, versionID, string(state), note)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeNotFound).WithDetail("no version %s", versionID)
	}
	return nil
}

// SetDisposition records what a person decided.
//
// The guard `human_disposition = 'pending'` makes this a one-way step. A
// decision that could be revised in place would let "we rejected that" quietly
// become "we accepted it", with nothing recording that anybody changed their
// mind. Revisiting means a new version.
func (r *Repository) SetDisposition(ctx context.Context, q db.Querier, versionID string,
	d Disposition, byUserID, reason string, at time.Time) error {
	const op = "workspace.Repository.SetDisposition"

	if d != Accepted && d != Rejected {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a person decides accepted or rejected; %q is a state the system reaches on its own", d)
	}
	if strings.TrimSpace(byUserID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a disposition must name the person who made it. There is deliberately no way to record one without (PRD SAF-05).")
	}
	tag, err := q.Exec(ctx, `
		update forge_artifact_versions
		   set human_disposition = $2, dispositioned_by = $3, dispositioned_at = $4, disposition_reason = $5
		 where id = $1 and human_disposition = 'pending'`,
		versionID, string(d), byUserID, at, reason)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		existing, findErr := r.FindVersion(ctx, q, versionID)
		if findErr != nil {
			return findErr
		}
		return errs.New(op, errs.CodeConflict).
			WithDetail("version %d is already %s and a disposition is not revised in place. "+
				"Append a new version if the decision has changed, so both are readable.",
				existing.Version, existing.Disposition)
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func kindStrings(ks []Kind) []string {
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, string(k))
	}
	return out
}

func statusStrings(ss []Status) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, string(s))
	}
	return out
}

func isUnique(err error, constraints ...string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	for _, c := range constraints {
		if pgErr.ConstraintName == c {
			return true
		}
	}
	return false
}

// passThrough keeps a domain error from scanNode/scanVersion intact and wraps
// anything else as a storage failure.
func passThrough(op string, err error) error {
	if errs.CodeOf(err) != errs.CodeInternal {
		return err
	}
	return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
}
