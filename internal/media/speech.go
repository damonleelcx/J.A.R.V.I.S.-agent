package media

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// FORGE's own voice in a room (PRD AUD-01, AUD-05, AUD-07).
//
// # Why the server speaks rather than each browser
//
// A room is shared. If every client synthesised FORGE's words itself, everybody
// would hear a different voice — browsers ship different ones — starting at
// slightly different moments, and an interruption would stop one person's
// playback while the others talked on. That is not one participant speaking; it
// is several recordings of the same sentence.
//
// So FORGE joins the room as a participant with a track of its own. Everybody
// hears the same audio at the same instant, and barge-in is a real stop: cut the
// track and it stops for everyone together.
//
// # Why the voice is G.711 and not Opus
//
// The provider streams raw 16-bit PCM at 24 kHz. Putting that into the SFU means
// encoding, and **there is no usable pure-Go Opus encoder** — pion/opus ships a
// decoder only, and every working encoder is cgo bindings to libopus. Taking cgo
// would end CGO_ENABLED=0 for the four platforms `make dist` builds.
//
// G.711 µ-law needs no library: it is arithmetic, and every WebRTC stack has
// supported it for twenty years. The cost is 8 kHz telephone quality against the
// humans' 48 kHz Opus, and the cost was measured rather than assumed — the same
// utterance pushed through this exact transform still transcribes as "two point
// five", identically to the 24 kHz original. Telephone quality loses timbre, not
// numbers, and numbers are what this product cannot afford to lose.
//
// See `docs/spikes/2026-09-03-forge-voice-in-a-room/`.

// Speaker synthesises text into audio. Implemented by the model client.
//
// onPCM receives 16-bit little-endian mono samples at llm.SpeechSampleRate.
// Returning an error stops the stream, which is how an interruption cancels an
// utterance the provider is still generating.
type Speaker interface {
	Speak(ctx context.Context, text string, onPCM func([]byte) error) error
}

const (
	// g711Rate is what PCMU carries. Fixed by the codec, not a choice.
	g711Rate = 8000
	// ttsRate must match llm.SpeechSampleRate. Duplicated as a constant rather
	// than imported so this package does not depend on the model client for a
	// number; the fence asserts they agree, because a mismatch plays FORGE's
	// voice at the wrong pitch — audible, and easy to mistake for a bad voice.
	ttsRate = 24000
	// decimation is the exact integer ratio between them.
	decimation = ttsRate / g711Rate
	// frameSamples is one 20 ms G.711 frame: the packetisation every WebRTC
	// stack expects for audio.
	frameSamples = g711Rate / 50
	frameLen     = 20 * time.Millisecond
)

// TTSSampleRate is the rate FORGE's speech arrives at, exported so a fence can
// assert it matches what the model client actually synthesises. The two are
// separate constants on purpose — the media plane does not depend on the model
// client for a number — and a disagreement is only audible, never an error.
const TTSSampleRate = ttsRate

// EncodeULaw encodes one 16-bit sample as G.711 µ-law.
//
// Exported for the fence that checks it against an independent implementation.
// A dozen lines of bit manipulation written from a spec is exactly the kind of
// code that is self-consistently wrong, and a test using my own decoder would
// agree with any such mistake.
func EncodeULaw(sample int16) byte { return pcmToULaw(sample) }

// DecodeULaw decodes one µ-law code, for the fence that compares this encoder's
// accuracy against another's. Decoding is not otherwise needed: the SFU forwards
// FORGE's audio without ever reading it back.
func DecodeULaw(code byte) int { return ulawToLinear(code) }

// voice is FORGE's track in one room.
type voice struct {
	track *webrtc.TrackLocalStaticSample

	mu sync.Mutex
	// cancel stops whatever is currently being said. Nil when silent.
	cancel context.CancelFunc
	// speaking is read by the barge-in path on every inbound packet, so it is
	// kept separate from cancel rather than derived from it.
	speaking bool
	// pending holds samples not yet a whole frame. Speech arrives in provider-
	// sized chunks that do not divide into 20 ms.
	pending []byte
}

