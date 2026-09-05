package geometry_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Geometry against a real database and the real migration chain.
//
// The unit tests hold the vocabularies and the arithmetic. These hold what only
// exists once there is somewhere to write: that a variant IS an artifact
// version, that the version and its geometry are one transaction, that
// successive proposals of the same assembly accumulate rather than scatter, and
// that the schema accepts every value Go believes it will.

type harness struct {
	pool    *db.Pool
	svc     *geometry.Service
	ws      *workspace.Service
	clk     *clock.Fake
	userID  string
	project string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}
	ctx := context.Background()
	schema := "forge_geo_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	if len(schema) > 60 {
		schema = schema[:60]
	}
	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 8, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	admin, err := db.Connect(ctx, cfg(url), logx.Discard())
	if err != nil {
		t.Fatalf("cannot reach the test database: %v", err)
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
		t.Fatalf("migrating the test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if c, err := db.Connect(context.Background(), cfg(url), logx.Discard()); err == nil {
			_, _ = c.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
			c.Close()
		}
	})

	clk := clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	h := &harness{pool: pool, clk: clk,
		svc: geometry.NewService(pool, clk, logx.Discard()),
		ws:  workspace.NewService(pool, clk, logx.Discard())}
	h.seed(t)
	return h
}

func (h *harness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	now := h.clk.Now()

	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	u := &identity.User{ID: id.New(id.PrefixUser), Email: "owner@example.com",
		Status: identity.StatusActive, PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id,
		PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := identity.NewRepository().CreateUser(ctx, h.pool, u); err != nil {
		t.Fatal(err)
	}
	h.userID = u.ID

	h.project = id.New(id.PrefixProject)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, created_at, updated_at) values ($1,$2,'P',$3,$3)`,
		h.project, h.userID, now); err != nil {
		t.Fatal(err)
	}
	if err := access.NewService(h.pool, h.clk, logx.Discard()).
		EnsureOwner(ctx, h.pool, h.project, h.userID); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) proposal(name string, parts ...geometry.Part) geometry.NewVariant {
	return geometry.NewVariant{
		ProjectID: h.project, InitiatorID: h.userID,
		Agent: workspace.AgentConverse, Generator: "claude-opus-5",
		Inputs: map[string]any{"message": "design me a " + name},
		Document: geometry.Document{
			Name: name, Units: "mm", Parts: parts,
			Assumptions: []string{"60 mm plate, chosen not given"},
			NotVerified: []string{"nothing here has been analysed"},
		},
	}
}

func plate(id string, width float64) geometry.Part {
	return geometry.Part{ID: id, Name: id, Shape: "box",
		Size:     map[string]float64{"width": width, "height": 5, "depth": width},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
}

// A variant IS an artifact version: it lands in forge_artifact_versions with
// WRK-04's seven facts, and the geometry hangs off it. If this ever stops being
// true, the whole reason there is no forge_variants table has gone with it.
func TestSave_AVariantIsAnArtifactVersion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	v, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 {
		t.Errorf("first proposal is version %d", v.Version)
	}
	if v.Path != "geometry/bracket.forge.json" {
		t.Errorf("path is %q", v.Path)
	}

	// The row is a real artifact version, readable through the artifact
	// lifecycle that knows nothing about geometry.
	hist, err := h.ws.History(ctx, v.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if hist.Artifact.Kind != workspace.ArtifactModel {
		t.Errorf("artifact kind is %q; geometry is a model", hist.Artifact.Kind)
	}
	if len(hist.Versions) != 1 {
		t.Fatalf("the lifecycle sees %d versions", len(hist.Versions))
	}
	got := hist.Versions[0]
	if got.Agent != workspace.AgentConverse {
		t.Errorf("agent is %q", got.Agent)
	}
	if got.ToolCallID != nil {
		t.Errorf("a conversational proposal named a tool call: %v", *got.ToolCallID)
	}
	if got.Verification != workspace.Unverified || got.Disposition != workspace.Pending {
		t.Errorf("a fresh variant starts %s/%s; both must be the honest default",
			got.Verification, got.Disposition)
	}
	var inputs map[string]any
	if err := json.Unmarshal(got.Inputs, &inputs); err != nil {
		t.Fatal(err)
	}
	if inputs["message"] != "design me a bracket" {
		t.Errorf("the version does not record what it was made from: %v", inputs)
	}
}

// Successive proposals of the same assembly accumulate as versions of ONE
// artifact. Without this there is no history to compare and "make it taller"
// produces an unrelated row.
func TestSave_TheSameAssemblyAccumulates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 72)))
	if err != nil {
		t.Fatal(err)
	}
	if first.ArtifactID != second.ArtifactID {
		t.Fatal("two proposals of the same assembly became two artifacts, so neither is a revision of the other")
	}
	if second.Version != 2 {
		t.Errorf("the second proposal is version %d", second.Version)
	}

	other, err := h.svc.Save(ctx, h.proposal("enclosure", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	if other.ArtifactID == first.ArtifactID {
		t.Error("an unrelated assembly was appended to the bracket's history")
	}
}

// The version and its geometry are ONE transaction. A version with no geometry
// behind it looks exactly like a normal row afterwards, which is the failure
// mode this path is arranged to avoid.
func TestSave_AVersionIsNeverWrittenWithoutItsGeometry(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A part id used twice is refused by Validate, so nothing at all should be
	// written — not the artifact, not a version, not a geometry row.
	bad := h.proposal("bracket", plate("p", 60), plate("p", 72))
	if _, err := h.svc.Save(ctx, bad); err == nil {
		t.Fatal("a variant with a duplicate part id was stored")
	}
	var versions int
	if err := h.pool.QueryRow(ctx, `select count(*) from forge_artifact_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 0 {
		t.Fatalf("%d version rows survive a refused save", versions)
	}

	// And on the happy path, every version of a model artifact has geometry.
	if _, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 60))); err != nil {
		t.Fatal(err)
	}
	var orphans int
	if err := h.pool.QueryRow(ctx, `
		select count(*) from forge_artifact_versions v
		  join forge_artifacts a on a.id = v.artifact_id
		 where a.kind = 'model'
		   and not exists (select 1 from forge_geometry g where g.version_id = v.id)`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("%d model versions have no geometry behind them", orphans)
	}
}

