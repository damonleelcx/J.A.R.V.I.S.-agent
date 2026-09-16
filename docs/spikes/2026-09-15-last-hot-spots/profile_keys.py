"""What is LEFT of the interference check after #114, step by step
(docs/spikes/2026-09-15-last-hot-spots).

#114 took the build123d Locations out of keying and left the 1M check at 51 s:
_candidate_pairs 10.9 s, _measures 7.4 s and about 32 s of keys. This says what those
32 s are.

_build runs as shipped up to the check, with _interferences replaced by a capture of
its arguments. Then, on those same solids, in the same process:

  1. _measures and _candidate_pairs timed, as the check calls them, and _measures
     again without its moved boxes so the boxes' own share is by subtraction;
  2. the keying loop alone, timed: `for i, j in pairs: pair_key(i, j)`;
  3. the same loop split by subtraction -- each loop does what the one before did and
     one step more, written out from _pair_keys' own body, so the difference between
     two is that step;
  4. what the pairs LOOK like: how many distinct keys, rotations, pairs of rotations,
     pairs of definitions, and groups of (definitions, rotations) there are among
     them, and how many pairs reach each branch. A bulk key would key one group at a
     time, so the group count is what says whether that can pay;
  5. every primitive a key pays for, timed one call at a time;
  6. the keying loop under cProfile.

Usage: <cad venv python> profile_keys.py <sidecar.py> <barrel-N.json> <out dir> <tag>
                                          [--no-profile]
"""
import cProfile
import hashlib
import importlib.util
import io
import json
import math
import os
import pstats
import sys
import time

import psutil

sidecar_path, request_path, out_dir, tag = sys.argv[1:5]
profile = "--no-profile" not in sys.argv[5:]
spec = importlib.util.spec_from_file_location("sidecar", sidecar_path)
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)

captured = {}


def capture(solids, ids, labels, placed=None):
    captured["args"] = (solids, ids, labels, placed)
    return [], False, 0, {"pairs": 0, "booleans": 0, "reused": 0, "found": 0, "summarized": False, "buried": 0}


sc._interferences = capture
with open(request_path, "rb") as fh:
    request = json.loads(fh.read())
request["format"] = ""
t = time.perf_counter()
sc._build(request)
build_s = time.perf_counter() - t
del request
solids, ids, labels, placed = captured["args"]
n = len(solids)

psutil.cpu_percent(None)
row = {"tag": tag, "sidecar": os.path.abspath(sidecar_path), "parts": n, "build_to_check_s": build_s}

# --- 1. the first two steps ------------------------------------------------
rotations = [None] * n
t = time.perf_counter()
boxes, volumes = sc._measures(solids, placed, rotations)
row["measures_s"] = time.perf_counter() - t

t = time.perf_counter()
pairs, box_tests = sc._candidate_pairs(boxes)
row["candidate_pairs_s"] = time.perf_counter() - t
row["pairs"], row["box_tests"] = len(pairs), box_tests

# _measures without the moved box: the same reads and interning, box building skipped.
real_box = sc._moved_box_direct
sc._moved_box_direct = lambda box, e: None
try:
    t = time.perf_counter()
    sc._measures(solids, placed, [None] * n)
    row["measures_without_boxes_s"] = time.perf_counter() - t
finally:
    sc._moved_box_direct = real_box

# --- 2. the keying loop ----------------------------------------------------
shape_ids, slabs = {}, {}
for p in placed:
    if p is not None and p[0] not in shape_ids:
        shape_ids[p[0]] = len(shape_ids)
        slabs[p[0]] = sc._slabs(p[0], p[2])

pair_key = sc._pair_keys(placed, shape_ids, slabs, rotations)
t = time.perf_counter()
for i, j in pairs:
    pair_key(i, j)
row["keys_s"] = time.perf_counter() - t
row["memo_entries"] = len(pair_key.memo)

# --- 3. the loop split by subtraction --------------------------------------
#
# Written out from _pair_keys' body; each loop is the one before plus one step. The
# figures are differences, so a constant per-iteration cost cancels.
split = {}
_rotation, _rounded, _pose_of = sc._rotation, sc._rounded_rotation, sc._pose_of
_inside_split, _carried_fast, _slid = sc._inside_split, sc._carried_fast, sc._slid

t = time.perf_counter()
for i, j in pairs:
    pass
split["loop_only_s"] = time.perf_counter() - t

inverses = [None] * n
t = time.perf_counter()
for i, j in pairs:
    pi, pj = placed[i], placed[j]
    li, lj = pi[1].wrapped, pj[1].wrapped
    inv_i = inverses[i]
    if inv_i is None:
        inv_i = inverses[i] = li.Inverted()
    inv_j = inverses[j]
    if inv_j is None:
        inv_j = inverses[j] = lj.Inverted()
    va, vb = (inv_i * lj).Transformation().Value, (inv_j * li).Transformation().Value
    ta, tb = (va(1, 4), va(2, 4), va(3, 4)), (vb(1, 4), vb(2, 4), vb(3, 4))
