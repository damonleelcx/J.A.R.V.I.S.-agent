package media_test

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	forgemedia "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/media"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// FORGE's voice in a room (PRD AUD-01, AUD-05, AUD-07).
//
// The speaker is substituted so these are fast and free; what it produces is
// real PCM at the real rate, and the encoding it goes through is the production
// one. The end-to-end check against the actual provider lives in
// TestForgesVoiceIsIntelligible, gated on a key.

// fakeSpeaker emits a tone, so a receiver can tell audio from silence.
type fakeSpeaker struct {
	mu      sync.Mutex
	seconds float64
	started chan struct{}
	once    sync.Once
	// stopped records that the stream was cancelled rather than running out,
	// which is what an interruption must look like from the provider's side.
	stopped bool
}

func newFakeSpeaker(seconds float64) *fakeSpeaker {
	return &fakeSpeaker{seconds: seconds, started: make(chan struct{})}
}

func (f *fakeSpeaker) Speak(ctx context.Context, _ string, onPCM func([]byte) error) error {
	// 20 ms of 24 kHz PCM per chunk, delivered in real time — the shape the real
	// provider streams in, so an interruption has somewhere to land.
	const chunkSamples = llm.SpeechSampleRate / 50
	chunks := int(f.seconds * 50)
	phase := 0.0
	for i := 0; i < chunks; i++ {
		select {
		case <-ctx.Done():
			f.mu.Lock()
			f.stopped = true
			f.mu.Unlock()
			return nil
		default:
		}
		buf := make([]byte, chunkSamples*2)
		for j := 0; j < chunkSamples; j++ {
			phase += 2 * math.Pi * 440 / llm.SpeechSampleRate
			binary.LittleEndian.PutUint16(buf[j*2:], uint16(int16(math.Sin(phase)*8000)))
		}
		if err := onPCM(buf); err != nil {
			f.mu.Lock()
			f.stopped = true
			f.mu.Unlock()
			return nil
		}
		f.once.Do(func() { close(f.started) })
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func (f *fakeSpeaker) interrupted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func speakingSFU(t *testing.T, sp forgemedia.Speaker) (*forgemedia.SFU, *signalRecorder) {
	t.Helper()
	sfu, err := forgemedia.New(forgemedia.Options{
		Config: testConfig(5), Log: logx.Discard(), Clock: clock.System{}, Speaker: sp,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sfu.Close)
	sig := newSignalRecorder()
	sfu.SetSignaller(sig)
	return sfu, sig
}

// FORGE speaks and a participant hears it, on a track that names FORGE.
//
// The track identity matters as much as the audio: AUD-05 requires FORGE to
// identify itself as AI, and the room record already keeps it as a distinct
// speaker kind rather than a null one. A client labelling speakers from the
// transport must not be able to mistake it for a person.
func TestForgeSpeaksAndIsHeard(t *testing.T) {
	speaker := newFakeSpeaker(3)
	sfu, sig := speakingSFU(t, speaker)
	const roomID = "rom_voice"

	alice := newClient(t, "stm_alice", "usr_alice")
	sig.on("stm_alice", func(sdp string) { alice.answerRenegotiation(sfu, roomID, sdp) })
	alice.speak()
	if err := alice.offer(sfu, roomID); err != nil {
		t.Fatal(err)
	}

	go func() { _ = sfu.Say(context.Background(), roomID, "Setting the wall thickness.") }()

	deadline := time.Now().Add(30 * time.Second)
	for alice.packetsFrom("stm_forge") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("FORGE spoke and nobody heard it")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := alice.labelOf("stm_forge"); got != "forge" {
		t.Errorf("FORGE's track is labelled %q; a client could take it for a person", got)
	}
}

// A participant speaking stops FORGE mid-sentence (AUD-01).
func TestSomebodySpeakingInterruptsForge(t *testing.T) {
	speaker := newFakeSpeaker(20) // long enough that it cannot simply finish
	sfu, _ := speakingSFU(t, speaker)
	const roomID = "rom_bargein"

	alice := newClient(t, "stm_alice", "usr_alice")
	alice.speak()
	if err := alice.offer(sfu, roomID); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() { _ = sfu.Say(context.Background(), roomID, "A long explanation."); close(done) }()

	select {
	case <-speaker.started:
	case <-time.After(30 * time.Second):
		t.Fatal("FORGE never started speaking")
	}
	if !sfu.Speaking(roomID) {
		t.Fatal("FORGE is not marked as speaking, so an interruption proves nothing")
	}

	// The barge-in itself. Delivered through the same call the packet path makes,
	// because a test that reached inside would not prove the wiring.
	sfu.Silence(context.Background(), roomID)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("FORGE went on talking after being interrupted")
	}
	if sfu.Speaking(roomID) {
		t.Error("FORGE still reports itself as speaking")
	}
	if !speaker.interrupted() {
		t.Error("the provider stream was not cancelled; an interrupted sentence is still being paid for")
	}
}

// Interrupting when nothing is being said is harmless.
//
// AUD-07 puts stop-speaking on screen at all times, so it is pressed when there
// is nothing to stop more often than when there is.
func TestSilencingAQuietRoomIsHarmless(t *testing.T) {
	sfu, _ := speakingSFU(t, newFakeSpeaker(1))
	sfu.Silence(context.Background(), "rom_quiet")
	if sfu.Speaking("rom_quiet") {
		t.Error("a room nobody is in reports FORGE as speaking")
	}
}

// A deployment with no speech configured is not broken.
func TestARoomWithNoVoiceStillWorks(t *testing.T) {
	sfu, _ := speakingSFU(t, nil)
	if err := sfu.Say(context.Background(), "rom_novoice", "anything"); err != nil {
		t.Fatalf("speaking with no speaker configured returned %v; it should be a no-op", err)
	}
	if sfu.Speaking("rom_novoice") {
		t.Error("a deployment with no voice reports FORGE as speaking")
	}
}

// The µ-law encoder is never less accurate than an independent implementation.
//
// # Why this is checked against ffmpeg, and why NOT byte for byte
//
// The encoder is bit manipulation written from a spec — exactly the kind of code
// that is self-consistently wrong. A test round-tripping it through my own
// decoder would agree with any such mistake, including an inverted sign or an
// off-by-one exponent, both of which produce audio that is quiet and wrong
// rather than obviously broken. So it is checked against ffmpeg's `pcm_mulaw`,
// which shares no code with it.
//
// Byte equality was the first attempt and is the WRONG bar. ffmpeg quantises to
// 14 bits before its table lookup, because G.711 is specified on 14-bit values;
// this encoder works on the full 16. The two therefore disagree by one code on
// the midpoints between levels, which is a difference of convention and is
// inaudible. Demanding byte equality would mean adopting ffmpeg's bit depth to
// satisfy a test rather than because it is better.
//
// What is asserted instead is the property that actually matters and that no
// plausible bug survives: **every sample encodes at least as accurately as the
// reference does.** A sign inversion, a wrong exponent or an un-inverted output
// all fail it immediately and by a wide margin.
func TestMuLawIsNeverWorseThanAReferenceEncoder(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed; this fence needs an independent encoder")
	}
	dir := t.TempDir()

	// A sweep rather than a tone: it walks the whole range, so every µ-law
	// exponent band and both clipping points are exercised.
	const samples = 24000
	pcm := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		v := int16(math.Sin(2*math.Pi*float64(i)/400) * float64(i) / samples * 32767)
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
	}
	raw := filepath.Join(dir, "in.pcm")
	if err := os.WriteFile(raw, pcm, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "ref.ul")
	if err := exec.Command("ffmpeg", "-loglevel", "error", "-y",
		"-f", "s16le", "-ar", "8000", "-ac", "1", "-i", raw,
		"-f", "mulaw", out).Run(); err != nil {
		t.Skipf("ffmpeg could not produce a reference: %v", err)
	}
	reference, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(reference) != samples {
		t.Fatalf("the reference has %d bytes for %d samples", len(reference), samples)
	}

	var worse, identical int
	for i := 0; i < samples; i++ {
		sample := int(int16(binary.LittleEndian.Uint16(pcm[i*2:])))
		mine := forgemedia.EncodeULaw(int16(sample))
		if mine == reference[i] {
			identical++
			continue
		}
		mineErr := abs(forgemedia.DecodeULaw(mine) - sample)
		refErr := abs(forgemedia.DecodeULaw(reference[i]) - sample)
		if mineErr > refErr {
			if worse < 3 {
				t.Errorf("sample %d (%d): encoded 0x%02x decoding to %d (error %d); "+
					"the reference encoded 0x%02x decoding to %d (error %d)",
					i, sample, mine, forgemedia.DecodeULaw(mine), mineErr,
					reference[i], forgemedia.DecodeULaw(reference[i]), refErr)
			}
			worse++
		}
	}
	if worse > 0 {
		t.Fatalf("%d of %d samples encode less accurately than the reference", worse, samples)
	}
	// A sanity floor: the two must mostly AGREE, or "never worse" could be
	// satisfied by an encoder that is merely different in some benign-looking way.
	if identical*100/samples < 90 {
		t.Errorf("only %d%% of samples match the reference exactly; the two implementations "+
			"should differ only on midpoints", identical*100/samples)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// The canonical points, where a sign or inversion error shows up immediately.
func TestMuLawEncodesTheCanonicalValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sample int16
	}{
		{"silence", 0},
		{"loudest positive", 32767},
		{"loudest negative", -32768},
		{"quiet positive", 100},
		{"quiet negative", -100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code := forgemedia.EncodeULaw(tc.sample)
			back := forgemedia.DecodeULaw(code)
			// Sign must survive. An inverted encoder round-trips to roughly the
			// right magnitude with the wrong sign, which sounds like noise rather
			// than like silence.
			if tc.sample > 0 && back < 0 || tc.sample < 0 && back > 0 {
				t.Fatalf("%d encoded to 0x%02x which decodes to %d; the sign is inverted",
					tc.sample, code, back)
			}
			// µ-law's step is coarse at the top and fine at the bottom; a tenth of
			// the magnitude is well inside it everywhere and far tighter than any
			// real bug.
			tolerance := abs(int(tc.sample))/10 + 8
			if abs(back-int(tc.sample)) > tolerance {
				t.Fatalf("%d round-tripped to %d, outside the %d tolerance",
					tc.sample, back, tolerance)
			}
		})
	}
}

