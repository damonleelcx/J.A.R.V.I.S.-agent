# The workbench microphone sent nothing

**Found:** 2026-09-15, reported from production by the deployment's owner:
*"this audio button is not taking my audio input to send to the agent"* — the
round mic button beside "…or type it".
**Severity:** high for voice. On the owner's network, push-to-talk could not
work at all, and the page said nothing about why.
**Owner:** the workbench voice layer (`internal/httpapi/assets/voice.js`,
`workbench.js`). Secondary: `internal/llm/transcribe.go`, whose errors hid the
one sentence that fixes the deployment.

## Summary

Four independent causes, each silent:

1. Push-to-talk used **only** the browser's `SpeechRecognition`. Chrome and
   Edge run it on Google's servers, which cannot be reached from mainland China,
   where the owner is. FORGE's own server already had a transcriber, used by
   rooms, and the workbench never called it.
2. A transcript that arrived while a turn was in flight hit `send()`'s
   `if (state.busy) return` and was **dropped**.
3. The hold ended on **`mouseleave`**, and a quick second press threw
   `InvalidStateError` inside a `try/catch` that swallowed it.
4. `no-speech`, `aborted` and the swallowed start error **showed nothing**;
   `network` was shown as the bare code.

The fix records while the button is held and transcribes on the server, keeps
anything said during a turn in the text box, holds with pointer capture, and
puts every failure into `#voice-note`.

**And one finding that the fix alone does not cure:** the production model
endpoint serves **no speech-to-text model**. The server path is built, fenced
and reports this by name — but push-to-talk will not produce text on that
deployment until `FORGE_LLM_TRANSCRIBER_MODEL` names a model the endpoint
serves. See *Not in this fix*.

## Symptom

The screenshot: *Thinking…*, *first token 33020ms*, hands-free off, speak
replies on, model qwen3.7-plus. Holding the mic and speaking produced no turn
and no message. The production endpoint is
`https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1`.

## Impact

- **Push-to-talk, in mainland China (and any network that blocks Google):**
  nothing, silently. The recogniser either produced no result or fired
  `onerror` with `network`, shown as `Speech recognition error: network`.
- **Push-to-talk, anywhere, during a turn:** whatever was said while FORGE was
  thinking or speaking was discarded. With a first token 33 seconds away, that
  is most of the time spent waiting.
- **Typing during a turn:** the same drop, one step earlier — the submit handler
  cleared the box and then called `send()`, which returned. The message was gone.
- **Every browser:** a click did nothing; drifting off the 36px circle ended
  the hold mid-sentence; a quick re-press did nothing.

## Preconditions

- Cause 1: a browser whose `SpeechRecognition` needs a service that cannot be
  reached — Chrome or Edge on a network that blocks Google.
- Cause 2: speaking (or pressing send) while `state.busy`.
- Cause 3: a mouse, a hold longer than a steady hand keeps still, or two presses
  closer together than the recogniser takes to end a session.

## Root cause

**1. The wrong recogniser for the network.** `voice.js` said it used the
browser's speech stack because it "keeps audio on the device". In Chrome it does
not: the audio goes to Google. `startListening` called `rec.start()` and nothing
else; there was no other way in. Meanwhile `internal/llm/transcribe.go` already
transcribed room audio on the server, through the deployment's own endpoint.

**2. `send()` guards against a second turn by returning.** `onTranscript` called
`send(text)` directly, so a transcript during a turn met
`if (state.busy || !text.trim()) return;`. The submit handler did
`input.value = ''; send(text);` — clearing first.

**3. The hold.** `workbench.js` bound `mousedown`/`touchstart` → start and
`mouseup`/`mouseleave`/`touchend` → stop. `mouseleave` fires the moment the
cursor leaves the button. And `startListening` was
`try { this.rec.start(); … } catch (e) { /* already running */ }`: Chrome throws
`InvalidStateError` until the previous session's `onend`, so a second press
inside that window was dropped without a trace.

**4. The errors.** `rec.onerror` returned on `no-speech` and `aborted` — right
for hands-free, where they fire on every pause, and wrong for a hold, where
`no-speech` is the only sign the microphone heard nothing.

**The endpoint (measured 2026-09-15, with the production key, each request a
fraction of a cent):**

| model | request | answer |
|---|---|---|
| `qwen3-asr-flash-2026-02-10` (the default) | Ogg, WebM, MP4 | **404** `Model not exist.` |
| `qwen3-asr-flash` | Ogg | **404** |
| `qwen3.7-plus`, `qwen3.8-flash`, `qwen3.6-flash`, `qwen3.8-max` | Ogg, WebM, MP4 | **400** `An incorrect modal audio was entered` |
| `qwen-audio-3.0-realtime-plus` | Ogg, WebM, MP4 | **200** `{"status_message":"Success."}` — no choices, no text |

