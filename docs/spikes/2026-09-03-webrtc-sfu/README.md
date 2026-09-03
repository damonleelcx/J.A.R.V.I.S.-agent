# Spike: realtime audio transport — WebRTC SFU and server-side transcription

**Date:** 2026-09-03 · **Status:** complete. SFU core proven end to end;
transcription proven and one reproducible defect found ·
**Owner:** voice transport (COL-01, AUD-01/03, NFR-04)

## The question

`docs/implementation-plan.md` (Wave 6) names realtime multi-party audio transport
as the one thing COL-01 is missing, and `internal/domain/collab/room.go` says the
record was shaped to accept one. Building it rests on two premises that had never
been tested in this repository:

1. **Can this codebase carry a WebRTC SFU at all** — does `pion/webrtc` negotiate
   and forward RTP, and what does it actually cost in dependencies?
2. **Is there a usable server-side ASR path** on the configured provider, so that
   a forwarded stream can become an attributed turn in `forge_room_turns`?

Both are answered below with real measurements. Reproduce with:

```bash
./probe-asr.sh        # transcription matrix (network, costs a few fractions of a cent)
(cd sfu-core && GOWORK=off go run .)   # the SFU core, offline
```

---

## Result 1 — the SFU core works, and speaker separation is structural

`sfu-core/main.go` wires a sender → SFU → receiver chain with real peer
connections and a real Opus track, then forwards RTP through it:

```
PASS  inbound track identified, stream id = participant-alice
PASS  RTP arriving at SFU
PASS  receiver got 5 forwarded RTP packets
RESULT SFU-CORE-OK streamID=participant-alice
```

The line that matters is the first one. **The inbound track arrives already
carrying the participant's identity**, so "who is speaking" is a property of the
transport rather than something inferred from the audio. That is what makes
AUD-03's speaker separation and COL-01's per-speaker attribution true by
construction — for participants on separate clients.

It does **not** separate several people sharing one microphone. That needs
diarization on a mixed stream and is not solved here; the implementation leaves a
seam for it and the docs must not claim otherwise.

### What it costs

`github.com/pion/webrtc/v4 v4.2.19`, **pure Go — builds with `CGO_ENABLED=0`**,
which matters because `make dist` cross-compiles.

22 modules are linked into the binary, **20 of them new** to this repository: the
16-module pion tree, plus `github.com/google/uuid`, `github.com/wlynxg/anet`,
`golang.org/x/net` and `golang.org/x/time`. Only `golang.org/x/crypto` and
`golang.org/x/sys` were already present.

`go list -m all` additionally shows `onsi/ginkgo`, `onsi/gomega` and
`sclevine/agouti`; those are **test-only dependencies of pion packages and are
never linked** — verified with `go list -deps`, which is why the module-graph
count (22 added) and the linked count (20 added) differ.

This repository has three direct dependencies today and is deliberate about it.
Twenty new linked modules is the real price of media transport, recorded here so
it is a decision rather than a surprise.

---

## Result 2 — ASR exists, but not at the endpoint you would expect

`FORGE_LLM_BASE_URL` defaults to DashScope's OpenAI-compatible endpoint. The
OpenAI-style transcription route **does not exist there**:

```
POST /v1/audio/transcriptions   →  404   (confirmed with a valid key)
```

Transcription is reached through `/chat/completions` with an `input_audio`
content part, base64 as a `data:audio/wav;base64,` URI. Working model:
**`qwen3-asr-flash-2026-02-10`**. The provider lists 49 audio-capable models;
`fun-asr-flash` and `qwen-audio-3.0-asr-flash` reject this request shape with
`UNSUPPORTED_FORMAT`.

**The realtime variants are not reachable over compatible-mode REST**
(`qwen3-asr-flash-realtime` → HTTP 500). They require DashScope's dedicated
realtime WebSocket API. Phase 3 therefore chunks audio and uses batch REST, or
takes on a WebSocket client dependency — a decision that belongs to Phase 3 with
these numbers in hand.

