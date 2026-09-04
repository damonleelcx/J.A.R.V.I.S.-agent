package httpapi

import (
	"context"
	"net/http"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/collab"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/media"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Audio signalling for a shared session (PRD COL-01, AUD-03, NFR-04).
//
// # The shape of the exchange, and why it is this shape
//
// A participant offers once; the server answers. That establishes their UPLINK —
// their microphone — and nothing else, because an answer can only describe media
// sections the offer already contained.
//
// Their DOWNLINKS, one per other participant, arrive by renegotiation: the
// server sends an offer of its own whenever the set of tracks a peer should
// receive changes, which is every time somebody joins or leaves. Those offers
// travel on the room's existing SSE stream, addressed to one connection, and are
// answered here.
//
//	client  POST /v1/rooms/{id}/media/offer   ──▶  uplink established
//	server  SSE  event: media-offer           ──▶  downlinks added
//	client  POST /v1/rooms/{id}/media/answer  ──▶  exchange closed
//
// # Why the stream id is the identity and the user id is not
//
// Signalling is per CONNECTION. One person with two tabs is two peers with two
// different sets of tracks, and an offer meant for one is wrong for the other.
// The stream id is minted by the SSE stream and handed to the client in its
// first frame; a client that has not opened a stream cannot ask for audio,
// which is correct — there would be nowhere to send its offers.

// mediaEnabled reports the media plane, or the reason there is not one.
//
// The two failure modes are kept apart deliberately. MEDIA_DISABLED means the
// operator did not turn it on and names the variable; a construction failure
// means it IS on and broken, and reporting that as "disabled" would send
// somebody to change a setting that is already correct.
func (h *RoomHandlers) mediaEnabled() error {
	const op = "httpapi.RoomMedia"
	if h.sfuErr != nil {
		return errs.Wrap(op, errs.CodeConfigInvalid, h.sfuErr).
			WithDetail("the media plane is enabled but failed to start: %v", h.sfuErr)
	}
	if h.sfu == nil {
		return errs.New(op, errs.CodeMediaDisabled)
	}
	return nil
}

// requireLiveStream checks the caller may act in the room and that the stream
// they name is really theirs.
//
// The ownership check is the one that matters. Without it a caller could name
// somebody else's stream id and renegotiate their audio out from under them —
// the media equivalent of putting words in another person's mouth, which the
// transcript already refuses.
func (h *RoomHandlers) requireLiveStream(r *http.Request, roomID, streamID, userID string) error {
	const op = "httpapi.RoomMedia"

	room, err := h.roomFor(r, roomID, userID)
	if err != nil {
		return err
	}
	if !room.Open() {
		return errs.New(op, errs.CodeConflict).
			WithDetail("room %s is closed; a closed session carries no audio", roomID)
	}
	if streamID == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("stream_id is required; take it from the hello frame on GET /v1/rooms/%s/events", roomID)
	}
	if !h.hub.StreamBelongsTo(roomID, streamID, userID) {
		return errs.New(op, errs.CodeForbidden).
			WithDetail("stream %s is not an open stream of yours in room %s; open GET /v1/rooms/%s/events and use the stream id it gives you",
				streamID, roomID, roomID)
	}
	return nil
}

// MediaOffer handles POST /v1/rooms/{id}/media/offer.
func (h *RoomHandlers) MediaOffer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamID string `json:"stream_id"`
		SDP      string `json:"sdp"`
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.mediaEnabled(); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	if err := h.requireLiveStream(r, roomID, req.StreamID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if req.SDP == "" {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.MediaOffer", errs.CodeValidationFailed).
			WithDetail("sdp is required and was empty"))
		return
	}

	// The room's own setting decides whether anything said here is written down.
	// Read from the record rather than remembered, so a room that was taken off
	// the record stays off it for somebody joining afterwards.
	room, err := h.svc.Find(r.Context(), roomID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	answer, err := h.sfu.Join(r.Context(), media.JoinRequest{
		RoomID: roomID, StreamID: req.StreamID,
		Who:        media.Participant{UserID: user.ID, Label: speakerLabel(user.DisplayName, user.Email)},
		OfferSDP:   req.SDP,
		Transcribe: room.Transcribing,
	})
	if err != nil {
		// A refusal at the ceiling is a normal outcome of a busy room, not a
		// fault, and is logged so an operator can see rooms filling up.
		if errs.Is(err, errs.CodeRoomAtCapacity) {
			h.deps.Log.Info(r.Context(), logx.EventMediaRefused,
				"room_id", roomID, "user_id", user.ID, logx.FieldErrorCode, string(errs.CodeRoomAtCapacity))
		}
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"sdp": answer, "type": "answer"})
}

