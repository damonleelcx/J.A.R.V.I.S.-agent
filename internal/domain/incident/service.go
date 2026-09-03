package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Service is the use-case layer over incident response.
type Service struct {
	pool   *db.Pool
	engine *engine.Repository
	clock  clock.Clock
	log    *logx.Logger
}

// NewService wires the service.
func NewService(pool *db.Pool, clk clock.Clock, log *logx.Logger) *Service {
	return &Service{pool: pool, engine: engine.NewRepository(), clock: clk, log: log}
}

// Open starts an incident.
func (s *Service) Open(ctx context.Context, i *Incident) (*Incident, error) {
	const op = "incident.Service.Open"

	if err := i.Validate(); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	i.ID, i.OpenedAt, i.CreatedAt, i.UpdatedAt = id.New(id.PrefixIncident), now, now, now
	i.Status = StatusOpen

	_, err := s.pool.Exec(ctx, `
		insert into forge_incidents
			(id, project_id, goal_id, title, statement, severity, status,
			 opened_by, opened_at, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,'open',$7,$8,$8,$8)`,
		i.ID, i.ProjectID, i.GoalID, i.Title, i.Statement, string(i.Severity), i.OpenedBy, now)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.log.Warn(ctx, logx.EventIncidentOpened, "incident_id", i.ID, "project_id", i.ProjectID,
		"severity", string(i.Severity), "title", i.Title, "by", i.OpenedBy)
	return i, nil
}

// Act appends one of SAF-07's seven verbs.
//
// The evidence-first rule is checked against the stored record inside the same
// transaction as the append, so two responders acting at once cannot both pass a
// check that neither would pass alone.
func (s *Service) Act(ctx context.Context, incidentID string, a *Action) (*Action, error) {
	const op = "incident.Service.Act"

	if a.TakenAt.IsZero() {
		a.TakenAt = s.clock.Now()
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inc, err := s.load(ctx, tx, incidentID, true)
	if err != nil {
		return nil, err
	}
	if err := inc.AllowsAction(a.Kind, a.Outcome); err != nil {
		return nil, err
	}

	var next int
	if err := tx.QueryRow(ctx,
		`select coalesce(max(seq), 0) + 1 from forge_incident_actions where incident_id = $1`,
		incidentID).Scan(&next); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	a.ID, a.IncidentID, a.Seq = id.New(id.PrefixAction), incidentID, next

	if _, err := tx.Exec(ctx, `
		insert into forge_incident_actions
			(id, incident_id, seq, kind, target, detail, outcome, taken_by, taken_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		a.ID, a.IncidentID, a.Seq, string(a.Kind), a.Target, a.Detail,
		string(a.Outcome), a.TakenBy, a.TakenAt); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}

	s.log.Warn(ctx, logx.EventIncidentAction, "incident_id", incidentID, "seq", a.Seq,
		"kind", string(a.Kind), "target", a.Target, "outcome", string(a.Outcome), "by", a.TakenBy)
	return a, nil
}

// Contain marks the bleeding stopped.
//
// Refused while nothing destructive has actually been done, because containment
// is a claim about the world rather than about the record: an incident marked
// contained after nothing but a dry run says the danger passed when nobody did
// anything.
func (s *Service) Contain(ctx context.Context, incidentID, byUserID string) error {
	const op = "incident.Service.Contain"

	inc, err := s.Find(ctx, incidentID)
	if err != nil {
		return err
	}
	if inc.Status == StatusClosed {
		return errs.New(op, errs.CodeConflict).WithDetail("incident %s is closed", incidentID)
	}
	var acted bool
	for _, a := range inc.Actions {
		if a.Kind.Destructive() && a.Outcome.Changed() {
			acted = true
		}
	}
	if !acted {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("nothing has been stopped, revoked, quarantined or rolled back on incident %s, so "+
				"there is nothing to have contained. Marking it contained would say the danger passed "+
				"when nobody did anything.", incidentID)
	}
	if _, err := s.pool.Exec(ctx,
		`update forge_incidents set status = 'contained', updated_at = $2 where id = $1 and status = 'open'`,
		incidentID, s.clock.Now()); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.log.Warn(ctx, logx.EventIncidentAction, "incident_id", incidentID, "kind", "contain", "by", byUserID)
	return nil
}

// Close finishes an incident, which requires a review.
func (s *Service) Close(ctx context.Context, incidentID, byUserID, review string) error {
	const op = "incident.Service.Close"

	inc, err := s.Find(ctx, incidentID)
	if err != nil {
		return err
	}
	if err := inc.Closeable(review); err != nil {
		return err
	}
	if strings.TrimSpace(byUserID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("closing an incident must name the person who closed it")
	}
	now := s.clock.Now()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		update forge_incidents
		   set status = 'closed', review = $2, closed_by = $3, closed_at = $4, updated_at = $4
		 where id = $1 and status <> 'closed'`, incidentID, review, byUserID, now)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeConflict).WithDetail("incident %s was closed by somebody else first", incidentID)
	}
	// The review is also an action, so the timeline of the response reads in one
	// place rather than ending abruptly with the closure recorded elsewhere.
	var next int
	if err := tx.QueryRow(ctx,
		`select coalesce(max(seq), 0) + 1 from forge_incident_actions where incident_id = $1`,
		incidentID).Scan(&next); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if _, err := tx.Exec(ctx, `
		insert into forge_incident_actions (id, incident_id, seq, kind, detail, outcome, taken_by, taken_at)
		values ($1,$2,$3,'review',$4,'done',$5,$6)`,
		id.New(id.PrefixAction), incidentID, next, review, byUserID, now); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.log.Warn(ctx, logx.EventIncidentClosed, "incident_id", incidentID, "by", byUserID)
	return nil
}

