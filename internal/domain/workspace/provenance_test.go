package workspace_test

import (
	"context"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
)

// Work generated FROM a requirement is joined to it (PRD VIS-01, WRK-03).
//
// # What was wrong before this
//
// The workbench's "build from" flow reads a requirement's own words into the
// turn, and the geometry that came back recorded those node ids in its inputs —
// a JSON blob nothing traverses. The graph itself held a column of requirements
// and a column of files with nothing between them, so "what was built from this
// requirement?" and "where did this shape come from?" had no answer that did not
// involve reading every version's inputs by hand.

func requirementIn(t *testing.T, h *harness, title string) *workspace.Node {
	t.Helper()
	n, err := h.svc.Add(context.Background(), workspace.NewNode{
		ProjectID: h.project, Kind: workspace.KindRequirement, Title: title,
		How: claim.Observed, CreatedBy: h.userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// edgesBetween returns the kinds of every edge from the artifact's anchor to n.
func edgesBetween(t *testing.T, h *harness, artifactID, toID string) []workspace.EdgeKind {
	t.Helper()
	g, err := h.svc.Load(context.Background(), h.project)
	if err != nil {
		t.Fatal(err)
	}
	anchor := ""
	for i := range g.Nodes {
		if ref, ok := g.Nodes[i].AnchorRef(); ok && ref == artifactID {
			anchor = g.Nodes[i].ID
		}
	}
	if anchor == "" {
		t.Fatalf("the artifact is not anchored in the graph, so nothing could point at it")
	}
	var kinds []workspace.EdgeKind
	for _, e := range g.Edges {
		if e.FromID == anchor && e.ToID == toID {
			kinds = append(kinds, e.Kind)
		}
	}
	return kinds
}

func TestGeneratingFromARequirementRecordsWhereItCameFrom(t *testing.T) {
	h := newHarness(t)
	req := requirementIn(t, h, "must bolt to a 40mm hole pattern")

	art, _, err := h.svc.RecordChange(context.Background(), workspace.Change{
		ProjectID: h.project, Path: "geometry/bracket.forge.json",
		Kind:        workspace.ArtifactModel,
		InitiatorID: h.userID, Agent: workspace.AgentConverse,
		DerivedFrom: []string{req.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := edgesBetween(t, h, art.ID, req.ID)
	if len(got) == 0 {
		t.Fatal("geometry generated from a requirement is not joined to it.\n" +
			"The requirement's words went into the turn and the shape came back, and the graph " +
			"records neither direction of that: \"what was built from this?\" and \"where did " +
			"this come from?\" are both unanswerable by traversal.")
	}
	if len(got) != 1 || got[0] != workspace.EdgeDerivesFrom {
		t.Fatalf("expected exactly one derives_from edge, got %v", got)
	}
}

// It is provenance, and it must never become a claim that the work MEETS the
// requirement.
//
// # Why this fence exists rather than being obvious
//
// `satisfies` is the tempting edge and it is the wrong one. Nothing checked that
// the geometry meets the requirement — a model was told what the requirement
// said and proposed a shape. Recording `satisfies` would have the SYSTEM assert
// an unverified claim on its own behalf, which is what the split between
// Verification and Disposition exists to prevent and what PRD RSN-06 forbids in
// so many words. Anything traversing the graph afterwards would read the
// requirement as met because somebody once described it out loud.
//
// If a shape does turn out to meet a requirement, a person or a test says so,
// with the edge that means it.
func TestProvenanceIsDerivedFromAndNeverSatisfies(t *testing.T) {
	h := newHarness(t)
	req := requirementIn(t, h, "must survive a 200N load")

	art, _, err := h.svc.RecordChange(context.Background(), workspace.Change{
		ProjectID: h.project, Path: "geometry/beam.forge.json",
		Kind:        workspace.ArtifactModel,
		InitiatorID: h.userID, Agent: workspace.AgentConverse,
		DerivedFrom: []string{req.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range edgesBetween(t, h, art.ID, req.ID) {
		if k == workspace.EdgeSatisfies {
			t.Fatal("the build-from flow recorded `satisfies`.\n" +
				"Nothing checked that this geometry meets the requirement — a model was told what " +
				"the requirement said and proposed a shape. `satisfies` reads \"this meets that\", " +
				"so the system would be asserting an unverified claim on its own behalf and every " +
				"later traversal would read the requirement as met (PRD RSN-06, SAF-05).\n" +
				"Provenance is `derives_from`. Satisfaction is somebody's finding.")
		}
	}
}

// A second turn built from the same requirement must not destroy the version it
// was recording.
//
// # The trap this is for
//
// The edge already exists by then — same anchor, same requirement — and an
// ordinary insert would raise a unique violation. In Postgres a FAILED STATEMENT
// ABORTS THE WHOLE TRANSACTION, so "the relation already holds, carry on" is not
// something the caller can decide afterwards: the version, the timeline event
// and the anchor written beside it are already lost. This is exactly the trap
// EnsureAnchor was written for, one edge over, and it would have fired on the
// SECOND build from any requirement — the common case, not an edge case.
func TestBuildingFromTheSameRequirementTwiceKeepsBothVersions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	req := requirementIn(t, h, "must fit a NEMA 17 face")

	change := workspace.Change{
		ProjectID: h.project, Path: "geometry/mount.forge.json",
		Kind:        workspace.ArtifactModel,
		InitiatorID: h.userID, Agent: workspace.AgentConverse,
		DerivedFrom: []string{req.ID},
	}
	if _, _, err := h.svc.RecordChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	art, v2, err := h.svc.RecordChange(ctx, change)
	if err != nil {
		t.Fatalf("the second build from the same requirement failed, taking its version with it: %v\n"+
			"An edge that already holds must be a no-op, not a failed statement — a failed "+
			"statement aborts the transaction that was recording the change.", err)
	}
	if v2.Version != 2 {
		t.Fatalf("the second change recorded version %d; the first one is gone", v2.Version)
	}
	if got := edgesBetween(t, h, art.ID, req.ID); len(got) != 1 {
		t.Fatalf("the same relation is recorded %d times (%v); every traversal now counts it "+
			"once per turn somebody spent on it", len(got), got)
	}
}

// A node this project does not have is not linked, and the change still lands.
//
// # Why not simply fail
//
// The ids come from a client. A node in somebody else's project, or one deleted
// between the tick and the save, cannot be linked truthfully at all — and
// attempting it would fail a foreign key or the cross-project check INSIDE the
// transaction carrying the artifact version, so a stray id would cost somebody
// the work they just did. The resolvable ones are linked, the rest are dropped,
// and the drop is logged rather than silent.
func TestANodeThisProjectDoesNotHaveIsNotLinked(t *testing.T) {
	h := newHarness(t)
	real := requirementIn(t, h, "must be 3mm thick")

	art, version, err := h.svc.RecordChange(context.Background(), workspace.Change{
		ProjectID: h.project, Path: "geometry/washer.forge.json",
		Kind:        workspace.ArtifactModel,
		InitiatorID: h.userID, Agent: workspace.AgentConverse,
		DerivedFrom: []string{real.ID, id.New(id.PrefixNode)},
	})
	if err != nil {
		t.Fatalf("a stray node id cost the caller its artifact version: %v", err)
	}
	if version.Version != 1 {
		t.Fatalf("version %d; the change did not land", version.Version)
	}
	if got := edgesBetween(t, h, art.ID, real.ID); len(got) != 1 {
		t.Fatalf("the resolvable requirement was not linked either: %v", got)
	}
}

// Naming the artifact's own anchor is ignored rather than fatal.
//
// forge_edges carries a check constraint forbidding a self-edge, and a violated
// check is a failed statement — which would abort the transaction and lose the
// version. A caller naming the node the change is ABOUT has said something
// meaningless, not something worth destroying their work over.
func TestNamingTheArtifactsOwnAnchorIsIgnored(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	art, _, err := h.svc.RecordChange(ctx, workspace.Change{
		ProjectID: h.project, Path: "geometry/ring.forge.json",
		Kind:        workspace.ArtifactModel,
		InitiatorID: h.userID, Agent: workspace.AgentConverse,
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := h.svc.Load(ctx, h.project)
	if err != nil {
		t.Fatal(err)
	}
	anchor := ""
	for i := range g.Nodes {
		if ref, ok := g.Nodes[i].AnchorRef(); ok && ref == art.ID {
			anchor = g.Nodes[i].ID
		}
	}

	if _, _, err := h.svc.RecordChange(ctx, workspace.Change{
		ProjectID: h.project, Path: "geometry/ring.forge.json",
		Kind:        workspace.ArtifactModel,
		InitiatorID: h.userID, Agent: workspace.AgentConverse,
		DerivedFrom: []string{anchor},
	}); err != nil {
		t.Fatalf("naming the change's own anchor lost the version: %v", err)
	}
}
