// Package media is the realtime audio plane for a shared session (PRD COL-01,
// AUD-01, AUD-03, NFR-04).
//
// # What this is
//
// A Selective Forwarding Unit. Each participant sends one audio stream up and
// receives one stream per other participant back. The server forwards RTP; it
// does not decode, mix, or re-encode.
//
// # Why an SFU rather than a mesh or a mixer
//
// A **mesh** (everybody connects to everybody) is N×(N−1) connections. It is
// fine for three people and collapses well before NFR-04's twenty, and it leaves
// the server with no audio at all — which would make server-side transcription
// impossible later.
//
// A **mixer** (MCU) decodes every stream, sums them, and re-encodes one. That is
// expensive, and it destroys the thing this product needs most: once the streams
// are summed, "who said that" is gone. AUD-03 asks for speaker separation and
// COL-01 asks for attribution; a mixer makes both unrecoverable.
//
// An **SFU** keeps every participant on their own stream all the way through, so
// "who is speaking" is a property of the transport rather than something guessed
// from the audio afterwards. That is the whole reason for the choice.
//
// # The limit this does NOT solve, stated rather than implied
//
// Separation is per CONNECTION. Four people around one laptop microphone arrive
// as one stream and are one speaker as far as this package is concerned.
// Separating them needs diarization on a mixed stream, which is not built. A
// room where each person has their own client is separated; a conference room
// with a shared microphone is not.
package media

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// State is how somebody is taking part right now (PRD AUD-07).
//
// One enum rather than two booleans, because "muted" and "paused" are not
// independent and a pair of flags would eventually be set to a combination that
// means nothing. A single value has one answer to "what is this person doing".
type State string

const (
	// StateActive — sending and receiving normally.
	StateActive State = "active"
	// StateMuted — microphone off. Still hearing everybody.
	StateMuted State = "muted"
	// StatePaused — stepped out. Neither sending nor being transcribed, still in
	// the room's record as present.
	StatePaused State = "paused"
)

// Valid reports whether s is a state this server recognises.
func (s State) Valid() bool { return s == StateActive || s == StateMuted || s == StatePaused }

// Silent reports whether this state stops the participant's audio.
func (s State) Silent() bool { return s == StateMuted || s == StatePaused }

// Participant is who is behind a stream.
//
// Label travels with the participant rather than being looked up when a turn is
// written, and that is the record's rule rather than a convenience: COL-01 keeps
// the speaker's name AS IT WAS AT THE TIME, because a transcript that resolved
// names later would show a renamed or deleted account's current state instead of
// who spoke. Resolving it at join is the last moment it is certainly correct.
type Participant struct {
	UserID string
	Label  string
}

// Signaller delivers a server-initiated offer to one connection.
//
// An interface rather than a direct dependency on the room hub, because the
// media plane must not reach into the transcript: it knows about streams and
// SDP, and nothing about turns, participants or approvals. The HTTP layer wires
// the two together.
type Signaller interface {
	// OfferTo sends a renegotiation offer to one stream. It must not block on
	// the client answering.
	OfferTo(ctx context.Context, roomID, streamID, sdp string) error
}

// rtpBufferBytes is one read of a forwarded packet.
//
// 1500 is an ordinary MTU. An Opus frame is far smaller; the headroom costs
// nothing and a short read would truncate a packet rather than fail loudly.
const rtpBufferBytes = 1500

// peer is one participant's connection.
type peer struct {
	streamID string
	who      Participant
	roomID   string
	pc       *webrtc.PeerConnection

	// mic is this participant's own audio, republished for everybody else. Nil
	// until their track actually arrives.
	mic *webrtc.TrackLocalStaticRTP

	// negMu serialises renegotiation for this peer, and pending records an offer
	// that could not be sent because one was already in flight.
	//
	// Both halves are needed. WebRTC permits exactly one offer/answer exchange
	// at a time; a second offer sent while the first is unanswered is refused by
	// the client, and the track that prompted it would then never be delivered —
	// somebody in the room would be permanently inaudible, with nothing logged.
	negMu   sync.Mutex
	pending bool

	// sending records which sources this peer already receives, so a track is not
	// added twice and can be removed when its owner leaves.
	sending map[string]*webrtc.RTPSender

	// state is mute and pause, ENFORCED HERE rather than trusted to the client.
	//
	// A mute that only stops the browser sending is a picture of a mute: it is
	// undone by a bug, a stale tab, or anybody who edits the page. AUD-07 asks
	// for a control, and a privacy control that the person relying on it cannot
	// verify is not one. So the server drops the audio, and the client's own
	// track.enabled is a latency optimisation on top of it.
	state atomic.Value // State
}

