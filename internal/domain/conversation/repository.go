package conversation

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Repository is the persistence port for the conversation record.
type Repository struct{}

// NewRepository returns the Postgres implementation.
func NewRepository() *Repository { return &Repository{} }

const turnColumns = `id, conversation_id, owner_id, coalesce(project_id, ''), seq,
	role, text, detail, images, said_at`

func scanTurn(row pgx.Row) (*Turn, error) {
	var t Turn
	var role string
	if err := row.Scan(&t.ID, &t.ConversationID, &t.OwnerID, &t.ProjectID, &t.Seq,
		&role, &t.Text, &t.Detail, &t.Images, &t.SaidAt); err != nil {
		return nil, err
	}
	t.Role = Role(role)
	// The column carries a check constraint, so an unrecognised value means the
	// constraint and this build have drifted apart. Reported rather than
	// coerced: a turn silently attributed to the wrong speaker is a record that
	// says somebody said something they did not.
	if !t.Role.Valid() {
		return nil, errs.New("conversation.scanTurn", errs.CodeStateCorrupt).
			WithDetail("turn %s is attributed to %q, which is not a speaker this build recognises",
				t.ID, role)
	}
	return &t, nil
}

// Append writes one turn and numbers it.
//
// # Why the sequence is computed in the statement
//
// Reading the last seq and then inserting leaves a window, and a workbench with
// two tabs open is exactly the thing that would find it. Computed in the insert,
// the read and the write are one statement — and the unique index behind it is
// the backstop rather than the plan, so a genuine race is REFUSED rather than
// producing two turns that claim the same position in the conversation.
func (r *Repository) Append(ctx context.Context, q db.Querier, t *Turn) error {
	const op = "conversation.Repository.Append"

	if err := t.Validate(); err != nil {
		return err
	}
	if t.SaidAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("turn %s has no timestamp; the application clock owns every timestamp in this system", t.ID)
	}
	var projectID *string
	if t.ProjectID != "" {
		p := t.ProjectID
		projectID = &p
	}
	err := q.QueryRow(ctx, `
		insert into forge_conversation_turns
			(id, conversation_id, owner_id, project_id, seq, role, text, detail, images, said_at)
		select $1, $2, $3, $4, coalesce(max(seq), 0) + 1, $5, $6, $7, $8, $9
		from forge_conversation_turns where conversation_id = $2
		returning seq`,
		t.ID, t.ConversationID, t.OwnerID, projectID,
		string(t.Role), t.Text, t.Detail, t.Images, t.SaidAt).Scan(&t.Seq)
	if err != nil {
		if isUnique(err, "forge_conversation_turns_conversation_id_seq_key") {
			return errs.Wrap(op, errs.CodeConflict, err).
				WithDetail("two turns tried to take the same position in this conversation; " +
					"the later one was refused rather than overwriting the record")
		}
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// List returns a conversation's turns in order, for this owner only.
//
// Scoped by owner in the QUERY rather than checked afterwards: a conversation id
// is guessable, and a filter applied after the read is a filter somebody
// eventually forgets to apply.
func (r *Repository) List(ctx context.Context, q db.Querier, conversationID, ownerID string) ([]Turn, error) {
	const op = "conversation.Repository.List"

	rows, err := q.Query(ctx, `select `+turnColumns+`
		from forge_conversation_turns
		where conversation_id = $1 and owner_id = $2
		order by seq`, conversationID, ownerID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	out := []Turn{}
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return out, nil
}

// Recent returns the last `limit` turns in order, and how many there are in all.
//
// # Why the total comes back with them
//
// The tail is what the model is given; the total is what was said. A caller that
// only had the tail could not tell a whole conversation from the end of a long
// one, and would present a trimmed context as a complete one — which is how a
// model comes to deny something the record plainly contains.
func (r *Repository) Recent(ctx context.Context, q db.Querier, conversationID, ownerID string, limit int) ([]Turn, int, error) {
	const op = "conversation.Repository.Recent"

	if limit <= 0 {
		return nil, 0, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a history window of %d turns is not a window", limit)
	}
	var total int
	if err := q.QueryRow(ctx, `select count(*) from forge_conversation_turns
		where conversation_id = $1 and owner_id = $2`, conversationID, ownerID).Scan(&total); err != nil {
		return nil, 0, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if total == 0 {
		// Not an error. A conversation's first turn has nothing before it, and
		// that is the common case rather than a failure.
		return []Turn{}, 0, nil
	}

	// The LAST n, returned in the order they were said. Ordered descending to
	// take the tail and re-ordered here rather than in SQL, because a window
	// function for a handful of rows is harder to read than a reverse.
	rows, err := q.Query(ctx, `select `+turnColumns+`
		from forge_conversation_turns
		where conversation_id = $1 and owner_id = $2
		order by seq desc limit $3`, conversationID, ownerID, limit)
	if err != nil {
		return nil, 0, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	out := []Turn{}
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, total, nil
}

// OwnerOf returns who a conversation belongs to, or NOT_FOUND when it has no
// turns — which is the only sense in which a conversation does not exist.
func (r *Repository) OwnerOf(ctx context.Context, q db.Querier, conversationID string) (string, error) {
	const op = "conversation.Repository.OwnerOf"

	var owner string
	err := q.QueryRow(ctx,
		`select owner_id from forge_conversation_turns where conversation_id = $1 limit 1`,
		conversationID).Scan(&owner)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errs.Wrap(op, errs.CodeNotFound, err).
				WithDetail("no conversation %s", conversationID)
		}
		return "", errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return owner, nil
}

// Delete removes a conversation, and reports how much it removed.
//
// The count is returned rather than discarded because "deleted" and "there was
// nothing there" are different answers to the person who pressed the button, and
// PRD AUD-07 makes deletion a control somebody relies on rather than a
// convenience.
func (r *Repository) Delete(ctx context.Context, q db.Querier, conversationID, ownerID string) (int64, error) {
	const op = "conversation.Repository.Delete"

	tag, err := q.Exec(ctx,
		`delete from forge_conversation_turns where conversation_id = $1 and owner_id = $2`,
		conversationID, ownerID)
	if err != nil {
		return 0, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return tag.RowsAffected(), nil
}

// isUnique reports whether err is a unique violation on one of these
// constraints. Matched by NAME rather than by code alone: "some unique index
// rejected this" is not the same fact as "this exact invariant held", and
// treating them as one is how an unrelated collision gets reported as the
// expected case.
func isUnique(err error, constraints ...string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	for _, c := range constraints {
		if pgErr.ConstraintName == c {
			return true
		}
	}
	return false
}
