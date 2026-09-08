# She said every reply twice

**Found:** 2026-09-08, reported from production as "the agent is repeating the
voice response twice", alongside the banner *"Using the browser's voice instead
of FORGE's: this browser could not decode the audio (audio/mpeg)"*.
**Severity:** high on the voice path — every spoken reply was read twice, for as
long as her own voice was failing to decode.
**Status:** fixed. The decode failure underneath it is a **separate, still-open**
defect, but the server side of it has been excluded with evidence — see "What
this does NOT fix" below.

## What happened

With `speak replies` on, FORGE read each answer aloud twice, back to back, in
the browser's voice. One banner appeared explaining the fallback; two voices
came out of the speakers.

## Root cause

A media element whose source will not decode reports the failure **twice**:

- it fires `error` on the element, and
- it rejects the `play()` promise with `NotSupportedError`.

Both, for one file. Measured in Chrome against an undecodable blob typed
`audio/mpeg`:

```
['onerror', 'play-rejected:NotSupportedError']
```

`_speakRemote` in `internal/httpapi/assets/voice.js` handled each of those
separately, and **each handler called `_speakLocal(text, onDone)`**. So one
failed load queued two utterances on `speechSynthesis`, which speaks them in
sequence — the reply, then the reply again. `onDone` also ran twice, so the
turn's status transition fired twice.

Each handler was individually correct. Neither knew the other had already run.

**Why it looked like an unrelated bug:** `_fellBack` latches — it reports the
reason once per page, deliberately, because a reason repeated on every reply is
noise. That latch worked. So the screen showed *one* banner while the speakers
produced *two* voices, and nothing on screen connected the repetition to the
audio failure causing it. The repetition was reported as its own defect.

**Why it survived:** the doubling only happens when the remote voice fails to
decode. Until `/v1/speech` started serving bytes this browser rejects, the
second handler never ran, and the bug was unreachable.

## The fix

One latch per utterance, in `_speakRemote`. The first path to fail owns the
fallback and owns the reason; a later path is the same failure seen a second
time and is dropped.

This is deliberately a **different** latch from `remoteVoice`:

| Latch | Scope | Question it answers |
|---|---|---|
| `handled` (new) | one utterance | has this utterance already fallen back? |
| `remoteVoice` | the page | should we stop asking the server at all? |

Keeping them separate is load-bearing. An autoplay refusal (`NotAllowedError`)
must fall back for **this** utterance without disabling her voice for the rest
of the page — the failure mode fixed in the commit before this one. Collapsing
the two latches into one would reintroduce it.

## Verification

`scripts/voice-fallback-check.js` (`make test-voice-fallback`, and part of
`make check`) loads the real `voice.js`, stubs the browser, drives a failing
load, and counts utterances.

It is proven to go **red on the pre-fix code**, in both orders the two failures
can arrive:

```
FAIL  onerror, then play() rejects    spoken=2 (want 1)  onDone=2 (want 1)
FAIL  play() rejects, then onerror    spoken=2 (want 1)  onDone=2 (want 1)
```

and green after. A string-matching fence would not have caught this — the old
code read as correct in both handlers. Only running both and counting shows it.

The script takes an optional path argument so it can be proven against a
known-bad copy **without editing the tree under test**; a drill that mutates the
working copy produces failures indistinguishable from real ones.

The third case asserts the happy path: audio that plays must not reach the
browser voice at all.

## What this does NOT fix

The reason her own voice fell back in the first place — the browser refusing to
decode the audio — is **still open**. This change only makes that failure
produce one voice instead of two, with an accurate reason.

### What was ruled out, with evidence

The first hypothesis was that the endpoint *labels* rather than *verifies*:
`SpeakMP3` defaults to `ct = "audio/mpeg"` when the vendor sends no
`Content-Type`, so PCM under a missing content type would be served as MP3 and
produce exactly this symptom. **That hypothesis is wrong.** Probed against the
live vendor with the deployment's own key, voice and backbone:

```
HTTP 200   bytes=200201
vendor Content-Type: audio/mpeg          <- sent by Fish, not defaulted by us
first 16 bytes: ff fb 90 c4 00 00 ...    <- MPEG-1 Layer III frame sync
```

and walking every frame rather than trusting the first:

```
frames parsed: 479      stray/unparsed bytes: 0
bitrates: [128] kbps    sample rates: [44100] Hz
duration: 12.51 s       VERDICT: well-formed MP3 end to end
```

The production log agrees the handler passed real audio through:

```
forge.tts.spoke  provider=fish  chars=65  bytes=157987  content_type=audio/mpeg
```

So the server side is **proven correct**: the vendor returns a well-formed MP3,
declares `audio/mpeg` itself, and the handler writes those bytes unmodified.
Nothing here needs changing, and the `ct` default is not implicated.

### What is left

The failure is in the browser, not in what is served. The reporting browser had
a Chrome update pending ("Finish update" in the toolbar) and was in an Incognito
window; a Chrome that has updated in place can lose its bundled media decoders
until it is relaunched, which surfaces as `MEDIA_ERR_SRC_NOT_SUPPORTED` on
formats it otherwise plays. That is a suspicion, not a finding — it has not been
reproduced.

Next step is to relaunch the browser and retry. If it still refuses, the thing
to capture is the `audio.error.code` and `networkState` on the failing element,
since the server side is now excluded.

One incidental oddity worth noting for later: 12.51 s of audio for a 65-character
sentence is roughly three times longer than the words take to say, and the file
begins with a long run of zero bytes. Fish appears to pad heavily. It is not the
cause of this bug and is not addressed here.

## Also fixed here: the speech rate never reached playback

`_speakRemote` assigned `audio.rate`, which is the `SpeechSynthesisUtterance`
spelling and not a property of a media element. The assignment silently created
a stray field, so the chosen rate was ignored whenever FORGE used her own voice,
while the browser-voice path honoured it — the same setting doing two different
things depending on which voice was live.

Nothing errored, which is why it survived: a mistyped property on a JS object is
not a fault. It is fenced behaviourally — the fence reads `playbackRate` back off
the element after a `speak()` at rate 1.5 — rather than by grepping the source,
because the bug was a plausible-looking assignment rather than a missing one.

## Related

- `docs/bugfix/` — the autoplay-refusal fix this one must not undo lives in
  commit `c0bcb6a`, fenced by `internal/httpapi/voice_echo_test.go`.
- Fence: `scripts/voice-fallback-check.js`, `make test-voice-fallback`.
