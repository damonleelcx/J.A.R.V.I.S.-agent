package media_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	forgemedia "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/media"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Spoken audio becoming an attributed turn (PRD AUD-03, COL-01).
//
// # The joint these tests exist for
//
// The pipeline is RTP → Ogg Opus → provider → turn. A test with a fake
// transcriber proves segmentation and attribution and would happily pass on a
// build that produced a MALFORMED Ogg container, because a fake never tries to
// decode it. So the container the pipeline actually produced is captured, and a
// second test — against the real provider — transcribes that exact byte string.

// realOpusFrames reads individual Opus packets out of the committed fixture.
//
// Real encoded speech rather than synthetic payloads: an Ogg container assembled
// from made-up bytes is well-formed and transcribes to nothing, which would make
// the end-to-end test below silently vacuous.
//
// The pages are walked by hand because pion's oggreader returns whole PAGES and
// does not expose the segment table. A page holds roughly fifty 20 ms packets,
// so feeding pages to the track would send fifty frames glued together as one —
// producing a container that is well-formed and decodes to noise, which is
// exactly the failure this fixture exists to rule out.
//
// Ogg page layout: "OggS", version, headerType, granule(8), serial(4), seq(4),
// crc(4), segmentCount(1), segmentTable(segmentCount), then the segment bodies.
// A packet ends at the first segment shorter than 255.
func realOpusFrames(t *testing.T, path string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var packets [][]byte
	var current []byte
	for off := 0; off+27 <= len(raw); {
		if !bytes.Equal(raw[off:off+4], []byte("OggS")) {
			t.Fatalf("expected an Ogg page at byte %d", off)
		}
		count := int(raw[off+26])
		tableAt := off + 27
		if tableAt+count > len(raw) {
			break
		}
		table := raw[tableAt : tableAt+count]
		body := tableAt + count

		for _, segLen := range table {
			if body+int(segLen) > len(raw) {
				break
			}
			current = append(current, raw[body:body+int(segLen)]...)
			body += int(segLen)
			if segLen < 255 { // the packet ends here
				packets = append(packets, current)
				current = nil
			}
		}
		off = body
	}

	// The first two packets are OpusHead and OpusTags — the container's own
	// headers, not speech. Sending them as audio would prepend garbage.
	var frames [][]byte
	for _, pkt := range packets {
		if bytes.HasPrefix(pkt, []byte("OpusHead")) || bytes.HasPrefix(pkt, []byte("OpusTags")) {
			continue
		}
		if len(pkt) > 0 {
			frames = append(frames, pkt)
		}
	}
	if len(frames) < 100 {
		t.Fatalf("the fixture yielded only %d Opus packets; it should hold seconds of 20 ms frames", len(frames))
	}
	return frames
}

// captureSink records what the pipeline produced.
type captureSink struct {
	mu    sync.Mutex
	audio [][]byte
	turns []capturedTurn
	// speaking records activity transitions, in order.
	speaking []bool
	done     chan struct{}
	once     sync.Once
}

type capturedTurn struct {
	roomID string
	who    forgemedia.Participant
	text   string
}

func newCaptureSink() *captureSink {
	return &captureSink{done: make(chan struct{})}
}

// Transcribe implements media.Transcriber, keeping the exact bytes it was given.
func (c *captureSink) Transcribe(_ context.Context, audio []byte, mimeType string) (*llm.Transcript, error) {
	c.mu.Lock()
	c.audio = append(c.audio, append([]byte(nil), audio...))
	c.mu.Unlock()
	if mimeType != "audio/ogg" {
		return nil, nil
	}
	return &llm.Transcript{Text: "transcribed", Model: "fake"}, nil
}

func (c *captureSink) VoiceTurn(_ context.Context, roomID string, who forgemedia.Participant, text string) error {
	c.mu.Lock()
	c.turns = append(c.turns, capturedTurn{roomID, who, text})
	c.mu.Unlock()
	c.once.Do(func() { close(c.done) })
	return nil
}

