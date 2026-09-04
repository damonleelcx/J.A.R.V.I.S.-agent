package workspace_test

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The pack boundary, at the one place a project is created (PRD SEC-07).
//
// # Why this is here and not only in internal/domain/pack
//
// Those tests assert the table says medicine is unavailable. They pass whether
// or not anything reads the table — which is exactly how the column got into
// the state this closes: `pack` was required to be non-empty and then consulted
// by nothing, so a project marked `medical` recorded that a medical rule set
// applied while no rule anywhere applied it.
//
// EnsureProject is the only producer of projects in this build, so it is where
// the boundary either exists or does not.

func TestEnsureProject_RefusesAPackThisBuildIsNotValidatedFor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, err := h.svc.EnsureProject(ctx, h.pool, "", h.userID, "Patient planning", "medical")
	if err == nil {
		t.Fatal("a project was created in the medical pack.\n" +
			"PRD SEC-07 asks that regulated deployments stay inside validated intended use, and " +
			"the carve-out says patient-specific use is not enabled by this codebase — a project " +
			"that exists in that pack makes both statements false")
	}
	if errs.CodeOf(err) != errs.CodeForbidden {
		t.Errorf("refused with %s; a validated-use boundary is a permission answer, not a "+
			"malformed-input one", errs.CodeOf(err))
	}
	// The refusal has to explain itself. Somebody reading it needs to know
	// whether they hit policy or a bug, and what would change the answer.
	for _, want := range []string{"validated", "clinician", "software"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// An unknown pack is refused too, and differently.
//
// It used to be accepted: the column took any non-empty string, so a typo
// produced a project whose declared rule set did not exist. That reads in the
// record exactly like a project with rules.
func TestEnsureProject_RefusesAPackThatIsNotOne(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, err := h.svc.EnsureProject(ctx, h.pool, "", h.userID, "Typo", "sofware")
	if err == nil {
		t.Fatal("a project was created in a pack that does not exist")
	}
	if errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Errorf("refused with %s; an unrecognised name is bad input, not a policy answer",
			errs.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "software") {
		t.Errorf("the refusal does not say what IS available: %v", err)
	}
}

// And the packs this build implements still work, which is the half that makes
// the boundary bearable rather than an outage.
func TestEnsureProject_AcceptsThePacksThisBuildImplements(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for _, pack := range []string{"software", "general", "SOFTWARE"} {
		id, err := h.svc.EnsureProject(ctx, h.pool, "", h.userID, "Project "+pack, pack)
		if err != nil {
			t.Errorf("%s is available and was refused: %v", pack, err)
			continue
		}
		if id == "" {
			t.Errorf("%s produced no project id", pack)
		}
	}
}