split["plus_products_s"] = time.perf_counter() - t

inverses = [None] * n
t = time.perf_counter()
for i, j in pairs:
    pi, pj = placed[i], placed[j]
    li, lj = pi[1].wrapped, pj[1].wrapped
    inv_i = inverses[i]
    if inv_i is None:
        inv_i = inverses[i] = li.Inverted()
    inv_j = inverses[j]
    if inv_j is None:
        inv_j = inverses[j] = lj.Inverted()
    va, vb = (inv_i * lj).Transformation().Value, (inv_j * li).Transformation().Value
    ta, tb = (va(1, 4), va(2, 4), va(3, 4)), (vb(1, 4), vb(2, 4), vb(3, 4))
    ra, rb = _rotation(va), _rotation(vb)
    qa, qb = _rounded(ra), _rounded(rb)
split["plus_rotations_unmemoized_s"] = time.perf_counter() - t

# With the memo, as shipped: the rotations are read once per pair of rotations.
key2 = sc._pair_keys(placed, shape_ids, {}, rotations)
t = time.perf_counter()
for i, j in pairs:
    key2(i, j)
split["no_slabs_whole_s"] = time.perf_counter() - t

inverses = [None] * n
t = time.perf_counter()
for i, j in pairs:
    pi, pj = placed[i], placed[j]
    li, lj = pi[1].wrapped, pj[1].wrapped
    inv_i = inverses[i]
    if inv_i is None:
        inv_i = inverses[i] = li.Inverted()
    inv_j = inverses[j]
    if inv_j is None:
        inv_j = inverses[j] = lj.Inverted()
    va, vb = (inv_i * lj).Transformation().Value, (inv_j * li).Transformation().Value
    ta, tb = (va(1, 4), va(2, 4), va(3, 4)), (vb(1, 4), vb(2, 4), vb(3, 4))
    ra, rb = _rotation(va), _rotation(vb)
    qa, qb = _rounded(ra), _rounded(rb)
    pose_f, pose_b = _pose_of(qa, ta), _pose_of(qb, tb)
split["plus_poses_s"] = time.perf_counter() - t

inverses = [None] * n
t = time.perf_counter()
for i, j in pairs:
    pi, pj = placed[i], placed[j]
    li, lj = pi[1].wrapped, pj[1].wrapped
    inv_i = inverses[i]
    if inv_i is None:
        inv_i = inverses[i] = li.Inverted()
    inv_j = inverses[j]
    if inv_j is None:
        inv_j = inverses[j] = lj.Inverted()
    va, vb = (inv_i * lj).Transformation().Value, (inv_j * li).Transformation().Value
    ta, tb = (va(1, 4), va(2, 4), va(3, 4)), (vb(1, 4), vb(2, 4), vb(3, 4))
    ra, rb = _rotation(va), _rotation(vb)
    qa, qb = _rounded(ra), _rounded(rb)
    pose_f, pose_b = _pose_of(qa, ta), _pose_of(qb, tb)
    frame_i, frame_j = slabs[pi[0]], slabs[pj[0]]
    inside_i = _inside_split(ra, ta, frame_i[0], frame_j[1])
    inside_j = _inside_split(rb, tb, frame_j[0], frame_i[1])
split["plus_inside_s"] = time.perf_counter() - t
split["keys_whole_s"] = row["keys_s"]

# The cache lookups the check then does on those keys.
keys = [pair_key(i, j) for i, j in pairs]
cache = {}
t = time.perf_counter()
for k in keys:
    if k in cache:
        pass
    else:
        cache[k] = None
split["cache_lookups_s"] = time.perf_counter() - t
split["distinct_keys"] = len(cache)
row["split"] = split

# --- 4. what the pairs look like -------------------------------------------
#
# The counts a bulk key would rest on. rotations[i] is _measures' interned rotation
# bits; a group is one pair of definitions at one pair of rotations.
groups, rotpairs, defpairs, rots = set(), set(), set(), set()
same_def = both_nonslab = reach_inside = reach_carried = 0
for i, j in pairs:
    a, b = shape_ids[placed[i][0]], shape_ids[placed[j][0]]
    ri, rj = rotations[i], rotations[j]
    groups.add((a, b, ri, rj))
    rotpairs.add((ri, rj))
    defpairs.add((a, b))
    rots.add(ri)
    rots.add(rj)
    if a == b:
        same_def += 1
    if slabs[placed[i][0]][0] is None and slabs[placed[j][0]][0] is None:
        both_nonslab += 1