// A workbench conversation has no project until something needs keeping. The
// project is created through the one producer, WITH the membership row — without
// which the person who just made it cannot see it.
func TestSave_MakesAProjectTheCreatorCanActuallySee(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	n := h.proposal("bracket", plate("p", 60))
	n.ProjectID = ""
	v, err := h.svc.Save(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if v.ProjectID == "" || v.ProjectID == h.project {
		t.Fatalf("no new project was made: %q", v.ProjectID)
	}
	acc := access.NewService(h.pool, h.clk, logx.Discard())
	if err := acc.Require(ctx, v.ProjectID, h.userID, access.PermProjectRead); err != nil {
		t.Fatalf("the creator cannot read the project their variant went into: %v", err)
	}
	if err := acc.Require(ctx, v.ProjectID, h.userID, access.PermContentWrite); err != nil {
		t.Fatalf("the creator cannot add to the project they just created: %v", err)
	}
}

// Every unit Go can resolve must be one the schema accepts. Otherwise the two
// vocabularies drift and a perfectly valid variant fails to save in production
// on a unit no test happened to use.
func TestSave_EveryUnitGoResolvesIsAcceptedByTheSchema(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for _, declared := range []string{"mm", "cm", "m", "in", "inches", "furlongs", ""} {
		n := h.proposal("thing "+declared, plate("p", 60))
		n.Document.Units = declared
		v, err := h.svc.Save(ctx, n)
		if err != nil {
			t.Fatalf("a variant declaring %q could not be stored: %v", declared, err)
		}
		resolved, known := geometry.ParseUnit(declared)
		if v.Units != resolved {
			t.Errorf("%q resolved to %q on the way in and reads back as %q", declared, resolved, v.Units)
		}
		if !known && v.UnitsNote() == "" {
			t.Errorf("%q was stored as unspecified with no explanation for the reader", declared)
		}
		if v.UnitsDeclared != strings.TrimSpace(declared) {
			t.Errorf("what the model actually said (%q) was not kept; it reads back as %q",
				declared, v.UnitsDeclared)
		}
	}
}

// A comparison is derived from what is in the database, and it must refuse to
// span projects: authorisation is per project, so a set spanning two would be
// checked against one and disclose the other.
func TestCompare_RefusesToSpanProjects(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	mine, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := h.proposal("bracket", plate("p", 72))
	elsewhere.ProjectID = ""
	other, err := h.svc.Save(ctx, elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.Compare(ctx, []string{mine.VersionID, other.VersionID})
	if err == nil {
		t.Fatal("a comparison spanning two projects was produced; one permission check would cover both")
	}
	if !strings.Contains(err.Error(), "different projects") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// A comparison naming a variant that does not exist is an error, not a shorter
// list. Quietly dropping a column shows somebody a side-by-side of two things
// when they asked about three, and nothing on the screen says so.
func TestCompare_NamingAMissingVariantIsAnError(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	a, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 72)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Compare(ctx, []string{a.VersionID, b.VersionID}); err != nil {
		t.Fatalf("two real variants would not compare: %v", err)
	}
	_, err = h.svc.Compare(ctx, []string{a.VersionID, "ver_does_not_exist"})
	if errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("comparing against a missing variant returned %v", err)
	}
}

// Reading a variant back must give the document that was stored, byte for byte
// in meaning: the export and the viewport both replay it, and a lossy round trip
// would show somebody a different shape from the one they approved.
func TestSave_TheDocumentSurvivesTheRoundTrip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	n := h.proposal("bracket", plate("p", 60),
		geometry.Part{ID: "boss", Name: "pilot boss", Shape: "cylinder",
			Size:     map[string]float64{"radius": 11, "height": 6},
			Position: []float64{0, -3, 0}, Rotation: []float64{0, 1.5708, 0},
			Color: "#b8bcc4", Opacity: 1, Note: "locates the motor"})
	saved, err := h.svc.Save(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	read, err := h.svc.Find(ctx, saved.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(n.Document)
	got, _ := json.Marshal(read.Document)
	if string(want) != string(got) {
		t.Fatalf("the document changed in storage.\n want: %s\n  got: %s", want, got)
	}
	if read.Frame != geometry.FrameAssembly {
		t.Errorf("the coordinate frame did not travel with the geometry: %q", read.Frame)
	}
	if read.Generator != "claude-opus-5" {
		t.Errorf("the generator did not survive: %q", read.Generator)
	}
}

// A project's variants come back newest first, which is the order a person
// scans a list of "what did we try".
func TestList_NewestFirst(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for i, w := range []float64{50, 60, 72} {
		h.clk.Advance(time.Duration(i+1) * time.Minute)
		if _, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", w))); err != nil {
			t.Fatal(err)
		}
	}
	list, err := h.svc.List(ctx, h.project, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("%d variants", len(list))
	}
	if list[0].Version != 3 || list[2].Version != 1 {
		t.Errorf("order is v%d, v%d, v%d", list[0].Version, list[1].Version, list[2].Version)
	}
}

// ---------------------------------------------------------------------------
// Adopting an earlier variant (the carried defect wave 7 surfaced)
// ---------------------------------------------------------------------------

// Appending a version supersedes the previous one, and a superseded version can
// no longer be accepted or rejected. Correct for a file; wrong for variants,
// which are alternatives you choose between. This is the fence over the state
// that made the choosing impossible.
func TestAdopt_ASupersededVariantCannotBeRuledOnDirectly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 72))); err != nil {
		t.Fatal(err)
	}
	err = h.ws.Dispose(ctx, first.VersionID, workspace.Accepted, h.userID, "I preferred this one")
	if err == nil {
		t.Fatal("a superseded version was accepted directly; if this now works, Adopt exists for a " +
			"problem that has gone away and should be reconsidered rather than kept")
	}
}

