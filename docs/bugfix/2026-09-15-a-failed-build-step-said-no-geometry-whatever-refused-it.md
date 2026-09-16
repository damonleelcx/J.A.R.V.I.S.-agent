# A failed build step said "no geometry" whatever refused it, and a tree step could drop a subsystem silently

**Found:** 2026-09-15, after two live car builds (in-request `docs/spikes/2026-09-15-car-tree`, and the build-goal
run on `agent/build-goal-entry`) each lost their first step — the goal run its first two — and neither could say why.
**Severity:** high for diagnosis, medium for the product. Nothing wrong was installed by the first defect; every step
it hid was paid for (about 11–13k tokens each) and could not be investigated. The second defect installs a model
missing whole subsystems and tells nobody.
**Owner:** agent (build loop) — `internal/agent/assemble.go` `buildOneStep`, shared by the in-request build and the
build goal (A1).

## Summary

1. Seven different reasons a build step keeps nothing all produced the one note `Step N (name) produced no geometry.`,
   and a step refused for adding faults said `it would have broken the model` without naming one.
2. A step on a tree that placed a new sub-assembly by patching the root replaced the root whole, dropped every
   placement already in it, and the step's note was empty.
3. (Harness, found while diagnosing) the live measurement never gave the build a kernel renderer, so each step's
   interference check did not run and each look was of the described model, not the built one.

## Symptom

```
step 1/7 Chassis Frame       parts=0  produced no geometry
step 4/7 Drivetrain Assembly parts=4  was left out: it would have broken the model
step 5/7 Braking System      parts=4  was left out: it would have broken the model
```

Reproduced offline (a stub model answering `buildOneStep` once per case), every one of these first-step replies
produced exactly `Step 1 (Chassis Frame) produced no geometry.`:

| reply | what actually refused it |
|---|---|
| `"position": [0, track/2, 0]` | JSON does not parse (`parseReply` kept the words and returned no error) |
| a `// comment` inside the JSON | JSON does not parse |
| `"parameters": [{"value": "800"}]` | a string where a number goes; not a dimension `repairDimensions` reads |
| a child `"position": ["-track/2", 0, 0]` | the same, in a tree, where the repair does not look |
| `prototype_edit` on the empty first model | `resolveEdit`: no model on screen |
| `definitions` + `assemblies`, no `root` | `settleDocument`: places nothing |
| `{"build_in_passes": true}` and speech only | no prototype and no edit |

And a brakes step, shown `{"focus":"brakes","new":true,"other_placements":1}` and replying with the root patched
to `{"id":"car","children":[{"id":"brakes","ref":"brakes"}]}`, was accepted with the chassis gone and note `""`.

## Impact

- Two live runs, about 330k tokens between them, left the most important failure of each undiagnosable. The spike
  READMEs could only list the candidates.
- A car whose chassis step was lost has nothing for later steps to mount to; the look check then saw "floating"
  parts on four steps. Consistent with, not proven by, the missing step.
- Any tree build (in-request or goal) whose plan names a new sub-assembly per step could lose earlier subsystems with
  no note, no fault and no refusal.

## Preconditions

1. A build in passes (`assemble`), in either entry point.
2. For (1): any step reply that is not a readable, rooted, fault-free document or edit.
3. For (2): a model already written as a tree, and a step whose `assembly` does not exist yet, whose reply patches the
   root without restating its children.

## Root cause

1. **One sentence for four gates.** `buildOneStep` wrote the same note after `parseReply` (whose last resort returns
   the reply's words with a nil error when the JSON cannot be read), after `resolveEdit` (whose error was discarded),
   after a nil prototype, and after `settleDocument` returned nil. The fault refusal compared counts and discarded the
   lists.
2. **The new-assembly view could not be answered safely.** `SubtreeModel` showed a step creating an assembly only the
   root's interfaces and a count of its children. Placing the new assembly means patching the root, `Edit.Apply`
   replaces a patched assembly whole (by design, `upsertAssembly`), and the step had nothing to restate. The one check
   that would have said so, `vanishedParts`, compared top-level `Parts` — empty on both sides of a tree.
3. **The measurement built a different conversation from production's.** `httpapi` wires `WithSolids`; the live
   harness wired only `WithScripts`.

**Classification:** (1) an observability gap in a loop built before trees and edits had several ways to fail; (2) a
design gap between stage A2's view and D1f's whole-assembly patch; (3) a harness drift.

## Why it did not show up before

The build loop's fences stub whole documents and flat edits that succeed, or a reply that is plainly empty; none
stubbed an unreadable reply, an edit on the empty model or a rootless tree. A2's fences checked that the view is
small and names the focus, not that a step could place a new assembly from it without losing the rest. The live
runs kept neither the replies nor, until this change, the car.

## Fix

- `internal/agent/stepgates.go`: each refusal names its gate — `came back unreadable` (with the decoder's complaint,
  the character offset and a one-line excerpt, and `cut off at the reply limit` when the provider says so),
  `sent no geometry` (and whether it asked for `build_in_passes`, with its speech), `sent an edit that could not be
  applied` (with `resolveEdit`'s detail), `produced a model that places nothing` (definitions and assemblies with no
  root), and `was left out: it would have broken the model` with the faults it added, by name, three at most and the
  rest counted. Bounded to 700 characters of detail. `StepGateOf` reads the gate back for the measurement.
- `buildOneStep` asks `unreadableDetail` whenever a step arrives with neither a prototype nor an edit, so
  `parseReply`'s word-keeping last resort no longer hides an unreadable reply.
- `SubtreeModel` shows a step creating an assembly the root's children (`root_children`); the step prompt and the
  contract say a patched assembly is replaced whole and the root must be sent with every child it already has.
- `vanishedParts` compares a tree's placements (expanded, by path), and reports a lost sub-assembly once with its
  part count: `removed chassis (2 parts)`.
- The live harness renders each step through the kernel (`liveSolids`, now world meshes with coverage as
  production's), and keeps every call's prompt and reply beside the car.

## Verification

- `TestAssemble_AFailedStepSaysWhichGateRefusedIt` — one subtest per gate, each asserting the phrase, the detail
  and `StepGateOf`.
- `TestAssemble_ARefusedStepsNoteIsBounded` — fifty added faults and a 50 KB unreadable reply both give a short note.
- `TestAssemble_AStepThatDropsAPlacementSaysSo`, `TestSubtreeModel_ANewAssemblyIsShownWhatTheRootAlreadyPlaces`.
- `TestCarMeasure_ARefusedStepIsNamedByItsGate`.
- Drilled: `scripts/drill-fences.sh`, section "Live car findings", all red.

## Regression prevention

The gates are constants read by one function (`StepGateOf`) that both the notes' fence and the measurement's fence
exercise, so a reworded gate fails a test instead of silently reading as "no problem" in the next live run.

## Not in this fix

- **Which gate refused the live steps.** That is measured in `docs/spikes/2026-09-15-car-quality`, from the replies the
  harness now keeps; the root cause of the empty first step is recorded there.
- Refusing a step that drops placements. It is reported, as a dropped part always has been; whether a build step
  should refuse it is a product decision.
- Keeping rejected replies on a build goal's task result (A1's branch). The note now carries the gate and the reason;
  the raw reply is kept only by the live harness.