// currentState reads a peer's state, defaulting to active.
func (p *peer) currentState() State {
	if v, ok := p.state.Load().(State); ok && v != "" {
		return v
	}
	return StateActive
}

// Options is everything the media plane needs.
//
// A struct rather than a parameter list because transcription arrived with four
// collaborators of its own, and a seven-argument constructor is where callers
// start passing them in the wrong order.
type Options struct {
	Config config.MediaConfig
	Log    *logx.Logger
	Clock  clock.Clock
	// Transcriber, Turns and Activity are optional together: with no transcriber
	// or no sink, audio is forwarded and nothing is written down. That is a
	// supported deployment, not a degraded one.
	Transcriber Transcriber
	Turns       TurnSink
	Activity    ActivitySink
	// Speaker gives FORGE a voice in the room. Nil means it has none, and the
	// room carries human audio only — a supported deployment, and the one every
	// build before wave 9.6 had.
	Speaker Speaker
}

// SFU forwards audio between the participants of rooms.
type SFU struct {
	api    *webrtc.API
	cfg    webrtc.Configuration
	log    *logx.Logger
	max    int
	signal Signaller
	// text is the transcription pipeline, or nil when nothing is being written
	// down. Every call on it is nil-safe, so the forwarding path has no branch.
	text *transcription
	// speaker synthesises FORGE's voice, or nil when this deployment has none.
	speaker Speaker
	// voices is FORGE's track per room, created with the room's first
	// participant so an utterance needs no renegotiation before it can be heard.
	voices map[string]*voice

	mu    sync.Mutex
	rooms map[string]map[string]*peer // roomID -> streamID -> peer
}

// New builds the media plane.
//
// Returns an error rather than panicking on a bad port range: the range is
// operator-supplied, and a server that refuses to start with a clear message is
// better than one that starts and cannot carry audio.
func New(o Options) (*SFU, error) {
	const op = "media.New"

	c, log := o.Config, o.Log

	// Only Opus is registered. Registering the default set would advertise video
	// codecs this server will never forward, and an SDP that offers what cannot
	// be delivered is a debugging trap rather than a feature.
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, errs.Wrap(op, errs.CodeConfigInvalid, err)
	}

	// G.711 alongside Opus, for FORGE's own voice. Humans send Opus; FORGE sends
	// PCMU because no pure-Go Opus encoder exists — see speech.go.
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1,
		},
		PayloadType: 0,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, errs.Wrap(op, errs.CodeConfigInvalid, err)
	}

	// The browser's own voice-activity flag, per packet (RFC 6464).
	//
	// This is what makes barge-in real. The alternative — treating any arriving
	// packet as speech — does not work: WebRTC clients send continuously through
	// silence, so FORGE would be interrupted the moment it opened its mouth and
	// could never finish a sentence. The browser has already done the detection;
	// this reads its answer.
	if err := m.RegisterHeaderExtension(
		webrtc.RTPHeaderExtensionCapability{URI: sdp.AudioLevelURI},
		webrtc.RTPCodecTypeAudio); err != nil {
		return nil, errs.Wrap(op, errs.CodeConfigInvalid, err)
	}

	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, errs.Wrap(op, errs.CodeConfigInvalid, err)
	}

	se := webrtc.SettingEngine{}
	if err := se.SetEphemeralUDPPortRange(uint16(c.UDPPortMin), uint16(c.UDPPortMax)); err != nil {
		return nil, errs.Wrap(op, errs.CodeConfigInvalid, err).
			WithDetail("media UDP port range %d-%d was refused: %v", c.UDPPortMin, c.UDPPortMax, err)
	}
	if c.PublicIP != "" {
		// Behind a NAT the server's own view of its address is the private one,
		// and a candidate carrying it is unreachable. Told explicitly because a
		// server cannot discover this about itself reliably.
		se.SetNAT1To1IPs([]string{c.PublicIP}, webrtc.ICECandidateTypeHost)
	}

	var ice []webrtc.ICEServer
	if len(c.ICEServers) > 0 {
		ice = append(ice, webrtc.ICEServer{URLs: c.ICEServers})
	}

	s := &SFU{
		api: webrtc.NewAPI(webrtc.WithMediaEngine(m),
			webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se)),
		cfg:     webrtc.Configuration{ICEServers: ice},
		log:     log,
		max:     c.MaxParticipants,
		rooms:   make(map[string]map[string]*peer),
		voices:  make(map[string]*voice),
		speaker: o.Speaker,
	}
	if c.Transcribe {
		s.text = newTranscription(transcriptionDeps{
			transcriber: o.Transcriber, turns: o.Turns, activity: o.Activity,
			clock: o.Clock, log: log,
			silenceGap: c.SilenceGap, maxSegment: c.MaxSegment,
			workers: c.TranscribeWorkers,
			// Eight segments of headroom per worker. Deep enough to ride out a
			// slow response without dropping; shallow enough that a provider
			// that has stopped answering is noticed in seconds rather than
			// filling memory with audio nobody will ever read.
			queueDepth: c.TranscribeWorkers * 8,
		})
	}
	return s, nil
}

