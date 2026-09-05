package workspace_test

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
)

// The pack's first validator: what this DOMAIN expects, checked against the graph.
//
// # Why this is the pack's job and not a fixed list
//
// "Incomplete" is a property of a graph IN A DOMAIN. A civil project with no
// recorded load case is missing the thing every number in it should rest on; a
// software project is missing nothing by having none. A single list would nag
// every project about kinds most will never use, or ask so little it never
// noticed the one that mattered.
//
// # Why gaps and never defects
//
// A project part-way through legitimately has them. A validator that turned an
// unfinished project red is one people learn to run with the flag that hides it,
// and then it is not a validator at all — the same reasoning `standards.go`
// followed when it chose to label a recalled figure rather than refuse the turn.

func reviewOf(t *testing.T, h *harness, projectID string) *workspace.Review {
	t.Helper()
	rev, err := h.svc.Review(context.Background(), projectID)
	if err != nil {
		t.Fatalf("reviewing: %v", err)
	}
	return rev
}

func gapProblems(rev *workspace.Review) string {
	var out []string
	for _, g := range rev.Gaps {
		out = append(out, g.Problem)
	}
	return strings.Join(out, " ")
}

// An empty civil project is told what civil work records.
func TestReview_ReportsWhatTheDomainExpects(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	id, err := h.svc.EnsureProject(ctx, h.pool, "", h.userID, "Bridge study", "Civil engineering")
	if err != nil {
		t.Fatal(err)
	}
	rev := reviewOf(t, h, id)

	// Civil expects requirement, constraint, component, hazard, evidence.
	for _, want := range []string{
		"domain-expects-requirement", "domain-expects-hazard", "domain-expects-evidence",
	} {
		if !strings.Contains(gapProblems(rev), want) {
			t.Errorf("a civil project with an empty graph was not told about %q.\n"+
				"gaps: %s", want, gapProblems(rev))
		}
	}
	if !rev.Sound() {
		t.Error("an unfinished project was reported as UNSOUND. A gap is an absence and a " +
			"defect is a contradiction; conflating them makes every young project look broken")
	}
	// The wording has to say which domain is asking, or a reader cannot tell
	// whether this is a universal rule or their project's.
	var detail string
	for _, g := range rev.Gaps {
		if g.Problem == "domain-expects-hazard" {
			detail = g.Detail
		}
	}
	if !strings.Contains(detail, "Civil engineering") {
		t.Errorf("the gap does not name the domain asking for it: %q", detail)
	}
}

// A domain expects less of a project that has what it asks for.
func TestReview_TheGapClosesWhenTheNodeExists(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	id, err := h.svc.EnsureProject(ctx, h.pool, "", h.userID, "Bridge study", "Civil engineering")
	if err != nil {
		t.Fatal(err)
	}
	before := gapProblems(reviewOf(t, h, id))
	if !strings.Contains(before, "domain-expects-hazard") {
		t.Fatalf("setup: expected a hazard gap, got %s", before)
	}

	if _, err := h.svc.Add(ctx, workspace.NewNode{
		ProjectID: id, Kind: workspace.KindHazard, Title: "Scour at the pier",
		How: claim.Inferred, CreatedBy: h.userID,
	}); err != nil {
		t.Fatal(err)
	}
	if after := gapProblems(reviewOf(t, h, id)); strings.Contains(after, "domain-expects-hazard") {
		t.Errorf("the hazard gap survived a hazard being recorded: %s", after)
	}
}

// Domains differ, which is the whole point.
//
// If software and civil produced the same gaps, the pack would be decorative
// here and a fixed list would do the same job.
func TestReview_DifferentDomainsExpectDifferentThings(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	civil, err := h.svc.EnsureProject(ctx, h.pool, "", h.userID, "Bridge", "Civil engineering")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := h.svc.EnsureProject(ctx, h.pool, "", h.userID, "Service", "software")
	if err != nil {
		t.Fatal(err)
	}
	c, s := gapProblems(reviewOf(t, h, civil)), gapProblems(reviewOf(t, h, soft))
	if c == s {
		t.Fatalf("civil and software produced identical expectations (%s), so the domain "+
			"is not deciding anything", c)
	}
	if !strings.Contains(c, "domain-expects-hazard") {
		t.Error("civil does not expect a hazard record")
	}
	if strings.Contains(s, "domain-expects-hazard") {
		t.Error("software is being asked for a hazard record")
	}
	if !strings.Contains(s, "domain-expects-interface") {
		t.Error("software does not expect an interface")
	}
}
