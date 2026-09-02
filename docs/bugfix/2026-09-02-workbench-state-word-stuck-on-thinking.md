# The workbench said "Thinking…" after it had finished thinking

**Date:** 2026-09-02 · **Phase:** 6 (workbench) · **Severity:** medium — the
product's primary state display could be permanently wrong · **Owner:** workbench
front end

## Symptom

On a machine with no audio output, a completed turn left the voice surface
reading **Thinking…** forever. The reply was on screen, the transcript was
complete, the header showed `spoke at 582ms · full reply 14622ms`, geometry had
rendered — and the one large word the interface uses to say what it is doing was
still claiming a model call was in flight.

Observed in the browser preview pane, which has no audio device.

## Root cause

The turn's completion handler was:

```js
state.busy = false;
if (!spoke) setStatus('idle');
```

`spoke` meant "we called `voice.speak()`", and the code assumed that speaking
would always reach its own `onend` and reset the status there.

`speechSynthesis` does not guarantee that. An utterance that never starts —
no audio device, an autoplay policy block, one of Chrome's long-standing silent
drops — fires **neither** `onstart` nor `onend`. Nothing ever reset the word.

The deeper mistake is the shape of the condition: the status was derived from
**what we asked for** rather than **what is happening**. Any state display
computed from an intention rather than an observation will eventually disagree
with reality, and this one had no path back.

## Fix

`internal/httpapi/assets/workbench.js` — resolve the state from the voice
layer's actual condition, and give speech an explicit completion callback:

```js
voice.speak(ev.text, function () { if (!state.busy) setStatus('idle'); });
...
state.busy = false;
setStatus(voice.speaking ? 'speaking' : 'idle');
```

Two independent paths now return the display to rest: the utterance ending, and
the turn ending. Neither depends on the other, and if speech never starts the
second one is unaffected.

## Acceptance

Reproduced and confirmed on the live workbench in a browser with no audio:

- **before** — reply complete, `meta` showing measured latencies, state word
  frozen at `Thinking…`
- **after** — same conditions, `{"state":"Ready","meta":"spoke at 224ms · full
  reply 1359ms"}`

## Not fixed here, deliberately

There is still no *automated* fence on this. It needs a browser with a
controllable `speechSynthesis`, which this repository has no harness for, and a
fake `speechSynthesis` in a unit test would assert against the fake's behaviour
rather than the browser's — which is the assumption that caused the bug. The gap
is stated rather than papered over.

## Related

- `docs/prd.md` AUD-07 — mute, stop-speaking and pause must always be reachable
  and truthful. A permanent "Thinking…" is the visual half of that failing.
