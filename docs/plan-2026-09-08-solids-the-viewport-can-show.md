# Solids the viewport can show, and a loft to shape them with

**Status:** EXECUTED, all four stages, 2026-09-08. Written after "the 3d object
is trash — needs to be more complex and accurate", and the question "can you use
only 3d triangles but millions of them?". Outcomes are recorded at the end.

## The answer to the triangle question

Yes — and that is already what happens, one step later than you'd think.

Triangles are the right thing to *draw* and the wrong thing to *author*. A
million triangles is ~9M floats: three orders of magnitude past any context
window, and a language model has no mechanism for placing vertices coherently.
What it can emit is a **construction program** — a few hundred tokens — which
OpenCASCADE then tessellates into as many triangles as the screen deserves.

So the instinct is right and the producer is wrong. The triangles should come
from the kernel, not the model.

## A correction to my first reading

I told the user this build had **no booleans**. That was wrong, and it was wrong
for an avoidable reason: I grepped for `union` / `subtract` / `intersect`, and
this vocabulary calls them **`cut`** and **`fuse`**. They have existed since the
features wave, they are performed by the CAD kernel, and `feature.go` reasons
carefully about why a hole is an existing part used as a tool rather than a
shape of its own.

Only one of my two claims survived: **`loft` genuinely does not exist** (0
references).

## What is actually wrong, in the code's own words

`feature.go`, on the cost of the design it chose:

> The cost is that a tool part is DRAWN by the renderer, which has no boolean
> operations: on screen the four holes are four small cylinders standing in the
> plate rather than voids through it. That is a real divergence and it is
> labelled rather than hidden.

That is the defect behind "the 3d object is trash". **The viewport never shows
the solid the kernel builds.** It draws raw primitives and appends a note saying
what it could not show. So:

- a bolt hole is a cylinder poking through a plate, not a hole;
- a fillet is invisible;
- a `fuse` of two bodies is two bodies;
- and the STEP file exported from the same document is *correct* — the divergence
  is only on screen, which is the one place a person judges the result.

A model that authored a perfect parametric bracket would still look like a pile
of blocks. The sports car is that failure plus a second one: the model reached
for `box` when `sweep` and `revolve` were already available.

## Plan

Each stage closes on its own tests and leaves the product working.

### Stage 1 — the kernel can hand back a mesh

`sidecar.py` already builds the real solid (`_apply` runs cut/fuse/fillet/chamfer
via build123d) and can only `export_step`. Add a `mesh` format that tessellates
the built assembly and returns triangles, and `Kernel.BuildMesh` in Go beside
`BuildDocument`.

*Closes when:* a plate with a `cut` tessellates to a mesh whose volume is less
than the plate's, and a filleted box has more distinct face normals than an
unfilleted one — both against the real kernel, no fake.

### Stage 2 — the viewport draws that mesh

`GET /v1/geometry/{id}/mesh`, and the studio prefers it when the deployment has
a kernel. Primitives remain the fallback, unchanged, for deployments without
Python — and `FeatureNotes` then says what it always said. Where the kernel IS
present the note disappears, because the divergence it describes no longer
exists.

*Closes when:* the same document renders with a hole through it; and with the
kernel disabled, the old primitive path and its note are byte-for-byte what they
are today.

### Stage 3 — `loft`

A new shape: two or more profiles at positions along an axis, blended. This is
the one thing missing that no combination of existing shapes can fake, and it is
what a car body, a hull, a blade or a bottle actually is.

*Closes when:* a two-profile loft builds in the kernel, tessellates, round-trips
through STEP, and its measured extents match the profiles that defined it.

### Stage 4 — make the model use any of it

The vocabulary was already richer than what the model reaches for. This is a
prompt-and-eval stage, measured by the existing suite rather than by opinion:
the fraction of prototypes using a shape other than box/cylinder, and the
fraction with any feature at all.

## Risks, stated

- **A deployment without Python must not regress.** Every stage keeps the
  primitive path intact and reachable; the kernel stays absent-by-default and
  absent loudly.
- **Mesh size on the wire.** A dense tessellation of a large assembly is
  megabytes. Stage 2 needs a deflection parameter and a cap, and should say when
  it simplified rather than silently coarsening.
- **Latency.** The kernel costs ~2.5 s to start and ~46 ms to build. Tessellation
  is on the same process; the mesh request must not block first paint — the
  primitives draw first and the mesh replaces them.