// MediaAnswer handles POST /v1/rooms/{id}/media/answer.
func (h *RoomHandlers) MediaAnswer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamID string `json:"stream_id"`
		SDP      string `json:"sdp"`
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.mediaEnabled(); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	if err := h.requireLiveStream(r, roomID, req.StreamID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if req.SDP == "" {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.MediaAnswer", errs.CodeValidationFailed).
			WithDetail("sdp is required and was empty"))
		return
	}
	if err := h.sfu.Answer(r.Context(), roomID, req.StreamID, req.SDP); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusNoContent, nil)
}

// OfferTo implements media.Signaller.
//
// Server-initiated offers ride the room's existing SSE stream rather than a
// second connection: the client already holds one, it is already authenticated,
// and its lifetime is exactly the lifetime of the media peer.
func (h *RoomHandlers) OfferTo(ctx context.Context, roomID, streamID, sdp string) error {
	h.hub.Publish(ctx, collab.Event{
		Kind:     collab.EventMediaOffer,
		RoomID:   roomID,
		ToStream: streamID,
		SDP:      sdp,
		At:       h.deps.Clock.Now(),
	})
	return nil
}

// VoiceTurn implements media.TurnSink.
//
// A spoken turn is an ordinary turn. It goes through the same collab.Service.Say
// as a typed one, with the same attribution rule and the same check constraint
// behind it, and differs only in its channel — so a transcript can say which
// turns were spoken aloud without there being two ways to write one down.
func (h *RoomHandlers) VoiceTurn(ctx context.Context, roomID string, who media.Participant, text string) error {
	userID := who.UserID
	_, err := h.svc.Say(ctx, roomID, &collab.Turn{
		Speaker:   collab.SpeakerHuman,
		SpeakerID: &userID,
		// The label as it was when they joined, carried with the participant.
		// Resolving it here would show a renamed account's current name rather
		// than who spoke.
		SpeakerLabel: who.Label,
		Text:         text,
		Channel:      collab.ChannelVoice,
	})
	return err
}

// SpeechActivity implements media.ActivitySink.
//
// Published to the room rather than recorded. Who is talking right now is a fact
// about this instant; the transcript already records what was said, and writing
// a row every time somebody drew breath would bury it.
func (h *RoomHandlers) SpeechActivity(ctx context.Context, roomID, streamID string, who media.Participant, speaking bool) {
	h.hub.Publish(ctx, collab.Event{
		Kind:       collab.EventSpeaking,
		RoomID:     roomID,
		UserID:     who.UserID,
		FromStream: streamID,
		Present:    speaking,
		At:         h.deps.Clock.Now(),
	})
}

// --- AUD-07 controls and SEC-06 privacy ----------------------------------

// audioPolicy is the one true sentence about what happens to what is said.
//
// Rendered server-side rather than composed by each client, for the same reason
// an edge's sentence is: two clients writing their own would eventually describe
// the same state differently, and a privacy statement that disagrees with itself
// is worse than none. It is also deliberately specific about the part people
// most need to know — that transcription sends audio to a third party.
func audioPolicy(transcribing, mediaEnabled bool) string {
	switch {
	case !mediaEnabled:
		return "No audio. This deployment has no media plane, so this room is text only."
	case transcribing:
		return "Audio is forwarded live between participants and sent to a speech " +
			"provider to be transcribed into this room's record. Audio itself is never stored."
	default:
		return "Audio is forwarded live between participants and is not transcribed, " +
			"sent to any provider, or stored. Nothing said aloud is written down."
	}
}