---

## Result 3 — the defect: engineering values are silently corrupted

This is the finding that changes the design.

Spoken (macOS `say`, so the input is known exactly):

> "Set the bracket wall thickness to **two point five** millimetres, tolerance
> plus or minus **zero point one**, on part A K seven three."

Returned with no system context:

> "Set the bracket wall thickness to **two five** millimeters tolerance plus or
> minus **zero one** on part AK seven three."

**The decimal points are gone.** A 2.5 mm wall becomes "two five"; a ±0.1 mm
tolerance becomes "zero one". In an engineering transcript that is not a
transcription artifact, it is a wrong number that reads as a right one — exactly
what RSN-06 ("no fabricated measurements") exists to prevent, arriving through a
path RSN-06 does not currently watch.

### Controlled experiment

Control variable: the system-message context. Model held at
`qwen3-asr-flash-2026-02-10`. 5 trials per cell, all deterministic.

| utterance | empty context | domain vocabulary | decimal rule | vocabulary + decimal |
|---|---|---|---|---|
| long sentence | ✗ "two five" | ✓ | ✓ | **✓ 5/5** |
| short sentence | ✗ "one five" | ✗ **"one five"** | ✓ | **✓ 5/5** |
| bare "zero point five" | ✓ | — | — | **✓ 3/3** |

Two conclusions:

- **Domain vocabulary alone does not fix it.** It corrected the identifier
  (`AK73` instead of "AK seven three") but still dropped the decimal on the short
  utterance, 5/5. AUD-03 lists vocabulary and unit-aware transcription as separate
  capabilities and this is the evidence for why: they are not the same lever.
- **The explicit decimal rule is the lever that worked in every cell.** The
  production context must carry both — vocabulary for identifiers and terms, the
  decimal rule for values.

### Why this is more dangerous than it looks

The bare utterance "zero point five" transcribes correctly with *no* context at
all. The corruption appears **only in realistic engineering sentences**, where
surrounding words give the model something to normalise against. A naive smoke
test — one short phrase, eyeballed — passes. The failure is reserved for the
sentences the product actually exists to handle.

Consequence for implementation: the ASR system context is not a tuning
parameter, it is a correctness requirement, and it needs a fence that feeds real
speech through and asserts the decimal survives.

---

## Result 4 — the latency budget is already spent

`qwen3-asr-flash-2026-02-10`, 2.61 s audio chunk. Measured twice: **0.67–0.80 s**
driving `curl` directly, and **0.74–0.92 s** through `probe-asr.sh`, whose timing
also covers base64 encoding. The committed run is `data/asr-matrix.txt`. Call it
**0.7–0.9 s per chunk**; the spread does not change the conclusion.

AUD-02 asks for end-of-utterance → first audio within 700 ms. Server-side
transcription consumes that entire budget **before the conversational model is
called at all**. The browser's local `SpeechRecognition` in `voice.js` has no such
penalty.

This does not block the transport — people hearing each other is the point of an
SFU, and that path has no ASR in it. It does mean Phase 3 must decide where the
transcript comes from, and `config.go`'s existing note on the `Converse` model
(chosen for latency, and still not meeting AUD-02) already tells us the honest
answer is to measure and display the real figure rather than claim the target.

---

## What this changes in the plan

- **Phase 2 (SFU) is unblocked.** pion works, it is pure Go, and the cost is known.
- **Phase 3 (transcription) has a mandatory piece it did not have before:** an ASR
  system context carrying both domain vocabulary and an explicit decimal rule, and
  a fence over real speech asserting values survive. Without it the room record
  fills with plausible wrong numbers.
- **Phase 3's transport for ASR is an open choice** — chunked batch REST (works
  today, ~700 ms) versus DashScope's realtime WebSocket API (another dependency).
  Deferred to Phase 3 with the numbers above.
- **AUD-02 stays reported as unverified**, as `docs/prd.md` already requires.
