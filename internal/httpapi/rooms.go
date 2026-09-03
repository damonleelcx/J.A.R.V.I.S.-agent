package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/collab"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/media"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The shared session over HTTP (PRD COL-01).
//
// # Why this exists
//
// collab/room.go has built the durable record since Wave 6 — who was present,
// who said what, which approvals were made — but until now the only way to reach
// it was `forgectl`. An operator could read a transcript; the people actually in
// the meeting had no way to be in one. That is the gap this closes: rooms become
// something a browser can open, join, and watch live.
//
// # The permission choice, stated because it is a choice
//
// Everything here is gated on access.PermProjectRead — the same permission that
// lets somebody see the project at all. A room is a meeting, not project
// content: a viewer who can read the work can sit in the discussion about it and
// speak in it.
//
// What that deliberately does NOT relax is consequence. The approvals made
// during a room are gated by access.PermApprovalDecide exactly as they are
// everywhere else (goals.go), and linking one to a room records where it
// happened rather than granting the right to make it. Being in the room is not
// authority.
//
// No new Permission constant was introduced for this. One would have had to be
// granted to somebody before rooms worked at all, and "you may attend meetings"
// is not a distinction this product needs to make.

// RoomHandlers serve the shared session.
type RoomHandlers struct {
	deps Deps
	svc  *collab.Service
	hub  *collab.Hub
	// sfu is the audio plane. Nil when this deployment has media turned off,
	// which is the default and not an error.
	sfu *media.SFU
	// conv answers when somebody asks FORGE something in a room. Nil when this
	// deployment has no model, which the room survives: everything except FORGE
	// replying works without one.
	conv *agent.Conversation
	// sfuErr records a media plane that was ASKED for and could not be built.
	//
	// Kept apart from a nil sfu so the two are never reported as the same thing:
	// "the operator did not turn it on" and "it is turned on and broken" need
	// different answers. The server still serves rooms either way — audio is an
	// addition to the main path, and losing it must not take text with it.
	sfuErr error
}

// NewRoomHandlers wires the room endpoints.
//
// The hub is attached to the SERVICE, not held only here, so that a turn written
// by any caller reaches the room's subscribers — see the ordering note on
// collab.Service.
func NewRoomHandlers(d Deps) *RoomHandlers {
	hub := collab.NewHub(d.Log)
	h := &RoomHandlers{
		deps: d,
		svc:  collab.NewService(d.Pool, d.Clock, d.Log).WithHub(hub),
		hub:  hub,
	}
	if d.LLM != nil {
		h.conv = agent.NewConversation(d.LLM, persona.DefaultCharacter())
	}
	if d.Config != nil && d.Config.Media.Enabled {
		// The transcriber is optional and separately so: a deployment with audio
		// and no model forwards sound and writes nothing down, which is a
		// supported choice rather than a broken one. Passed as nil when there is
		// no model, and the media plane then has no transcription pipeline at
		// all rather than one that fails on every segment.
		var tr media.Transcriber
		if oc, ok := d.LLM.(*llm.OpenAICompatible); ok && oc != nil {
			tr = oc
		} else if d.Config.Media.Transcribe {
			d.Log.Warn(context.Background(), logx.EventMediaRefused,
				"reason", "transcription is enabled but no model client is configured; room audio will not be written down")
		}
		sfu, err := media.New(media.Options{
			Config: d.Config.Media, Log: d.Log, Clock: d.Clock,
			Transcriber: tr, Turns: h, Activity: h,
		})
		if err != nil {
			// Logged at ERROR and carried, not swallowed. A deployment that asked
			// for audio and did not get it must say so loudly; it must not fail
			// to start, because rooms, presence and the transcript still work.
			d.Log.ErrorWith(context.Background(), logx.EventMediaRefused, err,
				"reason", "the media plane was enabled but could not be built")
			h.sfuErr = err
		} else {
			sfu.SetSignaller(h)
			h.sfu = sfu
		}
	}
	return h
}

// TurnDTO is one utterance as a reader sees it.
//
// SpeakerLabel is sent as recorded rather than resolved from the account,
// matching the record: a transcript that rendered names from forge_users would
// show a renamed account's current name, not who spoke.
type TurnDTO struct {
	ID           string `json:"id"`
	Seq          int    `json:"seq"`
	Speaker      string `json:"speaker"`
	SpeakerID    string `json:"speaker_id,omitempty"`
	SpeakerLabel string `json:"speaker_label"`
	Text         string `json:"text"`
	Channel      string `json:"channel"`
	SaidAt       string `json:"said_at"`
	// Redacted, RedactedAt and RedactedBy describe a turn whose content was
	// deleted under SEC-06. The row is still here on purpose: a transcript that
	// simply omitted it would read as though those seconds were silent.
	Redacted   bool   `json:"redacted,omitempty"`
	RedactedAt string `json:"redacted_at,omitempty"`
	RedactedBy string `json:"redacted_by,omitempty"`
}