// Evidence is a snapshot of what the system said at one instant.
//
// # What it is for
//
// SAF-07's "preserve evidence" is the step everything else destroys. Stopping a
// goal changes its status; revoking a secret changes its row; rolling an artifact
// back changes what the current version is. Each of those is the right thing to
// do and each of them removes something an investigation wanted.
//
// So this reads the state, verifies the audit chain over it, and writes the whole
// thing into the incident action's detail — where nothing later in the response
// can reach it.
type Evidence struct {
	CapturedAt string         `json:"captured_at"`
	GoalID     string         `json:"goal_id,omitempty"`
	GoalStatus string         `json:"goal_status,omitempty"`
	TaskCounts map[string]int `json:"task_counts,omitempty"`
	// ChainIntact is the audit verifier's verdict AT CAPTURE TIME. Recording it
	// here matters more than it looks: after the response has written its own
	// events, "was the chain intact when this started?" is no longer answerable
	// by running the verifier.
	ChainIntact   *bool           `json:"chain_intact,omitempty"`
	ChainSummary  string          `json:"chain_summary,omitempty"`
	OpenApprovals int             `json:"open_approvals"`
	ActiveSecrets []string        `json:"active_secrets,omitempty"`
	Extra         json.RawMessage `json:"extra,omitempty"`
	// Incomplete names what could not be captured. A snapshot that silently
	// omitted half the system would be worse than none: it reads as a complete
	// picture of a smaller problem.
	Incomplete []string `json:"incomplete,omitempty"`
}

// PreserveEvidence captures the current state and appends it as an action.
//
// It never fails the whole capture because one part could not be read: a partial
// snapshot is worth having and its gaps are named in Incomplete. The outcome is
// recorded as `partial` in that case, so the incident record does not claim a
// complete capture.
func (s *Service) PreserveEvidence(ctx context.Context, incidentID, byUserID string) (*Action, *Evidence, error) {
	const op = "incident.Service.PreserveEvidence"

	inc, err := s.Find(ctx, incidentID)
	if err != nil {
		return nil, nil, err
	}
	now := s.clock.Now()
	ev := &Evidence{CapturedAt: now.UTC().Format(time.RFC3339)}

	if inc.GoalID != nil {
		ev.GoalID = *inc.GoalID
		var status string
		if err := s.pool.QueryRow(ctx, `select status from forge_goals where id = $1`, *inc.GoalID).Scan(&status); err != nil {
			ev.Incomplete = append(ev.Incomplete, "goal status: "+err.Error())
		} else {
			ev.GoalStatus = status
		}
		if counts, err := s.taskCounts(ctx, *inc.GoalID); err != nil {
			ev.Incomplete = append(ev.Incomplete, "task counts: "+err.Error())
		} else {
			ev.TaskCounts = counts
		}
		if report, err := s.engine.VerifyChain(ctx, s.pool, *inc.GoalID); err != nil {
			ev.Incomplete = append(ev.Incomplete, "audit chain: "+err.Error())
		} else {
			intact := report.Intact()
			ev.ChainIntact, ev.ChainSummary = &intact, report.Summary()
		}
		if err := s.pool.QueryRow(ctx,
			`select count(*) from forge_approvals where goal_id = $1 and decision = 'pending'`,
			*inc.GoalID).Scan(&ev.OpenApprovals); err != nil {
			ev.Incomplete = append(ev.Incomplete, "open approvals: "+err.Error())
		}
	}
	if names, err := s.activeSecrets(ctx, inc.ProjectID); err != nil {
		ev.Incomplete = append(ev.Incomplete, "secrets: "+err.Error())
	} else {
		ev.ActiveSecrets = names
	}

	blob, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return nil, nil, errs.Wrap(op, errs.CodeSerializationFail, err)
	}
	outcome := OutcomeDone
	if len(ev.Incomplete) > 0 {
		outcome = OutcomePartial
	}
	action, err := s.Act(ctx, incidentID, &Action{
		Kind: ActionPreserveEvidence, Outcome: outcome,
		Detail: string(blob), TakenBy: byUserID, TakenAt: now,
	})
	if err != nil {
		return nil, nil, err
	}
	return action, ev, nil
}

