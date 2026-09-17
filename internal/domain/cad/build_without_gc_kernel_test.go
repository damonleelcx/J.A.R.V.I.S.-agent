package cad_test

import (
	"testing"
)

// A build with Python's cycle collector paused (one million after; sidecar.py,
// _BUILD_WITHOUT_GC). docs/spikes/2026-09-17-one-million-after.

type gcRuns struct {
	Error string `json:"error"`
	Rows  []struct {
		Request                     string `json:"request"`
		Parts                       int    `json:"parts"`
		Found                       int    `json:"found"`
		CollectionsRunning          int    `json:"collections_running"`
		CollectionsPaused           int    `json:"collections_paused"`
		UnreachableAfterPausedBuild int    `json:"unreachable_after_paused_build"`
		Identical                   bool   `json:"identical"`
	} `json:"rows"`
	Problems []string `json:"problems"`
}

// The same request answers the same with the collector paused — in every format, with
// clashes, a feature, a feature that fails and a part that cannot be built — the
// collector does not run during the build, a paused build leaves nothing for it to
// collect, and it is running again afterwards however the build ended, unless the
// caller had paused it.
//
// testdata/build_without_gc.py.
func TestKernel_ABuildWithTheCycleCollectorPausedAnswersTheSameAndRestoresIt(t *testing.T) {
	var got gcRuns
	testdataJSON(t, "build_without_gc.py", &got)
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	if len(got.Rows) != 4 {
		t.Fatalf("%d request(s) compared, want 4 (format \"\", mesh, step, export job)", len(got.Rows))
	}
	for _, r := range got.Rows {
		t.Logf("%s: %d parts, %d clashes; collector ran %d time(s) running, %d paused; %d unreachable after; identical %v",
			r.Request, r.Parts, r.Found, r.CollectionsRunning, r.CollectionsPaused, r.UnreachableAfterPausedBuild, r.Identical)
		// The count must be able to see a collection, or "0 while paused" proves nothing.
		if r.CollectionsRunning == 0 {
			t.Errorf("%s: the collector never ran even with it on; the fixture is too small to show the pause", r.Request)
		}
	}
	for _, p := range got.Problems {
		t.Error(p)
	}
}
