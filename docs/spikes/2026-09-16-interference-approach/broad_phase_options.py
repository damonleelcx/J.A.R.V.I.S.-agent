"""Would a sweep and prune beat the grid on this assembly?  A count, not a guess
(docs/spikes/2026-09-16-interference-approach).

With the narrow phase in arrays, _candidate_pairs is the largest single step of the
check. The obvious alternative is a sweep and prune: sort the boxes by their low
corner on one axis, sweep, and test every pair whose intervals overlap on it. What
that costs is decided entirely by HOW MANY pairs overlap on the sweep axis, which is
a number, not an implementation:

    overlapping pairs on axis a  =  C(n, 2) - #{(i, j) : lo_j > hi_i or lo_i > hi_j}

and the right-hand count is a sorted searchsorted away. So the price of the best
possible sweep and prune on this assembly can be measured exactly without writing
one, and compared with the box tests the grid actually makes.

It also prices the other end: how many pairs a sweep would have to keep ACTIVE, and
what the grid pays for its per-axis levels (the group passes and how many boxes sit
in each level).

Usage: <cad venv python> broad_phase_options.py <sidecar.py> <barrel-N.json> <out dir> <tag>
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
sc._build(request)
del request
solids, ids, labels, placed = captured["args"]
n = len(solids)
psutil.cpu_percent(None)
row = {"tag": tag, "parts": n}

boxes, volumes = sc._measures(solids, placed)
# The grid, as shipped, twice.
grid = []
for _ in (1, 2):
    t = time.perf_counter()
    pairs, box_tests = sc._candidate_pairs(boxes)
    grid.append(time.perf_counter() - t)
row["grid_s"] = grid
row["grid_pairs"] = len(pairs)
row["grid_box_tests"] = box_tests

present = [k for k, b in enumerate(boxes) if b is not None]
lo = np.array([boxes[k][0] for k in present])
hi = np.array([boxes[k][1] for k in present])
m = len(present)
row["boxes"] = m
row["every_pair"] = m * (m - 1) // 2

# --- the best possible sweep and prune, per axis ---------------------------
sweep = {}
for a in range(3):
    los = np.sort(lo[:, a])
    # pairs (i, j) with lo_j > hi_i, counted once per disjoint pair
    after = m - np.searchsorted(los, hi[:, a], side="right")
    disjoint = int(after.sum())
    sweep["axis_%d_overlapping_pairs" % a] = row["every_pair"] - disjoint
    # What a sweep would hold open: the number of boxes whose interval covers each
    # box's low corner, at its worst.
    opened = np.searchsorted(los, hi[:, a], side="right")
    closed = np.searchsorted(np.sort(hi[:, a]), lo[:, a], side="left")
    sweep["axis_%d_widest_active_set" % a] = int((opened - closed).max())
row["sweep_and_prune"] = sweep
row["sweep_best_axis_pairs"] = min(sweep["axis_%d_overlapping_pairs" % a] for a in range(3))
row["sweep_best_over_grid"] = round(row["sweep_best_axis_pairs"] / max(box_tests, 1), 1)

# --- what the grid's levels are doing -------------------------------------
longest = sorted(max(hi[k, a] - lo[k, a] for a in range(3)) for k in range(m))
cell = max(longest[len(longest) // 2], 1e-6)
levels = {}
for k in range(m):
    lv = tuple(sc._grid_level(hi[k, a] - lo[k, a], cell) for a in range(3))
    levels[lv] = levels.get(lv, 0) + 1
row["grid_cell"] = cell
row["grid_levels"] = {str(k): v for k, v in sorted(levels.items())}
row["grid_level_groups"] = len(levels)
row["grid_group_passes"] = len(levels) * (len(levels) + 1) // 2

mem = psutil.Process().memory_info()
row["peak_rss_gb"] = getattr(mem, "peak_wset", mem.rss) / 1e9
row["system_cpu_pct"] = psutil.cpu_percent(None)
with open(sidecar_path, "rb") as fh:
    row["sidecar_sha1"] = hashlib.sha1(fh.read()).hexdigest()[:7]
row["started"] = time.strftime("%Y-%m-%dT%H:%M:%S")
with open(os.path.join(out_dir, "broad-phase.jsonl"), "a") as fh:
    fh.write(json.dumps(row) + "\n")
print(json.dumps(row, indent=1))
