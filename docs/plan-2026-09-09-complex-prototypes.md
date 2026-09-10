# Complex prototypes: what it takes, in stages

**Asked for:** models like the reference — a vintage car with sculpted fenders,
wire wheels, chrome detail. **Decision, 2026-09-09: everything needed.**

## What that reference actually is, and why it matters

It is a polygon art asset: a subdivision-surface mesh, a few hundred thousand
quads, made by a human modeller or bought from a marketplace. Nothing in it
carries a dimension, a constraint or a manufacturing intent. Its wheel spokes are
modelled individually because a person modelled them.

FORGE makes B-rep solids with real dimensions that export to STEP and can be
machined. That is a different medium, and the honest framing is that no amount of
work here produces *that file*. What the work produces is a model of comparable
COMPLEXITY that is also correct — hundreds of features, dimensionally sound.

Where the two goals split, this plan takes correct. A pretty mesh with no
engineering meaning is available today from image-to-3D generators and is not
what "a durable engineering partner" is for.

## The three walls, with the evidence

| Wall | Evidence |
|---|---|
| The vocabulary tops out | Ten shapes, one JSON object per part. A 60-spoke wheel is 60 hand-written objects. A sculpted fender is not expressible: there is no surface primitive. Live models run 6–13 parts. |
| The agent builds blind | Vision is configured and reads only images the USER uploads. FORGE has never looked at its own render. Measured: length/width swapped 2/10 runs; `90` written for a quarter turn and read as 117°; wheels buried inside a 4.5 m-wide body; corner radii larger than the edges they sit on. A person catches every one of those in a glance. |
| One reply must hold the whole model | No incremental construction. Complexity is capped by a single generation, and it frays at 13 parts — silently dropped parts was a shipped bug on 2026-09-09. |

## Stages

Each stage closes on its own: it ships, it is fenced, and it is measured live
against the previous stage's number. No stage depends on a later one landing.

### Stage 1 — FORGE looks at what it built — **DONE 2026-09-09**

The agent renders the built solid server-side and reads it back with the vision
model before the turn is emitted.

- A rasterizer in Go over `geometry.Tessellate`'s triangles — orthographic, z
  buffered, four views (front, side, top, iso) on one contact sheet. No new
  dependency and no headless browser: the triangles are already there, and the
  renderer that exists is WebGL in the reader's browser, which the server cannot
  reach.
- The sheet goes to `RoleVision` as a `data:image/png;base64,…`, which is exactly
  how a user's uploaded image already reaches the model.
- What it finds feeds the existing repair round-trip, which already takes a list
  of problems and asks for a corrected document.

**Why first:** it is the smallest change, it adds no new surface to the document
format, and it aims at every failure measured this week. It does not raise the
complexity ceiling — it stops the agent working blind, which is what makes the
later stages measurable rather than guesswork.

**Measured, live, 3/3 and 4.56s for all three:**

| shown | what it said |
|---|---|
| a wheel buried inside a 4500mm-wide body | "a flat square plate lying horizontally instead of a vertical wheel" |
| a spoiler 3m above the car | "floating in the air above the main body, not attached to it" |
| the car as it actually ships | *nothing* |

The third row is the one that decides whether this is usable. A checker that
complains about a correct model drives repairs that damage it, and this
repository has already had to delete one rule that fired on the right answer.

**Four defects were found getting there, three of them in this stage's own code:**

1. `qwen-vl-max`, the configured vision model, does not exist on this endpoint.
   Production had vision deliberately unset and refused image uploads with a
   clear message, so nothing was broken — but the feature had never been
   available. `qwen3.8-max` reads images and is now the default.
2. `look()` swallowed that error, so every call reported "nothing wrong" about a
   car with a wheel inside its body. A check that fails silently is worse than no
   check, because it is trusted. It returns an error now, and "could not look"
   can never again read as "looked and it is fine".