// Say speaks text into a room, interrupting whatever FORGE was saying.
//
// Returns when the utterance finishes or is interrupted. The caller decides
// whether to wait; the room does not.
func (s *SFU) Say(ctx context.Context, roomID, text string) error {
	if s.speaker == nil {
		return nil // no speech configured; the room is text and human audio only
	}
	v := s.voiceFor(roomID)
	if v == nil {
		return nil // nobody is in the room to hear it
	}

	// A new utterance replaces the old one. Two overlapping sentences from the
	// same speaker is not a conversation, and whoever asked for this one wants
	// it said now.
	s.Silence(ctx, roomID)

	speech, cancel := context.WithCancel(ctx)
	v.mu.Lock()
	v.cancel = cancel
	v.speaking = true
	v.pending = v.pending[:0]
	v.mu.Unlock()

	defer func() {
		cancel()
		v.mu.Lock()
		v.cancel, v.speaking = nil, false
		v.mu.Unlock()
	}()

	started := time.Now()
	var written int
	err := s.speaker.Speak(speech, text, func(pcm []byte) error {
		if speech.Err() != nil {
			return speech.Err() // interrupted; stops the provider stream too
		}
		n, err := v.write(pcm)
		written += n
		return err
	})
	// Whatever is left over is under one frame — a few milliseconds. Dropped
	// rather than padded: a partial frame of silence at the end of every sentence
	// is a click.
	if err != nil {
		s.log.WarnWith(ctx, logx.EventTTSFailed, err, "room_id", roomID)
		return err
	}
	if speech.Err() != nil {
		s.log.Info(ctx, logx.EventTTSInterrupted, "room_id", roomID,
			"spoken_ms", written/(g711Rate/1000))
		return nil
	}
	s.log.Info(ctx, logx.EventTTSSpoke, "room_id", roomID,
		"chars", len(text), "spoken_ms", written/(g711Rate/1000),
		"first_audio_ms", time.Since(started).Milliseconds())
	return nil
}

