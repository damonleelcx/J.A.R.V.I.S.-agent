# Measurement: the last two hot spots at 1,000,000 occurrences

**Date:** 2026-09-15 · **Status:** partial — see "What this does NOT establish" · **Follows:**
[`2026-09-15-check-profile`](../2026-09-15-check-profile/README.md) (#114), on #113, #106, #94 and #89

## Summary

- **The shapes phase is a quarter faster, and the saving is a flat per-occurrence cost: ×0.73–0.80 in all four
  mode/size cells, which in `full` mode is 15.4 µs an occurrence at 90,880 and 15.3 µs at 302,560** (11.3–15.4 µs
  across all four cells; the spread is baseline noise, not a second effect). It was 12 `copy.deepcopy` calls
  and two `Location.__init__` calls per occurrence, both of which are per-DEFINITION work; the worst single attribute
  was `rotation`, because build123d's `Location.__deepcopy__` ignores the memo and rebuilds it every time.
- **Every placed copy is still build123d's to the bit** (174 solids in three formats, 0 differences) and **every pair
  key is still the key build123d's Locations gave** (251,919 pairs across 8 fixtures, keyed four ways, 0 differing
  keys). Eight times the occurrences asks for no more deepcopies (56 → 56), no more Locations (24 → 24) and leaves no
  attribute unrecognized (0 → 0), where build123d's own path grows 705 → 5,017 and 118 → 734.
- ‼️ **The interference check barely moved: ×0.94 at 90,880 and ×0.98 at 302,560, against the 39% of keying that
  containment cost in the profile.** Taking containment once per group of pairs is exact and is 536 groups instead of
  394,712 calls, but what is left per pair — a dict lookup, three adds and two comparisons an axis — is most of what
  `_inside_split` was doing, and the pose skip cannot fire on the 82% of barrel pairs that have an axis marked. **A
  profile share is an upper bound on what removing it saves; this is the measurement that says so.**
- **Both chosen bounds were measured, and both are kept — now on numbers instead of estimates.** An interference list
  entry costs ~168 bytes, ~1.5 µs to encode, ~1.7–2.1 µs to decode and ~440–510 bytes of Go allocation, linearly from
  1,000 to 528,000 entries with no knee. A repair prompt runs at **3.31 bytes a token, not ~4**, so 16 KiB is ~4,950
  tokens; the barrel never binds that budget at any size (942 bytes at every budget from 2 to 64 KiB), and where it
  does bind it buys 7.0 groups a KiB at 16 KiB, rising 5.0 → 7.2 across the sweep.
- **Eight drills were written; three went red and are committed, five stayed green and were removed.** Two of those
  are unreachable by any fixture; **three are gaps in the fence, so two of this branch's claims are asserted but not
  shown to be breakable** (see Drills).
- **No 1M after-timings were taken, and no limit is raised.** Every after-number here is 90,880 and 302,560, measured
  interleaved on a machine at 46–57% CPU.

## Why this measurement

\#114 left the 1M barrel's build at 102 s and named the two things left in it
(recommendation 3): **the shapes phase, 39 s (38%)**, and **what remains of the interference check, 53 s**, of
which about 32 s is still keying 2,191,348 candidate pairs. Neither had been profiled since #113 changed them. It also
left two bounds that were **chosen rather than measured**, and said so in both places:

1. `_INTERFERENCE_LIST_LIMIT` = 10,000 — "a per-entry cost times a chosen count, not a measured optimum".
2. `maxRepairProblemBytes` = 16 KiB — "about 4,000 tokens at ~4 bytes a token (not measured with a tokenizer)".

## ‼️ What the machine was doing, and why every number here is a same-session ratio

The laptop is shared with other agents (a live model car run, a browser workbench session, offline tests) and with
Windows Defender, which alone took 7–13% throughout. Machine CPU over these runs was **25–57%**, well above the ~30%
this repository treats as quiet.

That shows up directly: this session's *before* rows are slower than #114's for the same frozen sidecar and the same
request — shapes at 90,880 occurrences is 4.02 s here against #114's 3.3 s (1.22×), and at 302,560 it is 15.24 s
against 11.6 s (1.31×). **Nothing is inferred by comparing a number here with a number in #114.** Before and after are
measured in this session, and the load-robust results are the same-run ratios and the counts.

Two things made that worse during the session, and both are recorded rather than smoothed over:

- ‼️ **The first after-set was contaminated by this measurement's own author.** A drill dry-run was started while the
  builds were running, and it covered the whole after window: every before row sat at 21–39% CPU and every after row
  at 44–51%, which biased the comparison *against* the change. Those rows are kept as
  `data/results-contended.jsonl` and are not used for anything.
- **A second antivirus started mid-session.** `NortonSvc.exe` and `nllToolsSvc.exe` appear from 22:02 onward beside
  `MsMpEng.exe`, raising the floor to 46–53% for the rest of the runs.

