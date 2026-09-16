"""Where the kernel's SHAPES phase goes, on the real _build
(docs/spikes/2026-09-15-last-hot-spots).

#113 cut the shapes phase from 273 s to 39 s at 1M by building a Plane per distinct
rotation, dropping the B-rep copy `Shape.moved` makes and throws away, and
integrating a volume once per definition. 39 s is still 38% of the 1M build and had
never been profiled after that change. This says what a placed occurrence still
costs, and of what.

_build runs as shipped at format "", with _interferences replaced by a no-op, three
times on the same request in one process:

  1. uninstrumented and timed, phase by phase, with garbage collection timed by a gc
     callback and the machine's CPU read around it -- this gives the SECONDS;
  2. again with the three calls the shapes loop makes counted and timed where _build
     makes them (_shape_key, _placement, _located), and build123d's Location and
     Plane constructors and copy.deepcopy counted underneath them -- this gives the
     SHARES, at about 0.2 us a wrapped call;
  3. again under cProfile, written as text and as a .prof file -- the profiler roughly
     doubles a Python call, so this too gives shares, not seconds.

Then a micro-benchmark: each primitive one occurrence pays for, timed one call at a
time on the fixture's own first placed solid, so a share can be read as "this many
microseconds of OCCT and this many of Python around it".

Usage: <cad venv python> profile_shapes.py <sidecar.py> <barrel-N.json> <out dir> <tag>
                                            [--no-profile] [--no-micro]
"""
import copy as copy_module
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
flags = sys.argv[5:]
profile = "--no-profile" not in flags
micro = "--no-micro" not in flags
spec = importlib.util.spec_from_file_location("sidecar", sidecar_path)
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)

# The check is not what this measures, and at 1M it is half the build.
sc._interferences = lambda solids, ids, labels, placed=None: (
    [], False, 0, {"pairs": 0, "booleans": 0, "reused": 0, "found": 0, "summarized": False, "buried": 0})

with open(request_path, "rb") as fh:
    REQUEST = json.loads(fh.read())
REQUEST["format"] = ""
REQUEST.pop("properties", None)


def timed_build(label):
    """_build on a fresh copy of the request, with gc collections counted and timed."""
    gc_time = [0.0, 0]
    started = [0.0]

    def on_gc(phase, info):
        if phase == "start":
            started[0] = time.perf_counter()
        else:
            gc_time[0] += time.perf_counter() - started[0]
            gc_time[1] += 1

    gc.callbacks.append(on_gc)
    psutil.cpu_percent(None)
    t = time.perf_counter()
    try:
        reply = sc._build(dict(REQUEST))
    finally:
        gc.callbacks.remove(on_gc)
    wall = time.perf_counter() - t
    cpu = psutil.cpu_percent(None)
    if not reply.get("ok"):
        raise SystemExit("%s: the build failed: %s" % (label, reply.get("error")))
    return reply, {"label": label, "build_s": wall, "phases": reply.get("phases"),
                   "gc_s": gc_time[0], "gc_collections": gc_time[1], "system_cpu_pct": cpu,
                   "parts": reply.get("parts"), "shape_builds": reply.get("shape_builds")}


# --- 1. the seconds --------------------------------------------------------
reply, plain = timed_build("uninstrumented")
mem = psutil.Process().memory_info()
plain["peak_rss_gb"] = getattr(mem, "peak_wset", mem.rss) / 1e9
del reply

# --- 2. where they go ------------------------------------------------------
#
# Counted and timed where _build calls them. A wrapper costs about 0.2 us, so at a
# million occurrences each wrapped call adds ~0.2 s to the phase; the seconds above
# are the ones to quote, these are the shares.
tally = {}


def counting(name, fn):
    row = tally.setdefault(name, {"calls": 0, "s": 0.0})

    def wrapped(*a, **k):
        t = time.perf_counter()
        try:
            return fn(*a, **k)
        finally:
            row["calls"] += 1
            row["s"] += time.perf_counter() - t
    return wrapped


def just_counting(name, fn):
    row = tally.setdefault(name, {"calls": 0, "s": 0.0})

    def wrapped(*a, **k):
        row["calls"] += 1
        return fn(*a, **k)
    return wrapped


import build123d  # noqa: E402  (after the sidecar, which imports it)

originals = {"_shape_key": sc._shape_key, "_placement": sc._placement, "_located": sc._located,
             "_shape": sc._shape}
plane_init, location_init = build123d.Plane.__init__, build123d.Location.__init__
vector_init, deepcopy = build123d.Vector.__init__, copy_module.deepcopy
for name in originals:
    setattr(sc, name, counting(name, originals[name]))
build123d.Plane.__init__ = just_counting("Plane.__init__", plane_init)
build123d.Location.__init__ = just_counting("Location.__init__", location_init)
build123d.Vector.__init__ = just_counting("Vector.__init__", vector_init)
copy_module.deepcopy = counting("copy.deepcopy", deepcopy)
try:
    reply, instrumented = timed_build("instrumented")
finally:
    for name, fn in originals.items():
        setattr(sc, name, fn)
    build123d.Plane.__init__ = plane_init
    build123d.Location.__init__ = location_init
    build123d.Vector.__init__ = vector_init
    copy_module.deepcopy = deepcopy
