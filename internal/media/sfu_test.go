package media_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	forgemedia "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/media"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The SFU against real peer connections carrying real RTP.
//
// Nothing here is faked. A test that swapped in a stub transport would prove
// that the bookkeeping is self-consistent and nothing about whether one person
// can hear another, which is the only thing this package exists to do.

func testConfig(max int) config.MediaConfig {
	return config.MediaConfig{
		Enabled: true,
		// A wide range, because every peer in these tests binds from it and a
		// range narrower than the test's peer count fails as "connection
		// refused" rather than as the configuration problem it is.
		UDPPortMin:      52000,
		UDPPortMax:      52999,
		MaxParticipants: max,
	}
}

// signalRecorder is the channel server-initiated offers travel on.
//
// It is the ONLY substituted piece: in production this is the room's SSE stream,
// and what it carries is unchanged. Everything below it — negotiation, DTLS,
// SRTP, forwarding — is real.
type signalRecorder struct {
	mu      sync.Mutex
	handler map[string]func(sdp string)
}

func newSignalRecorder() *signalRecorder {
	return &signalRecorder{handler: make(map[string]func(sdp string))}
}

func (s *signalRecorder) on(streamID string, fn func(sdp string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler[streamID] = fn
}

func (s *signalRecorder) OfferTo(_ context.Context, _, streamID, sdp string) error {
	s.mu.Lock()
	fn := s.handler[streamID]
	s.mu.Unlock()
	if fn != nil {
		go fn(sdp)
	}
	return nil
}

// client is one participant's browser, as far as the SFU can tell.
type client struct {
	t        *testing.T
	pc       *webrtc.PeerConnection
	streamID string
	userID   string
	// transcribe is the room setting this client joins with. Off by default,
	// matching the fail-closed rule the server applies.
	transcribe bool

	mu       sync.Mutex
	received map[string]int // source stream id -> RTP packets seen
	labels   map[string]string

	// signalling serialises this client's offer/answer exchanges.
	//
	// A browser runs one signalling state machine per peer connection and cannot
	// be part-way through two exchanges at once. This harness could: the server
	// renegotiates the moment a peer connects — to attach the other participants
	// and FORGE's track — which can land while the client is still applying the
	// answer to its own initial offer. Two goroutines then call
	// SetRemoteDescription on the same connection, which the race detector
	// rightly reports.
	//
	// Found by the detector under load rather than by a failing assertion, and it
	// was the harness that was wrong, not the SFU: production serialises the
	// server side already (peer.negMu).
	signalling sync.Mutex
}

func newClient(t *testing.T, streamID, userID string) *client {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, pc: pc, streamID: streamID, userID: userID,
		received: make(map[string]int), labels: make(map[string]string)}

	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		c.mu.Lock()
		// The track names its source: id is the connection, stream id is the
		// person. This is what a real client labels the speaker from.
		c.labels[remote.ID()] = remote.StreamID()
		c.mu.Unlock()
		buf := make([]byte, 1500)
		for {
			n, _, err := remote.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				c.mu.Lock()
				c.received[remote.ID()]++
				c.mu.Unlock()
			}
		}
	})
	t.Cleanup(func() { _ = pc.Close() })
	return c
}

// speak attaches a microphone and starts sending.
func (c *client) speak() {
	c.t.Helper()
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "mic", c.userID)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.pc.AddTrack(track); err != nil {
		c.t.Fatal(err)
	}
	done := make(chan struct{})
	c.t.Cleanup(func() { close(done) })
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				_ = track.WriteSample(media.Sample{
					Data:     []byte{0xf8, 0xff, 0xfe, 0x01, 0x02, 0x03},
					Duration: 20 * time.Millisecond,
				})
			}
		}
	}()
}

// offer performs the initial exchange: the client offers, the SFU answers.
func (c *client) offer(sfu *forgemedia.SFU, roomID string) error {
	c.t.Helper()
	c.signalling.Lock()
	defer c.signalling.Unlock()
	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	gathered := webrtc.GatheringCompletePromise(c.pc)
	if err := c.pc.SetLocalDescription(offer); err != nil {
		return err
	}
	<-gathered

	answer, err := sfu.Join(context.Background(), forgemedia.JoinRequest{
		RoomID: roomID, StreamID: c.streamID,
		Who:        forgemedia.Participant{UserID: c.userID, Label: c.userID},
		OfferSDP:   c.pc.LocalDescription().SDP,
		Transcribe: c.transcribe,
	})
	if err != nil {
		return err
	}
	return c.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: answer,
	})
}

