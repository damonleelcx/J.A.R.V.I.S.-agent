// Package collab is the shared session and the handoff (PRD COL-01, COL-02).
//
// # What COL-01 is here, and what it is not
//
// "Multi-user voice room with identified speakers and a record of who approved
// what." What is built is the RECORD: who was present, who said what, and which
// approvals were made while they were.
//
// What is NOT built is realtime multi-party audio transport. Nothing in this
// architecture carries one, and inventing a half of it would be worse than
// saying so. The record is deliberately transport-agnostic — a turn arrives with
// a speaker and text, and where it came from is a field — so a WebRTC bridge, a
// phone gateway and somebody typing all write the same row.
//
// A transcript is useful long before its audio is, and it is the part an auditor
// asks for.
//
// # The rule the room exists for
//
// **Every turn names its speaker.** There is no anonymous option and no default.
// An unattributed utterance in a multi-user room is the exact failure COL-01
// exists to prevent: six months later, "somebody said the tolerance was fine" is
// worth nothing, and "Priya said the tolerance was fine at 14:02, and Tom
// approved the change at 14:05" is the whole point.
package collab

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// SpeakerKind is who is talking.
type SpeakerKind string

const (
	// SpeakerHuman — a named person. Always carries a user id and a label.
	SpeakerHuman SpeakerKind = "human"
	// SpeakerForge — FORGE itself. Carries no user, and says so.
	//
	// A separate kind rather than a null speaker, because "nobody said this" and
	// "FORGE said this" must not look the same. PRD AUD-05 requires FORGE to
	// identify itself as AI; a transcript where its turns are indistinguishable
	// from an unattributed one fails that.
	SpeakerForge SpeakerKind = "forge"
)

// Valid reports whether k is recognised.
func (k SpeakerKind) Valid() bool { return k == SpeakerHuman || k == SpeakerForge }

// Channel is how a turn arrived.
type Channel string

const (
	ChannelVoice Channel = "voice"
	ChannelText  Channel = "text"
	ChannelAPI   Channel = "api"
)

// Valid reports whether c is recognised.
func (c Channel) Valid() bool { return c == ChannelVoice || c == ChannelText || c == ChannelAPI }

// Turn is one thing said.
type Turn struct {
	ID      string
	RoomID  string
	Seq     int
	Speaker SpeakerKind
	// SpeakerID is the user, for a human turn. Nil for FORGE.
	SpeakerID *string
	// SpeakerLabel is the name AS RECORDED AT THE TIME.
	//
	// A transcript that rendered names by joining to forge_users would show a
	// renamed or deleted account's CURRENT state, which is not what was said in
	// the room. The label is a fact about the moment, like the text.
	SpeakerLabel string
	Text         string
	Channel      Channel
	SaidAt       time.Time
}

// Validate enforces attribution before a turn is written.
func (t *Turn) Validate() error {
	const op = "collab.Turn.Validate"

	if !t.Speaker.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a turn's speaker is %q; it must be human or forge. There is no anonymous speaker.", t.Speaker)
	}
	if strings.TrimSpace(t.Text) == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("a turn with no text is not a turn")
	}
	if t.Channel == "" {
		t.Channel = ChannelText
	}
	if !t.Channel.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("channel %q is not one of voice, text, api", t.Channel)
	}
	switch t.Speaker {
	case SpeakerHuman:
		if t.SpeakerID == nil || strings.TrimSpace(*t.SpeakerID) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("a human turn must name the person who said it. Six months from now, " +
					"\"somebody said the tolerance was fine\" is worth nothing.")
		}
		if strings.TrimSpace(t.SpeakerLabel) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("a human turn must record the speaker's name as it was at the time; " +
					"rendering it later from the account would show who they are now, not who said this")
		}
	case SpeakerForge:
		if t.SpeakerID != nil {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("a FORGE turn names a user; FORGE is not a user and a transcript must not " +
					"suggest a person said this")
		}
	}
	return nil
}

// Participant is somebody who was in the room.
type Participant struct {
	UserID   string
	JoinedAt time.Time
	LeftAt   *time.Time
}

// Present reports whether they were in the room at an instant.
//
// The question a room record is kept for: who was present when that was
// approved.
func (p *Participant) Present(at time.Time) bool {
	if at.Before(p.JoinedAt) {
		return false
	}
	return p.LeftAt == nil || at.Before(*p.LeftAt)
}