The endpoint lists: deepseek-v4-flash-0731, deepseek-v4-pro, deepseek-v4.1-flash,
glm-5.2, qwen-audio-3.0-realtime-plus, qwen-audio-3.0-tts-plus, qwen3.6-flash,
qwen3.7-max, qwen3.7-plus, qwen3.8-flash, qwen3.8-max, wan2.7-image,
wan2.7-image-pro. None transcribes through chat completions.

Two things in `llm.Transcribe` would have hidden that from the person at the
microphone: the 404 was `EXTERNAL_UNAVAILABLE` (a 503, whose detail — the list
above and the setting to change — is withheld from HTTP responses), and the
realtime model's 200-with-no-choices came back as an empty transcript,
indistinguishable from silence.

## Why it did not show up before

- Voice was developed and tested where Google is reachable, where cause 1 does
  not exist.
- Nothing ran voice.js. `TestHandsFreeEchoGuardsArePresent` and the two node
  scripts check the echo guard and the speech fallback; no test pressed the
  button, released it, pressed again, or spoke during a turn.
- Each piece was individually reasonable: returning from `send()` while busy,
  ending a hold when the pointer leaves, ignoring `no-speech`. The failure is in
  the sequence.
- The server transcriber's only caller was the media plane, which reads every
  error the same way and every empty answer as a quiet room — so a retired model
  looked like nobody talking.

## Fix

**Server — `POST /v1/transcribe`** (`internal/httpapi/transcribe.go`).
Session-authenticated like `/v1/speech`. The body is the recording as recorded;
the content type is the container. `audio/webm`, `audio/ogg`, `audio/mp4`,
`audio/mpeg`, `audio/wav` are accepted, codec parameters dropped before the data
URI. Nothing is transcoded. Refused by name: no transcriber (501, naming
`FORGE_LLM_TRANSCRIBER_MODEL`), other containers (415, listing the accepted
ones), empty (400), over 2 MiB (413, naming the 60-second hold). Returns
`{text, model}`. Logs `forge.asr.transcribed` / `forge.asr.failed` with
`surface=workbench`. Not rate limited by address: `ClientIP` reads the
connection, so behind the ingress one per-IP ceiling would be the whole
deployment's.

**`llm.Transcribe`.** A 404 is now `CONNECTOR_UNAVAILABLE`, so its detail (the
served models and the setting) reaches the page. A 200 with no choices sets
`Transcript.Unanswered`; the media plane still reads it as an empty segment, and
the workbench refuses to call it silence.

**`/v1/meta/models`** now carries `transcription: {server, model}`.

**`voice.js`.**
- Push-to-talk records with `MediaRecorder` (opened through
  `ForgeAudioInput.open`, so the same constraints as rooms; Opus in WebM, or MP4
  on Safari; 32 kbit/s; stopped and said at 60 seconds), uploads on release,
  reports `transcribing`, and hands the text on. Uploads are delivered in the
  order spoken.
- The path is chosen and named: `server` when the deployment transcribes,
  `browser` otherwise, `none` — with the reason, and the mic disabled — when
  neither can work. A 501 drops the server path for the page; a `network` error
  drops the browser path. Hands-free stays on the browser's recogniser, for its
  interim results, and is disabled with the reason where that cannot work.
- A press while the recogniser's last session is still closing is remembered
  and started in `onend`; any other start error is said.
- Every failure is a sentence in `#voice-note`: permission refused, no
  microphone, microphone in use, nothing captured, recording failed, released
  before the microphone opened, network, provider error (with request id), no
  transcriber here, no words recognised, `no-speech` during a hold, the
  recogniser unable to reach Google.
- `ForgeVoice.makeHold` / `bindHold`: pointer events with `setPointerCapture`;
  release on `pointerup`, `pointercancel` or `lostpointercapture`; a press
  shorter than 350 ms is discarded with *"Hold to talk…"*. The space bar shares
  the same hold.
- `ForgeVoice.deliverSpoken`: while a turn is in flight a transcript goes into
  the text box — appended after anything typed, with a note — instead of
  `send()`.

**Why the text box and not a queue.** A queued utterance is sent later against a
reply the person had not heard when they spoke, and they cannot see or change it
first. The text box keeps it visible and editable, never overwrites what they
typed, and sends when they decide — where a typed message already waits.