So the timings below come from a **re-run that interleaves before and after** — pass 1 before, pass 1 after, pass 2
before, pass 2 after — so that a drifting load falls on both sides alike, and the ratio means something even though
the absolute seconds are inflated for both.

## 1. Where the shapes phase goes

`profile_shapes.py` on the frozen #114 sidecar (sha1 `838a553`), `_interferences` replaced by a no-op, format `""`.
Three passes per size: uninstrumented (the seconds), instrumented at the call sites (the shares), and under cProfile.
`data/profile-shapes.jsonl`.

| occurrences | shapes | µs an occurrence | machine CPU |
|---:|---:|---:|---:|
| 90,880 | 4.02 s | 44.2 | 25.6% |
| 302,560 | 15.24 s | 50.4 | 31.8% |
| 1,008,160 | 68.26 s | 67.7 | **56.8% — contaminated, not used** |

The 1M row was taken under the heaviest load of the session and is recorded, not used. 4 distinct shape keys and 80
distinct matrices at every size.

### What an occurrence pays for, at 90,880 (instrumented, and one call at a time)

| call | µs each, instrumented | µs each, alone | what it is |
|---|---:|---:|---|
| `_located` | 26.3 | 13.8 | the placed copy |
| `_placement` | 16.1 | 10.6 | the occurrence's frame |
| `_shape_key` | 6.5 | 4.0 | `json.dumps` of ten fields |

cProfile at 90k, by own time (`data/profile-shapes-9.txt`; also `-30.txt` and `-100.txt`) — the profiler roughly
doubles a Python call, so these are shares:

| calls | own s | what |
|---:|---:|---|
| 1,265,934 | 1.249 | `copy.deepcopy` — **12 an occurrence**, from `_located` |
| 181,761 | 0.855 | `Location.__init__`: nine `kwargs.pop`s and a discarded `gp_Trsf`, twice an occurrence |
| 4,080,615 | 0.579 | `dict.get` (mostly inside those two) |
| 91,276 | 0.576 | `Vector.__init__` |
| 89,281 | 0.576 | `_deepcopy_tuple` |
| 90,801 | 0.526 | `Vector.to_pnt` |
| 90,880 | 0.446 | `json.encoder.iterencode`, from `_shape_key` |
| 90,881 | 0.303 (1.078 cum) | `Location.__deepcopy__` |

- **The single worst attribute is `rotation`.** build123d's `Location.__deepcopy__` **ignores the memo** and returns
  `Location(self.wrapped.Transformation())`, so every occurrence rebuilds a Location that is the same for every copy of
  a definition. (It also means build123d's own `moved()` turns a `Box`'s `Rotation` into a plain `Location`; a
  replacement has to do that too.)
- **`_placement` is 6.9 µs of `gp_Ax3` + `SetTransformation` + `Invert` and 2.4 µs of `Location(...)`**, of which the
  Location wrapper is pure keyword parsing.
- **A tuple of the same ten fields is 1.28 µs against `json.dumps`' 4.03 µs.**

## 2. What is left of the interference check

`profile_keys.py`, same frozen sidecar, on `_build`'s own solids. `data/profile-keys.jsonl`.

| occurrences | pairs | `_measures` | `_candidate_pairs` | the keying loop | µs a pair |
|---:|---:|---:|---:|---:|---:|
| 90,880 | 197,356 | 1.01 s | 1.32 s | **4.05 s** | 20.5 |
| 302,560 | 657,508 | 2.79 s | 4.03 s | **11.21 s** | 17.0 |
| 1,008,160 | — | — | — | not taken (see below) | — |

Split by subtraction at 90k, each loop the one before plus a step: products 1.48 s, + rotations (unmemoized) 3.82 s,
+ poses 4.07 s, + containment 4.95 s; the whole loop with the memo 4.05 s; **without slabs 2.49 s, so the containment
half is 1.56 s — 39% of keying.** Cache lookups 0.04 s; 15 distinct keys.

cProfile at 90k, by own time (`data/profile-keys-9.txt`, and `-30.txt` at 302,560):

| calls | own s | what |
|---:|---:|---|
| 394,712 | 0.855 | `_inside_split` — two a pair, and 3,787,632 `abs()` inside it |
| 1,186,872 | 0.429 | `round()` — six a pair, from the two `_pose_of`s |
| 163,224 | 0.412 | `_carried_fast` |
| 394,712 | 0.212 (0.640 cum) | `_pose_of` |

### ‼️ The number the change rests on

**The 197,356 candidate pairs at 90k are 536 groups of (two definitions, two rotations)** — 4 definitions, 80
rotations, 7 pairs of definitions, 152 pairs of rotations. The same 536 groups at 302,560 occurrences. And **82% of
pairs are two *different* definitions** (35,424 of 197,356 share one).