// Room is a shared session.
type Room struct {
	ID           string
	ProjectID    string
	GoalID       *string
	Title        string
	Status       string
	OpenedBy     string
	OpenedAt     time.Time
	ClosedAt     *time.Time
	Participants []Participant
	Turns        []Turn
	// ApprovalIDs are the gates decided while this room was open.
	ApprovalIDs []string
}

// Open reports whether the room is still running.
func (r *Room) Open() bool { return r.Status == "open" }

// PresentAt names everybody in the room at an instant.
func (r *Room) PresentAt(at time.Time) []string {
	var out []string
	for i := range r.Participants {
		if r.Participants[i].Present(at) {
			out = append(out, r.Participants[i].UserID)
		}
	}
	return out
}

// Service manages rooms.
type Service struct {
	pool  *db.Pool
	clock clock.Clock
	log   *logx.Logger
}

// NewService wires the service.
func NewService(pool *db.Pool, clk clock.Clock, log *logx.Logger) *Service {
	return &Service{pool: pool, clock: clk, log: log}
}

// OpenRoom starts a shared session.
func (s *Service) OpenRoom(ctx context.Context, projectID, goalID, title, byUserID string) (*Room, error) {
	const op = "collab.Service.OpenRoom"

	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(byUserID) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a room names its project and who opened it")
	}
	now := s.clock.Now()
	room := &Room{
		ID: id.New(id.PrefixRoom), ProjectID: projectID, Title: title,
		Status: "open", OpenedBy: byUserID, OpenedAt: now,
	}
	if goalID != "" {
		room.GoalID = &goalID
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		insert into forge_rooms (id, project_id, goal_id, title, status, opened_by, opened_at, created_at, updated_at)
		values ($1,$2,$3,$4,'open',$5,$6,$6,$6)`,
		room.ID, projectID, room.GoalID, title, byUserID, now); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	// Whoever opened it is in it. A room with nobody in it is a row.
	if _, err := tx.Exec(ctx,
		`insert into forge_room_participants (room_id, user_id, joined_at) values ($1,$2,$3)`,
		room.ID, byUserID, now); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.log.Info(ctx, logx.EventRoomOpened, "room_id", room.ID, "project_id", projectID, "by", byUserID)
	return room, nil
}

// Join records somebody entering.
func (s *Service) Join(ctx context.Context, roomID, userID string) error {
	const op = "collab.Service.Join"

	room, err := s.Find(ctx, roomID)
	if err != nil {
		return err
	}
	if !room.Open() {
		return errs.New(op, errs.CodeConflict).
			WithDetail("room %s is closed; a closed transcript does not gain participants", roomID)
	}
	now := s.clock.Now()
	// Re-joining clears the previous departure rather than adding a second row:
	// somebody whose connection dropped and came back was in the room the whole
	// time as far as the record is concerned, and two rows would make
	// "were they present" ambiguous.
	if _, err := s.pool.Exec(ctx, `
		insert into forge_room_participants (room_id, user_id, joined_at) values ($1,$2,$3)
		on conflict (room_id, user_id) do update set left_at = null`,
		roomID, userID, now); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// Leave records somebody going.
//
// A departure rather than a delete: "who was present when that was approved" is
// the question a room record is kept for, and a deleted participant answers it
// wrongly for every instant they were there.
func (s *Service) Leave(ctx context.Context, roomID, userID string) error {
	const op = "collab.Service.Leave"

	if _, err := s.pool.Exec(ctx,
		`update forge_room_participants set left_at = $3 where room_id = $1 and user_id = $2 and left_at is null`,
		roomID, userID, s.clock.Now()); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// Say appends a turn.
//
// The sequence is allocated inside the caller's transaction and unique
// (room_id, seq) makes a race lose rather than produce two turn sevens — the
// same shape as the event sequence in the engine.
func (s *Service) Say(ctx context.Context, roomID string, t *Turn) (*Turn, error) {
	const op = "collab.Service.Say"

	if t.SaidAt.IsZero() {
		t.SaidAt = s.clock.Now()
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	room, err := s.Find(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if !room.Open() {
		return nil, errs.New(op, errs.CodeConflict).
			WithDetail("room %s is closed; a transcript does not gain turns after the session ended", roomID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var next int
	if err := tx.QueryRow(ctx,
		`select coalesce(max(seq), 0) + 1 from forge_room_turns where room_id = $1`, roomID).Scan(&next); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	t.ID, t.RoomID, t.Seq = id.New(id.PrefixTurn), roomID, next

	if _, err := tx.Exec(ctx, `
		insert into forge_room_turns
			(id, room_id, seq, speaker_kind, speaker_id, speaker_label, text, channel, said_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		t.ID, roomID, t.Seq, string(t.Speaker), t.SpeakerID, t.SpeakerLabel,
		t.Text, string(t.Channel), t.SaidAt); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.log.Debug(ctx, logx.EventRoomTurn, "room_id", roomID, "seq", t.Seq, "speaker", string(t.Speaker))
	return t, nil
}

