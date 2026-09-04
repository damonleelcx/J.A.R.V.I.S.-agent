package workspace_test

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
)

// Every recorded change puts its artifact in the project graph (PRD WRK-03).
//
// KindArtifact existed to hold files since the workspace model was written and
// nothing produced one, so every artifact this system recorded belonged to no
// graph — reachable only by an id somebody already had, and invisible to a
// canvas that starts from a project.

func TestRecordingAChangeAnchorsItInTheGraph(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	art, _, err := h.svc.RecordChange(ctx, workspace.Change{
		ProjectID: h.project, Path: "cmd/forged/main.go", InitiatorID: h.userID,
		Agent: workspace.AgentConverse, Diff: "@@ -1 +1 @@", Summary: "wired the thing",
	})
	if err != nil {
		t.Fatal(err)
	}

	g, err := h.svc.Load(ctx, h.project)
	if err != nil {
		t.Fatal(err)
	}
	var found *workspace.Node
	for i := range g.Nodes {
		if g.Nodes[i].Kind == workspace.KindArtifact {
			if ref, ok := g.Nodes[i].AnchorRef(); ok && ref == art.ID {
				found = &g.Nodes[i]
			}
		}
	}
	if found == nil {
		t.Fatal("a recorded change left its artifact out of the project graph.\n" +
			"Nothing can then list a project's files: the only way to reach one is an id " +
			"somebody already has, which is not a starting point any surface has")
	}
	// The path, so a traversal reads as the file rather than as "artifact art_…".
	if found.Title != "cmd/forged/main.go" {
		t.Errorf("the anchor is titled %q; an artifact is identified BY its path, so the "+
			"title cannot go stale and should be the thing a person recognises", found.Title)
	}
	if found.CreatedBy != h.userID {
		t.Errorf("the anchor is attributed to %q, not to whoever's intent it serves", found.CreatedBy)
	}
}

// A second change to the same file reuses the anchor rather than adding one.
//
// Two anchors for one artifact makes every traversal return its edges twice,
// and neither anchor is the wrong one to delete.
func TestASecondChangeReusesTheSameAnchor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, _, err := h.svc.RecordChange(ctx, workspace.Change{
			ProjectID: h.project, Path: "internal/thing.go", InitiatorID: h.userID,
			Agent: workspace.AgentConverse, Diff: "@@", Summary: "again",
		}); err != nil {
			t.Fatal(err)
		}
	}
	g, err := h.svc.Load(ctx, h.project)
	if err != nil {
		t.Fatal(err)
	}
	var anchors int
	for i := range g.Nodes {
		if g.Nodes[i].Kind == workspace.KindArtifact {
			anchors++
		}
	}
	if anchors != 1 {
		t.Fatalf("three changes to one file produced %d anchors; every traversal would return "+
			"that file's edges once per anchor", anchors)
	}
}

