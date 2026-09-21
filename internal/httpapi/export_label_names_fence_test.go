package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
)

// Both STEP downloads NAME the parts FORGE could not build.
//
// geometry/export.go's own label promises the reader, in the STEP format's lossy
// list, that "a part the kernel cannot build is left out of the file, and the
// download names it in its X-Forge-Export-Label header". Both downloads gave a
// count instead — "3 part(s) could not be built and are NOT in this file" — which
// tells somebody the file is wrong without telling them which part to go and
// look for. A count is the least actionable honest thing a label can say.
//
// The names come out of exportLabelClauses, the one place either label's clauses
// are written (PR 160), so the in-request download and the off-node job's cannot
// name different parts, and neither can go back to counting alone.
func TestExportLabel_BothDownloadsNameThePartsTheyCouldNotBuild(t *testing.T) {
	const infill = "Infill"
	// What the kernel actually puts in Build.Skipped: the part, then why
	// (cad/sidecar.py writes "%s: %s" % (label or id, reason)).
	refused := []string{
		"Bracket: the fillet of 12 mm is larger than the 6 mm wall it rounds",
		"Hub: no profile closed",
	}
	// Five of them, to see the cap: three named, then " and 2 more".
	many := []string{"P1: a", "P2: b", "P3: c", "P4: d", "P5: e"}
	manyMesh := []string{"L1", "L2", "L3", "L4", "L5"}

	for _, c := range []struct {
		design   string
		skipped  []string
		meshOnly []string
		want     []string // substrings BOTH labels must carry
		absent   []string // substrings NEITHER label may carry
	}{
		{design: "neither",
			absent: []string{"could not be built", "mesh-only"}},

		{design: "skipped parts only",
			skipped: refused,
			want: []string{
				"2 part(s) could not be built and are NOT in this file: " +
					refused[0] + "; " + refused[1] + "; ",
			},
			absent: []string{"mesh-only", " and 0 more"}},

		{design: "mesh-only parts only",
			meshOnly: []string{infill},
			want:     []string{"1 mesh-only part(s) are NOT in this file: " + infill + " ("},
			absent:   []string{"could not be built"}},

		{design: "both",
			skipped:  refused,
			meshOnly: []string{infill},
			want: []string{
				"1 mesh-only part(s) are NOT in this file: " + infill + " (",
				"2 part(s) could not be built and are NOT in this file: " + refused[0],
				refused[1],
			}},

		{design: "more of each than the cap allows",
			skipped:  many,
			meshOnly: manyMesh,
			want: []string{
				"5 part(s) could not be built and are NOT in this file: " +
					many[0] + "; " + many[1] + "; " + many[2] + " and 2 more; ",
				"5 mesh-only part(s) are NOT in this file: " +
					manyMesh[0] + ", " + manyMesh[1] + ", " + manyMesh[2] + " and 2 more (",
			},
			// The cap is a cap: past the third name nothing is said but the count.
			absent: []string{many[3], many[4], manyMesh[3], manyMesh[4]}},
	} {
		inRequest := stepExportLabel("v1", &cad.Build{Skipped: c.skipped, MeshOnly: c.meshOnly})
		job := exportJobLabel(&agent.Export{VersionID: "v1", Skipped: c.skipped, MeshOnly: c.meshOnly})

		// Clause for clause: the base sentences differ on purpose (the job's says
		// no interference check ran), so only the half in front can be compared.
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
				t.Errorf("%s: a label says %q about a design that has none of it: %s | %s",
					c.design, no, job, inRequest)
			}
		}
	}

	// The cap is exactly three: a fourth name is the one that is dropped.
	three := []string{"A: x", "B: y", "C: z"}
	if got := exportLabelClauses(nil, three, nil, nil); !strings.Contains(got, "C: z") ||
		strings.Contains(got, "more") {
		t.Errorf("three refused parts are not all named, or are capped early: %q", got)
	}
	four := append(append([]string{}, three...), "D: w")
	if got := exportLabelClauses(nil, four, nil, nil); strings.Contains(got, "D: w") ||
		!strings.Contains(got, "and 1 more") {
		t.Errorf("a fourth refused part is not folded into \"and N more\": %q", got)
	}

	// The feature-failure clause has always named its own, so nothing about it
	// changed here — but it must not have stopped, and the two labels must still
	// agree on it.
	fails := []string{"fillet f1 on Bracket: OCCT refused"}
	inRequest := stepExportLabel("v1", &cad.Build{FeatureFailures: fails})
	job := exportJobLabel(&agent.Export{VersionID: "v1", FeatureFailures: fails})
	if !strings.Contains(inRequest, fails[0]) || !strings.Contains(job, fails[0]) {
		t.Errorf("a feature that could not be applied is no longer named:\n%s\n%s", inRequest, job)
	}
	if got, want := clauses(t, job), clauses(t, inRequest); got != want {
		t.Errorf("feature failures: the job's label says %q where the in-request one says %q", got, want)
	}

	// And the names the header carries are the names the stored job HAS: the
	// report column the worker writes already holds them (agent/stepexport.go,
	// exportReport.skipped), so what the status reply shows and what the header
	// says come off the same field.
	dto := toExportDTO(&agent.Export{ID: "e1", Skipped: refused})
	if fmt.Sprint(dto.Skipped) != fmt.Sprint(refused) {
		t.Errorf("the export status does not carry the refused parts: %+v", dto.Skipped)
	}
}