// ParticipantDTO is somebody who was in the room.
type ParticipantDTO struct {
	UserID   string `json:"user_id"`
	JoinedAt string `json:"joined_at"`
	LeftAt   string `json:"left_at,omitempty"`
	Present  bool   `json:"present"`
}

// RoomDTO is a session. Turns are omitted from list responses.
type RoomDTO struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	GoalID    string `json:"goal_id,omitempty"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	// Transcribing and AudioPolicy are SEC-06's visible state. The sentence is
	// rendered server-side so every client says the same thing about what
	// happens to what is said — including that transcription sends audio to a
	// third party, which is the part people most need to know.
	Transcribing bool             `json:"transcribing"`
	AudioPolicy  string           `json:"audio_policy"`
	OpenedBy     string           `json:"opened_by"`
	OpenedAt     string           `json:"opened_at"`
	ClosedAt     string           `json:"closed_at,omitempty"`
	Participants []ParticipantDTO `json:"participants,omitempty"`
	Turns        []TurnDTO        `json:"turns,omitempty"`
	ApprovalIDs  []string         `json:"approval_ids,omitempty"`
}

func toTurnDTO(t collab.Turn) TurnDTO {
	dto := TurnDTO{
		ID: t.ID, Seq: t.Seq, Speaker: string(t.Speaker),
		SpeakerLabel: t.SpeakerLabel, Text: t.Text, Channel: string(t.Channel),
		SaidAt: t.SaidAt.UTC().Format(time.RFC3339),
	}
	if t.SpeakerID != nil {
		dto.SpeakerID = *t.SpeakerID
	}
	if t.Redacted() {
		dto.Redacted = true
		dto.RedactedAt = t.RedactedAt.UTC().Format(time.RFC3339)
		if t.RedactedBy != nil {
			dto.RedactedBy = *t.RedactedBy
		}
	}
	return dto
}

func toRoomDTO(r *collab.Room, now time.Time, withTurns, mediaEnabled bool) RoomDTO {
	dto := RoomDTO{
		ID: r.ID, ProjectID: r.ProjectID, Title: r.Title, Status: r.Status,
		Transcribing: r.Transcribing,
		AudioPolicy:  audioPolicy(r.Transcribing, mediaEnabled),
		OpenedBy:     r.OpenedBy, OpenedAt: r.OpenedAt.UTC().Format(time.RFC3339),
		ApprovalIDs: r.ApprovalIDs,
	}
	if r.GoalID != nil {
		dto.GoalID = *r.GoalID
	}
	if r.ClosedAt != nil {
		dto.ClosedAt = r.ClosedAt.UTC().Format(time.RFC3339)
	}
	for _, p := range r.Participants {
		pd := ParticipantDTO{
			UserID:   p.UserID,
			JoinedAt: p.JoinedAt.UTC().Format(time.RFC3339),
			Present:  p.Present(now),
		}
		if p.LeftAt != nil {
			pd.LeftAt = p.LeftAt.UTC().Format(time.RFC3339)
		}
		dto.Participants = append(dto.Participants, pd)
	}
	if withTurns {
		for _, t := range r.Turns {
			dto.Turns = append(dto.Turns, toTurnDTO(t))
		}
	}
	return dto
}

// roomFor loads a room and checks the caller may act in its project.
//
// The project comes from the ROOM's row rather than from the request, so a
// caller cannot name a project they can reach and a room they cannot — the same
// rule as requireGoalPermission.
func (h *RoomHandlers) roomFor(r *http.Request, roomID, userID string) (*collab.Room, error) {
	room, err := h.svc.Find(r.Context(), roomID)
	if err != nil {
		return nil, err
	}
	if err := h.deps.requirePermission(r, room.ProjectID, userID, access.PermProjectRead); err != nil {
		return nil, err
	}
	return room, nil
}

// OpenRoom handles POST /v1/rooms.
func (h *RoomHandlers) OpenRoom(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		GoalID    string `json:"goal_id"`
		Title     string `json:"title"`
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	user, _ := UserFrom(r.Context())
	if err := h.deps.requirePermission(r, req.ProjectID, user.ID, access.PermProjectRead); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	room, err := h.svc.OpenRoom(r.Context(), req.ProjectID, req.GoalID, req.Title, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	// The opener is in the room. Without this the first turn would come from
	// somebody the participant record says was never there.
	if err := h.svc.Join(r.Context(), room.ID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	full, err := h.svc.Find(r.Context(), room.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusCreated, toRoomDTO(full, h.deps.Clock.Now(), true, h.sfu != nil))
}

// List handles GET /v1/rooms?project_id=&open=true.
func (h *RoomHandlers) List(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	user, _ := UserFrom(r.Context())
	if err := h.deps.requirePermission(r, projectID, user.ID, access.PermProjectRead); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	rooms, err := h.svc.List(r.Context(), projectID, r.URL.Query().Get("open") == "true")
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	now := h.deps.Clock.Now()
	out := make([]RoomDTO, 0, len(rooms))
	for i := range rooms {
		out = append(out, toRoomDTO(&rooms[i], now, false, h.sfu != nil))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"rooms": out})
}

// Get handles GET /v1/rooms/{id} — the full record, transcript included.
func (h *RoomHandlers) Get(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	room, err := h.roomFor(r, r.PathValue("id"), user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, toRoomDTO(room, h.deps.Clock.Now(), true, h.sfu != nil))
}

// Join handles POST /v1/rooms/{id}/join.
func (h *RoomHandlers) Join(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	if _, err := h.roomFor(r, roomID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.svc.Join(r.Context(), roomID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	room, err := h.svc.Find(r.Context(), roomID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, toRoomDTO(room, h.deps.Clock.Now(), false, h.sfu != nil))
}

// Leave handles POST /v1/rooms/{id}/leave.
func (h *RoomHandlers) Leave(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	if _, err := h.roomFor(r, roomID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.svc.Leave(r.Context(), roomID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusNoContent, nil)
}

// Say handles POST /v1/rooms/{id}/turns.
//
// The speaker is taken from the authenticated session, never from the body. A
// turn whose speaker a caller could name would let anybody put words in
// somebody else's mouth, which is the one thing the room record must not permit.
func (h *RoomHandlers) Say(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text    string `json:"text"`
		Channel string `json:"channel"`
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	if _, err := h.roomFor(r, roomID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	channel := collab.Channel(req.Channel)
	if req.Channel == "" {
		channel = collab.ChannelText
	}
	userID := user.ID
	turn, err := h.svc.Say(r.Context(), roomID, &collab.Turn{
		Speaker:      collab.SpeakerHuman,
		SpeakerID:    &userID,
		SpeakerLabel: speakerLabel(user.DisplayName, user.Email),
		Text:         req.Text,
		Channel:      channel,
	})
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusCreated, toTurnDTO(*turn))
}

// Close handles POST /v1/rooms/{id}/close.
func (h *RoomHandlers) Close(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	if _, err := h.roomFor(r, roomID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.svc.Close(r.Context(), roomID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	// A closed session carries no audio. Without this the peers survive the
	// transcript that ended, and people go on hearing each other in a room whose
	// record says the meeting is over — with nothing being written down.
	if h.sfu != nil {
		h.sfu.CloseRoom(r.Context(), roomID)
	}
	room, err := h.svc.Find(r.Context(), roomID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, toRoomDTO(room, h.deps.Clock.Now(), false, h.sfu != nil))
}

// streamHeartbeat is how often an idle stream emits a comment frame.
//
// Not decoration: proxies and load balancers close connections that have been
// silent, and a room where nobody has spoken for two minutes is the normal case,
// not an error. The comment costs nothing and is ignored by EventSource.
const streamHeartbeat = 25 * time.Second

// Events handles GET /v1/rooms/{id}/events — the live stream.
//
// Server-Sent Events rather than a WebSocket, matching converse.go: the traffic
// is one-directional and low-rate, it needs no second connection primitive, and
// EventSource reconnects on its own.
//
// # What a client must do with this
//
// The stream is a HINT, not the record. On connect, and again after any `lagged`
// event, a client re-reads GET /v1/rooms/{id} and renders that. Events only save
// it from polling in between. A client that treats the stream as the truth will
// eventually render a transcript with a hole in it.
func (h *RoomHandlers) Events(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	room, err := h.roomFor(r, roomID, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if !room.Open() {
		// A closed room has a transcript, not a stream. Refused rather than
		// handing back a connection that can never produce an event.
		WriteError(w, r, h.deps.Log, errs.New("httpapi.RoomEvents", errs.CodeConflict).
			WithDetail("room %s is closed; read its transcript with GET /v1/rooms/%s", roomID, roomID))
		return
	}

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.RoomEvents", errs.CodeInternal).
			WithDetail("this server cannot flush a response, so it cannot stream"))
		return
	}

	// Subscribe BEFORE announcing readiness, so a turn written between the
	// permission check and the first read is delivered rather than missed.
	sub := h.hub.Subscribe(roomID, user.ID)
	defer sub.Close()
	// The control stream owns the media peer. When this connection ends — a
	// closed tab, a dropped network — the participant's audio goes with it,
	// rather than being forwarded into a socket nobody is reading.
	defer func() {
		if h.sfu != nil {
			h.sfu.Leave(context.WithoutCancel(r.Context()), roomID, sub.ID)
		}
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	h.deps.Log.Info(r.Context(), logx.EventRoomStreamOpened,
		"room_id", roomID, "user_id", user.ID, "stream_id", sub.ID,
		"subscribers", h.hub.Subscribers(roomID))

	send := func(kind string, payload any) bool {
		body, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, body); err != nil {
			return false // the reader went away
		}
		flusher.Flush()
		return true
	}

	// The first frame names the connection, and says whether audio is available
	// on it. A client cannot ask for media before it knows its own stream id —
	// signalling is addressed per connection, not per person — and it should not
	// render a microphone button for a deployment that has no media plane.
	if !send(string(collab.EventHello), map[string]any{
		"stream_id":     sub.ID,
		"user_id":       user.ID,
		"media_enabled": h.sfu != nil,
		"transcribing":  room.Transcribing,
		"audio_policy":  audioPolicy(room.Transcribing, h.sfu != nil),
	}) {
		return
	}

	ticker := time.NewTicker(streamHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-sub.Events:
			if !ok {
				// The hub closed this subscription. If it was for falling
				// behind, say so before hanging up: the client then re-reads the
				// room instead of reconnecting to a transcript with a hole in it.
				if sub.Lagged() {
					send(string(collab.EventLagged), map[string]any{"reread": "/v1/rooms/" + roomID})
				}
				return
			}
			switch ev.Kind {
			case collab.EventTurn:
				if !send(string(collab.EventTurn), toTurnDTO(*ev.Turn)) {
					return
				}
			case collab.EventPresence:
				if !send(string(collab.EventPresence), map[string]any{
					"user_id": ev.UserID, "present": ev.Present,
					"at": ev.At.UTC().Format(time.RFC3339),
				}) {
					return
				}
			case collab.EventRedacted:
				// The fact only. The client re-reads the record rather than being
				// told which turns went, so there is one implementation of what a
				// redacted turn looks like and it is the server's.
				if !send(string(collab.EventRedacted), map[string]any{
					"by": ev.UserID, "at": ev.At.UTC().Format(time.RFC3339),
				}) {
					return
				}
			case collab.EventTranscribing:
				// The privacy state changed under everybody's feet; they are told
				// at once, with the same sentence the room reports on read.
				if !send(string(collab.EventTranscribing), map[string]any{
					"transcribing": ev.Present,
					"by":           ev.UserID,
					"audio_policy": audioPolicy(ev.Present, h.sfu != nil),
					"at":           ev.At.UTC().Format(time.RFC3339),
				}) {
					return
				}
			case collab.EventSpeaking:
				if !send(string(collab.EventSpeaking), map[string]any{
					"user_id": ev.UserID, "stream_id": ev.FromStream,
					"speaking": ev.Present,
					"at":       ev.At.UTC().Format(time.RFC3339),
				}) {
					return
				}
			case collab.EventMediaOffer:
				// Addressed to this connection by the hub; see Event.ToStream.
				if !send(string(collab.EventMediaOffer), map[string]any{"sdp": ev.SDP}) {
					return
				}
			case collab.EventClosed:
				send(string(collab.EventClosed), map[string]any{
					"by": ev.UserID, "at": ev.At.UTC().Format(time.RFC3339),
				})
				return
			}
		}
	}
}

// speakerLabel is the name recorded on a turn.
//
// Display name where there is one, address otherwise. Never empty: Turn.Validate
// refuses an unlabelled human turn, and an account with no display name is
// ordinary rather than an error.
func speakerLabel(displayName, email string) string {
	if s := strings.TrimSpace(displayName); s != "" {
		return s
	}
	return strings.TrimSpace(email)
}