// Close stops the media plane and its transcription pipeline.
func (s *SFU) Close() {
	s.text.close()
}

// SetSignaller wires the channel server-initiated offers travel on.
//
// Separate from New because the signaller is the HTTP layer's room hub and the
// hub is built alongside the handlers, which need the SFU. One of the two has to
// be attached second.
func (s *SFU) SetSignaller(sig Signaller) { s.signal = sig }

// Participants reports how many connections are carrying audio in a room.
func (s *SFU) Participants(roomID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rooms[roomID])
}

// JoinRequest is one participant asking to be heard.
//
// A struct rather than a parameter list: the call grew a room-privacy flag in
// wave 9.4, and "one more string argument" is where callers start passing them
// in the wrong order.
type JoinRequest struct {
	RoomID   string
	StreamID string
	Who      Participant
	OfferSDP string
	// Transcribe is the ROOM's setting, passed in rather than looked up.
	//
	// The zero value is false, and that is deliberate: if this is ever forgotten
	// the room is off the record, which is the failure that harms nobody. The
	// opposite default would transcribe a conversation somebody had chosen to
	// keep off the record, and they would never know.
	Transcribe bool
}

// Join accepts a participant's offer and answers it.
//
// The answer establishes the participant's UPLINK only. Their downlinks — one
// per other participant — are added by a renegotiation once the connection is
// established, because an answer can only describe media sections the offer
// already contained.
func (s *SFU) Join(ctx context.Context, req JoinRequest) (string, error) {
	const op = "media.SFU.Join"

	roomID, streamID, who, offerSDP := req.RoomID, req.StreamID, req.Who, req.OfferSDP
	// Applied before any audio can arrive, so a room that is off the record never
	// has a window in which it is not.
	s.text.setRoom(roomID, req.Transcribe)

	s.mu.Lock()
	room := s.rooms[roomID]
	if room == nil {
		room = make(map[string]*peer)
		s.rooms[roomID] = room
	}
	// NFR-04's ceiling, refused rather than degraded. A room that quietly
	// admitted one more would get worse for everybody already in it, and the
	// person who caused it is the only one who could not tell.
	if _, rejoining := room[streamID]; !rejoining && len(room) >= s.max {
		s.mu.Unlock()
		return "", errs.New(op, errs.CodeRoomAtCapacity).
			WithDetail("room %s already holds %d of %d participants", roomID, len(room), s.max)
	}
	// A re-offer from a stream that is already here replaces its connection: a
	// reconnecting client must not leave its previous peer behind, forwarding
	// into a socket nobody reads.
	if existing := room[streamID]; existing != nil {
		s.mu.Unlock()
		s.Leave(ctx, roomID, streamID)
		s.mu.Lock()
		room = s.rooms[roomID]
		if room == nil {
			room = make(map[string]*peer)
			s.rooms[roomID] = room
		}
	}
	s.mu.Unlock()

	pc, err := s.api.NewPeerConnection(s.cfg)
	if err != nil {
		return "", errs.Wrap(op, errs.CodeInternal, err)
	}
	p := &peer{streamID: streamID, who: who, roomID: roomID, pc: pc,
		sending: make(map[string]*webrtc.RTPSender)}
	p.state.Store(StateActive)

	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		s.onTrack(ctx, p, remote)
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			// Downlinks are attached HERE rather than before the answer is
			// returned. Offering earlier would race the answer over a different
			// connection — the client would receive a renegotiation for a
			// session it has not finished establishing and reject it.
			s.attachExisting(ctx, p)
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			s.log.Info(ctx, logx.EventMediaPeerLeft,
				"room_id", roomID, "stream_id", streamID, "state", state.String())
			s.Leave(ctx, roomID, streamID)
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer, SDP: offerSDP,
	}); err != nil {
		_ = pc.Close()
		return "", errs.Wrap(op, errs.CodeValidationFailed, err).
			WithDetail("the offer could not be applied: %v", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		return "", errs.Wrap(op, errs.CodeInternal, err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		return "", errs.Wrap(op, errs.CodeInternal, err)
	}
	// Waiting for gathering rather than trickling candidates: the client then
	// receives one complete answer and needs no second channel to finish
	// connecting. The server's candidates are host candidates at a known
	// address, so this completes immediately unless a STUN server is configured.
	select {
	case <-gathered:
	case <-ctx.Done():
		_ = pc.Close()
		return "", errs.Wrap(op, errs.CodeInternal, ctx.Err()).
			WithDetail("gathering ICE candidates did not finish before the request was cancelled")
	}

	s.mu.Lock()
	s.rooms[roomID][streamID] = p
	count := len(s.rooms[roomID])
	s.mu.Unlock()

	s.log.Info(ctx, logx.EventMediaPeerJoined,
		"room_id", roomID, "stream_id", streamID, "user_id", who.UserID, "participants", count)
	return pc.LocalDescription().SDP, nil
}

// voiceFor returns FORGE's track for a room, creating it on first use.
//
// Created when the room's first participant arrives rather than when FORGE first
// speaks, so an utterance costs only the provider's ~0.6 s and not a
// renegotiation round trip on top of it. The track exists and is silent until
// there is something to say.
func (s *SFU) voiceFor(roomID string) *voice {
	s.mu.Lock()
	if v := s.voices[roomID]; v != nil {
		s.mu.Unlock()
		return v
	}
	if len(s.rooms[roomID]) == 0 {
		s.mu.Unlock()
		return nil // nobody to hear it
	}
	s.mu.Unlock()

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		forgeStreamID, forgeUserID)
	if err != nil {
		s.log.ErrorWith(context.Background(), logx.EventTTSFailed, err, "room_id", roomID)
		return nil
	}
	v := &voice{track: track}

	s.mu.Lock()
	if existing := s.voices[roomID]; existing != nil {
		s.mu.Unlock()
		return existing // another caller won the race
	}
	s.voices[roomID] = v
	peers := make([]*peer, 0, len(s.rooms[roomID]))
	for _, p := range s.rooms[roomID] {
		peers = append(peers, p)
	}
	s.mu.Unlock()

	for _, p := range peers {
		s.addTrack(context.Background(), p, forgeStreamID, nil, track)
	}
	return v
}

