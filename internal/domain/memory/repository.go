package memory

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Repository is the persistence port for memory.
//
// Like every other repository here it takes a db.Querier per call rather than
// holding a pool, so the same code runs inside or outside a transaction.
// Remember needs that: it reads the forgotten-key tombstone and writes the item,
// and those two must not be able to interleave with a concurrent Forget.
type Repository struct{}

// NewRepository returns the Postgres implementation.
func NewRepository() *Repository { return &Repository{} }

const itemColumns = `id, scope, goal_id, project_id, user_id, key, value, how,
	source, pinned, expires_at, forgotten_at, forgotten_by, forgotten_reason,
	created_at, updated_at`

func scanItem(row pgx.Row) (*Item, error) {
	var i Item
	var scope, how string
	err := row.Scan(&i.ID, &scope, &i.GoalID, &i.ProjectID, &i.UserID, &i.Key,
		&i.Value, &how, &i.Source, &i.Pinned, &i.ExpiresAt,
		&i.ForgottenAt, &i.ForgottenBy, &i.ForgottenReason, &i.CreatedAt, &i.UpdatedAt)
	if err != nil {
		return nil, err
	}
	i.Scope, i.How = Scope(scope), claim.Epistemic(how)

	// Both columns carry a database check constraint, so a value this build does
	// not recognise means the constraint and the code have drifted apart. It is
	// reported rather than coerced: silently treating an unknown layer as a
	// known one would give it somebody else's retention and audience.
	if !i.Scope.Valid() {
		return nil, errs.New("memory.scanItem", errs.CodeStateCorrupt).
			WithDetail("memory item %s is in layer %q, which this build does not recognise", i.ID, scope)
	}
	if !i.How.Valid() {
		return nil, errs.New("memory.scanItem", errs.CodeStateCorrupt).
			WithDetail("memory item %s has epistemic label %q, which this build does not recognise", i.ID, how)
	}
	return &i, nil
}

// liveClause is the read-time retention guarantee, in one place.
//
// Every read that serves a caller composes this. It is not an optimisation on
// top of the expiry sweep — it is the guarantee, and the sweep only reclaims
// space. A deployment whose sweep has not run for a week still must not hand
// back week-old turn memory as though it were current, and this is why it does
// not.
const liveClause = `forgotten_at is null and (pinned or expires_at is null or expires_at > $1)`

// Upsert writes an item, replacing the value at its key within its layer.
//
// # Why it refuses a forgotten key
//
// A user who deletes a memory means it. FORGE writes memory unprompted, so
// without this refusal the next turn that observed the same fact would write the
// row back and the deletion would quietly undo itself — the user would have no
// way to tell, because nothing would report it. The refusal is loud, names the
// key, and the remedy on MEMORY_FORGOTTEN says how to deliberately re-open it.
func (r *Repository) Upsert(ctx context.Context, q db.Querier, i *Item) error {
	const op = "memory.Repository.Upsert"

	if err := i.Validate(); err != nil {
		return err
	}
	if i.CreatedAt.IsZero() || i.UpdatedAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("memory item %q has no timestamp; the application clock owns every timestamp in this system", i.Key)
	}

	existing, err := r.FindByKey(ctx, q, i.Scope, ownerOf(i), i.Key)
	switch {
	case err == nil && existing.Forgotten():
		return errs.New(op, errs.CodeMemoryForgotten).
			WithDetail("%q was forgotten in %s memory at %s%s",
				i.Key, i.Scope, existing.ForgottenAt.UTC().Format(time.RFC3339),
				reasonSuffix(existing.ForgottenReason))
	case err != nil && errs.CodeOf(err) != errs.CodeNotFound:
		return err
	case err == nil:
		// Same key, same layer, same owner: this is a rewrite of that item.
		// Identity is preserved so that a pin, an expiry a user set by hand, and
		// the created_at that says how long FORGE has believed this all survive
		// a value being corrected.
		_, execErr := q.Exec(ctx, `
			update forge_memory
			   set value = $2, how = $3, source = $4, expires_at = $5, updated_at = $6
			 where id = $1`,
			existing.ID, i.Value, string(i.How), i.Source, i.ExpiresAt, i.UpdatedAt)
		if execErr != nil {
			return wrapWrite(op, execErr)
		}
		i.ID, i.Pinned, i.CreatedAt = existing.ID, existing.Pinned, existing.CreatedAt
		return nil
	}

	_, err = q.Exec(ctx, `
		insert into forge_memory
			(id, scope, goal_id, project_id, user_id, key, value, how, source,
			 pinned, expires_at, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)`,
		i.ID, string(i.Scope), i.GoalID, i.ProjectID, i.UserID, i.Key, i.Value,
		string(i.How), i.Source, i.Pinned, i.ExpiresAt, i.CreatedAt)
	if err != nil {
		return wrapWrite(op, err)
	}
	return nil
}