func (c *captureSink) SpeechActivity(_ context.Context, _, _ string, _ forgemedia.Participant, speaking bool) {
	c.mu.Lock()
	c.speaking = append(c.speaking, speaking)
	c.mu.Unlock()
}

func (c *captureSink) captured() ([][]byte, []capturedTurn, []bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.audio, c.turns, c.speaking
}

// transcribingConfig is the media config with a short silence gap, so a test
// does not wait the production 800 ms for every segment.
func transcribingConfig() config.MediaConfig {
	c := testConfig(5)
	c.Transcribe = true
	c.SilenceGap = 300 * time.Millisecond
	// Comfortably longer than the fixture. At 5 s the maximum-duration rule cut
	// this ~5 s utterance in half and the end-to-end test then transcribed only
	// the first piece — passing, while proving less than it claimed.
	c.MaxSegment = 30 * time.Second
	c.TranscribeWorkers = 2
	return c
}

// speakFixture pushes real encoded speech into the room as a live participant.
func speakFixture(t *testing.T, sfu *forgemedia.SFU, sig *signalRecorder, roomID string, frames [][]byte) *client {
	t.Helper()
	c := newClient(t, "stm_speaker", "usr_speaker")
	c.transcribe = true // this room IS being written down
	sig.on("stm_speaker", func(sdp string) { c.answerRenegotiation(sfu, roomID, sdp) })
	speakFixtureOn(t, c, sfu, roomID, frames)
	return c
}