// forgeStreamID and forgeUserID name FORGE's track.
//
// A reserved, non-user identity: the room record already keeps FORGE as a
// distinct speaker kind rather than a null one, because "nobody said this" and
// "FORGE said this" must never look the same (AUD-05). The same rule holds on
// the wire, so a client labelling speakers from the transport cannot mistake it
// for a person.
const (
	forgeStreamID = "stm_forge"
	forgeUserID   = "forge"
)

// onTrack republishes a participant's microphone to everybody else.
func (s *SFU) onTrack(ctx context.Context, p *peer, remote *webrtc.TrackRemote) {
	// The track carries WHO as well as what: its id names the connection and its
	// stream id names the person, so a receiving client labels the speaker from
	// the transport rather than guessing. This is what makes AUD-03's speaker
	// separation structural.
	local, err := webrtc.NewTrackLocalStaticRTP(remote.Codec().RTPCodecCapability,
		p.streamID, p.who.UserID)
	if err != nil {
		s.log.ErrorWith(ctx, logx.EventMediaForwardFailed, err,
			"room_id", p.roomID, "stream_id", p.streamID)
		return
	}

	s.mu.Lock()
	p.mic = local
	others := make([]*peer, 0, len(s.rooms[p.roomID]))
	for _, other := range s.rooms[p.roomID] {
		if other.streamID != p.streamID {
			others = append(others, other)
		}
	}
	s.mu.Unlock()

	for _, other := range others {
		s.addTrack(ctx, other, p.streamID, local, nil)
	}

	go s.forward(ctx, p, remote, local)
}

