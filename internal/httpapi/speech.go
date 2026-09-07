package httpapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tts"
)

// FORGE's own voice on the workbench (PRD AUD-05).
//
// # Why this exists when the browser can already speak
//
// voice.js reads answers aloud with the browser's speechSynthesis, and that is
// a real path that must keep working: it needs no vendor, no key and no network,
// and it is what a deployment with no speech vendor has. But it is the
// BROWSER'S voice — macOS reads FORGE in Samantha, Windows in another — so the
// character sounds like whatever machine she is being read on, and different on
// every one.
//
// The media plane already solved this for rooms, through media.Speaker. It could
// not solve it for the workbench, because the workbench has no WebRTC: it is a
// page with an <audio> element. So this endpoint hands the same provider's
// output to that element.
//
// # Why WAV, and why that is not a second format
//
// The provider streams 16-bit PCM at llm.SpeechSampleRate because that is what
// the media plane consumes. A browser will not play raw PCM, but WAV is PCM
// with a 44-byte header — so this wraps rather than converts. There is no
// second encoder, no decoder, and no place where a sample rate could disagree
// with the media plane's.
//
// The utterance is buffered before the header is written, because a WAV header
// carries the length. That is affordable here and only here: an utterance is a
// few seconds and a few hundred KB, and unlike the room path nobody is holding
// a conversation against it in real time.

type speechRequest struct {
	Text string `json:"text"`
}

// Speak handles POST /v1/speech.
func (h *ConverseHandlers) Speak(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.Speak"

	if h.deps.Speaker == nil {
		// Refused, not degraded. The caller falls back to the browser's own
		// voice, and it must be able to tell "this deployment has no vendor"
		// from "the vendor failed" — the first is a configuration a person
		// chose, the second is a fault.
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeConnectorUnavailable).
			WithDetail("this deployment has no speech vendor configured, so FORGE has no voice "+
				"of her own. Set FORGE_TTS_PROVIDER to give her one; without it the browser "+
				"reads answers aloud in its own voice, which still works"))
		return
	}

	var req speechRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if len(req.Text) == 0 {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeValidationFailed).
			WithDetail("there is nothing to speak"))
		return
	}

	var pcm bytes.Buffer
	err := h.deps.Speaker.Speak(r.Context(), req.Text, func(chunk []byte) error {
		pcm.Write(chunk)
		return nil
	})
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if pcm.Len() == 0 {
		// Silence is exactly what a broken voice sounds like, so it is reported
		// rather than served as valid audio nobody can hear.
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeExternalUnavailable).
			WithDetail("the speech vendor returned no audio"))
		return
	}

	out := wavFromPCM(pcm.Bytes(), llm.SpeechSampleRate)
	h.deps.Log.Info(r.Context(), logx.EventTTSSpoke,
		"provider", h.deps.Speaker.Name(), "chars", len([]rune(req.Text)), "bytes", len(out))

	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// wavFromPCM prepends a canonical 44-byte WAV header to 16-bit mono PCM.
//
// Written out rather than pulled from a library: it is 44 bytes of a format that
// has not changed since 1991, and a dependency for it would be a supply chain
// for something this file can state completely.
func wavFromPCM(pcm []byte, sampleRate int) []byte {
	const (
		channels      = 1
		bitsPerSample = 16
	)
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	var b bytes.Buffer
	b.Grow(44 + len(pcm))
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16)) // PCM chunk size
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))  // PCM, uncompressed
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&b, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bitsPerSample))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// SpeakerFor builds the deployment's voice, or nil when it has none.
//
// Called once at wiring and put on Deps, so the workbench endpoint and the media
// plane share one instance and one decision. A deployment cannot end up with
// FORGE sounding like one thing in a room and another at the workbench, which
// is the exact failure this seam exists to prevent.
//
// A vendor that is named but cannot be built is logged loudly and returns nil:
// the workbench then falls back to the browser's voice and says so. It does not
// stop the process, because every non-speech path still works.
func SpeakerFor(ctx context.Context, cfg *config.Config, log *logx.Logger) tts.Speaker {
	if cfg == nil || !cfg.TTS.Configured() {
		return nil
	}
	fish, err := tts.NewFish(cfg.TTS.APIURL, cfg.TTS.APIKey,
		cfg.TTS.VoiceID, cfg.TTS.Model, llm.SpeechSampleRate)
	if err != nil {
		log.ErrorWith(ctx, logx.EventMediaRefused, err,
			"reason", "a speech vendor was configured but could not be built; FORGE has no voice of her own")
		return nil
	}
	return fish
}