3. The model answers `{"problems": ["…"]}` — bare strings, not the objects the
   contract asks for. Read strictly that is an empty list: a correct observation
   about a real defect, discarded.
4. Vision deliberated: **123000 ms against 1500 ms**, 6562 reasoning tokens
   against 29 tokens of answer, the same answer either way. `deliberation.go`
   already existed for exactly this and `RoleVision` was simply not in its
   table.

**And one question was removed.** "Are the proportions grossly wrong?" was the
only source of false positives — it called a 1900 x 800 x 4500 car body "grossly
too short for a sports car". Sizes are checked by measurement in `turned.go` and
do not need an opinion.

### Stage 2 — Build across turns instead of all at once — **DONE 2026-09-09**

An agent loop that decomposes a model into subsystems and builds them one at a
time, verifying each before the next.

- `prototype_edit` already exists and already guarantees that what an edit does
  not mention cannot be lost — it is the right mechanism and needs no change.
- `planner.go` already decomposes goals into tasks; this extends the same idea to
  geometry rather than inventing a second planner.

**Measured live: 60 parts and 4 features, all buildable, in 25 minutes** — from a
ceiling of 6. Every pass ran the same gauntlet a single turn faces, and the
visual check from Stage 1 did real work inside it:

| step | what looking caught |
|---|---|
| 4 | "the hub flange and brake assembly are floating in the air, completely detached from the chassis rails" |
| 5 | "the tire is floating clear of the alloy wheel" — corrected |
| 6 | "the steering column is oriented vertically, sticking straight up" |
| 8 | "the wiring harness is a single straight line floating in the air" |

Step 3 was refused outright because it would have broken the model, and the
eleven parts built before it survived.

**One defect, found live and worth keeping:** the build loop had its own copy of
the reply parser AND its own step prompts, and neither carried the geometry
contract. The model answered with a schema it invented —
`{"type":"box_beam","dimensions":{"length":4200},"position":{"x":-400}}` — and
SEVEN OF EIGHT steps produced nothing. The failure is silent in the worst way:
each step reports "produced no geometry" and the build finishes, so it reads as a
model that could not do the job rather than a prompt that never asked properly.
There is now one parser and one contract, both shared.

### Stage 3 — The model writes CAD code — **DONE 2026-09-09**

Instead of a JSON parts list, the model emits build123d Python, which the sidecar
already runs.

This is the stage that changes the ceiling by orders of magnitude: loops,
variables, real sketch-and-feature workflow, fillets on selected edges. A
60-spoke wheel becomes a `for` loop.

**Built in two halves, because the first one turned out to cover most of it.**

**`repeat`** (geometry/repeat.go) is the declarative half. What a loop actually
buys in CAD is REPETITION — variables and expressions were already here, edge
selection by rule was already here — so a wire wheel's sixty spokes are one part
with a `repeat`, a flange's twelve bolt holes are one part, and a feature naming
the part acts on every copy so one `fuse` welds all sixty spokes to the hub. No
sandbox, and everything that reads a Document keeps working.

**`script`** (cad/script.py, cad/script.go) is the executed half, for what a
pattern cannot say: an involute gear tooth, a spiral, a lattice, a profile
sampled from a formula. Proven with a real 20-tooth involute spur gear —
43,345 mm³ and 1.6 MB of STEP, built from the involute formula.

A scripted part is an ordinary part from that point on: the script runs in its
own process, hands back STEP, and the kernel imports it, so it can be cut,
filleted, fused and exported like a box. The Document stays the source of truth,
which is what keeps `prototype_edit`, compare, the panel and the repair pass
working.

**Two costs, both real and neither hidden:**

