package workspace_test

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Recording who a raised ceiling rests on (0021_project_review_authority.sql).
//
// # Why this write is fenced harder than the others
//
// Everything else about the pack NARROWS what may happen. This widens it, on the
// strength of something this build cannot check. The rules that keep it honest
// are all about attribution: a claim with no author raises nothing, a domain
// with no raised ceiling refuses the claim outright, and clearing is as easy as
// setting.

func civilProject(t *testing.T, h *harness) string {
	t.Helper()
	id, err := h.svc.EnsureProject(context.Background(), h.pool, "", h.userID,
		"Bridge study", "Civil engineering")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRecordReviewAuthority_RoundTripsAndClears(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := civilProject(t, h)

	if a, err := h.svc.ReviewAuthorityFor(ctx, h.pool, id); err != nil {
		t.Fatal(err)
	} else if a.Recorded() {
		t.Fatal("a fresh project already has an authority recorded")
	}

	if err := h.svc.RecordReviewAuthority(ctx, h.pool, id,
		"R. Okonkwo", "CEng MICE 481920", h.userID); err != nil {
		t.Fatalf("recording: %v", err)
	}
	a, err := h.svc.ReviewAuthorityFor(ctx, h.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Recorded() || a.Holder != "R. Okonkwo" || a.RecordedBy != h.userID {
		t.Fatalf("read back %+v", a)
	}
	if a.RecordedAt.IsZero() {
		t.Error("the claim carries no time, so nobody can say when responsibility was accepted")
	}

	// Clearing is the way back down, and it must be as easy as the way up: a
	// mechanism that raises a ceiling and cannot lower it is one nobody should
	// switch on.
	if err := h.svc.RecordReviewAuthority(ctx, h.pool, id, "", "", ""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if a, _ := h.svc.ReviewAuthorityFor(ctx, h.pool, id); a.Recorded() {
		t.Errorf("the claim survived being cleared: %+v", a)
	}
}

// A claim with no author is refused.
//
// The database check constraint refuses the row and this refuses it earlier with
// a reason. Both matter: the constraint is what makes it impossible, and the
// message is what tells somebody why.
func TestRecordReviewAuthority_RefusesAnUnattributedClaim(t *testing.T) {
	h := newHarness(t)
	id := civilProject(t, h)

	err := h.svc.RecordReviewAuthority(context.Background(), h.pool, id, "R. Okonkwo", "", "")
	if err == nil {
		t.Fatal("an authority was recorded with nobody attesting to it.\n" +
			"A raised ceiling resting on a value with no author is a ceiling resting on nobody")
	}
	if !strings.Contains(err.Error(), "attributed") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
}

// A domain with no raised ceiling refuses the claim rather than storing it inert.
//
// Storing it would be worse than refusing: the project would carry a record that
// looks exactly like one raising a ceiling, and nothing would have changed.
func TestRecordReviewAuthority_RefusesADomainThatOffersNoRaisedCeiling(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	id, err := h.svc.EnsureProject(ctx, h.pool, "", h.userID, "Sketch", "Other")
	if err != nil {
		t.Fatal(err)
	}
	err = h.svc.RecordReviewAuthority(ctx, h.pool, id, "R. Okonkwo", "", h.userID)
	if err == nil {
		t.Fatal("an authority was recorded on a project whose domain nothing raises")
	}
	if errs.CodeOf(err) != errs.CodeForbidden {
		t.Errorf("refused with %s; this is a policy answer", errs.CodeOf(err))
	}
	if a, _ := h.svc.ReviewAuthorityFor(ctx, h.pool, id); a.Recorded() {
		t.Error("the refused claim was stored anyway")
	}
}
