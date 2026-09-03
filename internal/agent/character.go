package agent

import (
	"context"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Reading a project's character (PRD RSN-04).
//
// # Why this lives here and not in persona
//
// persona is pure: it imports strings and nothing else, so what FORGE is told to
// be can be read, diffed and reasoned about without a database in the room. A
// reader belongs with the code that needs one, and the only things that need one
// are the agents in this package.
//
// # Why a miss is not an error
//
// Every method here falls back to the character it was given rather than
// failing. A project row that cannot be read is a reason to argue at the default
// intensity, not a reason to refuse to think: the alternative is a database
// hiccup taking down goal execution to protect a tone setting. The fallback is
// logged at WARN so it is visible rather than silent — a deployment where every
// call quietly reverts to the default would otherwise look exactly like a
// deployment where nobody had changed the setting.

// CharacterStore resolves the character a project is worked in.
//
// Nil is a legal value everywhere it is held. A deployment that never sets a
// character never needs one, and the agents fall back to the character they were
// constructed with.
type CharacterStore struct {
	pool *db.Pool
	log  *logx.Logger
}

// NewCharacterStore returns a store reading from forge_projects.
func NewCharacterStore(pool *db.Pool, log *logx.Logger) *CharacterStore {
	if pool == nil {
		return nil
	}
	return &CharacterStore{pool: pool, log: log}
}

// For returns the character for a project, or fallback when it cannot say.
//
// The receiver may be nil, and projectID may be empty — the conversation
// surfaces have no project in some deployments — so both are answered with the
// fallback rather than with an error.
func (s *CharacterStore) For(ctx context.Context, projectID string, fallback persona.Character) persona.Character {
	if s == nil || projectID == "" {
		return fallback
	}
	out := fallback
	err := s.pool.QueryRow(ctx,
		`select critique_intensity, verbosity from forge_projects where id = $1`, projectID).
		Scan(&out.CritiqueIntensity, &out.Verbosity)
	if err != nil {
		// Named rather than swallowed. A wrong tone is not worth an outage, and a
		// silent revert to the default is not worth debugging twice.
		if s.log != nil {
			s.log.WarnWith(ctx, logx.EventCharacterFallback, err, "project_id", projectID)
		}
		return fallback
	}
	// Address is not stored per project: it is a fact about a person, and a
	// project-wide "call me Priya" would be wrong for everybody else. Carried
	// through from the fallback so this method never drops what it was given.
	out.Address = fallback.Address
	return out
}
