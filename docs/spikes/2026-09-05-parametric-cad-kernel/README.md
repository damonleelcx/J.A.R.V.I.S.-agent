# Spike: a parametric CAD kernel, and whether the model can drive one

**Date:** 2026-09-05 · **Status:** complete. Kernel installed, real B-Rep built
and exported, parameter sweep run, 3 live model runs captured ·
**Owner:** workbench

## The question

You asked for complex 3D. The 2026-09-02 Zoo spike answered "should we integrate
Zoo" (no, not now) but never asked the two questions that decide whether we can
do it **ourselves**:

- **Premise A** — can a real CAD kernel run here at all, and what does it cost?
- **Premise B** — can our model produce output good enough to drive one?

Reproduce with:

```bash
python3 -m venv venv && ./venv/bin/pip install build123d
./venv/bin/python bracket.py '{}' bracket.step          # Premise A
./venv/bin/python premise_b.py 3 runs.json              # Premise B (costs money)
```

---

## Premise A — PROVEN, and cheaper than expected

`build123d` 0.11.1 on OpenCASCADE, installed into a clean venv on **Python
3.14 / arm64** in **44 seconds**. The `cadquery-ocp` wheel is 62.7 MB and a
`cp314` build exists, which was not a given — the version was the first thing
expected to block this and did not.

The same part the Zoo spike used, a NEMA 17 bracket, built from named
parameters:

| | |
|---|---|
| Result | **valid B-Rep solid**, 37 faces, 87 edges |
| STEP export | 118 809 bytes, `ISO-10303-21`, Open CASCADE |
| **Build time** | **0.046 s** |
| **Export time** | **0.015 s** |
| Import time | 2.49 s, once per process |

### The number that changes the plan

**46 milliseconds.** The Zoo spike measured 180 s and still counting — and that
was *the agent thinking*, not the kernel working. A local kernel is not slow, and
the plan's assumption that "a build is a goal, not a turn" is wrong: at 46 ms a
build fits inside a conversational turn with room to spare. The only real cost is
the 2.5 s import, which a warm sidecar pays once.

### What the parameter sweep found

Nine parameter changes, eight held. The one that broke is the finding:

```
{"plate_size":50,"plate_thickness":8,"rib_depth":6}   BREAK
```

`rib_length` was an independent 52 mm while `plate_size` moved. At 50 mm the ribs
overhung the plate, and the fillet had no valid edge to sit on.

Making `rib_length` **derived** — `plate_size - 2 * fillet_radius` — fixed it,
and the same model then held across a **3.4× size range**:

| plate_size | 35 | 42.3 | 50 | 60 | 80 | 120 |
|---|---|---|---|---|---|---|
| solid | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

> **Naming a parameter is not enough. What makes a model survive a change is the
> RELATIONSHIP being the thing recorded.**

That is the single most important result for the implementation plan, and it
sharpens the 2026-09-02 spike's own conclusion — which said the difference was
that Zoo's ribs were "constrained and named". Named is the easy half.

### One more thing worth knowing

Both fillet failures were **OCCT refusing to build invalid geometry**, not OCCT
producing something wrong. A kernel that fails loudly is the behaviour this
codebase wants; it is the same stance as `CONNECTOR_UNAVAILABLE`.

---

## Premise B — the structure arrives, the figures do not

Three runs against **qwen-plus**, asked for parameters, derived expressions and
features. Raw replies in `data/premise-b-runs.json`.

| property | rate |
|---|---|
| produces derived expressions, not fixed numbers | **3/3** |
| every parameter carries a unit | **3/3** |
| every `"standard"` claim carries a source | **3/3** |
| **the NEMA 17 bolt figure is correct** | **0/3** |

The structure is reliable. Run 1's derived block is genuinely good:

```
rib_length      = plate_height - 2 * edge_margin
rib_position_y1 = edge_margin + rib_height / 2
```

That is exactly the relationship whose absence broke Premise A's sweep — the
model produced it unprompted.

### And then the figures

| run | claimed | labelled | resulting bolt pitch | truth | error |
|---|---|---|---|---|---|
| 1 | 42.3 mm "diagonal" | `standard` + source | 29.91 mm | 31 mm | **−3.5%** |
| 2 | 42.3 mm "diagonal" | `standard` + source | 29.91 mm | 31 mm | **−3.5%** |
| 3 | 47.1 mm "diagonal" | `standard` + source | 33.30 mm | 31 mm | **+7.4%** |

**42.3 mm is the NEMA 17 FRAME size, not a bolt dimension** — the 2026-09-02
spike records the correct set: *"42.3 mm frame, 31 mm square bolt spacing, 22 mm
pilot"*. The correct diagonal of a 31 mm square is 43.84 mm, which no run
produced.

Every run then applied the same expression, `(spacing / sqrt(2)) / 2`, treating
its parameter as a diagonal. **Note what that means: had the model supplied the
correct 31 mm square pitch, this expression would have made it wrong.**

### Three conclusions, and the second is uncomfortable

1. **The model can express structure.** Phase 1 of the plan is viable.
2. **Structure makes a wrong figure MORE convincing, not less.** The old failure
   was prose — *"50 mm, matching the NEMA 17 footprint"* — which the existing
   standards detector catches by pattern. The new failure is a typed field with
   `how: "standard"` and a citation, which reads as provenance and is not.
3. **Expressions are a new place for errors to hide.** A wrong number can be
   checked against a published figure. A wrong *relationship* produces plausible
   numbers from correct inputs, and nothing in this codebase checks one today.

---

## What this means for the plan

**Premise A passes; the plan's latency assumption was wrong and Phase 4 should be
dropped or deferred** — builds fit in a turn.

**Premise B passes on structure and fails on figures**, which does not stop
Phase 1 but changes what it must include: the honesty machinery has to extend
from prose into the typed fields, or the parametric representation ships a
better-dressed version of the defect `standards.go` was written for.

Concretely, `internal/agent/standards.go` and the
`standardFiguresAreNotFabricated` scorer currently read the reply's TEXT. They
would need to read parameter values and — the harder half — evaluate derived
expressions to see what figure a model actually ends up with.

## Related

- `docs/spikes/2026-09-02-zoo-text-to-cad/` — the NEMA 17 reference figures and
  the four-run baseline this is measured against
- `docs/bugfix/2026-09-02-fabricated-standards-figures.md` — the prose version of
  the same defect
- `internal/agent/standards.go` — the detector that would have to grow typed
  fields