func (s *Service) taskCounts(ctx context.Context, goalID string) (map[string]int, error) {
	rows, err := s.pool.Query(ctx,
		`select status, count(*) from forge_tasks where goal_id = $1 group by status`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

// activeSecrets records WHICH handles were live, by name. Never a value, and
// never the environment variable: evidence lands in an incident record that
// several people read, and a snapshot that named the variables would be a map of
// where to look.
func (s *Service) activeSecrets(ctx context.Context, projectID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`select name from forge_secrets where project_id = $1 and revoked_at is null order by name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Find returns one incident with its actions.
func (s *Service) Find(ctx context.Context, incidentID string) (*Incident, error) {
	return s.load(ctx, s.pool, incidentID, true)
}

func (s *Service) load(ctx context.Context, q db.Querier, incidentID string, withActions bool) (*Incident, error) {
	const op = "incident.Service.load"

	var i Incident
	var severity, status string
	err := q.QueryRow(ctx, `
		select id, project_id, goal_id, title, statement, severity, status,
		       opened_by, opened_at, review, closed_by, closed_at, created_at, updated_at
		  from forge_incidents where id = $1`, incidentID).
		Scan(&i.ID, &i.ProjectID, &i.GoalID, &i.Title, &i.Statement, &severity, &status,
			&i.OpenedBy, &i.OpenedAt, &i.Review, &i.ClosedBy, &i.ClosedAt, &i.CreatedAt, &i.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no incident %s", incidentID)
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	i.Severity, i.Status = Severity(severity), Status(status)
	if !i.Severity.Valid() || !i.Status.Valid() {
		return nil, errs.New(op, errs.CodeStateCorrupt).
			WithDetail("incident %s has severity %q and status %q; at least one is unrecognised by this build",
				i.ID, severity, status)
	}
	if !withActions {
		return &i, nil
	}
	rows, err := q.Query(ctx, `
		select id, incident_id, seq, kind, target, detail, outcome, taken_by, taken_at
		  from forge_incident_actions where incident_id = $1 order by seq asc`, incidentID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()
	for rows.Next() {
		var a Action
		var kind, outcome string
		if err := rows.Scan(&a.ID, &a.IncidentID, &a.Seq, &kind, &a.Target, &a.Detail,
			&outcome, &a.TakenBy, &a.TakenAt); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		a.Kind, a.Outcome = ActionKind(kind), Outcome(outcome)
		i.Actions = append(i.Actions, a)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return &i, nil
}

// List returns a project's incidents, newest first.
func (s *Service) List(ctx context.Context, projectID string, openOnly bool) ([]Incident, error) {
	const op = "incident.Service.List"

	sql := `select id, project_id, goal_id, title, statement, severity, status,
	               opened_by, opened_at, review, closed_by, closed_at, created_at, updated_at
	          from forge_incidents where project_id = $1`
	if openOnly {
		sql += ` and status <> 'closed'`
	}
	sql += ` order by opened_at desc limit 200`

	rows, err := s.pool.Query(ctx, sql, projectID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		var i Incident
		var severity, status string
		if err := rows.Scan(&i.ID, &i.ProjectID, &i.GoalID, &i.Title, &i.Statement, &severity, &status,
			&i.OpenedBy, &i.OpenedAt, &i.Review, &i.ClosedBy, &i.ClosedAt, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		i.Severity, i.Status = Severity(severity), Status(status)
		out = append(out, i)
	}
	return out, rows.Err()
}

// Summary is one line for a human or a log.
func (i *Incident) Summary() string {
	destructive := 0
	for _, a := range i.Actions {
		if a.Kind.Destructive() && a.Outcome.Changed() {
			destructive++
		}
	}
	return fmt.Sprintf("%s · %s · %d action(s), %d of them changed something · evidence %s",
		i.Severity, i.Status, len(i.Actions), destructive,
		map[bool]string{true: "preserved", false: "NOT PRESERVED"}[i.EvidencePreserved()])
}
