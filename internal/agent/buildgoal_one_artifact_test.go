package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// One artifact per build (decided 2026-09-15).
//
// A geometry version is stored against the artifact its document's NAME
// resolves to. #119 watched a build rename the model half way through and start
// a SECOND artifact — desk-lamp-base.forge.json v1, then desk-lamp.forge.json
// v1 — so one build's work appeared as two files, each holding half a history,
// and "the versions this build kept" stopped being one list.
//
// A build goal now pins the artifact its first kept step created. These are the
// properties that pin has to have, held against real Postgres because all of
// them are properties of rows: which artifact a version hangs off, how many
// artifacts a project ends up with, and what one goal can reach of another's.

const lampThreeSteps = `{"steps":[{"name":"base","what":"the base"},
	{"name":"stem","what":"the stem"},{"name":"shade","what":"the shade"}]}`

const lampTwoSteps = `{"steps":[{"name":"base","what":"the base"},{"name":"stem","what":"the stem"}]}`

// namedWhole is a whole-document reply that NAMES the model, the way a first
// step does.
func namedWhole(docName, partID, partName string) string {
	return `{"speech":"built ` + partName + `","prototype":{"name":"` + docName + `","units":"mm","parts":[
	  {"id":"` + partID + `","name":"` + partName + `","shape":"box","size":{"width":100,"height":100,"depth":100}}]}}`
}

// renamedAdd adds a part AND renames the model — what a build step does once it
// knows what it is making, and the thing that used to split the build in two.
func renamedAdd(docName, partID, partName string) string {
	return `{"speech":"added ` + partName + `","prototype_edit":{"patch":{"name":"` + docName + `","parts":[
	  {"id":"` + partID + `","name":"` + partName + `","shape":"box","size":{"width":100,"height":100,"depth":100}}]}}}`
}

// keptBy is the versions a goal's steps kept, in step order.
//
// Read from the TASK RESULTS rather than from the project's variants, because
// that is how the next step and the timeline find them, and because a project
// may hold another goal's work as well — which is exactly what one of the
// fences below is about.
func (h *buildHarness) keptBy(t *testing.T, goalID string) []geometry.Variant {
	t.Helper()
	var out []geometry.Variant
	for _, task := range h.tasks(t, goalID) {
		var done struct {
			Result buildStepResult `json:"result"`
		}
		if json.Unmarshal(task.Result, &done) != nil || done.Result.VersionID == "" {
			continue
		}
		v, err := h.geo.Find(context.Background(), done.Result.VersionID)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, *v)
	}
	return out
}

// modelArtifacts is how many artifacts a project holds, with their paths.
func (h *buildHarness) modelArtifacts(t *testing.T) []string {
	t.Helper()
	rows, err := h.pool.Query(context.Background(),
		`select path from forge_artifacts where project_id = $1 order by path`, h.project)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

// A three-step build whose steps rename the model keeps ONE artifact with three
// versions, each carrying its own document name.
func TestBuildGoal_ABuildThatRenamesTheModelKeepsOneArtifactWithAVersionPerStep(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	stub := &goalStub{tokens: 1, replies: []string{lampThreeSteps,
		namedWhole("desk lamp base", "base", "Base"),
		renamedAdd("desk lamp", "stem", "Stem"),
		renamedAdd("desk lamp with shade", "shade", "Shade")}}
	h.plan(t, goal, stub)

	if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalSucceeded) {
		t.Fatalf("the build ended %s", status)
	}

	kept := h.keptBy(t, goal.ID)
	if len(kept) != 3 {
		t.Fatalf("the build kept %d version(s) for three steps", len(kept))
	}
	for i, v := range kept {
		if v.ArtifactID != kept[0].ArtifactID {
			t.Errorf("step %d kept its version on artifact %s (%s) and step 1 kept its on %s (%s); "+
				"a build's history has to be ONE list, and the Files panel shows it where the artifact says",
				i+1, v.ArtifactID, v.Path, kept[0].ArtifactID, kept[0].Path)
		}
		if v.Version != i+1 {
			t.Errorf("step %d kept v%d; the steps of a build append in order", i+1, v.Version)
		}
	}
	// The rename is not swallowed by the pin: each version says what the model
	// was called when that step kept it.
	wantNames := []string{"desk lamp base", "desk lamp", "desk lamp with shade"}
	for i, want := range wantNames {
		if kept[i].Name != want || kept[i].Document.Name != want {
			t.Errorf("step %d's version is named %q (document %q), want %q — the step renamed the model "+
				"and the record has to say so", i+1, kept[i].Name, kept[i].Document.Name, want)
		}
	}
	// And the history keeps the path it was opened under, so the versions before
	// the rename do not move.
	if kept[0].Path != "geometry/desk-lamp-base.forge.json" {
		t.Errorf("the build's artifact is at %q, not the path its first step opened", kept[0].Path)
	}
	if paths := h.modelArtifacts(t); len(paths) != 1 {
		t.Errorf("the project holds %d artifact(s) for one build: %v", len(paths), paths)
	}
}

