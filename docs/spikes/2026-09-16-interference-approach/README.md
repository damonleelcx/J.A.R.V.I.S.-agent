# Measurement: a different approach to the interference check — the narrow phase in arrays

**Date:** 2026-09-16 · **Status:** done · **Follows:**
[`2026-09-15-last-hot-spots`](../2026-09-15-last-hot-spots/README.md) (#121), on
[`2026-09-15-check-profile`](../2026-09-15-check-profile/README.md) (#114), #113, #106,
[`2026-09-15-large-box-index`](../2026-09-15-large-box-index/README.md) (#94) and
[`2026-09-15-one-million-occurrences`](../2026-09-15-one-million-occurrences/README.md) (#89)

## Summary

- **Keying the candidate pairs is 5× faster: ×0.199 and ×0.207 at 90,880 occurrences, ×0.166 and ×0.193 at
  302,560**, interleaved loop/array/loop/array. **The whole check is ×0.46 and ×0.49 at 90,880 and ×0.52 and ×0.51 at
  302,560**, and the shipped build is ×0.68/×0.73 and ×0.75/×0.73 — 28.9 s → 21.6 s at 302,560 occurrences.
- **The answer is byte-identical**, not merely equal: `_interferences`' whole return as JSON has the same sha1 both
  ways at both sizes (1,685,723 B `f0c79a1b13c6` at 90,880; 1,685,725 B `85425a5a0c12` at 302,560), and the four
  interleaved builds agree on volume, found (158,400 / 528,000), listed (10,000), booleans (15), reuses, candidate
  pairs, box tests and peak RSS.
- **What made it possible is a bit-exactness result, measured and not assumed.** A pair's relative translation is,
  from `gp_Trsf::Multiply` and `gp_Trsf::Invert`, `-(R_i^T · t_i) + R_i^T · t_j` with each row's sum taken left to
  right. Written that way in numpy it is OCCT's own product **bit for bit**: 1,184,136 of 1,184,136 translation
  entries at 90,880 occurrences and 3,945,048 of 3,945,048 at 302,560, and 272,640 / 907,680 inverse entries the
  same. So the arrays do the arithmetic that needs no rounding, and everything that rounds, compares or builds a
  tuple stays in the shipped scalar tail, run **once per distinct row** — 1,232 rows for 197,356 pairs.
- ‼️ **Two approaches were measured and lose, and the numbers say so without an implementation.** A sweep and prune
  over sorted intervals would examine **31,359,360 pairs at 90,880 and 104,501,520 at 302,560 — 73.7× and 72.9× the
  box tests the grid actually makes**, and its best axis needs every box in the assembly open at once. Batching pairs
  into fewer OCCT calls has nothing to batch: the booleans are **15, and 0.025 s of a 2.4 s check**.
- ‼️ **A found defect in the fence's own premise: the check's answer is not repeatable on a cold process.** A pair's
  FIRST OCCT boolean is not always its later ones. On prisms seed 1 with a feature-changed solid, where nearly every
  one of 37,675 pairs pays a boolean of its own, the LOOP's first run and its second differ in the last two bits of
  some volumes (5336.596433397901 against 5336.596433397913) and every run after the first is identical. This is the
  shipped check, not this branch. Every comparison here is made on warmed solids and the fence reports which
  fixtures were affected (4 of 40).
- ‼️ **The array path's keys are EQUAL to the loop's, and not always the same repr.** `-0.0` and `0.0` are one dict
  key and two reprs; the barrel's 197,356 pairs are 19 distinct key reprs and 15 distinct keys, so the shipped check
  already measures them as one clash. The array path hands out one tuple per key, so 11,308 of the barrel fixture's
  86,320 pairs get an equal key carrying the other zero's sign. **Equality is the only thing the answer depends on**,
  and it is what the fence requires to be exact.
- **Nothing measured here raises a limit**, and nothing at 1,000,000 occurrences was measured.

## Why this measurement

\#121 profiled what was left of the check after #114, changed the two things its profile named, and got **×0.94 at
90,880 and ×0.98 at 302,560** — against a profile share of 39%. Its own conclusion (recommendation, and the ‼️ in its
summary) was that *"a profile share is an upper bound on what removing it saves"* and that the remaining time needed a
different approach rather than another micro-optimisation. #114 had already taken the build123d `Location`s out of
keying (119 s → 53 s at 1M); #121 found that what was left per pair — a dict lookup, three adds and two comparisons
an axis — *is* most of what the code was doing.

So the question here is not "what else in the loop is slow" but "does the loop have to run once per pair at all".

## ‼️ What the machine was doing

The laptop is shared and its own speed drifts by more than the effect being measured. Two examples from this session,
both recorded rather than smoothed over:

- The same script (`profile_keys.py` from #121, unchanged, on the same frozen sidecar and the same 90,880-occurrence
  barrel) reported `keys_s` **2.60 s at 00:30 and 6.88 s at 00:45**, and `measures_s` 0.73 s then 1.50 s — a 2.6×
  drift in fifteen minutes at 18.5% and 20.8% machine CPU. Nothing in the tree changed between them.
- Inside one run of `profile_narrow.py` at 302,560, the two interleaved loop passes came out **11.19 s and 24.31 s**.
  The array passes in the same run were 1.86 s and 4.68 s, so the *ratios* were 0.166 and 0.193 — which is the whole
  reason the passes are interleaved rather than blocked.

**So every timing here is a same-run ratio, and every absolute second is reported with the load it was taken at.**
`data/load.log` (00:33–01:03, the interleaved builds) and `data/load2.log` (01:32–01:39, the profiles) sample every
10 s: `MsMpEng.exe` takes 3–13% throughout and `Cursor.exe` about 5%. The load-robust results are the ratios, the
counts, and the byte identity.

## 1. Where the check goes now, after #121 and #126

`profile_narrow.py` on the committed sidecar (sha1 `3cd1f4b`): `_build` as shipped up to the check with its arguments
captured, then the steps timed on those same solids in the same process. `data/profile-narrow.jsonl`.

| step | 90,880 | 302,560 | share at 302,560 |
|---|---:|---:|---:|
| `_measures` (a moved box and the rotation bits per occurrence) | 1.01 s | 2.92 s | 34% |
| `_candidate_pairs` (the grid) | 1.24 s | 3.85 s | **45%** |
| the narrow phase, array path | 0.64–0.80 s | 1.86 s | 22% |
| the per-pair keying loop it replaces | 3.10–4.02 s | 11.19–24.31 s | — |
| the OCCT booleans inside it | **0.025 s** (15) | **0.046 s** (15) | 0.5% |
| candidate pairs / groups / distinct rows / distinct keys | 197,356 / 312 / 1,232 / 15 | 657,508 / 536 / 1,608 / 15 | |
| machine CPU | 40–46% | 23–29% | |

- **The picture has inverted since #114.** There, keying was ~80% of the check and the broad phase ~10%. Here the
  broad phase is the **largest single step at 45%**, `_measures` is 34%, and the whole narrow phase — keys, cache,
  booleans, filtering, ranking and the listed entries — is 22%.
- **The booleans were never the cost and are now visibly not.** 15 of them, 25–46 ms, on a check that answers
  2.19 million pairs at 1M. That prices "batch the pairs into fewer OCCT calls" at zero (§4).

### What is inside the array narrow phase, at 90,880

| step | s | of the array path |
|---|---:|---:|
| the pairs into an array (`np.array` over 197,356 tuples) | 0.028 | 4% |
| the solids that are in a pair (`np.unique`) | 0.014 | 2% |
| **the per-solid placement guard** (`FirstPower`, `NextLocation().IsIdentity()`, `ScaleFactor`, `Form`) | **0.418** | **52–65%** |
| everything else: grouping, the products, containment, the dedupe, 1,232 tail calls | 0.179 | 22–28% |

‼️ **The same split at 302,560 is not usable and is not used.** It was taken after the interleaved timings in the same
script, by which point the machine had slowed 2.2×, and `rest_s` comes out **negative** (−1.81 s) because the
subtraction mixes two speeds. The 90,880 split is internally consistent (every part positive, summing to the whole)
and is the one quoted. The number that survives at both sizes is the guard's own: 90,840 and 302,436 solids guarded,
0.418 s and 3.35 s, which is 4.6 µs and 11.1 µs a solid at two very different machine speeds.

- **The per-solid guard is now the largest thing in the narrow phase**, and it is not new work: the per-pair loop
  pays exactly the same guard per solid inside `rotation_id`. It is what decides whether a placement is one datum
  at power 1 with scale 1, which is what #114's rotation memo is guarded by.

## 2. The approach that wins: the narrow phase one array at a time

### The bit-exactness result it rests on

`_pair_keys` spends 47% of itself on two OCCT products and six `Value()` calls a pair (#121's split: products 1.48 s
of 4.05 s at 90,880). From OCCT's own arithmetic:

- `gp_Trsf::Multiply(T)` sets `loc = loc_A + (matrix_A · loc_B) · scale_A`;
- `gp_Trsf::Invert` transposes the matrix and takes `loc` through it, negated, over the scale;
- `gp_XYZ::Multiply(gp_Mat)` writes each row as `m0·x + m1·y + m2·z`, left to right.

So for a placement that is one datum at power 1 with scale 1 — which is what `_placement` makes, and what the guard
above establishes — the relative translation is `-(R_i^T · t_i) + R_i^T · t_j`, and negation is exact in IEEE 754, so
the order OCCT reverses in does not matter. **Written in numpy in exactly that order it is OCCT's answer bit for
bit:**

| fixture | inverse entries identical | product entries identical |
|---|---:|---:|
| 90,880-occurrence barrel, all 197,356 candidate pairs both ways | 272,640 / 272,640 | 1,184,136 / 1,184,136 |
| 302,560-occurrence barrel, all 657,508 candidate pairs both ways | 907,680 / 907,680 | 3,945,048 / 3,945,048 |

‼️ **This is measured, not proved, and the OCCT source was not read here** — only its documented behaviour, and
numpy not contracting `a*b + c` into an FMA — which it does not, because each elementwise operation is a separate
call. The fence is what holds it: every key on eight fixtures, four placement variants each, against build123d's own
`Location` path.

### What the arrays do, and what they do not

Per **group** — one pair of definitions at one pair of rotations, of which the barrel's 2,191,348 pairs at 1M are
536 — the array path computes:

1. both relative translations, by the formula above;
2. both containment tests, from #121's per-group `_containment_plan` — `mid = ((t[r] + p0) + p1) + p2` in the same
   order and with `reach` still a term of its own, so #121's two floating-point traps are kept;
3. the ±inf marks `_slid` would write, **into the translations, before the rows are deduped**. That is what collapses
   a rivet row along a stringer into one row: the axis it slides along is `inf` for every rivet.

And then it stops. The key is built by `_key_tail` — the shipped scalar tail, factored out and called by both paths,
so there is one implementation of the rounding, the `min()`, the mark counting and the tuple — **once per distinct
row**: 1,232 rows for 197,356 pairs at 90,880, 1,608 for 657,508 at 302,560.

‼️ **The rows are deduped on the RAW translations, never on a rounded one.** `np.round` is a
scale-rint-unscale and Python's `round` is correctly-rounded decimal; they are not the same function, and only
Python's may decide a key. Deduping on raw values can only split a key into two rows, never merge two keys — and two
rows that build the same tuple are merged by the key dictionary anyway.

‼️ **The `min()` of the two directions has to see the rounded pose.** The first version of this compared raw
translations in numpy and got 25,039 keys wrong out of 197,356, because a pair at `ta = (0.0, -4.5e-13, 8.0)` and
`tb = (0.0, +4.5e-13, -8.0)` compares equal on the second axis once rounded and does not before. Running the shipped
tail per row is what makes that impossible rather than merely fixed.

### The rest of the check, also in arrays

`_bulk_interferences` replaces the per-pair loop after the keys as well, because at 1M the loop's own body — a dict
lookup, four comparisons and a tuple appended 1.76 million times — is the next thing in the way:

- **the booleans are paid in the loop's order.** `np.unique(..., return_index=True)` gives each distinct key the pair
  it is FIRST met at, which is exactly the pair the loop would measure it on; the keys are then walked in that order.
- **the budget stops the search at the same pair.** When the boolean budget is reached, the pair the (budget+1)-th
  distinct key was first met at is where the loop would have broken; every pair from there on is dropped and
  `truncated` is set. `reused` is the pairs reached minus the booleans, which is what the loop counts.
- **the filters, the ranking and the buried count are array operations**: `shared / min(v_i, v_j)` is the same IEEE
  division, `np.argsort(-fraction, kind="stable")` is the same stable order by fraction descending with ties in
  discovery order that `heapq.nsmallest(10000, key=(-f, order))` gives, and `at` is ascending in pair index, which is
  the order the loop appends in.
- **a refused boolean is a nan here and a `None` there**, and both are passed over.

### When it does NOT run

- **Without numpy.** It is asked for separately from build123d and guarded; if the import fails the per-pair loop
  answers. (numpy is a hard dependency of build123d 0.11.1 — its `Requires` names it, as do scipy, scikit-learn,
  ezdxf and svgpathtools — so a kernel that imported build123d has it. The guard is there because nothing else in
  `sidecar.py` needs numpy, not because it is expected to be missing.)
- **For a group smaller than `_BULK_MIN_GROUP` (16).** The array path pays a few dozen numpy calls per group whatever
  its size; below about sixteen pairs those cost more than the loop they replace. ‼️ **This is a dispatch threshold,
  not a limit on any answer** — both paths give the same key, and the fence runs with groups of every size. It is a
  chosen number, reasoned from numpy's per-call overhead against the loop's ~15 µs a pair, and **not measured**: no
  sweep over it was taken (see "What this does NOT establish").
- **For a pair whose two placements are equal**, which goes to the loop because that is where #114's same-datum guard
  lives: `TopLoc_Location` cancels two copies of one datum to the exact identity, which a product of two different
  datums is not. Equal entries are a superset of one shared datum, so nothing that needs the guard escapes it.
- **For a pair whose placement is a chain, a power other than 1 or a scale other than 1**, and for a pair involving a
  solid a feature changed. On the fixtures this is a real share and not a corner: the chained-locations variant hands
  861–31,527 pairs back to the loop, and the feature-changed variant 516–18,634.
- ‼️ **A model where every group is small pays the per-solid guard twice** — once here and once inside the loop's own
  `rotation_id`, about 3 µs a solid, because the two keep separate caches. Measured per solid (4.6 and 11.1 µs at two
  machine speeds), not measured as a whole-check regression on such a model: the live car, which is the real case,
  was not re-measured here.

### The result

**The check alone, interleaved loop/array/loop/array in one process** (`check_identity.py`,
`data/check-identity.jsonl`, 01:36–01:39, 22–35% machine CPU):

| occurrences | loop | array | loop | array | ratios | answer |
|---:|---:|---:|---:|---:|---|---|
| 90,880 | 11.88 s | **5.80 s** | 12.57 s | **6.46 s** | 0.49, 0.51 | 1,685,723 B, sha1 `f0c79a1b13c6`, **both** |
| 302,560 | 42.54 s | **19.55 s** | 41.41 s | **16.04 s** | 0.46, 0.39 | 1,685,725 B, sha1 `85425a5a0c12`, **both** |

An earlier pass of the same script at 19–28% CPU (`data/check-identity-earlier.jsonl`) gave 10.74/4.92 and
10.73/5.11 at 90,880 and 35.71/17.96 and 37.06/16.15 at 302,560 — ratios 0.46, 0.48, 0.50, 0.44. The absolutes move
with the machine; the ratios do not.

**The shipped build, interleaved before/after/before/after in four child processes** (#89's `measure.py`, `full` mode,
the two frozen sidecars alternating, 00:33–01:03; `data/results-before-*.jsonl`, `data/results-after-*.jsonl`).
Before is this branch's base (#126 head, sha1 `1a331e2`); after is the committed file (`3cd1f4b`).

| occurrences | code | build s | shapes | assembly | **interference** | reply B | volume | peak GB | CPU |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 90,880 | before | 8.91 | 2.56 | 0.69 | **5.15** | 1,686,233 | 333,596,543.0 | 0.88 | 28.4% |
| 90,880 | **after** | **6.04** | 2.52 | 0.66 | **2.37** | 1,686,232 | 333,596,543.0 | 0.88 | 21.5% |
| 90,880 | before | 8.53 | 2.50 | 0.63 | **4.92** | 1,686,234 | 333,596,543.0 | 0.88 | 21.3% |
| 90,880 | **after** | **6.21** | 2.70 | 0.63 | **2.40** | 1,686,234 | 333,596,543.0 | 0.88 | 20.9% |
| 302,560 | before | 28.87 | 8.44 | 2.08 | **16.97** | 1,686,237 | 1,097,462,272.0 | 1.98 | 22.3% |
| 302,560 | **after** | **21.64** | 8.75 | 2.26 | **8.88** | 1,686,236 | 1,097,462,272.0 | 2.01 | 24.0% |
| 302,560 | before | 29.47 | 8.71 | 2.14 | **17.10** | 1,686,236 | 1,097,462,272.0 | 1.98 | 21.9% |
| 302,560 | **after** | **21.47** | 8.83 | 2.38 | **8.67** | 1,686,236 | 1,097,462,272.0 | 2.01 | 25.0% |

- **Same-pass ratios: the check 0.46 and 0.49 at 90,880, 0.52 and 0.51 at 302,560; the build 0.68 and 0.73, then 0.75
  and 0.73.** The check falls from 58% of the 90,880 build to 39%, and from 58% of the 302,560 build to 41%.
- **The same build, proven not assumed:** identical volume to every digit printed, identical found (158,400 and
  528,000), listed (10,000), booleans (15), reuses (197,341 and 657,493), candidate pairs (197,356 and 657,508) and
  box tests (425,480 and 1,432,752), `truncated` false everywhere, and reply bytes differing by at most 5 of
  1,686,000 because the reply carries its own phase timings.
- **Memory: +0.03 GB at 302,560** (1.98 → 2.01 GB peak), unchanged at 90,880 (0.88 GB both). That is the pair arrays
  and the per-solid translations. Extrapolated linearly it is ~0.1 GB at 1,000,000 occurrences against #125's largest
  recorded 1M peak of 5.78 GB — an extrapolation, not a measurement.
- **Shapes and assembly move within run-to-run noise**, as they should: nothing here touches them.

## 3. The approach that loses: a sweep and prune instead of the grid

With the narrow phase in arrays, `_candidate_pairs` is the largest step of the check (45% at 302,560). The obvious
alternative to a grid is a sweep and prune: sort the boxes by their low corner on one axis, sweep it, and test every
pair whose intervals overlap there.

**It does not need writing to be priced.** What it costs is decided by how many pairs overlap on the sweep axis, and
that is an exact count:

    overlapping pairs on axis a  =  C(n, 2) − #{(i, j) : lo_j > hi_i or lo_i > hi_j}

and the right-hand count is one sort and one `searchsorted`. `broad_phase_options.py`, `data/broad-phase.jsonl`:

| occurrences | the grid | its box tests | its pairs | best sweep axis | pairs it would examine | × the grid | widest active set |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 90,880 | 1.10 / 1.21 s | 425,480 | 197,356 | x | **31,359,360** | **73.7×** | 90,880 (every box) |
| 302,560 | 5.99 / 6.24 s | 1,432,752 | 657,508 | x | **104,501,520** | **72.9×** | 302,560 (every box) |

The other two axes are worse still: 74,759,030 pairs at 90,880 and 826,979,618 at 302,560 (y and z are equal by the
barrel's symmetry), with active sets of 7,010 and 23,327.

- **A sweep and prune loses by 73× on candidate work alone, at both sizes**, before any constant factor: the box
  tests would be 104 million rather than 1.4 million at 302,560, and each is six float comparisons. No constant
  factor was measured for it, because none is needed — the ratio is the result.
- ‼️ **And the axis with the fewest overlaps is the axis with the worst active set.** Along x, the 80 stringers span
  the whole barrel, so a sweep on x has every box in the assembly open at once. On y and z the active set is small
  but the pair count is 8× worse. There is no good axis, which is exactly the finding #94 recorded when it replaced
  K2b's single-axis sweep with a grid, and exactly why the grid's levels are **per axis**.
- **What the grid is actually doing, for the record:** 6 level groups and 21 group passes at both sizes. At 302,560:
  `(0,0,0)` 297,600 boxes, `(0,1,1)` 2,480 skins, `(2,0,1)` and `(2,1,0)` 660 each, `(2,1,1)` 1,080, `(5,0,0)` the
  80 stringers. Cell 14.58 mm.

## 4. The approach with nothing to win: batching pairs into fewer OCCT calls

Measured in §1, one boolean at a time: **15 booleans, 0.0248 s at 90,880 and 0.0459 s at 302,560**, slowest single
boolean 6.6 ms and 11.3 ms. That is 1.0% and 0.5% of the check.

- **There is nothing to batch.** The pose cache (#89) and the slide along a box (#94) already reduce 2.19 million
  pairs to 15 exact booleans at 1M, and a `BRepExtrema`/`BndLib` primitive that replaced Python-side pair work would
  have to replace the *keying*, not the booleans — and keying is what §2 does in arrays, at 2.8–3.2 µs a pair
  against the loop's 15.7–17.0.
- **Not measured:** whether an OCCT primitive exists that takes a whole array of relative placements. None is exposed
  through OCP in a form that avoids a Python call per pair, which is the cost that matters; that is read from the
  binding, not benchmarked.

## 5. Parallelism across the pool: not taken, and why, with numbers

- **The narrow phase is now 0.64–0.80 s at 90,880 and 1.86 s at 302,560.** Getting to the check costs 3.85 s at
  90,880 (`build_to_check_s`), because the solids have to exist first. OCCT shapes do not cross a process boundary
  except by rebuilding them or by a B-rep round trip, so a worker would pay more to receive the work than the work
  now costs.
- ‼️ **And #125's finding stands: at the pod's 1 CPU more kernel processes are SLOWER** (pool 2 lost in all four
  forged runs and at 22.1 s against 19.7 s on worker), and two kernels want ~1,010 MiB of a 1 GiB request. A win here
  would be a laptop/amd64 finding, would need `cpu: 1 → 2` beside it, and is damon's decision, not a manifest change.
- **This is reasoning from measured numbers, not a measurement of a parallel narrow phase.** None was written.

## Fences

`go test ./internal/domain/cad -run 'TestKernel_TheArrayNarrowPhase'` with `FORGE_CAD_PYTHON` set.

| fence | holds | measured |
|---|---|---|
| `TestKernel_TheArrayNarrowPhaseGivesTheLoopsAnswer` | every pair's key from the array path is the key build123d's `Location`s gave, by `==`; and the whole check on those keys is the loop's check byte for byte — list, flag, box tests, pairs, booleans, reuses, found, buried | 8 fixtures × 4 placement variants: **251,919 pairs a variant, 1,007,676 key comparisons, 0 unequal**, and 40 whole-check answers identical by sha1 |
| `TestKernel_TheArrayNarrowPhaseGivesTheLoopsAnswerOnThreeFixtures` | the same, over tied pins, turned rails and crossbars, and a turned barrel | the fence the drills break: ~2–3 min a run against the thorough one's 646 s |

Each fixture is run five ways: as built, with **every ninth solid's placement dropped** (a feature-changed solid,
whose key is `None` and which pays a boolean of its own — 516 to 18,634 pairs handed back to the loop), with **every
seventh solid at its neighbour's `Location` object** (one datum for two solids), with **every fifth placement made a
chain of three datums** (861 to 31,527 pairs handed back), and with **the boolean budget squeezed to 5** so the
truncation path is compared too (7 of the 8 fixtures truncate; the two-bay barrel's candidate pairs are fewer
distinct keys than 5, so its search has nothing to stop).

**The fence has to reach what could go wrong, and says so if it does not:** it fails unless the fixtures produce at
least 1,000 slid keys, 20 carried, 100 groups, 100 distinct rows, one key equal with the other zero's sign, one pair
handed back to the loop, a truncated search per fixture but one, and a feature-changed variant that adds keys of its
own. On the full set: 37,284 slid, 16,521 carried, 2,541 groups, 155,840 rows, 47,651 zero-sign keys, 205,331 pairs
handed back.

‼️ **Why `==` and not `repr`.** The per-pair fence (#114, #121) compares keys by `repr`, which tells `-0.0` from
`0.0`. That is the right test for a path that returns a fresh tuple per pair. This path returns one tuple per
*distinct key*, and `-0.0 == 0.0` with equal hashes — so the shipped check already puts both in one cache entry, and
the barrel's 19 key reprs are 15 keys. Requiring `repr` here would be requiring the array path to reproduce which of
two equal keys a pair happened to build, which nothing downstream can see. `repr_differ` is counted and reported
(11,308 of the barrel fixture's 86,320 pairs), and the byte-identical whole answer is what shows it does not matter.

‼️ **The fence compares on WARMED solids, and this is why.** A pair's first OCCT boolean in a process is not always
its later ones — see the Summary. Every comparison runs the loop once and throws that answer away first, and
`first_run_differs` is reported per variant: 4 of 40 comparisons sat on solids whose first pass differed from the
second, all four the prisms fixtures with a feature-changed solid, where ~37,000 pairs each pay their own boolean.
Without the warm-up the fence fails on those four for a reason that has nothing to do with this branch.

‼️ **What was run, and what was not.** `go vet ./...` clean. `internal/agent` passes whole (17.7 s).
`TestKernel_TheArrayNarrowPhaseGivesTheLoopsAnswer` (8 fixtures, 646 s) and #114's and #121's own fences —
`APairKeyWithoutLocationsIsTheKeyBuild123dGave`, `KeyingMorePairsBuildsNoMoreLocations`, `APlacedCopy…`,
`PlacingMoreCopies…` (294 s together) — pass, both taken **before two later edits to `sidecar.py`: a comment, and
building the per-pair fallback only when some pair needs it**. On the file exactly as committed,
`TestKernel_TheArrayNarrowPhaseGivesTheLoopsAnswerOnThreeFixtures` passes (40.9 s) and `go vet` is clean.

**The whole `internal/domain/cad` package was NOT run to completion on the committed file.** It was started twice and
stopped both times — the second time because the laptop was needed for an arm64 image build — so the Windows-only
failures this branch inherits (`TestScript_*`, `TestKernel_ReturnsTheSurfaceOfTheSolidItBuilt`,
`TestKernel_ExportingManyOccurrencesGrowsLinearly`, `ARepeatedScriptedPartRunsItsScriptOnce`,
`ARepeatedScriptedPartIsBuiltEveryTime`, `AScriptedPartIsExportedAndMeshed`) are carried over from #121's and #114's
runs rather than re-observed here. **Run it before merging.**

## Drills

`scripts/drill-fences.sh`, section "Interference approach: the narrow phase one array at a time", through a temporary
runner assembled from the script's own header, this section and its footer, in `scripts/` so `ROOT` resolves.
Dry run first: **0 anchors moved**. `data/drills.log`.

**Seventeen were written. 13 went red and are committed; 4 stayed green and were removed with a note in the script.**
`13 went red, 4 stayed green, 0 unproven, 0 anchor(s) moved`, and the tree was byte-identical afterwards.

| drill (committed) | went red on |
|---|---|
| the rows are told apart by their first column alone | 772 of 1,378 keys differ on rails and crossbars; 706 rows became 30 |
| a group is keyed by its definitions and not its rotations | 696 of 1,378 keys differ — a turned crossbar keyed with a rail's rotation |
| a group is keyed by its rotations and not its definitions | 1,086 of 1,378 keys differ, and the answer differs at byte 14 |
| the relative translation is read in the other solid's frame | 630 of 1,378 keys differ: `103.0` where the loop gives `-103.0` |
| the placement's inverse is added rather than subtracted | 790 of 1,378 keys differ |
| the group's rotation is read by rows rather than transposed | 94 of 1,378 keys differ (the fixture's turned parts) |
| a group's containment is tested against one axis for all three | 598 of 1,378 keys differ |
| a solid with no placement is left out of the translations | the fence failed in 0.12 s: every translation after the first unplaced solid is another solid's |
| the search stops at the key's number, not the pair it was met at | the squeezed-budget variant, the only one that truncates, is not the loop's answer |
| a reuse is counted for every pair, not those the search reached | the squeezed-budget variant again: `reused` counts pairs the search never reached |
| the list's ties are broken by an unstable sort | the answer differs at byte 20 with the same 5,759 bytes |
| the larger solid is reported first | the answer differs at byte 16 |
| every clash found is counted buried | the answer differs at byte 5,755 — the `buried` count |

‼️ **The four that stayed green, and what each says.** Every one is recorded in the script where the drill was, not
reworded away:

| removed drill | why nothing can catch it |
|---|---|
| a row forgets the pose in the other frame (dedupe on the forward translation alone) | **The four backward columns are redundant.** Within one group both translations come from the same `t_j - t_i` through two fixed rotations, so the forward one determines the backward one, and the marks the backward one carries are decided by `variant`, which is column 0. Kept because the redundancy is an argument about exact arithmetic and the columns cost nothing. |
| only the forward pose is marked before the rows are deduped | **Marking before the dedupe is a performance device, not a correctness one.** `_key_tail`'s `_slid` writes exactly those slots with exactly those values again, so dropping a mark only makes the rows finer — more than 1,232 rows for 197,356 pairs — and the answer does not move. That is the honest description of what the marks are for, and no fence can see it. |
| a clash under the minimum volume is listed | **A fixture gap, not a fence gap.** No fixture in the set has a clash whose volume is under 1 mm³ *and* whose fraction is over 0.001, so nothing can tell the two forms apart — on either path. It wants a fixture; see Recommendations. |
| two solids at one location are keyed by the array path (the same-placement guard disabled) | **The same drill #114 wrote against the same guard, with the same result.** Two different datums with the same rotation compose to the identity off by ~1e-16, which rounds to the same pose and reaches the key only through containment, at exactly its boundary; no fixture can put a solid there, because bounds carry OCCT's tolerance. The guard is kept so the key stays the loop's in that case, and nothing measured shows it is needed. |

## What this establishes

- **Where the interference check's time goes after #121 and #126**, at 90,880 and 302,560 occurrences: the broad
  phase is now the largest step (45% at 302,560), `_measures` 34%, and the whole narrow phase 22% — an inversion of
  #114's picture, where keying was ~80%.
- **That a pair's relative translation and its containment test can be computed one array at a time, bit for bit**:
  5,129,184 translation entries and 1,180,320 inverse entries across two barrels, 0 differing bits, with the key
  itself built by the shipped scalar tail once per distinct row (1,232 rows for 197,356 pairs).
- **That doing so keys the pairs 5× faster (×0.166 to ×0.207 at two sizes, interleaved), makes the check ×0.46 to
  ×0.52 and the shipped build ×0.68 to ×0.75, and gives an answer that is byte-identical** — the same sha1 for the
  check's whole return at both sizes, the same volume, found, listed, booleans, reuses, pairs and box tests across
  four interleaved builds, for +0.03 GB of peak memory at 302,560.
- **That a sweep and prune over sorted intervals loses on this assembly by 73×**, measured as an exact pair count at
  two sizes rather than as an implementation, and that its best axis is the one whose active set is the whole
  assembly.
- **That batching pairs into fewer OCCT calls has nothing to win**: 15 booleans and 0.025–0.046 s, 0.5–1.0% of the
  check.
- ‼️ **That the shipped check's boolean volumes are not repeatable on a cold process** — a pair's first boolean can
  differ from its later ones in the last two bits, shown on four fixtures where nearly every pair pays its own
  boolean, with the LOOP on both sides of the comparison.
- **That `-0.0` and `0.0` are one clash and two reprs**, so the barrel's 19 distinct key reprs are 15 distinct keys,
  and `repr` is the wrong equivalence for a path that returns one tuple per key.
- **That no limit needs to move.** Nothing here is a basis for changing the 4,096 export or 8,192 view ceilings, the
  2,000-boolean budget, the 10,000-entry list, the 16 KiB repair budget or the 100,000-occurrence door.

## What this does NOT establish

- **Anything at 1,000,000 occurrences.** Nothing here was run at 1M. #125 measured the 1M `full` build at 242.8 s on
  this laptop (and 1,208.2 s in the same cell under another agent's load), so a 1M pair of runs is 8–40 minutes of
  wall clock, and the task this branch was given says to work at 90,880 and 302,560. That the 1M check is ×0.5
  **follows from** ratios that are flat from 90,880 to 302,560 and a narrow phase that is linear in the pairs — an
  inference, not a measurement.
- **That the bit-exactness holds on any compiler or OCCT build but this one.** It is measured on
  cadquery-ocp-novtk under build123d 0.11.1 on Windows/amd64, on placements this kernel makes. If OCCT's
  `gp_Trsf` product were contracted differently, or numpy fused a multiply-add, the fence would catch it — and on
  a different platform it would catch it *there*, having never been run there.
- **`_BULK_MIN_GROUP` = 16.** Reasoned from numpy's per-call overhead against the loop's per-pair cost, **not
  measured**: no sweep over the threshold was taken, and no fixture was built whose groups sit near it deliberately.
  It changes only which of two equivalent paths runs.
- **The pathological case: many groups, or many distinct keys.** The barrel is 536 groups and 15 keys. A model with
  one group per pair would pay the array path's fixed cost per group and get nothing, which `_BULK_MIN_GROUP` is
  meant to catch; and a model whose pairs are nearly all distinct keys would have the array path compute rows for
  every pair up to the truncation point where the loop would have stopped at the 2,000th boolean. **Neither was
  measured at scale.** The prisms fixtures are the closest thing here — 37,675 pairs, 37,029 distinct keys, 34,630
  rows — and they are 275 solids, not 300,000.
- **Any model that is not the barrel, at scale.** The live car (`2026-09-12-car-ceiling`) was not re-measured, and
  it is the case where the array path can only lose: few enough pairs per group that most go to the loop anyway,
  plus the per-solid guard paid twice. The fixtures show the answer is the same there; nothing shows the seconds.
- **`_measures` and `_candidate_pairs`.** Both were measured and left alone; they are now 79% of the check
  (Recommendations).
- **Mesh and STEP modes.** A `mesh` run replaces the check with the broad phase only, so it cannot show this branch
  either way; STEP skips it. Neither was re-measured.
- **A quiet machine, or a second opinion on any absolute second.** The profiles ran at 23–46% machine CPU, the
  interleaved builds at 21–28%, and the laptop's own speed drifted 2.2–2.6× within single scripts. Ratios, counts and
  byte identity are the results; the seconds are not comparable with #114's, #121's, or with each other across the
  session.
- **That a model repairs any better.** Unchanged from #114 and #121: no model was sent anything.

## Recommendations (nothing here is changed)

1. **`_candidate_pairs` is now the largest step of the check — 45% at 302,560, 3.85 s — and nothing has touched it
   since #94.** A sweep and prune is not the way (§3). What its Python does is filing 302,560 boxes into cell
   dictionaries and looping 21 group passes; that is the same shape of work the narrow phase just moved into arrays
   (cell indices are `floor(x / cell)` as integers, the home-cell test is an integer comparison, and `_boxes_miss` is
   six float comparisons), and it would need the same equivalence fence: the same pairs in the same order **and the
   same `box_tests` count**, which is in the reply.
2. **`_measures` is 34% and its 12 `Value()` calls an occurrence are most of it.** #121 said this needs a rotation id
   threaded down from `_build`; that is now cheaper than it was, because `_build` already knows the matrix as plain
   floats and the array path already wants the translations as an array. The moved box itself is arithmetic on eight
   corners — array work.
3. **The per-solid placement guard is 52–65% of the narrow phase** (0.418 s of 0.64–0.80 s at 90,880): four OCCT
   calls a solid to ask whether a placement is one datum at power 1 with scale 1. `_measures` already holds the
   `gp_Trsf` those calls re-fetch, so handing `Form()` and `ScaleFactor()` over with the translations would remove
   two of the four. Measured here, deliberately not taken: this branch already changed `_measures`' signature once.
4. **Re-measure at 1,008,160 on a quiet machine**, with the before/after sidecars interleaved as here, and prove the
   same build the way #125 did. This branch's claim at 1M is an inference.
5. **Keep every limit where it is.** Nothing measured here is a basis for moving any of them.
6. **The cold-boolean finding deserves its own look.** If a pair's first OCCT boolean can differ from its later ones,
   then two builds of the same document can report slightly different clash volumes, and a repair judged by a
   fraction near a threshold could flip. The barrel is unaffected (15 booleans, and #114/#125 both show byte-identical
   1M replies), but a model with thousands of distinct poses is exactly the case this was seen on.

## Re-running

```bash
export GOWORK=off PYTHONUTF8=1 D=/some/dir PY=.cadvenv/bin/python
S=docs/spikes/2026-09-16-interference-approach
FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=9,30 go test -count=1 -timeout 40m \
  -run TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry      # barrel-<bays>.json
mkdir -p $D/before $D/after && cp internal/domain/cad/sidecar.py $D/after/sidecar.py   # before: #126's head
$PY $S/load_log.py $D/load.log &
# ‼️ INTERLEAVED, not two blocks. This laptop's own speed drifted 2.6x in fifteen
# minutes during this session, which is more than the effect being measured.
for pass in 1 2; do
  for side in before after; do
    $PY docs/spikes/2026-09-15-one-million-occurrences/measure.py --dir $D --bays 9,30 \
      --modes full --repeat 1 --cap 1200 --sidecar $D/$side/sidecar.py
    mv $D/results.jsonl $D/results-$side-$pass.jsonl
  done
done
for b in 9 30; do
  $PY $S/profile_narrow.py internal/domain/cad/sidecar.py $D/barrel-$b.json $D $b
  $PY $S/broad_phase_options.py internal/domain/cad/sidecar.py $D/barrel-$b.json $D $b
  $PY $S/check_identity.py internal/domain/cad/sidecar.py $D/barrel-$b.json $D/check-identity.jsonl b$b
done
FORGE_CAD_PYTHON=$PY go test -count=1 -timeout 40m -run TestKernel_TheArrayNarrowPhase ./internal/domain/cad
```
