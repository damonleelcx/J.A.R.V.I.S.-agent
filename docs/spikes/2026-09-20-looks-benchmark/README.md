# The looks judge, and what it said about today's output (looks, stage D)

**Date:** 2026-09-20/21 · **Branch:** `looks/judge`, stacked on PR 159 (`looks/integration`) · **Stage:** "looks
designed", stage D — D1 the judge, D2 the benchmark.

damon, 2026-09-18: "looks designed" is a FORGE goal, AND the defect check stays closed to styling —
`internal/agent/look.go` and `sketch.go` forbid styling comments because every styling complaint in testing was wrong
and damaged good models, and the repair loop must never act on one. Looks are judged by a **separate gate**. This is
that gate (`internal/looks`) and the fixed benchmark it was run against.

## What the judge is allowed to say

Three words about two renders of the same design change: **better**, **worse**, **no difference**. It returns no
document, no edit and no problem — `TestTheGateCanOnlySayBetterWorseOrNoDifference` reads `judge.go` and fails if it
ever names one — and no file that builds `internal/agent`, `internal/domain/*`, `internal/llm` or `internal/eval` may
import it, which `TestTheRepairLoopCannotSeeAStylingVerdict` holds over 200+ files. That is what makes "separate"
structural rather than a comment.

A change is **ACCEPTED** only when all of:

1. the judge prefers the new render with the new one shown **first**, and
2. prefers it again with the images **swapped**, and
3. where both sides have a surface-normals view, prefers it both ways round **in that view too**, and
4. faults, buried pairs and skipped parts — read from the existing defect check's output, never re-judged here — **do
   not increase**.

One question carries **at most two images** (`MaxImagesPerQuestion`), and `askOnce` refuses rather than truncating: a
third picture turns "which of these two" into an open invitation to comment, which is the question `look.go` was
written to avoid, and it destroys the swap that makes position bias measurable.

Every verdict is written out with the exact images that produced it — [`verdicts/`](verdicts/) holds one directory per
change, each with `verdict.json` (the answer verbatim, which view, which order, what the check said on both sides) and
the four PNGs **in the order they were sent**.

## The live run: three attempts, 96,727 tokens of the 100,000 damon approved

The ceiling is **all-time over this directory**, not per-run ([`ledger.json`](ledger.json)). That is not a nicety: this
benchmark needed three attempts, and under a per-run ceiling those three would have been entitled to 300,000 tokens
between them with nothing in the harness saying so.

| run | spent | what happened |
|---|---|---|
| 1 | 18,046 (**estimated**) | Aborted. Headless Chrome dumped its 295 KB DOM and then never exited, and `cmd.Output()` blocked behind a pipe Chrome's children still held; the run was killed before it could report. It placed the same single bracket build call run 2 measured, so it is charged at that call's cost. The only estimated row in the ledger. |
| 2 | 18,046 | The bracket built and rendered; its look check, its judge and all four remaining prompts were refused, because the per-prompt ceiling (14,000) was under the cost of one build turn. |
| 3 | 55,768 | Bracket judged; gear, car and enclosure built; lever refused by the ceiling. The judge was refused on gear and car — see "three defects" below. |
| 4 | 4,867 | The three built models re-judged from disk, free. Enclosure and lever refused: no budget left for a build. |
| **total** | **96,727** | of a 100,000 ceiling. 78,681 of it metered exactly; 18,046 estimated. |

Provider `token-plan.cn-beijing.maas.aliyuncs.com`, converse `qwen3.7-plus`, vision `qwen3.8-max`. The key was loaded
into the harness shell only, never printed, logged or committed.

## The benchmark set, and what the judge preferred

Five fixed prompts — a bracket, a gear, a car, an enclosure, a lever. Each is one ordinary sentence, because a
benchmark prompt written to suit the system measures the prompt. The change being judged is the **renderer**: the same
document drawn by `origin/main`'s `forge3d.js` (`82c9e55`) and by this branch's, at 640 × 400, nothing labelled — a
caption reading "AFTER" is the answer written on the question.

