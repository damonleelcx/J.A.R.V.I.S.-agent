package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Where there is a CAD kernel, the STEP label is the kernel's, not a refusal.
//
// Found asking every export route as a viewer through the real forged
// (docs/spikes/2026-09-17-unverified-paths): /v1/geometry/formats said STEP was
// available, GET .../export?format=step returned the file, and GET .../export/label?
// format=step answered 501 "no CAD kernel configured". The workbench fetches the label
// before it shows a download link, so its enabled Export STEP button never offered one.
// docs/bugfix/2026-09-17-the-step-label-said-there-was-no-kernel-where-there-was-one.md
func TestAPI_TheSTEPLabelIsTheKernelsWhereThereIsAKernelAndARefusalWhereThereIsNone(t *testing.T) {
	x := newExportsHarness(t, newExportTestStore())
	v := x.save(t, exportPlate())

	label := func(d Deps) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := getAs(x.viewer, "/v1/geometry/"+v.VersionID+"/export/label?format=step")
		r.SetPathValue("id", v.VersionID)
		NewGeometryHandlers(d).ExportLabel(rec, r)
		return rec
	}

	withKernel := x.d
	// Available is all the label asks of the kernel: it builds nothing, so the
	// interpreter named here is never started.
	withKernel.CAD = cad.New("python-that-the-label-never-starts", logx.Discard())
	rec := label(withKernel)
	if rec.Code != http.StatusOK {
		t.Fatalf("the STEP label in a deployment with a kernel: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Label struct {
			FormatKind string   `json:"format_kind"`
			Headline   string   `json:"headline"`
			Lossy      []string `json:"lossy"`
			Tess       []any    `json:"tessellation"`
			NotVerif   []string `json:"not_verified"`
		} `json:"label"`
		Triangles int `json:"triangles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Label.FormatKind != "parametric" || !strings.Contains(body.Label.Headline, "B-Rep") {
		t.Errorf("the STEP label reads as a mesh's: kind %q, headline %q", body.Label.FormatKind, body.Label.Headline)
	}
	if strings.Contains(body.Label.Headline, "tessellated") || len(body.Label.Tess) != 0 || body.Triangles != 0 {
		t.Errorf("the STEP label describes tessellation the kernel does not do: %s", rec.Body.String())
	}
	joined := strings.Join(body.Label.Lossy, " ")
	if strings.Contains(joined, "Curved surfaces are gone") || !strings.Contains(joined, "millimetres") {
		t.Errorf("the STEP label's losses are a mesh's, or do not say the file is in millimetres: %q", joined)
	}
	if len(body.Label.NotVerif) == 0 {
		t.Errorf("the STEP label dropped the design's unverified list")
	}

	// And without a kernel the refusal stands, naming the setting.
	if rec := label(x.d); rec.Code != http.StatusNotImplemented {
		t.Errorf("the STEP label with no kernel: %d, want the 501 refusal: %s", rec.Code, rec.Body.String())
	}
}