// forward copies RTP from one participant to the track everybody else receives,
// and — when anything is listening for it — into that participant's transcript.
//
// Packets are read parsed rather than as raw bytes, because the Ogg container
// transcription builds needs the header fields. Forwarding re-marshals them,
// which for audio is negligible beside a DTLS round trip.
func (s *SFU) forward(ctx context.Context, p *peer, remote *webrtc.TrackRemote, local *webrtc.TrackLocalStaticRTP) {
	channels := remote.Codec().Channels
	// The segment must be closed when the speaker stops for good, not left for
	// the sweeper: the last thing somebody said before leaving belongs in the
	// record as much as anything else they said.
	defer s.text.closeSegment(ctx, p.roomID, p.streamID, p.who)

	for {
		pkt, _, err := remote.ReadRTP()
		if err != nil {
			if err != io.EOF {
				s.log.Info(ctx, logx.EventMediaPeerLeft,
					"room_id", p.roomID, "stream_id", p.streamID, "reason", "track ended")
			}
			return
		}
		// Barge-in (AUD-01). The browser's own voice-activity flag, read from the
		// RTP header — not "a packet arrived", which is true continuously and
		// would stop FORGE the instant it started.
		//
		// Deliberately BEFORE the mute check: somebody muted is not interrupting
		// anybody, and the check below returns early.
		if s.speakingNow(pkt) && !p.currentState().Silent() && s.Speaking(p.roomID) {
			s.log.Info(ctx, logx.EventTTSInterrupted,
				"room_id", p.roomID, "by", p.who.UserID, "reason", "a participant spoke")
			s.Silence(ctx, p.roomID)
		}

		// Mute and pause are enforced HERE, at the one place every packet passes.
		// Dropping after this point would still transcribe; dropping before it
		// would still be audible. Both halves of "off" have to be the same
		// decision or the control means different things to different people.
		if p.currentState().Silent() {
			continue
		}

		// Transcription first and never fatal: it is a side path, and a room
		// must go on carrying audio when the thing writing it down cannot.
		s.text.write(ctx, p.roomID, p.streamID, p.who, pkt, channels)

		if err := local.WriteRTP(pkt); err != nil {
			if err == io.ErrClosedPipe {
				return // everybody who was listening has gone
			}
			// A write failure here silences one participant for everybody. It is
			// logged rather than swallowed, because the alternative is a room
			// where somebody is inaudible and nothing says so.
			s.log.WarnWith(ctx, logx.EventMediaForwardFailed, err,
				"room_id", p.roomID, "stream_id", p.streamID)
			return
		}
	}
}

// speakingNow reports whether a packet carries actual speech.
//
// Read from the RFC 6464 audio-level extension the browser already sends. Absent
// — a client that negotiated no extension — the answer is false: FORGE finishing
// its sentence is a smaller harm than FORGE never being able to speak, and the
// explicit stop control (AUD-07) still works either way.
func (s *SFU) speakingNow(pkt *rtp.Packet) bool {
	for _, id := range pkt.Header.GetExtensionIDs() {
		raw := pkt.Header.GetExtension(id)
		if len(raw) != 1 {
			continue
		}
		var level rtp.AudioLevelExtension
		if err := level.Unmarshal(raw); err != nil {
			continue
		}
		return level.Voice
	}
	return false
}

// attachExisting gives a newly connected peer everybody else's audio.
func (s *SFU) attachExisting(ctx context.Context, p *peer) {
	s.mu.Lock()
	type src struct {
		streamID string
		track    *webrtc.TrackLocalStaticRTP
	}
	var sources []src
	for _, other := range s.rooms[p.roomID] {
		if other.streamID != p.streamID && other.mic != nil {
			sources = append(sources, src{other.streamID, other.mic})
		}
	}
	s.mu.Unlock()

	var added bool
	for _, sc := range sources {
		if s.addTrackLocked(ctx, p, sc.streamID, sc.track, nil) {
			added = true
		}
	}
	if added {
		s.renegotiate(ctx, p)
	}
}

