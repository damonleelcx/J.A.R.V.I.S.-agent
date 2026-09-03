package secrets

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Lookup reads a value from the environment.
//
// An interface with one implementation, because the tests need to supply values
// without putting real-looking credentials in the process environment of a test
// binary — and because a deployment that later wants a vault agent implements
// this rather than editing the broker.
type Lookup interface {
	Get(name string) (string, bool)
}

// EnvLookup reads the process environment.
type EnvLookup struct{}

// Get returns the value of an environment variable.
func (EnvLookup) Get(name string) (string, bool) { return os.LookupEnv(name) }

// MapLookup is a Lookup backed by a map, for tests and for a deployment that
// loads values from somewhere else at startup.
type MapLookup map[string]string

// Get returns a value from the map.
func (m MapLookup) Get(name string) (string, bool) { v, ok := m[name]; return v, ok }

// Broker declares, grants and resolves secrets.
type Broker struct {
	pool   *db.Pool
	lookup Lookup
	clock  clock.Clock
	log    *logx.Logger
}

// NewBroker wires the broker.
func NewBroker(pool *db.Pool, lookup Lookup, clk clock.Clock, log *logx.Logger) *Broker {
	if lookup == nil {
		lookup = EnvLookup{}
	}
	return &Broker{pool: pool, lookup: lookup, clock: clk, log: log}
}

const secretColumns = `id, project_id, name, source, env_var, description,
	revoked_at, revoked_by, revoked_reason, created_by, created_at, updated_at`

