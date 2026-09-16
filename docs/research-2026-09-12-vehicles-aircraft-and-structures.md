# Cars, planes, rockets, houses, bridges: what it would take

**Status: research. Nothing here is decided and nothing here is built.**

**Measured since writing, 2026-09-12:** a live car build was run before anything
was built against these walls — 28 parts, builds in the real kernel, 151,638
tokens. It confirmed walls B, C, F and H, **corrected wall A**, and did not reach
D or E. Numbers and the correction are inlined below; the full run is
[`docs/spikes/2026-09-12-car-ceiling/`](spikes/2026-09-12-car-ceiling/README.md).
Written 2026-09-12, after Stage 11 (`docs/plan-2026-09-09-complex-prototypes.md`)
landed the `"gear"` shape and the build loop reached 60 parts in 25 minutes.

**Asked for:** FORGE still cannot produce a car, a plane, a rocket, a house or a
bridge. What is needed to build more complex and more accurate prototypes?

**Also asked, mid-research:** *would Blender be useful?* Answered in its own
section, and the short version is **for pictures yes, for geometry no** — with
the reasoning and the two things that would be better spent instead.

---

## First, what is actually here today

Read out of the code rather than remembered, because the last plan's most useful
habit was checking:

| | what exists | where |
|---|---|---|
| shapes | `box`, `cylinder`, `cone`, `sphere`, `plane`, `extrusion`, `revolve`, `sweep`, `section`, `gear`, `script` — **11** | `agent/converse.go` contract, `cad/sidecar.py` `_shape` |
| features | `cut`, `fuse`, `fillet`, `chamfer`, `loft` — **5** | `geometry/feature.go:92-112` |
| patterns | `repeat`: linear offset, or polar about x/y/z. Cap **512** | `geometry/repeat.go:139` |
| structure | a **flat list** of parts, each with an absolute `position` and `rotation` in the assembly frame | `geometry/document.go` |
| build | plan of **2–10** steps, budget **12**, turn budget **30m** | `agent/assemble.go:50`, `config.go:756` |
| escape hatch | `script` — **209** build123d names, off unless `FORGE_ALLOW_SCRIPTS` | `cad/builders.txt`, `cad/script.go:51` |
| export | `obj`, `stl`, `step` | `geometry/export.go:74` |
| checks | faults, measurement, one vision pass over a 4-view contact sheet | `geometry/faults.go`, `agent/turned.go`, `agent/look.go` |
| **not** here | interference, clearance, kinematics — stated in the code, not inferred | `geometry/assembly.go:44` |

The script manifest already contains `mirror`, `offset`, `thicken`, `Shell`,
`loft`, `sweep`, `Spline`, `Bezier`, `make_hull`, `Helix`, `Airfoil`, `draft`,
`section`, `split`, `project`, and the whole `Joint` family. **The kernel is not
the thing that is missing.** Everything below is about what the *document* can
say and what the *agent* can be relied on to write.

---

## The five objects, and what each one actually demands

Not "a car is hard". What specifically does each need that the eleven shapes
cannot say?

| | car | plane | rocket | house | bridge |
|---|---|---|---|---|---|
| mirror symmetry about one plane | ●●● | ●●● | — | ● | ●● |
| a thin wall / skin over a volume | ●●● | ●●● | ●●● | ●●● | ●● |
| standard structural sections (I, C, RHS, rebar) | ●● | ●● | ● | ●●● | ●●● |
| members placed **along a curve** | ● | ●●● | ● | ●● | ●●● |
| a sculpted outer surface through stations | ●●● | ●●● | ●● | — | — |
| repeated **sub-assemblies**, not repeated solids | ●●● | ●●● | ●● | ●●● | ●●● |
| specified-by-numbers shapes (airfoil, dome, nozzle) | ● | ●●● | ●●● | — | — |
| hundreds of parts in one model | ●●● | ●●● | ●● | ●●● | ●●● |

Reading down the columns gives five domains. Reading **across the rows** gives
something much more useful: six of the eight rows are the same few missing ideas
serving every column at once. That is where the work is.

---

## The walls, in the order they bite

### Wall A — the document has no assembly tree

`Part` has `position`, `rotation` and no parent (`geometry/document.go:76-168`).
Every one of a car's parts is placed in **global** coordinates by the model, in a
single flat list.

