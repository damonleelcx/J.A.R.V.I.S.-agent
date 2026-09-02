package engine

import (
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// TestSchemaAndCodeAgreeOnTaskStatuses is a schema/code coherence fence.
//
// The database has a CHECK constraint listing task statuses; this package has a
// transition table listing them. If they drift, the failure is ugly: code writes
// a status the constraint rejects, so a task transition fails at the very moment
// it matters — usually mid-run, usually under load. Worse in the other
// direction: the constraint permits a value the transition table does not know,
// and a task lands in a state no code path can advance, stuck silently forever.
//
// This reads the actual migration SQL that ships in the binary rather than a
// copy, so the two cannot be reconciled by editing the test.
func TestSchemaAndCodeAgreeOnTaskStatuses(t *testing.T) {
	sql := migrationSQL(t, "engine")
	constraint := extractCheckList(t, sql, "forge_tasks_status_check")

	inCode := map[string]bool{}
	for _, s := range AllTaskStatuses() {
		inCode[string(s)] = true
	}
	if len(inCode) < 8 {
		t.Fatalf("AllTaskStatuses returned %d entries; this fence would be weak", len(inCode))
	}

	for _, v := range constraint {
		if !inCode[v] {
			t.Errorf("the database CHECK permits status %q, which the transition table does not know. "+
				"A task could land there and no code path could advance it.", v)
		}
	}
	inSQL := map[string]bool{}
	for _, v := range constraint {
		inSQL[v] = true
	}
	for s := range inCode {
		if !inSQL[s] {
			t.Errorf("the code can produce status %q, which the database CHECK rejects. "+
				"The transition would fail at write time, mid-run.", s)
		}
	}
}

func TestSchemaAndCodeAgreeOnGoalStatuses(t *testing.T) {
	sql := migrationSQL(t, "workspace")
	constraint := extractCheckList(t, sql, "forge_goals_status_check")

	inCode := map[string]bool{}
	for _, s := range AllGoalStatuses() {
		inCode[string(s)] = true
	}
	for _, v := range constraint {
		if !inCode[v] {
			t.Errorf("database permits goal status %q, unknown to the code", v)
		}
	}
	for s := range inCode {
		found := false
		for _, v := range constraint {
			if v == s {
				found = true
			}
		}
		if !found {
			t.Errorf("code can produce goal status %q, rejected by the database", s)
		}
	}
}

func TestSchemaAndCodeAgreeOnAutonomyAndRisk(t *testing.T) {
	sql := migrationSQL(t, "workspace")

	autonomy := extractCheckList(t, sql, "forge_goals_autonomy_check")
	for _, a := range AllAutonomyLevels() {
		found := false
		for _, v := range autonomy {
			if v == string(a) {
				found = true
			}
		}
		if !found {
			t.Errorf("autonomy level %q is unknown to the database CHECK", a)
		}
	}

	risk := extractCheckList(t, sql, "forge_goals_risk_check")
	for _, r := range AllRiskTiers() {
		found := false
		for _, v := range risk {
			if v == string(r) {
				found = true
			}
		}
		if !found {
			t.Errorf("risk tier %q is unknown to the database CHECK", r)
		}
	}
}

// TestTerminalStatesAreTerminal — reopening a finished task would make the
// timeline unreadable ("succeeded at 10:04, running at 11:20, succeeded at
// 11:31"). A replan creates a new task instead.
func TestTerminalStatesAreTerminal(t *testing.T) {
	for _, from := range AllTaskStatuses() {
		if !from.Terminal() {
			continue
		}
		for _, to := range AllTaskStatuses() {
			if from == to {
				continue
			}
			if CanTransition(from, to) {
				t.Errorf("terminal state %q can transition to %q", from, to)
			}
			if err := ValidateTransition(from, to); err == nil {
				t.Errorf("ValidateTransition allowed %q → %q from a terminal state", from, to)
			}
		}
	}
}

// TestEveryNonTerminalStateCanReachATerminalOne guards against a state with no
// way out — a task that can be entered and never finished, which shows up as a
// goal that never completes and no error anywhere explaining why.
func TestEveryNonTerminalStateCanReachATerminalOne(t *testing.T) {
	for _, start := range AllTaskStatuses() {
		if start.Terminal() {
			continue
		}
		if !reachesTerminal(start, map[TaskStatus]bool{}) {
			t.Errorf("state %q cannot reach any terminal state; a task there would be stuck forever", start)
		}
	}
}

func reachesTerminal(s TaskStatus, seen map[TaskStatus]bool) bool {
	if s.Terminal() {
		return true
	}
	if seen[s] {
		return false
	}
	seen[s] = true
	for _, next := range transitions[s] {
		if reachesTerminal(next, seen) {
			return true
		}
	}
	return false
}

// TestApprovalDoesNotJumpStraightToSuccess is the specific shortcut that would
// defeat the whole gate: a task must not be able to move from "waiting for a
// human" directly to "succeeded" without passing back through execution.
func TestApprovalDoesNotJumpStraightToSuccess(t *testing.T) {
	for _, to := range []TaskStatus{StatusSucceeded, StatusVerifying, StatusRunning} {
		if CanTransition(StatusAwaitingApproval, to) {
			t.Errorf("awaiting_approval → %q is permitted; an approved task must be requeued "+
				"so a fresh worker resumes it under a new lease, not silently continued", to)
		}
	}
	if !CanTransition(StatusAwaitingApproval, StatusReady) {
		t.Error("an approved task must be able to return to the queue")
	}
	if !CanTransition(StatusAwaitingApproval, StatusFailed) {
		t.Error("a rejected task must be able to fail")
	}
}

// TestOnlyActiveStatesHoldLeases mirrors the database constraint
// forge_tasks_lease_only_when_active. A lease held in a non-active state hides
// the task from the queue forever.
func TestOnlyActiveStatesHoldLeases(t *testing.T) {
	expected := map[TaskStatus]bool{
		StatusClaimed: true, StatusRunning: true, StatusVerifying: true,
	}
	for _, s := range AllTaskStatuses() {
		if got, want := s.HoldsLease(), expected[s]; got != want {
			t.Errorf("%q.HoldsLease() = %v, want %v", s, got, want)
		}
	}
	// Awaiting approval must not hold one: a gate can stay open for days, and a
	// lease held across it either expires and looks like a crashed worker, or
	// occupies a worker slot doing nothing.
	if StatusAwaitingApproval.HoldsLease() {
		t.Error("awaiting_approval must not hold a lease")
	}
	sql := migrationSQL(t, "engine")
	if !strings.Contains(sql, "forge_tasks_lease_only_when_active") {
		t.Fatal("the database constraint this mirrors is missing; the two would drift silently")
	}
}

func TestIllegalTransitionsExplainThemselves(t *testing.T) {
	err := ValidateTransition(StatusPending, StatusSucceeded)
	if err == nil {
		t.Fatal("pending → succeeded should be refused")
	}
	if errs.CodeOf(err) != errs.CodeInvariantViolated {
		t.Errorf("code = %v, want INVARIANT_VIOLATED", errs.CodeOf(err))
	}
	// The message must name the legal alternatives, or a developer hitting it
	// has to open this file to find out what they were allowed to do.
	if !strings.Contains(err.Error(), "legal targets") {
		t.Errorf("the error should list legal targets: %v", err)
	}

	if err := ValidateTransition(StatusPending, StatusPending); err == nil {
		t.Error("a self-transition should be refused; it would append a timeline event describing no change")
	}
	if err := ValidateTransition("nonsense", StatusReady); err == nil {
		t.Error("an unrecognised current status should be refused")
	} else if errs.CodeOf(err) != errs.CodeStateCorrupt {
		t.Errorf("an unrecognised STORED status is corruption, not a caller error; got %v", errs.CodeOf(err))
	}
}

// TestProhibitedIsNotALowAutonomyLevel — treating "prohibited" as the bottom of
// the ladder is the mistake that lets an off-by-one comparison enable it.
func TestProhibitedIsNotALowAutonomyLevel(t *testing.T) {
	if AutonomyProhibited.AtLeast(AutonomyDiscuss) {
		t.Error("prohibited satisfies discuss; it must satisfy nothing")
	}
	if AutonomyProhibited.AtLeast(AutonomyProhibited) {
		t.Error("prohibited satisfies itself; a caller could treat it as sufficient")
	}
	if AutonomyProhibited.AllowsExecution() {
		t.Error("prohibited allows execution")
	}
	for _, a := range AllAutonomyLevels() {
		if a == AutonomyProhibited {
			continue
		}
		if a.AtLeast(AutonomyProhibited) {
			t.Errorf("%q satisfies prohibited", a)
		}
	}
}

func TestAutonomyLadderIsOrdered(t *testing.T) {
	ordered := []Autonomy{AutonomyDiscuss, AutonomyDraft, AutonomySandboxExecute, AutonomyApprovalGated}
	for i, lower := range ordered {
		for j, higher := range ordered {
			if j <= i {
				continue
			}
			if !higher.AtLeast(lower) {
				t.Errorf("%q should satisfy %q", higher, lower)
			}
			if lower.AtLeast(higher) {
				t.Errorf("%q should NOT satisfy %q", lower, higher)
			}
		}
	}
	if AutonomyDraft.AllowsExecution() {
		t.Error("draft must not permit execution; it produces artifacts for review")
	}
	if !AutonomySandboxExecute.AllowsExecution() {
		t.Error("sandbox_execute must permit execution")
	}
}

// TestApprovalThresholdIsR2 pins a judgement call rather than an arbitrary
// constant. Gating R0/R1 — reversible, sandboxed work — trains reviewers to
// click through, which is how a gate stops being a control.
func TestApprovalThresholdIsR2(t *testing.T) {
	for _, tier := range []RiskTier{RiskR0, RiskR1} {
		if tier.RequiresApproval() {
			t.Errorf("%q requires approval; gating reversible sandbox work trains reviewers to click through", tier)
		}
	}
	for _, tier := range []RiskTier{RiskR2, RiskR3, RiskR4} {
		if !tier.RequiresApproval() {
			t.Errorf("%q does not require approval", tier)
		}
	}
	// R5 is refused, not gated. Reporting it as "requires approval" would imply
	// that some approval could authorise it.
	if RiskR5.RequiresApproval() {
		t.Error("R5 reports as requiring approval; it is prohibited, and no approval authorises it")
	}
	if !RiskR5.Prohibited() {
		t.Error("R5 must be prohibited")
	}
	for _, tier := range []RiskTier{RiskR0, RiskR1, RiskR2, RiskR3, RiskR4} {
		if tier.Prohibited() {
			t.Errorf("%q reports as prohibited", tier)
		}
	}
}

// --- helpers ---------------------------------------------------------------

// migrationSQL returns the text of the migration whose name contains substr,
// read from the embedded chain that actually ships.
func migrationSQL(t *testing.T, substr string) string {
	t.Helper()
	ms, err := db.LoadMigrations(db.Files, db.MigrationsDir)
	if err != nil {
		t.Fatalf("loading migrations: %v", err)
	}
	for _, m := range ms {
		if strings.Contains(m.Name, substr) {
			return m.SQL
		}
	}
	t.Fatalf("no migration whose name contains %q", substr)
	return ""
}

// extractCheckList pulls the quoted values out of a named CHECK ... in (...)
// constraint.
//
// Whitespace-tolerant by regex, deliberately. The first version searched for the
// literal "in (" and silently skipped past a constraint written as
//
//	check (status in
//	       ('pending','ready',...))
//
// matching the NEXT single-line constraint instead — so it compared task
// statuses against risk tiers and reported drift that did not exist. A fence
// that reports a false positive gets disabled, which is how the bug it guards
// comes back.
var checkListRe = regexp.MustCompile(`(?s)in\s*\(([^)]*)\)`)

func extractCheckList(t *testing.T, sql, constraintName string) []string {
	t.Helper()

	i := strings.Index(sql, constraintName)
	if i < 0 {
		t.Fatalf("constraint %q not found in the migration; the fence cannot compare anything", constraintName)
	}
	m := checkListRe.FindStringSubmatch(sql[i:])
	if m == nil {
		t.Fatalf("constraint %q is not an `in (...)` check", constraintName)
	}

	var out []string
	for _, part := range strings.Split(m[1], ",") {
		v := strings.Trim(strings.TrimSpace(part), "'")
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		t.Fatalf("extracted no values from %q; the comparison would be vacuous", constraintName)
	}
	return out
}
