"""Where the kernel's interference check goes, on the real _build's own solids
(docs/spikes/2026-09-15-check-profile).

_build runs as shipped up to the check, with _interferences replaced by a capture of
its arguments. Then, on those same solids, in the same process:

  1. the check, uninstrumented and timed, with garbage collection timed by gc callback
     and the machine's CPU read around it; its answer is written to check-<tag>.json,
     which is the reference an optimized check must reproduce exactly;
  2. its first two steps alone (_measures, _candidate_pairs), timed;
  3. the check again under cProfile, written as text (top by own time and by
     cumulative time) and as a .prof file.

The profiler costs a lot per Python call, and the check is mostly Python calls, so
(3) gives shares, not seconds; (1) gives the seconds.

Usage: <cad venv python> profile_check.py <sidecar.py> <barrel-N.json> <out dir> <tag> [--no-profile]
"""
import cProfile
import gc
import importlib.util
import io
import json
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

real = sc._interferences
captured = {}


def capture(solids, ids, labels, placed=None):
    captured["args"] = (solids, ids, labels, placed)
    return [], False, 0, {"pairs": 0, "booleans": 0, "reused": 0}


sc._interferences = capture
with open(request_path, "rb") as fh:
    request = json.loads(fh.read())
request["format"] = ""
t = time.perf_counter()
sc._build(request)
build_s = time.perf_counter() - t
del request
args = captured.pop("args")

gcstat = {"collections": 0, "s": 0.0, "gen2": 0, "gen2_s": 0.0}
started = [0.0]


def on_gc(phase, info):
    if phase == "start":
        started[0] = time.perf_counter()
        return
    dt = time.perf_counter() - started[0]
    gcstat["collections"] += 1
    gcstat["s"] += dt
    if info.get("generation") == 2:
        gcstat["gen2"] += 1
        gcstat["gen2_s"] += dt


psutil.cpu_percent(None)
gc.callbacks.append(on_gc)
t = time.perf_counter()
listed, truncated, box_tests, stats = real(*args)
check_s = time.perf_counter() - t
gc.callbacks.remove(on_gc)
cpu = psutil.cpu_percent(None)
with open(os.path.join(out_dir, "check-%s.json" % tag), "w") as fh:
    json.dump({"listed": listed, "truncated": truncated, "box_tests": box_tests, "stats": stats}, fh)
found = stats.get("found")
del listed

solids, ids, labels, placed = args
t = time.perf_counter()
boxes, volumes = sc._measures(solids, placed)
measures_s = time.perf_counter() - t
t = time.perf_counter()
pairs, _ = sc._candidate_pairs(boxes)
candidate_s = time.perf_counter() - t
n_pairs = len(pairs)

# The per-pair loop, split by subtraction, uninstrumented: each loop below does
# what the one before it did and one step more, over every candidate pair.
split = {}
shape_ids, slabs = {}, {}
t = time.perf_counter()
for p in placed:
    if p is not None and p[0] not in shape_ids:
        shape_ids[p[0]] = len(shape_ids)
        slabs[p[0]] = sc._slabs(p[0], p[2])
split["slabs_s"] = time.perf_counter() - t
t = time.perf_counter()
for i, j in pairs:
    pass
split["bare_loop_s"] = time.perf_counter() - t
t = time.perf_counter()
for i, j in pairs:
    pi, pj = placed[i], placed[j]
    ahead, back = pi[1].inverse() * pj[1], pj[1].inverse() * pi[1]
split["relative_locations_s"] = time.perf_counter() - t
t = time.perf_counter()
for i, j in pairs:
    pi, pj = placed[i], placed[j]
    ahead, back = pi[1].inverse() * pj[1], pj[1].inverse() * pi[1]
    sc._pose(ahead), sc._pose(back)
split["plus_poses_s"] = time.perf_counter() - t
t = time.perf_counter()
for i, j in pairs:
    pi, pj = placed[i], placed[j]
    ahead, back = pi[1].inverse() * pj[1], pj[1].inverse() * pi[1]
    sc._pose(ahead), sc._pose(back)
    sc._inside(ahead, slabs[pi[0]], slabs[pj[0]]), sc._inside(back, slabs[pj[0]], slabs[pi[0]])
split["plus_inside_s"] = time.perf_counter() - t
t = time.perf_counter()
keys = [sc._pair_key(placed, shape_ids, i, j, slabs) for i, j in pairs]
split["pair_keys_s"] = time.perf_counter() - t
cache = {}
t = time.perf_counter()
for key in keys:
    if key in cache:
        pass
    else:
        cache[key] = None
split["cache_lookups_s"] = time.perf_counter() - t
split["distinct_keys"] = len(cache)
del keys, cache, boxes, volumes, pairs

row = {"tag": tag, "sidecar": os.path.abspath(sidecar_path), "parts": len(solids), "build_to_check_s": build_s,
       "check_s": check_s, "check_gc": gcstat, "measures_s": measures_s, "candidate_pairs_s": candidate_s,
       "pairs": n_pairs, "found": found, "stats": stats, "box_tests": box_tests, "truncated": truncated,
       "split": split, "system_cpu_pct": cpu, "started": time.strftime("%Y-%m-%dT%H:%M:%S")}

if profile:
    pr = cProfile.Profile()
    psutil.cpu_percent(None)
    t = time.perf_counter()
    pr.enable()
    real(*args)
    pr.disable()
    row["profiled_check_s"] = time.perf_counter() - t
    row["profiled_cpu_pct"] = psutil.cpu_percent(None)
    pr.dump_stats(os.path.join(out_dir, "check-%s.prof" % tag))
    text = io.StringIO()
    st = pstats.Stats(pr, stream=text)
    text.write("== %s: by own time ==\n" % tag)
    st.sort_stats("tottime").print_stats(40)
    text.write("\n== %s: by cumulative time ==\n" % tag)
    st.sort_stats("cumulative").print_stats(40)
    with open(os.path.join(out_dir, "profile-%s.txt" % tag), "w") as fh:
        fh.write(text.getvalue())

mem = psutil.Process().memory_info()
row["peak_rss_gb"] = getattr(mem, "peak_wset", mem.rss) / 1e9
with open(os.path.join(out_dir, "profile-check.jsonl"), "a") as fh:
    fh.write(json.dumps(row) + "\n")
print(json.dumps(row, indent=1))
