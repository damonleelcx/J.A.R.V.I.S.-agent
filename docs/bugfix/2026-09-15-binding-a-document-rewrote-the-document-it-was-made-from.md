# Binding a document rewrote the document it was made from

**Found:** 2026-09-15, while binding a tree's child positions to parameters (branch `agent/bound-child-positions`,
stacked on #111). Offline: found by reading `binding.go` and `edit.go` against the new bindings, then confirmed with
scripted replies. No live model call.
**Severity:** medium. A build step that changed a parameter and was then refused left the model the build carries on
with contradicting its own parameters, and a respec rewrote its source document's outline in memory. Nothing stored was
corrupted.
**Owner:** agent — `internal/domain/geometry` (`binding.go`: `bind`, `bindPart`, `clone`; `tree.go`, `interface.go`).

## Summary

1. `Edit.Apply` copies a document's lists of parts, definitions and assemblies, but the size maps, position slices,
   outlines and children inside them stay shared with the base. `Bind` writes the numbers it evaluates into exactly
   those, in place. Binding the document an edit produced therefore rewrote the model the edit was applied to.
2. `clone` (what `WithParameters`, and so `Service.Respec`, works on) deep-copied sizes and positions, but shared a
   part's `profile`, `holes` and `path`, which `Bind` writes an expression's coordinates into. A respec rewrote its
   SOURCE's outline.
