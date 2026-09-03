package collab

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The handoff (PRD COL-02).
//
// "State, actions, versions, approvals, evidence, open risks, recommended next
// work." Seven things, and every one of them already exists somewhere in this
// system: waves 1 to 5 built the goal state machine, the timeline, the artifact
// lifecycle, the approval gates, the graph's evidence nodes, its risks, and the
// review that finds gaps.
//
// So a handoff is DERIVED, not stored. It is a reading of the system at an
// instant, and storing one would create a second truth that goes stale the
// moment anything moves — the thing a handoff is most dangerous for, because its
// whole purpose is to be believed by somebody who was not there.
//
// # The rule it inherits from NFR-07
//
// A handoff must never imply completion. It is handed to somebody picking up
// half-finished work, and the failure mode is a document that reads like a
// summary of finished business. So it states what is unresolved FIRST and counts
// it, and `Complete()` is false whenever anything is outstanding — including
// when the goal itself has not ended.

// Handoff is everything somebody needs to pick up where another person stopped.
type Handoff struct {
	GoalID string
	Title  string
	// TakenAt is the instant this describes. A handoff is a photograph, and one
	// without a timestamp is a photograph somebody will treat as live.
	TakenAt time.Time

	// 1. State.
	Status         string
	OutcomeSummary string
	TasksTotal     int
	TasksByStatus  map[string]int

	// 2. Actions — what happened, most recent first, bounded.
	RecentActions []string

	// 3. Versions — artifacts and where each stands.
	Versions []VersionLine

	// 4. Approvals — decided and outstanding.
	ApprovalsPending int
	ApprovalsDecided []string

	// 5. Evidence — what has actually been checked.
	Evidence []string

	// 6. Open risks — from the graph, plus anything the review found wrong.
	OpenRisks    []string
	GraphDefects []string

	// 7. Recommended next work — derived, and honest about being derived.
	Recommended []string

	// Unresolved is everything still owed, counted so a reader cannot skim past
	// it. This is the field that keeps a handoff from reading as a conclusion.
	Unresolved []string
}

// VersionLine is one artifact's current standing.
type VersionLine struct {
	Path         string
	Version      int
	Verification string
	Disposition  string
	Usable       bool
	Why          string
}

// Complete reports whether there is nothing left to hand over.
//
// False whenever anything is outstanding, including a goal that has not ended.
// A handoff of live work is the normal case and must not read as a wrap-up.
func (h *Handoff) Complete() bool { return len(h.Unresolved) == 0 }

// Summary is the one line a reader sees first.
//
// It leads with what is unfinished. A summary that led with what was done would
// be read as a conclusion by exactly the person least able to tell.
func (h *Handoff) Summary() string {
	if h.Complete() {
		return fmt.Sprintf("%s — %s. Nothing outstanding.", h.Title, h.Status)
	}
	return fmt.Sprintf("%s — %s, with %d thing(s) still open. This is work in progress, not a result.",
		h.Title, h.Status, len(h.Unresolved))
}

// TakeHandoff assembles the document for a goal.
//
// Every part is read at the same instant and gaps are reported rather than
// omitted: a handoff that silently dropped a section it could not read would
// hand somebody a shorter problem than the one they have.
func (s *Service) TakeHandoff(ctx context.Context, goalID string) (*Handoff, error) {
	const op = "collab.Service.TakeHandoff"

	h := &Handoff{GoalID: goalID, TakenAt: s.clock.Now(), TasksByStatus: map[string]int{}}

	var projectID string
	if err := s.pool.QueryRow(ctx, `
		select project_id, title, status, coalesce(outcome_summary, '')
		  from forge_goals where id = $1`, goalID).
		Scan(&projectID, &h.Title, &h.Status, &h.OutcomeSummary); err != nil {
		return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no goal %s", goalID)
	}

	// --- 1. state ---
	if err := s.readTaskCounts(ctx, h, goalID); err != nil {
		h.Unresolved = append(h.Unresolved, "task counts could not be read: "+err.Error())
	}
	if h.Status != string(engine.GoalSucceeded) && h.Status != string(engine.GoalFailed) &&
		h.Status != string(engine.GoalCancelled) {
		h.Unresolved = append(h.Unresolved,
			fmt.Sprintf("the goal is %s — the work has not ended", h.Status))
	}

	// --- 2. actions ---
	if err := s.readRecentActions(ctx, h, goalID); err != nil {
		h.Unresolved = append(h.Unresolved, "the timeline could not be read: "+err.Error())
	}

	// --- 3. versions, 4. approvals, 5. evidence, 6. risks ---
	if err := s.readVersions(ctx, h, projectID); err != nil {
		h.Unresolved = append(h.Unresolved, "artifact versions could not be read: "+err.Error())
	}
	if err := s.readApprovals(ctx, h, goalID); err != nil {
		h.Unresolved = append(h.Unresolved, "approvals could not be read: "+err.Error())
	}
	if err := s.readGraph(ctx, h, projectID); err != nil {
		h.Unresolved = append(h.Unresolved, "the project graph could not be read: "+err.Error())
	}

	// --- 7. recommended next work ---
	h.Recommended = recommend(h)

	sort.Strings(h.Unresolved)
	s.log.Info(ctx, logx.EventHandoffTaken, "goal_id", goalID,
		"unresolved", len(h.Unresolved), "complete", h.Complete())
	return h, nil
}