That is exactly the failure Stage 1's vision check keeps catching: *"the hub
flange and brake assembly are floating in the air, completely detached from the
chassis rails"*, *"wheels buried inside a 4.5 m-wide body"*. Those are not
reasoning failures. They are what arithmetic-in-your-head looks like when a
front-left wheel's position is `body_x + wheelbase/2 − track/2 − hub_offset` and
the model must type the answer rather than the expression.

‼️ **Measured 2026-09-12, and this framing was half wrong.** 21 of 28 positions in
the live build were BOUND to expressions (`position_from`), not typed as numbers.
The model does the arithmetic properly. The wall is real but it is not arithmetic
— it is that there is nothing to attach *to*. Three of the five productive steps
ended with a part floating in space, and all three were parts whose job is to
CONNECT two others:

> *"The damper is floating in the air … not connected to the chassis or the
> control arm"* · *"the Left Front Brake Line … not physically connected to
> either"* (could not be corrected) · *"the Left Tie Rod … a thin line floating
> in space"*

An expression over global parameters can put a damper at a coordinate. It cannot
say "this end is on the control arm". That is what a frame — or a joint — buys.

**This is the highest-leverage single change in this document**, and it is a
change to a shipped contract — `careful-api-creation` applies, so it is a
decision, not a task.

### Wall B — nothing can say "the other side"

`repeat` does linear and polar only (`geometry/repeat.go`). A car, a plane and a
bridge are all mirror-symmetric about one plane, and `mirror` is already in the
script manifest and unreachable from the document.

Today symmetry costs double the parts, double the tokens, and double the chances
of the left-hand side drifting from the right. It is also the cheapest thing on
this list to add.

**Measured: 18 of 28 parts (64%) were authored twice** as left/right pairs.
`CAR-SYMMETRY mirror_pairs=9`.

### Wall C — nothing is hollow

Every one of the five objects is a **thin-walled shell**: a body panel, a
fuselage skin, a tank wall, a house wall, a box girder. The vocabulary has no
`shell`, no `offset`, no `thicken` — all three are in the manifest and reachable
only from `script`, which is off by default and builds roughly six in ten.

A solid car body is not a wrong-looking car. It is a car that weighs 4 tonnes and
whose every downstream number is meaningless.

**Measured, and worse than stated: `parts_with_holes=0 cut_features=0` of 28.**
Not one part has a wall, a bore or a void, and all seven features in the build
were `fuse`. The solid came out at 0.53 m³.

### Wall D — members cannot follow a curve

A bridge deck follows an alignment. Hangers hang from a catenary at stations
along it. Fuselage frames sit at stations along a datum line. Ribs sit along a
spar. `sweep` carries a *section* along a path; nothing places *parts* along one,
and `repeat` cannot curve.

### Wall E — every section is hand-typed

An I-beam, a channel, an RHS, a rebar cage, a NACA aerofoil: today each is a
polyline the model types point by point. There is no catalogue, so there is
nothing to check it against — a 203×133×25 UB whose flange is 2 mm wrong looks
exactly like one that is right, in the viewport and in the contact sheet both.

This is the same shape of problem the `"gear"` stage solved, and it solved it the
same way: **stop asking the model to derive what is specified.**

### Wall F — verification does not scale past about sixty parts

One contact sheet, four views, one vision call per turn. At 60 parts that worked
and was measured. At 400 parts a single sheet is a grey smudge, and no prompt
fixes that.

Worse, the checks that *would* matter at that scale do not exist at all:
interference, clearance, mass and centre of gravity, envelope. `assembly.go:44`
says so in plain words. A 400-part car with two parts occupying the same cubic
centimetre is a defect **no current check can see**, and it is not the kind of
defect a vision model catches — it is inside the solid.

**Measured at 28 parts, not 400** — `document_faults=0`, kernel `builds=yes`,
vision passed, and:

```
master-cylinder is 100% inside engine-block
left-upright    is  70% inside left-front-rotor
```

This is the run's most important result. A model that builds, reports no faults
and passes the visual check, with the master cylinder entirely inside the engine
block, is the exact failure this product exists to refuse. Nothing lied — nothing
asked.

**✅ Half of this wall is now closed (2026-09-12).** Something asks now:
`geometry/interference.go` + `cad/sidecar.py` compute shared material between the
solids that SURVIVE the features, and `agent/interference.go` reports it in the
turn and repairs the buried cases. It costs no model call — the numbers come from
the kernel build the render already runs.

