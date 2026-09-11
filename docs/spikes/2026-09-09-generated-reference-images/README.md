# Can a generated image be the reference a prototype is built against?

**Asked for, 2026-09-09:** FORGE should sketch a 2D image first, analyse it,
build the complete Document from it, build the 3D from the Document, and keep
working until the 3D matches the 2D. Then: *"maybe not just one image but many
from different angles."*

**Question this spike answers:** is that buildable on this deployment, and what
can the generated image actually be trusted to say?

Everything below is a real call against the live endpoint with the production
key. Prompts, raw shapes and the model's own readings are quoted so the
conclusions can be checked rather than taken.

## 1. Is there an image generator at all?

`FORGE_LLM_BASE_URL` is `https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1`.
Listing models with the production key returns **12**, two of which generate
images:

```
deepseek-v4-flash-0731  deepseek-v4-pro  glm-5.2
qwen-audio-3.0-realtime-plus  qwen-audio-3.0-tts-plus
qwen3.6-flash  qwen3.7-max  qwen3.7-plus  qwen3.8-flash  qwen3.8-max
wan2.7-image  wan2.7-image-pro
```

So yes — and no new vendor, no new key, no new bill to set up.

## 2. What is the wire format? (It is not OpenAI's.)

Three shapes were tried. Only the third works.

| attempt | result |
|---|---|
| `POST /images/generations` | `400 InvalidParameter: url error, please check url` |
| `POST /chat/completions`, `content` a **string** | `400 InvalidParameter: Input should be a valid list: input.messages.0.content` |
| `POST /chat/completions`, `content` a **list of parts** | **200** |

The reply is **DashScope-native, not OpenAI-shaped**:

```json
{"request_id":"…",
 "output":{"choices":[{"message":{"role":"assistant",
   "content":[{"type":"image","image":"https://dashscope-….oss-accelerate.aliyuncs.com/…"}]},
   "finish_reason":"stop"}],"finished":true},
 "usage":{"image_count":1,"input_tokens":978,"output_tokens":2,"size":"2048*2048"}}
```

**Consequences for the implementation:**

- `llm.OpenAICompatible` parses `choices[…]` at the top level and would find
  nothing here. This needs its own decode path, on the same base URL and key.
- The image comes back as an **OSS URL**, not base64. 2048×2048, ~5.9 MB.
- Asynchronous submission is **refused** on this key —
  `403 AccessDenied: current user api does not support asynchronous calls` — so
  the call is synchronous and holds the connection for the whole generation.
- ✅ **The vision model reads that URL directly.** So the "analyse the image"
  step needs no download, no storage, and no new egress from the pod: the URL is
  handed to `RoleVision` exactly as an uploaded image already is.

## 3. What does it actually draw? (The finding that changes the design.)

Prompt: *"A clean black-and-white 2D technical line drawing of a **20-tooth
involute spur gear**, front elevation, centred, plain white background, no
shading, no text."*

Measured two independent ways.

**By pixels** — sampling a circle at 0.99 of the outer radius and counting runs
of dark pixels gives **30 tip crossings**. **By the vision model**, shown the
image and asked to report what it sees:

```json
{"teeth": 28, "tooth_form": "rounded petal-like lobes",
 "has_bore": true, "dimensions_shown": false}
```

**28–30 teeth against the 20 requested. Petal lobes, not involute flanks. No
dimensions anywhere.**

The picture is a good *impression* of a gear and a wrong *specification* of one.

⚠️ This is what makes the last sentence of the request dangerous as literally
stated. "Keep working until the 3D matches the 2D" would take the 20-tooth
involute gear the script loop now builds and kernel-verifies, and repair it
toward a 28-tooth petal shape. The loop would work exactly as specified and make
the part worse — the same trap `look.go` was rescued from twice, where a repair
driven by a wrong complaint damages a good model.

## 4. "Many images from different angles"

Two candidates, one part: *an L-shaped bracket with two bolt holes in the upright
and a slot in the base.*

### (a) Three independent generations — front, side, top

The vision model, shown all three and asked how many distinct viewing directions
they contain:

```json
{"distinct_viewing_directions": 1,
 "second_image_is": "front view", "third_image_is": "front view",
 "usable_as_orthographic_set": false}
```

All three are the front. **Asking for an angle does not get you that angle** —
the generator draws the recognisable picture of the object every time. Three
calls, three times the cost and latency, one viewing direction, and three
subtly different objects.

### (b) One generation carrying three panels

```json
{"same_object_in_all_panels": true, "distinct_viewing_directions": 2,
 "middle_panel_is": "a foreshortened/rotated front (face) view of the plate, not a true side view",
 "usable_as_orthographic_set": false}
```

**Consistency is solved** — one generation cannot contradict itself, and every
panel is the same object. Two distinct directions rather than three, and still
no true side elevation.

### What that settles

| | separate calls per angle | one call, many panels |
|---|---|---|
| same object in every view | ✗ | ✓ |
| distinct viewing directions | 1 | 2 |
| a true orthographic set | ✗ | ✗ |
| cost / latency | 3× | 1× |

**One call carrying several panels is strictly better on every axis.** It is also
the most of "many from different angles" this generator can deliver.

And neither is a projection. That is not a prompt to be tuned — the model draws
pictures, not projections — so **no depth or thickness can be recovered from the
reference**, however many panels it has.

## 5. What this spike concludes

Buildable, on the existing key and endpoint, with a new decode path and no new
vendor. With one constraint that the evidence forces rather than suggests:

- **The image is authoritative for FORM** — silhouette, arrangement, what kind of
  thing this is, which is what it is genuinely good at.
- **The request stays authoritative for COUNTS and DIMENSIONS.** The generator
  gives 28 teeth for 20 and no dimensions at all; nothing else can supply them,
  and a picture that has none must not be allowed to overrule numbers a person
  gave.
- **The match check asks closed questions about form only.** Never "does it have
  the right number of teeth" — that is measurement, and `look.go` already carries
  the scar tissue explaining why an opinion about size drives damaging repairs.

Use one generation with several panels. Do not make one call per angle.
