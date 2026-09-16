"""Measure stage V1 (interference at scale) past S0's build ceiling.

Phase 5, stage V1 of docs/plan-2026-09-13-millions-of-parts.md accepts on "30k
occurrences finish in a recorded time with full coverage". cad.BuildDocument refuses
more than 4096 parts (stage S0), so this calls the sidecar's own _build directly:

  pinned -- N/3 plates, each with two pins standing through it (2N/3 clashes, two
            poses), with _INTERFERENCE_CACHE on (as shipped) and off;
  plane  -- N studs spread evenly over a plane, touching nothing (box tests only).

Usage: <cad venv python> docs/spikes/2026-09-15-interference-index/measure.py [N ...]
"""
import importlib.util
import math
import os
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
SIDECAR = os.path.join(HERE, "..", "..", "..", "internal", "domain", "cad", "sidecar.py")

spec = importlib.util.spec_from_file_location("sidecar", SIDECAR)
sidecar = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sidecar)

IDENTITY = [1, 0, 0, 0, 1, 0, 0, 0, 1]


def pinned(n):
    solids = []
    for c in range(n // 3):
        x = 40.0 * c
        solids.append({"id": "plate-%d" % c, "label": "Plate", "shape": "box",
                       "dims": {"width": 20.0, "height": 4.0, "depth": 20.0},
                       "matrix": IDENTITY, "position": [x, 0.0, 0.0]})
        for side, dx in (("l", -5.0), ("r", 5.0)):
            solids.append({"id": "pin-%s-%d" % (side, c), "label": "Pin", "shape": "cylinder",
                           "dims": {"radius": 2.0, "height": 10.0},
                           "matrix": IDENTITY, "position": [x + dx, 0.0, 0.0]})
    return {"solids": solids, "operations": [], "format": ""}


def plane(n):
    side = int(math.ceil(math.sqrt(n)))
    return {"solids": [{"id": "stud-%d" % i, "label": "Stud", "shape": "box",
                        "dims": {"width": 4.0, "height": 6.0, "depth": 8.0}, "matrix": IDENTITY,
                        "position": [10.0 * (i % side), 0.0, 20.0 * (i // side)]} for i in range(n)],
            "operations": [], "format": ""}


def run(request, cached=True):
    sidecar._INTERFERENCE_CACHE = cached
    try:
        t = time.perf_counter()
        reply = sidecar._build(request)
        wall = time.perf_counter() - t
    finally:
        sidecar._INTERFERENCE_CACHE = True
    return wall, reply


def main():
    sizes = [int(a) for a in sys.argv[1:]] or [3000, 9000, 30000]
    print("%7s %-14s %8s %14s %10s %9s %9s %8s %7s %9s" % (
        "N", "mode", "build s", "interference s", "box tests", "pairs", "booleans", "reused", "found", "truncated"))
    for n in sizes:
        for mode, request, cached in (("pinned cached", pinned(n), True), ("pinned uncached", pinned(n), False),
                                      ("plane", plane(n), True)):
            wall, r = run(request, cached)
            print("%7d %-14s %8.2f %14.3f %10d %9d %9d %8d %7d %9s" % (
                len(request["solids"]), mode, wall, r["phases"]["interferences"], r["interference_box_tests"],
                r["interference_pairs"], r["interference_booleans"], r["interference_reused"],
                len(r["interferences"]), r["interferences_truncated"]))
            sys.stdout.flush()


if __name__ == "__main__":
    main()