func scanSecret(row pgx.Row) (*Secret, error) {
	var s Secret
	var source string
	err := row.Scan(&s.ID, &s.ProjectID, &s.Name, &source, &s.EnvVar, &s.Description,
		&s.RevokedAt, &s.RevokedBy, &s.RevokedReason, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	s.Source = Source(source)
	if !s.Source.Valid() {
		return nil, errs.New("secrets.scanSecret", errs.CodeStateCorrupt).
			WithDetail("secret %s declares source %q, which this build does not recognise; "+
				"it will not be resolved rather than being read some other way", s.ID, source)
	}
	return &s, nil
}

// Declare registers a handle.
func (b *Broker) Declare(ctx context.Context, s *Secret) (*Secret, error) {
	const op = "secrets.Broker.Declare"

	if s.Source == "" {
		s.Source = SourceEnv
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	now := b.clock.Now()
	s.ID, s.CreatedAt, s.UpdatedAt = id.New(id.PrefixSecret), now, now

	_, err := b.pool.Exec(ctx, `
		insert into forge_secrets
			(id, project_id, name, source, env_var, description, created_by, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$8)`,
		s.ID, s.ProjectID, s.Name, string(s.Source), s.EnvVar, s.Description, s.CreatedBy, now)
	if err != nil {
		if strings.Contains(err.Error(), "forge_secrets_project_id_name_key") {
			return nil, errs.Wrap(op, errs.CodeConflict, err).
				WithDetail("this project already declares a secret named %q", s.Name)
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	// Deliberately NOT checked here: whether the environment variable is set.
	// A deployment declares its handles from a migration or a setup script and
	// sets the variables when the service starts, and refusing a declaration for
	// a variable that is not yet exported would make the two steps ordered for
	// no reason. Resolution refuses, loudly, at the moment of use.
	b.log.Info(ctx, logx.EventSecretDeclared, "secret_id", s.ID, "name", s.Name,
		"project_id", s.ProjectID, "env_var", s.EnvVar)
	return s, nil
}

// Grant permits a tool to receive a secret.
func (b *Broker) Grant(ctx context.Context, secretID, toolName, byUserID string) error {
	const op = "secrets.Broker.Grant"

	if strings.TrimSpace(toolName) == "" || strings.TrimSpace(byUserID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a grant names a tool and the person who made it")
	}
	sec, err := b.Find(ctx, secretID)
	if err != nil {
		return err
	}
	if sec.Revoked() {
		return errs.New(op, errs.CodeForbidden).
			WithDetail("secret %q was revoked at %s and cannot be granted to anything",
				sec.Name, sec.RevokedAt.UTC().Format(time.RFC3339))
	}
	_, err = b.pool.Exec(ctx, `
		insert into forge_secret_grants (secret_id, tool_name, granted_by, granted_at)
		values ($1,$2,$3,$4)
		on conflict (secret_id, tool_name) do nothing`,
		secretID, toolName, byUserID, b.clock.Now())
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	b.log.Info(ctx, logx.EventSecretGranted, "secret_id", secretID, "tool", toolName, "by", byUserID)
	return nil
}

// Revoke withdraws a secret from every tool at once.
//
// One of SAF-07's seven verbs, and the reason revocation is a timestamp rather
// than a delete: after an incident the first question is when it stopped being
// usable and who stopped it, and a deleted row answers neither.
func (b *Broker) Revoke(ctx context.Context, secretID, byUserID, reason string) error {
	const op = "secrets.Broker.Revoke"

	if strings.TrimSpace(byUserID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a revocation must name the person who made it; a withdrawal nobody made cannot be accounted for")
	}
	now := b.clock.Now()
	tag, err := b.pool.Exec(ctx, `
		update forge_secrets
		   set revoked_at = $2, revoked_by = $3, revoked_reason = $4, updated_at = $2
		 where id = $1 and revoked_at is null`, secretID, now, byUserID, reason)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		// Either it is gone or it is already revoked. Revoking twice must not
		// move the date: "when did this stop being usable" has one answer.
		if _, findErr := b.Find(ctx, secretID); findErr != nil {
			return findErr
		}
		return nil
	}
	b.log.Warn(ctx, logx.EventSecretRevoked, "secret_id", secretID, "by", byUserID, "reason", reason)
	return nil
}

// Find returns one secret declaration with its grants.
func (b *Broker) Find(ctx context.Context, secretID string) (*Secret, error) {
	const op = "secrets.Broker.Find"

	s, err := scanSecret(b.pool.QueryRow(ctx, `select `+secretColumns+` from forge_secrets where id = $1`, secretID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no secret %s", secretID)
		}
		if errs.CodeOf(err) != errs.CodeInternal {
			return nil, err
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := b.loadGrants(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

// List returns a project's declarations, with their grants.
func (b *Broker) List(ctx context.Context, projectID string, includeRevoked bool) ([]Secret, error) {
	const op = "secrets.Broker.List"

	sql := `select ` + secretColumns + ` from forge_secrets where project_id = $1`
	if !includeRevoked {
		sql += ` and revoked_at is null`
	}
	sql += ` order by name asc`

	rows, err := b.pool.Query(ctx, sql, projectID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	out := []Secret{}
	for rows.Next() {
		s, err := scanSecret(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	for i := range out {
		if err := b.loadGrants(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (b *Broker) loadGrants(ctx context.Context, s *Secret) error {
	rows, err := b.pool.Query(ctx,
		`select tool_name from forge_secret_grants where secret_id = $1 order by tool_name`, s.ID)
	if err != nil {
		return errs.Wrap("secrets.Broker.loadGrants", errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()
	s.Tools = nil
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return errs.Wrap("secrets.Broker.loadGrants", errs.CodeDatabaseUnavail, err)
		}
		s.Tools = append(s.Tools, t)
	}
	return rows.Err()
}

// Available describes one handle to the model.
//
// Name and purpose only. There is no field here that could hold a value, which
// is the point: this struct is what gets serialised into a prompt.
type Available struct {
	Handle      string   `json:"handle"`
	Description string   `json:"description"`
	Tools       []string `json:"usable_by"`
}

// Describe lists the handles a project's model may reference.
//
// Only granted, unrevoked secrets: telling the model about a handle it cannot
// use produces a tool call that gets refused, and a refusal the model could not
// have predicted teaches it nothing.
func (b *Broker) Describe(ctx context.Context, projectID string) ([]Available, error) {
	list, err := b.List(ctx, projectID, false)
	if err != nil {
		return nil, err
	}
	out := []Available{}
	for i := range list {
		s := list[i]
		if len(s.Tools) == 0 {
			continue
		}
		out = append(out, Available{
			Handle: s.Handle(), Description: s.Description, Tools: s.Tools,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out, nil
}

// Resolution is the outcome of resolving the handles in one tool call.
type Resolution struct {
	// Values maps handle name to value. Never logged, never serialised, never
	// returned to a caller that does not need it.
	Values map[string]string
	// Redactor scrubs these values out of whatever the tool returns.
	Redactor *Redactor
	// TooShort names handles whose values cannot be redacted safely.
	TooShort []string
}

// Substitute replaces every handle in a string with its value.
func (r *Resolution) Substitute(s string) string {
	if r == nil || len(r.Values) == 0 {
		return s
	}
	for name, value := range r.Values {
		s = strings.ReplaceAll(s, HandlePrefix+name, value)
	}
	return s
}

// Resolve looks up every handle a tool call references, enforcing scope.
//
// # Why it refuses rather than passing an unresolved handle through
//
// If a handle cannot be resolved — unknown, revoked, not granted to this tool,
// or its variable is not set — the call is REFUSED. The tempting alternative is
// to leave the literal `secret://x` in the argument and let the tool deal with
// it, and that is how a request goes out with `Authorization: Bearer
// secret://github_token`: the tool does something, it fails in a way that has
// nothing to do with credentials, and the model spends the rest of the run
// debugging the wrong thing.
//
// Refusing names the handle and the reason, which is the one message that gets
// the operator to the actual problem.
func (b *Broker) Resolve(ctx context.Context, projectID, toolName string, handles []string) (*Resolution, error) {
	const op = "secrets.Broker.Resolve"

	if len(handles) == 0 {
		return &Resolution{Values: map[string]string{}, Redactor: NewRedactor(nil)}, nil
	}
	values := map[string]string{}
	for _, name := range handles {
		s, err := b.findByName(ctx, projectID, name)
		if err != nil {
			if errs.Is(err, errs.CodeNotFound) {
				return nil, errs.New(op, errs.CodeSecretUnavailable).
					WithDetail("no secret is declared as %s%s in this project", HandlePrefix, name)
			}
			return nil, err
		}
		if s.Revoked() {
			return nil, errs.New(op, errs.CodeSecretUnavailable).
				WithDetail("%s was revoked at %s%s", s.Handle(),
					s.RevokedAt.UTC().Format(time.RFC3339), reasonSuffix(s.RevokedReason))
		}
		if !s.GrantedTo(toolName) {
			// The refusal says which tools MAY have it. An operator reading this
			// needs to know whether to grant the tool or to stop the model
			// reaching for it, and those are opposite responses.
			return nil, errs.New(op, errs.CodeSecretNotGranted).
				WithDetail("%s is not granted to %q. It is granted to: %s",
					s.Handle(), toolName, toolList(s.Tools))
		}
		value, ok := b.lookup.Get(s.EnvVar)
		if !ok || value == "" {
			return nil, errs.New(op, errs.CodeSecretUnavailable).
				WithDetail("%s reads %s and that variable is not set in this process. "+
					"FORGE brokers secrets rather than storing them: set the variable where the service starts.",
					s.Handle(), s.EnvVar)
		}
		values[name] = value
	}

	res := &Resolution{
		Values: values, Redactor: NewRedactor(values), TooShort: SkippedShort(values),
	}
	if len(res.TooShort) > 0 {
		// Not fatal, and not silent. A value this short cannot be scrubbed out
		// of tool output without destroying legitimate text, so the model will
		// eventually see it; the operator has to know that.
		b.log.Warn(ctx, logx.EventSecretUnredactable,
			"tool", toolName, "handles", strings.Join(res.TooShort, ","),
			"detail", "value is too short to redact from tool output without false positives")
	}
	return res, nil
}

func (b *Broker) findByName(ctx context.Context, projectID, name string) (*Secret, error) {
	const op = "secrets.Broker.findByName"

	s, err := scanSecret(b.pool.QueryRow(ctx,
		`select `+secretColumns+` from forge_secrets where project_id = $1 and name = $2`,
		projectID, name))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err)
		}
		if errs.CodeOf(err) != errs.CodeInternal {
			return nil, err
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := b.loadGrants(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

func toolList(tools []string) string {
	if len(tools) == 0 {
		return "nothing — it has no grants, which is the state a newly declared secret starts in"
	}
	return strings.Join(tools, ", ")
}

func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return " (" + reason + ")"
}