// SetMediaState handles POST /v1/rooms/{id}/media/state — mute, pause, resume.
//
// Enforced at the server (media.SFU.forward drops the packets), not merely
// reported. A mute that only stops the browser sending is a picture of a mute.
func (h *RoomHandlers) SetMediaState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamID string `json:"stream_id"`
		State    string `json:"state"`
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.mediaEnabled(); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	// Your own stream only. Muting somebody else is not a control, it is an
	// assault on their participation, and the ownership rule already exists.
	if err := h.requireLiveStream(r, roomID, req.StreamID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	state := media.State(req.State)
	if err := h.sfu.SetState(r.Context(), roomID, req.StreamID, state); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	// Everybody sees it. A mute nobody else can observe leaves the room
	// wondering why somebody stopped answering.
	h.hub.Publish(r.Context(), collab.Event{
		Kind: collab.EventSpeaking, RoomID: roomID, UserID: user.ID,
		FromStream: req.StreamID, Present: !state.Silent(), At: h.deps.Clock.Now(),
	})
	WriteJSON(w, http.StatusOK, map[string]any{"stream_id": req.StreamID, "state": string(state)})
}

// SetTranscribing handles POST /v1/rooms/{id}/transcribing.
//
// AUD-07 calls this "end-recording". The noun is different on purpose: nothing
// is recorded, because no audio is stored. What stops is the room being written
// down — end-recording(stop transcribing) — and calling it recording would
// promise a deletion of audio that was never kept.
func (h *RoomHandlers) SetTranscribing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
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
	if err := h.svc.SetTranscribing(r.Context(), roomID, req.On, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	// The media plane learns immediately, so the change takes effect on the next
	// packet rather than at the next join.
	if h.sfu != nil {
		h.sfu.SetTranscribing(r.Context(), roomID, req.On)
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"transcribing": req.On,
		"audio_policy": audioPolicy(req.On, h.sfu != nil),
	})
}

// DeleteVoice handles DELETE /v1/rooms/{id}/voice — SEC-06's deletion.
//
// # What "independent audio deletion" means when no audio is stored
//
// It means deleting the voice-derived half of the transcript, leaving typed
// turns and the room itself intact. There is no audio to delete separately
// because none was ever kept — the media plane forwards and the transcriber
// buffers a few seconds in memory.
//
// scope=me deletes only what the caller said, which is the case the requirement
// is really about. scope=room deletes every spoken turn and needs the authority
// to change the project, not merely to be in the room: erasing what other people
// said is not a participant's decision.
func (h *RoomHandlers) DeleteVoice(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	room, err := h.roomFor(r, roomID, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "me"
	}
	var speaker *string
	switch scope {
	case "me":
		id := user.ID
		speaker = &id
	case "room":
		if err := h.deps.requirePermission(r, room.ProjectID, user.ID, access.PermContentWrite); err != nil {
			WriteError(w, r, h.deps.Log, err)
			return
		}
	default:
		WriteError(w, r, h.deps.Log, errs.New("httpapi.DeleteVoice", errs.CodeValidationFailed).
			WithDetail("scope must be me or room; %q is neither", scope))
		return
	}

	n, err := h.svc.RedactVoice(r.Context(), roomID, speaker, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"redacted": n,
		"scope":    scope,
		"effect": "The content of those turns is gone. Each row remains, naming who spoke " +
			"and when it was deleted, so the transcript does not read as though those " +
			"seconds were silent.",
	})
}

// --- FORGE's voice in the room (PRD AUD-01, AUD-05, AUD-07) ---------------

