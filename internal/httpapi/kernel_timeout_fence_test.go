package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad/cadtest"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A kernel build that takes too long says so on the wire, on every endpoint that
// builds with the kernel: 504 CAD_KERNEL_TIMEOUT, not 501 "no working backend in
// this deployment". On main those are the built mesh and the STEP export.
//
// ‼️ The 501 is what the 2026-09-15 ceiling spike (PR #95) got for a 30,023-part
// car after 66 s, and it sends whoever reads it to FORGE_CAD_PYTHON on a deployment
// whose kernel was working. docs/bugfix/2026-09-15-a-kernel-build-that-ran-out-of-time-was-reported-as-no-kernel.md
//
// The kernel is cadtest's fake, and the time that runs out is the request's own
// deadline: the kernel's 30 s limit cannot be shortened from outside the package,
// and both end in the same error (the cad fences cover the limit itself).
func TestAPI_AKernelBuildThatTakesTooLongIsA504ThatSaysSo(t *testing.T) {
	g := geometryHarness(t)
	python, _ := cadtest.FakeKernel(t)
	k := cad.New(python, logx.Discard())
	t.Cleanup(k.Close)
	g.h.deps.CAD = k

	doc := func(id string) geometry.Document {
		return geometry.Document{
			Name: "slow", Units: "mm",
			Parts: []geometry.Part{{ID: id, Name: "Block", Shape: "box",
				Size:     map[string]float64{"width": 10, "height": 10, "depth": 10},
				Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
			Assumptions: []string{"chosen"}, NotVerified: []string{"nothing checked"},
		}
	}
	v, err := g.svc.Save(context.Background(), geometry.NewVariant{
		ProjectID: g.project, InitiatorID: g.owner.ID,
		Agent: workspace.AgentConverse, Generator: "test",
		Inputs: map[string]any{"message": "a slow block"}, Document: doc(cadtest.Slow),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, target string
		serve        http.HandlerFunc
	}{
		{"mesh", "/v1/geometry/" + v.VersionID + "/mesh", g.h.Mesh},
		{"step export", "/v1/geometry/" + v.VersionID + "/export?format=step", g.h.Export},
	} {
		t.Run(c.name, func(t *testing.T) {
			// A process already running, so the deadline below is spent in the
			// build and not in starting a test binary as a kernel.
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := k.BuildMesh(ctx, doc("fine"), geometry.Millimetre); err != nil {
				t.Fatalf("warming the fake kernel: %v", err)
			}

			r := withPath(req(g.owner, "GET", c.target, ""), v.VersionID)
			deadline, stop := context.WithTimeout(r.Context(), 1500*time.Millisecond)
			defer stop()
			rec := httptest.NewRecorder()
			c.serve(rec, r.WithContext(deadline))

			var body struct {
				Error struct {
					Code      string `json:"code"`
					Message   string `json:"message"`
					Retryable bool   `json:"retryable"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("status %d, body %q: %v", rec.Code, rec.Body.String(), err)
			}
			if rec.Code != http.StatusGatewayTimeout || body.Error.Code != string(errs.CodeKernelTimeout) {
				t.Errorf("status %d %s, want %d %s: %s", rec.Code, body.Error.Code,
					http.StatusGatewayTimeout, errs.CodeKernelTimeout, rec.Body.String())
			}
			if !strings.Contains(body.Error.Message, "took too long") {
				t.Errorf("the message does not say the build took too long: %q", body.Error.Message)
			}
			if strings.Contains(body.Error.Message, "no working backend") {
				t.Errorf("a slow build reads as a missing kernel: %q", body.Error.Message)
			}
			if body.Error.Retryable {
				t.Error("a timeout is offered as retryable; the same build takes as long again")
			}
		})
	}
}