Also measured, and deliberately **not** acted on: `_measures` is 1.01 s at 90k but its moved boxes are only 0.25 s of
that — the rest is `_entries`' twelve `Value()` calls an occurrence, which cannot be memoized by rotation without
threading a rotation id down from `_build`, so it was left alone.

## 3. The changes

Three, each behind a switch that restores the old path as the reference its fence compares against.

1. **`_located` decides once per definition what `copy.deepcopy` would do to each attribute** and replays it per
   occurrence (`_PLACE_WITHOUT_DEEPCOPY`): an atomic is assigned across (deepcopy returns it unchanged), a `Location`
   is rebuilt as `Location.__deepcopy__` builds it, an empty dict or list becomes a fresh empty one, **anything else is
   asked once whether `copy.deepcopy(value, {}) is value` and assigned across when it is**, and whatever is left
   **falls back to `copy.deepcopy`**, counted in `_located_fallbacks`. So it is an optimization of what it recognizes
   and never a claim about the rest.

   ‼️ The probe is there because the first version was not enough, and the fence said so rather than a guess: every
   cylinder, cone and sphere carries `align`, a tuple of `Align` enum members, which no type rule above covered, so
   deepcopies and fallbacks still grew with the occurrences (77 → 245 and 25 → 193 across 59 → 451 occurrences).
   Asking deepcopy what it does, once per definition, covers it without this code knowing the type.
2. **`_location_of` builds that Location without `Location.__init__`** (`_LOCATION_WITHOUT_INIT`). Read from build123d
   0.11.1: given a `TopLoc_Location`, `__init__` sets exactly `location_index = 0` and `_wrapped`, and nothing else.
3. **`_pair_keys` takes containment once per group** (`_CONTAINMENT_PER_GROUP`). `_inside_split`'s `mid` is the
   translation's own entry plus three products of the relative rotation with the other box's centre, and its `reach`
   three products with its half-extents: neither depends on the translation, which enters only as `mid`'s first term.
   So the products are taken 536 times instead of 394,712, and a pair adds three floats. It also **skips the pose of
   the direction that cannot win**: `min()` settles on the first element whenever the two definitions differ.

‼️ **Two traps, both avoided deliberately:**

- **`reach` stays a term of its own.** Folding it into the bound (`mid >= low + reach`) is a *different float* from
  `mid - reach >= low`, and at the containment boundary the two can disagree.
  ‼️ **Its drill STAYED GREEN**, and so did the drill for the addition order: reassociating either changed no key on
  any of the eight fixtures, because none of them places a pair within a last-bit of that boundary. So both forms are
  **reasoned, not fenced**. They are kept because the reasoning is sound and the form costs nothing, and they are
  recorded here as unproven — the same way #114 recorded its same-datum guard, whose drill also stayed green.
- **The per-definition plan is scoped to ONE build.** It was first written as a module-global dict keyed by
  `id(shape)`. The sidecar outlives a request, CPython reuses the id of a collected object, and a plan carries the
  definition's own attribute *values* — so a stale hit would have placed another definition's attributes onto a copy,
  silently. Found by re-reading before running anything, not by a test.

## Fences

`go test ./internal/domain/cad -run 'TestKernel_(APlacedCopy|PlacingMoreCopies|APairKeyWithoutLocations|KeyingMorePairs)'`
with `FORGE_CAD_PYTHON` set. All four pass on this branch.

| fence | holds | measured |
|---|---|---|
| `TestKernel_APlacedCopyIsTheCopyBuild123dMade` | every attribute of every placed copy, its B-rep, placement, bounds, mesh, part properties, interferences and STEP are build123d's | 174 solids across three formats, **0 differences** |
| `TestKernel_PlacingMoreCopiesCopiesNoBRepsAndBuildsNoMorePlanes` | eight times the occurrences asks build123d for no more B-rep copies, Planes, volume integrals, **deepcopies or Locations** | 59 → 451 occurrences: deepcopies **56 → 56**, Locations **24 → 24**, unrecognized attributes **0 → 0**; build123d's own path 705 → 5,017 and 118 → 734 |
| `TestKernel_APairKeyWithoutLocationsIsTheKeyBuild123dGave` | every pair of solids on 8 fixtures keys identically by `repr`, with the memo on and off, the slide on and off, **containment per group and per pair**, and on the barrel with shared and chained locations | **251,919 pairs** (4 prism seeds 37,675 each, rails 1,378, tied pins 2,346, 150 blocks 11,175, barrel 86,320), keyed four ways each, plus the barrel's two location variants at 86,320: **0 differing keys**, 7,088 slid and 32 carried on the barrel alone |
| `TestKernel_KeyingMorePairsBuildsNoMoreLocations` | the check builds no build123d `Location` per pair | 404 and 3,120 pairs: **0** Locations both, against build123d's 1,616 and 12,480 |

