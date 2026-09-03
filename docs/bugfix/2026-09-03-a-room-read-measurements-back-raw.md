# A room read measurements back exactly as written

**Date:** 2026-09-03 · **Phase:** 9.6 (FORGE speaks in a room) · **Severity:**
medium — AUD-04 unmet on the surface it matters most on, with no visible symptom
· **Owner:** the room voice, wave 9.6

## Symptom

FORGE says, in a room: *"Wall thickness is 2.5mm ±0.1, cure at 80°C."*

The speech model was handed that string verbatim, so what came out of the
speakers was whatever the model decided "2.5mm" and "±" sound like. A listener
writing the number down got it wrong, or did not get it at all — `±` is silent
in most voices, which turns a tolerance into a bare dimension.

Nothing else looked wrong. The request succeeded, audio arrived inside the
AUD-02 budget, the transcript in the record was correct, and `tts.spoke` logged
a normal utterance. The only evidence was in the room, audible, and only to
somebody who already knew what the sentence was supposed to say.

The same sentence typed into the **workbench** was read back correctly, because
the workbench synthesises in the browser and `assets/voice.js` has normalised
its text since it shipped.

## Root cause

PRD **AUD-04** ("technical readback of numbers, units, tolerances, IDs,
coordinates") was implemented once, in the browser, when the only place FORGE
spoke was the browser.

Wave 9.6 added a second speech path with a different reason for existing: a room
is shared, so the server synthesises and everybody hears one voice at one
instant (see the header of `internal/media/speech.go`). That path was built as
audio plumbing — provider stream, resampling, G.711, barge-in — and the text
went straight through it. `SFU.Say` passed its argument to `Speaker.Speak`
untouched.

So this is not a rule that broke. It is a **requirement with two consumers and
one producer**: a second surface appeared under an existing requirement and
nobody re-asked whether the requirement was met on it. The requirement index
still said AUD-04 was done, and on the surface anybody checked, it was.

Worth naming because the shape recurs: the readback matters *more* in a room
than in the workbench, because a room is where a number is being agreed between
people. The requirement was met on the cheap surface and missed on the expensive
one.

## Fix

**`internal/media/readback.go`** (new) — `Readable(text)`, the readback rules
ported from `assets/voice.js`, rule for rule and in the same order.

**`internal/media/speech.go`** — `SFU.Say` normalises before synthesis:

```go
spoken := Readable(text)
```

Placed in `Say` rather than at the HTTP call site so it holds for every caller,
and rather than inside `llm.Speak` so it holds for every `Speaker`
implementation — swapping speech providers is the likelier future change, and it
must not silently drop the requirement again.

It runs **after** the turn is written to the record, so the transcript keeps what
FORGE wrote (`2.5mm ±0.1`) while the voice says the spoken form. Those are the
same fact at two resolutions and the record wants the first one.

### The duplication, and why it is not shared

The rules now exist twice, in JavaScript and in Go. The alternatives were a
network round trip per browser utterance, or generating the JavaScript from the
Go — both disproportionate to eleven regular expressions. This is the same trade
`speech.go` already makes for the sample rate constant, and it is made safe the
same way: **a fence, not a promise.**

`TestTheReadbackRulesHaveNotDrifted` counts the substitutions in
`assets/voice.js` and compares them to the Go table. Counting is coarse
deliberately — it survives reformatting and fires on the one thing that actually
goes wrong, a rule added to one copy only. It cannot check that matching rules
*mean* the same thing; the case table is what checks that.

## Verification

`internal/media/readback_internal_test.go`:

- `TestReadbackMakesTextTranscribable` — versions, six units, tolerances,
  identifiers, markdown, and prose left alone.
- `TestTheReadbackRulesHaveNotDrifted` — the two copies still agree.

`internal/media/speech_test.go`:

- `TestForgeSpeaksTheReadableForm` — asserts what the **speaker received**, not
  what the caller passed. A fence on the caller's argument would have passed
  throughout the entire life of this bug.

Each was drilled by mutation and watched fail:

| mutation | fence that fired |
|---|---|
| `Say` passes `text` again instead of `spoken` | `TestForgeSpeaksTheReadableForm` |
| one rule dropped from the Go table | `TestTheReadbackRulesHaveNotDrifted` (11 vs 10) |
| markdown rule moved ahead of the identifier rule | `TestReadbackMakesTextTranscribable` (2 cases) |
| `\b` put back on the front of the version rule | `TestReadbackMakesTextTranscribable` (2 cases) |
| dotted-number rule pinned back to exactly three segments | `TestReadbackMakesTextTranscribable` (2 cases) |

The Go port was also checked against the real browser copy by evaluating
`readable()` straight out of `assets/voice.js` under node on the same inputs.
Byte-identical output on all twenty-one.

## The version rule was widened at the same time

**`v0.2.0` was not normalised — by either copy.** The rule opened with `\b`, and
there is no word boundary between `v` and `0`, so the most common way to write a
version was the one form it did not match. Bare `0.2.0` worked.

The doc comment above `readable()` uses `"v0.2.0"` as its worked example, which
is how this survived in the browser since it shipped: the comment described the
behaviour everyone assumed, and nobody ran the string through the function. It
surfaced here only because the port's first test case was written from that
comment and failed.

Raised as a separate decision rather than folded into the fix, because widening
it changes what the **workbench** says as well as what a room says. Decided:
widen.

The front of the pattern is now simply unanchored — `(\d+)\.(\d+)\.(\d+)\b` —
rather than special-cased for `v`. Two reasons:

- Anchoring it precisely would need a lookbehind, and **RE2 has none**, so the
  Go and JavaScript copies could not have stayed identical that way. A rule that
  can only be written in one of the two engines is a rule that will drift.
- Unanchored covers `rev1.2.3` and `V2.0.1` without anybody enumerating the ways
  people write a version.

The cost is that digits after any letters now normalise: `abc1.2.3` becomes
`abc1 point 2 point 3`. Spelling out a dotted number in an unexpected place is a
much cheaper mistake than skipping one in an expected place.

Both copies were then run over the same twenty-one inputs — the Go through
`Readable`, the JavaScript by evaluating `readable()` straight out of
`assets/voice.js` under node — and the outputs were **byte-identical**, including
every case above.

## The rule stopped counting segments

`1.2.3.4` was read as **"1 point 2 point 3.4"** — the first three segments spoken
and the fourth left as a decimal. Worse than either treatment on its own, and the
reason was that the pattern hardcoded exactly three segments while the trailing
`\b` happily matched the dot in front of the fourth.

Segment count is not something this rule should have an opinion about. A version
has three, an IP address has four, a build number has as many as it likes, and
all of them want the same treatment. The pattern is now:

```
\d+(?:\.\d+){2,}\b
```

with the match split on `.` and rejoined with " point ", in both copies. Two dots
minimum, so a plain decimal is untouched — `2.5` and `$2.50` are already read
correctly and rewriting every decimal in every sentence would touch far more text
than AUD-04 asks for.

The trailing `\b` stays. It keeps `1.2.3mm` out of this rule and leaves it to the
unit rules, which is where a measurement belongs.

### The engines had to be checked, not reasoned about

`{2,}` followed by `\b` forces backtracking on `1.2.3mm`: the greedy match takes
all three segments, fails the boundary against the `m`, and backs off. **RE2 does
not backtrack** — it simulates — so Go and JavaScript agreeing here is a fact
about two implementations, not a deduction.

Measured: both copies run over twenty-one inputs, the Go through `Readable` and
the JavaScript by evaluating `readable()` out of `assets/voice.js` under node.
Byte-identical, including every backtracking case.

### Residue, recorded and not chased

`1.2.3.4mm` becomes `1 point 2 point 3.4 millimetres` — the same half-spoken
shape, one level deeper, because the backtracking lands on the first three
segments and leaves `.4mm` to the unit rule.

Left alone: no measurement is written with three dots in it. This input was
constructed to find the edge, not observed. Removing the trailing `\b` would fix
it and would change what `1.2.3mm` does, which is the case that actually occurs.
Both engines agree on the output, so at worst it is consistently odd. Asserted in
`TestReadbackMakesTextTranscribable` so it reads as a decision rather than a
surprise.