// Ask handles POST /v1/rooms/{id}/ask — a participant puts something to FORGE.
//
// # Why this exists rather than FORGE deciding when to speak
//
// Something has to make FORGE talk, or the voice built underneath this is a
// mechanism with no producer — machinery that works and that nothing ever calls.
// Deciding when FORGE should interject in a conversation between several people
// is a genuine product question and not one to answer by guessing, so this is
// the smaller, explicit path: somebody asks, FORGE answers.
//
// # What happens, in order
//
// The question is recorded, FORGE's reply is recorded, and then it is spoken.
// The record is written before the audio for the same reason every write in this
// wave is: a room where somebody heard something that is not in the transcript
// is the failure COL-01 exists to prevent.
func (h *RoomHandlers) Ask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if h.conv == nil {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.Ask", errs.CodeConfigInvalid).
			WithDetail("no model is configured in this deployment, so FORGE cannot answer. "+
				"Set FORGE_LLM_API_KEY. Everything else in the room works without it."))
		return
	}
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	room, err := h.roomFor(r, roomID, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	// The question, in the record, attributed. Before the answer, so a reply that
	// fails still leaves the room showing what was asked.
	userID := user.ID
	if _, err := h.svc.Say(r.Context(), roomID, &collab.Turn{
		Speaker: collab.SpeakerHuman, SpeakerID: &userID,
		SpeakerLabel: speakerLabel(user.DisplayName, user.Email),
		Text:         req.Text, Channel: collab.ChannelText,
	}); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	reply, err := h.conv.Respond(r.Context(), room.ProjectID, roomHistory(room), req.Text, "", nil)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	// FORGE's turn names FORGE, and carries no user. AUD-05: a transcript where
	// its turns were indistinguishable from a person's would fail the requirement
	// that it always identifies itself as AI — the record has kept them a
	// separate speaker kind since wave 6 precisely for this moment.
	spoken := h.sfu != nil
	channel := collab.ChannelText
	if spoken {
		channel = collab.ChannelVoice
	}
	if _, err := h.svc.Say(r.Context(), roomID, &collab.Turn{
		Speaker: collab.SpeakerForge, SpeakerLabel: "FORGE",
		Text: reply.Speech, Channel: channel,
	}); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	if spoken {
		// Detached, and deliberately: an utterance runs for seconds and the
		// request must not. Its context is separated from the request's for the
		// same reason — the answer should not stop mid-word because the browser
		// that asked navigated away.
		go func(text string) {
			ctx := context.WithoutCancel(r.Context())
			if err := h.sfu.Say(ctx, roomID, text); err != nil {
				// The turn is already in the record; what failed is the delivery.
				// Said loudly rather than swallowed, because the room looks
				// entirely normal while FORGE is inexplicably silent.
				h.deps.Log.WarnWith(ctx, logx.EventTTSFailed, err, "room_id", roomID)
			}
		}(reply.Speech)
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"speech": reply.Speech,
		"detail": reply.Detail,
		"spoken": spoken,
	})
}

// StopSpeaking handles POST /v1/rooms/{id}/stop-speaking — AUD-07's control.
//
// Anybody in the room may use it. Stopping a machine talking is not a privilege:
// the requirement puts it on screen at all times, and a control that asked
// whether you were senior enough to interrupt would not be one.
func (h *RoomHandlers) StopSpeaking(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	roomID := r.PathValue("id")
	if _, err := h.roomFor(r, roomID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	was := h.sfu != nil && h.sfu.Speaking(roomID)
	if h.sfu != nil {
		h.sfu.Silence(r.Context(), roomID)
	}
	// Reports whether there was anything to stop, so a client can tell "stopped"
	// from "there was nothing being said" rather than showing the same thing for
	// both.
	WriteJSON(w, http.StatusOK, map[string]any{"was_speaking": was})
}

// roomHistory turns the room's transcript into conversation history.
//
// Redacted turns are dropped rather than passed as empty: their content was
// deleted under SEC-06, and feeding it to a model would be the one place that
// deletion did not reach.
func roomHistory(room *collab.Room) []agent.Turn {
	out := make([]agent.Turn, 0, len(room.Turns))
	for _, t := range room.Turns {
		if t.Redacted() {
			continue
		}
		role := "user"
		if t.Speaker == collab.SpeakerForge {
			role = "forge"
		}
		out = append(out, agent.Turn{Role: role, Content: t.Text})
	}
	return out
}