**What the extra coverage costs.** `APairKeyWithoutLocationsIsTheKeyBuild123dGave` keys every pair of every fixture
once per variant, and this branch adds a fourth variant (containment per pair), so it now runs four all-pairs passes
instead of three: 138.4 s in the run where all four fences were executed together, against
`KeyingMorePairsBuildsNoMoreLocations`' 133.6 s. That is a deliberate trade — the fence is the only thing standing
between a one-bit key change and two clashes silently becoming one — but it is a third more of an already slow test.

‼️ **The counting fence earned its place.** The first version of `_located`'s plan passed
`APlacedCopyIsTheCopyBuild123dMade` — every copy was correct — while deepcopies still grew 77 → 245 and unrecognized
attributes 25 → 193 across 59 → 451 occurrences, because `align` was falling back once per occurrence. The
equivalence fence could not see that; only the count could.

## Drills

`scripts/drill-fences.sh`, section "Last hot spots", eight drills, run through a temporary runner assembled from the
script's own header, this section and its footer. `data/drills.log`.

‼️ **The runner has to live in `scripts/`.** The header does `ROOT="$(dirname "${BASH_SOURCE[0]}")/.."` and `cd`s
there, so a runner kept in a scratch directory resolves ROOT to the wrong tree: the first attempt backed up nothing,
found none of the sources, and refused all eight drills as "NOT BACKED UP" — which is the script's safety argument
working exactly as intended (it never applied a mutation it could not revert), but it is not a drill run.

**Dry run first: 0 anchors moved, and the tree byte-identical afterwards.** Every one of the eight mutations still
matches the source it edits, which is the check that stops a drill from silently doing nothing and reporting a green
fence.

**Eight were written. Three went red and are committed; five stayed green and were removed.** The script's contract
is that every drill goes red, so a drill that cannot fail is a claim and is taken out with a note, as #114 did with
its same-datum guard. `3 went red, 5 stayed green, 0 unproven, 0 anchor(s) moved`, tree byte-identical afterwards.

| drill (committed) | red fence | went red on |
|---|---|---|
| a copy's rotation is deepcopied per occurrence again | PlacingMoreCopies… | deepcopies 91 → 315 and Locations 59 → 283 across 59 → 451 occurrences |
| a containment plan is keyed by the rotations and not the definitions | APairKeyWithoutLocations… | keys differ |
| the direction that cannot win is skipped on a tie too | APairKeyWithoutLocations… | 4,402 of 37,675 keys differ on prisms seed 1 |

‼️ **The five that stayed green, and what that means — two different problems, not one.**

*Three are gaps in the fence, and the change's claims are weaker than they looked:*

| removed drill | should have been caught by | why it was not |
|---|---|---|
| a placed copy shares its definition's empty containers | APlacedCopy… | not established; the identity check exists (`b.__dict__[key] is value`) and did not fire |
| a placement's Location is missing the attribute the constructor sets | APlacedCopy… | the fence compares a placement only through its transformation numbers, and compares `__dict__` for the placed **solid** — never for the `Location` object, so `location_index` is not inspected at all |
| a placement builds its Location through the constructor again | PlacingMoreCopies… | not established; the `Locations` counter demonstrably works (it moved 59 → 283 on the red drill above) and did not move here |

So **"a copy's attributes are not shared with its definition" and "a placement is not built through `Location.__init__`"
are asserted by the fences but not shown to be breakable.** The equivalence result that *is* shown is the one that
matters most — every placed copy is build123d's to the bit on 174 solids in three formats — but these three
properties should be treated as unfenced until the fence is sharpened. See Recommendations.

*Two are properties no fixture can reach*, and are kept in the code on reasoning alone (see §3): folding `reach` into
the bound, and adding the translation last. Both are genuine floating-point differences that changed **no key on any
of the eight fixtures**, because none of them places a pair within a last-bit of the containment boundary.

## 4. The two bounds

### 4a. `_INTERFERENCE_LIST_LIMIT`, the 10,000-entry interference list

`measure_list_bound.py` on the 302,560-occurrence barrel (528,000 clashes found, 230,400 of them buried), built ONCE
with the bound lifted; the reply is then serialized with its first N entries, which is exactly what the bound cuts,
since the list is already worst-first. `data/list-bound.jsonl`.

| listed | reply bytes | the list's share | bytes an entry | sidecar encode |
|---:|---:|---:|---:|---:|
| 1,000 | 167,811 | 167,170 | 167.2 | 1.4 ms |
| **10,000 (shipped)** | **1,686,237** | 1,685,596 | 168.6 | **14.2 ms** |
| 50,000 | 8,433,853 | 8,433,212 | 168.7 | 75.0 ms |
| 100,000 | 16,899,356 | 16,898,715 | 169.0 | 149.1 ms |
| 528,000 (all of them) | 93,478,959 | 93,478,318 | 177.0 | 833.6 ms |

- **An entry costs a flat ~168 bytes and ~1.5 µs to encode. There is no knee anywhere** — nothing about this bound is
  a cliff, so it cannot be chosen by finding one. A reply with no list at all is 641 bytes.
