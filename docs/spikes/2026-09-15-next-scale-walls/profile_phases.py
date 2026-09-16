"""Where the kernel's shapes and assembly phases go, on the real _build, split by
wrapping the calls it makes (docs/spikes/2026-09-15-next-scale-walls).

Each wrapped call is counted and timed: FORGE's _shape_key, _placement, _shape and
(from this change) _located; build123d's Shape.moved and the BRepBuilderAPI_Copy
inside it; the assembly's Compound, its bounding box, get_type and volume, and the
per-solid volume integral. Garbage collections are counted and timed by gc callback.
The wrappers add about a microsecond a call, which is in every row alike. The
interference check is replaced by a no-op: it is not the phase measured here.

Usage: <cad venv python> profile_phases.py <sidecar.py> <barrel-N.json> <out.jsonl> <tag>
"""
import gc
import importlib.util
import json
import os
import sys
import time

import psutil

spec = importlib.util.spec_from_file_location("sidecar", sys.argv[1])
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)
import build123d.topology.shape_core as core
import build123d.topology.composite as composite

acc = {}


def timed(name, fn):
    def wrapper(*a, **k):
        t = time.perf_counter()
        try:
            return fn(*a, **k)
        finally:
            e = acc.setdefault(name, [0, 0.0])
            e[0] += 1
            e[1] += time.perf_counter() - t
    return wrapper


# FORGE's own steps.
for name in ("_shape_key", "_placement", "_shape"):
    setattr(sc, name, timed(name, getattr(sc, name)))
for name in ("_located",):
    if hasattr(sc, name):
        setattr(sc, name, timed(name, getattr(sc, name)))
# build123d's: a located copy, and the B-rep copy inside it; the compound; its box and volume.
core.Shape.moved = timed("Shape.moved", core.Shape.moved)
core.BRepBuilderAPI_Copy = timed("BRepBuilderAPI_Copy", core.BRepBuilderAPI_Copy)
sc.Compound = timed("Compound(built)", sc.Compound)
composite.Compound.bounding_box = timed("Compound.bounding_box", composite.Compound.bounding_box)
composite.Compound.get_type = timed("Compound.get_type", composite.Compound.get_type)
core.Shape.compute_mass = staticmethod(timed("Shape.compute_mass", core.Shape.compute_mass))
vol = composite.Compound.volume.fget
composite.Compound.volume = property(timed("Compound.volume", vol))

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


gc.callbacks.append(on_gc)
sc._interferences = lambda solids, ids, labels, placed=None: ([], False, 0, {"pairs": 0, "booleans": 0, "reused": 0})

with open(sys.argv[2], "rb") as fh:
    request = json.loads(fh.read())
request["format"] = ""
psutil.cpu_percent(None)
t = time.perf_counter()
reply = sc._build(request)
wall = time.perf_counter() - t
cpu = psutil.cpu_percent(None)
mem = psutil.Process().memory_info()
row = {"tag": sys.argv[4], "sidecar": os.path.abspath(sys.argv[1]), "parts": reply.get("parts"),
       "wall_s": wall, "phases": reply.get("phases"), "volume": reply.get("volume"), "bounds": reply.get("bounds"),
       "calls": {k: {"n": v[0], "s": v[1], "us_each": 1e6 * v[1] / v[0] if v[0] else 0} for k, v in acc.items()},
       "gc": gcstat, "gc_objects": len(gc.get_objects()), "system_cpu_pct": cpu,
       "peak_rss_gb": getattr(mem, "peak_wset", mem.rss) / 1e9}
with open(sys.argv[3], "a") as fh:
    fh.write(json.dumps(row) + "\n")
print(json.dumps(row, indent=1))
