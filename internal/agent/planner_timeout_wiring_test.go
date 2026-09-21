package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
)

// Every place that builds a planner gives it the planner's own timeout.
//
// # Why a fence and not a comment
//
// FORGE_PLANNER_REQUEST_TIMEOUT exists because the planner's single call was
// measured at 128 s against a 180 s bound shared with every other call (GitHub
// issue 13). A setting is only worth the sites that pass it: when this was
// first written the setting was loaded, validated, documented and reached
// nothing, because Planner.WithRequestTimeout had no callers. That is the exact
// failure this holds shut — a fifth construction site added without the
// forwarder would be a deployment where the planner quietly goes back to the
// general bound, and nothing would say so until it timed out.
//
// # Why it reads the source rather than the built program
//
// The thing being held is "no call site forgot", and a call site that forgot is
// not reachable from a test: it is reachable from a deployment. Parsing is
// crude and deliberate — it names the file and line, which is what somebody
// adding the fifth site needs.
//
// Test files are skipped: a test that builds an intake to exercise something
// else is not a deployment path, and requiring the call there would make the
// fence noise rather than signal.

// plannerBuilders are the constructors that produce a planner. A call to one of
// these, in non-test code, must be chained with the matching forwarder.
var plannerBuilders = map[string]string{
	"agent.NewIntake(":  "WithPlannerRequestTimeout(",
	"agent.NewPlanner(": "WithRequestTimeout(",
	// Unqualified, for call sites inside package agent itself.
	"NewPlanner(": "WithRequestTimeout(",
}

func TestEverySiteThatBuildsAPlannerGivesItThePlannersOwnTimeout(t *testing.T) {
	root := filepath.Join("..", "..")

	// The one call that is allowed to carry no timeout: NewIntake's own, which
	// is what WithPlannerRequestTimeout then configures. A constructor cannot
	// call its own forwarder.
	const insideTheConstructor = "internal/agent/intake.go"

	var checked int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".cadvenv", "docs":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(path),
			filepath.ToSlash(root)+"/"))
		if rel == insideTheConstructor {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			for builder, forwarder := range plannerBuilders {
				if !strings.Contains(line, builder) {
					continue
				}
				// The declaration of a constructor is not a call of it.
				if strings.HasPrefix(strings.TrimSpace(line), "func "+builder) {
					continue
				}
				// Qualified and unqualified forms overlap; the qualified one is
				// the more specific and is checked on its own pass.
				if builder == "NewPlanner(" && strings.Contains(line, "agent.NewPlanner(") {
					continue
				}
				checked++
				// The forwarder may be on this line or on the chained lines
				// after it. Six is past the longest chain in the tree today and
				// well short of the next unrelated statement.
				window := strings.Join(lines[i:min(i+6, len(lines))], "\n")
				if !strings.Contains(window, forwarder) {
					t.Errorf("%s:%d builds a planner with %s and never calls %s.\n"+
						"That deployment's planner is bounded by the general "+
						"FORGE_LLM_REQUEST_TIMEOUT, which the planner is measured to "+
						"overrun; it will read as the request hanging. Chain %s on.",
						rel, i+1, builder, forwarder, forwarder)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	// A fence that found nothing to check is a fence that has stopped working —
	// a rename of either constructor would otherwise make this pass silently.
	if checked < 4 {
		t.Fatalf("only %d planner construction site(s) found; there are at least four "+
			"(the goal endpoints, `goal new`, `goal plan` and the evaluation harness). "+
			"Either a constructor was renamed and this fence no longer matches it, or "+
			"planning moved and this test is checking nothing.", checked)
	}
}

// And the forwarder actually reaches the planner rather than being a setter
// onto a copy.
func TestIntake_ThePlannersOwnTimeoutReachesThePlannerItWillUse(t *testing.T) {
	in := &Intake{planner: NewPlanner(nil, persona.DefaultCharacter())}

	if got := in.planner.requestTimeout; got != 0 {
		t.Fatalf("a fresh planner already has a timeout of %s; zero is the default that "+
			"means 'the general bound, exactly as before'", got)
	}

	in = in.WithPlannerRequestTimeout(7 * time.Minute)
	if got := in.planner.requestTimeout; got != 7*time.Minute {
		t.Errorf("the intake's planner has a timeout of %s after being given 7m; the "+
			"forwarder configured something the intake will not use", got)
	}
}