- The 177 bytes an entry at 528,000 is not a nonlinearity: entries deeper in the barrel carry longer part ids.
- ‼️ The encode times were taken at 43% machine CPU. The **byte counts are load-independent** and are what the choice
  rests on; the milliseconds are indicative.

**What Go pays to read it**, `TestScaleUp_MeasureTheInterferenceReply` on those same replies
(`data/go-reply-reply-full-*.json` — the test names its record after the reply file, so `reply-` appears twice), at
36–39% machine CPU:

| listed | reply bytes | Go decode | allocated | held after GC | `InterferenceProblems` | its problem lines |
|---:|---:|---:|---:|---:|---:|---:|
| 1,000 | 167,811 | 3.5 ms | 0.29 MiB | 0.65 MiB | 0.0 ms, 1,000 buried | 141,000 B |
| **10,000 (shipped)** | **1,686,237** | **20.6 ms** | **4.18 MiB** | 2.11 MiB | 4.8 ms, 10,000 | 1,410,000 B |
| 50,000 | 8,433,853 | 89.6 ms | 24.14 MiB | 8.65 MiB | 22.9 ms, 50,000 | 7,050,000 B |
| 100,000 | 16,899,356 | 166.1 ms | 48.75 MiB | 16.66 MiB | 44.0 ms, 100,000 | 14,100,000 B |
| 528,000 (all) | 93,478,959 | 1,018.9 ms | 245.23 MiB | 84.00 MiB | 114.5 ms, 230,400 | 32,486,400 B |

**Linear on every axis: ~168 bytes, ~1.7–2.1 µs of decode and ~440–510 bytes allocated per entry.** There is no
threshold to find, so the bound has to be chosen on what a reader *gains*, not on where a cost explodes.

**What each consumer gains from more than 10,000** (read from the code on this branch):

| consumer | reads | more entries buys |
|---|---|---|
| the turn's note (`list`) | the first 3, and `Found` for "and N more" | nothing |
| `repairAsks` | buried clashes, grouped by placement | more groups — but the 16 KiB budget describes at most 112 of them (4b), and on the barrel it is 1 group at any size |
| `repairVerdict` | the kernel's own `Buried` count (#115), **not the list** | nothing |
| `look.go` sub-assembly looks | marks a group as clashing if a *listed* clash names it | a group whose only clashes rank below the bound; at most `maxSubAssemblyLooks` (4) groups are looked at |
| the render sheet | carries the list | nothing; it is the held memory above |

**Decision: keep 10,000, now on measured numbers rather than on "above every fence's clash count".** Nothing reads
past the head except `repairAsks`' grouping, which its own budget caps far below what 10,000 entries can feed, and the
buried count a repair is judged by no longer comes from the list at all. Raising it to 100,000 would cost 10× the
bytes, decode and allocation (16.9 MB, 166 ms, 48.7 MiB) to give one consumer more groups than it can print and
another a deeper search among 4 looks. Lowering it to 1,000 would save 1.5 MB and 17 ms and start truncating
`look.go`'s evidence and `repairAsks`' groups on a model with many faults. 10,000 is where those meet, and the
per-entry costs above are what the next person should re-derive it from.

### 4b. `maxRepairProblemBytes`, the 16 KiB repair-prompt budget

16 KiB was reasoned as "about 4,000 tokens at ~4 bytes a token (not measured with a tokenizer)" and as a quarter of
`maxStepContextBytes` (64 KiB). Both halves are now measured.

**The bytes-per-token assumption, on FORGE's own prompt text.** `tokens.py` on the repair prompt #114's measurement
actually produced for the 1,008,160-occurrence barrel
(`docs/spikes/2026-09-15-check-profile/data/repair-prompt-100.txt`):

| the barrel's repair prompt | bytes | characters | tokens (`o200k_base`) | tokens (`cl100k_base`) | bytes a token |
|---|---:|---:|---:|---:|---:|
| as #114 sends it | 946 | 942 | 286 | 286 | **3.31** |

- **It is 3.31 bytes a token, not ~4.** So the budget is worth about **4,950 tokens**, not "about 4,000" — the
  assumption was optimistic by ~24%. Both encodings agree to the token on this text, which is unsurprising: it is
  mostly ASCII prose, ids and digits.
- For scale on the same measure, 64 KiB of step context is ~19,800 tokens, and `converseMaxTokens` (the *response*
  cap) is 16,000.
- ‼️ **The prompt the barrel actually produces is 946 bytes — 5.8% of the 16 KiB budget.** At every size #114
  measured (90,880, 302,560 and 1,008,160 occurrences) the summary was 942–943 bytes, because the barrel's worst
  10,000 clashes are one group. **So on the barrel this bound never binds.** What binds it is the many-group case, which
  only the synthetic fences reach (600 tied groups, and the budget-filling sweep).