| prompt | parts | shapes | faults | buried | skipped | verdict | accepted | tokens (build + judge) |
|---|---|---|---|---|---|---|---|---|
| bracket | 5 | cylinder ×4, extrusion ×1 | 0 | 0 | 0 | **no difference** | no | 18,046 + 1,619 |
| gear | 3 | box, cylinder, extrusion | **1** | **1** | 0 | **better** | **yes** | 18,133 + 1,626 |
| car | 58 | cylinder ×28, section ×17, extrusion ×12, box | 0 | 0 | 0 | **no difference** | no | 18,302 + 1,622 |
| enclosure | — | — | — | — | — | not judged | — | 17,708 + 0 |
| lever | — | — | — | — | — | not reached | — | 0 |

**Two of the three judged prompts were refused for position bias, not for looking worse.** That is the headline, and it
is why the swap is not optional:

- **bracket** — shown the new render first, the judge preferred the OLD one: *"Image 2 presents a cleaner, unified
  geometry without the distracting, semi-transparent artifacts visible at the joints in Image 1."* Swapped, it said
  there was no difference: *"Both images show the same raw, blocky geometry with sharp square corners and flat
  shading."* Two different answers about one pair. Verdict: no difference.
- **car** — new first, no difference. Swapped, better. Same pair, two answers. Verdict: no difference.
- **gear** — better both ways round, and it named the same thing both times (softer lighting, smoother shading, no
  grid). Faults 1→1, buried 1→1, skipped 0→0, so the guard passed. **Accepted.**

A single ask would have reported "worse" for the bracket and "better" for the car, and both would have been noise.

**The normals confirmation could not run on any of these pairs, and the record says so in each row.** It needs a
normals view on *both* sides, and `origin/main`'s renderer predates `setSurfaceView`. The after-side normals renders
are kept anyway ([`shots/*-after-normals.png`](shots/)) and the next renderer change will have one on both sides.

## Three defects this exposed

1. **The gear builds with a fault and a buried pair.** "a spur gear with 24 teeth, 3 mm module, 12 mm thick, on a hub
   with a keyed bore" produced three parts (box, cylinder, extrusion) with `doc.Faults()` = 1 and one pair the kernel
   found sharing material. Not a rendering problem; the model. Kept as
   [`models/gear.json`](models/gear.json) so it can be examined without paying for it again.
2. **The enclosure produced no model at all, for 17,708 tokens.** "a sealed aluminium electronics enclosure 160 by 100
   by 60 mm with a lid" returned a turn with no prototype. The harness now records what the turn SAID instead, because
   "produced no model" cannot be told apart from a refusal, a clarifying question or a bug — but that change came after
   this run, so what it said is not known here.
3. **The judge says today's render has "semi-transparent artifacts visible at the joints"** on the bracket, and
   "identical motion blur artifacts" on the car. Said once each and contradicted on the swap, so it is a lead and not a
   finding — but it is a lead about the branch's own renderer and worth a look.

## What is in here

- [`benchmark.json`](benchmark.json) — the whole run: every prompt's row, tokens per prompt and in total, both ceilings.
- [`ledger.json`](ledger.json) — every run this benchmark has ever made and what it spent. **Deleting it starts a new
  budget**, and that is visible in the diff.
- [`verdicts/<prompt>/`](verdicts/) — `verdict.json` and the four images per judged change, in the order sent.
- [`models/<prompt>.json`](models/) — the documents, kept so a re-run costs only what it still needs.
- [`shots/`](shots/) — every render, `<prompt>-{before,after}-{shaded,normals}.png`.

Re-run with `make looks-benchmark`. The ledger means a re-run cannot spend the ceiling twice; delete
`models/<id>.json` to rebuild one prompt.

## Not done

- **Two of the five prompts were never built.** The enclosure answered without a model and the lever was refused by the
  ceiling. The set is five; this run judged three.
- **No normals pair was ever put to the judge live.** Fenced offline and proven to be a different picture in a real
  browser (`TestTheNormalsViewIsADifferentPictureInARealBrowser`), but not exercised against a real model.
- **The guard never discriminated live.** Judging a renderer change means the same document on both sides, so the
  counts are equal by construction and the guard confirms rather than refuses. Its refusing behaviour is fenced offline
  (`TestAPrettierPictureCannotPayForAWorseModel`, five ways).
- **One run's spend is an estimate**, and the ledger says which.
- The conversation was given **no kernel**, to keep the build to one turn; these are pre-repair documents and the
  kernel was run separately over each finished one for the faults, buried pairs and skipped parts above.