// FindByKey returns one item by key within a layer, forgotten or not.
//
// Reads that serve a user go through Recall, which applies the retention
// clause. This one deliberately does not: Upsert needs to see a forgotten row in
// order to refuse it, and the inspection surface needs to show a user the
// deletion they asked for.
func (r *Repository) FindByKey(ctx context.Context, q db.Querier, scope Scope, owner string, key string) (*Item, error) {
	const op = "memory.Repository.FindByKey"

	column, err := ownerColumn(scope)
	if err != nil {
		return nil, err
	}
	sql := `select ` + itemColumns + ` from forge_memory where scope = $1 and key = $2`
	args := []any{string(scope), key}
	if column != "" {
		sql += ` and ` + column + ` = $3`
		args = append(args, owner)
	}

	item, err := scanItem(q.QueryRow(ctx, sql, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).
				WithDetail("no %s memory at key %q", scope, key)
		}
		return nil, wrapRead(op, err)
	}
	return item, nil
}

// Query describes one recall.
//
// Scopes is the layers to read, in the order the caller wants them merged.
// Owners maps each layer's owner column to the id it must equal — a caller
// asking for user memory without naming the user gets nothing, rather than
// everybody's.
type Query struct {
	Scopes []Scope
	Owners map[Owner]string
	// Prefix restricts to keys beginning with it. Empty reads the whole layer.
	Prefix string
	// Limit caps rows per layer. Zero uses defaultRecallLimit.
	Limit int
}

const defaultRecallLimit = 50

// List returns the live items matching a query, newest first within each layer.
//
// The retention clause is applied here in SQL, so an expired item is not
// returned even when nothing has swept it. Items are returned in the caller's
// layer order, which is how the service merges narrow context over broad.
func (r *Repository) List(ctx context.Context, q db.Querier, query Query, now time.Time) ([]Item, error) {
	const op = "memory.Repository.List"

	limit := query.Limit
	if limit <= 0 {
		limit = defaultRecallLimit
	}

	var out []Item
	for _, scope := range query.Scopes {
		layer, err := LayerOf(scope)
		if err != nil {
			return nil, err
		}
		column, err := ownerColumn(scope)
		if err != nil {
			return nil, err
		}

		sql := `select ` + itemColumns + ` from forge_memory where ` + liveClause + ` and scope = $2`
		args := []any{now, string(scope)}
		if column != "" {
			owner, ok := query.Owners[layer.Owner]
			if !ok || strings.TrimSpace(owner) == "" {
				// Not an empty result: an unowned query for an owned layer is a
				// caller bug, and returning nothing would look like "there is
				// nothing there" rather than "you did not say whose".
				return nil, errs.New(op, errs.CodeValidationFailed).
					WithDetail("%s memory is owned by a %s, so recalling it needs one named", scope, layer.Owner)
			}
			args = append(args, owner)
			sql += ` and ` + column + ` = $` + strconv.Itoa(len(args))
		}
		if query.Prefix != "" {
			args = append(args, query.Prefix)
			// A literal prefix match. like with an escaped pattern would let a
			// user's key characters change what the query means.
			sql += ` and left(key, length($` + strconv.Itoa(len(args)) + `)) = $` + strconv.Itoa(len(args))
		}
		args = append(args, limit)
		sql += ` order by pinned desc, updated_at desc limit $` + strconv.Itoa(len(args))

		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return nil, wrapRead(op, err)
		}
		items, err := collect(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

// ListAll returns every item in a layer including forgotten and expired ones,
// for the inspection and export surfaces (PRD MEM-02).
//
// Export must show a user what FORGE holds, and "what it holds" includes the
// rows it is refusing to use. An export that quietly omitted them would tell the
// user their deletion left nothing behind, which is not true.
func (r *Repository) ListAll(ctx context.Context, q db.Querier, scope Scope, owner string) ([]Item, error) {
	const op = "memory.Repository.ListAll"

	column, err := ownerColumn(scope)
	if err != nil {
		return nil, err
	}
	sql := `select ` + itemColumns + ` from forge_memory where scope = $1`
	args := []any{string(scope)}
	if column != "" {
		args = append(args, owner)
		sql += ` and ` + column + ` = $2`
	}
	sql += ` order by key asc`

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrapRead(op, err)
	}
	return collect(rows)
}

// SetPinned pins or unpins an item.
func (r *Repository) SetPinned(ctx context.Context, q db.Querier, id string, pinned bool, at time.Time) error {
	const op = "memory.Repository.SetPinned"
	return r.touch(ctx, q, op, `update forge_memory set pinned = $2, updated_at = $3
	                             where id = $1 and forgotten_at is null`, id, pinned, at)
}

// SetExpiry sets or clears an item's expiry. A nil expiry means never.
func (r *Repository) SetExpiry(ctx context.Context, q db.Querier, id string, expires *time.Time, at time.Time) error {
	const op = "memory.Repository.SetExpiry"
	return r.touch(ctx, q, op, `update forge_memory set expires_at = $2, updated_at = $3
	                             where id = $1 and forgotten_at is null`, id, expires, at)
}

// Forget records a user's deletion and clears the content.
//
// The row survives on purpose. Its key goes on occupying the layer's unique
// index, which is the mechanism that stops the agent writing the same fact back
// on the next turn — see Upsert. What is kept is only that somebody asked, when,
// and why; the value itself becomes JSON null, because the user asked FORGE to
// forget the value.
//
// The `forgotten_at is null` guard makes it idempotent: forgetting twice must
// not move the date, because "when did this stop being used?" has one answer.
func (r *Repository) Forget(ctx context.Context, q db.Querier, id, byUserID, reason string, at time.Time) error {
	const op = "memory.Repository.Forget"

	if strings.TrimSpace(byUserID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("forgetting a memory must name the person who asked; a deletion nobody made cannot be accounted for")
	}
	tag, err := q.Exec(ctx, `
		update forge_memory
		   set value = 'null'::jsonb, pinned = false,
		       forgotten_at = $4, forgotten_by = $2, forgotten_reason = $3, updated_at = $4
		 where id = $1 and forgotten_at is null`, id, byUserID, reason, at)
	if err != nil {
		return wrapWrite(op, err)
	}
	if tag.RowsAffected() == 0 {
		// Either it does not exist or it is already forgotten. Both mean the
		// caller's intent already holds, so this is reported as not-found only
		// when the row is genuinely absent.
		if _, findErr := r.findByID(ctx, q, id); findErr != nil {
			return findErr
		}
	}
	return nil
}

// Purge deletes a forgotten item outright, re-opening its key.
//
// This is the deliberate act that undoes a tombstone, and it is separate from
// Forget precisely so that it cannot happen by accident. It refuses to purge an
// item that is still live: purging a live item is just an unrecorded deletion,
// which is the thing Forget exists to prevent.
func (r *Repository) Purge(ctx context.Context, q db.Querier, id string) error {
	const op = "memory.Repository.Purge"

	item, err := r.findByID(ctx, q, id)
	if err != nil {
		return err
	}
	if !item.Forgotten() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("memory %q is live; forget it first so the deletion is recorded, then purge it if the key must be re-usable", item.Key)
	}
	if _, err := q.Exec(ctx, `delete from forge_memory where id = $1`, id); err != nil {
		return wrapWrite(op, err)
	}
	return nil
}