// Silence stops FORGE mid-sentence. Safe when it is not speaking.
//
// This is AUD-07's stop-speaking and the acting half of AUD-01's barge-in. It
// cancels the provider stream as well as the playback, so an interrupted
// sentence is not still being paid for.
func (s *SFU) Silence(ctx context.Context, roomID string) {
	s.mu.Lock()
	v := s.voices[roomID]
	s.mu.Unlock()
	if v == nil {
		return
	}
	v.mu.Lock()
	cancel := v.cancel
	v.cancel, v.speaking = nil, false
	v.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Speaking reports whether FORGE is talking in a room.
func (s *SFU) Speaking(roomID string) bool {
	s.mu.Lock()
	v := s.voices[roomID]
	s.mu.Unlock()
	if v == nil {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.speaking
}

// write turns provider PCM into whole G.711 frames on the track.
//
// Returns the number of 8 kHz samples written, so the caller can report how much
// was actually said — which is the only honest measure of an interrupted turn.
func (v *voice) write(pcm []byte) (int, error) {
	v.mu.Lock()
	v.pending = append(v.pending, pcm...)
	frames, rest := FramesFrom(v.pending)
	v.pending = append(v.pending[:0], rest...)
	v.mu.Unlock()

	var written int
	for _, frame := range frames {
		if err := v.track.WriteSample(pionmedia.Sample{Data: frame, Duration: frameLen}); err != nil {
			return written, err
		}
		written += frameSamples
	}
	return written, nil
}

// FramesFrom turns provider PCM into whole 20 ms G.711 frames, returning what
// was left over.
//
// Exported so the end-to-end fence can put real speech through the SAME
// transform the room uses. A test that resampled and encoded its own copy would
// prove that its copy works.
//
// Leftovers are returned rather than padded: speech arrives in provider-sized
// chunks that do not divide into 20 ms, and a partial frame of silence between
// every pair of chunks is a click on every syllable.
func FramesFrom(buf []byte) (frames [][]byte, rest []byte) {
	// Two 24 kHz bytes per sample, three samples per output sample.
	const inPerOut = 2 * decimation
	whole := len(buf) / (inPerOut * frameSamples)
	if whole == 0 {
		return nil, buf
	}
	frames = make([][]byte, 0, whole)
	for i := 0; i < whole; i++ {
		start := i * inPerOut * frameSamples
		frame := make([]byte, frameSamples)
		for j := 0; j < frameSamples; j++ {
			// Average each group of three input samples before taking one.
			//
			// A box filter rather than picking every third sample: plain
			// decimation folds everything above 4 kHz back into the audible band
			// as aliasing, which sounds like a lisp on exactly the consonants
			// that distinguish spoken digits. Crude, and enough — the output is
			// telephone-band anyway.
			at := start + j*inPerOut
			sum := 0
			for k := 0; k < decimation; k++ {
				sum += int(int16(uint16(buf[at+k*2]) | uint16(buf[at+k*2+1])<<8))
			}
			frame[j] = pcmToULaw(int16(sum / decimation))
		}
		frames = append(frames, frame)
	}
	return frames, buf[whole*inPerOut*frameSamples:]
}

// pcmToULaw encodes one 16-bit sample as G.711 µ-law, choosing the code whose
// decoded value is NEAREST the input.
//
// # Why a table built from the decoder, rather than the textbook algorithm
//
// The classic encoder (Sun's g711.c, and every copy of it) truncates: it finds
// the segment and drops the low bits. That is valid µ-law and it is what this
// function did first — until a cross-check against ffmpeg's independent encoder
// disagreed on 496 samples out of 24000, always by one code.
//
// Decoding both candidates settled which was right. For an input of 124, the
// truncating answer decodes to 132 and the nearest answer to 120: errors of 8
// and 4. Truncation is systematically worse, by up to a whole quantisation step,
// on the quiet samples where µ-law's resolution is finest and speech spends most
// of its time.
//
// So the table is built by inverting the decoder and taking the nearest code —
// which is also what ffmpeg does, and is why the fence can assert byte equality
// against it rather than settling for "close enough".
var ulawTable = buildULawTable()

func pcmToULaw(sample int16) byte { return ulawTable[uint16(sample)] }

// ulawToLinear decodes one µ-law code (ITU-T G.711).
func ulawToLinear(u byte) int {
	u = ^u
	t := (int(u&0x0F) << 3) + 0x84
	t <<= (u & 0x70) >> 4
	if u&0x80 != 0 {
		return 0x84 - t
	}
	return t - 0x84
}

// buildULawTable maps every 16-bit sample to its nearest µ-law code.
//
// Sign is decided first and magnitude second, which is how µ-law is actually
// structured: codes 0x80-0xFF are positive, 0x00-0x7F are their mirror, and the
// two halves differ only in bit 7.
//
// Doing it that way is not tidiness. A single nearest-neighbour search over all
// 256 codes has an ambiguity at zero — µ-law has BOTH a positive and a negative
// zero, decoding to the same value — so the choice fell to whichever the sort
// happened to place first, and an unstable sort made half the samples disagree
// with the reference. Splitting by sign means the question never arises.
//
// 64 KiB, built once at start-up. The walk is linear because decoded magnitude
// is monotonic in the sorted list, so the cursor only moves forwards.
func buildULawTable() *[65536]byte {
	type level struct {
		magnitude int
		code      byte
	}
	// The 128 positive codes and what each decodes to.
	levels := make([]level, 128)
	for i := 0; i < 128; i++ {
		code := byte(0x80 | i)
		levels[i] = level{ulawToLinear(code), code}
	}
	sort.Slice(levels, func(a, b int) bool { return levels[a].magnitude < levels[b].magnitude })

	abs := func(n int) int {
		if n < 0 {
			return -n
		}
		return n
	}
	table := new([65536]byte)
	cursor := 0
	for v := 0; v <= 32767; v++ {
		// Strictly nearer, so an exact tie — every midpoint between two codes —
		// keeps the smaller magnitude, matching the reference encoder.
		for cursor+1 < len(levels) && abs(levels[cursor+1].magnitude-v) < abs(levels[cursor].magnitude-v) {
			cursor++
		}
		code := levels[cursor].code
		table[uint16(int16(v))] = code
		if v != 0 {
			// The negative half is the positive one with the sign bit flipped.
			table[uint16(int16(-v))] = code ^ 0x80
		}
	}
	// -32768 has no positive counterpart to mirror; it clamps to the loudest code.
	// Through a variable because the constant conversion is rejected at compile
	// time, being out of uint16's range before the int16 reinterpretation.
	var mostNegative int16 = math.MinInt16
	table[uint16(mostNegative)] = levels[len(levels)-1].code ^ 0x80
	return table
}