**What each budget buys.** `TestScaleUp_MeasureTheRepairPrompt` sweeps `repairAsksWithin` over candidate budgets, on
the barrel's own replies and on the many-group shape the fences use (600 parts each buried once in a housing, so 600
placements are at fault). `data/repair-sweep.txt`, `data/repair-prompt-manygroups-*.txt`.

| budget | barrel (90,880 and 302,560) | 600 groups: described | its bytes | its lines | ≈ tokens at 3.31 B |
|---:|---|---:|---:|---:|---:|
| 2 KiB | 942 B, 3 lines, 1 group | 10 of 600 | 1,984 | 13 | ~600 |
| 4 KiB | 942 B, 3 lines, 1 group | 24 | 3,958 | 27 | ~1,200 |
| 8 KiB | 942 B, 3 lines, 1 group | 54 | 8,188 | 57 | ~2,470 |
| **16 KiB (shipped)** | **942 B, 3 lines, 1 group** | **112** | **16,381** | 115 | **~4,950** |
| 32 KiB | 942 B, 3 lines, 1 group | 227 | 32,711 | 230 | ~9,880 |
| 64 KiB | 942 B, 3 lines, 1 group | 458 | 65,513 | 461 | ~19,790 |

- ‼️ **The budget never binds on any model FORGE has built.** The barrel's prompt is 942 bytes at *every* budget from
  2 KiB to 64 KiB, because its worst 10,000 clashes are one group — one child placed in one assembly, copied by
  patterns. The live car that found this defect class buried four clashes and is asked as lines. So this bound is not
  a tuning knob for anything real; it is a guard for a model with many placements at fault.
- **Where it does bind it is filled almost exactly** — 16,381 of 16,384 bytes — and buys **7.0 groups a KiB at
  16 KiB** (~145 bytes a group), rising from 5.0 a KiB at 2 KiB through 6.8 at 8 KiB to 7.2 at 64 KiB as the fixed
  header and the worst-clash line amortise: 10 groups at 2 KiB, 54 at 8, 112 at 16, 227 at 32, 458 at 64.
- **The "~4 bytes a token" assumption was wrong in the unsafe direction:** 3.31 measured, so 16 KiB is ~4,950 tokens,
  not ~4,000.

**Decision: keep 16 KiB, and say why on the numbers.** At 16 KiB a model is told about 112 of 600 groups for ~4,950
tokens, which is ~31% of `converseMaxTokens` (16,000) — and the whole document is sent with it. Doubling to 32 KiB
buys groups 113–227 for ~9,880 tokens, ~62% of that cap, on a model already broken in more places than one prompt
will repair; the 64 KiB step-context comparison #114 reasoned from still holds as a ceiling, not a target. Halving to
8 KiB would drop to 54 groups and save nothing that matters, since no real model reaches even 2 KiB. **The bound is
unchanged; what changed is that it now rests on 3.31 bytes a token and a measured 5.0–7.2 groups a KiB instead of an
estimate.**

## 5. Before and after

`measure.py` with the two frozen sidecars **interleaved** — pass 1 before, pass 1 after, pass 2 before, pass 2 after —
two runs of every cell, machine CPU 46–57% (mean 52%) and symmetric across both sides. Seconds are the sidecar's own
phases, meaned over the two runs. `data/results.jsonl`, `data/load-timings.log`.

| mode | occurrences | build | shapes | assembly | interference | mesh | properties |
|---|---:|---:|---:|---:|---:|---:|---:|
| full | 90,880 | 13.39 → **11.61** (×0.87) | 5.28 → **3.88** (×0.73) | 0.79 → 0.82 | 6.78 → **6.35** (×0.94) | — | — |
| full | 302,560 | 46.15 → **41.15** (×0.89) | 18.31 → **13.67** (×0.75) | 2.89 → 2.96 | 23.18 → **22.69** (×0.98) | — | — |
| mesh | 90,880 | 13.92 → **13.52** (×0.97) | 4.99 → **3.97** (×0.80) | 0.79 → 0.85 | 1.60 → 1.81 † | 1.97 → 1.98 | 4.00 → 4.32 |
| mesh | 302,560 | 48.08 → **42.35** (×0.88) | 17.64 → **13.45** (×0.76) | 2.65 → 2.58 | 5.83 → 5.45 † | 5.90 → 5.44 | 14.34 → 13.73 |

† A `mesh` run keys no pair at all (see "What this does NOT establish"), so its interference column is not evidence
about this branch. The interference result below is read from `full` mode only.

- **The shapes phase is ×0.73–0.80 in all four cells**, and in `full` mode the saving per occurrence is the same at
  both sizes: 1.40 s over 90,880 occurrences and 4.64 s over 302,560 — **15.4 and 15.3 µs an occurrence**. A flat
  per-occurrence cost removed, not a curve bent, which is what the profile said it would be.
  ‼️ The `mesh` cells give 11.3 µs and 13.8 µs for the same change. The work removed is identical in both modes, so
  that spread is the noise in the baseline (mesh's before-shapes is 4.99 s at 90,880 against full's 5.28 s for the
  same phase), not a second result. **15.3 µs is a `full`-mode figure; across all four cells the saving is
  11.3–15.4 µs an occurrence.**