// The two halves of the pipeline agree about the sample rate.
//
// The model client synthesises at one rate and the media plane resamples from
// another; they are separate constants in separate packages because the media
// plane must not depend on the model client for a number. If they ever disagree,
// FORGE speaks at the wrong pitch — audible, and easy to mistake for a badly
// chosen voice rather than a bug.
func TestTheSpeechSampleRatesAgree(t *testing.T) {
	if forgemedia.TTSSampleRate != llm.SpeechSampleRate {
		t.Fatalf("the media plane resamples from %d Hz and the provider synthesises at %d Hz; "+
			"FORGE would speak at the wrong pitch", forgemedia.TTSSampleRate, llm.SpeechSampleRate)
	}
}

// What FORGE says is what comes out of the room.
//
// # Why this needs the real provider and cannot be faked
//
// Every other test here uses a substituted speaker, so all of them would pass on
// a build that sent a malformed request, asked for the wrong sample rate, or
// encoded to something a browser will not decode. This one runs the whole path —
// the real speech model, the real resampling, the real G.711 — and then asks the
// transcriber what it heard.
//
// It is also the check on the decision this wave rests on. G.711 was chosen over
// cgo-Opus on the grounds that telephone quality loses timbre and not numbers.
// That claim is asserted here, on FORGE's own voice, rather than left in a
// design note.
//
// Skipped without FORGE_LLM_API_KEY — `make test-asr`.
func TestForgesVoiceIsIntelligible(t *testing.T) {
	if os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("FORGE_LLM_API_KEY is unset; run with `make test-asr`")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed; needed to package the audio for transcription")
	}
	cfg, _, err := config.Load(config.SectionNone)
	if err != nil {
		t.Fatal(err)
	}
	cfg.LLM.RequestTimeout = 90 * time.Second
	client := llm.NewOpenAICompatible(cfg.LLM, logx.Discard(), clock.System{})

	const sentence = "Set the wall thickness to two point five millimetres."

	// The production transform, called the way the room calls it.
	var ulaw []byte
	var pending []byte
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := client.Speak(ctx, sentence, func(pcm []byte) error {
		pending = append(pending, pcm...)
		frames, rest := forgemedia.FramesFrom(pending)
		pending = rest
		for _, f := range frames {
			ulaw = append(ulaw, f...)
		}
		return nil
	}); err != nil {
		t.Fatalf("FORGE could not speak: %v", err)
	}
	if len(ulaw) < 8000 {
		t.Fatalf("only %d G.711 bytes were produced — under a second of speech for a "+
			"sentence that takes several", len(ulaw))
	}

	// Decoded back to PCM, which is what a listener's browser does, then packaged
	// for the transcriber.
	dir := t.TempDir()
	pcm := make([]byte, len(ulaw)*2)
	for i, code := range ulaw {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(forgemedia.DecodeULaw(code))))
	}
	raw := filepath.Join(dir, "heard.pcm")
	if err := os.WriteFile(raw, pcm, 0o600); err != nil {
		t.Fatal(err)
	}
	ogg := filepath.Join(dir, "heard.ogg")
	if err := exec.Command("ffmpeg", "-loglevel", "error", "-y",
		"-f", "s16le", "-ar", "8000", "-ac", "1", "-i", raw,
		"-c:a", "libopus", "-b:a", "16k", ogg).Run(); err != nil {
		t.Skipf("ffmpeg could not package the audio: %v", err)
	}
	audio, err := os.ReadFile(ogg)
	if err != nil {
		t.Fatal(err)
	}

	heard, err := client.Transcribe(context.Background(), audio, "audio/ogg")
	if err != nil {
		t.Fatalf("what FORGE said could not be transcribed: %v", err)
	}
	t.Logf("FORGE was asked to say: %q", sentence)
	t.Logf("what came out of the room:  %q", heard.Text)

	lower := strings.ToLower(heard.Text)
	if lower == "" {
		t.Fatal("FORGE's voice transcribed to nothing; the audio is being produced and is not intelligible")
	}
	// The decimal, through FORGE's own voice this time. Telephone quality loses
	// timbre, not numbers — that is the whole basis for choosing G.711 over cgo.
	if !strings.Contains(lower, "two point five") {
		t.Errorf("the decimal did not survive FORGE's own voice: %q", heard.Text)
	}
	for _, word := range []string{"wall thickness", "millimet"} {
		if !strings.Contains(lower, word) {
			t.Errorf("%q is missing from what came out: %q", word, heard.Text)
		}
	}
}