**`workbench.js`.** Wired to the above; the submit handler checks `state.busy`
before clearing the box; `#voice-path` says which path is in use; the state word
shows *Transcribing…*. `pages.go` loads `audio-input.js` on the workbench and
adds `#voice-path`; `workbench.css` gives the mic `touch-action: none` and a
transcribing state.

## Verification

- **Fences, red first.** With origin/main's `voice.js`, `workbench.js` and
  `pages.go` swapped in, every `TestVoiceInput_*` and the wiring fence failed; the
  Go handler and `llm` fences do not compile against main (no handler, no
  `Unanswered`). All green on the fix.
- **Drills.** A section in `scripts/drill-fences.sh`, *"The workbench
  microphone"*: 25 mutations, each putting one cause back. Run with a temporary
  runner: **25 went red, 0 stayed green, 0 unproven, 0 anchors moved**, tree
  restored byte-identical.
- **Suites.** `go vet ./...` clean; `go test ./internal/httpapi/ ./internal/llm/
  ./internal/media/` green (with the test database); `node
  scripts/echo-guard-check.js` and `node scripts/voice-fallback-check.js` green.
- **Real provider.** `make test-asr`'s two commands against the production
  endpoint, 2026-09-15:
  - `TestTranscriptionPreservesSpokenDecimals` (Ogg, both fixtures),
    `TestTranscriptionOfWhatBrowsersRecord` (WebM, MP4) and
    `TestThePipelinesOwnContainerTranscribesCorrectly`: **red**, all with
    `CONNECTOR_UNAVAILABLE … returned 404: Model not exist.` — the endpoint
    serves no transcription model. The new classification is what those lines
    now say; before this fix they read as an outage.
  - `TestTranscribingNothingIsNotAnError`, `TestAnAbsurdlyLargeSegmentIsRefusedLocally`,
    `TestSpokenAudioBecomesAnAttributedTurn` (segmentation only): green.
  - `TestForgesVoiceIsIntelligible`: red, `llm.Speak` 404 — the speaker model
    (`FORGE_LLM_SPEAKER_MODEL`, default `qwen3-omni-flash`) is not served either.
    Not touched here; it is the same configuration gap on the other direction.

  So the browser containers have **never been seen transcribed**; that is the
  first thing to run once a transcription model is configured.

## Regression prevention

- `internal/httpapi/voice_input_test.go` — runs the real `voice.js` in node
  against a stubbed `MediaRecorder`, `getUserMedia`, `SpeechRecognition` and
  `fetch`, whose recogniser throws `InvalidStateError` the way Chrome does:
  - `TestVoiceInput_PushToTalkRecordsAndUploadsToTheServer`
  - `TestVoiceInput_TheMicUsesAPathThatCanWorkAndSaysWhich`
  - `TestVoiceInput_EveryFailureReachesTheNote` (eight failures)
  - `TestVoiceInput_PressingAgainBeforeTheLastSessionEndedStillListens`
  - `TestVoiceInput_TheHoldSurvivesTheCursorLeavingTheButton`
  - `TestVoiceInput_AQuickPressSaysHoldToTalk`
  - `TestVoiceInput_WhatWasSaidDuringATurnIsKeptInTheTextBox`
  - `TestWorkbench_TheMicIsWiredToTheHoldAndKeepsWhatWasSaid`
- `internal/httpapi/transcribe_test.go` — the endpoint's refusals by name, the
  container handling, the route, and `/v1/meta/models`.
- `internal/llm/transcribe_unit_test.go` — 404 is unavailable-here, not an
  outage; no choices is unanswered, not silence; silence is still silence.
- `internal/llm/transcribe_browser_test.go` — real provider, WebM and MP4, under
  `make test-asr`.

## Not in this fix

- **A transcription model for production.** Configuration, and damon's call:
  set `FORGE_LLM_TRANSCRIBER_MODEL` to a speech-to-text model the endpoint
  serves. The token-plan endpoint serves none today. Options: ask the provider to
  enable one on this plan, or point transcription at an endpoint that serves
  `qwen3-asr-flash` (which would need a separate base URL and key setting that
  does not exist yet). Until then the page says exactly this when the mic is
  held.
- **The realtime model.** `qwen-audio-3.0-realtime-plus` needs DashScope's
  realtime WebSocket API, not chat completions (see
  `docs/spikes/2026-09-03-webrtc-sfu/`).
- **Hands-free through the server.** It still needs the browser's recogniser;
  server-side voice activity detection is a separate piece of work.
- **The deployed build** was not inspected, and a real microphone in the owner's
  browser was not exercised. Deploying is a separate step.