- **The interference phase moves far less than the profile suggested: 0.43 s at 90,880 and 0.49 s at 302,560**
  (×0.94 and ×0.98), against the 1.56 s that containment cost at 90,880. Two reasons, both measured:
  - the grouped test still pays a 4-tuple dict lookup, three adds and two comparisons per pair per slab axis, which is
    a large share of what `_inside_split` was doing; and
  - ‼️ **the pose skip barely fires on this fixture.** 161,932 of 197,356 barrel pairs (82%) have an axis marked, so
    they take the both-poses path whatever happens; only the other 18% can skip a `_pose_of`. The skip is worth more
    on a model whose parts are not mostly buried in one another — which the barrel is not.
- Assembly, mesh and part properties move within run-to-run noise, as they should: nothing here touches them.

## What this establishes

- **What the shapes phase spends, by call, after #113**, at 90,880 and 302,560 occurrences (and at 1,008,160 under
  load, recorded but unused): 44–50 µs an occurrence, of which `_located` 13.8 µs, `_placement` 10.6 µs and
  `_shape_key` 4.0 µs; 12 `copy.deepcopy` calls and two `Location.__init__` calls per occurrence; and that the worst
  single attribute is `rotation`, because build123d's `Location.__deepcopy__` ignores the memo.
- **That those are per-DEFINITION costs, not per-occurrence ones, and removing them is exact.** 15.4 and 15.3 µs an
  occurrence in `full` mode at the two sizes, 11.3–15.4 µs across all four cells (shapes ×0.73–0.80), with every
  placed copy still build123d's to the bit on 174 solids in three
  formats, and deepcopies, `Location.__init__` calls and unrecognized attributes all flat (56, 24, 0) at eight times
  the occurrences where build123d's path grows 705 → 5,017 and 118 → 734.
- **That a barrel's candidate pairs are very few groups: 536 groups of (two definitions, two rotations) among 197,356
  pairs at 90,880, and the same 536 among 657,508 at 302,560** — and that containment can therefore be taken once per
  group with every key identical to build123d's by `repr` across 251,919 pairs on eight fixtures.
- ‼️ **And that doing so is worth much less than the profile implied: ×0.94 and ×0.98 of the interference phase, not
  the 39% of keying containment cost.** The measured reasons are in §5 — the grouped test still pays per pair, and the
  pose skip cannot fire on the 82% of barrel pairs that have an axis marked. **A profile share is an upper bound on
  what removing it saves, and this is the number that says so.**
- **What an interference list entry costs, end to end: ~168 bytes, ~1.5 µs to encode, ~1.7–2.1 µs to decode and
  ~440–510 bytes allocated in Go**, linearly from 1,000 to 528,000 entries, with no threshold — and which consumer
  gains what from a larger list.
- **That FORGE's own repair-prompt text runs at 3.31 bytes a token, not ~4**, so 16 KiB is ~4,950 tokens; that the
  barrel never binds that budget at any size (942 bytes at every budget from 2 to 64 KiB, because its worst 10,000
  clashes are one group); and that where the budget does bind it buys 7.0 groups a KiB at 16 KiB (5.0–7.2 across the
  sweep) and is filled to within 3 bytes of itself.
- **Both bounds are kept, and now rest on those numbers instead of on an estimate.** No limit is raised.

## What this does NOT establish

- **Anything at 1,000,000 occurrences, after the change.** The 1M after-timings were not taken. The 1M keying profile
  was killed part-way (the machine is shared and its python was holding 36.5 GB), and the 1M *before* shapes row was
  taken at 56.8% CPU and is not used. Every after-number here is 90,880 and 302,560 only.
- **That the 1M build is faster.** It follows from the per-occurrence and per-pair costs only if they scale as they did
  from 90k to 300k, which is an inference, not a measurement.