Computing it on the kept solids is the whole design, and it is Stage 7's rule
once more. A bounding-box test over the document (which is what the measurement
used) reports **every bolt hole** as an interference, because the tessellator
performs no boolean and the cut tool is still a solid cylinder standing in the
plate — the exact false positive `look.go` had to be taught to ignore. It also
reports a part inside a hollow case, where the true answer is zero. Both are
fenced by trying them: `TestKernel_ACutToolIsNotAnInterference` and
`TestKernel_APartInsideAHollowEnclosureIsNotAnInterference`.

It costs no model call and, measured, no meaningful time either: the broad phase
means a 28-part car pays for 19 booleans and 0.047 s, and 120 parts pays 0.661 s
— against turns of forty to a hundred seconds. Six mutation drills under
"Interference" in `scripts/drill-fences.sh` prove each fence can go red.

**What is still open in this wall:** mass, centre of gravity and envelope are
still not computed, and the contact sheet still does not scale past roughly sixty
parts. And `BuriedFraction` — the line between "report it" and "rewrite it" — was
chosen **without live data** and needs the distribution from the next build.

### Wall G — the build budget is an afternoon, not a project

`assemblyBudget = 12` steps, 2–10 planned, 30-minute turn. The measured 60 parts
took 25 of those 30 minutes. A car of the complexity actually being asked for is
several hundred parts and several hours.

**Measured: the live build planned 7 steps and step 6 was "Wheels and Tires".** It
was refused by the loop's own guard — a pass that raises the fault count is
dropped and the previous state kept, which is correct behaviour — so the run
finished as a sports car with no wheels, no tyres and no body. 28 parts, 9.3
minutes, ~30k tokens per step.

FORGE's whole architecture is the answer to this and it is not currently pointed
at it: *"a model wakes up, rebuilds its state from a database, does a bounded
piece of work, saves the result, and safely continues later."* A build should be
a **durable job with a resumable plan**, not one HTTP turn — the worker already
exists (`cmd/forge-worker`, `agent/worker.go`).

### Wall H — the script path plateaued at ~6/10, and Stage 11 showed the way past it

Stage 10 recorded it honestly: names, signatures, operators, usage and parameters
were all answered and the rate did not move off roughly six in ten. The one lever
that *did* move it was Stage 11's, and it moved it a long way:

| | builds as asked |
|---|---|
| ask the model to derive an involute in a script | 0 of 10 through the export, 6 of 10 as a script |
| give it a `"gear"` shape and four numbers | **10 of 10** |

**Measured 2026-09-12: `CAR-SHAPES box=13 cylinder=11 extrusion=3 gear=1`.** Zero
lofts, revolves, sweeps, sections or scripts; zero fillets, zero chamfers. And
`gear=1` — the Stage 11 shape was reached for unprompted, inside a build about
something else. **A named shape in the vocabulary gets used**, which is the
strongest available evidence that this lever generalises.

**That result is the strategy for everything below.** Where a thing is *specified
by engineering numbers*, FORGE should draw it. Where it is genuinely freeform, a
script is right. The five objects are mostly the first kind and everybody assumes
they are the second.

---

## What to build — the shared half first

Six of the eight demand rows are served by **five additions** that are not
domain-specific at all. This is the part with the best ROI by a wide margin,
because one implementation serves a bridge, a house frame, a car chassis and an
airframe alike.

| # | addition | serves | kernel already has it |
|---|---|---|---|
| 1 | **frame / instance** — a part may sit in a named frame; a frame may be placed, repeated and mirrored | all five | placement is already a matrix |
| 2 | **mirror** — as a feature, or as a `repeat` kind | car, plane, bridge, house | `mirror` |
| 3 | **shell / thickness** — hollow a solid, or give a surface a wall | all five | `offset`, `thicken`, `Shell` |
| 4 | **section catalogue** — named standard profiles (I/UB/UC, C, RHS/SHS, angle, pipe, rebar, NACA 4-digit) resolving to an outline, exactly as `gear.go` resolves teeth | house, bridge, plane, car | outlines already build |
| 5 | **member / alignment** — a section swept along a named curve, and parts placed at stations along it | bridge, plane, house, rocket | `sweep`, `Spline`, `Helix` |