// Adopting brings the chosen geometry forward as a NEW version, which a person
// can then rule on. The history stays append-only and "we went back to v1" is a
// fact the ledger records rather than one it hides.
func TestAdopt_BringsAnEarlierVariantForwardSoItCanBeAccepted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 72))); err != nil {
		t.Fatal(err)
	}

	adopted, err := h.svc.Adopt(ctx, first.VersionID, h.userID, "60 mm is the right plate")
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Version != 3 {
		t.Errorf("the adopted copy is v%d; adopting appends rather than reopening", adopted.Version)
	}
	if adopted.ArtifactID != first.ArtifactID {
		t.Error("the adopted copy landed on a different artifact, so it is not part of the same history")
	}

	// The geometry is the one that was chosen, byte for byte.
	want, _ := json.Marshal(first.Document)
	got, _ := json.Marshal(adopted.Document)
	if string(want) != string(got) {
		t.Fatalf("the adopted geometry differs from what was chosen.\n want: %s\n  got: %s", want, got)
	}

	// A human chose it; the generator still says what DREW it. Two facts, kept
	// apart.
	if adopted.Agent != workspace.AgentHuman {
		t.Errorf("adopting was recorded as %q; a person made this choice", adopted.Agent)
	}
	if adopted.Generator != first.Generator {
		t.Errorf("the generator changed to %q; adopting does not redraw anything", adopted.Generator)
	}

	// Bringing a variant forward is not a check. Inheriting a verdict would
	// assert that something had looked at the new row.
	if adopted.Verification != workspace.Unverified || adopted.Disposition != workspace.Pending {
		t.Errorf("the adopted copy starts %s/%s; it should be unverified and undecided",
			adopted.Verification, adopted.Disposition)
	}

	// And it names what it came from, or nobody can ask why v3 looks like v1.
	var inputs map[string]any
	if err := json.Unmarshal(adopted.Inputs, &inputs); err != nil {
		t.Fatal(err)
	}
	if inputs["adopted_from"] != first.VersionID {
		t.Errorf("the adopted copy does not name its source: %v", inputs)
	}
	if inputs["reason"] != "60 mm is the right plate" {
		t.Errorf("the reason was not recorded: %v", inputs)
	}

	// The whole point: it can now be accepted.
	if err := h.ws.Dispose(ctx, adopted.VersionID, workspace.Accepted, h.userID, "chosen after comparing"); err != nil {
		t.Fatalf("the adopted version could not be accepted, so adopting solved nothing: %v", err)
	}
}