func (s *Service) readTaskCounts(ctx context.Context, h *Handoff, goalID string) error {
	rows, err := s.pool.Query(ctx,
		`select status, count(*) from forge_tasks where goal_id = $1 group by status`, goalID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return err
		}
		h.TasksByStatus[status] = n
		h.TasksTotal += n
		if !engine.TaskStatus(status).Terminal() {
			h.Unresolved = append(h.Unresolved,
				fmt.Sprintf("%d task(s) are %s", n, status))
		}
	}
	return rows.Err()
}

func (s *Service) readRecentActions(ctx context.Context, h *Handoff, goalID string) error {
	// Bounded: a handoff is read by a person, and an unbounded timeline is a
	// document nobody finishes. The audit chain has the rest.
	rows, err := s.pool.Query(ctx, `
		select kind, summary, actor, created_at from forge_events
		 where goal_id = $1 order by seq desc limit 20`, goalID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, summary, actor string
		var at time.Time
		if err := rows.Scan(&kind, &summary, &actor, &at); err != nil {
			return err
		}
		h.RecentActions = append(h.RecentActions,
			fmt.Sprintf("%s  %-22s %s (%s)", at.UTC().Format("01-02 15:04"), kind, summary, actor))
	}
	return rows.Err()
}

func (s *Service) readVersions(ctx context.Context, h *Handoff, projectID string) error {
	rows, err := s.pool.Query(ctx, `
		select a.path, v.version, v.verification_state, v.human_disposition,
		       v.verification_note, v.disposition_reason
		  from forge_artifacts a
		  join forge_artifact_versions v on v.artifact_id = a.id
		 where a.project_id = $1
		   and v.version = (select max(version) from forge_artifact_versions x where x.artifact_id = a.id)
		 order by a.path`, projectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var l VersionLine
		var vnote, dreason string
		if err := rows.Scan(&l.Path, &l.Version, &l.Verification, &l.Disposition, &vnote, &dreason); err != nil {
			return err
		}
		// The usable verdict is computed here rather than left to the reader,
		// for the same reason WRK-04 computes it: "may I rely on this" needs
		// both facts and somebody skimming will use one.
		v := workspace.Version{
			Version:          l.Version,
			Verification:     workspace.Verification(l.Verification),
			Disposition:      workspace.Disposition(l.Disposition),
			VerificationNote: vnote, DispositionReason: dreason,
		}
		if err := v.Usable(); err != nil {
			l.Usable, l.Why = false, err.Error()
			h.Unresolved = append(h.Unresolved,
				fmt.Sprintf("%s v%d cannot be relied on: %s", l.Path, l.Version, l.Disposition))
		} else {
			l.Usable, l.Why = true, "verified by a machine and accepted by a person"
		}
		h.Versions = append(h.Versions, l)
	}
	return rows.Err()
}

func (s *Service) readApprovals(ctx context.Context, h *Handoff, goalID string) error {
	rows, err := s.pool.Query(ctx, `
		select id, decision, summary, coalesce(decided_by, '')
		  from forge_approvals where goal_id = $1 order by requested_at`, goalID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var approvalID, decision, summary, decidedBy string
		if err := rows.Scan(&approvalID, &decision, &summary, &decidedBy); err != nil {
			return err
		}
		if decision == "pending" {
			h.ApprovalsPending++
			h.Unresolved = append(h.Unresolved, "an approval is waiting on a person: "+summary)
			continue
		}
		// Who decided it, named. PRD SAF-05 in the document somebody reads when
		// they pick the work up.
		who := decidedBy
		if who == "" {
			who = "nobody recorded"
		}
		h.ApprovalsDecided = append(h.ApprovalsDecided,
			fmt.Sprintf("%s — %s by %s", summary, decision, who))
	}
	return rows.Err()
}