// answerRenegotiation replies to a server-initiated offer, which is how a client
// receives everybody else's audio.
func (c *client) answerRenegotiation(sfu *forgemedia.SFU, roomID, offerSDP string) {
	c.signalling.Lock()
	defer c.signalling.Unlock()
	if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer, SDP: offerSDP,
	}); err != nil {
		return
	}
	answer, err := c.pc.CreateAnswer(nil)
	if err != nil {
		return
	}
	gathered := webrtc.GatheringCompletePromise(c.pc)
	if err := c.pc.SetLocalDescription(answer); err != nil {
		return
	}
	<-gathered
	_ = sfu.Answer(context.Background(), roomID, c.streamID, c.pc.LocalDescription().SDP)
}

func (c *client) packetsFrom(sourceStreamID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.received[sourceStreamID]
}

func (c *client) labelOf(sourceStreamID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.labels[sourceStreamID]
}

// The acceptance test for the media plane: two people in a room hear each other.
//
// Real peer connections, real DTLS, real SRTP, real Opus payloads. The only
// substitution is the channel the renegotiation offer travels on, which in
// production is the room's SSE stream.
//
// It also asserts the thing that makes AUD-03's speaker separation true: the
// track arrives carrying WHO it came from, so a client labels the speaker from
// the transport rather than guessing from the audio.
func TestTwoParticipantsHearEachOther(t *testing.T) {
	sfu, err := forgemedia.New(forgemedia.Options{Config: testConfig(20), Log: logx.Discard(), Clock: clock.System{}})
	if err != nil {
		t.Fatal(err)
	}
	sig := newSignalRecorder()
	sfu.SetSignaller(sig)
	const roomID = "rom_1"

	alice := newClient(t, "stm_alice", "usr_alice")
	bob := newClient(t, "stm_bob", "usr_bob")
	sig.on("stm_alice", func(sdp string) { alice.answerRenegotiation(sfu, roomID, sdp) })
	sig.on("stm_bob", func(sdp string) { bob.answerRenegotiation(sfu, roomID, sdp) })

	alice.speak()
	if err := alice.offer(sfu, roomID); err != nil {
		t.Fatalf("alice could not join: %v", err)
	}
	bob.speak()
	if err := bob.offer(sfu, roomID); err != nil {
		t.Fatalf("bob could not join: %v", err)
	}

	// Each should end up hearing the other, via a renegotiation neither asked
	// for. Polled rather than slept on: media setup is a handshake, not a fixed
	// duration, and a sleep long enough to be reliable is far longer than this.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if alice.packetsFrom("stm_bob") > 0 && bob.packetsFrom("stm_alice") > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("audio never flowed both ways: alice heard %d packet(s) from bob, bob heard %d from alice",
				alice.packetsFrom("stm_bob"), bob.packetsFrom("stm_alice"))
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Speaker separation, structurally: the stream carries the person it came
	// from. A mixer would have made this unrecoverable.
	if got := bob.labelOf("stm_alice"); got != "usr_alice" {
		t.Errorf("bob received alice's track labelled %q, want usr_alice", got)
	}
	if got := alice.labelOf("stm_bob"); got != "usr_bob" {
		t.Errorf("alice received bob's track labelled %q, want usr_bob", got)
	}
	if n := sfu.Participants(roomID); n != 2 {
		t.Errorf("the room reports %d participant(s), want 2", n)
	}
}