// Anchoring shares the caller's transaction, so a change and its anchor are one
// fact.
//
// The recovery CreateNode does — insert, and on a unique violation read the
// winner — is correct outside a transaction and wrong inside one: a failed
// statement aborts the whole transaction in Postgres, so the recovery read
// fails too and the caller loses the version it was writing. EnsureAnchor uses
// ON CONFLICT DO NOTHING for that reason, and this drives it through the real
// transactional path twice to prove the second pass does not poison anything.
func TestAnchoringDoesNotPoisonTheTransactionItRunsIn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, _, err := h.svc.RecordChange(ctx, workspace.Change{
		ProjectID: h.project, Path: "a/b.go", InitiatorID: h.userID,
		Agent: workspace.AgentConverse, Summary: "one",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The second call hits the conflict path inside its own transaction. If that
	// aborted the transaction, this version would not exist afterwards.
	_, v2, err := h.svc.RecordChange(ctx, workspace.Change{
		ProjectID: h.project, Path: "a/b.go", InitiatorID: h.userID,
		Agent: workspace.AgentConverse, Summary: "two",
	})
	if err != nil {
		t.Fatalf("the second change to an already-anchored file failed: %v\n"+
			"An anchor insert that errors rather than doing nothing takes the artifact "+
			"version down with it", err)
	}
	hist, err := h.svc.History(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist.Versions) != 2 {
		t.Fatalf("the artifact has %d versions; the second one was rolled back with its anchor",
			len(hist.Versions))
	}
	if v2.Version != 2 {
		t.Errorf("the second version is numbered %d", v2.Version)
	}
}

// An artifact recorded in a caller-owned transaction is anchored in the same
// one — the path geometry takes (PRD VIS-04).
func TestRecordChangeInAnchorsInTheCallersTransaction(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	art, _, err := h.svc.RecordChangeIn(ctx, tx, workspace.Change{
		ProjectID: h.project, Path: "renders/bracket.json", InitiatorID: h.userID,
		Agent: workspace.AgentConverse, Kind: workspace.ArtifactModel, Summary: "a render",
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	// Rolled back: the anchor must go with the version, or a graph would list a
	// file whose history does not exist.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	g, err := h.svc.Load(ctx, h.project)
	if err != nil {
		t.Fatal(err)
	}
	for i := range g.Nodes {
		if ref, ok := g.Nodes[i].AnchorRef(); ok && ref == art.ID {
			t.Fatal("a rolled-back change left an anchor behind, so the graph lists a file " +
				"with no history")
		}
	}
	if !strings.Contains(art.Path, "bracket") {
		t.Fatalf("unexpected artifact %q", art.Path)
	}
}

// The backfill anchors artifacts that predate anchoring, and does it once.
//
// # Why this needs its own fence
//
// RecordChange anchoring what it records fixes the future. Every artifact
// written before that belongs to no graph, and a listing that shows the new
// ones and silently omits the old is the worse failure: nothing distinguishes
// "this project has no files" from "these files predate the fix".
//
// The migration ran at harness setup, before this test wrote anything — so this
// creates the pre-fix state by hand, applies the migration's own SQL, and then
// applies it again. Reading the shipped file rather than restating it is the
// point: a fence that carries its own copy of the SQL proves the copy works.
func TestTheBackfillAnchorsOlderArtifactsExactlyOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	sqlBytes, err := db.Files.ReadFile(db.MigrationsDir + "/0019_anchor_existing_artifacts.sql")
	if err != nil {
		t.Fatalf("the backfill is not in the shipped migration chain: %v", err)
	}

	// An artifact and a version written straight to the tables: exactly the
	// shape RecordChange left behind before it anchored.
	artID := id.New(id.PrefixArtifact)
	if _, err := h.pool.Exec(ctx, `
		insert into forge_artifacts (id, project_id, path, kind, created_at, updated_at)
		values ($1,$2,'legacy/part.scad','file',now(),now())`, artID, h.project); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `
		insert into forge_artifact_versions
			(id, artifact_id, version, initiator_id, agent, inputs, diff,
			 verification_state, verification_note, human_disposition, created_at)
		values ($1,$2,1,$3,'converse','{}','', 'unverified','','pending',now())`,
		id.New(id.PrefixVersion), artID, h.userID); err != nil {
		t.Fatal(err)
	}

	count := func() int {
		var n int
		if err := h.pool.QueryRow(ctx,
			`select count(*) from forge_nodes where kind = 'artifact' and artifact_id = $1`,
			artID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(); got != 0 {
		t.Fatalf("the pre-fix artifact already has %d anchor(s); this test proves nothing", got)
	}

	if _, err := h.pool.Exec(ctx, string(sqlBytes)); err != nil {
		t.Fatalf("the backfill failed: %v", err)
	}
	if got := count(); got != 1 {
		t.Fatalf("after the backfill the artifact has %d anchors, expected 1", got)
	}

	// Re-runnable: every migration in this chain has to be, and a data migration
	// that inserts is the one where that is easy to get wrong.
	if _, err := h.pool.Exec(ctx, string(sqlBytes)); err != nil {
		t.Fatalf("the backfill is not re-runnable: %v", err)
	}
	if got := count(); got != 1 {
		t.Errorf("a second run produced %d anchors; a duplicate anchor makes every traversal "+
			"return that file's edges twice", got)
	}

	// And what it wrote is true rather than convenient.
	var title, how, createdBy string
	if err := h.pool.QueryRow(ctx,
		`select title, how, created_by from forge_nodes where kind='artifact' and artifact_id=$1`,
		artID).Scan(&title, &how, &createdBy); err != nil {
		t.Fatal(err)
	}
	if title != "legacy/part.scad" {
		t.Errorf("the anchor is titled %q rather than the path a person would recognise", title)
	}
	if how != string(claim.Observed) {
		t.Errorf("the anchor is labelled %q; the file demonstrably exists, so this is not a guess", how)
	}
	if createdBy != h.userID {
		t.Errorf("the anchor is attributed to %q rather than to the initiator of the artifact's "+
			"first version; an invented attribution is worse than none", createdBy)
	}
}

// An artifact with no versions is skipped rather than attributed to somebody.
func TestTheBackfillWillNotInventAnAttribution(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	sqlBytes, err := db.Files.ReadFile(db.MigrationsDir + "/0019_anchor_existing_artifacts.sql")
	if err != nil {
		t.Fatal(err)
	}
	artID := id.New(id.PrefixArtifact)
	if _, err := h.pool.Exec(ctx, `
		insert into forge_artifacts (id, project_id, path, kind, created_at, updated_at)
		values ($1,$2,'orphan.txt','file',now(),now())`, artID, h.project); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, string(sqlBytes)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.pool.QueryRow(ctx,
		`select count(*) from forge_nodes where kind='artifact' and artifact_id=$1`, artID).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("an artifact with no versions was anchored, which means created_by was filled " +
			"with somebody who did not make it")
	}
}