- ‼️ **Anything about the keying change from `mesh` mode.** A `mesh` run replaces `_interferences` with the broad
  phase only (`measure.py`'s `broad_phase_only`, from #89), so it never keys a pair and cannot show this branch's
  containment change either way. Its interference column moves with the grid, not with anything changed here — which
  is why the ratios below are read from `full` mode alone. The mesh rows are kept because they do measure the shapes
  phase, and the mesh and part-property phases.
- **Mesh at 1M, and STEP at any size.** STEP was not re-measured at all here; #106's numbers stand. STEP at 1M stays
  refused (owner decision).
- **A quiet machine.** Nothing here was measured on one. The profiles ran at 25.6–56.8% machine CPU and the
  interleaved timing re-run at 46–55% (mean 52%), with `MsMpEng.exe` taking 7–13% and a second antivirus
  (`NortonSvc.exe`, `nllToolsSvc.exe`) starting partway through. Same-session ratios and counts are the results to
  read; absolute seconds are not comparable with #114's, and not with each other across the session either.
- **The deployed tokenizer.** The token figures are tiktoken's `o200k_base` and `cl100k_base` run offline. FORGE talks
  to an OpenAI-compatible endpoint whose server-side tokenizer is not available here, so they are a proxy that
  establishes the bytes-per-token FORGE's own prompt text runs at — not the count that endpoint would bill.
- **That a model repairs better at any budget.** Unchanged from #114: no model was sent either form.
- **`_measures`' twelve `Value()` calls an occurrence**, and `_candidate_pairs` (1.32 s at 90k, 4.03 s at 300k), which
  were measured and left alone.

## Recommendations (nothing here is changed)

1. **Sharpen `testdata/placed_copies.py` until the three removed drills go red.** It should compare a placement's
   `Location` object by its own `__dict__`, not only by its transformation numbers, and the reason the container
   identity check and the `Locations` counter did not fire on their mutations should be established rather than
   guessed. Until then, "a copy's attributes are not shared with its definition" and "a placement is not built through
   `Location.__init__`" are unfenced.
2. **`_shape_key` as a tuple is the next cheap win in the shapes phase: 4.03 µs against 1.28 µs**, about 2.7 µs an
   occurrence, measured here and deliberately not taken — the key is parsed as JSON by `_slabs`, so it is a
   cross-function contract and wanted a branch of its own rather than a third intricate change in this one.
3. **`_measures` is 1.01 s at 90,880 and its moved boxes are only 0.25 s of that**; the rest is `_entries`' twelve
   `Value()` calls an occurrence. The rotation part of a placement is one of 80, so it could be read once per
   rotation — but only by threading a rotation id down from `_build`, which is a change to the `placed` tuple.
4. **`_candidate_pairs` is now the largest single step of the check that nothing has touched**: 1.32 s at 90,880 and
   4.03 s at 302,560, against 6.35 s and 22.69 s for the whole phase after this branch.
5. **Re-measure at 1,008,160 on a quiet machine**, and re-take the 1M keying profile that was killed here. Nothing in
   this spike measures the size the plan is about.
6. **Keep every limit where it is.** Nothing measured here is a basis for moving the 4,096 export or 8,192 view
   ceilings, the 2,000-boolean budget, the 100,000-occurrence door, or either of the two bounds measured above.

## Re-running

```bash
export GOWORK=off PYTHONUTF8=1 D=/some/dir PY=.cadvenv/bin/python
FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=9,30,100 go test -count=1 -timeout 40m \
  -run TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry
mkdir -p $D/before $D/after && cp internal/domain/cad/sidecar.py $D/after/sidecar.py   # before: #114's head
$PY docs/spikes/2026-09-15-last-hot-spots/load_log.py $D/load.log &
for b in 9 30; do
  $PY docs/spikes/2026-09-15-last-hot-spots/profile_shapes.py $D/before/sidecar.py $D/barrel-$b.json $D/p $b
  $PY docs/spikes/2026-09-15-last-hot-spots/profile_keys.py   $D/before/sidecar.py $D/barrel-$b.json $D/p $b
done
# ‼️ INTERLEAVED, not two blocks. The machine is shared, so a block of "before" runs
# followed by a block of "after" runs measures the load as much as the change — the
# first attempt here did exactly that and had to be thrown away (results-contended.jsonl).
# Two passes of --repeat 1, alternating sidecars, puts any drift on both sides.
for pass in 1 2; do
  for side in before after; do
    extra=""; [ $side = after ] && extra="--write-reply"
    $PY docs/spikes/2026-09-15-one-million-occurrences/measure.py --dir $D --bays 9,30 \
      --modes full,mesh --repeat 1 --cap 1200 --sidecar $D/$side/sidecar.py $extra
  done
done
# The two bounds. The list's per-entry cost is the same either side of this branch,
# so it is measured on the after sidecar, into a directory of its own: the Go decode
# test globs reply-full-*.json, and measure.py --write-reply writes names of that
# shape too (reply-full-<bays>.json), which would otherwise be mixed in.
mkdir -p $D/listbound
$PY docs/spikes/2026-09-15-last-hot-spots/measure_list_bound.py $D/after/sidecar.py $D/barrel-30.json $D/listbound
FORGE_SCALE_MEASURE_REPLY=$D/listbound go test -count=1 -run TestScaleUp_MeasureTheInterferenceReply ./internal/domain/cad
FORGE_SCALE_MEASURE_REPAIR=$D go test -count=1 -run TestScaleUp_MeasureTheRepairPrompt ./internal/agent
<venv with tiktoken> docs/spikes/2026-09-15-last-hot-spots/tokens.py $D/repair-prompt-*.txt
```
