# A placement from the root could not name the root, and the refusal named no child and offered no fix

**Found:** 2026-09-15, the live car run verifying the stacked car PRs (`docs/spikes/2026-09-15-car-verified`,
data in `data/calls.jsonl`, calls 5, 6 and 8).
**Severity:** high. Step 2 of eight was refused for a path that meant exactly what FORGE would have accepted
written one word shorter, and neither of its two repairs could act on the refusal. The step, its uprights and
hubs, and the wheels of step 7 that needed those hubs were all lost.
**Owner:** agent — `internal/domain/geometry` (`interface.go`, `tree.go`: attachment resolution and refusals),
`internal/agent` (`georepair.go`: the fault repair's prompt), `internal/httpapi/assets/forge3d.js` (the
browser's copy of the resolver).

## Summary

1. A child of the ROOT whose `at` began with the root's own id was refused. Root `chassis`,
   `at: "chassis/cockpit-floor"` → `assembly "chassis" has no child "chassis"`. #111 says a leading root id is
   dropped; it was dropped only where a refusal SUGGESTS a path to write, never where one is resolved, so
   nothing the step could have written that way actually attached.
2. The refusal named no child. The fault's sentence begins "is attached at", and the fault repair's prompt sent
   the sentence without the fault's `Name` — so the repair read `- is attached at "chassis/cockpit-floor", but …`
   and could not tell which of the step's nine children was meant.
3. The refusal carried no remedy. `has no child` and `declares no interface` said what had failed and nothing to
   do about it, so both repairs rewrote the path to another one that does not exist.

## Symptom

```
step 2/8 Suspension Uprights and Hubs parts=0   Step 2 (Suspension Uprights and Hubs) was left out: it would
  have broken the model: it had 1 fault(s) where the model before it had 0, and these are new: suspension-mounts
  is attached at "chassis/cockpit-floor", but assembly "chassis" has no child "chassis".
```

The step's reply declared `{"id":"suspension-mounts","ref":"suspension-mounts","at":"chassis/cockpit-floor"}`
under `placements`, on a step view whose `attaches_to` had just shown it `cockpit-floor` among the root's seven
interfaces. Repair 1 (call 6) was shown the nameless sentence, moved the declared path to `"cockpit-floor"` —
correct — and was rejected anyway; repair 2 (call 8) moved it back. Steps 4's refusals reached their repairs the
same way: `- is attached at "powertrain/transmission-output", but …`.

## Impact

- Any step that declares where the root places what it built can spell the path the way a reader naturally
  writes it — from the root, naming the root — and lose the whole step.
- Both repair calls of a lost step (≈6,700 completion tokens) were spent on a refusal neither could act on.
- The cost compounded: step 7's wheels needed step 2's hubs, so one refusal cost two subsystems.

## Preconditions

1. A tree build whose root is an assembly with interfaces, and a step that declares a `placements` entry (or an
   author who writes a child of the root) with `at` beginning `"<root id>/"`.
2. (2, 3) Any attachment refusal at all reaching the fault repair.

## Root cause

1. `attachments.interfaceIn` resolves every path segment-by-segment from the assembly it is written in, and the
   root is just another assembly to it: `"chassis/cockpit-floor"` asks the root for a child called `chassis`.
   #111's leading-root-id drop lives in `outsideProblem`, which only composes the *suggestion* text for a path
   that leaves its assembly. Nothing in the resolver ever dropped one.
2. `repairGeometry` built its prompt as `"- " + f.Detail`. `Problem.Name` carries the part's path and the step's
   note has always printed it (`addedFaults`); this one prompt dropped it.
3. `childFrame` and `interfaceIn` describe the lookup that failed. Only the cross-assembly refusal added by #111
   carried an instruction.

**Classification:** (1) a resolver stricter than the contract a reader can infer; (2) a renderer dropping a field;
(3) refusals without remedies.

## Why it did not show up before

Every fence and every example wrote a root-level `at` without the root's id, because that is how the contract
shows it — so no test ever asked what the other spelling does. #111 was fenced offline only, and its own live
verification (#118) was the first run in which a model wrote one. The nameless repair prompt had the same
history: every earlier fault the repair saw was about a part whose sentence began with a dimension, where the
missing name read as awkward rather than as ambiguous.

## Fix

- **A path resolved FROM the root may begin with the root's own id** (`interface.go`, `interfaceIn`): when the
  assembly being resolved from IS the root, the first segment is the root's id, and the root places no child of
  that name, the segment is dropped — so `"chassis/cockpit-floor"` resolves exactly as `"cockpit-floor"` does.
  One place, so a declared placement (`stepdeclared.go` appends it to the root) and a hand-written child of the
  root get the same rule.
  - **Only from the root.** A child written inside another assembly that names the root is still refused and
    told to attach from the root instead (`leaves`, `outsideProblem`): resolving it would let an assembly
    written once reach outside itself, which is the rule D1d rests on. The live step's own four uprights are
    that case, and they are now refused with the path to write.
  - **A real child wins.** The drop happens only where the root places nothing of that name, so the leniency can
    never take a path away from the child that already owned it.
- **Every attachment refusal names its child** (`tree.go`, `namedChild`): the child's own name reads before "is
  attached at", and the fault's `Name` carries the id.
- **Every attachment refusal carries a remedy** (`interface.go`, `attachRemedy`): the paths that DO attach here —
  the assembly's own interfaces, then those on what it places, bounded at 6 with the rest counted, because a
  step's note clips a fault at 200 characters. The list comes from `interfacesUnder`, lifted out of
  `InterfacesFromRoot` so the paths a refusal offers are the same ones the step view shows as `root_interfaces`
  and the same ones a placement resolves.
- **The fault repair is shown the name** (`georepair.go`): `"- " + Name + " " + Detail`, and the same for the
  "could not be corrected" note a reader sees.
- **The browser agrees** (`forge3d.js`): the same drop, with the same real-child guard, so what Go places the
  browser draws.

## Verification

- Geometry: `TestInterface_APathFromTheRootMayBeginWithTheRootsOwnID` (the root's own interface, a sibling's, a
  mirrored sibling's nested one — each identical to the path without the root id; a real child of that name
  wins; a child inside another assembly is still sent to the root),
  `TestInterface_EveryAttachmentFaultNamesTheChildAndWhatToWriteInstead` (named, remedied, bounded, and the
  empty case).
- Agent: `TestRepair_EveryFaultTheRepairIsShownNamesThePartItIsAbout`.
- Replays of the live run's saved replies (`internal/agent/testdata/car-verified-*`):
  `TestReplay_CarVerifiedsDeclaredPlacementFromTheRootNamesTheRoot` — the declared path resolves on the live
  model to the frame `"cockpit-floor"` names; the step is still refused, now for its own four uprights, with the
  root path to write for each; the repair prompt carries every child, path and remedy.
  `TestReplay_CarVerifiedsRepairsAreToldWhichChildAndWhatToWrite` — both saved repair documents.
- Browser parity: `TestRendererFlattensATreeLikeTheExporter`, case "a placement from the root whose path names
  the root".
- Drilled in `scripts/drill-fences.sh`, "Root-id placement", nine drills, all red.
- **Not live-verified.** A live re-run waits on damon's token go-ahead.

## Regression prevention

The leniency is in the resolver every reader comes through, so the step note, the repair prompt, the export
notes and the browser cannot disagree about which paths attach. The remedy's path list is produced by the same
walker that answers `root_interfaces`, so a refusal cannot offer a path a placement would refuse. The repair
prompt now renders faults the way the step note already did, from the same two fields.

## Not in this fix

- FORGE does not rewrite a child that reaches out of its assembly: which assembly a child belongs in is the
  design, and the live step's four uprights are still refused rather than moved to the root.
- The step contract is not taught the leading-root-id spelling. It is accepted, not advertised; the prompt still
  shows `root_interfaces` paths, which carry no root id.
- Nothing tells a reader that a leading root id WAS dropped. A path that resolves says nothing at all.
- The repair's monotone rule is unchanged: a pass that fixes one fault and reveals four is still rejected as
  worse. Run 4's first repair is now judged against the step's true eight faults rather than the one that
  masked them, but nothing here makes a repair that exposes more faults acceptable.
