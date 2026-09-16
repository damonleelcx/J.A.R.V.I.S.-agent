"""Where the 1M interference check goes AFTER #121, and what the array narrow phase
does with it (docs/spikes/2026-09-16-interference-approach).

#121 moved the check 0.94-0.98x and said the rest needed a different approach. This
says where the time is now and measures the approach.

_build runs as shipped up to the check, with _interferences replaced by a capture of
its arguments. Then, on those same solids, in the same process:

  1. _measures and _candidate_pairs timed, as the check calls them;
  2. the per-pair keying loop and the array path, timed INTERLEAVED (loop, array,
     loop, array), because the laptop's own speed drifts by more than 2x over a
     session and a block of one followed by a block of the other measures that;
  3. the array path split: the pairs into an array, the per-solid placement guard,
     and what is left (grouping, the products, the containment, the dedupe);
  4. what the pairs LOOK like: groups of (two definitions, two rotations), distinct
     rows keyed, distinct keys, pairs handed back to the loop;
  5. the OCCT booleans themselves, timed, so "batch the booleans" can be priced;
  6. two BIT-exactness checks the array path rests on: that
     -(R^T . t_src) + (R^T . t_dst) in numpy is the translation OCCT's own product
     gives, and that -(R^T . t) is the translation OCCT's own Inverted() gives.

Usage: <cad venv python> profile_narrow.py <sidecar.py> <barrel-N.json> <out dir> <tag>
"""
import hashlib
import importlib.util
import json
import os
import sys
import time

import numpy as np
import psutil

sidecar_path, request_path, out_dir, tag = sys.argv[1:5]
spec = importlib.util.spec_from_file_location("sidecar", sidecar_path)
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)

captured = {}


def capture(solids, ids, labels, placed=None):
    captured["args"] = (solids, ids, labels, placed)
    return [], False, 0, {"pairs": 0, "booleans": 0, "reused": 0, "found": 0,
                          "summarized": False, "buried": 0}


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
row = {"tag": tag, "sidecar": os.path.abspath(sidecar_path), "parts": n,
       "build_to_check_s": build_s}

# --- 1. the two steps before the narrow phase ------------------------------
rotations, frames = [None] * n, []
t = time.perf_counter()
boxes, volumes = sc._measures(solids, placed, rotations, frames)
row["measures_s"] = time.perf_counter() - t
t = time.perf_counter()
pairs, box_tests = sc._candidate_pairs(boxes)
row["candidate_pairs_s"] = time.perf_counter() - t
row["pairs"], row["box_tests"] = len(pairs), box_tests

shape_ids, slabs = {}, {}
for p in placed:
    if p is not None and p[0] not in shape_ids:
        shape_ids[p[0]] = len(shape_ids)
        slabs[p[0]] = sc._slabs(p[0], p[2])

# --- 2. the loop and the array path, interleaved ---------------------------
loops, arrays, stats = [], [], {}
for rep in (1, 2):
    scalar = sc._pair_keys(placed, shape_ids, slabs, rotations)
    t = time.perf_counter()
    for i, j in pairs:
        scalar(i, j)
    loops.append(time.perf_counter() - t)
    t = time.perf_counter()
    of_pair, keys, ii, jj = sc._bulk_keys(placed, shape_ids, slabs, rotations, pairs,
                                          frames, stats)
    arrays.append(time.perf_counter() - t)
row["loop_keys_s"] = loops
row["array_keys_s"] = arrays
row["ratio_per_pass"] = [round(a / l, 3) for a, l in zip(arrays, loops)]
row["cpu_after_keys_pct"] = psutil.cpu_percent(None)

# --- 3. the array path split ----------------------------------------------
split = {}
t = time.perf_counter()
ij = np.array(pairs, dtype=np.int64)
split["pairs_into_an_array_s"] = time.perf_counter() - t
t = time.perf_counter()
touched = np.unique(ij)
split["unique_solids_s"] = time.perf_counter() - t
t = time.perf_counter()
guarded = 0
for k in touched.tolist():
    loc = placed[k][1].wrapped
    if loc.FirstPower() == 1 and loc.NextLocation().IsIdentity():
        trsf = loc.Transformation()
        if trsf.ScaleFactor() == 1.0:
            guarded += int(trsf.Form()) >= 0
