package agent

import (
	"context"

	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Reading the domain a project is worked in, for the conversation surface.
//
// # Why this exists
//
// `forge_projects.pack` records the industry a project works in and used to be
// read by nothing. The worker now reads it as a permission ceiling
// (agent.grantFor). This is the other half: the conversation needs the domain's
// UNITS AND VOCABULARY, or FORGE answers an architect in the register of a
// software engineer and is confidently useless.
//
// # Why a miss falls back rather than failing — and why the WORKER does not
//
// Deliberately different from workspace.Service.PackFor, which returns an error
// the worker turns into a failed task. The two reads answer different questions:
//
//	the worker asks   "how far may this work go?"      — a permission
//	this asks         "what vocabulary does it use?"   — guidance
//
// Widening a permission on a failed read would be a safety defect. Losing the
// vocabulary is a worse answer, not an unsafe one, and taking the workbench down
// over it would be the wrong trade — the same reasoning CharacterStore records
// for the project's tone. The fallback is logged at WARN so a deployment where
// every call quietly loses its domain does not look like one where nobody set
// an industry.
//
// See docs/bugfix/2026-09-04-the-pack-was-written-and-never-read.md.

// DomainStore resolves the pack a project is worked in.
//
// Nil is a legal value everywhere it is held: the evaluation harness and the
// no-project conversation surfaces have no database in the room.
type DomainStore struct {
	pool *db.Pool
	log  *logx.Logger
	// pinned, when set, is the answer for every project. See PinnedDomainStore.
	pinned *domainpack.Definition
}

// NewDomainStore returns a store reading from forge_projects.
func NewDomainStore(pool *db.Pool, log *logx.Logger) *DomainStore {
	if pool == nil {
		return nil
	}
	return &DomainStore{pool: pool, log: log}
}

// PinnedDomainStore answers every project with one pack, ignoring the database.
//
// # Why this exists, and why it is not a back door
//
// The evaluation suite has no database and no project — deliberately, so a run's
// score cannot drift with somebody's per-project setting — and it still has to
// measure whether FORGE answers a civil question in civil terms. Without this
// every evaluation of every industry would run under `general` and measure
// nothing about the domain.
//
// It cannot widen anything. A pack's other job is the permission ceiling, and
// that is read by workspace.Service.PackFor on the execution path, which this
// does not touch. What is pinned here reaches the PROMPT and nothing else.
func PinnedDomainStore(def domainpack.Definition) *DomainStore {
	return &DomainStore{pinned: &def}
}

// For returns the pack in force on a project.
//
// A nil receiver, an empty project id, an unreadable row or a domain this build
// does not recognise all answer `general` — the pack whose definition IS an
// unknown domain. That is the honest answer to "which rules apply here?" when
// this cannot say, and it carries no conventions, so nothing is asserted about
// a domain nobody established.
func (s *DomainStore) For(ctx context.Context, projectID string) domainpack.Definition {
	general, _ := domainpack.Lookup(string(domainpack.General))
	if s != nil && s.pinned != nil {
		return *s.pinned
	}
	if s == nil || projectID == "" {
		return general
	}
	var stored string
	if err := s.pool.QueryRow(ctx,
		`select pack from forge_projects where id = $1`, projectID).Scan(&stored); err != nil {
		if s.log != nil {
			s.log.WarnWith(ctx, logx.EventCharacterFallback, err, "project_id", projectID,
				"detail", "the project's domain could not be read; answering without its "+
					"units or vocabulary")
		}
		return general
	}
	def, known := domainpack.Lookup(stored)
	if !known {
		if s.log != nil {
			s.log.Warn(ctx, logx.EventCharacterFallback, "project_id", projectID, "pack", stored,
				"detail", "the project declares a domain this build does not recognise, so no "+
					"pack conventions are in force")
		}
		return general
	}
	return def
}
