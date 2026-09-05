package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The geometry HTTP surface, on the wire.
//
// # Why this exists now
//
// Wave 11 put a re-derivation endpoint in front of the binding layer and wave
// 12's panel reads its response by name — b.variant.version, b.caveats[].detail.
// The domain is covered by its own integration tests and the ROUTE by a mount
// check, which between them still leave the one thing a browser depends on
// unproven: the shape that comes back. A renamed field breaks the panel and
// nothing else, and nothing else would notice.

type geoHarness struct {
	h       *GeometryHandlers
	svc     *geometry.Service
	owner   *identity.User
	other   *identity.User
	project string
	cad     *cad.Kernel
}

func geometryHarness(t *testing.T) *geoHarness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. " +
			"Run `make db-up` then `make test-integration`.")
	}
	ctx := context.Background()
	const schema = "forge_http_geometry"

	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 6, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	admin, err := db.Connect(ctx, cfg(url), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "drop schema if exists "+schema+" cascade"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := db.Connect(ctx, cfg(url+sep+"search_path="+schema), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	d := testDeps()
	d.Pool = pool
	d.Clock = clock.System{}
	// The real router wires this; a harness calling handlers directly must too,
	// or every permission check refuses and the tests pass for the wrong reason.
	d.Access = access.NewService(pool, d.Clock, logx.Discard())

	now := time.Now().UTC()
	mk := func(email string) *identity.User {
		u := &identity.User{ID: id.New(id.PrefixUser), Email: email}
		if _, err := pool.Exec(ctx, `
			insert into forge_users (id, email, display_name, status, password_hash, password_algo,
				password_changed_at, created_at, updated_at)
			values ($1,$2,'T','active','x','argon2id',$3,$3,$3)`, u.ID, u.Email, now); err != nil {
			t.Fatal(err)
		}
		return u
	}
	// The CAD kernel, when this machine has one. Unset is the default and the
	// tests below cover both: with a kernel STEP is written, without one it is
	// refused with the sentence that fixes it.
	d.CAD = cad.New(os.Getenv("FORGE_CAD_PYTHON"), logx.Discard())
	t.Cleanup(d.CAD.Close)

	g := &geoHarness{h: NewGeometryHandlers(d), svc: geometry.NewService(pool, d.Clock, logx.Discard()),
		cad: d.CAD}
	g.owner = mk("geo-owner@example.com")
	g.other = mk("geo-intruder@example.com")
	g.project = newProject(t, pool, d.Access, g.owner.ID, "P", now)
	return g
}

// save stores the parametric bracket whose rib follows its plate.
func (g *geoHarness) save(t *testing.T) *geometry.Variant {
	t.Helper()
	v, err := g.svc.Save(context.Background(), geometry.NewVariant{
		ProjectID: g.project, InitiatorID: g.owner.ID,
		Agent: workspace.AgentConverse, Generator: "claude-opus-5",
		Inputs: map[string]any{"message": "a bracket"},
		Document: geometry.Document{
			Name: "bracket", Units: "mm",
			Parameters: []geometry.Parameter{
				{Name: "plate_size", Value: 60, Unit: "mm", How: geometry.Chosen},
				{Name: "fillet_radius", Value: 3, Unit: "mm", How: geometry.Chosen},
				{Name: "bolt_circle", Value: 31, Unit: "mm",
					How: geometry.FromStandard, Source: "NEMA 17"},
			},
			Derived: []geometry.Derived{
				{Name: "rib_length", Expression: "plate_size - 2 * fillet_radius",
					Why: "the rib follows the plate"},
			},
			Parts: []geometry.Part{
				{ID: "plate", Name: "Plate", Shape: "box",
					Size:     map[string]float64{"width": 60, "height": 5, "depth": 60},
					SizeFrom: map[string]string{"width": "plate_size", "depth": "plate_size"},
					Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
				{ID: "rib", Name: "Rib", Shape: "box",
					Size:     map[string]float64{"width": 54, "height": 10, "depth": 6},
					SizeFrom: map[string]string{"width": "rib_length"},
					Position: []float64{0, 7, 15}, Rotation: []float64{0, 0, 0}},
			},
			Assumptions: []string{"60 mm plate, chosen"},
			NotVerified: []string{"nothing analysed"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The response the panel reads, field by field. Every name asserted here is one
// the browser dereferences; renaming any of them breaks the panel silently.
func TestAPI_RespecReturnsTheShapeThePanelReads(t *testing.T) {
	g := geometryHarness(t)
	v := g.save(t)

	rec := httptest.NewRecorder()
	g.h.Respec(rec, withPath(req(g.owner, "POST",
		"/v1/geometry/"+v.VersionID+"/respec", `{"parameters":{"plate_size":100}}`), v.VersionID))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Variant struct {
			VersionID string            `json:"version_id"`
			Version   int               `json:"version"`
			Name      string            `json:"name"`
			Document  geometry.Document `json:"document"`
		} `json:"variant"`
		Caveats []struct {
			Severity string `json:"severity"`
			Name     string `json:"name"`
			Detail   string `json:"detail"`
		} `json:"caveats"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Variant.Name != "bracket" || body.Variant.Version != 2 {
		t.Errorf("variant = %q v%d, want bracket v2", body.Variant.Name, body.Variant.Version)
	}
	if body.Variant.VersionID == v.VersionID {
		t.Error("the response points at the SOURCE version; the panel would offer to re-derive the old one")
	}
	if body.Note == "" {
		t.Error("no note; the panel tells the person nothing was accepted by making this")
	}
	// caveats must be [] and never null: the panel does caveats.length.
	if !strings.Contains(rec.Body.String(), `"caveats":[`) {
		t.Errorf("caveats is not an array: %s", rec.Body.String())
	}

	// And the geometry actually moved.
	byID := map[string]geometry.Part{}
	for _, p := range body.Variant.Document.Parts {
		byID[p.ID] = p
	}
	if got := byID["plate"].Size["width"]; got != 100 {
		t.Errorf("plate width = %g, want 100", got)
	}
	if got := byID["rib"].Size["width"]; got != 94 {
		t.Errorf("rib width = %g, want 94 — the rib must follow the plate", got)
	}
}

// The panel shows what a parameter was quoted from, so GET has to carry it.
func TestAPI_GetCarriesTheParametersAndTheirProvenance(t *testing.T) {
	g := geometryHarness(t)
	v := g.save(t)

	rec := httptest.NewRecorder()
	g.h.Get(rec, withPath(req(g.owner, "GET", "/v1/geometry/"+v.VersionID, ""), v.VersionID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Variant struct {
			Document geometry.Document `json:"document"`
		} `json:"variant"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Variant.Document.Parameters) != 3 {
		t.Fatalf("%d parameters reached the client; the panel would have nothing to show",
			len(body.Variant.Document.Parameters))
	}
	var found bool
	for _, p := range body.Variant.Document.Parameters {
		if p.Name == "bolt_circle" {
			found = true
			if p.How != geometry.FromStandard || p.Source != "NEMA 17" {
				t.Errorf("the recalled figure lost its provenance on the wire: how=%q source=%q",
					p.How, p.Source)
			}
		}
	}
	if !found {
		t.Error("bolt_circle did not survive to the client")
	}
	if len(body.Variant.Document.Derived) != 1 {
		t.Error("the derived expression did not reach the client, so the panel cannot show " +
			"what follows from what")
	}
}

// A caller who may not write to the project is told the variant does not exist,
// rather than that it exists and is not theirs.
func TestAPI_RespecRefusesSomebodyElsesVariant(t *testing.T) {
	g := geometryHarness(t)
	v := g.save(t)

	rec := httptest.NewRecorder()
	g.h.Respec(rec, withPath(req(g.other, "POST",
		"/v1/geometry/"+v.VersionID+"/respec", `{"parameters":{"plate_size":100}}`), v.VersionID))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "permission") || strings.Contains(rec.Body.String(), "forbidden") {
		t.Errorf("the refusal reveals that the variant exists: %s", rec.Body.String())
	}
}

// An override naming nothing is a client mistake and must not append a copy.
func TestAPI_RespecRefusesAnOverrideThatWouldChangeNothing(t *testing.T) {
	g := geometryHarness(t)
	v := g.save(t)

	for _, body := range []string{
		`{"parameters":{"plaet_size":100}}`,
		`{"parameters":{"rib_length":99}}`,
		`{"parameters":{}}`,
	} {
		rec := httptest.NewRecorder()
		g.h.Respec(rec, withPath(req(g.owner, "POST",
			"/v1/geometry/"+v.VersionID+"/respec", body), v.VersionID))
		if rec.Code == http.StatusCreated {
			t.Errorf("%s was accepted and appended an identical copy", body)
		}
	}
}

// withPath sets the {id} path value the handler reads. httptest.NewRequest does
// not populate it; without this every handler looks up the empty string and the
// tests pass or fail for a reason that has nothing to do with the code.
func withPath(r *http.Request, id string) *http.Request {
	r.SetPathValue("id", id)
	return r
}

// A parametric export, end to end: a stored variant becomes a real B-Rep on the
// wire. This is the one path where every piece has to line up at once — the
// document, the shared reduction in geometry.Solids, the sidecar, and the header
// that tells a person what they are holding.
func TestAPI_ExportsARealSTEPFileWhenAKernelIsConfigured(t *testing.T) {
	g := geometryHarness(t)
	if !g.cad.Available() {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the parametric export path")
	}
	v := g.save(t)

	rec := httptest.NewRecorder()
	g.h.Export(rec, withPath(req(g.owner, "GET",
		"/v1/geometry/"+v.VersionID+"/export?format=step", ""), v.VersionID))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "ISO-10303-21;") {
		t.Fatalf("this is not a STEP file: %.60q", body)
	}
	if !strings.Contains(body, "END-ISO-10303-21;") {
		t.Error("the file is truncated")
	}
	// The header must not call a B-Rep tessellated. It is the only thing a
	// person sees at the moment of download, and here it would be wrong in the
	// direction that matters: a downstream tool is RIGHT to treat these surfaces
	// as exact, and must still know nothing about the shape was checked.
	label := rec.Header().Get("X-Forge-Export-Label")
	if strings.Contains(label, "tessellated") && !strings.Contains(label, "not tessellated") {
		t.Errorf("the label calls a B-Rep tessellated: %q", label)
	}
	if !strings.Contains(label, "has been analysed or checked") {
		t.Errorf("the label does not say the shape is unchecked: %q", label)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "model/step" {
		t.Errorf("content type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, ".step") {
		t.Errorf("content disposition %q", cd)
	}
}

// And the default configuration, which must never quietly become something else.
func TestAPI_RefusesSTEPWithoutAKernel(t *testing.T) {
	g := geometryHarness(t)
	v := g.save(t)

	// A handler wired exactly as a deployment with no FORGE_CAD_PYTHON.
	noKernel := *g.h
	rec := httptest.NewRecorder()
	withoutCAD(&noKernel).Export(rec, withPath(req(g.owner, "GET",
		"/v1/geometry/"+v.VersionID+"/export?format=step", ""), v.VersionID))

	if rec.Code == http.StatusOK {
		t.Fatal("a STEP file was produced with no kernel configured")
	}
	if !strings.Contains(rec.Body.String(), "FORGE_CAD_PYTHON") {
		t.Errorf("the refusal does not say how to get one: %s", rec.Body.String())
	}
}

// The formats list is what a person and a model both read to find out what this
// deployment can write, so it has to follow the kernel rather than the build.
func TestAPI_TheFormatsListFollowsTheKernel(t *testing.T) {
	g := geometryHarness(t)

	stepAvailable := func(h *GeometryHandlers) bool {
		rec := httptest.NewRecorder()
		h.Formats(rec, req(g.owner, "GET", "/v1/geometry/formats", ""))
		var body struct {
			Formats []struct {
				Name      string `json:"name"`
				Available bool   `json:"available"`
				Reason    string `json:"reason"`
			} `json:"formats"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, f := range body.Formats {
			if f.Name == "step" {
				if !f.Available && !strings.Contains(f.Reason, "FORGE_CAD_PYTHON") {
					t.Errorf("step is unavailable and the reason does not say how to fix it: %q", f.Reason)
				}
				return f.Available
			}
		}
		t.Fatal("step is not in the formats list at all; a person who cannot find it concludes " +
			"it was forgotten and a model invents something")
		return false
	}

	noKernel := *g.h
	if stepAvailable(withoutCAD(&noKernel)) {
		t.Error("step is advertised as available with no kernel configured")
	}
	if g.cad.Available() && !stepAvailable(g.h) {
		t.Error("step is advertised as unavailable with a kernel configured")
	}
}

// withoutCAD returns the handler as a deployment with no kernel would have it.
func withoutCAD(h *GeometryHandlers) *GeometryHandlers {
	h.deps.CAD = cad.New("", logx.Discard())
	return h
}

// A feature the kernel refused must be in the download label.
//
// This is the most dangerous thing the kernel can produce: a bracket whose
// fillet OCCT would not build looks like a bracket, downloads like a bracket,
// and has square corners where the design says rounded. Observed live on
// 2026-09-05 — a model asked for a 5 mm fillet on a 6 mm plate and the kernel
// said no, correctly, and the person holding the file had no way to know.
func TestAPI_ADroppedFeatureIsInTheDownloadLabel(t *testing.T) {
	g := geometryHarness(t)
	if !g.cad.Available() {
		t.Skip("FORGE_CAD_PYTHON is unset")
	}
	v, err := g.svc.Save(context.Background(), geometry.NewVariant{
		ProjectID: g.project, InitiatorID: g.owner.ID,
		Agent: workspace.AgentConverse, Generator: "test",
		Inputs: map[string]any{"message": "a bracket"},
		Document: geometry.Document{
			Name: "overfilleted", Units: "mm",
			Parts: []geometry.Part{{ID: "plate", Name: "Plate", Shape: "box",
				Size:     map[string]float64{"width": 60, "height": 6, "depth": 60},
				Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
			// 5 mm on a 6 mm plate: OCCT refuses, exactly as it did live.
			Features:    []geometry.Feature{{ID: "round-edges", Op: "fillet", Of: "plate", Radius: 5, Edges: "all"}},
			Assumptions: []string{"chosen"}, NotVerified: []string{"nothing checked"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	g.h.Export(rec, withPath(req(g.owner, "GET",
		"/v1/geometry/"+v.VersionID+"/export?format=step", ""), v.VersionID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	label := rec.Header().Get("X-Forge-Export-Label")
	if !strings.Contains(label, "could NOT be applied") {
		t.Fatalf("the refused fillet is not in the label, so the file says nothing about "+
			"having square corners where the design said rounded: %q", label)
	}
	if !strings.Contains(label, "round-edges") {
		t.Errorf("the label does not name the feature: %q", label)
	}
}