// Sweep deletes expired, unpinned, never-forgotten items and reports how many.
//
// It reclaims space; it does not enforce retention — reads already do that. So a
// deployment that never runs it is slower and larger, not wrong, which is the
// correct dependency for a maintenance job to have.
//
// Forgotten rows are never swept: their whole purpose is to outlive the content
// so the key stays claimed.
func (r *Repository) Sweep(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	const op = "memory.Repository.Sweep"

	tag, err := q.Exec(ctx, `
		delete from forge_memory
		 where forgotten_at is null and pinned = false
		   and expires_at is not null and expires_at <= $1`, now)
	if err != nil {
		return 0, wrapWrite(op, err)
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) findByID(ctx context.Context, q db.Querier, id string) (*Item, error) {
	const op = "memory.Repository.findByID"

	item, err := scanItem(q.QueryRow(ctx, `select `+itemColumns+` from forge_memory where id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no memory item %s", id)
		}
		return nil, wrapRead(op, err)
	}
	return item, nil
}

// FindByID returns one item by id regardless of its state.
func (r *Repository) FindByID(ctx context.Context, q db.Querier, id string) (*Item, error) {
	return r.findByID(ctx, q, id)
}

func (r *Repository) touch(ctx context.Context, q db.Querier, op, sql string, args ...any) error {
	tag, err := q.Exec(ctx, sql, args...)
	if err != nil {
		return wrapWrite(op, err)
	}
	if tag.RowsAffected() == 0 {
		item, findErr := r.findByID(ctx, q, args[0].(string))
		if findErr != nil {
			return findErr
		}
		return errs.New(op, errs.CodeMemoryForgotten).
			WithDetail("memory %q was forgotten and cannot be modified; purge it if it should be usable again", item.Key)
	}
	return nil
}

func collect(rows pgx.Rows) ([]Item, error) {
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapRead("memory.collect", err)
	}
	return out, nil
}

// ownerColumn maps a layer to the column carrying its owner. Organisation
// memory has none, and returns "" rather than an error: it is org-wide by
// definition, not a layer with a missing owner.
func ownerColumn(scope Scope) (string, error) {
	layer, err := LayerOf(scope)
	if err != nil {
		return "", err
	}
	switch layer.Owner {
	case OwnerGoal:
		return "goal_id", nil
	case OwnerProject:
		return "project_id", nil
	case OwnerUser:
		return "user_id", nil
	default:
		return "", nil
	}
}

func ownerOf(i *Item) string {
	for _, p := range []*string{i.GoalID, i.ProjectID, i.UserID} {
		if p != nil && *p != "" {
			return *p
		}
	}
	return ""
}

func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return " (" + reason + ")"
}

func wrapRead(op string, err error) error {
	if errs.CodeOf(err) != errs.CodeInternal {
		return err // already a domain error from scanItem
	}
	return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
}

func wrapWrite(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return errs.Wrap(op, errs.CodeConflict, err).
			WithDetail("another item already holds this key in this layer")
	}
	return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
}