instrumented["calls"] = tally
instrumented["us_each"] = {k: 1e6 * v["s"] / v["calls"] for k, v in tally.items() if v["calls"]}
del reply

# --- 3. under cProfile -----------------------------------------------------
profiled = None
if profile:
    pr = cProfile.Profile()
    psutil.cpu_percent(None)
    t = time.perf_counter()
    pr.enable()
    sc._build(dict(REQUEST))
    pr.disable()
    profiled = {"profiled_build_s": time.perf_counter() - t, "profiled_cpu_pct": psutil.cpu_percent(None)}
    pr.dump_stats(os.path.join(out_dir, "shapes-%s.prof" % tag))
    text = io.StringIO()
    st = pstats.Stats(pr, stream=text)
    text.write("== %s: by own time ==\n" % tag)
    st.sort_stats("tottime").print_stats(40)
    text.write("\n== %s: by cumulative time ==\n" % tag)
    st.sort_stats("cumulative").print_stats(40)
    with open(os.path.join(out_dir, "profile-shapes-%s.txt" % tag), "w") as fh:
        fh.write(text.getvalue())

# --- 4. what one occurrence pays for, primitive by primitive ---------------
#
# Timed one call at a time on the fixture's own first solid, so the shares above can
# be read as OCCT against the Python around it.
prims = {}
if micro:
    from build123d import Location, Plane, Vector
    from build123d.topology import downcast
    from OCP.gp import gp_Ax3, gp_Trsf
    from OCP.TopLoc import TopLoc_Location

    solid = REQUEST["solids"][0]
    m, position = solid["matrix"], solid["position"]
    shape = sc._shape(solid)
    origin = Vector(*position)
    plane = Plane(origin=origin, x_dir=Vector(m[0], m[3], m[6]), z_dir=Vector(m[2], m[5], m[8]))
    axes = (plane.z_dir.to_dir(), plane.x_dir.to_dir())
    trsf = gp_Trsf()
    trsf.SetTransformation(gp_Ax3(origin.to_pnt(), axes[0], axes[1]))
    trsf.Invert()
    location = Location(TopLoc_Location(trsf))
    key = sc._shape_key(solid)

    def bench(name, fn, n=20000):
        fn()
        t = time.perf_counter()
        for _ in range(n):
            fn()
        prims[name] = 1e6 * (time.perf_counter() - t) / n

    def place_trsf():
        o = Vector(*position).to_pnt()
        tr = gp_Trsf()
        tr.SetTransformation(gp_Ax3(o, axes[0], axes[1]))
        tr.Invert()
        return tr

    bench("_shape_key (json.dumps)", lambda: sc._shape_key(solid))
    bench("json.dumps of the same dict", lambda: json.dumps(
        {k: solid.get(k) for k in sc._SHAPE_KEYS}, sort_keys=True, separators=(",", ":")))
    bench("a tuple of the same fields", lambda: tuple(repr(solid.get(k)) for k in sc._SHAPE_KEYS))
    bench("repr(matrix)", lambda: repr(m))
    bench("Vector(*position)", lambda: Vector(*position))
    bench("Vector.to_pnt", lambda: origin.to_pnt())
    bench("gp_Trsf()", lambda: gp_Trsf())
    bench("gp_Ax3(pnt, dir, dir)", lambda: gp_Ax3(origin.to_pnt(), axes[0], axes[1]))
    bench("SetTransformation + Invert (whole)", place_trsf)
    bench("TopLoc_Location(trsf)", lambda: TopLoc_Location(trsf))
    bench("Location(TopLoc_Location)", lambda: Location(TopLoc_Location(trsf)))
    bench("_placement (shared frames)", lambda: sc._placement(solid, {repr(m): axes}))
    bench("_placement (no frames dict)", lambda: sc._placement(solid))
    bench("shape.wrapped.Moved(loc)", lambda: shape.wrapped.Moved(location.wrapped), 5000)
    bench("downcast(Moved)", lambda: downcast(shape.wrapped.Moved(location.wrapped)), 5000)
    bench("_located", lambda: sc._located(shape, location), 5000)
    bench("copy.deepcopy of the shape's dict values",
          lambda: [copy_module.deepcopy(v, {}) for k, v in shape.__dict__.items() if k != "topo_parent"], 5000)
    prims["shape attributes"] = sorted(shape.__dict__)
    prims["shape class"] = type(shape).__name__
    prims["shape key bytes"] = len(key)

# --- out -------------------------------------------------------------------
solids = REQUEST["solids"]
row = {"tag": tag, "sidecar": os.path.abspath(sidecar_path), "sidecar_sha1": None,
       "occurrences": len(solids),
       "distinct_shape_keys": len({sc._shape_key(s) for s in solids}),
       "distinct_matrices": len({repr(s["matrix"]) for s in solids}),
       "plain": plain, "instrumented": instrumented, "profiled": profiled, "primitives_us": prims,
       "started": time.strftime("%Y-%m-%dT%H:%M:%S")}
import hashlib  # noqa: E402

with open(sidecar_path, "rb") as fh:
    row["sidecar_sha1"] = hashlib.sha1(fh.read()).hexdigest()[:7]
with open(os.path.join(out_dir, "profile-shapes.jsonl"), "a") as fh:
    fh.write(json.dumps(row) + "\n")
print(json.dumps(row, indent=1))
