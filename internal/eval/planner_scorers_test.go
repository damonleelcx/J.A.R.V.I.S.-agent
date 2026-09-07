package eval

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
)

// The fences over the planner scorers (wave 32).
//
// Each one is driven with a plan that should hold and one that should not,
// because a scorer that has only ever been shown a passing case is a scorer
// nobody has watched judge anything.

func planned(tasks ...agent.PlannedTask) *Observation {
	return &Observation{Plan: &agent.PlanResult{Tasks: tasks}}
}

func task(key, tier string, deps ...string) agent.PlannedTask {
	return agent.PlannedTask{Key: key, RiskTier: tier, DependsOn: deps}
}

func TestSomeTasksCanStartAtOnce_TellsAChainFromAGraph(t *testing.T) {
	s := someTasksCanStartAtOnce()

	chain := planned(task("a", "r1"), task("b", "r1", "a"), task("c", "r1", "b"))
	if held, detail := s.Judge(chain); held {
		t.Errorf("a chain was scored as independent work: %s", detail)
	}
	graph := planned(task("a", "r1"), task("b", "r1"), task("c", "r1", "a", "b"))
	if held, detail := s.Judge(graph); !held {
		t.Errorf("a plan with two tasks that can start at once was scored as a chain: %s", detail)
	}
	// The measured case, verbatim in shape: three tasks, one free. This is what
	// qwen3.7-plus produced three times out of three on 2026-09-07, and it is
	// the reason the case exists.
	if held, _ := s.Judge(planned(task("a", "r1"), task("b", "r1", "a"))); held {
		t.Error("a two-task chain was scored as independent work")
	}
}

func TestNoTaskExceedsTheCeiling_ReadsTheLadder(t *testing.T) {
	s := noTaskExceedsTheGoalsRiskCeiling("r2")

	if held, detail := s.Judge(planned(task("a", "r1"), task("b", "r2"))); !held {
		t.Errorf("tasks at and below the ceiling were refused: %s", detail)
	}
	held, detail := s.Judge(planned(task("a", "r1"), task("b", "r3")))
	if held {
		t.Error("an r3 task under an r2 ceiling was accepted. That is work the executor " +
			"refuses, so the plan stops half way waiting for an approval nobody can give.")
	}
	if !strings.Contains(detail, "b=r3") {
		t.Errorf("the offending task is not named: %s", detail)
	}
	// A tier this build does not recognise counts as ABOVE. A risk that cannot
	// be read is not one to assume is safe.
	if held, _ := s.Judge(planned(task("a", "urgent"))); held {
		t.Error("a task whose risk tier is not a tier was assumed to be inside the ceiling")
	}
}

func TestTheQuestionScorersAreEachOthersControl(t *testing.T) {
	asked := &Observation{Plan: &agent.PlanResult{ClarificationNeeded: "which enclosure?"}}
	planned3 := planned(task("a", "r1"), task("b", "r1"))

	refuse := anUnderspecifiedGoalIsQuestionedRatherThanGuessed()
	control := aWellSpecifiedGoalIsNotQuestioned()

	if held, _ := refuse.Judge(asked); !held {
		t.Error("a question about an underspecified goal was scored as a failure")
	}
	if held, _ := refuse.Judge(planned3); held {
		t.Error("planning on an invented premise was scored as a success")
	}
	// And the control inverts, which is the whole point of the pair: without it
	// a planner that questions everything scores 100% and never plans.
	if held, _ := control.Judge(asked); held {
		t.Error("a question about a well-specified goal was scored as a success. The pair " +
			"must invert, or a planner that asks about everything looks perfect.")
	}
	if held, _ := control.Judge(planned3); !held {
		t.Error("a plan for a well-specified goal was scored as a failure")
	}
}

// A refused plan is scored, not skipped.
//
// Plan() rejects a model's output that would deadlock or name a task that does
// not exist. Recording that as a failed RUN would exclude it from scoring and
// turn the suite's worst outcome into no outcome at all.
func TestARefusedPlanIsJudgedRatherThanSkipped(t *testing.T) {
	refused := &Observation{PlanRefused: "the plan contains a dependency cycle: a → b → a"}
	held, detail := aPlanIsAcceptedByTheHarness().Judge(refused)
	if held {
		t.Error("a plan FORGE refused to execute was scored as acceptable")
	}
	if !strings.Contains(detail, "cycle") {
		t.Errorf("the refusal's reason was not carried into the report: %s", detail)
	}
}

// A plan that asks a question is ACCEPTED by the harness — Validate allows no
// tasks when a clarification is set — and must be scored that way, or the
// refusal-to-guess case would report its own success as a failure.
func TestAQuestionCountsAsAnAcceptablePlan(t *testing.T) {
	asked := &Observation{Plan: &agent.PlanResult{ClarificationNeeded: "which bracket?"}}
	if held, detail := aPlanIsAcceptedByTheHarness().Judge(asked); !held {
		t.Errorf("a planner asking a question was scored as producing an unusable plan: %s",
			detail)
	}
}
