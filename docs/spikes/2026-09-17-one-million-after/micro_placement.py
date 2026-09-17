"""Two per-occurrence costs in _placement and _located that were NOT changed, priced one
call at a time (docs/spikes/2026-09-17-one-million-after, item 5): the origin built as a
build123d Vector and converted, against a gp_Pnt built directly; and downcast() of the
moved shape, against the cast chosen once for the definition's shape type.

Usage: <cad venv python> micro_placement.py <sidecar.py> <barrel-N.json>
"""
import importlib.util
import json
import sys
import time

import psutil

spec = importlib.util.spec_from_file_location("sidecar", sys.argv[1])
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)
from build123d import Vector  # noqa: E402
from build123d.topology.shape_core import Shape, downcast  # noqa: E402
from OCP.gp import gp_Pnt  # noqa: E402

with open(sys.argv[2], "rb") as fh:
    solid = json.loads(fh.read())["solids"][0]
pos = solid["position"]
shape = sc._shape(solid)
loc = sc._placement(solid, {})
cast = Shape.downcast_LUT[shape.wrapped.ShapeType()]


def bench(fn, n=200000):
    fn()
    t = time.perf_counter()
    for _ in range(n):
        fn()
    return 1e6 * (time.perf_counter() - t) / n


psutil.cpu_percent(None)
out = {}
for rep in (1, 2):
    out["Vector(*pos).to_pnt() r%d" % rep] = bench(lambda: Vector(*pos).to_pnt())
    out["gp_Pnt(*pos) r%d" % rep] = bench(lambda: gp_Pnt(*pos))
    out["downcast(Moved) r%d" % rep] = bench(lambda: downcast(shape.wrapped.Moved(loc.wrapped)), 50000)
    out["cast(Moved) r%d" % rep] = bench(lambda: cast(shape.wrapped.Moved(loc.wrapped)), 50000)
a, b = Vector(*pos).to_pnt(), gp_Pnt(*pos)
out["same_bits"] = (a.X(), a.Y(), a.Z()) == (b.X(), b.Y(), b.Z())
out["host_cpu_pct"] = psutil.cpu_percent(None)
print(json.dumps(out, indent=1))
