"""The interference check with and without reusing a clash measured at the same
pose, for interference_index_kernel_test.go.

Usage: interference_cache.py <sidecar.py>

Builds one fixture twice through _build, with _INTERFERENCE_CACHE off and on, and
prints both answers as one JSON object.
"""

import importlib.util
import json
import math
import sys


def load(path):
    spec = importlib.util.spec_from_file_location("sidecar", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def turn_z(degrees):
    c, s = math.cos(math.radians(degrees)), math.sin(math.radians(degrees))
    return [c, -s, 0, s, c, 0, 0, 0, 1]


def turn_x(degrees):
    c, s = math.cos(math.radians(degrees)), math.sin(math.radians(degrees))
    return [1, 0, 0, 0, c, -s, 0, s, c]


ELL = {"start": [0.0, 0.0, 0.0], "edges": [
    {"to": [20.0, 0.0, 0.0]}, {"to": [20.0, 5.0, 0.0]}, {"to": [5.0, 5.0, 0.0]},
    {"to": [5.0, 15.0, 0.0]}, {"to": [0.0, 15.0, 0.0]}, {"to": [0.0, 0.0, 0.0]}]}


def fixture():
    """Row-major rotation matrices, as geometry.Solid sends them."""
    plate = {"width": 20.0, "height": 4.0, "depth": 20.0}
    pin = {"radius": 2.0, "height": 10.0}
    solids = []
    # Each station: a plate, and a pin through it turned about z. A turned pin
    # leans, and shares a different volume with its plate. Turns repeat, so the
    # cached run reuses answers; they also differ, so a pose read without its
    # rotation would reuse the wrong one.
    for n, deg in enumerate((0, 0, 35, 35, 35, 70, 0, 70, 20)):
        x = 100.0 * n
        solids.append({"id": "plate-%d" % n, "label": "Plate", "shape": "box", "dims": plate,
                       "matrix": turn_z(0), "position": [x, 0.0, 0.0]})
        solids.append({"id": "pin-%d" % n, "label": "Pin", "shape": "cylinder", "dims": pin,
                       "matrix": turn_z(deg), "position": [x + 3.0, 0.0, 2.0]})
    # An L through a plate, plain and mirrored: the mirror is a different shape.
    for n, mirrored in enumerate((False, True, True)):
        x = 1000.0 + 100.0 * n
        solids.append({"id": "slab-%d" % n, "label": "Slab", "shape": "box", "dims": plate,
                       "matrix": turn_z(0), "position": [x, 0.0, 0.0]})
        ell = {"id": "ell-%d" % n, "label": "Ell", "shape": "extrusion", "dims": {"depth": 5.0},
               "outline": ELL, "matrix": turn_z(90), "position": [x + 2.0, -6.0, 0.0]}
        if mirrored:
            ell["mirrored"] = True
        solids.append(ell)
    # A plate cut by a drill, with a pin standing in what is left: changed, so
    # measured on its own.
    solids.append({"id": "plate-cut", "label": "Plate", "shape": "box", "dims": plate,
                   "matrix": turn_z(0), "position": [2000.0, 0.0, 0.0]})
    solids.append({"id": "drill", "label": "Drill", "shape": "cylinder", "dims": {"radius": 3.0, "height": 20.0},
                   "matrix": turn_z(0), "position": [2000.0, 0.0, 0.0]})
    solids.append({"id": "pin-cut", "label": "Pin", "shape": "cylinder", "dims": pin,
                   "matrix": turn_z(0), "position": [2006.0, 0.0, 2.0]})
    operations = [{"id": "hole", "op": "cut", "of": "plate-cut", "with": ["drill"]}]
    # Added 2026-09-15 (large-box index): pins along a rail, straight and turned.
    # Inside the rail's length a pin shares the same volume wherever it sits, so
    # the cached run reuses it; a pin half over an end, or standing higher in the
    # rail, shares less and must be measured — a key that forgot either would
    # reuse a volume from a pin inside.
    rail = {"width": 400.0, "height": 20.0, "depth": 20.0}
    long_pin = {"radius": 2.0, "height": 30.0}
    for n, deg in enumerate((0, 30)):
        cx, c, s = 3000.0 + 1000.0 * n, math.cos(math.radians(deg)), math.sin(math.radians(deg))
        solids.append({"id": "rail-%d" % n, "label": "Rail", "shape": "box", "dims": rail,
                       "matrix": turn_z(deg), "position": [cx, 0.0, 0.0]})
        for m, (along, up, across) in enumerate(((-150, 0, 0), (0, 0, 0), (60, 0, 5), (197, 0, 0),
                                                 (-120, 8, 0), (200, 0, 0), (-200, 0, 0))):
            solids.append({"id": "rail-%d-pin-%d" % (n, m), "label": "Pin", "shape": "cylinder",
                           "dims": long_pin, "matrix": turn_z(deg),
                           "position": [cx + along * c - up * s, along * s + up * c, float(across)]})
        # Crossbars through the rail: the rail lies inside a crossbar's height and
        # depth, the crossbar inside the rail's length; one over the end shares less.
        for m, along in enumerate((-80, 40, 120, 198)):
            solids.append({"id": "rail-%d-bar-%d" % (n, m), "label": "Crossbar", "shape": "box",
                           "dims": {"width": 10.0, "height": 30.0, "depth": 60.0}, "matrix": turn_z(deg),
                           "position": [cx + along * c, along * s, 0.0]})
    # Crossbars turned a quarter about x, so a bar's height runs along the rail's
    # depth: the rail lies inside the bar's height, which frees the bar along the
    # rail's DEPTH, not its height. Both bars stand partly off the rail's height by
    # different amounts; a slide carried to the wrong axis would give them one key.
    solids.append({"id": "rail-2", "label": "Rail", "shape": "box", "dims": rail,
                   "matrix": turn_z(0), "position": [6000.0, 0.0, 0.0]})
    for m, (along, up) in enumerate(((0.0, 8.0), (100.0, 5.0))):
        solids.append({"id": "rail-2-bar-%d" % m, "label": "Crossbar", "shape": "box",
                       "dims": {"width": 10.0, "height": 30.0, "depth": 12.0}, "matrix": turn_x(90),
                       "position": [6000.0 + along, up, 0.0]})
    return {"solids": solids, "operations": operations, "format": ""}


def run(sidecar, cached):
    sidecar._INTERFERENCE_CACHE = cached
    try:
        reply = sidecar._build(fixture())
    finally:
        sidecar._INTERFERENCE_CACHE = True
    if not reply.get("ok") or reply.get("skipped") or reply.get("features_failed"):
        raise SystemExit(json.dumps({"error": "the fixture did not build: %s %s %s" % (
            reply.get("error"), reply.get("skipped"), reply.get("features_failed"))}))
    return {"found": reply["interferences"], "truncated": reply["interferences_truncated"],
            "pairs": reply["interference_pairs"], "booleans": reply["interference_booleans"],
            "reused": reply["interference_reused"]}


def main():
    sidecar = load(sys.argv[1])
    json.dump({"uncached": run(sidecar, False), "cached": run(sidecar, True)}, sys.stdout)


if __name__ == "__main__":
    main()
