package media

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Spoken turns into the room's record (PRD AUD-03, COL-01).
//
// # Why nothing is decoded
//
// The SFU forwards Opus and never decodes it. Transcription keeps that property:
// packets are repackaged into an **Ogg Opus** container and sent as-is. The
// alternative — decoding to PCM — needs libopus through cgo, which would break
// the CGO_ENABLED=0 cross-compile that `make dist` depends on, for four
// platforms, in exchange for nothing the provider asked for. Verified against
// the real provider: it accepts Ogg Opus and returns the same text it returns
// for WAV.
//
// # How an utterance is decided, and what that is NOT
//
// A segment closes when the packets stop for `silenceGap`, or when it has run
// for `maxSegment`, whichever comes first.
//
// This is **not** voice activity detection. It cannot be, without decoding: it
// watches packet arrival, not speech. It works because WebRTC clients stop
// sending during silence (discontinuous transmission), and the maximum-duration
// rule is what keeps it from depending on that — a client that streams comfort
// noise continuously still produces segments, just fixed-length ones that may
// cut a sentence in half.
//
// Stated rather than implied, because "silence detection" would suggest an
// accuracy this does not have.
//
// # What a failure here must never do
//
// Silence somebody. Transcription is a side path off the media path: if the
// provider is down, if the queue is full, if a segment is malformed, people go
// on hearing each other and the room goes on working. What is lost is transcript
// content, and that is logged at WARN every time rather than absorbed — a
// transcript with a hole in it that nobody was told about is worse than a
// missing one.

// Transcriber turns one segment of audio into text.
//
// An interface so the media plane can be tested without a provider, and so a
// deployment without transcription simply has none rather than a stub that
// silently returns nothing.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mimeType string) (*llm.Transcript, error)
}

// TurnSink records a spoken turn in the room's transcript.
//
// The media plane does not import the collab package and must not: it knows
// about streams and RTP, and nothing about turns, participants or approvals.
// What it produces is "this person said this"; where that is written down is the
// HTTP layer's business.
type TurnSink interface {
	VoiceTurn(ctx context.Context, roomID string, who Participant, text string) error
}

// ActivitySink reports somebody starting or stopping speaking.
//
// Derived from packet arrival, so it is a coarse signal — see the note above on
// what segmentation is not. It drives the "who is talking" indicator, and it is
// the signal an interruption would be detected from.
type ActivitySink interface {
	SpeechActivity(ctx context.Context, roomID, streamID string, who Participant, speaking bool)
}

// oggMIME is what the container is called on the wire.
const oggMIME = "audio/ogg"

// minSegmentPackets is the shortest segment worth sending.
//
// At 20 ms per packet this is about a fifth of a second. Below it a segment is a
// click, a door, or a microphone being picked up — paying a provider to
// transcribe those would cost money to produce nothing, many times a minute.
const minSegmentPackets = 10

// segment is one participant's audio between two silences.
type segment struct {
	buf     *bytes.Buffer
	ogg     *oggwriter.OggWriter
	started time.Time
	last    time.Time
	packets int
	// owner is the person speaking, remembered when the segment opens.
	//
	// The sweeper closes segments on a timer and has only the key, which names
	// the room and the stream. Attribution cannot be re-derived there — the peer
	// may already have gone — and an unattributed turn is the one thing COL-01
	// refuses to record.
	owner Participant
}

// transcription is the per-SFU transcription pipeline.
type transcription struct {
	tr       Transcriber
	turns    TurnSink
	activity ActivitySink
	clock    clock.Clock
	log      *logx.Logger

	silenceGap time.Duration
	maxSegment time.Duration

	mu   sync.Mutex
	segs map[string]*segment // "roomID\x00streamID" -> open segment
	// rooms records which rooms are being written down (PRD SEC-06).
	//
	// Absent means NO. Fail-closed on purpose: a room missing from this map is
	// one nobody has told us about, and transcribing it would write down a
	// conversation that may have been chosen to be off the record — a harm the
	// people in it would never discover. The opposite mistake loses transcript
	// and is visible in the room immediately.
	rooms map[string]bool

	jobs chan transcribeJob
	stop chan struct{}
	wg   sync.WaitGroup
}