// addTrack attaches one source to a peer and renegotiates if anything changed.
func (s *SFU) addTrack(ctx context.Context, p *peer, sourceStreamID string,
	track *webrtc.TrackLocalStaticRTP, sample *webrtc.TrackLocalStaticSample) {
	if s.addTrackLocked(ctx, p, sourceStreamID, track, sample) {
		s.renegotiate(ctx, p)
	}
}

// addTrackLocked adds a source, reporting whether it was new.
//
// Idempotent by source: a track added twice would be heard twice, and the second
// copy would never be removed when its owner left.
func (s *SFU) addTrackLocked(ctx context.Context, p *peer, sourceStreamID string,
	track *webrtc.TrackLocalStaticRTP, sample *webrtc.TrackLocalStaticSample) bool {
	s.mu.Lock()
	if _, already := p.sending[sourceStreamID]; already {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	// Exactly one of the two is set: humans arrive as forwarded RTP, FORGE as
	// synthesised samples. A single method taking both keeps one path for adding
	// a source, removing it, and renegotiating afterwards.
	var added webrtc.TrackLocal = track
	if sample != nil {
		added = sample
	}
	sender, err := p.pc.AddTrack(added)
	if err != nil {
		s.log.WarnWith(ctx, logx.EventMediaForwardFailed, err,
			"room_id", p.roomID, "stream_id", p.streamID, "source", sourceStreamID)
		return false
	}
	s.mu.Lock()
	p.sending[sourceStreamID] = sender
	s.mu.Unlock()

	// RTCP from the receiver has to be drained or it backs up. Nothing is done
	// with it for audio; reading is the point.
	go func() {
		buf := make([]byte, rtpBufferBytes)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	return true
}

// renegotiate offers a peer its current set of tracks.
//
// Serialised per peer, and deferred if an exchange is already in flight — see
// the note on peer.negMu.
func (s *SFU) renegotiate(ctx context.Context, p *peer) {
	p.negMu.Lock()
	if p.pc.SignalingState() != webrtc.SignalingStateStable {
		p.pending = true
		p.negMu.Unlock()
		return
	}
	p.pending = false
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		p.negMu.Unlock()
		s.log.WarnWith(ctx, logx.EventMediaRenegotiateFailed, err,
			"room_id", p.roomID, "stream_id", p.streamID)
		return
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		p.negMu.Unlock()
		s.log.WarnWith(ctx, logx.EventMediaRenegotiateFailed, err,
			"room_id", p.roomID, "stream_id", p.streamID)
		return
	}
	p.negMu.Unlock()

	<-webrtc.GatheringCompletePromise(p.pc)
	if s.signal == nil {
		s.log.Warn(ctx, logx.EventMediaRenegotiateFailed,
			"room_id", p.roomID, "stream_id", p.streamID, "reason", "no signaller is wired")
		return
	}
	if err := s.signal.OfferTo(ctx, p.roomID, p.streamID, p.pc.LocalDescription().SDP); err != nil {
		s.log.WarnWith(ctx, logx.EventMediaRenegotiateFailed, err,
			"room_id", p.roomID, "stream_id", p.streamID)
		return
	}
	s.log.Debug(ctx, logx.EventMediaRenegotiated, "room_id", p.roomID, "stream_id", p.streamID)
}

// SetState mutes, pauses or resumes a participant (PRD AUD-07).
func (s *SFU) SetState(ctx context.Context, roomID, streamID string, st State) error {
	const op = "media.SFU.SetState"

	if !st.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("%q is not a participant state; use active, muted or paused", st)
	}
	s.mu.Lock()
	p := s.rooms[roomID][streamID]
	s.mu.Unlock()
	if p == nil {
		return errs.New(op, errs.CodeNotFound).
			WithDetail("stream %s is not carrying audio in room %s", streamID, roomID)
	}
	p.state.Store(st)
	if st.Silent() {
		// Whatever they were part-way through saying ends here rather than being
		// glued to whatever they say when they come back.
		s.text.closeSegment(ctx, roomID, streamID, p.who)
	}
	s.log.Info(ctx, logx.EventMediaStateChanged,
		"room_id", roomID, "stream_id", streamID, "state", string(st))
	return nil
}