1. **It executes generated code**, and that decision was taken explicitly. OFF
   unless `FORGE_ALLOW_SCRIPTS` is set, so a deployment that cannot accept it
   does not have the feature at all rather than half-having it.

   The sandbox is two layers. An AST whitelist refuses imports, dunder
   attributes, and every NAME not on a list, before anything runs — nine escapes
   are tested by trying them, starting with
   `().__class__.__bases__[0].__subclasses__()`, which is the documented way out
   of a restricted-builtins sandbox and the reason dunders are refused by name
   rather than by blacklisting escapes that are not enumerable. Then a stripped,
   short-lived process: no environment (this one holds a database URL and a
   provider key), a temporary working directory, `python -I`, and CPU, memory,
   file-descriptor and core limits.

   It is defence in depth and NOT a container, and the code says so where
   somebody changing it will read it. **Known gap: macOS cannot set the
   address-space limit and skips it with a warning** — the CPU limit and the
   wall clock still apply, and Linux gets both.
2. **Everything downstream assumes a structured document.** `prototype_edit`,
   compare, the parts panel, the repair round-trip and every fence read
   `geometry.Document`. Generated code must produce the same named parts, or
   those features stop working. The bridge is the design problem, not the
   codegen.

### Stage 4 — Freeform surfaces — **DONE 2026-09-09**

Most of this turned out to already exist, which reading the kernel showed and
guessing would not have. OCCT lofts SMOOTHLY through sections — a surface with
continuous curvature is what `loft` already produced — and the viewport already
draws the kernel's real blended surface rather than an approximation of it. A car
body is a loft through six sections and always was.

What was missing was the ability to SAY which you wanted, so a loft now carries
`ruled`: smooth by default, because that is the reason to loft at all, and
faceted on request for a shape that really is — a hopper, a transition duct —
where a smooth blend would round corners that exist. With guidance that a
sculpted body wants more stations rather than fewer cleverer outlines.

Beyond that, `script` reaches build123d's whole surface API — Spline, Bezier,
make_hull, sweeps with guide rails — which is the answer to "author a control
net" that does not require inventing a way for a model to type control points.

### Stage 5 — The agent runs what it wrote, and keeps fixing it until it builds — **DONE 2026-09-09**

Stages 1–4 raised the ceiling and left one hole under it. Every check in a turn
reads the DOCUMENT — `Faults()` walks outlines, `turned.go` measures,
`look.go` renders — and a scripted part's shape is not in the document. It is
whatever build123d makes when the script runs, and the script did not run until
somebody asked for the solid, after the turn was over.

So the chain from Stage 3 worked to the last link and failed there, invisibly:

| stage | result |
|---|---|
| the model chose `shape: "script"` | ✅ |
| the sandbox accepted it, every builder name resolved, the script ran | ✅ |
| **build123d built the gear** | ❌ `ValueError: A face or sketch must be provided` |

The reader was told the gear was made. The reason it was not arrived later, on an
export, phrased for a person — and never reached the model, which is the only
party that could fix it.

**What was built** (`agent/scriptrepair.go`): the turn runs each script it wrote,
and on a refusal hands the model *one script and the builder's own words* and
asks for a script back. The reply is accepted **only when the kernel builds it**
— so this is the one check in a turn that cannot report a fix that did not work.
Up to 4 rewrites a part, 8 a turn, 4 minutes; it stops early when the same
message comes back twice, because a script is rebuilt from scratch each run and a
repeated message means the same thing is still wrong.

**And the turn was given a budget of its own.** It was bounded by
`RequestTimeout + 15s` — one model call — so Stage 2's 25-minute build could
never finish over HTTP: it was cancelled at 3m15s, mid-stream, and from inside
the turn that is indistinguishable from a model that failed. `FORGE_TURN_BUDGET`
(30m) bounds the turn; each call is still bounded by `FORGE_LLM_REQUEST_TIMEOUT`,
so this allows more calls and never a longer one. The stream's write deadline is
lifted on the one endpoint whose job is to stay open.

**Measured live**, asking for a 20-tooth module-2 spur gear: **2 of 4 runs build**,
kernel-verified, against 0 before — and the two that do not now say so **in the
turn**, with the builder's sentence, instead of on an export hours later.

