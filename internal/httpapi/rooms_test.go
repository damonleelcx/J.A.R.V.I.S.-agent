package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/collab"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The shared session over HTTP (PRD COL-01) against live Postgres.
//
// What these reach that the domain fences cannot: the authorisation boundary,
// since a room id arrives from the client; the wire shape, which must give a
// caller no way to attribute a turn to somebody else; and the live stream, whose
// whole purpose is that a second person sees what the first one said.

type roomHarness struct {
	h       *RoomHandlers
	pool    *db.Pool
	member  *identity.User
	other   *identity.User
	project string
}

func roomsHarness(t *testing.T) *roomHarness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
	ctx := context.Background()
	schema := "forge_http_rooms"

	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 6, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	admin, err := db.Connect(ctx, cfg(url), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "drop schema if exists "+schema+" cascade"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := db.Connect(ctx, cfg(url+sep+"search_path="+schema), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	d := testDeps()
	d.Pool = pool
	d.Clock = clock.System{}
	d.Access = access.NewService(pool, d.Clock, logx.Discard())

	now := time.Now().UTC()
	mk := func(email, display string) *identity.User {
		u := &identity.User{ID: id.New(id.PrefixUser), Email: email, DisplayName: display}
		if _, err := pool.Exec(ctx, `
			insert into forge_users (id, email, display_name, status, password_hash, password_algo,
				password_changed_at, created_at, updated_at)
			values ($1,$2,$3,'active','x','argon2id',$4,$4,$4)`,
			u.ID, u.Email, display, now); err != nil {
			t.Fatal(err)
		}
		return u
	}
	rh := &roomHarness{h: NewRoomHandlers(d), pool: pool}
	rh.member = mk("priya@example.com", "Priya")
	rh.other = mk("intruder@example.com", "Intruder")
	rh.project = newProject(t, pool, d.Access, rh.member.ID, "P", now)
	return rh
}

// open creates a room through the API and returns it.
func (rh *roomHarness) open(t *testing.T, title string) RoomDTO {
	t.Helper()
	rec := httptest.NewRecorder()
	rh.h.OpenRoom(rec, req(rh.member, "POST", "/v1/rooms",
		`{"project_id":"`+rh.project+`","title":"`+title+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("open room: %d %s", rec.Code, rec.Body.String())
	}
	var room RoomDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	return room
}

func (rh *roomHarness) say(t *testing.T, user *identity.User, roomID, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := req(user, "POST", "/v1/rooms/"+roomID+"/turns", body)
	r.SetPathValue("id", roomID)
	rh.h.Say(rec, r)
	return rec
}

// COL-01's rule, over the wire: every turn names its speaker, and the speaker is
// the authenticated caller.
//
// The check is on the WIRE SHAPE rather than on a validation branch — there is
// no speaker field to send, and the strict decoder turns an attempt into a
// refusal instead of a quietly ignored field. A room where a caller could name
// the speaker would make the whole record worthless: "Priya approved it" has to
// mean Priya said it.
func TestAPI_ARoomTurnCannotNameSomebodyElseAsTheSpeaker(t *testing.T) {
	rh := roomsHarness(t)
	room := rh.open(t, "design review")

	rec := rh.say(t, rh.member, room.ID,
		`{"text":"the tolerance is fine","speaker_id":"`+rh.other.ID+`"}`)
	if rec.Code == http.StatusCreated {
		t.Fatalf("a turn naming another speaker was accepted: %s", rec.Body.String())
	}

	// And an ordinary turn is attributed to the caller, as recorded at the time.
	rec = rh.say(t, rh.member, room.ID, `{"text":"the tolerance is fine","channel":"voice"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var turn TurnDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &turn); err != nil {
		t.Fatal(err)
	}
	if turn.SpeakerID != rh.member.ID || turn.SpeakerLabel != "Priya" {
		t.Fatalf("turn attributed to %s/%q, want %s/Priya", turn.SpeakerID, turn.SpeakerLabel, rh.member.ID)
	}

	// HTTP 201 is not evidence of a write. The row is read back from Postgres,
	// because a constraint violation swallowed by the driver would still have
	// produced a plausible response above.
	var kind, label, channel string
	var speakerID *string
	if err := rh.pool.QueryRow(context.Background(),
		`select speaker_kind, speaker_id, speaker_label, channel from forge_room_turns where id = $1`,
		turn.ID).Scan(&kind, &speakerID, &label, &channel); err != nil {
		t.Fatalf("the turn is not in the transcript: %v", err)
	}
	if kind != "human" || speakerID == nil || *speakerID != rh.member.ID || label != "Priya" {
		t.Fatalf("stored attribution is %s/%v/%q", kind, speakerID, label)
	}
	if channel != "voice" {
		t.Fatalf("stored channel is %q, want voice — a transcript must say which turns were spoken", channel)
	}
}

// The authorisation boundary. Room ids come from the client, and the project is
// resolved from the ROOM's row, so naming a room in a project you cannot reach
// must refuse rather than leak its transcript.
func TestAPI_ARoomRefusesSomebodyOutsideItsProject(t *testing.T) {
	rh := roomsHarness(t)
	room := rh.open(t, "design review")

	for _, tc := range []struct {
		name string
		call http.HandlerFunc
		verb string
	}{
		{"read", rh.h.Get, "GET"},
		// Search reads the same record through a different door. A boundary that
		// held for Get and not for this one would let a stranger ask what was
		// said in a room they cannot open, one query at a time.
		{"search", rh.h.Search, "GET"},
		{"join", rh.h.Join, "POST"},
		{"speak", rh.h.Say, "POST"},
		{"close", rh.h.Close, "POST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := req(rh.other, tc.verb, "/v1/rooms/"+room.ID, `{"text":"let me in"}`)
			r.SetPathValue("id", room.ID)
			tc.call(rec, r)
			// Specifically 404 NOT_FOUND, and that is the designed answer
			// rather than a bug: access.Require reports a project the caller
			// holds no role in exactly as it reports one that does not exist,
			// so a stranger cannot discover which room ids are real by reading
			// status codes. A member whose ROLE is insufficient gets 403
			// instead — a different case, and not this one.
			//
			// Pinned to the reason, not merely to "some failure": a refusal
			// caused by a broken fixture would otherwise pass as if the
			// boundary had held.
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s by a non-member returned %d %s, want 404", tc.name, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "NOT_FOUND") {
				t.Fatalf("%s refused with an unexpected code: %s", tc.name, rec.Body.String())
			}
		})
	}
}

// The point of the whole phase: somebody in the room sees what somebody else
// said, without polling.
//
// This is the acceptance test for the live spine. It runs the real SSE handler
// against a real server and a real subscriber, because a hub unit test proves
// fan-out and proves nothing about whether the HTTP layer ever delivers it.
func TestAPI_AParticipantSeesAnotherParticipantsTurnLive(t *testing.T) {
	rh := roomsHarness(t)
	room := rh.open(t, "design review")

	// A real server: httptest.NewRecorder does not stream, and a test that used
	// one would pass while the endpoint buffered everything to the end — which
	// is the exact failure this endpoint exists to avoid.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyUser, rh.member))
		r.SetPathValue("id", room.ID)
		rh.h.Events(w, r)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	streamReq, err := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream returned %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("stream Content-Type is %q", ct)
	}

	// Wait until the subscription is registered. Speaking before the handler has
	// subscribed would race, and the resulting flake would look like a lost
	// event rather than a test that spoke too early.
	deadline := time.Now().Add(5 * time.Second)
	for rh.h.hub.Subscribers(room.ID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stream never registered a subscriber")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if rec := rh.say(t, rh.member, room.ID, `{"text":"two point five millimetres","channel":"voice"}`); rec.Code != http.StatusCreated {
		t.Fatalf("say: %d %s", rec.Code, rec.Body.String())
	}

	// The first frame names the connection. A client cannot ask for audio before
	// it knows its own stream id, so this frame arriving first is part of the
	// contract rather than noise to skip past.
	reader := bufio.NewScanner(resp.Body)
	event, data := readSSE(t, reader)
	if event != "hello" {
		t.Fatalf("first event was %q, want hello", event)
	}
	var hello struct {
		StreamID     string `json:"stream_id"`
		UserID       string `json:"user_id"`
		MediaEnabled bool   `json:"media_enabled"`
	}
	if err := json.Unmarshal([]byte(data), &hello); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hello.StreamID, "stm_") {
		t.Fatalf("hello carried stream_id %q; a client has nothing to address media with", hello.StreamID)
	}
	if hello.UserID != rh.member.ID {
		t.Fatalf("hello named user %q, want %s", hello.UserID, rh.member.ID)
	}
	if hello.MediaEnabled {
		t.Error("media_enabled is true in a harness with media off; a client would render a microphone that cannot work")
	}

	event, data = readSSE(t, reader)
	if event != "turn" {
		t.Fatalf("second event was %q, want turn", event)
	}
	var turn TurnDTO
	if err := json.Unmarshal([]byte(data), &turn); err != nil {
		t.Fatal(err)
	}
	if turn.Text != "two point five millimetres" {
		t.Fatalf("streamed turn text is %q", turn.Text)
	}
	// The stream carries the PERSISTED turn: same seq and speaker the transcript
	// will show, so a live view and the record cannot disagree about ordering.
	if turn.Seq != 1 || turn.SpeakerLabel != "Priya" {
		t.Fatalf("streamed turn is seq %d by %q, want seq 1 by Priya", turn.Seq, turn.SpeakerLabel)
	}
}

// A closed room has a transcript, not a stream. Handing back a connection that
// can never produce an event would leave a client waiting forever on a session
// that ended.
func TestAPI_AClosedRoomRefusesAStreamAndSaysWhereToRead(t *testing.T) {
	rh := roomsHarness(t)
	room := rh.open(t, "design review")

	rec := httptest.NewRecorder()
	r := req(rh.member, "POST", "/v1/rooms/"+room.ID+"/close", "")
	r.SetPathValue("id", room.ID)
	rh.h.Close(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r = req(rh.member, "GET", "/v1/rooms/"+room.ID+"/events", "")
	r.SetPathValue("id", room.ID)
	rh.h.Events(rec, r)
	if rec.Code != http.StatusConflict {
		t.Fatalf("streaming a closed room returned %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/v1/rooms/"+room.ID) {
		t.Fatalf("the refusal does not say where to read the transcript: %s", rec.Body.String())
	}
}

// Opening a room puts the opener in it. Without that the first turn would come
// from somebody the participant record says was never there — and "who was
// present when that was approved" is the question the record exists to answer.
func TestAPI_OpeningARoomPutsTheOpenerInIt(t *testing.T) {
	rh := roomsHarness(t)
	room := rh.open(t, "design review")

	if len(room.Participants) != 1 || room.Participants[0].UserID != rh.member.ID {
		t.Fatalf("participants after open: %+v", room.Participants)
	}
	if !room.Participants[0].Present {
		t.Fatal("the opener is not marked present in their own room")
	}
}

// readSSE reads one `event:`/`data:` frame, skipping keep-alive comments.
//
// Takes the scanner rather than the body so successive calls continue where the
// last one stopped. A fresh scanner per frame would re-buffer and lose whatever
// it had already read past — which looks exactly like a dropped event.
func readSSE(t *testing.T, scanner *bufio.Scanner) (event, data string) {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, ":"): // keep-alive
			continue
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			return event, strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatalf("the stream ended without an event: %v", scanner.Err())
	return "", ""
}

// Every room route is mounted, and every one of them requires a session.
//
// This is the gap the handler tests above cannot reach: they call methods
// directly, so they would all pass on a build where the routes were never added
// to the router and no browser could reach a room at all. Writing a handler and
// forgetting to mount it is an ordinary mistake and a silent one.
//
// The two failure codes are what make the assertion precise. 404 means the route
// does not exist; 401 means it exists and refused an anonymous caller. Asserting
// merely "not 200" would pass on an unmounted route.
//
// Known limit, stated rather than implied: this checks the routes it lists. It
// cannot notice a NEW room route added without a case here — but deleting or
// renaming any listed one turns its 401 into a 404 and fails.
func TestAPI_EveryRoomRouteIsMountedAndRequiresASession(t *testing.T) {
	router := NewRouter(testDeps())

	for _, tc := range []struct{ method, target string }{
		{"GET", "/v1/rooms"},
		{"POST", "/v1/rooms"},
		{"GET", "/v1/rooms/room_1"},
		{"GET", "/v1/rooms/room_1/search"},
		{"POST", "/v1/rooms/room_1/join"},
		{"POST", "/v1/rooms/room_1/leave"},
		{"POST", "/v1/rooms/room_1/turns"},
		{"POST", "/v1/rooms/room_1/close"},
		{"GET", "/v1/rooms/room_1/events"},
		{"POST", "/v1/rooms/room_1/media/offer"},
		{"POST", "/v1/rooms/room_1/media/answer"},
		{"POST", "/v1/rooms/room_1/media/state"},
		{"POST", "/v1/rooms/room_1/transcribing"},
		{"DELETE", "/v1/rooms/room_1/voice"},
		{"POST", "/v1/rooms/room_1/ask"},
		{"POST", "/v1/rooms/room_1/stop-speaking"},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(tc.method, tc.target, strings.NewReader("{}"))
			r.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, r)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s %s is not routed; the handler exists but no browser can reach it",
					tc.method, tc.target)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s returned %d to an anonymous caller, want 401: %s",
					tc.method, tc.target, rec.Code, rec.Body.String())
			}
		})
	}
}

// --- the audio plane (PRD COL-01, AUD-03, NFR-04) -------------------------

// mediaHarness is the room harness with the audio plane switched on, and a
// second person who is genuinely a member of the project.
//
// The second member matters. An intruder from outside is stopped by the
// permission check long before anything media-specific runs, so a test using one
// would prove nothing about stream ownership.
func mediaHarness(t *testing.T) (*roomHarness, *identity.User) {
	t.Helper()
	rh := roomsHarness(t)

	d := testDeps()
	d.Pool = rh.pool
	d.Clock = clock.System{}
	d.Access = access.NewService(rh.pool, d.Clock, logx.Discard())
	d.Config.Media = config.MediaConfig{
		Enabled: true, UDPPortMin: 53000, UDPPortMax: 53999, MaxParticipants: 20,
	}
	rh.h = NewRoomHandlers(d)

	if err := d.Access.SetRole(context.Background(), access.Grant{
		ProjectID: rh.project, UserID: rh.other.ID,
		Role: access.RoleContributor, By: rh.member.ID,
	}); err != nil {
		t.Fatal(err)
	}
	return rh, rh.other
}

// With no media plane configured, asking for audio says so and names the switch.
//
// Reported as its own code rather than as a generic failure, because the two
// possible causes need opposite responses: the operator has not turned it on, or
// it is on and broken. "Audio unavailable" with no reason is the kind of thing
// people file bugs about.
func TestAPI_AskingForAudioWithoutAMediaPlaneSaysSo(t *testing.T) {
	rh := roomsHarness(t) // media off, which is the default
	room := rh.open(t, "design review")

	rec := httptest.NewRecorder()
	r := req(rh.member, "POST", "/v1/rooms/"+room.ID+"/media/offer",
		`{"stream_id":"stm_whatever","sdp":"v=0"}`)
	r.SetPathValue("id", room.ID)
	rh.h.MediaOffer(rec, r)

	if rec.Code != http.StatusConflict {
		t.Fatalf("offering audio with no media plane returned %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "MEDIA_DISABLED") {
		t.Fatalf("the refusal does not carry MEDIA_DISABLED: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "FORGE_MEDIA_ENABLED") {
		t.Fatalf("the refusal does not name the variable to set: %s", rec.Body.String())
	}
}

// Nobody can drive somebody else's audio.
//
// This is the media counterpart of the transcript's rule that a caller cannot
// name the speaker. A stream id is a connection, and renegotiating a connection
// that is not yours would let you rearrange what another person in the meeting
// hears — or hang up their microphone.
//
// The attacker here is a REAL member of the project, so the permission check
// passes and what refuses them is stream ownership specifically.
func TestAPI_AParticipantCannotDriveAnotherParticipantsStream(t *testing.T) {
	rh, intruder := mediaHarness(t)
	room := rh.open(t, "design review")

	// A real stream, belonging to the member.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyUser, rh.member))
		r.SetPathValue("id", room.ID)
		rh.h.Events(w, r)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	streamReq, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	event, data := readSSE(t, scanner)
	if event != "hello" {
		t.Fatalf("first event was %q, want hello", event)
	}
	var hello struct {
		StreamID     string `json:"stream_id"`
		MediaEnabled bool   `json:"media_enabled"`
	}
	if err := json.Unmarshal([]byte(data), &hello); err != nil {
		t.Fatal(err)
	}
	if !hello.MediaEnabled {
		t.Fatal("media_enabled is false in a harness with media on")
	}

	for _, tc := range []struct {
		name string
		call http.HandlerFunc
	}{
		{"offer", rh.h.MediaOffer},
		{"answer", rh.h.MediaAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := req(intruder, "POST", "/v1/rooms/"+room.ID+"/media/"+tc.name,
				`{"stream_id":"`+hello.StreamID+`","sdp":"v=0\r\n"}`)
			r.SetPathValue("id", room.ID)
			tc.call(rec, r)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("a member drove another member's stream and got %d: %s",
					rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "FORBIDDEN") {
				t.Fatalf("refused with an unexpected code: %s", rec.Body.String())
			}
		})
	}

	// And the owner of the stream is refused for a DIFFERENT reason — a
	// malformed offer, not a permission problem. Without this the test above
	// would pass on a build that refused everybody, which is not the property
	// being claimed.
	rec := httptest.NewRecorder()
	r := req(rh.member, "POST", "/v1/rooms/"+room.ID+"/media/offer",
		`{"stream_id":"`+hello.StreamID+`","sdp":"v=0\r\n"}`)
	r.SetPathValue("id", room.ID)
	rh.h.MediaOffer(rec, r)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("the stream's own owner was refused as a stranger: %s", rec.Body.String())
	}
}

// A stream id is scoped to its room. One learned in a room you may enter grants
// nothing in a room you may not.
func TestAPI_AStreamIdFromAnotherRoomIsRefused(t *testing.T) {
	rh, _ := mediaHarness(t)
	roomA := rh.open(t, "room A")
	roomB := rh.open(t, "room B")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyUser, rh.member))
		r.SetPathValue("id", roomA.ID)
		rh.h.Events(w, r)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	streamReq, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	_, data := readSSE(t, scanner)
	var hello struct {
		StreamID string `json:"stream_id"`
	}
	if err := json.Unmarshal([]byte(data), &hello); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r := req(rh.member, "POST", "/v1/rooms/"+roomB.ID+"/media/offer",
		`{"stream_id":"`+hello.StreamID+`","sdp":"v=0\r\n"}`)
	r.SetPathValue("id", roomB.ID)
	rh.h.MediaOffer(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("a stream id from room A was accepted in room B: %d %s", rec.Code, rec.Body.String())
	}
}

// --- SEC-06 privacy and AUD-07 controls ----------------------------------

// Deleting what you said removes the content and keeps the fact.
//
// This is where SEC-06 and COL-01 pull against each other, and the resolution is
// asserted rather than assumed: a person may erase what they said, and the
// record must not then read as though those seconds were silent.
func TestAPI_DeletingVoiceRemovesContentAndKeepsTheFact(t *testing.T) {
	rh := roomsHarness(t)
	room := rh.open(t, "design review")

	if rec := rh.say(t, rh.member, room.ID, `{"text":"the tolerance is two point five","channel":"voice"}`); rec.Code != http.StatusCreated {
		t.Fatalf("say: %d %s", rec.Code, rec.Body.String())
	}
	if rec := rh.say(t, rh.member, room.ID, `{"text":"typed, not spoken","channel":"text"}`); rec.Code != http.StatusCreated {
		t.Fatalf("say: %d %s", rec.Code, rec.Body.String())
	}

	rec := httptest.NewRecorder()
	r := req(rh.member, "DELETE", "/v1/rooms/"+room.ID+"/voice?scope=me", "")
	r.SetPathValue("id", room.ID)
	rh.h.DeleteVoice(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Redacted int    `json:"redacted"`
		Effect   string `json:"effect"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Redacted != 1 {
		t.Fatalf("redacted %d turn(s), want 1 — only the spoken one", out.Redacted)
	}

	// Read the room back and check both halves of the rule.
	rec = httptest.NewRecorder()
	r = req(rh.member, "GET", "/v1/rooms/"+room.ID, "")
	r.SetPathValue("id", room.ID)
	rh.h.Get(rec, r)
	var back RoomDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Turns) != 2 {
		t.Fatalf("the transcript holds %d turn(s), want 2; a deleted turn must not vanish", len(back.Turns))
	}
	voice, text := back.Turns[0], back.Turns[1]
	if !voice.Redacted {
		t.Error("the spoken turn is not marked redacted")
	}
	if voice.Text != "" {
		t.Errorf("the spoken turn still carries its content: %q", voice.Text)
	}
	// The fact survives: who spoke, when, and who deleted it.
	if voice.SpeakerLabel == "" || voice.SpeakerID != rh.member.ID {
		t.Errorf("the redacted turn lost its attribution: %+v", voice)
	}
	if voice.RedactedBy != rh.member.ID || voice.RedactedAt == "" {
		t.Errorf("the deletion is unattributed: by=%q at=%q", voice.RedactedBy, voice.RedactedAt)
	}
	// Independent: the typed turn is untouched.
	if text.Redacted || text.Text != "typed, not spoken" {
		t.Errorf("deleting voice took a typed turn with it: %+v", text)
	}

	// And it reached Postgres, not just the response.
	var storedText string
	var redactedAt *time.Time
	if err := rh.pool.QueryRow(context.Background(),
		`select text, redacted_at from forge_room_turns where id = $1`, voice.ID).
		Scan(&storedText, &redactedAt); err != nil {
		t.Fatal(err)
	}
	if storedText != "" || redactedAt == nil {
		t.Fatalf("the stored row still holds %q (redacted_at=%v)", storedText, redactedAt)
	}
}

// scope=me deletes only your own turns, not everybody's.
func TestAPI_DeletingYourOwnVoiceLeavesOtherPeoplesAlone(t *testing.T) {
	rh, other := mediaHarness(t)
	room := rh.open(t, "design review")

	if rec := rh.say(t, rh.member, room.ID, `{"text":"mine","channel":"voice"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	// The other member has to be in the room to speak in it.
	joinRec := httptest.NewRecorder()
	jr := req(other, "POST", "/v1/rooms/"+room.ID+"/join", "")
	jr.SetPathValue("id", room.ID)
	rh.h.Join(joinRec, jr)
	if rec := rh.say(t, other, room.ID, `{"text":"theirs","channel":"voice"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}

	rec := httptest.NewRecorder()
	r := req(rh.member, "DELETE", "/v1/rooms/"+room.ID+"/voice?scope=me", "")
	r.SetPathValue("id", room.ID)
	rh.h.DeleteVoice(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	var remaining int
	if err := rh.pool.QueryRow(context.Background(),
		`select count(*) from forge_room_turns where room_id = $1 and redacted_at is null and channel = 'voice'`,
		room.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("%d unredacted voice turn(s) remain, want 1 — the other person's", remaining)
	}
}

// Erasing everybody's speech needs authority over the project, not merely a seat
// in the room. Deleting what other people said is not a participant's decision.
func TestAPI_ErasingTheWholeRoomsVoiceNeedsMoreThanAttendance(t *testing.T) {
	rh, other := mediaHarness(t) // `other` is a contributor
	room := rh.open(t, "design review")

	// A viewer is in the project and may sit in the room, and may not erase it.
	viewer := rh.member // start from a known-good caller
	_ = viewer
	if err := access.NewService(rh.pool, clock.System{}, logx.Discard()).SetRole(
		context.Background(), access.Grant{
			ProjectID: rh.project, UserID: other.ID,
			Role: access.RoleViewer, By: rh.member.ID,
		}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r := req(other, "DELETE", "/v1/rooms/"+room.ID+"/voice?scope=room", "")
	r.SetPathValue("id", room.ID)
	rh.h.DeleteVoice(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a viewer erased the whole room's speech: %d %s", rec.Code, rec.Body.String())
	}

	// And somebody who DOES hold the permission is allowed. Without this the
	// test above would pass on a build that refused everybody, which is not the
	// property being claimed — the refusal has to be about the role.
	rec = httptest.NewRecorder()
	r = req(rh.member, "DELETE", "/v1/rooms/"+room.ID+"/voice?scope=room", "")
	r.SetPathValue("id", room.ID)
	rh.h.DeleteVoice(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("an owner was refused a room-wide erasure: %d %s", rec.Code, rec.Body.String())
	}
}

// A room says what happens to what is said in it, in one sentence, everywhere.
func TestAPI_ARoomStatesWhatHappensToWhatIsSaid(t *testing.T) {
	rh := roomsHarness(t) // media off in this harness
	room := rh.open(t, "design review")

	if !room.Transcribing {
		t.Error("a new room is not transcribing; COL-01 wants a record by default")
	}
	if room.AudioPolicy == "" {
		t.Fatal("the room states nothing about what happens to what is said")
	}
	// With no media plane the honest statement is that there is no audio at all.
	if !strings.Contains(room.AudioPolicy, "text only") {
		t.Errorf("with media off the policy reads %q", room.AudioPolicy)
	}

	// Turning the transcript off changes the sentence and says nothing is written.
	rec := httptest.NewRecorder()
	r := req(rh.member, "POST", "/v1/rooms/"+room.ID+"/transcribing", `{"on":false}`)
	r.SetPathValue("id", room.ID)
	rh.h.SetTranscribing(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r = req(rh.member, "GET", "/v1/rooms/"+room.ID, "")
	r.SetPathValue("id", room.ID)
	rh.h.Get(rec, r)
	var back RoomDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.Transcribing {
		t.Error("the room still reports itself as transcribing after being turned off")
	}
}

// A closed room's transcript does not change its mind about whether it was
// being written.
func TestAPI_AClosedRoomCannotStartOrStopBeingTranscribed(t *testing.T) {
	rh := roomsHarness(t)
	room := rh.open(t, "design review")

	rec := httptest.NewRecorder()
	r := req(rh.member, "POST", "/v1/rooms/"+room.ID+"/close", "")
	r.SetPathValue("id", room.ID)
	rh.h.Close(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r = req(rh.member, "POST", "/v1/rooms/"+room.ID+"/transcribing", `{"on":false}`)
	r.SetPathValue("id", room.ID)
	rh.h.SetTranscribing(rec, r)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a closed room accepted a change of transcription state: %d %s", rec.Code, rec.Body.String())
	}
}

// A deletion reaches everybody's open transcript, not only the person who made it.
//
// # The defect this exists for
//
// Found in a browser, not in a test. RedactVoice wrote the redaction and
// published nothing, so every other participant's open room went on displaying
// the deleted words indefinitely — and the person who had just asked for them to
// be gone had no way to discover that. A deletion that reaches only the deleter
// is not a deletion, whatever the database says.
//
// The event carries no content. Clients re-read the record, so there is one
// implementation of what a redacted turn looks like and it is the server's.
func TestAPI_ADeletionReachesEverybodyElsesTranscript(t *testing.T) {
	rh := roomsHarness(t)
	room := rh.open(t, "design review")
	if rec := rh.say(t, rh.member, room.ID, `{"text":"the bore is eight millimetres","channel":"voice"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyUser, rh.member))
		r.SetPathValue("id", room.ID)
		rh.h.Events(w, r)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	streamReq, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	if ev, _ := readSSE(t, scanner); ev != "hello" {
		t.Fatalf("first event was %q", ev)
	}
	deadline := time.Now().Add(5 * time.Second)
	for rh.h.hub.Subscribers(room.ID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stream never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	rec := httptest.NewRecorder()
	r := req(rh.member, "DELETE", "/v1/rooms/"+room.ID+"/voice?scope=me", "")
	r.SetPathValue("id", room.ID)
	rh.h.DeleteVoice(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	event, data := readSSE(t, scanner)
	if event != "redacted" {
		t.Fatalf("subscribers were told %q, not that a deletion happened; every open "+
			"transcript would go on showing the deleted words", event)
	}
	var payload struct {
		By string `json:"by"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.By != rh.member.ID {
		t.Errorf("the deletion event names %q as the deleter, want %s", payload.By, rh.member.ID)
	}
	if strings.Contains(data, "bore") {
		t.Errorf("the deletion event carries the deleted content: %s", data)
	}
}

// Nothing is published when nothing was deleted.
//
// Otherwise every client re-reads the whole room each time somebody presses a
// delete button with nothing of theirs to delete.
func TestAPI_ADeletionThatRemovesNothingTellsNobody(t *testing.T) {
	rh := roomsHarness(t)
	room := rh.open(t, "design review")
	// A typed turn only — there is no spoken turn to redact.
	if rec := rh.say(t, rh.member, room.ID, `{"text":"typed only","channel":"text"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}

	sub := rh.h.hub.Subscribe(room.ID, rh.member.ID)
	defer sub.Close()

	rec := httptest.NewRecorder()
	r := req(rh.member, "DELETE", "/v1/rooms/"+room.ID+"/voice?scope=me", "")
	r.SetPathValue("id", room.ID)
	rh.h.DeleteVoice(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	select {
	case ev := <-sub.Events:
		t.Fatalf("a deletion that removed nothing published %s", ev.Kind)
	case <-time.After(200 * time.Millisecond):
	}
}

// Deleted speech does not reach the model.
//
// # Why this is its own fence
//
// SEC-06's deletion removes a turn's content from the record, and every path
// that reads the record has to honour that. Conversation history is the one that
// would not announce itself: a redacted turn passed through here would be sent
// to a provider as part of the next prompt — content somebody asked to have
// deleted, leaving the system by a route nobody was looking at, with the room
// showing "deleted" the whole time.
//
// Dropped rather than passed as empty, because an empty turn in a history is
// still a turn: it tells the model somebody spoke and said nothing, which is not
// what happened.
func TestDeletedSpeechIsNotSentToTheModel(t *testing.T) {
	at := time.Now().UTC()
	who := "usr_1"
	room := &collab.Room{
		Turns: []collab.Turn{
			{Speaker: collab.SpeakerHuman, SpeakerID: &who, SpeakerLabel: "Priya",
				Text: "the bore is eight millimetres", Channel: collab.ChannelVoice},
			{Speaker: collab.SpeakerForge, SpeakerLabel: "FORGE",
				Text: "Noted.", Channel: collab.ChannelVoice},
			{Speaker: collab.SpeakerHuman, SpeakerID: &who, SpeakerLabel: "Priya",
				Text: "", Channel: collab.ChannelVoice, RedactedAt: &at, RedactedBy: &who},
		},
	}

	history := roomHistory(room)
	if len(history) != 2 {
		t.Fatalf("history has %d turn(s), want 2 — the redacted one must not be there: %+v",
			len(history), history)
	}
	for _, turn := range history {
		if turn.Content == "" {
			t.Error("an empty turn reached the history; it tells the model somebody spoke and said nothing")
		}
	}
	// FORGE's own turns come back as FORGE, not as another participant. A model
	// told its own words were somebody else's will answer them.
	if history[0].Role != "user" || history[1].Role != "forge" {
		t.Errorf("roles are %q and %q, want user and forge", history[0].Role, history[1].Role)
	}
}

// Searching a transcript over HTTP (PRD AUD-06).
//
// The domain test in internal/domain/collab covers what matches; this covers
// what a browser actually receives — that the route is wired to the service, and
// that a result is shaped like the transcript rather than like a search engine.
// The room page renders both through the same function, so a divergence here
// shows up as search results that draw differently from the record they came
// from.
func TestAPI_TheTranscriptCanBeSearchedOverHTTP(t *testing.T) {
	rh := roomsHarness(t)
	room := rh.open(t, "design review")

	for _, text := range []string{
		"the bracket is too thin at the root",
		"we should widen both brackets by 2mm",
		"the tolerance is fine",
	} {
		if rec := rh.say(t, rh.member, room.ID, `{"text":`+strconv.Quote(text)+`}`); rec.Code != http.StatusCreated {
			t.Fatalf("saying %q returned %d: %s", text, rec.Code, rec.Body.String())
		}
	}

	search := func(q string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		r := req(rh.member, "GET", "/v1/rooms/"+room.ID+"/search?q="+url.QueryEscape(q), "")
		r.SetPathValue("id", room.ID)
		rh.h.Search(rec, r)
		return rec
	}

	// The plural finds the singular. This is the whole reason the search moved
	// off the page: in the browser's substring filter it returned nothing.
	rec := search("brackets")
	if rec.Code != http.StatusOK {
		t.Fatalf("searching returned %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Turns []TurnDTO `json:"turns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the search result: %v\nbody: %s", err, rec.Body.String())
	}
	if len(got.Turns) != 2 {
		t.Fatalf("searching \"brackets\" returned %d turns, want 2: %s", len(got.Turns), rec.Body.String())
	}
	// Shaped like the transcript: whoever said it, when, and through which
	// channel. A search result that carried only text would have to be rendered
	// by a second code path, which is how two views of one record start
	// disagreeing about who said what.
	for _, turn := range got.Turns {
		if turn.ID == "" || turn.SpeakerLabel == "" || turn.SaidAt == "" || turn.Seq == 0 {
			t.Errorf("a search result is missing its attribution: %+v", turn)
		}
	}
	if got.Turns[0].Seq > got.Turns[1].Seq {
		t.Error("search results are not in the order they were said")
	}

	// An empty query is refused rather than answered with the whole transcript.
	if rec := search("   "); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty search returned %d, want 400: %s", rec.Code, rec.Body.String())
	}
	// And what people type does not produce a 500. to_tsquery would have.
	for _, q := range []string{"bracket and", "bracket &", "!!!"} {
		if rec := search(q); rec.Code != http.StatusOK {
			t.Errorf("searching %q returned %d, want 200: %s", q, rec.Code, rec.Body.String())
		}
	}
}
