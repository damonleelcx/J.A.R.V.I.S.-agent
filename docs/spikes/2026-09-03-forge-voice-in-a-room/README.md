# Spike: FORGE's voice in a room

**Date:** 2026-09-03 · **Status:** complete. The question is answered and the
decision it feeds is open · **Owner:** voice transport (AUD-01, AUD-05, AUD-07)

## The question

Waves 9 to 9.5 built a room that carries audio between people, transcribes it,
and controls it. **FORGE has no voice in it.** It speaks in the workbench through
the browser's own synthesiser, where barge-in is handled locally; a room has
nothing for AUD-01's barge-in to interrupt and nothing for AUD-07's
stop-speaking to stop.

Making FORGE audible in a room means choosing where its speech is produced, and
that choice is not free. So: *can the server produce speech the SFU can carry,
and at what cost?*

Reproduce with `./probe.sh` (network; costs a fraction of a cent).

---

## Result 1 — the provider streams speech, and quickly

Not at `/audio/speech` — that route does not exist here (404), the same shape as
the ASR finding in the previous spike. Speech comes from `/chat/completions` with
`modalities: ["text","audio"]`, and **only when streaming**: the non-streaming
response bills for audio tokens and returns no audio at all.

`qwen3-omni-flash`, three runs:

| | first audio | speech produced | stream complete |
|---|---|---|---|
| trial 1 | 0.63 s | 5.36 s | 1.79 s |
| trial 2 | 0.66 s | 3.44 s | 1.37 s |
| trial 3 | 0.54 s | 3.20 s | 1.20 s |

Two things matter here. **First audio in ~0.6 s**, so FORGE can begin speaking
about six tenths of a second after deciding to. And audio arrives *faster than
real time* — 3.2 s of speech delivered in 1.2 s — so a player never starves.

---

## Result 2 — the audio is raw PCM, and that is the whole problem

The stream carries **raw 16-bit PCM, 24 kHz, mono**. No container, no Opus.
Confirmed by decoding it at 24 kHz and transcribing it back:

> "Setting the wall thickness to two point five millimetres."

The SFU forwards Opus and never encodes anything — that is what has kept the
whole media plane pure Go and `CGO_ENABLED=0` across four platforms. Publishing
PCM into it means encoding.

**There is no usable pure-Go Opus encoder.** `github.com/pion/opus` exports
`NewDecoder` and `NewDecoderWithOutput` and nothing else; it carries
`internal/celt/encoder.go`, but no public encoder API. Every working Opus encoder
in Go is cgo bindings to libopus.

So the question becomes: what can the server produce that a browser will play,
without cgo?

---

## Result 3 — G.711 is good enough, and that is measured rather than assumed

Every WebRTC stack supports G.711 (PCMU/PCMA). Encoding 16-bit PCM to µ-law is
arithmetic — a few dozen lines of pure Go, no dependency. The cost is
telephone-grade audio: 8 kHz, against the 48 kHz Opus the humans in the room are
using.

The thing worth measuring is not "does it sound worse" — it does — but **whether
it still carries the content this product exists for.** The previous spike
established that a dropped decimal point is the failure that matters. So the same
utterance was pushed through the exact transform a G.711 track applies (24 kHz →
8 kHz → µ-law → back) and transcribed:

| path | transcript |
|---|---|
| full quality, 24 kHz | "Setting the wall thickness to **two point five** millimeters." |
| **through G.711 µ-law at 8 kHz** | "Setting the wall thickness to **two point five** millimeters." |

Identical. The decimal survives. Telephone quality loses timbre, not numbers.

---

## The three shapes this can take

| | where speech is produced | quality | pure Go | one voice, one instant |
|---|---|---|---|---|
| **A** | server → **G.711 track in the SFU** | 8 kHz | **yes** | **yes** |
| **B** | server → Opus track, via cgo | 48 kHz | **no** | yes |
| **C** | each browser, from text | browser's own | yes (no server audio) | **no** |

**A** puts FORGE in the room as a participant. Everybody hears the same audio at
the same moment, and barge-in is a real stop: cut the track and it stops for
everyone together.

**B** is A with better audio, bought by taking cgo into the build. That ends
`CGO_ENABLED=0` for linux/amd64, linux/arm64, darwin/arm64 and windows/amd64 —
`make dist` would need a cross-compiling C toolchain per platform. A release
pipeline regression in exchange for audio fidelity.

**C** broadcasts the text and lets each browser speak it, exactly as the
workbench does today. No server audio, no TTS bill, no media-plane code. Two
costs: the voice is whatever the platform provides — different on macOS, Windows
and Android — which is a direct loss against AUD-05's "voice identity and tone";
and FORGE is not really in the room, it is N separate playbacks that can drift.
Barge-in still synchronises acceptably, because the *stop* is a server event.

---

## What this spike does not settle

Whether the 8 kHz voice is acceptable *as the character's voice* is a product
judgement, not a measurement. The numbers say the words survive; they do not say
whether FORGE sounding like a telephone call, beside participants who do not, is
the right impression for this product to give.

That is the decision this spike exists to inform, and it is deliberately left
open.