// LinkApproval records that a gate was decided in this room.
//
// A join table rather than a column on forge_approvals: an approval exists
// whether or not a room did, and a nullable room_id on it would put a
// collaboration concern inside the engine's own aggregate.
func (s *Service) LinkApproval(ctx context.Context, roomID, approvalID string) error {
	const op = "collab.Service.LinkApproval"

	if _, err := s.pool.Exec(ctx, `
		insert into forge_room_approvals (room_id, approval_id, linked_at) values ($1,$2,$3)
		on conflict (room_id, approval_id) do nothing`,
		roomID, approvalID, s.clock.Now()); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// Close ends a session.
func (s *Service) Close(ctx context.Context, roomID, byUserID string) error {
	const op = "collab.Service.Close"

	now := s.clock.Now()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`update forge_rooms set status = 'closed', closed_at = $2, updated_at = $2
		  where id = $1 and status = 'open'`, roomID, now)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeConflict).WithDetail("room %s is already closed", roomID)
	}
	// Everybody still in it left when it closed. Without this the transcript
	// says they are present forever, which makes "who was here at the end"
	// answer with people who had gone home.
	if _, err := tx.Exec(ctx,
		`update forge_room_participants set left_at = $2 where room_id = $1 and left_at is null`,
		roomID, now); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.log.Info(ctx, logx.EventRoomClosed, "room_id", roomID, "by", byUserID)
	return nil
}

// Find returns a room with its participants, turns and linked approvals.
func (s *Service) Find(ctx context.Context, roomID string) (*Room, error) {
	const op = "collab.Service.Find"

	var r Room
	err := s.pool.QueryRow(ctx, `
		select id, project_id, goal_id, title, status, opened_by, opened_at, closed_at
		  from forge_rooms where id = $1`, roomID).
		Scan(&r.ID, &r.ProjectID, &r.GoalID, &r.Title, &r.Status, &r.OpenedBy, &r.OpenedAt, &r.ClosedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no room %s", roomID)
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}

	prows, err := s.pool.Query(ctx,
		`select user_id, joined_at, left_at from forge_room_participants where room_id = $1 order by joined_at`, roomID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	for prows.Next() {
		var p Participant
		if err := prows.Scan(&p.UserID, &p.JoinedAt, &p.LeftAt); err != nil {
			prows.Close()
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		r.Participants = append(r.Participants, p)
	}
	prows.Close()

	trows, err := s.pool.Query(ctx, `
		select id, room_id, seq, speaker_kind, speaker_id, speaker_label, text, channel, said_at
		  from forge_room_turns where room_id = $1 order by seq`, roomID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	for trows.Next() {
		var t Turn
		var kind, channel string
		if err := trows.Scan(&t.ID, &t.RoomID, &t.Seq, &kind, &t.SpeakerID, &t.SpeakerLabel,
			&t.Text, &channel, &t.SaidAt); err != nil {
			trows.Close()
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		t.Speaker, t.Channel = SpeakerKind(kind), Channel(channel)
		r.Turns = append(r.Turns, t)
	}
	trows.Close()

	arows, err := s.pool.Query(ctx,
		`select approval_id from forge_room_approvals where room_id = $1 order by linked_at`, roomID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer arows.Close()
	for arows.Next() {
		var a string
		if err := arows.Scan(&a); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		r.ApprovalIDs = append(r.ApprovalIDs, a)
	}
	return &r, arows.Err()
}

// List returns a project's rooms, newest first.
func (s *Service) List(ctx context.Context, projectID string, openOnly bool) ([]Room, error) {
	sql := `select id, project_id, goal_id, title, status, opened_by, opened_at, closed_at
	          from forge_rooms where project_id = $1`
	if openOnly {
		sql += ` and status = 'open'`
	}
	sql += ` order by opened_at desc limit 100`

	rows, err := s.pool.Query(ctx, sql, projectID)
	if err != nil {
		return nil, errs.Wrap("collab.Service.List", errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()
	out := []Room{}
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.GoalID, &r.Title, &r.Status,
			&r.OpenedBy, &r.OpenedAt, &r.ClosedAt); err != nil {
			return nil, errs.Wrap("collab.Service.List", errs.CodeDatabaseUnavail, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