// SetTranscribing changes whether a room is written down, mid-session.
func (s *SFU) SetTranscribing(ctx context.Context, roomID string, on bool) {
	s.text.setRoom(roomID, on)
	if !on {
		// Anything already captured and not yet sent belongs to the period when
		// the room WAS being written down, so it is flushed rather than dropped:
		// stopping is not retroactive, and pretending otherwise would quietly
		// lose a sentence somebody expected to be in the record.
		s.text.flushRoom(ctx, roomID)
	}
	s.log.Info(ctx, logx.EventRoomTranscribing, "room_id", roomID, "on", on)
}

// Answer applies a participant's reply to a server-initiated offer.
func (s *SFU) Answer(ctx context.Context, roomID, streamID, answerSDP string) error {
	const op = "media.SFU.Answer"

	s.mu.Lock()
	p := s.rooms[roomID][streamID]
	s.mu.Unlock()
	if p == nil {
		return errs.New(op, errs.CodeNotFound).
			WithDetail("stream %s is not carrying audio in room %s", streamID, roomID)
	}

	p.negMu.Lock()
	err := p.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: answerSDP,
	})
	pending := p.pending
	p.pending = false
	p.negMu.Unlock()

	if err != nil {
		return errs.Wrap(op, errs.CodeValidationFailed, err).
			WithDetail("the answer could not be applied: %v", err)
	}
	// An offer that arrived while this exchange was open was deferred rather
	// than dropped. Now that the peer is stable again it is sent, or the track
	// that prompted it would never reach this participant.
	if pending {
		s.renegotiate(ctx, p)
	}
	return nil
}

// Leave removes a participant and stops everybody else receiving them.
//
// Idempotent: a client that disconnects and is also reaped by connection state
// arrives here twice, and the second call must be harmless.
func (s *SFU) Leave(ctx context.Context, roomID, streamID string) {
	s.mu.Lock()
	room := s.rooms[roomID]
	p := room[streamID]
	if p == nil {
		s.mu.Unlock()
		return
	}
	delete(room, streamID)
	remaining := make([]*peer, 0, len(room))
	for _, other := range room {
		remaining = append(remaining, other)
	}
	empty := len(room) == 0
	if empty {
		delete(s.rooms, roomID)
	}
	s.mu.Unlock()

	if empty {
		s.Silence(ctx, roomID)
		s.mu.Lock()
		delete(s.voices, roomID)
		s.mu.Unlock()
		// The room's privacy setting goes with it. Left behind, a later room
		// reusing the id would inherit a decision nobody made for it — and the
		// fail-closed default is the safe end to land on.
		s.text.forget(roomID)
	}

	_ = p.pc.Close()

	for _, other := range remaining {
		s.mu.Lock()
		sender := other.sending[streamID]
		delete(other.sending, streamID)
		s.mu.Unlock()
		if sender == nil {
			continue
		}
		if err := other.pc.RemoveTrack(sender); err != nil {
			s.log.WarnWith(ctx, logx.EventMediaForwardFailed, err,
				"room_id", roomID, "stream_id", other.streamID, "source", streamID)
			continue
		}
		// Removing a track changes what this peer receives, so it has to be told.
		// Without this the client keeps a dead audio element for somebody who has
		// left the meeting.
		s.renegotiate(ctx, other)
	}
	s.log.Info(ctx, logx.EventMediaPeerLeft, "room_id", roomID, "stream_id", streamID)
}

// CloseRoom drops every participant. Called when a session ends.
func (s *SFU) CloseRoom(ctx context.Context, roomID string) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.rooms[roomID]))
	for streamID := range s.rooms[roomID] {
		ids = append(ids, streamID)
	}
	s.mu.Unlock()
	for _, streamID := range ids {
		s.Leave(ctx, roomID, streamID)
	}
}

// Describe reports the media plane's shape, for the health surface.
func (s *SFU) Describe() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var peers int
	for _, room := range s.rooms {
		peers += len(room)
	}
	return fmt.Sprintf("%d room(s), %d participant(s), ceiling %d", len(s.rooms), peers, s.max)
}
