package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Whether the transcription endpoint actually serves the transcription model.
//
// # The problem this solves
//
// GET /v1/meta/models told the workbench `"server": true` whenever
// FORGE_LLM_TRANSCRIBER_MODEL named a model — and it always does, because it has
// a default. So the page recorded the first hold, uploaded it, and only then
// learnt from a 501 that the endpoint had never served that model. Measured in
// production on 2026-09-17: token-plan.cn-beijing.maas.aliyuncs.com answered
// `404 Model not exist.` for qwen3-asr-flash-2026-02-10, and serves no speech to
// text model at all. The person's first sentence was lost, and the page said
// "transcribed by FORGE" right up to the moment it was not.
//
// "Configured" is a fact about this process; "served" is a fact about the
// provider. Only the second tells a person whether pressing the button works.
//
// # Why cached, and why it is never asked on a request
//
// The question is one GET /models against the transcription endpoint — the same
// request the 404 path already makes to name what IS served. It is asked once
// at startup in the background, and afterwards only when the answer has gone
// stale, again in the background. A request for /v1/meta/models reads the cached
// answer; the one exception is a request that arrives before the very first
// answer, which waits a bounded moment for it (see TranscriberAvailability).
//
// # Why the answer expires
//
// Providers add models as well as retire them. A "not served" that lasted for
// the life of the pod would keep the microphone off after the owner's provider
// started serving the model, until somebody restarted FORGE for no visible
// reason. So every answer has a lifetime, the negative one short.
//
// # What counts as "not served"
//
// ONLY a model list that was read and does not contain the model. A list that
// could not be read — the provider is unreachable, or does not implement
// /models — proves nothing either way: transcription stays offered, marked
// unverified, and an upload that fails names its own failure as before. Turning
// voice off because a list endpoint hiccuped at boot would be this bug inverted.
//
// ‼️ The check must never be able to stop forged starting. It runs in a
// goroutine with its own timeout, and nothing waits on it but the first page.

// TranscriberAvailability is what the deployment can honestly say about speech to
// text right now.
type TranscriberAvailability struct {
	// Model is the configured transcription model, "" when there is none.
	Model string
	// Served is whether transcription should be offered.
	Served bool
	// Verified is true when Served comes from the endpoint's own model list.
	// Served && !Verified means the list could not be read.
	Verified bool
	// Reason says, in one plain sentence an owner can act on, why server
	// transcription is off or unverified. "" when it is served and verified.
	Reason string
}

// How long an answer is trusted.
//
// A served model is re-checked rarely: if it is retired, the first upload's 404
// says so by name AND marks the answer stale (see forgetTranscriberAnswer). A
// model that is not served is re-checked every few minutes so a provider that
// starts serving it turns the microphone on without a restart. An unreadable
// list is retried soonest, because it is the answer most likely to be a blip.
const (
	servedTTL     = 30 * time.Minute
	notServedTTL  = 5 * time.Minute
	unverifiedTTL = time.Minute
)

// transcriberCheck is the cached answer and the one check that may be running.
type transcriberCheck struct {
	mu      sync.Mutex
	answer  TranscriberAvailability
	at      time.Time
	known   bool
	running bool
	// first is closed when the first answer arrives, so the first page can wait
	// for it without polling.
	first chan struct{}
}

// CheckTranscriber asks the transcription endpoint whether it serves the
// transcription model, and caches the answer. Blocking; forged calls it in a
// goroutine at startup.
func (c *OpenAICompatible) CheckTranscriber(ctx context.Context) TranscriberAvailability {
	chk := c.sttCheck()
	chk.mu.Lock()
	if chk.running {
		chk.mu.Unlock()
		return c.waitTranscriberAnswer(ctx, modelListTimeout)
	}
	chk.running = true
	chk.mu.Unlock()
	return c.runTranscriberCheck(ctx)
}

// TranscriberAvailability returns the cached answer without making a request.
//
// A stale answer is returned as it is and refreshed in the background, so the
// next page gets the new one. Before the first answer exists it starts the check
// if nobody has, and waits at most `wait` for it; if that passes, transcription
// is reported as offered but unverified — the same as an unreadable list.
func (c *OpenAICompatible) TranscriberAvailability(ctx context.Context, wait time.Duration) TranscriberAvailability {
	model := c.models[RoleTranscriber]
	if model == "" {
		return TranscriberAvailability{Reason: "no transcription model is configured: set FORGE_LLM_TRANSCRIBER_MODEL"}
	}
	chk := c.sttCheck()
	chk.mu.Lock()
	known, answer, at := chk.known, chk.answer, chk.at
	start := !chk.running && (!known || c.clock.Now().Sub(at) >= ttlOf(answer))
	if start {
		chk.running = true
	}
	chk.mu.Unlock()

	if start {
		// context.Background: the check belongs to the deployment, not to the
		// request that happened to notice it was due, and must finish if that
		// request goes away.
		go c.runTranscriberCheck(context.Background())
	}
	if known {
		return answer
	}
	return c.waitTranscriberAnswer(ctx, wait)
}