// Adopting the version that is already current would append an identical row and
// supersede the thing it copied — a no-op dressed as a change.
func TestAdopt_RefusesTheVersionThatIsAlreadyCurrent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	only, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.Adopt(ctx, only.VersionID, h.userID, "")
	if errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("adopting the current version returned %v", err)
	}
	if !strings.Contains(err.Error(), "disposition") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}
}

// A design nobody chose has no authority behind it — the same rule as every
// other human decision in this system (PRD SAF-05).
func TestAdopt_MustNameWhoChoseIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Save(ctx, h.proposal("bracket", plate("p", 72))); err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.Adopt(ctx, first.VersionID, "", "no one")
	if err == nil {
		t.Fatal("an unattributed adoption was accepted")
	}
	// The MESSAGE matters, not only the refusal. Three things would reject this
	// — Adopt's own check, NewVariant.Validate, and the initiator_id foreign key
	// — and only the first says anything a caller can act on. Asserting the
	// wording is what makes this a fence over the guard rather than over the
	// schema that happens to sit behind it.
	if errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Errorf("the refusal came back as %s, which means it fell through to the database rather "+
			"than being caught where the reason can be explained: %v", errs.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "who chose it") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Re-specifying: the parameters actually drive the shape (wave 11)
// ---------------------------------------------------------------------------

// parametricProposal is a bracket whose rib FOLLOWS its plate, which is the
// arrangement the 2026-09-05 kernel spike found survives a size change.
func (h *harness) parametricProposal(name string) geometry.NewVariant {
	n := h.proposal(name)
	n.Document.Parameters = []geometry.Parameter{
		{Name: "plate_size", Value: 60, Unit: "mm", How: geometry.Chosen},
		{Name: "fillet_radius", Value: 3, Unit: "mm", How: geometry.Chosen},
	}
	n.Document.Derived = []geometry.Derived{
		{Name: "rib_length", Expression: "plate_size - 2 * fillet_radius",
			Why: "a rib that does not follow the plate overhangs it when the plate shrinks"},
	}
	n.Document.Parts = []geometry.Part{
		{ID: "plate", Name: "Plate", Shape: "box",
			Size:     map[string]float64{"width": 60, "height": 5, "depth": 60},
			SizeFrom: map[string]string{"width": "plate_size", "depth": "plate_size"},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		{ID: "rib", Name: "Rib", Shape: "box",
			Size:     map[string]float64{"width": 54, "height": 10, "depth": 6},
			SizeFrom: map[string]string{"width": "rib_length"},
			Position: []float64{0, 7, 15}, Rotation: []float64{0, 0, 0}},
	}
	return n
}

// The wave's whole point, through the real database: change one parameter and
// the geometry that comes back is a different shape.
func TestRespec_ChangingOneParameterMovesEveryDimensionThatFollowsIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.parametricProposal("bracket"))
	if err != nil {
		t.Fatal(err)
	}

	next, caveats, err := h.svc.Respec(ctx, first.VersionID, h.userID,
		map[string]float64{"plate_size": 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range caveats {
		if c.Severity == geometry.Error {
			t.Fatalf("%s: %s", c.Name, c.Detail)
		}
	}

	byID := map[string]geometry.Part{}
	for _, p := range next.Document.Parts {
		byID[p.ID] = p
	}
	if got := byID["plate"].Size["width"]; got != 100 {
		t.Errorf("plate width = %g, want 100", got)
	}
	if got := byID["rib"].Size["width"]; got != 94 {
		t.Errorf("rib width = %g, want 94 — the rib must follow the plate", got)
	}
	if byID["rib"].Size["width"] > byID["plate"].Size["width"] {
		t.Error("the rib overhangs the plate")
	}
	if got := byID["plate"].Size["height"]; got != 5 {
		t.Errorf("an UNBOUND dimension changed to %g; only what follows a parameter should move", got)
	}
}

// It has to survive the round trip. The binding expressions live in the stored
// document, so a variant read back from Postgres must still be re-specifiable —
// otherwise this works exactly once, in memory, and never again.
func TestRespec_TheBindingsSurviveStorage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.parametricProposal("bracket"))
	if err != nil {
		t.Fatal(err)
	}
	read, err := h.svc.Find(ctx, first.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := read.Document.Parts[1].SizeFrom["width"]; got != "rib_length" {
		t.Fatalf("the binding did not survive the database: size_from.width = %q", got)
	}
	if len(read.Document.Derived) != 1 || read.Document.Derived[0].Expression != "plate_size - 2 * fillet_radius" {
		t.Fatalf("the derived expression did not survive: %+v", read.Document.Derived)
	}
	// And re-specify FROM the stored copy, not the in-memory one.
	next, _, err := h.svc.Respec(ctx, read.VersionID, h.userID, map[string]float64{"plate_size": 40})
	if err != nil {
		t.Fatal(err)
	}
	if got := next.Document.Parts[1].Size["width"]; got != 34 {
		t.Errorf("rib width = %g after a round trip, want 34", got)
	}
}

