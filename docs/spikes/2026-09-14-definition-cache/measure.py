"""Measure stage K1 (definition cache) where the Go path cannot: past S0's build ceiling.

Phase 4, stage K1 of docs/plan-2026-09-13-millions-of-parts.md accepts on "1 definition
x 10k occurrences builds with one shape build (counted), exact volume per occurrence".
cad.BuildDocument refuses more than 4096 parts (stage S0), so this calls the sidecar's
own _build directly with the same request the kernel would send, at 1,000, 4,096 and
10,000 occurrences, twice each:

  cached    -- the sidecar as shipped: each distinct shape built once;
  uncached  -- _shape_key made unique per occurrence, which is the sidecar before K1.

Primitives only (a box and a cylinder), placed on a grid far enough apart that no
two bounding boxes meet, so the interference check stays out of the timing.

Usage: <cad venv python> docs/spikes/2026-09-14-definition-cache/measure.py
"""
import importlib.util
import json
import math
import os
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
SIDECAR = os.path.join(HERE, "..", "..", "..", "internal", "domain", "cad", "sidecar.py")

spec = importlib.util.spec_from_file_location("sidecar", SIDECAR)
sidecar = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sidecar)

SHAPES = {
    "box": ({"width": 4.0, "height": 6.0, "depth": 8.0}, 4.0 * 6.0 * 8.0),
    "cylinder": ({"radius": 2.0, "height": 10.0}, math.pi * 2.0 * 2.0 * 10.0),
}
IDENTITY = [1, 0, 0, 0, 1, 0, 0, 0, 1]


def request(shape, n):
    dims, _ = SHAPES[shape]
    side = int(math.ceil(math.sqrt(n)))
    solids = []
    for i in range(n):
        solids.append({"id": "%s-%d" % (shape, i + 1), "label": shape, "shape": shape, "dims": dims,
                       "matrix": IDENTITY, "position": [30.0 * (i % side), 0.0, 30.0 * (i // side)]})
    return {"solids": solids, "operations": [], "format": ""}


def run(shape, n, cached):
    original = sidecar._shape_key
    if not cached:
        sidecar._shape_key = lambda s: s["id"]
    try:
        t = time.perf_counter()
        reply = sidecar._build(request(shape, n))
        wall = time.perf_counter() - t
    finally:
        sidecar._shape_key = original
    _, volume = SHAPES[shape]
    exact = abs(reply.get("volume", 0.0) - n * volume) <= 1e-6 * n * volume
    return wall, reply.get("shape_builds"), reply.get("parts"), exact


def main():
    print("%-9s %7s %-9s %9s %13s %7s %s" % ("shape", "N", "mode", "build s", "shape_builds", "parts", "volume exact"))
    for shape in ("box", "cylinder"):
        for n in (1000, 4096, 10000):
            for cached in (True, False):
                wall, builds, parts, exact = run(shape, n, cached)
                print("%-9s %7d %-9s %9.2f %13s %7s %s" % (shape, n, "cached" if cached else "uncached",
                                                         wall, builds, parts, exact))
                sys.stdout.flush()


if __name__ == "__main__":
    main()