type transcribeJob struct {
	roomID, streamID string
	who              Participant
	audio            []byte
}

func segKey(roomID, streamID string) string { return roomID + "\x00" + streamID }

// newTranscription starts the pipeline. Returns nil if transcription is off.
func newTranscription(c transcriptionDeps) *transcription {
	if c.transcriber == nil || c.turns == nil {
		return nil
	}
	t := &transcription{
		tr: c.transcriber, turns: c.turns, activity: c.activity,
		clock: c.clock, log: c.log,
		silenceGap: c.silenceGap, maxSegment: c.maxSegment,
		segs:  make(map[string]*segment),
		rooms: make(map[string]bool),
		// Bounded on purpose. An unbounded queue turns a slow provider into
		// unbounded memory, and the room is the thing that must not fall over.
		jobs: make(chan transcribeJob, c.queueDepth),
		stop: make(chan struct{}),
	}
	for i := 0; i < c.workers; i++ {
		t.wg.Add(1)
		go t.worker()
	}
	t.wg.Add(1)
	go t.sweep()
	return t
}

type transcriptionDeps struct {
	transcriber Transcriber
	turns       TurnSink
	activity    ActivitySink
	clock       clock.Clock
	log         *logx.Logger
	silenceGap  time.Duration
	maxSegment  time.Duration
	workers     int
	queueDepth  int
}

// setRoom records whether a room is being written down.
func (t *transcription) setRoom(roomID string, on bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.rooms[roomID] = on
	t.mu.Unlock()
}

// transcribing reports whether a room is being written down. Absent means no.
func (t *transcription) transcribing(roomID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rooms[roomID]
}

// flushRoom closes every open segment in a room and sends what was captured.
//
// Used when a room stops being transcribed: audio already captured belongs to
// the period when it WAS, and dropping it would silently lose a sentence
// somebody expected to be in the record. Stopping is not retroactive.
func (t *transcription) flushRoom(ctx context.Context, roomID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	var keys []string
	for key := range t.segs {
		if room, _, _ := splitKey(key); room == roomID {
			keys = append(keys, key)
		}
	}
	t.mu.Unlock()
	for _, key := range keys {
		room, stream, _ := splitKey(key)
		t.closeSegment(ctx, room, stream, Participant{})
	}
}

// forget drops a room's transcription state once nobody is in it.
func (t *transcription) forget(roomID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.rooms, roomID)
	t.mu.Unlock()
}

// write adds one packet to a participant's open segment, opening one if needed.
func (t *transcription) write(ctx context.Context, roomID, streamID string, who Participant, pkt *rtp.Packet, channels uint16) {
	if t == nil {
		return
	}
	// A room that is not being written down produces no segments at all — not
	// segments that are discarded later. There is then nothing captured to leak,
	// nothing buffered to be flushed by a later change of setting, and nothing
	// sent to a speech provider.
	if !t.transcribing(roomID) {
		return
	}
	now := t.clock.Now()
	key := segKey(roomID, streamID)

	// The lock is held across the whole of this, and the callbacks below are
	// fired after it is released.
	//
	// An earlier version unlocked in the middle to announce that somebody had
	// started speaking, then re-locked to write the packet. In that window the
	// sweeper could close and delete the very segment this call was holding, and
	// the write then went into a finalised Ogg container. Nothing about the room
	// would have looked wrong; the transcript would simply have been corrupt.
	t.mu.Lock()
	seg := t.segs[key]
	opened := false
	if seg == nil {
		buf := new(bytes.Buffer)
		// 48 kHz is Opus's internal rate and what WebRTC always negotiates; the
		// channel count comes from the track rather than being assumed, because
		// a header that disagrees with the payload produces a file the provider
		// decodes to noise.
		if channels == 0 {
			channels = 1
		}
		w, err := oggwriter.NewWith(buf, 48000, channels)
		if err != nil {
			t.mu.Unlock()
			t.log.WarnWith(ctx, logx.EventASRFailed, err,
				"room_id", roomID, "stream_id", streamID, "reason", "could not open an Ogg container")
			return
		}
		seg = &segment{buf: buf, ogg: w, started: now, owner: who}
		t.segs[key] = seg
		opened = true
	}
	seg.last = now
	seg.packets++
	err := seg.ogg.WriteRTP(pkt)
	t.mu.Unlock()

	if opened && t.activity != nil {
		t.activity.SpeechActivity(ctx, roomID, streamID, who, true)
	}
	if err != nil {
		t.log.WarnWith(ctx, logx.EventASRFailed, err,
			"room_id", roomID, "stream_id", streamID, "reason", "a packet could not be written to the segment")
	}
}

