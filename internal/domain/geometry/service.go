package geometry

import (
	"context"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Service stores and reads geometry variants (PRD VIS-04).
type Service struct {
	pool *db.Pool
	repo *Repository
	ws   *workspace.Service
	clk  clock.Clock
	log  *logx.Logger
}

// NewService wires the geometry service.
func NewService(pool *db.Pool, clk clock.Clock, log *logx.Logger) *Service {
	return &Service{
		pool: pool, repo: NewRepository(),
		ws:  workspace.NewService(pool, clk, log),
		clk: clk, log: log,
	}
}

// Save stores one proposed geometry as a new version of its assembly.
//
// # Why the version and the geometry are written together
//
// The version is the variant's identity and the geometry is its content, and a
// crash between two transactions would leave a version claiming to be a variant
// with nothing behind it. Afterwards it looks exactly like a normal row — the
// same failure the artifact lifecycle avoids by writing its timeline event in
// the same transaction as the version. One transaction makes "a variant exists"
// and "there is geometry to draw" the same fact.
//
// # Why the caller does not supply the version number
//
// It is the count of what came before, derived by the append. A caller that
// passed one could pass a number that already exists, and the unique constraint
// would then be the only thing standing between two variants claiming to be v3.
func (s *Service) Save(ctx context.Context, n NewVariant) (*Variant, error) {
	const op = "geometry.Service.Save"

	if err := n.Validate(); err != nil {
		return nil, err
	}
	// The unit is resolved ONCE, here, at the boundary — the same rule the
	// conversation follows. An unrecognised unit becomes unspecified and the
	// declaration is kept beside it, so a reader can tell "nothing was stated"
	// from "something was stated that FORGE cannot convert".
	unit, _ := ParseUnit(n.Document.Units)
	declared := strings.TrimSpace(n.Document.Units)

	// Normalised so a reader never meets a null where a list belongs. JSON
	// encodes a nil slice as null and an empty one as [], and every consumer
	// then needs its own opinion about which means "none".
	doc := n.Document
	if doc.Assumptions == nil {
		doc.Assumptions = []string{}
	}
	if doc.NotVerified == nil {
		doc.NotVerified = []string{}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A workbench conversation has no project until something needs keeping.
	// Created here rather than refused, through the ONE producer of projects, so
	// the membership row that makes it visible to its creator cannot be
	// forgotten.
	//
	// The domain comes from the caller now. It used to be the constant "general"
	// with the note that nothing in the conversation had declared one — true when
	// nothing could, and false once the workbench gained a selector. `general` is
	// still the answer when nobody says, and it is still the honest one: it is
	// the pack that MEANS unknown domain rather than a guess dressed as a choice.
	industry := strings.TrimSpace(n.Industry)
	if industry == "" {
		industry = string(pack.General)
	}
	projectID, err := s.ws.EnsureProject(ctx, tx, n.ProjectID, n.InitiatorID, doc.Name, industry)
	if err != nil {
		return nil, err
	}

	artifact, version, err := s.ws.RecordChangeIn(ctx, tx, workspace.Change{
		ProjectID: projectID,
		Path:      artifactPath(doc.Name),
		Kind:      workspace.ArtifactModel,

		InitiatorID: n.InitiatorID,
		Agent:       n.Agent,
		Inputs:      n.Inputs,
		// A geometry version has no textual diff. Empty is a legal, stated
		// value here — "nothing textual to show" — rather than a gap, which is
		// exactly the distinction the column's NOT NULL is keeping.
		Diff: "",

		GoalID:  n.GoalID,
		Summary: "proposed " + doc.Name,

		// The same list that is recorded in Inputs above, drawn as edges too.
		// Inputs answers "what was this made from" to somebody reading the
		// version; the edges answer it to anybody walking the graph, which is
		// what WRK-03 asks the graph to be able to do.
		DerivedFrom: n.DerivedFrom,
	})
	if err != nil {
		return nil, err
	}

	v := &Variant{
		VersionID: version.ID, ArtifactID: artifact.ID, ProjectID: artifact.ProjectID,
		Path: artifact.Path, Version: version.Version,
		Name: doc.Name, Document: doc,
		Inputs:        version.Inputs,
		Units:         unit,
		UnitsDeclared: declared,
		// One value today, stored rather than assumed: WRK-05 asks the frame to
		// travel with the coordinate, and a frame that exists only as a Go
		// constant is the frame being hoped for.
		Frame:            FrameAssembly,
		Generator:        strings.TrimSpace(n.Generator),
		Agent:            version.Agent,
		Verification:     version.Verification,
		VerificationNote: version.VerificationNote,
		Disposition:      version.Disposition,
		InitiatorID:      version.InitiatorID,
		CreatedAt:        version.CreatedAt,
	}
	if err := s.repo.Insert(ctx, tx, v); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.ws.LogVersioned(ctx, artifact, version)
	s.log.Info(ctx, logx.EventGeometrySaved,
		"version_id", v.VersionID, "project_id", v.ProjectID, "path", v.Path,
		"version", v.Version, "parts", len(v.Document.Parts),
		"units", string(v.Units), "generator", v.Generator)
	return v, nil
}

// Adopt brings an earlier variant forward as the current version, so a person
// can rule on it.
//
// # The conflict this resolves
//
// Appending a version marks the previous one `superseded`, and SetDisposition
// only acts on rows that are still `pending`. That is correct for a FILE — a
// later edit means nobody owes a decision on the earlier one — and wrong for
// VARIANTS, which are alternatives you choose between. A person who compared v1
// and v3 and preferred v1 had no way to say so: the comparison showed them the
// choice and the disposition endpoint refused it. VIS-04's whole purpose is
// choosing, and the choosing had no verb.
//
// # Why a copy forward rather than reopening the old row
//
// Three ways out were on the table (docs/implementation-plan.md). Reopening v1's
// disposition would mean mutating a settled row and would leave the artifact
// with two versions claiming to be current. Suspending supersession for model
// artifacts would split the lifecycle's meaning by artifact kind — a special
// case inside a shared rule, rejected on sight.
//
// So adopting APPENDS: the chosen geometry becomes the newest version, whose
// inputs name the variant it was taken from, and the person rules on that. The
// history stays append-only, `superseded` keeps meaning exactly what it says,
// and "we went back to v1" is a fact the timeline records rather than one it
// hides by rewriting.
//
// The adopted copy is a NEW proposal by the same generator: same document, same
// units, same frame. Its verification state starts unverified like any other
// version — bringing a variant forward is not a check, and inheriting v1's
// verdict would assert that something had looked at the new row.
func (s *Service) Adopt(ctx context.Context, versionID, byUserID, reason string) (*Variant, error) {
	const op = "geometry.Service.Adopt"

	if strings.TrimSpace(byUserID) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("adopting a variant must name who chose it; a design nobody chose has no authority behind it")
	}
	source, err := s.repo.Find(ctx, s.pool, versionID)
	if err != nil {
		return nil, err
	}
	// Adopting the version that is already current is a no-op dressed as a
	// change: it would append an identical row and supersede the thing it
	// copied. Refused with the thing to do instead.
	current, err := s.ws.Repo().CurrentVersion(ctx, s.pool, source.ArtifactID)
	if err != nil {
		return nil, err
	}
	if current.ID == source.VersionID {
		return nil, errs.New(op, errs.CodeConflict).
			WithDetail("%s v%d is already the current version of %s. Rule on it directly with "+
				"POST /v1/workspace/versions/%s/disposition — adopting it would append an identical "+
				"copy and supersede the original.", source.Name, source.Version, source.Path, source.VersionID)
	}

	adopted, err := s.Save(ctx, NewVariant{
		ProjectID:   source.ProjectID,
		InitiatorID: byUserID,
		// A human chose this. The GEOMETRY was drawn by the generator recorded
		// below, and the two facts stay separate: WRK-04's agent says which part
		// of FORGE acted, and adopting is an act of a person.
		Agent:     workspace.AgentHuman,
		Generator: source.Generator,
		Document:  source.Document,
		Inputs: map[string]any{
			"source":            "adopted",
			"adopted_from":      source.VersionID,
			"adopted_version":   source.Version,
			"adopted_from_path": source.Path,
			"adopted_by":        byUserID,
			"reason":            strings.TrimSpace(reason),
			"note": "This geometry is a copy of an earlier variant, brought forward so it could be " +
				"ruled on. Appending a version supersedes the previous one, and a superseded version " +
				"can no longer be accepted or rejected.",
		},
	})
	if err != nil {
		return nil, err
	}
	s.log.Info(ctx, logx.EventGeometryAdopted,
		"version_id", adopted.VersionID, "adopted_from", source.VersionID,
		"project_id", adopted.ProjectID, "by", byUserID)
	return adopted, nil
}

// Find returns one variant.
func (s *Service) Find(ctx context.Context, versionID string) (*Variant, error) {
	return s.repo.Find(ctx, s.pool, versionID)
}

// List returns a project's variants, newest first.
func (s *Service) List(ctx context.Context, projectID string, limit int) ([]Variant, error) {
	return s.repo.ListByProject(ctx, s.pool, projectID, limit)
}

// Compare renders several variants side by side, and says what differs.
//
// The comparison is DERIVED on every call and never stored. A stored one goes
// stale the moment a variant is verified or a person rules on it, and a stale
// comparison is worse than none: it is a document whose whole purpose is to be
// believed by somebody choosing between designs.
func (s *Service) Compare(ctx context.Context, versionIDs []string) (*Comparison, error) {
	const op = "geometry.Service.Compare"

	ids, err := dedupe(op, versionIDs)
	if err != nil {
		return nil, err
	}
	variants, err := s.repo.FindMany(ctx, s.pool, ids)
	if err != nil {
		return nil, err
	}
	if len(variants) < 2 {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a comparison needs at least two variants; %d was named", len(variants))
	}
	// Every variant in one comparison must belong to one project. Not a
	// convenience: authorisation is per project, so a set spanning two of them
	// would be checked against one and disclose the other.
	for _, v := range variants[1:] {
		if v.ProjectID != variants[0].ProjectID {
			return nil, errs.New(op, errs.CodeValidationFailed).
				WithDetail("these variants belong to different projects, and a comparison is read with " +
					"one project's permission")
		}
	}
	return Compare(variants), nil
}

// dedupe refuses a comparison that names the same variant twice.
//
// Silently collapsing it would answer a different question from the one asked:
// somebody who pastes an id twice is looking at two columns and wondering why
// they are identical, and the honest answer is that they named one variant.
func dedupe(op string, ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if seen[id] {
			return nil, errs.New(op, errs.CodeValidationFailed).
				WithDetail("variant %s was named twice; a variant cannot be compared with itself", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) < 2 {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a comparison needs at least two variants; %d was named", len(out))
	}
	if len(out) > MaxCompare {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("%d variants were named; a side-by-side comparison holds at most %d, "+
				"because past that nothing on the screen is legible enough to decide with", len(out), MaxCompare)
	}
	return out, nil
}

// MaxCompare is how many variants one comparison holds.
//
// A product limit rather than a technical one: the view is columns on a screen,
// and six of them are already narrower than the provenance each one has to
// carry. Stated as a constant so the API, the CLI and the workbench refuse at
// the same number instead of three different ones.
const MaxCompare = 6

// Respec re-derives a variant with different parameter values (wave 11).
//
// # What this is for
//
// It is the operation the 2026-09-05 kernel spike performed by hand nine times:
// change one parameter and see whether the design survives it. Until the binding
// layer existed there was nothing to change — a part's width was the number the
// model typed, and "set plate_size to 80" would have moved nothing.
//
// # Why it appends a version rather than editing in place
//
// The result keeps the source document's NAME, so it lands on the same artifact
// path and becomes the next version of the same thing. That is what makes the
// two comparable side by side (PRD VIS-04) rather than two unrelated designs —
// and it leaves the original exactly as the model produced it, which is what a
// replay depends on.
//
// # Why an override naming nothing is refused rather than ignored
//
// Setting a parameter that does not exist, or a derived value that is recomputed
// from its expression a moment later, produces a new version identical to the
// old one. That reads as success and is not, and the person who typed the name
// would have no way to tell. So it fails, and says which name it could not use.
func (s *Service) Respec(ctx context.Context, versionID, byUserID string, overrides map[string]float64) (*Variant, []Problem, error) {
	const op = "geometry.Service.Respec"

	if strings.TrimSpace(byUserID) == "" {
		return nil, nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("re-specifying a variant must name who changed it; a dimension nobody chose " +
				"has no authority behind it")
	}
	if len(overrides) == 0 {
		return nil, nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("no parameter was given to change, so this would append an identical copy. " +
				"Name at least one parameter and its new value")
	}
	source, err := s.repo.Find(ctx, s.pool, versionID)
	if err != nil {
		return nil, nil, err
	}

	next, problems := source.Document.WithParameters(overrides)

	// A problem whose name is one of the requested overrides is a refusal OF
	// that override — WithParameters names them after the key it could not use.
	// The rest are things the document already had to say about itself, and
	// those travel with the new variant rather than blocking it.
	for _, p := range problems {
		if p.Severity != Error {
			continue
		}
		if _, requested := overrides[p.Name]; !requested {
			continue
		}
		return nil, nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("cannot change %s of %s: %s", p.Name, source.Name, p.Detail)
	}

	changed := make(map[string]float64, len(overrides))
	for k, v := range overrides {
		changed[k] = v
	}
	saved, err := s.Save(ctx, NewVariant{
		ProjectID:   source.ProjectID,
		InitiatorID: byUserID,
		// A person chose the new value; the geometry that follows from it was
		// computed by FORGE. WRK-04's agent says which part of FORGE acted, and
		// choosing a dimension is an act of a person.
		Agent:     workspace.AgentHuman,
		Generator: source.Generator,
		Document:  *next,
		Inputs: map[string]any{
			"source":              "respecified",
			"respecified_from":    source.VersionID,
			"respecified_version": source.Version,
			"respecified_by":      byUserID,
			"parameters_changed":  changed,
			"note": "This geometry was COMPUTED from an earlier variant by changing the parameters " +
				"listed above and re-evaluating every expression that depends on them. It is not a " +
				"new proposal: no model was asked, and nothing here was checked.",
		},
	})
	if err != nil {
		return nil, nil, err
	}
	s.log.Info(ctx, logx.EventGeometryRespecified,
		"version_id", saved.VersionID, "respecified_from", source.VersionID,
		"project_id", saved.ProjectID, "by", byUserID, "parameters", len(changed))
	return saved, problems, nil
}