A second live turn after both fixes landed asked for a 16-tooth m2.5 gear
alongside the first: no shape complaint from the visual check, a *true* overlap
complaint instead, corrected — and both scripts in the stored document still
built when pulled from Postgres and run in the pod (12565.7 and 10176.6 mm³).

**Two defects in this stage were found by the live run and not by reading it:**
the verification did not have the last word (two later repairs hand back whole
new documents), and the visual check reports every scripted part as "a
rectangular block" because the renderer draws it as a bounding box — a false
positive by construction, on a gear whose script had just built 12565.7 mm³. Both
fixed; see
`docs/bugfix/2026-09-09-the-scripts-error-never-reached-the-model.md`.

### Stage 6 — Draw it first, and build against the drawing — **DONE 2026-09-10**

Asked for: FORGE should sketch a 2D image first, analyse it, build the complete
document from it, build the 3D from the document, and keep working until the 3D
matches the 2D. Then: *"maybe not just one image but many from different angles."*

**What was built.** `agent/sketch.go` and `llm/illustrate.go`. Before any
geometry is written, a reference picture is generated from the request, read by
the vision model into a description of its FORM, and that description goes into
the prompt that writes the document. When the solid exists it is compared back
against the same picture, and a difference is a repair.

Generating it AFTER the geometry would make it a picture of what was already
decided, and comparing a model's work against a picture drawn from that same work
is a check that cannot fail.

**Two measurements decided the design, and both came from a spike against the
real generator** (`docs/spikes/2026-09-09-generated-reference-images/`):

| what was asked | what came back |
|---|---|
| a 20-tooth involute spur gear | **28–30 teeth**, petal lobes not involute flanks, **no dimensions at all** |
| front, side and top in three calls | `distinct_viewing_directions: 1` — all three were the front |
| front, side and top in ONE call | same object in every panel, 2 distinct directions, still not a projection |

So the picture is authoritative for **form** and the request stays authoritative
for every **count and dimension** — otherwise "keep working until the 3D matches
the 2D" repairs a correct kernel-verified gear into a 28-tooth petal shape. And
it is **one call carrying several panels**, never one call per angle: three calls
buy three times the cost, one viewing direction and three different objects.

**The pattern this stage is really about.** Four times a rule was stated in a
prompt and not obeyed, and each one needed a deterministic guard behind it:

| the prompt said | what came back | what enforces it now |
|---|---|---|
| "Do NOT report any number" | *"**Two** circular holes are drilled…"* | `withoutNumbers` strips them |
| — the same, spelled out | *"**twenty-eight** teeth"* | hyphen-split word match |
| "Do NOT comment on…" a solid tool | *"protruding **pins or pegs** sticking out"* | the render's cut tools are named |
| "Do NOT count anything" | *"three bolt holes … only **two**"* | `formOnly` discards it |

A prompt states an intention; a filter is a guarantee. Every one of those was
found by running it, not by reading it.

**Measured live, end to end:** against an L-bracket that matches the drawing,
**0 problems**; against a sphere built for the same request, **1**, correctly
describing total absence. Passing the right model matters as much as catching the
wrong one — a checker that complains about a correct model drives repairs that
damage it, and this repository has already deleted one rule for exactly that.

The live test's own fixture had to be corrected **four times**, and the checker
was right every time: it found the missing bolt holes, then the missing slot,
then the pegs, then a count it should not have reported. That is the strongest
evidence here that the loop works.

**Off unless `FORGE_LLM_IMAGE_MODEL` is set.** It costs 30–60s per geometry turn
plus two vision calls, and a deployment that will not pay that has the feature
absent rather than slow — the same discipline the CAD kernel and vision follow.

## What is NOT promised

- The reference image. See the framing above.
- That Stage 3 lands without the security decision in it being made explicitly.
- That Stage 4 works. It is the one stage where the approach is not yet known.
