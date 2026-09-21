package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The two STEP downloads say the same thing about what is NOT in the file.
//
// The same design is written two ways: in the request, from the kernel's
// cad.Build (stepExportLabel), and off-node by forge-worker, from the stored job
// (exportJobLabel). Found by PR 159: the job's label carried the reduced-round
// clause but not the mesh-only one, so a person who downloaded the worker's file
// was NOT told that a lattice had been left out of it, while a person who waited
// for the in-request file was. Both now read one source, exportLabelClauses.

// clauses is the part of an export label in front of the base sentence: what the
// design asked for and this file does not have. The base sentences differ on
// purpose (the job's adds that no interference check ran), so only this half can
// be compared.
func clauses(t *testing.T, label string) string {
	t.Helper()
	i := strings.Index(label, "unverified proposal")
	if i < 0 {
		t.Fatalf("an export label with no base sentence: %q", label)
	}
	return label[:i]
}

func TestExportLabel_BothDownloadsSayTheSameThingWasLeftOut(t *testing.T) {
	const infill, decor = "Infill", "Trim"
	reduced := "corners: the fillet of 45 mm did not build on all 4 edge group(s) — 1 edge near (30, 0, 30) at 22.5 mm"

	for _, c := range []struct {
		design   string
		meshOnly []string
		reduced  []string
		want     []string // substrings both labels must carry
		absent   []string // substrings neither label may carry
	}{
		{design: "no lattice and no reduction",
			absent: []string{"mesh-only", "SMALLER"}},
		{design: "a lattice only",
			meshOnly: []string{infill},
			want: []string{"1 mesh-only part(s) are NOT in this file: " + infill,
				geometry.MeshOnlyLabel},
			absent: []string{"SMALLER"}},
		{design: "a reduced round only",
			reduced: []string{reduced},
			want:    []string{"1 fillet(s) or chamfer(s) were built SMALLER", "22.5 mm"},
			absent:  []string{"mesh-only"}},
		{design: "a lattice and a reduced round",
			meshOnly: []string{infill, decor},
			reduced:  []string{reduced},
			want: []string{"2 mesh-only part(s) are NOT in this file: " + infill + ", " + decor,
				geometry.MeshOnlyLabel, "1 fillet(s) or chamfer(s) were built SMALLER", "22.5 mm"}},
	} {
		inRequest := stepExportLabel("v1", &cad.Build{MeshOnly: c.meshOnly, FeatureReductions: c.reduced})
		job := exportJobLabel(&agent.Export{VersionID: "v1", MeshOnly: c.meshOnly, FeatureReductions: c.reduced})
		if got, want := clauses(t, job), clauses(t, inRequest); got != want {
			t.Errorf("%s: the job's label says %q where the in-request one says %q", c.design, got, want)
		}
		for _, want := range c.want {
			if !strings.Contains(job, want) {
				t.Errorf("%s: the job's label does not say %q: %s", c.design, want, job)
			}
			if !strings.Contains(inRequest, want) {
				t.Errorf("%s: the in-request label does not say %q: %s", c.design, want, inRequest)
			}
		}
		for _, no := range c.absent {
			if strings.Contains(job, no) || strings.Contains(inRequest, no) {
				t.Errorf("%s: a label says %q about a design that has none: %s | %s",
					c.design, no, job, inRequest)
			}
		}
	}

	// A design with neither is the label it always was: the base sentence alone.
	if got := clauses(t, exportJobLabel(&agent.Export{VersionID: "v1"})); got != "" {
		t.Errorf("a design with nothing left out carries a clause: %q", got)
	}

	// And the status reply carries the names too, so the panel can say what the
	// header says. Always an array, never null.
	dto := toExportDTO(&agent.Export{ID: "e1", MeshOnly: []string{infill}})
	if len(dto.MeshOnly) != 1 || dto.MeshOnly[0] != infill {
		t.Errorf("the export status does not name the mesh-only part: %+v", dto.MeshOnly)
	}
	b, _ := json.Marshal(toExportDTO(&agent.Export{ID: "e2"}))
	if !strings.Contains(string(b), `"mesh_only":[]`) {
		t.Errorf("mesh_only is not an empty array on a job with none: %s", b)
	}

	// The export panel says it as well, named, as it does for the other clauses.
	js, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	code := codeOnly(string(js))
	if !strings.Contains(code, "var jobMeshOnly = exp.mesh_only || [];") ||
		!strings.Contains(code, "html += section(jobMeshOnly.length + ' mesh-only part(s) are NOT in this file (") {
		t.Error("the STEP export panel does not say which mesh-only parts were left out")
	}
}
