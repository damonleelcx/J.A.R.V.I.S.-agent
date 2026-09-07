package httpapi

import (
	"context"
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
// # Why this asks for MP3 when the media plane takes PCM
//
// It first shipped wrapping the media plane's PCM in a WAV header — no second
// encoder, no decoder, nothing that could disagree about a sample rate. Elegant,
// and wrong twice.
//
// Wrong on size: half a megabyte of WAV per utterance, against roughly thirty
// kilobytes of MP3 for the same seconds, on a path that fires on every reply.
//
// Wrong on support: a WAV is PCM, and PCM is the codec a browser is LEAST likely
// to have. canPlayType answers "probably" for audio/mpeg everywhere and only
// "maybe" for audio/wav, and some engines refuse WAV outright — which is exactly
// what happened. The endpoint returned a byte-perfect 500KB WAV, the browser
// answered NotSupportedError, and voice.js fell back to the browser's own voice.
// Server-side every signal said success: 200, forge.tts.spoke, half a megabyte
// delivered. Nothing was wrong except that nobody could play it.
//
// Fish emits both, so neither format is converted here. The endpoint asks for
// what its consumer can actually play — which is the decision the WAV wrapper
// was avoiding having to make.

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

	// A vendor that can hand a browser a container it will actually play is used
	// that way. One that cannot is not degraded around silently — the caller is
	// told, and falls back to the browser's own voice.
	mp3, ok := h.deps.Speaker.(interface {
		SpeakMP3(context.Context, string) ([]byte, string, error)
	})
	if !ok {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeConnectorUnavailable).
			WithDetail("the configured speech vendor cannot produce audio this browser can "+
				"play; the browser's own voice is used instead"))
		return
	}

	out, ctype, err := mp3.SpeakMP3(r.Context(), req.Text)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	h.deps.Log.Info(r.Context(), logx.EventTTSSpoke,
		"provider", h.deps.Speaker.Name(), "chars", len([]rune(req.Text)),
		"bytes", len(out), "content_type", ctype)

	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
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