Note what #4 and #5 have in common with `gear.go`: they **expand into the
existing vocabulary before anything reads the part**, so the kernel, the
tessellator, `Faults`, the contact sheet, measurement, `prototype_edit` and the
repair loop all keep working unchanged. That is the pattern the gear stage
established and it is the reason it cost one file plus a browser mirror.

### Then the domain half

Only after the shared five, and each one is a *specified-by-numbers* shape in the
`"gear"` mould:

- **Aerospace** — `airfoil` (NACA series, chord, thickness, twist), `wing` (loft
  through airfoil stations with sweep, dihedral, taper), `fuselage` (loft through
  stations), `tank_dome` (ellipsoidal / torispherical), `nozzle` (bell contour).
- **Automotive** — mostly falls out of the shared five plus lofts. A concept body
  is a loft through 6–10 sections, mirrored; a wheel is a revolve plus a polar
  repeat; a chassis is members along an alignment. Stage 4 already established
  that `loft` blends smoothly and always did.
- **Architecture** — `wall`, `slab`, `column`, `beam`, `opening`, `storey`,
  `grid`. Cheap: each is an extrusion of a section along a line. Their value is
  not geometric, it is **semantic** — a wall that knows it is a wall is what makes
  an IFC export, a schedule, or a quantity take-off possible later.
- **Civil / bridge** — `alignment` (curve with stationing), `girder` (section
  along alignment), `cable` (catenary), `pier`, `deck`. All of these are #5 with
  a name.

---

## Should FORGE use somebody else's parametric engine?

Three candidates came up in the research and they are not equivalent.

**OpenVSP** (NASA, open source, Python API, exports STEP/IGES/STL). It is
*literally* what is being asked for in the aerospace column: an aircraft defined
by engineering parameters — wing area, aspect ratio, sweep, taper, airfoil,
fuselage stations — that produces real geometry. Adopting it would buy the
aerospace column in a fraction of the time it would take to grow it here. The
cost is a second geometry authority in a product whose whole design is a single
`geometry.Document` that everything reads; the honest framing is that FORGE would
have to treat a VSP model the way it treats a `script` today — an opaque solid
imported as STEP — and every document-level check would go blind on it again,
which is precisely the hole Stage 5 and Stage 7 were spent closing.

**IfcOpenShell** (open source, OCCT-based, IFC read/write). Same kernel family as
build123d, so no truth split at the geometry layer. The interesting half is not
geometry at all — it is the schema: walls, slabs, columns, storeys, materials,
quantities. If the architecture column is ever to be more than boxes, IFC is the
format the industry actually exchanges, and `.ifc` would be the fourth export
next to `obj`/`stl`/`step`.

**bd_warehouse** (build123d's own parts collection — fasteners, threads, bearings,
extrusion profiles). Small, same library, same sandbox story. The cheapest of the
three and the least transformative: it is a catalogue, which is #4 above with
someone else's numbers.

None of these is a decision this document takes.

---

## Blender: would it be useful?

**For geometry, no — and adopting it for geometry would undo the product's
central promise.** For rendering, maybe, and it should be measured before it is
believed.

### Why not for geometry

1. **It is a mesh modeller.** A B-rep stores a circle as a circle; a mesh stores
   it as a many-sided polygon that is never exactly round no matter how many
   sides it gets. FORGE's entire premise is dimensioned solids that export to
   STEP and can be machined. Anything that round-trips geometry through Blender
   comes back tessellated: the STEP export becomes a mesh-derived approximation,
   a fillet stops being a fillet, and a dimension stops being exact.
2. **The plan already took this decision, on evidence.** 2026-09-09: *"A pretty
   mesh with no engineering meaning is available today from image-to-3D
   generators and is not what a durable engineering partner is for."* Blender is
   a better way to get the same thing FORGE deliberately declined to be.
3. **It would split the source of truth.** Every check in this system reads
   `geometry.Document`. A Blender-authored body is not in the document — it is
   the `script` blindness problem again, at the scale of a whole car, and Stages
   5 and 7 are the record of how expensive that hole is to close.
4. **The sandbox story is far worse.** The current sandbox is an AST whitelist
   over 209 build123d names, plus a stripped short-lived process, and it is
   already documented as *not a container*. `bpy` is a whole application's API
   with file and module access; restricting it is a containment problem, not a
   whitelist problem.
5. **It does not solve any wall above.** Not one of A–H is a rendering problem.
   Mirror, shell, sections, alignments, frames, interference, budget — the OCCT
   kernel already in the pod does all of the geometry ones.