// sweep closes segments that have gone quiet or run long.
func (t *transcription) sweep() {
	defer t.wg.Done()
	// A quarter of the shortest gap that can close a segment, so the delay this
	// adds is a fraction of the silence a speaker has already produced.
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-tick.C:
			t.flushDue(context.Background())
		}
	}
}

func (t *transcription) flushDue(ctx context.Context) {
	now := t.clock.Now()
	t.mu.Lock()
	var due []string
	for key, seg := range t.segs {
		if now.Sub(seg.last) >= t.silenceGap || now.Sub(seg.started) >= t.maxSegment {
			due = append(due, key)
		}
	}
	t.mu.Unlock()
	for _, key := range due {
		room, stream, _ := splitKey(key)
		t.closeSegment(ctx, room, stream, Participant{})
	}
}

func splitKey(key string) (roomID, streamID string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return key[:i], key[i+1:], true
		}
	}
	return key, "", false
}

// closeSegment finalises a segment and queues it for transcription.
func (t *transcription) closeSegment(ctx context.Context, roomID, streamID string, who Participant) {
	if t == nil {
		return
	}
	key := segKey(roomID, streamID)

	t.mu.Lock()
	seg := t.segs[key]
	if seg == nil {
		t.mu.Unlock()
		return
	}
	delete(t.segs, key)
	owner := seg.owner
	packets := seg.packets
	_ = seg.ogg.Close() // finalises the container into buf
	audio := seg.buf.Bytes()
	t.mu.Unlock()

	if who.UserID == "" {
		// The sweeper closes on a timer and knows only the key. The speaker was
		// remembered when the segment opened, precisely for this.
		who = owner
	}
	if t.activity != nil {
		t.activity.SpeechActivity(ctx, roomID, streamID, who, false)
	}
	if packets < minSegmentPackets {
		return // a click, not an utterance
	}

	select {
	case t.jobs <- transcribeJob{roomID: roomID, streamID: streamID, who: who, audio: audio}:
	default:
		// The queue is full: the provider is slower than the room is talkative.
		// Dropped rather than blocking, because blocking here would back up into
		// the media path and silence people. Logged every time — a transcript
		// losing content silently is the failure this whole package exists to
		// avoid.
		t.log.Warn(ctx, logx.EventASRDropped,
			"room_id", roomID, "stream_id", streamID, "bytes", len(audio),
			"reason", "the transcription queue is full; transcript content was lost")
	}
}

func (t *transcription) worker() {
	defer t.wg.Done()
	for {
		select {
		case <-t.stop:
			return
		case job := <-t.jobs:
			t.run(job)
		}
	}
}

func (t *transcription) run(job transcribeJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	out, err := t.tr.Transcribe(ctx, job.audio, oggMIME)
	if err != nil {
		t.log.WarnWith(ctx, logx.EventASRFailed, err,
			"room_id", job.roomID, "stream_id", job.streamID, "bytes", len(job.audio))
		return
	}
	if out.Text == "" {
		return // silence, a cough, a chair
	}
	if err := t.turns.VoiceTurn(ctx, job.roomID, job.who, out.Text); err != nil {
		// The text was heard and could not be written down. Loud, because the
		// room looks entirely normal while its record is incomplete.
		t.log.WarnWith(ctx, logx.EventASRFailed, err,
			"room_id", job.roomID, "user_id", job.who.UserID, "reason", "the turn could not be recorded")
		return
	}
	t.log.Debug(ctx, logx.EventASRTranscribed,
		"room_id", job.roomID, "stream_id", job.streamID,
		"model", out.Model, "audio_tokens", out.AudioTokens, "chars", len(out.Text))
}

// close stops the pipeline and flushes what is open.
func (t *transcription) close() {
	if t == nil {
		return
	}
	close(t.stop)
	t.wg.Wait()
}
