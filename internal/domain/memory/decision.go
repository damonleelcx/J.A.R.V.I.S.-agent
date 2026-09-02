package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The decision log (PRD MEM-03).
//
// # Why this is not the event timeline
//
// forge_events records what HAPPENED. This records what was CHOSEN, what was
// rejected, why, and on what evidence. Those do not survive in a timeline: by
// the time anyone asks "why is it built this way", the reasoning is precisely
// the part that is gone, and the timeline shows only the consequence.
//
// # Why supersession rather than editing
//
// "We changed our minds" is itself a decision, with its own date and author.
// Editing the old row would erase the fact that the old answer was once
// believed — which is the most useful thing the log holds, because it is what
// tells a reader whether a question has already been settled and reopened.

// Alternative is one option that was considered and not taken.
//
// WhyNot is required. A list of rejected options with no reasons is worse than
// no list: it looks like the alternatives were weighed when nothing records
// that they were.
type Alternative struct {
	Option string `json:"option"`
	WhyNot string `json:"why_not"`
}

// Decision is one recorded choice.
type Decision struct {
	ID        string
	ProjectID string
	// GoalID is the goal it was decided during, when there was one. Decisions
	// outlive goals, so this is optional and the project is not.
	GoalID   *string
	AuthorID string

	Title     string
	Decision  string
	Rationale string

	Alternatives []Alternative
	// Evidence carries the epistemic label per item. A decision resting on a
	// measurement and one resting on a figure recalled from model weights must
	// not read the same six months later.
	Evidence []claim.Claim
	Affected []string

	SupersedesID *string
	DecidedAt    time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// SupersededByID is derived at read time from the row that points here, not
	// stored. Storing it would put the same fact in two columns that can
	// disagree, and the one that disagrees is always the one somebody reads.
	SupersededByID *string
}

// Current reports whether this is still the operative answer.
func (d *Decision) Current() bool { return d.SupersededByID == nil }

// Validate checks a decision before it is written.
func (d *Decision) Validate() error {
	const op = "memory.Decision.Validate"

	if strings.TrimSpace(d.Title) == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("a decision needs a title")
	}
	if strings.TrimSpace(d.Decision) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("decision %q records no decision; write what was chosen, in a sentence somebody can act on", d.Title)
	}
	if strings.TrimSpace(d.ProjectID) == "" || strings.TrimSpace(d.AuthorID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("decision %q must name a project and an author; an unattributed decision cannot be questioned", d.Title)
	}
	for i, a := range d.Alternatives {
		if strings.TrimSpace(a.Option) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("alternative %d of %q has no option named", i+1, d.Title)
		}
		if strings.TrimSpace(a.WhyNot) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("alternative %q of decision %q says why it was rejected nowhere; an unexplained rejection is not a considered one",
					a.Option, d.Title)
		}
	}
	for i := range d.Evidence {
		// Same rule as everywhere else: an unlabelled claim is downgraded rather
		// than refused, and Validate reports what it changed.
		d.Evidence[i].Validate()
	}
	if d.DecidedAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("decision %q has no date; the application clock owns every timestamp in this system", d.Title)
	}
	return nil
}

const decisionColumns = `id, project_id, goal_id, author_id, title, decision, rationale,
	alternatives, evidence, affected, supersedes_id, decided_at, created_at, updated_at`

