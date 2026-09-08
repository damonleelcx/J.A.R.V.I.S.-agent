# She said every reply twice

**Found:** 2026-09-08, reported from production as "the agent is repeating the
voice response twice", alongside the banner *"Using the browser's voice instead
of FORGE's: this browser could not decode the audio (audio/mpeg)"*.
**Severity:** high on the voice path — every spoken reply was read twice, for as
long as her own voice was failing to decode.
**Status:** fixed. The failure underneath it — her voice being blocked — was
also found and fixed: see
`docs/bugfix/2026-09-08-csp-blocked-her-own-voice.md`.

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

## What triggered it: the block underneath

The decode failure that made her fall back at all was **CSP**, not audio: the
policy stated no `media-src`, so the `blob:` URL her voice is played through
inherited `default-src 'self'` and was blocked before any decoder saw it. The
browser reports that as `MEDIA_ERR_SRC_NOT_SUPPORTED` — the same code as an
unplayable file — which is why it read as a codec problem.

Root cause and the full investigation:
`docs/bugfix/2026-09-08-csp-blocked-her-own-voice.md`.

That matters here because it is what made the doubling reachable at all. Until
`/v1/speech` started failing in the browser, the second handler never ran and
this bug could not fire.

## The message now diagnoses itself

`"could not decode the audio (audio/mpeg)"` was true and useless. It reported
the content type — which was never in doubt — and not the size, which nobody
could see. It cost a full investigation that excluded four subsystems and
narrowed nothing.

`describeWire()` now reports the three things that can actually be wrong, and
what separates them:

| Failure | What names it |
|---|---|
| truncation | `1024 bytes received of 147956 declared — TRUNCATED` |
| empty body | `the server sent no audio (…, 0 bytes …)` — its own message, because an empty body reaches the element as an unopenable source and reports as a *decode* failure, sending the reader after a codec problem that is not there |
| wrong container | `header said …` when the declared type and the blob disagree |
| decoder refusal | `media error 3 DECODE` vs `4 SRC_NOT_SUPPORTED` — 3 means the decoder got the bytes and choked; 4 means it would not accept the source at all |

A body with no `Content-Length` is called out as chunked rather than compared
against nothing, so "no declared length" cannot read as "matched".

`jobs` guards with `if (!blob.size) return false;` and falls back silently.
FORGE names it instead: an empty body is a fault worth seeing, not a condition
to swallow.

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
