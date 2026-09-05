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
//
// # Why the industry labels are in here (2026-09-04)
//
// The list used to be software, general and SOFTWARE. Every engineering domain
// was refused at this call — a mechanical engineer could not open a project to
// sketch a bracket, because the build cannot certify one. That conflated a
// domain with the riskiest action inside it; see internal/domain/pack.
//
// The names are the ones the product's industry selector SHOWS, not the pack
// ids, because that is what a person picks and the round trip from label to
// created project is the thing that either works or does not. The pack-level
// fence cannot observe it: it proves the table says yes, and this proves the
// only producer of projects agrees.
func TestEnsureProject_AcceptsEveryIndustryTheProductOffers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for _, pack := range []string{
		"Mechanical engineering", "Manufacturing", "Automotive", "Aerospace",
		"Civil engineering", "Electrical engineering", "Construction",
		"Product design", "Architecture", "Other",
		// The two the selector does not offer, and a case variant, which is what
		// this test asserted before industries existed.
		"software", "general", "SOFTWARE",
	} {
		id, err := h.svc.EnsureProject(ctx, h.pool, "", h.userID, "Project "+pack, pack)
		if err != nil {
			t.Errorf("%q is an industry this product offers and it was refused: %v", pack, err)
			continue
		}
		if id == "" {
			t.Errorf("%s produced no project id", pack)
		}
	}
}

// The row records the pack ID, whatever spelling created it.
//
// Lookup is forgiving on purpose — a person may type the label the selector
// showed them — and `pack` carries no check constraint (0003_workspace.sql), so
// nothing but this write stops the column collecting one domain under three
// spellings. Every future reader of the pack would then need to know all of
// them, which is how a single source of truth stops being one.
func TestEnsureProject_StoresTheCanonicalPackID(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for _, tc := range []struct{ given, want string }{
		{"Mechanical engineering", "mechanical"},
		{"Product design", "product-design"},
		{"Other", "general"},
		{"SOFTWARE", "software"},
	} {
		id, err := h.svc.EnsureProject(ctx, h.pool, "", h.userID, "Project "+tc.given, tc.given)
		if err != nil {
			t.Errorf("%q was refused: %v", tc.given, err)
			continue
		}
		var stored string
		if err := h.pool.QueryRow(ctx,
			`select pack from forge_projects where id = $1`, id).Scan(&stored); err != nil {
			t.Fatalf("reading back the project created as %q: %v", tc.given, err)
		}
		if stored != tc.want {
			t.Errorf("a project created as %q records pack %q; expected the canonical %q.\n"+
				"The column has no constraint, so a non-canonical value here is one every "+
				"reader of the pack would have to know about", tc.given, stored, tc.want)
		}
	}
}