// A step whose predecessor kept NOTHING still creates the artifact.
//
// ‼️ The first step that keeps anything has no artifact to pin — there is no
// history yet — and it must fall back to the name rather than refuse. This is
// the case a pin applied unconditionally breaks, and it breaks it invisibly:
// the build would fail on its first save with an artifact that cannot be found.
func TestBuildGoal_AStepWhosePredecessorKeptNothingStillCreatesTheArtifact(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	// Step 1 answers with speech and no geometry: nothing to keep, and the step
	// still succeeds — the model was asked and it was paid for.
	stub := &goalStub{tokens: 1, replies: []string{lampTwoSteps,
		`{"speech":"I need to know the height before I can shape the base."}`,
		namedWhole("desk lamp", "stem", "Stem")}}
	h.plan(t, goal, stub)

	if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalSucceeded) {
		t.Fatalf("the build ended %s", status)
	}
	kept := h.keptBy(t, goal.ID)
	if len(kept) != 1 {
		t.Fatalf("%d version(s) kept; step 1 kept nothing and step 2 kept one", len(kept))
	}
	if kept[0].Version != 1 {
		t.Errorf("the first version this build kept is v%d", kept[0].Version)
	}
	if kept[0].Path != "geometry/desk-lamp.forge.json" {
		t.Errorf("step 2 opened the history at %q; with nothing kept before it, the model's name is "+
			"what names the artifact", kept[0].Path)
	}
}

// The pinned artifact is never ANOTHER GOAL'S.
//
// Two builds in one project, arranged so the name rule would send each into the
// other's history: the second build's first step is called what the first
// build's newest version is called, and its second step renames the model to
// what the first build's ARTIFACT is called. Each build keeps its own list
// anyway, because a step pins what its own predecessor kept and can pin nothing
// else.
func TestBuildGoal_AnotherGoalsArtifactIsNeverAppendedTo(t *testing.T) {
	h := newBuildHarness(t)

	first := h.goal(t, nil)
	stubOne := &goalStub{tokens: 1, replies: []string{lampTwoSteps,
		namedWhole("desk lamp base", "base", "Base"),
		renamedAdd("desk lamp", "stem", "Stem")}}
	h.plan(t, first, stubOne)
	if status := h.settle(t, h.worker(t, stubOne), first.ID); status != string(engine.GoalSucceeded) {
		t.Fatalf("the first build ended %s", status)
	}

	second := h.goal(t, nil)
	stubTwo := &goalStub{tokens: 1, replies: []string{lampTwoSteps,
		namedWhole("desk lamp", "post", "Post"),
		renamedAdd("desk lamp base", "arm", "Arm")}}
	h.plan(t, second, stubTwo)
	if status := h.settle(t, h.worker(t, stubTwo), second.ID); status != string(engine.GoalSucceeded) {
		t.Fatalf("the second build ended %s", status)
	}

	one, two := h.keptBy(t, first.ID), h.keptBy(t, second.ID)
	if len(one) != 2 || len(two) != 2 {
		t.Fatalf("the two builds kept %d and %d versions; each built two steps", len(one), len(two))
	}
	if one[0].ArtifactID == two[0].ArtifactID {
		t.Fatalf("both builds kept their versions on artifact %s (%s); one build's steps must not append "+
			"to another's history", one[0].ArtifactID, one[0].Path)
	}
	for i, v := range two {
		if v.ArtifactID != two[0].ArtifactID {
			t.Errorf("the second build's step %d landed on %s (%s) rather than on its own artifact %s (%s)",
				i+1, v.ArtifactID, v.Path, two[0].ArtifactID, two[0].Path)
		}
		if v.Version != i+1 {
			t.Errorf("the second build's step %d kept v%d; a build that wandered into another's history "+
				"would number from where that one left off", i+1, v.Version)
		}
	}
	if paths := h.modelArtifacts(t); len(paths) != 2 {
		t.Errorf("the project holds %d artifact(s) for two builds: %v", len(paths), paths)
	}
}