### Where it could genuinely earn a place

1. **The presentation render.** Today's pictures come from a Go rasterizer:
   orthographic, z-buffered, flat. Blender headless (`blender -b -P`) with Cycles
   would produce the photorealistic image PRD VIS-06 already has the labelling
   discipline for (*"a render is labelled as a render"*). This is pure UX, it
   costs a large dependency in the pod, and it changes no number.
2. **Sharper eyes for the vision check** — the one with a real, testable
   hypothesis behind it. Shadows, contact shadows and ambient occlusion are
   exactly the cues that make *"floating clear of the chassis"* and *"buried
   inside the body"* obvious, and those are the two defects `look.go` exists to
   catch. It might measurably raise the catch rate on a flat-shaded sheet. It
   might also raise the false-positive rate, and this repository has already had
   to delete one rule that fired on correct models. **Measure both arms before
   believing either** — the harness for exactly this already exists
   (`agent/look_live_test.go`, the Stage 11 concurrent-arm method).
3. **glTF for the browser at high part counts.** Marginal: the tessellator
   controls deflection already, and the viewport is `forge3d.js`.

### The honest summary on Blender

It is the right tool for the picture at the end and the wrong tool for everything
before it. If the appeal is *"Blender can model a car"* — that is true, and the
car it models has no dimensions, no materials that mean anything, no thickness,
and cannot be exported to STEP. FORGE already refuses to call that a prototype.

---

## What I would recommend, and what needs a decision

**Recommended order**, each stage closing on its own the way Stages 1–11 did:

1. **Mirror** (Wall B). Smallest, immediately halves the authoring cost of three
   of the five objects, and measurable the same day: same prompt, two arms.
2. **Frames and instances** (Wall A). The big one. Needs a contract decision
   first.
3. **Shell / thickness** (Wall C). Turns every one of the five from a solid lump
   into something whose numbers could one day mean something.
4. **Sections and members along an alignment** (Walls D, E). One implementation,
   four domains, and it is `gear.go`'s pattern applied again.
5. ~~**Interference**~~ — **done 2026-09-12**, moved to the front after the live
   run showed it firing at 28 parts rather than 400. **Mass properties and
   envelope remain** (Wall F).
6. **Durable multi-hour builds on the worker** (Wall G).
7. **Domain shapes**, chosen by which column is wanted first.

**Decisions I cannot take, in priority order:**

| # | decision | why it is yours | if it goes the other way |
|---|---|---|---|
| 1 | **Does `Document` get an assembly tree?** A `frame` on a part plus placeable frames is an additive, optional field — but it is a shipped contract read by the viewport, the exporter, `prototype_edit`, compare and every fence. `careful-api-creation` says this is a decision. | It is the one change that touches everything downstream. | Stay flat, and accept that parts above ~100 will keep being placed by mental arithmetic — which is the measured defect source. |
| 2 | **Which column first?** Bridge and house are by far the cheapest (they are sections along lines; no freeform surfacing at all). Car and plane are the most impressive and need the most new ideas. Rocket sits between. | Product call, not a technical one. | — |
| 3 | **Own shapes, or adopt OpenVSP / IfcOpenShell?** Adopting buys a column quickly and adds a second geometry authority that document-level checks go blind on. Growing them here is slower and keeps one truth. | Directly a `source-of-truth 不要分裂` trade-off, and the cost lands on maintenance, which is priority 1 in the cost ordering. | — |
| 4 | **Blender for the presentation render?** Yes/no, and if yes it is a UX feature with a large dependency, gated the way vision and scripts are — absent unless configured. | It changes the pod and it changes nothing about correctness. | — |
| 5 | **Blender-rendered images for the vision check?** Only worth doing as a measured A/B against the current rasterizer, both arms, false positives counted. | Costs a spike before it costs anything else. | — |
| 6 | **Does `script` stay off by default?** Several of the shapes above would otherwise have to be built as shapes rather than reachable as scripts. | Security decision, already taken once explicitly. | — |

## What this document does NOT claim

- That any of the walls above is the *only* thing between here and a car. They
  are the ones readable from the code and from Stages 1–11's measurements; the
  last plan's record is that live runs find defects reading never would.
- Any number for how long any of this takes.
- That mirror, shell or frames will raise the live build rate. Stage 10's warning
  stands and applies to every row here: **whatever comes next should be measured
  before it is believed.**
