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

**And the same blind spot was found and fixed in `look.go`**, where it had been
live. `Tessellate` does not perform a cut, so a bolt hole is drawn as a solid
cylinder inside the plate — which is question 1 of `lookSystem` word for word,
"a part completely hidden inside another part". Confirmed against the real vision
model before fixing:

> Bolt Hole: The part is a solid cylinder protruding from the plate surface
> rather than a hole passing through it.

True about the picture, false about the model, firing on every mechanical part
with a hole in it. The fix names the cutting tools and says a solid inside the
part it cuts is what a correct hole looks like — but **deliberately keeps
question 2 on for them**: a tool floating clear of what it cuts removes nothing,
and that is a real defect only this check would notice. Both halves are measured
live: the hole that is cut correctly now reports **nothing**, and a tool 300 mm
above the plate reports *"floating high above the plate … so it does not
intersect the material to cut a hole."* Suppressing the noise must not take the
signal with it, and two drills hold that pair apart.

**Off unless `FORGE_LLM_IMAGE_MODEL` is set.** It costs 30–60s per geometry turn
plus two vision calls, and a deployment that will not pay that has the feature
absent rather than slow — the same discipline the CAD kernel and vision follow.

### Stage 7 — Look at the solid that was built, not the one that was described — **DONE 2026-09-10**

Two blind spots of the same shape had each been patched by APOLOGISING to the
vision model for the picture:

| what the checks saw | what it really was |
|---|---|
| *"a solid cylinder protruding from the plate surface rather than a hole"* | a correct plate with a bolt hole |
| *"a rectangular block rather than a circular gear with teeth and a bore"* | a gear whose script had built 12565.7 mm³ |

Both because `geometry.Tessellate` is a triangle builder: it performs no boolean
and runs no script, and says so in its own inferences. The apologies worked and
did not scale — one sentence per blind spot, each one a rule the model may
ignore, and a third blind spot would have needed a third.

**The kernel already builds the real surface.** `BuildMesh` returns triangles
with the features applied and the scripts run. What was missing was a door
between them: `ContactSheet` derived its groups internally, so nothing else could
be drawn. It is now split — `ContactSheetOf` takes already-built surfaces, and
`ContactSheet` is a thin wrapper over the tessellator — so both pictures go
through the same rasterizer, in the same colours, from the same four viewpoints,
and only their SOURCE differs.

**Measured live, same document, same question, same model, two pictures:**

| picture | hole through plate | solid post on plate |
|---|---|---|
| described | **false** | **true** |
| kernel | **true** | **false** |

The apology is not deleted — it is now conditional, shared by both checks in one
place, and emitted only for the fallback render. That mattered enough to fence:
telling a checker "a solid where a hole should be is correct here" about a
picture in which holes are real would teach it to ignore a hole that genuinely
failed to cut.

**One render per turn**, not one per check: building the surface runs the kernel
and, for a scripted part, the script. Each check re-draws it after a repair it
accepts, so nothing downstream compares against a document that no longer exists.

### Stage 8 — Tell it how the builder is called — **DONE 2026-09-10**

Once "Did you mean Rotation, Rot?" fixed the NAMES, what remained across the live
gear runs was the model guessing at the API behind a name that exists:

```
TypeError: BuildSketch.__init__() got an unexpected keyword argument 'local_mode'
Standard_TypeMismatch: TopoDS::Face
```

The model has the 209 names it may use and **none of their signatures**, so the
repair loop spent its budget re-guessing at an API rather than fixing geometry.
"X is not available here" cannot help when X is available.

**Where the signature comes from decides whether it is right.** It is produced in
the sandbox, at the moment of failure, from the build123d that is actually
installed. A list generated at build time would be a second artifact to keep in
step with the library, and a wrong signature is worse than none because it reads
as authoritative.

Three sources, in order: the builder the exception **names**
(`BuildSketch.__init__() got …`), the builders **called on the failing line**
(which is the only thing that points back at Python when OCCT blames a C++ type),
and the names a **"did you mean" already suggested** — because fixing the name
and then guessing the call is the same failure one step later.

