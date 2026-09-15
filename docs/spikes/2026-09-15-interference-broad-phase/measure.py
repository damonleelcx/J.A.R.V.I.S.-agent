"""Measure stage K2b (interference broad phase) past S0's build ceiling.

Phase 4, stage K2b of docs/plan-2026-09-13-millions-of-parts.md replaces the
interference check's every-pair box scan with a sort-and-sweep. cad.BuildDocument
refuses more than 4096 parts (stage S0), so this calls the sidecar's own _build
directly, with the request K1's spike used, at 1,000, 4,096 and 10,000 occurrences:

  sweep      -- the sidecar as shipped;
  every-pair -- _candidate_pairs replaced by the scan it replaced: every pair's
                boxes compared, in index order.

A box on a grid 30 mm apart, so no two boxes meet: all of the interference phase
is broad phase, which is the part K2b changed.

Usage: <cad venv python> docs/spikes/2026-09-15-interference-broad-phase/measure.py
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


def request(n):
    side = int(math.ceil(math.sqrt(n)))
    solids = [{"id": "box-%d" % (i + 1), "label": "box", "shape": "box",
               "dims": {"width": 4.0, "height": 6.0, "depth": 8.0}, "matrix": IDENTITY,
               "position": [30.0 * (i % side), 0.0, 30.0 * (i // side)]} for i in range(n)]
    return {"solids": solids, "operations": [], "format": ""}


def every_pair(boxes):
    pairs, tests = [], 0
    for i in range(len(boxes)):
        for j in range(i + 1, len(boxes)):
            tests += 1
            if not sidecar._boxes_miss(boxes[i], boxes[j]):
                pairs.append((i, j))
    return pairs, tests


def run(n, sweep):
    original = sidecar._candidate_pairs
    if not sweep:
        sidecar._candidate_pairs = every_pair
    try:
        t = time.perf_counter()
        reply = sidecar._build(request(n))
        wall = time.perf_counter() - t
    finally:
        sidecar._candidate_pairs = original
    phase = reply["phases"]["interferences"]
    return wall, phase, reply["interference_box_tests"], len(reply["interferences"])


def main():
    print("%7s %-10s %9s %15s %6s %12s %s" % ("N", "mode", "build s", "interference s", "share", "box tests", "found"))
    for n in (1000, 4096, 10000):
        for sweep in (True, False):
            wall, phase, tests, found = run(n, sweep)
            print("%7d %-10s %9.2f %15.3f %5.0f%% %12d %d" % (n, "sweep" if sweep else "every-pair",
                                                           wall, phase, 100 * phase / wall, tests, found))
            sys.stdout.flush()


if __name__ == "__main__":
    main()