// forgetTranscriberAnswer marks the cached answer stale, so the next page asks
// again. Called when an upload was refused with 404: the list said the model
// was served and the endpoint has just said otherwise.
func (c *OpenAICompatible) forgetTranscriberAnswer() {
	chk := c.sttCheck()
	chk.mu.Lock()
	chk.at = time.Time{}
	chk.mu.Unlock()
}

func (c *OpenAICompatible) waitTranscriberAnswer(ctx context.Context, wait time.Duration) TranscriberAvailability {
	chk := c.sttCheck()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-chk.first:
		chk.mu.Lock()
		defer chk.mu.Unlock()
		return chk.answer
	case <-timer.C:
	case <-ctx.Done():
	}
	return TranscriberAvailability{Model: c.models[RoleTranscriber], Served: true,
		Reason: "whether the transcription endpoint serves " + c.models[RoleTranscriber] +
			" is still being checked; transcription is offered, and a failed upload will say why"}
}

// runTranscriberCheck makes the request and stores the answer. The caller must
// have set running.
func (c *OpenAICompatible) runTranscriberCheck(ctx context.Context) TranscriberAvailability {
	model := c.models[RoleTranscriber]
	answer := TranscriberAvailability{Model: model}
	var served []string
	var err error
	if model != "" {
		served, err = c.servedModels(ctx, c.transcriberURL, c.transcriberKey)
	}
	// ‼️ Reason goes to an UNAUTHENTICATED page (GET /v1/meta/models is public),
	// so it names settings and models and never the endpoint's host, which can
	// carry a workspace id. The host, the error and the served list go to the log.
	detail := ""
	switch {
	case model == "":
		answer.Reason = "no transcription model is configured: set FORGE_LLM_TRANSCRIBER_MODEL"
	case err != nil:
		answer.Served = true
		answer.Reason = "the transcription endpoint's model list could not be read, so whether it serves " + model +
			" is unverified; transcription is offered, and a failed upload will say why"
		detail = fmt.Sprintf("GET %s/models failed: %v", c.transcriberURL, err)
	case servesModel(served, model):
		answer.Served, answer.Verified = true, true
	default:
		answer.Verified = true
		answer.Reason = "server transcription is off: the transcription endpoint does not serve " + model +
			", so a recording cannot be transcribed here. Set FORGE_LLM_TRANSCRIBER_BASE_URL and " +
			"FORGE_LLM_TRANSCRIBER_API_KEY to an endpoint that serves speech to text (DashScope's " +
			"compatible-mode endpoint serves qwen3-asr-flash), or FORGE_LLM_TRANSCRIBER_MODEL to a " +
			"speech-to-text model this endpoint serves"
		detail = fmt.Sprintf("%s serves: %s", c.transcriberURL, strings.Join(served, ", "))
		if len(served) == 0 {
			detail = c.transcriberURL + " lists no models at all, which usually means the key is scoped to a " +
				"different product or region than the host"
		}
	}

	chk := c.sttCheck()
	chk.mu.Lock()
	chk.answer, chk.at, chk.running = answer, c.clock.Now(), false
	if !chk.known {
		chk.known = true
		close(chk.first)
	}
	chk.mu.Unlock()

	if c.log != nil {
		c.log.Info(ctx, logx.EventASRChecked, "model", model, "served", answer.Served,
			"verified", answer.Verified, "reason", answer.Reason, "detail", detail)
	}
	return answer
}

func ttlOf(a TranscriberAvailability) time.Duration {
	switch {
	case !a.Verified:
		return unverifiedTTL
	case a.Served:
		return servedTTL
	default:
		return notServedTTL
	}
}

func servesModel(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// sttCheck returns the client's cache, creating it on first use so that every
// constructor — and a zero-valued client in a test — has one.
func (c *OpenAICompatible) sttCheck() *transcriberCheck {
	c.sttOnce.Do(func() { c.stt = &transcriberCheck{first: make(chan struct{})} })
	return c.stt
}