```
TypeError: BuildSketch.__init__() got an unexpected keyword argument 'local_mode'
  The builders on that line take:
  BuildSketch(*workplanes: 'Face | Plane | Location', mode: 'Mode' = <Mode.ADD>).

line 1: Cylindr is not available here. Did you mean Cylinder? … They take:
  Cylinder(radius: 'float', height: 'float', arc_size: 'float' = 360, …).
```

**And a refusal now names what was WRITTEN, not what the parser calls it.** A
live run reached for build123d's own `@` idiom and was told *"MatMult is not
allowed here"* — the parser's word, which the author never typed and cannot act
on. It says ``the `@` operator`` now.

**Measured, 20-tooth module-2 spur gear:**

| | builds |
|---|---|
| before | 3 of 8 |
| after | **8 of 12** (two independent batches of 4 of 6) |

⚠️ At that n this is suggestive, not significant. The stronger evidence is that
the failures **changed in kind**: no run since has failed on a wrong keyword or
an unknown name. What is left is `Standard_TypeMismatch` and *"finished without
assigning `result`"* — the model's own CAD logic and its own contract-following,
which is where the boundary belongs.

### Stage 8b — `@`, and the right name in the wrong case — **DONE 2026-09-10**

**`@` is allowed.** `ast.MatMult` was absent while every other binary operator was
present, and all of them dispatch to a dunder method — `Add` to `__add__`, `Mod`
to `__mod__`. `MatMult` dispatches to `__matmul__` and is not different in kind:
it binds no name, reaches no module, and cannot produce an object the script
could not already hold.

Its sibling was already allowed, which is the point. In build123d:

```
edge @ 0.5   is the point half way along
edge % 0.5   is the tangent there
```

`%` is `ast.Mod` and has worked since the sandbox was written. Refusing the other
half of a documented pair was arbitrary, and it cost a real run.

The price is paid in fences: the documented escape is tried **through** an `@`
expression, and `e.__matmul__(0.5)` — the dunder the operator dispatches to,
written by name — is still refused.

**And a suggestion now matches case-insensitively.** Getting the case wrong is
its own common miss — `polyline` for `Polyline`, `BOX` for `Box` — and a
case-sensitive comparison scores those no better than a typo. Measured against
this manifest, folding adds `BOX -> Box` and costs nothing: the names that must
suggest nothing (`urlopen`, `getattr`, `socket`, `exec`) still suggest nothing.

**And a suggestion must share the START of the typed name** — which is the rule
that actually works, and no cutoff does. Measured against this manifest:

| typed | suggested | score |
|---|---|---|
| `Rotate` | `Rotation` | **0.714** — the suggestion this feature exists for |
| `module` | `Mode` | 0.800 — nonsense: a gear term and an enum |
| `thickness` | `thicken` | 0.750 — nonsense: a parameter and an operation |
| `input` | `int` | 0.750 — nonsense |

The one that matters scores **lower** than the three that mislead, so any
threshold keeping it keeps them. A misspelling preserves the start of a word —
`Cylindr`, `polyline`, `BOX`, `sqrtt`, `make_facee` all do — and a wrong word does
not. Requiring 70% of the typed name to match as a prefix separates the two sets
completely, and drops the `input -> int` junk the cutoff alone had let through.

`module -> Mode` was **live**: a model wrote `m = module`, reaching for the gear
parameter, and was pointed at build123d's `Mode` enum.

**What string distance cannot reach.** A live run asked for `polarArray`; the real
name is `PolarLocations`, and no edit distance bridges those. That is a semantic
gap, not a spelling one, and nothing here closes it.

### Stage 9 — Degree trigonometry, and parameters a script can read — **DONE 2026-09-10**

**Trigonometry in expressions, with the convention in the name.** `sin_deg`,
`cos_deg`, `tan_deg`, `atan2_deg`. The old absence was a documented decision —
half the world writes sine in degrees and half in radians, they agree only at
zero, and the wrong one gives a plausible number rather than an error — and that
argument is answered by naming rather than ignored. The bare spellings are still
refused, and a fence holds them out: offering `cos` beside `cos_deg` hands the
ambiguity straight back.

Two things it needed beyond the table:

- `tan_deg(90)` returns 1.6e16 in Go rather than an error, and a document quietly
  carrying 1.6e16 mm would draw a part the size of the solar system. Refused by
  name.