// It appends, on the same artifact, so the two are a comparison rather than two
// unrelated designs (PRD VIS-04).
func TestRespec_AppendsAVersionOfTheSameArtifact(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.parametricProposal("bracket"))
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := h.svc.Respec(ctx, first.VersionID, h.userID, map[string]float64{"plate_size": 80})
	if err != nil {
		t.Fatal(err)
	}
	if next.ArtifactID != first.ArtifactID {
		t.Errorf("the re-specified variant is a different artifact, so the two cannot be compared")
	}
	if next.Version != 2 {
		t.Errorf("re-specifying produced v%d; it must append", next.Version)
	}
	// The original is what the model said, and stays that way.
	original, err := h.svc.Find(ctx, first.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := original.Document.Parts[0].Size["width"]; got != 60 {
		t.Errorf("the ORIGINAL variant was rewritten to %g; a replay would no longer match "+
			"what the person saw", got)
	}
}

// Setting a name nothing declares would append an identical copy and read as
// success. It fails instead, and names what it could not use.
func TestRespec_RefusesAnOverrideThatWouldChangeNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.parametricProposal("bracket"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		name   string
		params map[string]float64
		wants  string
	}{
		{"a typo", map[string]float64{"plaet_size": 80}, "not a parameter"},
		{"a derived value", map[string]float64{"rib_length": 99}, "is derived"},
		{"nothing at all", map[string]float64{}, "no parameter was given"},
	} {
		_, _, err := h.svc.Respec(ctx, first.VersionID, h.userID, bad.params)
		if err == nil {
			t.Errorf("%s was accepted and would have appended an identical copy", bad.name)
			continue
		}
		if !strings.Contains(err.Error(), bad.wants) {
			t.Errorf("%s: error does not say why: %v", bad.name, err)
		}
	}
}

// A parameter set to a value that breaks an expression must still produce a
// variant — the person asked to see it — with the breakage reported.
func TestRespec_ABreakingValueIsShownWithItsCaveat(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	n := h.parametricProposal("bracket")
	n.Document.Derived = append(n.Document.Derived,
		geometry.Derived{Name: "ratio", Expression: "plate_size / fillet_radius"})
	first, err := h.svc.Save(ctx, n)
	if err != nil {
		t.Fatal(err)
	}

	next, caveats, err := h.svc.Respec(ctx, first.VersionID, h.userID,
		map[string]float64{"fillet_radius": 0})
	if err != nil {
		t.Fatalf("the variant was refused rather than shown with its caveat: %v", err)
	}
	if next == nil {
		t.Fatal("no variant came back")
	}
	var told bool
	for _, c := range caveats {
		if c.Name == "ratio" && strings.Contains(c.Detail, "division by zero") {
			told = true
		}
	}
	if !told {
		t.Errorf("the division by zero was not reported: %+v", caveats)
	}
}
