package geometry

import (
	"context"
	"strings"

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
	// forgotten. "general" is the honest pack: nothing in this conversation has
	// declared a domain, and a pack is a rule set rather than a label.
	projectID, err := s.ws.EnsureProject(ctx, tx, n.ProjectID, n.InitiatorID, doc.Name, "general")
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