- **STEP is untouched.** Stage 1 adds a format; it does not change the exporter.

## What this deliberately does not do

Generate meshes with a model. A triangle soup cannot be dimensioned,
parametrised, re-derived or verified — and this product's entire frame
(*assumptions*, *not verified*, `wheelbase = 2700`, "Export STEP") depends on the
shape being a program. A prettier mesh would read as more trustworthy while
being less analysable, which is the direction this product exists not to go.


## Outcome

All four stages landed and were verified against the real kernel and a real
browser, not only in tests.

### Stage 1 — the kernel hands back a mesh

`sidecar.py` gained a `mesh` format: it tessellates the built assembly per
surviving solid and attributes each mesh to the part it came from. `BuildMesh`
sits beside `BuildDocument` and takes the same path, so the picture and the STEP
file cannot disagree.

The tolerance is chosen from the model's own size (`span / 2000`) rather than a
constant — 0.1 mm is invisible on a bracket and catastrophic on a car body — and
is searched against a 400,000-triangle budget, coarsening rather than truncating,
and REPORTING that it did.

*Closed by:* `TestKernel_ReturnsTheSurfaceOfTheSolidItBuilt` (every index
addresses a real vertex) and `TestKernel_ACutLeavesAVoidAndConsumesItsTool` —
the drilled plate has more triangles and less volume than the plain one, and the
tool is not a body.

### Stage 2 — the viewport draws it

`GET /v1/geometry/{id}/mesh`, and the studio prefers it. Normals are accumulated
per vertex, which yields FLAT shading for free because OCCT tessellates per face
(a box arrives as 24 vertices, not 8) — a crease stays a crease, which is the one
thing a CAD viewport must not lose.

Two things had to be handled honestly:

- **WebGL 1 indexes with an unsigned short.** `OES_element_index_uint` is
  requested and the index width is chosen per part; where the extension is
  missing and a body exceeds 65,535 vertices the primitive is drawn instead and
  says so. Truncating would draw a shape nobody built.
- **The primitives are drawn first and replaced.** The kernel can be absent and
  costs a round trip when it is not, so the shape appears exactly when it always
  did and becomes true a moment later.

*Verified live:* a plate with a `cut` renders with a real bore through it —
previously a red cylinder standing in a plate.

### Stage 3 — `loft`

Implemented as an OPERATION over parts, not a shape carrying its own outlines,
following the reasoning `feature.go` already gives for holes. A new shape
`section` is a drawing with no thickness; `loft` blends the target into the
sections it names, in the order it names them, and consumes them.

The payoff of that choice: sections are ordinary parts, so arcs, corner radii,
holes, parameter binding, self-intersection checks and unit conversion all work
on them already, with no second and weaker set of rules to drift.

One defect found while building it: **a loft of coplanar stations succeeds and
encloses nothing.** OCCT does not refuse it — it returns a shape with no volume
that exports as an empty solid and tessellates to nothing. It is now caught with
a message naming the cause (stations are separated along local Z, because a
section lies in its own XY plane).

*Closed by:* `TestKernel_ALoftBlendsItsStationsAndConsumesThem` — a 20→8 frustum
over 30 mm must come back as ONE body of 6240 mm³, which is
`h/3·(A₁+A₂+√(A₁A₂))` exactly — and `TestKernel_ASectionHasNoVolumeOfItsOwn`.

*Verified live:* a transition duct, rectangular at one end and round at the
other, rendered as one blended solid.

### Stage 4 — make the model use it

The prompt gained `section`, `loft`, and the instruction the whole exercise was
about:

> Reach for the shape that describes the thing, not the one that is easiest to
> type. A box is a box; it is not a car body, a bracket, a hull or a housing. …
> calling it "low-poly" or "conceptual" in the note does not make the geometry
> say what you meant.

Measured rather than asserted: `aChangingSectionIsLofted` and the
`draws-a-body-whose-section-changes` case (a round-to-rectangular transition
duct — neither end is the other and no primitive spans them). Tracked with no
floor, because the capability is new and the first runs establish the baseline.

### What is still true

The answer to the original question stands: **the triangles come from the
kernel, not from the model.** A million-triangle mesh is ~9M floats and no
language model can place them coherently — but a few hundred tokens of
construction program tessellates to as many as the screen deserves, and stays
dimensionable, parametric and exportable as STEP. That is the whole trade this
plan was built around.