func scanDecision(row pgx.Row) (*Decision, error) {
	var d Decision
	var alternatives, evidence, affected []byte
	err := row.Scan(&d.ID, &d.ProjectID, &d.GoalID, &d.AuthorID, &d.Title, &d.Decision,
		&d.Rationale, &alternatives, &evidence, &affected, &d.SupersedesID,
		&d.DecidedAt, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	for _, u := range []struct {
		raw  []byte
		into any
		what string
	}{
		{alternatives, &d.Alternatives, "alternatives"},
		{evidence, &d.Evidence, "evidence"},
		{affected, &d.Affected, "affected artifacts"},
	} {
		if len(u.raw) == 0 {
			continue
		}
		if err := json.Unmarshal(u.raw, u.into); err != nil {
			return nil, errs.Wrap("memory.scanDecision", errs.CodeStateCorrupt, err).
				WithDetail("decision %s has %s that cannot be decoded; the row and this build disagree about its shape", d.ID, u.what)
		}
	}
	return &d, nil
}

// RecordDecision writes a decision, optionally superseding an existing one.
//
// Supersession is checked rather than assumed: the target must exist, belong to
// the same project, and not already have been superseded. The last of those is
// what keeps "what do we currently believe?" to one answer — without it two
// people could each replace the same decision and the log would hold two
// contradictory currents with nothing to choose between them. The database
// enforces the same rule with a unique index, so a race loses rather than
// splitting the chain.
func (s *Service) RecordDecision(ctx context.Context, d *Decision) (*Decision, error) {
	const op = "memory.Service.RecordDecision"

	now := s.clock.Now()
	if d.DecidedAt.IsZero() {
		d.DecidedAt = now
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	d.ID, d.CreatedAt, d.UpdatedAt = id.New(id.PrefixDecision), now, now

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if d.SupersedesID != nil {
		prior, err := s.findDecision(ctx, tx, *d.SupersedesID)
		if err != nil {
			return nil, err
		}
		if prior.ProjectID != d.ProjectID {
			return nil, errs.New(op, errs.CodeValidationFailed).
				WithDetail("decision %s belongs to another project; a decision can only be superseded within the project that made it", prior.ID)
		}
		if prior.SupersededByID != nil {
			return nil, errs.New(op, errs.CodeDecisionSuperseded).
				WithDetail("decision %q was already superseded by %s; supersede that one instead", prior.Title, *prior.SupersededByID)
		}
	}

	alternatives, _ := json.Marshal(orEmpty(d.Alternatives))
	evidence, _ := json.Marshal(orEmptyClaims(d.Evidence))
	affected, _ := json.Marshal(orEmptyStrings(d.Affected))

	if _, err := tx.Exec(ctx, `
		insert into forge_decisions
			(id, project_id, goal_id, author_id, title, decision, rationale,
			 alternatives, evidence, affected, supersedes_id, decided_at, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)`,
		d.ID, d.ProjectID, d.GoalID, d.AuthorID, d.Title, d.Decision, d.Rationale,
		alternatives, evidence, affected, d.SupersedesID, d.DecidedAt, d.CreatedAt); err != nil {
		return nil, wrapDecisionWrite(op, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}

	s.log.Info(ctx, logx.EventDecisionMade, "decision_id", d.ID, "project_id", d.ProjectID,
		"title", d.Title, "evidence_items", len(d.Evidence))
	if d.SupersedesID != nil {
		s.log.Info(ctx, logx.EventDecisionSuperseded,
			"decision_id", *d.SupersedesID, "superseded_by", d.ID)
	}
	return d, nil
}

// FindDecision returns one decision, with its successor resolved.
func (s *Service) FindDecision(ctx context.Context, decisionID string) (*Decision, error) {
	return s.findDecision(ctx, s.pool, decisionID)
}

func (s *Service) findDecision(ctx context.Context, q db.Querier, decisionID string) (*Decision, error) {
	const op = "memory.Service.findDecision"

	d, err := scanDecision(q.QueryRow(ctx,
		`select `+decisionColumns+` from forge_decisions where id = $1`, decisionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no decision %s", decisionID)
		}
		if errs.CodeOf(err) != errs.CodeInternal {
			return nil, err
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := s.resolveSuccessor(ctx, q, d); err != nil {
		return nil, err
	}
	return d, nil
}

// resolveSuccessor fills SupersededByID from the row that points here.
func (s *Service) resolveSuccessor(ctx context.Context, q db.Querier, d *Decision) error {
	var successor *string
	err := q.QueryRow(ctx,
		`select id from forge_decisions where supersedes_id = $1`, d.ID).Scan(&successor)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			d.SupersededByID = nil
			return nil
		}
		return errs.Wrap("memory.Service.resolveSuccessor", errs.CodeDatabaseUnavail, err)
	}
	d.SupersededByID = successor
	return nil
}

// DecisionFilter narrows a listing.
type DecisionFilter struct {
	ProjectID string
	GoalID    string
	// CurrentOnly hides decisions that something later replaced. The default is
	// to show both, because "this was believed and then changed" is usually what
	// the reader came for.
	CurrentOnly bool
	Limit       int
}

// ListDecisions returns a project's decisions, newest first, each knowing
// whether it is still current.
func (s *Service) ListDecisions(ctx context.Context, f DecisionFilter) ([]Decision, error) {
	const op = "memory.Service.ListDecisions"

	if strings.TrimSpace(f.ProjectID) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("listing decisions needs a project id; decisions are project-scoped")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	// The successor is resolved in the same query with a left join, so a
	// hundred decisions cost one round trip rather than a hundred and one, and
	// so "current" cannot be computed from a snapshot taken at a different
	// instant from the rows it describes.
	sql := `select ` + prefixed(decisionColumns, "d") + `, s.id
	          from forge_decisions d
	          left join forge_decisions s on s.supersedes_id = d.id
	         where d.project_id = $1`
	args := []any{f.ProjectID}
	if f.GoalID != "" {
		args = append(args, f.GoalID)
		sql += ` and d.goal_id = $2`
	}
	if f.CurrentOnly {
		sql += ` and s.id is null`
	}
	args = append(args, limit)
	sql += ` order by d.decided_at desc limit $` + strconv.Itoa(len(args))

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	out := []Decision{}
	for rows.Next() {
		var d Decision
		var alternatives, evidence, affected []byte
		var successor *string
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.GoalID, &d.AuthorID, &d.Title, &d.Decision,
			&d.Rationale, &alternatives, &evidence, &affected, &d.SupersedesID,
			&d.DecidedAt, &d.CreatedAt, &d.UpdatedAt, &successor); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		_ = json.Unmarshal(nonEmpty(alternatives), &d.Alternatives)
		_ = json.Unmarshal(nonEmpty(evidence), &d.Evidence)
		_ = json.Unmarshal(nonEmpty(affected), &d.Affected)
		d.SupersededByID = successor
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return out, nil
}

// DecisionChain returns the supersession chain containing a decision, oldest
// first, so both the answer that was believed and the one that replaced it are
// readable side by side (PRD MEM-03).
func (s *Service) DecisionChain(ctx context.Context, decisionID string) ([]Decision, error) {
	const op = "memory.Service.DecisionChain"

	d, err := s.findDecision(ctx, s.pool, decisionID)
	if err != nil {
		return nil, err
	}

	// Walk back to the head. The chain is finite by construction — supersedes_id
	// may only name a row that already existed and is never updated — but the
	// guard is here anyway: a cycle introduced by a manual UPDATE would
	// otherwise hang the process rather than report corrupt state.
	chain := []Decision{*d}
	seen := map[string]bool{d.ID: true}
	for cur := d; cur.SupersedesID != nil; {
		prior, err := s.findDecision(ctx, s.pool, *cur.SupersedesID)
		if err != nil {
			return nil, err
		}
		if seen[prior.ID] {
			return nil, errs.New(op, errs.CodeStateCorrupt).
				WithDetail("the supersession chain through decision %s contains a cycle at %s; this cannot happen through the service and means the table was edited by hand", decisionID, prior.ID)
		}
		seen[prior.ID] = true
		chain = append([]Decision{*prior}, chain...)
		cur = prior
	}
	// Then forward to the current answer.
	for cur := d; cur.SupersededByID != nil; {
		next, err := s.findDecision(ctx, s.pool, *cur.SupersededByID)
		if err != nil {
			return nil, err
		}
		if seen[next.ID] {
			return nil, errs.New(op, errs.CodeStateCorrupt).
				WithDetail("the supersession chain through decision %s contains a cycle at %s; this cannot happen through the service and means the table was edited by hand", decisionID, next.ID)
		}
		seen[next.ID] = true
		chain = append(chain, *next)
		cur = next
	}
	return chain, nil
}

func prefixed(columns, alias string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

func nonEmpty(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}

func orEmpty(a []Alternative) []Alternative {
	if a == nil {
		return []Alternative{}
	}
	return a
}

func orEmptyClaims(c []claim.Claim) []claim.Claim {
	if c == nil {
		return []claim.Claim{}
	}
	return c
}

func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func wrapDecisionWrite(op string, err error) error {
	if strings.Contains(err.Error(), "forge_decisions_supersedes_once") {
		// Lost a race against a concurrent supersession of the same decision.
		return errs.Wrap(op, errs.CodeDecisionSuperseded, err).
			WithDetail("another decision superseded that one first; re-read the chain and supersede its current end")
	}
	return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
}
