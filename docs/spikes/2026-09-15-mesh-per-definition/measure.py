"""Measure stage K4 (mesh per definition) past S0's build ceiling.

Phase 4, stage K4 of docs/plan-2026-09-13-millions-of-parts.md accepts on "30k-occurrence
car mesh payload size and time recorded; triangles counted once per definition".
cad.BuildDocument refuses more than 4096 parts (stage S0), so this calls the sidecar's
own _build with format "mesh", N copies of one 4 x 6 x 8 mm box on a square grid 30 mm
apart, twice each:

  per-definition -- the sidecar as shipped;
  per-solid      -- _MESH_PER_DEFINITION off, the tessellation K4 replaced.

"mesh s" is the sidecar's phases.mesh; "payload MB" is the JSON of the reply's mesh
fields, which is what the mesh endpoint forwards.

Usage: <cad venv python> docs/spikes/2026-09-15-mesh-per-definition/measure.py [N ...]
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

IDENTITY = [1, 0, 0, 0, 1, 0, 0, 0, 1]
MESH_FIELDS = ("mesh", "mesh_definitions", "mesh_instances")


def request(n):
    side = int(math.ceil(math.sqrt(n)))
    solids = [{"id": "box-%d" % (i + 1), "label": "box", "shape": "box",
               "dims": {"width": 4.0, "height": 6.0, "depth": 8.0}, "matrix": IDENTITY,
               "position": [30.0 * (i % side), 0.0, 30.0 * (i // side)]} for i in range(n)]
    return {"solids": solids, "operations": [], "format": "mesh"}


def run(n, per_definition):
    sidecar._MESH_PER_DEFINITION = per_definition
    try:
        t = time.perf_counter()
        reply = sidecar._build(request(n))
        wall = time.perf_counter() - t
    finally:
        sidecar._MESH_PER_DEFINITION = True
    payload = len(json.dumps({k: reply[k] for k in MESH_FIELDS if k in reply}))
    return (wall, reply["phases"].get("mesh", 0.0), reply.get("mesh_triangles", 0),
            payload, len(reply.get("mesh_definitions") or []), reply.get("mesh_error", ""))


def main():
    sizes = [int(a) for a in sys.argv[1:]] or [1000, 4096, 10000, 30000]
    print("%7s %-15s %9s %8s %10s %11s %5s %s" % ("N", "mode", "build s", "mesh s", "triangles",
                                                 "payload MB", "defs", "error"))
    for n in sizes:
        for per_definition in (True, False):
            wall, mesh, triangles, payload, defs, error = run(n, per_definition)
            print("%7d %-15s %9.2f %8.3f %10d %11.2f %5d %s" % (
                n, "per-definition" if per_definition else "per-solid", wall, mesh, triangles,
                payload / 1e6, defs, error))
            sys.stdout.flush()


if __name__ == "__main__":
    main()