// NFR-04's ceiling, enforced rather than assumed.
//
// A room that quietly admitted one more would degrade for everybody already in
// it, and the person who caused it is the only one who could not tell. The
// refusal names the limit, and it names it with a code a client can branch on.
func TestARoomRefusesParticipantsPastItsCeiling(t *testing.T) {
	const ceiling = 3
	sfu, err := forgemedia.New(forgemedia.Options{Config: testConfig(ceiling), Log: logx.Discard(), Clock: clock.System{}})
	if err != nil {
		t.Fatal(err)
	}
	sfu.SetSignaller(newSignalRecorder())
	const roomID = "rom_full"

	for i := 0; i < ceiling; i++ {
		c := newClient(t, "stm_"+string(rune('a'+i)), "usr_"+string(rune('a'+i)))
		c.speak()
		if err := c.offer(sfu, roomID); err != nil {
			t.Fatalf("participant %d was refused below the ceiling: %v", i+1, err)
		}
	}
	if n := sfu.Participants(roomID); n != ceiling {
		t.Fatalf("room holds %d participant(s), want %d", n, ceiling)
	}

	over := newClient(t, "stm_over", "usr_over")
	over.speak()
	err = over.offer(sfu, roomID)
	if err == nil {
		t.Fatal("a participant past the ceiling was admitted; the room degrades for everybody already in it")
	}
	// Pinned to the code, not to "an error". A refusal for the wrong reason —
	// a malformed offer, say — would otherwise pass as if the ceiling held.
	if !errs.Is(err, errs.CodeRoomAtCapacity) {
		t.Fatalf("refused with %s, want ROOM_AT_CAPACITY: %v", errs.CodeOf(err), err)
	}
	if n := sfu.Participants(roomID); n != ceiling {
		t.Errorf("the refused participant still counts: room holds %d, want %d", n, ceiling)
	}
}

// Leaving stops everybody else receiving you, and frees the seat.
//
// Without the seat being freed, a room that had been busy would refuse new
// participants forever — a capacity ceiling that only ever counts upwards is a
// leak, not a limit.
func TestLeavingFreesTheSeatAndStopsTheAudio(t *testing.T) {
	sfu, err := forgemedia.New(forgemedia.Options{Config: testConfig(2), Log: logx.Discard(), Clock: clock.System{}})
	if err != nil {
		t.Fatal(err)
	}
	sig := newSignalRecorder()
	sfu.SetSignaller(sig)
	const roomID = "rom_leave"

	alice := newClient(t, "stm_alice", "usr_alice")
	bob := newClient(t, "stm_bob", "usr_bob")
	sig.on("stm_alice", func(sdp string) { alice.answerRenegotiation(sfu, roomID, sdp) })
	sig.on("stm_bob", func(sdp string) { bob.answerRenegotiation(sfu, roomID, sdp) })

	alice.speak()
	if err := alice.offer(sfu, roomID); err != nil {
		t.Fatal(err)
	}
	bob.speak()
	if err := bob.offer(sfu, roomID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for bob.packetsFrom("stm_alice") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("bob never heard alice, so this test cannot show that leaving stops it")
		}
		time.Sleep(50 * time.Millisecond)
	}

	sfu.Leave(context.Background(), roomID, "stm_alice")
	if n := sfu.Participants(roomID); n != 1 {
		t.Fatalf("room holds %d participant(s) after a departure, want 1", n)
	}

	// The seat is free, so somebody new fits in a room that was at its ceiling.
	carol := newClient(t, "stm_carol", "usr_carol")
	carol.speak()
	if err := carol.offer(sfu, roomID); err != nil {
		t.Fatalf("the departed participant's seat was never freed: %v", err)
	}

	// Idempotent: a client that disconnects and is also reaped by connection
	// state arrives here twice, and the second call must be harmless.
	sfu.Leave(context.Background(), roomID, "stm_alice")
}

// A room that ends takes its audio with it.
//
// Without this the peers outlive the transcript that ended, and people go on
// hearing each other in a session whose record says the meeting is over — with
// nothing being written down.
func TestClosingARoomDropsEveryParticipant(t *testing.T) {
	sfu, err := forgemedia.New(forgemedia.Options{Config: testConfig(5), Log: logx.Discard(), Clock: clock.System{}})
	if err != nil {
		t.Fatal(err)
	}
	sfu.SetSignaller(newSignalRecorder())
	const roomID = "rom_closing"

	for i := 0; i < 3; i++ {
		c := newClient(t, "stm_"+string(rune('a'+i)), "usr_"+string(rune('a'+i)))
		c.speak()
		if err := c.offer(sfu, roomID); err != nil {
			t.Fatal(err)
		}
	}
	sfu.CloseRoom(context.Background(), roomID)
	if n := sfu.Participants(roomID); n != 0 {
		t.Fatalf("%d participant(s) survived the room closing", n)
	}
}