func (s *Service) readGraph(ctx context.Context, h *Handoff, projectID string) error {
	ws := workspace.NewService(s.pool, s.clock, s.log)
	review, err := ws.Review(ctx, projectID)
	if err != nil {
		return err
	}
	for _, d := range review.Defects {
		h.GraphDefects = append(h.GraphDefects, d.Detail)
		h.Unresolved = append(h.Unresolved, "the project graph contradicts itself: "+d.Detail)
	}

	nodes, err := ws.Repo().ListNodes(ctx, s.pool, workspace.NodeFilter{
		ProjectID: projectID,
		Kinds:     []workspace.Kind{workspace.KindRisk, workspace.KindHazard, workspace.KindEvidence},
	})
	if err != nil {
		return err
	}
	for _, n := range nodes {
		switch n.Kind {
		case workspace.KindEvidence:
			h.Evidence = append(h.Evidence, fmt.Sprintf("%s [%s]", n.Title, n.How))
		default:
			if n.Status == workspace.StatusRetired {
				continue
			}
			h.OpenRisks = append(h.OpenRisks, fmt.Sprintf("%s: %s", n.Kind, n.Title))
			h.Unresolved = append(h.Unresolved, fmt.Sprintf("an open %s: %s", n.Kind, n.Title))
		}
	}
	// Gaps are listed as recommendations rather than as unresolved items: every
	// project in progress has them, and a handoff that counted them as debts
	// would report a healthy project as a mess.
	for _, g := range review.Gaps {
		h.Recommended = append(h.Recommended, g.Detail)
	}
	return nil
}

// recommend derives next work from what is outstanding.
//
// # Why this is derived rather than asked of a model
//
// The same rule as the claim ledger: a model asked "what should they do next"
// would answer, plausibly, and the answer would carry no more authority than the
// question. Everything here is read off state — a pending approval needs
// deciding, an unusable version needs a decision, a contradicting graph needs
// resolving — so a recommendation can be checked against the thing that produced
// it.
func recommend(h *Handoff) []string {
	out := append([]string(nil), h.Recommended...)

	if h.ApprovalsPending > 0 {
		out = append(out, fmt.Sprintf("decide the %d approval(s) waiting on a person — nothing downstream moves until then",
			h.ApprovalsPending))
	}
	for _, v := range h.Versions {
		if !v.Usable && v.Disposition == string(workspace.Pending) {
			out = append(out, fmt.Sprintf("look at %s v%d: a machine says %s and no person has ruled on it",
				v.Path, v.Version, v.Verification))
		}
	}
	if len(h.GraphDefects) > 0 {
		out = append(out, "resolve what the project graph contradicts before building further on it")
	}
	if len(out) == 0 && !h.Complete() {
		out = append(out, "the work is not finished and nothing specific is blocked — read the recent "+
			"actions and continue")
	}
	sort.Strings(out)
	return out
}

// Render writes the handoff as text somebody can paste into a message.
//
// Unresolved first, deliberately. A document that opened with what was done
// would be skimmed as a conclusion by exactly the person least equipped to know
// otherwise.
func (h *Handoff) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "HANDOFF — %s\n%s\nTaken at %s\n\n",
		h.Title, h.Summary(), h.TakenAt.UTC().Format(time.RFC3339))

	if len(h.Unresolved) > 0 {
		fmt.Fprintf(&b, "STILL OPEN (%d)\n", len(h.Unresolved))
		for _, u := range h.Unresolved {
			fmt.Fprintf(&b, "  - %s\n", u)
		}
		b.WriteString("\n")
	}
	if len(h.Recommended) > 0 {
		b.WriteString("SUGGESTED NEXT\n")
		for _, r := range h.Recommended {
			fmt.Fprintf(&b, "  - %s\n", r)
		}
		b.WriteString("  (derived from the state above, not from a model's opinion)\n\n")
	}
	fmt.Fprintf(&b, "STATE\n  %s", h.Status)
	if h.OutcomeSummary != "" {
		fmt.Fprintf(&b, " — %s", h.OutcomeSummary)
	}
	fmt.Fprintf(&b, "\n  %d task(s):", h.TasksTotal)
	for _, k := range sortedKeys(h.TasksByStatus) {
		fmt.Fprintf(&b, " %d %s,", h.TasksByStatus[k], k)
	}
	b.WriteString("\n\n")

	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s\n", title)
		for _, l := range lines {
			fmt.Fprintf(&b, "  - %s\n", l)
		}
		b.WriteString("\n")
	}
	if len(h.Versions) > 0 {
		b.WriteString("ARTIFACTS\n")
		for _, v := range h.Versions {
			mark := "  "
			if !v.Usable {
				mark = "! "
			}
			fmt.Fprintf(&b, "%s%s v%d — machine: %s, person: %s\n",
				mark, v.Path, v.Version, v.Verification, v.Disposition)
		}
		b.WriteString("\n")
	}
	section("APPROVALS DECIDED", h.ApprovalsDecided)
	section("EVIDENCE", h.Evidence)
	section("OPEN RISKS", h.OpenRisks)
	section("RECENT ACTIONS", h.RecentActions)
	return b.String()
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