3. (Gap, the reason this was looked at) a child's or an interface's position written as an expression
   (`["-half_track", 0, "half_wheelbase"]`, taught by #111) was stored as its number, so a respec moved every
   definition bound to a parameter and left the placements, which are most of a tree's dimensions, where they were.
   Closed in the same change: `position_from` on a child and an interface.

## Symptom

A build step on a car whose `arm` definition binds `width` to `half_track` (800 mm). The step's patch sets
`half_track` to 900 and attaches a child at an interface that does not exist, so the fault gate refuses it:

```
next=false note="Step 4 (Brakes) was left out: it would have broken the model: it had 1 fault(s) where the model
before it had 0, and these are new: brakes/disc is attached at \"nowhere\", but assembly \"brakes\" declares no
interface \"nowhere\" (it declares none)."
kept model: half_track=800 arm width=900
```

The step was refused and the model kept, as the loop promises. But the kept model's `half_track` still says 800 while
the arm bound to it is 900 wide.

A respec of a plate whose outline corner is `x_from: "plate"`, `WithParameters({"plate": 90})`: the variant's corner is
at 90, and so is the source's, which was 60.

## Impact

- **Refused build steps.** A refused step is meant to leave the model exactly as it was, so that ten good steps are
  not spoiled by an eleventh bad one. After one that changed a parameter, the model carried on held bound dimensions
  from the refused step against parameters from before it. The next step was shown those wrong numbers. The next
  accepted step's settle re-binds from the parameters, which puts the numbers back and adds a "states width = 900 but
  its own expression … works out to 800" caveat about a disagreement nobody wrote. This follows from the code and the
  confirmed state above. It was not observed in a live run.
- **Conversational edits.** The model on screen that an edit is applied to had its bound sizes and positions rewritten
  to the edit's parameters in memory. It is not re-saved, so storage was unaffected.
- **Respec.** `Service.Respec` reads its source fresh from the database and discards it, so no stored variant was
  changed. An in-memory caller of `WithParameters` (a comparison, a sweep) saw its source's outline move with the
  variant: the "two identical shapes side by side" failure `clone`'s own comment names.
- With child positions bound (this change), the same sharing would have reached `Children`, which `Edit.Apply` also
  shares, so every edit that changed a track would have moved the base's wheels.

## Preconditions

- For the edit case: a patch that changes a parameter (or a derived value) that something in the base binds, and a
  bind of the result: every build step and every conversational edit binds via `settleDocument`.
- For the respec case: a part whose outline, holes or path carries an `x_from`/`y_from`/`z_from`/`radius_from`.

## Root cause

`Bind` is documented as mutating its receiver, and it does that by writing into the maps and slices it finds:
`p.Size[key] = value`, `p.Position[index] = value`, `writeBack(p.Profile, …)`. That is correct only when those maps and
slices belong to that document alone. Two ways of making a document from another break that:

- `Edit.Apply` copies each list with `append([]Part(nil), base.Parts...)`. That copies the structs, and each struct still
  points at the base's `Size` map, `Position` slice, `Profile` points and, for assemblies, `Children` array. Its comment
  says the tree is "carried, not shared", which is true of the lists and not of what is inside them.
- `clonePartList` copied `Size`, `SizeFrom`, `PositionFrom`, `Position`, `Rotation` and `Material`, but not `Profile`,
  `Holes` or `Path`.

## Why it did not show up before

- `TestTree_ACloneSharesNothingWithTheOriginal` and `TestWithParameters_DoesNotTouchTheDocumentItCameFrom` checked
  sizes and positions, the fields `clone` did copy. No fence re-specified an outline and then looked at the source.
- No fence bound an edit's result and then looked at the base. Every edit fence compares the base's lists, which are
  copied, or binds nothing.
- The damage heals on the next accepted step, which re-binds from the parameters. What remains is a caveat that reads
  like the model's own arithmetic slip.
- Live builds rarely change a parameter in a later step, and the car runs that did were not refused on the same step.

## Fix

`internal/domain/geometry/binding.go`:

- `bind` writes into copies. It copies `Parts`, `Definitions` and `Assemblies` when anything in that list is bound.
  `bindPart` copies `Size` before a `size_from` is written, and `Profile`/`Holes`/`Path` before their coordinates are
  written back. The new `bindPosition`, shared by parts, definitions, children and interfaces, writes a copy of the
  position. `bindPlacements` copies an assembly's `Children` and `Interfaces`. nil stays nil and an empty list stays
  empty, so a document with nothing bound stores the same bytes.
- `clonePartList` copies `Profile`, `Holes` and `Path`, and `cloneAssemblies` copies a child's and an interface's new
  `position_from`.

The gap (item 3): `Child.PositionFrom` and `Interface.PositionFrom` (`tree.go`, `interface.go`) are bound by `bind`
exactly as a part's position is. The agent's placement reading (`agent/dimensionrepair.go`, `repairPlacements`) keeps an
expression it evaluates as that axis's `position_from`, beside the number.

Also checked for things a respec silently ignored, and none were found: definitions' sizes and outlines written as
expressions (read into `size_from` by #97's repair, bound by `bind`), feature radii (`radius_from`, evaluated when
features are read), a pattern path's points (an expression there is refused by name), and top-level parts.

## Verification

- `internal/domain/geometry/bound_placements_test.go`:
  - `TestBind_BindingAnEditedModelLeavesTheModelItWasMadeFromAlone`: an edit changing `half_track` and `hub_reach`
    over a base with a bound definition size, top-level part and child and interface positions. After `Bind` of the
    result, the base stores the same bytes as before, and the result moved.
  - `TestWithParameters_LeavesTheSourcesOutlineAlone`: an extrusion's corner and hole bound to `plate`, re-specified from
    60 to 90. The source keeps 60 and 52.
- `internal/agent/bound_positions_test.go`, `TestAssemble_ARefusedStepThatChangedAParameterLeavesTheModelAsItWas`: the
  symptom above, replayed through `buildOneStep`. The step is refused, and the kept model's arm is 800 wide.
- Failing before the fix: the scripted run in Symptom printed `arm width=900`; the drills below put the sharing back and
  the fences go red.

## Regression prevention

`scripts/drill-fences.sh`, section "Bound child positions": drills that share `Size`, a position, `Children` and an
outline with the source again each turn a fence red.

## Not in this fix

- `Edit.Apply` still shares maps and slices with its base. Binding no longer writes through them, but any other
  in-place writer of an edit's result would. No other one was found.
- Nothing measured live: whether a refused step that changed a parameter ever happened in a live build is not known.
