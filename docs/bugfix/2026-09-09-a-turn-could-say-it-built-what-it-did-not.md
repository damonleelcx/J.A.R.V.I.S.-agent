# A turn could say it built what it did not

**Found:** 2026-09-09, while testing "add a spoiler to the sports car" and then
"add wheel wells" against a real model.
**Severity:** high on the conversational path — the reply and the model
disagreed, and the reply was the confident one.
**Owner:** agent (conversation).

## Symptom

Two real turns, both against `qwen3.7-plus`:

| Asked | FORGE said | What was stored |
|---|---|---|
| "Add a rear spoiler" | *"I have added a rear spoiler… a swept profile to create an aerodynamic wing"* | the wing's outline had **two points**, which is a line — the part was left out |
| "Add wheel wells" | *"I am cutting wheel wells into the body"* | an arch loop repeated a point, so the loop had a zero-length edge and **the whole body was dropped** |

The second is the worse one: the car had no body at all.

## Root cause

Nothing between "the model wrote geometry" and "the turn is emitted and saved"
asked whether the geometry could be built. The builder knew — it records the
reason for every part it leaves out — but only as a note for a reader, arriving
beside the confident sentence that said the opposite.

The notes were themselves invisible on the built-solid path until earlier the
same day; see
`docs/bugfix/2026-09-09-say-why-a-part-is-missing.md`-equivalent commit.

## Why a repair and not a stricter prompt

The first case *was* partly a prompt fault and was fixed as one — the sweep
guidance asked for "an open line of at least two points" with the word "profile"
immediately before it. That fix is real and does not generalise: a repeated
coordinate in a loop is not a misunderstanding to be worded away, it is a slip,
and a model will make it again.

`dimensionrepair.go` takes the other route wherever one exists — read the intent
charitably — and that was the right call for `depth` on a cylinder, where the
meaning is unambiguous. A duplicated point has **no** charitable reading: nobody
can say which of the two was meant. So the model is shown exactly what the
builder said and asked to correct that and nothing else.

## What was built

`Document.Faults()` — the Error-severity problems only. `Solids()` already
reported everything it did as one flat list of sentences, which is right for a
person and useless for a decision: "no height was given, so 1 mm was used" and
"an outline needs at least 3 points, so it is not in this file" are the same kind
of string and completely different kinds of event. Severity already separated
them where they were produced and was being discarded at the boundary.

`repairIfFaulty` runs after the reply is parsed and before anything is emitted or
saved, on **both** reply paths — a rule enforced in one of two paths holds until
somebody uses the other one.

### The three rules that took three tries to get right

Each was wrong in a way only a live model exposed. The stubbed tests passed
throughout.

1. **"One attempt, and the result must be perfect."**
   Refused a real improvement. Faults **cascade** — the validator reports the
   first thing wrong with a loop and stops — so removing the repeated point
   revealed a containment problem underneath. Measured: one fault in, one fault
   out, entirely different.

2. **"Accept only a strictly lower fault count."**
   Still refused it, for the same reason: the count did not fall, the fault
   changed. The bar became *no worse, and something moved*, with termination
   coming from the attempt cap rather than from the count.

3. **"Any pass that reaches zero faults is good."**
   The model reached zero by **deleting both wheel arches**. A perfectly valid
   document that no longer contained the wheel wells somebody had just asked for
   — and FORGE would have said the wells were made. A part count does not catch
   it, because the arches live *inside* a part.

   So nothing may be removed: no part, no feature, no loop within a part. Points
   *inside* a loop are exempt, because removing a repeated point is the fix.

## Verification

`TestLiveGeometryRepair` (opt-in, `FORGE_LIVE_LLM_TESTS=1`) runs both halves
against a real model, with fixtures that are real output rather than invented:

- **mendable** — the two-point wing outline: the model adds a point, the part
  builds, nothing is deleted.
- **unmendable** — the edge-open wheel arch: the model's only route to zero
  faults is deletion, so the repair is **refused** and both arches survive with
  the fault still reported.

`georepair_test.go` fences the mechanism with a stub: one call for a clean
document (zero), the cap, the refusal of a rewrite that moved nothing, the
refusal of a redesign, and that a failed repair never costs the turn its
geometry.

## What this does not do

- **It cannot fix a wrong approach.** An arch cut from the bottom edge of a
  profile is not a hole in that profile, and no coordinate fix makes it one. The
  turn keeps the fault and says so.
- **It costs one model call per attempt, on the failure path only.** A document
  with nothing wrong never calls it — fenced.
- **It is not a quality check.** It asks "will this build", not "is this a good
  design". A buildable model with the wheels inside the bodywork passes.