shape = {"groups": len(groups), "rotation_pairs": len(rotpairs), "definition_pairs": len(defpairs),
         "rotations": len(rots), "pairs_of_one_definition": same_def,
         "pairs_neither_a_slab": both_nonslab, "definitions": len(shape_ids),
         "slab_definitions": sum(1 for v in slabs.values() if v[0] is not None)}
for k in keys:
    if k is None:
        continue
    m = (k[2][3], k[2][7], k[2][11])
    if any(math.isinf(x) for x in m):
        reach_inside += 1
    if any(x == -math.inf for x in m):
        reach_carried += 1
shape["keys_marked"] = reach_inside
shape["keys_carried"] = reach_carried
row["pair_shape"] = shape
del keys, cache

# --- 5. one call at a time -------------------------------------------------
prims = {}
i0, j0 = pairs[0]
li, lj = placed[i0][1].wrapped, placed[j0][1].wrapped
inv = li.Inverted()
prod = inv * lj
trsf = prod.Transformation()
va = trsf.Value
ra = _rotation(va)
qa = _rounded(ra)
ta = (va(1, 4), va(2, 4), va(3, 4))
frame_i, frame_j = slabs[placed[i0][0]], slabs[placed[j0][0]]
pose = _pose_of(qa, ta)
e = sc._entries(li)
local_box = sc._box_of(placed[i0][2])


def bench(name, fn, count=100000):
    fn()
    t0 = time.perf_counter()
    for _ in range(count):
        fn()
    prims[name] = 1e6 * (time.perf_counter() - t0) / count


bench("Inverted()", lambda: li.Inverted())
bench("a product", lambda: inv * lj)
bench("Transformation()", lambda: prod.Transformation())
bench("one Value()", lambda: va(1, 1))
bench("_rotation (9 Value)", lambda: _rotation(va))
bench("_rounded_rotation (9 round)", lambda: _rounded(ra))
bench("three translation Value()", lambda: (va(1, 4), va(2, 4), va(3, 4)))
bench("_pose_of (3 round + tuple)", lambda: _pose_of(qa, ta))
bench("_inside_split", lambda: _inside_split(ra, ta, frame_i[0], frame_j[1]))
bench("_carried_fast (one axis)", lambda: _carried_fast(pose, [0]))
bench("_slid", lambda: _slid(pose, [0], []))
bench("min of two key tuples", lambda: min((0, 1, pose), (1, 0, pose)))
bench("IsEqual", lambda: li.IsEqual(lj))
bench("FirstPower", lambda: li.FirstPower())
bench("NextLocation().IsIdentity()", lambda: li.NextLocation().IsIdentity())
bench("ScaleFactor + Form", lambda: (trsf.ScaleFactor(), int(trsf.Form())))
bench("_entries (12 Value)", lambda: sc._entries(li))
bench("_moved_box_direct", lambda: sc._moved_box_direct(local_box, e))
bench("_PACK_ROTATION", lambda: sc._PACK_ROTATION(e[0], e[1], e[2], e[4], e[5], e[6], e[8], e[9], e[10]))
bench("one dict get on a 4-tuple", lambda: pair_key.memo.get((0, 1)))
row["primitives_us"] = prims

# --- 6. under cProfile -----------------------------------------------------
if profile:
    key3 = sc._pair_keys(placed, shape_ids, slabs, rotations)
    pr = cProfile.Profile()
    psutil.cpu_percent(None)
    t = time.perf_counter()
    pr.enable()
    for i, j in pairs:
        key3(i, j)
    pr.disable()
    row["profiled_keys_s"] = time.perf_counter() - t
    row["profiled_cpu_pct"] = psutil.cpu_percent(None)
    pr.dump_stats(os.path.join(out_dir, "keys-%s.prof" % tag))
    text = io.StringIO()
    st = pstats.Stats(pr, stream=text)
    text.write("== %s: by own time ==\n" % tag)
    st.sort_stats("tottime").print_stats(30)
    text.write("\n== %s: by cumulative time ==\n" % tag)
    st.sort_stats("cumulative").print_stats(30)
    with open(os.path.join(out_dir, "profile-keys-%s.txt" % tag), "w") as fh:
        fh.write(text.getvalue())

mem = psutil.Process().memory_info()
row["peak_rss_gb"] = getattr(mem, "peak_wset", mem.rss) / 1e9
row["system_cpu_pct"] = psutil.cpu_percent(None)
with open(sidecar_path, "rb") as fh:
    row["sidecar_sha1"] = hashlib.sha1(fh.read()).hexdigest()[:7]
row["started"] = time.strftime("%Y-%m-%dT%H:%M:%S")
with open(os.path.join(out_dir, "profile-keys.jsonl"), "a") as fh:
    fh.write(json.dumps(row) + "\n")
print(json.dumps(row, indent=1))