- ‼️ **A trig function eats its argument's unit.** Without that, adding `cos_deg`
  MOVES the failure instead of removing it: `pitch_radius * cos_deg(pressure_angle)`
  reads mm and deg, and the value was refused for *"mixes units: deg and mm"*.
  The fence caught it on the first run. Dependency edges are untouched, so a
  standards claim still propagates — `pressure_angle` is still something
  `base_radius` depends on, it just does not lend it a unit.

The contract's function list is now **substituted from geometry's own table**. The
two were separate strings, and the contract went on saying *"There is no sine or
cosine here"* — a rule the model reads, describing a grammar that had changed
underneath it, which is worse than no rule at all.

**Parameters are in a script's scope — and deliberately not announced.**

| | gears that build |
|---|---|
| parameters absent (control) | **6 of 10** |
| parameters present and ANNOUNCED | **0 of 10** |
| parameters present, contract silent | **4 of 10** |

All three measured against the live endpoint, and the control re-run in the same
hour as the others so drift could not explain it. Announcing them is what does
the damage: told its parameters are available, the model commits to a parametric
document and writes a script leaning on it, and those fail on build123d usage
where the literal script it writes otherwise builds.

This is not a hidden capability. The model already believes it has this — the
first live run of the script path wrote `m = module`, `t = teeth_count`
unprompted and every name was refused. What changed is that the assumption is now
true instead of an error. Saying it out loud turns a safety net into an
invitation, and the invitation measures worse than silence.

Lengths arrive in millimetres (a `cm` parameter injected raw builds a part ten
times too small, silently, in the one shape whose dimensions nobody can read),
whole numbers arrive as integers (the first version handed everything over as
floats and `range(teeth_count)` raised in 6 of 9 runs), a builder always wins a
name collision, and a name starting with `_` never becomes a name.

### Stage 10 — Answer the USAGE question, not the spelling one — **DONE 2026-09-10**

With names and signatures answered, what remained was build123d usage. Two kinds,
both measured live and both now answered from the installed library at the moment
of failure:

- **`with Rotation(...)`** → *"'Rotation' object does not support the context
  manager protocol"*. Its signature — `Rotation(*args, **kwargs)` — answers
  nothing about that. It now says: *"That is not something you can use `with`.
  The ones you can are: BuildLine, BuildPart, BuildSketch, Locations,
  PolarLocations, …"*
- **bare `rotate(b, 45)`** → refused, and the closest global name is `Rotation`,
  a Location, so the spelling suggestion sent the repair somewhere useless.
  `rotate` is real — it is `shape.rotate(...)`. The suggestion is now
  **suppressed** and replaced by *"rotate is not a function here, but it IS a
  method on Matrix or Shape — write shape.rotate(...)"*.

The defining class is reported rather than the classes that merely have it: the
first version answered *"a method on Airfoil and ArcArcTangentArc"* —
alphabetically-first leaves inheriting from Shape. True and useless.

**Measured: 5 of 10, against a 6 of 10 control.** No movement in the rate at this
n, and both targeted failure modes are gone from the sample. What is left is the
model reaching outside the sandbox (`globals()`, correctly refused) and genuine
involute-geometry mistakes — `Standard_TypeMismatch`, `BRep_API: command not
done`. Those are its CAD competence, which no error message will fix.

⚠️ **The plateau is real.** Names, signatures, operators, usage and parameters
have all been answered, and the rate has not moved off roughly six in ten. The
one lever tried that was qualitatively different — telling the model more in the
contract — measured 0 of 10. Whatever comes next should be measured before it is
believed.

### The gap this closed

Four of ten live runs failed, and the most informative one wrote:

```python
m = module
t = teeth_count
pa = pressure_angle_deg * math.pi / 180
thick = thickness
```

Every one of those is a **parameter of the document the script belongs to** — the
panel shows them, `geometry.Parameters` resolves them, and the script's namespace
has none of them. The model assumes they are in scope because they are part of
the same part, and it is not an unreasonable assumption.

Nothing here closes that, and it is not a spelling problem: no suggester can turn
`module` into a number the document already holds. It needs the parameters put
into the script namespace, which is a change to what a script can SEE — a contract
decision, not a bug fix, and it is not taken here.

## What is NOT promised

- The reference image. See the framing above.
- That Stage 3 lands without the security decision in it being made explicitly.
- That Stage 4 works. It is the one stage where the approach is not yet known.
