# An expression written where a number was expected threw away the whole reply

**Found** 2026-09-06, by running the evaluation suite after the conversation
model was replaced. **Fixed** the same day.
**Fence:** `internal/agent/dimensionrepair_test.go`, on two real replies kept in
`internal/agent/testdata/`.

## What the person saw

> That reply came back in a shape FORGE could not read, so nothing of it is
> shown rather than showing you the machinery. Asking again usually settles it.

No geometry, no detail, no parameters. Everything the model said, gone.

## What was actually happening

The document contract offers TWO spellings for every dimension — a literal
(`x`, `size.width`) and an expression (`x_from`, `size_from`) — and the framing
tells the model, more than once, to prefer the expression. Asked for a V-belt
pulley, qwen3.7-plus wrote this:

```json
"profile": [{"x": "outside_radius", "y": "half_width - groove_depth * tan(...)"}]
"size":    {"radius": "bore_diameter / 2", "height": "width * 2"}
```

Valid JSON. Complete — `finish_reason` was `stop` both times. And unparseable:
Go refuses a string in a `float64` field, and ONE such field discards the entire
reply, because the whole document is one `json.Unmarshal`.

The model is not wrong in any way a person would recognise. It was asked for an
expression, it wrote one, and it put it beside the name of the thing it
describes.

## Impact

Whole documents lost, intermittently, on exactly the designs that need
expressions most — the ones where several dimensions relate to each other.

Measured against qwen3.7-plus at `--repeats 3` on 2026-09-06:

| case | scorer | before |
|---|---|---|
| `draws-a-turned-part` | drawn as a revolve | **0 of 3** |
| `covers-product-design` | the request is answered | **0 of 3** |
| `covers-aerospace` | the request is answered | 1 of 3 |
| `draws-a-closed-loop` | the outline resolves | 2 of 3 |

Every one of those was this and nothing else. The suite reported capabilities
that had gone dead; what had actually happened is that the reply was being
discarded on arrival.

## This is the defect the implementation plan carried unexplained

`converseMaxTokens`' comment in `internal/agent/converse.go` records that a
V-belt pulley failed to parse on **2026-09-05**, that truncation was investigated
and ruled out over three further runs, and that "that reply was simply malformed,
intermittently".

It was not malformed. It was this. The pulley is the shape most likely to
provoke it, because its outline is where a model has the most dimensions to
relate to one another — which is why the same case produced it on both days,
against two different models, four weeks of contract growth apart.

## Root cause, at three depths

- **Surface:** a string arrived in a `float64` field.
- **Deeper:** one unreadable field discards the whole document, because the
  reply is parsed as a single object with no partial reading.
- **Structural:** the contract has two slots for one idea and strongly
  recommends one of them. That is a shape which *invites* the mistake, and the
  cost of making it was set at "lose everything". Telling the model more firmly
  which slot to use makes the failure rarer without making it survivable.

## The fix

`internal/agent/dimensionrepair.go`. After a strict parse has already failed —
and only then, so a reply that parses is never rewritten — the document is walked
as generic JSON and a string in a numeric slot is READ. There are exactly two
honest readings and it does both:

- a number in quotes (`"65.0"`) is that number;
- an expression, where the contract has a field for the expression of that very
  dimension, is moved there.

Anything else is left exactly as it was and fails exactly as it did. Prose in a
parameter's `value` has no honest reading and is not invented one.

The numeric key is **deleted** rather than zeroed, because zero is a length, a
position and a radius that all mean something; an absent key takes the documented
default and is reported as an inference. A `position` is an array, so its bad
element is zeroed instead — deleting one would shift every axis after it.

The person is told. `Reply.Repaired` travels as a `notice` stream event and the
workbench renders it in the warning gold it already uses for "quoted from memory,
not checked", because it is the same kind of statement: what you are looking at
is not exactly what arrived.

## What was NOT done, and why

The contract was not changed to have one slot per dimension. That is the real
fix for the shape of the problem and it is a breaking change to every stored
document; this makes the existing shape survivable, which is what was needed
today. It is worth reconsidering the next time the contract moves.

## Regression

`make test` — `internal/agent/dimensionrepair_test.go`:

- `TestARealReplyThatWasLostNowParses` over both captured replies, which first
  asserts each fixture does **not** parse as it stands, so a fixture that stops
  being the failure it was collected for cannot quietly pass.
- `TestAnExpressionLandsInItsOwnField`, `TestTheNumberIsRemovedRatherThanSetToZero`,
  `TestAPositionKeepsItsShape`, `TestAQuotedNumberIsThatNumber`,
  `TestAValidReplyIsNotRewritten`, `TestTheExpressionFieldWinsWhenBothAreWritten`,
  `TestSomethingWithNoHonestReadingIsLeftAlone`.
