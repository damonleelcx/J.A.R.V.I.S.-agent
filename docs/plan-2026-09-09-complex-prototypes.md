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

### Stage 2 — Build across turns instead of all at once

An agent loop that decomposes a model into subsystems and builds them one at a
time, verifying each before the next.

- `prototype_edit` already exists and already guarantees that what an edit does
  not mention cannot be lost — it is the right mechanism and needs no change.
- `planner.go` already decomposes goals into tasks; this extends the same idea to
  geometry rather than inventing a second planner.

**Done when:** part count reachable without loss is measured, against the 6–13
that a single reply manages today.

### Stage 3 — The model writes CAD code

Instead of a JSON parts list, the model emits build123d Python, which the sidecar
already runs.

This is the stage that changes the ceiling by orders of magnitude: loops,
variables, real sketch-and-feature workflow, fillets on selected edges. A
60-spoke wheel becomes a `for` loop.

**Two costs, both real and neither hidden:**

1. **It executes generated code.** The sidecar today runs a fixed program over
   data. Stage 3 makes it run text a model wrote. That is a security boundary and
   needs an explicit decision before it is built, not after — restricted
   builtins, no imports, no filesystem, wall-clock and memory caps, and a
   sandbox that is argued for rather than assumed.
2. **Everything downstream assumes a structured document.** `prototype_edit`,
   compare, the parts panel, the repair round-trip and every fence read
   `geometry.Document`. Generated code must produce the same named parts, or
   those features stop working. The bridge is the design problem, not the
   codegen.

### Stage 4 — Freeform surfaces

NURBS and subdivision surfaces, so a sculpted body is expressible at all.
OpenCASCADE supports them; getting a model to author control nets does not have a
known good answer, so this stage carries research risk the others do not, and is
last for that reason.

## What is NOT promised

- The reference image. See the framing above.
- That Stage 3 lands without the security decision in it being made explicitly.
- That Stage 4 works. It is the one stage where the approach is not yet known.