// speakFixtureOn pushes speech through a client the caller already configured,
// so a test can choose the room's privacy setting before anybody joins.
func speakFixtureOn(t *testing.T, c *client, sfu *forgemedia.SFU, roomID string, frames [][]byte) {
	t.Helper()
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "mic", "usr_speaker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.pc.AddTrack(track); err != nil {
		t.Fatal(err)
	}
	if err := c.offer(sfu, roomID); err != nil {
		t.Fatalf("the speaker could not join: %v", err)
	}
	go func() {
		for _, frame := range frames {
			// 20 ms is the frame size the fixture was encoded at. Sent in real
			// time rather than as fast as possible, because the segmenter closes
			// on wall-clock silence and a burst would look like one instant.
			_ = track.WriteSample(media.Sample{Data: frame, Duration: 20 * time.Millisecond})
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

// Audio spoken in a room becomes an attributed turn.
//
// The transcriber is substituted here so the test is fast and free; what it
// receives is checked to be a real Ogg Opus container, and the test below sends
// those same bytes to the real provider.
func TestSpokenAudioBecomesAnAttributedTurn(t *testing.T) {
	cap := newCaptureSink()
	sfu, err := forgemedia.New(forgemedia.Options{
		Config: transcribingConfig(), Log: logx.Discard(), Clock: clock.System{},
		Transcriber: cap, Turns: cap, Activity: cap,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sfu.Close()
	sig := newSignalRecorder()
	sfu.SetSignaller(sig)
	const roomID = "rom_transcribe"

	speakFixture(t, sfu, sig, roomID, realOpusFrames(t, "../llm/testdata/engineering-utterance.ogg"))

	select {
	case <-cap.done:
	case <-time.After(60 * time.Second):
		audio, _, speaking := cap.captured()
		t.Fatalf("no turn was ever recorded: %d segment(s) transcribed, %d activity change(s)",
			len(audio), len(speaking))
	}

	audio, turns, speaking := cap.captured()
	if len(turns) == 0 {
		t.Fatal("no turn reached the sink")
	}
	// COL-01's rule survives the audio path: the turn names who said it.
	if turns[0].who.UserID != "usr_speaker" {
		t.Errorf("the turn is attributed to %q, want usr_speaker", turns[0].who.UserID)
	}
	if turns[0].who.Label == "" {
		t.Error("the turn carries no speaker label; an unattributed turn is what COL-01 refuses")
	}
	if turns[0].roomID != roomID {
		t.Errorf("the turn landed in room %q", turns[0].roomID)
	}

	// The container has to be a real one. A malformed Ogg would satisfy every
	// assertion above and transcribe to nothing in production.
	if len(audio) == 0 {
		t.Fatal("nothing was handed to the transcriber")
	}
	seg := audio[0]
	if !bytes.HasPrefix(seg, []byte("OggS")) {
		t.Fatalf("the segment is not an Ogg stream: first bytes %q", seg[:min(8, len(seg))])
	}
	if !bytes.Contains(seg[:min(200, len(seg))], []byte("OpusHead")) {
		t.Error("the segment carries no OpusHead; the provider would decode it to nothing")
	}
	if len(seg) < 1000 {
		t.Errorf("the segment is %d bytes; the fixture holds seconds of speech", len(seg))
	}

	// How the utterance was cut up, recorded because it is a real characteristic
	// rather than an accident: packet-gap segmentation splits on silence, so a
	// speaker who pauses mid-sentence produces two segments and two turns. The
	// production silence gap (800 ms) splits far less than this test's 300 ms.
	t.Logf("the utterance produced %d segment(s) and %d turn(s) at a %s silence gap",
		len(audio), len(turns), transcribingConfig().SilenceGap)

	// Somebody started speaking before they stopped.
	if len(speaking) < 2 || !speaking[0] {
		t.Errorf("speech activity was reported as %v; want a start then a stop", speaking)
	}

	// Kept for the end-to-end test below. The directory is created here rather
	// than committed: git cannot track an empty one, and the file itself is
	// generated output that must not be — so on a fresh clone this path does not
	// exist and the write would fail with a bare "no such file or directory".
	if err := os.MkdirAll(filepath.Dir(pipelineOutputPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pipelineOutputPath, seg, 0o600); err != nil {
		t.Fatal(err)
	}
}

// pipelineOutputPath is where the test above leaves the container it produced,
// so the test below can send the real thing rather than a re-encode.
const pipelineOutputPath = "testdata/pipeline-output.ogg"

// The whole path, end to end, against the real provider.
//
// # Why this is separate, and why it depends on the test above
//
// It transcribes the EXACT bytes the media pipeline produced from live RTP.
// Re-encoding the fixture here would test ffmpeg's Ogg writer instead of ours,
// which is the one thing that could be wrong.
//
// Skipped without FORGE_LLM_API_KEY — `make test-asr`.
func TestThePipelinesOwnContainerTranscribesCorrectly(t *testing.T) {
	if os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("FORGE_LLM_API_KEY is unset; run with `make test-asr`")
	}
	audio, err := os.ReadFile(pipelineOutputPath)
	if err != nil {
		t.Skipf("run TestSpokenAudioBecomesAnAttributedTurn first: %v", err)
	}

	cfg, _, err := config.Load(config.SectionNone)
	if err != nil {
		t.Fatal(err)
	}
	cfg.LLM.RequestTimeout = 60 * time.Second
	c := llm.NewOpenAICompatible(cfg.LLM, logx.Discard(), clock.System{})

	got, err := c.Transcribe(context.Background(), audio, "audio/ogg")
	if err != nil {
		t.Fatalf("the container this pipeline produced was refused by the provider: %v", err)
	}
	t.Logf("transcript of the pipeline's own output: %q", got.Text)

	if got.Text == "" {
		t.Fatal("the pipeline's container transcribed to nothing; it is well-formed enough to send and not to read")
	}
	// The same value the llm fence guards, arriving through the whole path this
	// time: RTP in, Ogg assembled here, transcript out.
	lower := strings.ToLower(got.Text)
	if !strings.Contains(lower, "two point five") {
		t.Errorf("the spoken decimal did not survive the full pipeline: %q", got.Text)
	}
	// The tail as well as the head. A container that loses its last pages
	// transcribes the opening of the sentence perfectly and stops, which reads
	// like a quiet speaker rather than like dropped audio.
	if !strings.Contains(lower, "zero point one") {
		t.Errorf("the end of the utterance did not survive the pipeline: %q", got.Text)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
