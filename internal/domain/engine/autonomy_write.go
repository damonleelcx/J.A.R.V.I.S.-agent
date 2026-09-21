package engine

import (
	"context"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Autonomy is write-once, and this is where that is said out loud.
//
// # What PRD AGT-04 actually guarantees
//
// "Progressive autonomy; never silently raises its own autonomy level." The
// ladder next door in state.go is carefully built — ordered so levels compare,
// with prohibited ranked -1 so it satisfies nothing including itself — and the
// three tests over it hold LADDER SEMANTICS. None of them holds the property.
//
// The property held because autonomy is set once, at goal creation
// (agent/intake.go, httpapi/goals_start.go), and no code path changed it
// afterwards. There was no UpdateAutonomy and no `update forge_goals` touching
// the column anywhere in the tree. That is a real guarantee and it rested
// entirely on an ABSENCE — and absence is not fenced. All three ladder tests
// would have stayed green on the day somebody added an endpoint that bumped a
// running goal's autonomy, which is the feature this repository will be asked
// for ("let the user raise it without re-planning") sooner or later.
//
// # What this file adds
//
// The words. RaisesAutonomy answers "would this be a raise" in one place, so the
// question is not re-derived at each new call site with a subtly different
// answer about prohibited. RefuseAutonomyRaise is what a surface calls when a
// caller asks for one: it says no, and it says so in the audit log, because
// AGT-04's word is *silently* and a refusal nothing records is close enough to
// silence to matter.
//
// The database holds the same rule from underneath
// (0028_autonomy_is_write_once.sql), so a future writer that never finds this
// file is still refused. And AutonomyIsWriteOnce in
// internal/httpapi/autonomy_fence_test.go is table-driven over every source site
// that writes the column, so a new one fails by name rather than shipping.

// RaisesAutonomy reports whether moving a goal from one level to another would
// be a raise (PRD AGT-04).
//
// # Prohibited, both ways
//
// Prohibited is not the bottom of the ladder, it is off it: a deliberate stop.
// So moving OFF prohibited to anything at all is a raise — it turns a refusal
// into a permission, which is the largest raise there is — and moving TO
// prohibited is never one, whatever it came from. Deriving this from
// autonomyRank would get both backwards, because prohibited's rank of -1 exists
// to keep AtLeast honest and not to order it.
func RaisesAutonomy(from, to Autonomy) bool {
	if from == to {
		return false
	}
	if to == AutonomyProhibited {
		return false
	}
	if from == AutonomyProhibited {
		return true
	}
	rf, okF := autonomyRank[from]
	rt, okT := autonomyRank[to]
	if !okF || !okT {
		// An unrecognised level must never be the safe side of a comparison. The
		// same rule Role.Allows follows for an unrecognised role.
		return true
	}
	return rt > rf
}

// RefuseAutonomyRaise is what a surface calls when somebody asks for a raise.
//
// It always refuses. It is not a policy hook with a permissive branch, because
// there is no principal in this build — human or agent — that may raise a goal's
// autonomy after the goal exists: the way to work at a higher level is to draft
// a new goal at that level and read its plan, which is AGT-02 and AGT-04 working
// together rather than a limitation of this function.
//
// The audit event is the point. AGT-04 forbids raising autonomy *silently*; a
// refusal that left no record would mean nobody could ever answer "has anything
// tried".
func RefuseAutonomyRaise(ctx context.Context, log *logx.Logger,
	goalID string, from, to Autonomy, by string) error {
	const op = "engine.RefuseAutonomyRaise"

	if log != nil {
		log.Info(ctx, logx.EventAutonomyRaiseRefused,
			"goal_id", goalID, "from", string(from), "to", string(to), "by", by)
	}
	return errs.New(op, errs.CodeForbidden).
		WithDetail("goal %s is at autonomy %q and cannot be moved to %q. A goal's autonomy is "+
			"fixed when it is created and nothing raises it afterwards (PRD AGT-04). To work "+
			"at a higher level, draft a new goal at that level and read the plan before "+
			"starting it — which is the point: autonomy rises only where somebody sees it "+
			"rise.", goalID, from, to)
}