split["per_solid_placement_guard_s"] = time.perf_counter() - t
split["solids_guarded"] = guarded
split["rest_s"] = min(arrays) - (split["pairs_into_an_array_s"] + split["unique_solids_s"]
                                 + split["per_solid_placement_guard_s"])
row["split"] = split

# --- 4. what the pairs look like ------------------------------------------
row["shape_of_the_pairs"] = dict(stats, definitions=len(shape_ids),
                                 slab_definitions=sum(1 for v in slabs.values() if v[0] is not None))

# --- 5. the booleans themselves -------------------------------------------
#
# Every distinct key's boolean, timed: what "batch the pairs into fewer OCCT calls"
# would have to beat.
seen, booleans = {}, []
for k, key in enumerate(keys):
    if key is None or key in seen:
        continue
    seen[key] = k
first_at = {}
of_list = of_pair.tolist()
for k, s in enumerate(of_list):
    if s not in first_at:
        first_at[s] = k
for s in sorted({v for v in seen.values()}):
    i, j = pairs[first_at[s]]
    t = time.perf_counter()
    try:
        float(getattr(solids[i] & solids[j], "volume", 0.0))
    except Exception:
        pass
    booleans.append(time.perf_counter() - t)
row["booleans"] = len(booleans)
row["booleans_total_s"] = sum(booleans)
row["booleans_slowest_s"] = max(booleans) if booleans else 0.0

# --- 6. the two bit-exactness checks --------------------------------------
tr = np.asarray(frames, dtype=float).reshape(n, 3)
rot = np.empty((n, 9))
inv = np.empty((n, 3))
for k in range(n):
    loc = placed[k][1].wrapped
    e = sc._entries(loc)
    rot[k] = (e[0], e[1], e[2], e[4], e[5], e[6], e[8], e[9], e[10])
    ie = sc._entries(loc.Inverted())
    inv[k] = (ie[3], ie[7], ie[11])

mine = np.empty((n, 3))
for r in range(3):
    acc = rot[:, r] * tr[:, 0]
    acc = acc + rot[:, 3 + r] * tr[:, 1]
    acc = acc + rot[:, 6 + r] * tr[:, 2]
    mine[:, r] = -acc
row["inverse_entries"] = int(inv.size)
row["inverse_entries_bit_identical"] = int((mine.view(np.int64) == inv.view(np.int64)).sum())

# The product, on every candidate pair, both ways round.
occt = np.empty((len(pairs), 6))
inverses = [None] * n
for k, (i, j) in enumerate(pairs):
    li, lj = placed[i][1].wrapped, placed[j][1].wrapped
    if inverses[i] is None:
        inverses[i] = li.Inverted()
    if inverses[j] is None:
        inverses[j] = lj.Inverted()
    va = (inverses[i] * lj).Transformation().Value
    vb = (inverses[j] * li).Transformation().Value
    occt[k] = (va(1, 4), va(2, 4), va(3, 4), vb(1, 4), vb(2, 4), vb(3, 4))
pi, pj = ij[:, 0], ij[:, 1]
got = np.empty((len(pairs), 6))
for half, (src, dst) in enumerate(((pi, pj), (pj, pi))):
    R, ts, td, it = rot[src], tr[src], tr[dst], inv[src]
    for r in range(3):
        acc = R[:, r] * td[:, 0]
        acc = acc + R[:, 3 + r] * td[:, 1]
        acc = acc + R[:, 6 + r] * td[:, 2]
        got[:, 3 * half + r] = it[:, r] + acc
row["product_entries"] = int(occt.size)
row["product_entries_bit_identical"] = int((got.view(np.int64) == occt.view(np.int64)).sum())

mem = psutil.Process().memory_info()
row["peak_rss_gb"] = getattr(mem, "peak_wset", mem.rss) / 1e9
row["system_cpu_pct"] = psutil.cpu_percent(None)
with open(sidecar_path, "rb") as fh:
    row["sidecar_sha1"] = hashlib.sha1(fh.read()).hexdigest()[:7]
row["started"] = time.strftime("%Y-%m-%dT%H:%M:%S")
with open(os.path.join(out_dir, "profile-narrow.jsonl"), "a") as fh:
    fh.write(json.dumps(row) + "\n")
print(json.dumps(row, indent=1))
