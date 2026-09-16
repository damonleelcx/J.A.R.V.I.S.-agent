package geometry_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// What one stored design may be, at the storage door and against the real schema.
// Phase 3, stage S0 of docs/plan-2026-09-13-millions-of-parts.md: its acceptance is
// an integration test with a 30k-occurrence car document, and its stored size
// recorded.

// limitsFor sets the process's geometry limits for one test and puts them back.
func limitsFor(t *testing.T, l geometry.Limits) {
	t.Helper()
	was := geometry.CurrentLimits()
	geometry.SetLimits(l)
	t.Cleanup(func() { geometry.SetLimits(was) })
}

// loggedService is the harness's service with its log captured.
func loggedService(h *harness) (*geometry.Service, *bytes.Buffer) {
	var buf bytes.Buffer
	return geometry.NewService(h.pool, h.clk, logx.New(logx.Options{Output: &buf})), &buf
}

// logLine returns the first JSON log record with the given message, or nil.
func logLine(t *testing.T, buf *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec["msg"] == msg {
			return rec
		}
	}
	return nil
}

// thirtyThousand is a car-sized design written the way D1 lets one be written:
// a handful of definitions and assemblies, and 30,000 placed parts.
func thirtyThousand(n geometry.NewVariant) geometry.NewVariant {
	rivet := geometry.Part{ID: "rivet", Name: "Rivet", Shape: "cylinder",
		Size:   map[string]float64{"radius": 2, "height": 6},
		Repeat: &geometry.Repeat{Count: 500, Offset: []float64{25, 0, 0}}}
	skin := geometry.Part{ID: "skin", Name: "Skin panel", Shape: "box",
		Size: map[string]float64{"width": 12500, "height": 2, "depth": 180}}
	n.Document.Parts = nil
	n.Document.Definitions = []geometry.Part{rivet, skin}
	n.Document.Assemblies = []geometry.Assembly{
		{ID: "seam", Children: []geometry.Child{{ID: "skin", Ref: "skin"}, {ID: "rivet", Ref: "rivet"}}},
		{ID: "car", Children: []geometry.Child{{ID: "seam", Ref: "seam",
			Pattern: &geometry.Pattern{Kind: "linear", Count: 60, Offset: []float64{0, 0, 200}}}}},
	}
	n.Document.Root = "car"
	return n
}

func TestSizeFence_AThirtyThousandOccurrenceCarIsStoredAndItsSizeRecorded(t *testing.T) {
	h := newHarness(t)
	svc, logs := loggedService(h)
	ctx := context.Background()

	v, err := svc.Save(ctx, thirtyThousand(h.proposal("car")))
	if err != nil {
		t.Fatalf("a 30k-occurrence car was refused at the storage door: %v", err)
	}
	back, err := svc.Find(ctx, v.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	// 60 seams, each a skin panel and 500 rivets.
	if n := len(back.Document.PlacedParts()); n != 60*501 {
		t.Fatalf("the stored design places %d parts, want %d", n, 60*501)
	}
	// Built nowhere yet, and drawn in the viewport since Phase 6, stage W1.
	if back.Document.DrawRefusal() == "" {
		t.Error("a 30k design came back without a building refusal; the kernel would try to build it")
	}
	if r := back.Document.ViewportRefusal(); r != "" {
		t.Errorf("a 30k design came back refused by the viewport, which draws it instanced: %q", r)
	}

	rec := logLine(t, logs, string(logx.EventGeometrySaved))
	if rec == nil {
		t.Fatalf("no %s record was logged:\n%s", logx.EventGeometrySaved, logs.String())
	}
	stored, _ := json.Marshal(back.Document)
	if b, _ := rec["bytes"].(float64); b <= 0 || b > float64(2*len(stored)) {
		t.Errorf("the saved record's bytes is %v; the stored document is %d bytes", rec["bytes"], len(stored))
	}
	if rec["definitions"] != float64(2) || rec["occurrences"] != float64(60*501) {
		t.Errorf("the saved record says %v definitions and %v occurrences; want 2 and %d",
			rec["definitions"], rec["occurrences"], 60*501)
	}
	t.Logf("stored size of a %d-occurrence car: %v bytes", 60*501, rec["bytes"])
}

func TestSizeFence_RefusesADesignOverTheByteCeiling(t *testing.T) {
	h := newHarness(t)
	svc, logs := loggedService(h)
	limitsFor(t, geometry.Limits{MaxDocumentBytes: 4096})

	n := h.proposal("bracket")
	for i := 0; i < 60; i++ {
		n.Document.Parts = append(n.Document.Parts, plate("plate-"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26)), 60))
	}
	_, err := svc.Save(context.Background(), n)
	if err == nil || errs.CodeOf(err) != errs.CodeValidationFailed || !strings.Contains(err.Error(), "FORGE_GEOMETRY_MAX_DOCUMENT_BYTES") {
		t.Fatalf("a design over a 4 KiB ceiling was not refused naming the setting: %v", err)
	}
	if list, _ := svc.List(context.Background(), h.project, 10); len(list) != 0 {
		t.Errorf("the refused design was stored anyway: %d variant(s)", len(list))
	}
	if rec := logLine(t, logs, string(logx.EventGeometryTooLarge)); rec == nil || rec["limit_bytes"] != float64(4096) {
		t.Errorf("the refusal was not logged with its threshold: %v\n%s", rec, logs.String())
	}
}

func TestSizeFence_RefusesMoreDefinitionsThanTheCeiling(t *testing.T) {
	h := newHarness(t)
	svc, _ := loggedService(h)
	limitsFor(t, geometry.Limits{MaxDefinitions: 3})

	n := h.proposal("frame")
	n.Document.Parts = nil
	n.Document.Root = "frame"
	var children []geometry.Child
	for _, id := range []string{"a", "b", "c", "d"} {
		n.Document.Definitions = append(n.Document.Definitions, plate(id, 10))
		children = append(children, geometry.Child{ID: id, Ref: id})
	}
	n.Document.Assemblies = []geometry.Assembly{{ID: "frame", Children: children}}
	_, err := svc.Save(context.Background(), n)
	if err == nil || errs.CodeOf(err) != errs.CodeValidationFailed || !strings.Contains(err.Error(), "FORGE_GEOMETRY_MAX_DEFINITIONS") {
		t.Fatalf("four definitions over a ceiling of three were not refused naming the setting: %v", err)
	}
}
