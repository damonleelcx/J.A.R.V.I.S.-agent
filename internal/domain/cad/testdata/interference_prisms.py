"""Clashes slid along cylinders and extrusions, against every pair measured, for
interference_prism_kernel_test.go.

Usage: interference_prisms.py <sidecar.py> <seed> [<seed> ...]

For each seed builds one randomized fixture through _build twice, with
_INTERFERENCE_CACHE off (every pair its own boolean) and on, and prints both
answers per seed as {"runs": [{"seed", "uncached", "cached"}]}.

The fixture is where a slide along a prism could go wrong:
  - shafts (cylinders) along each axis and turned, with collars around them inside
    their length, AT and just inside and outside the ends by less than, equal to and
    more than the containment margin, and half over; collars off the axis, collars
    turned about the shaft and leaning across it;
  - pins through a shaft crosswise, and pins leaning along it;
  - extruded L girders, plain, mirrored and turned, with cleats and pins along
    their depth, near and over their ends, and leaning pins (the girder case);
  - a CONE with rings along it, which is not a prism: a ring higher up shares less,
    so a slide claimed for cones reuses a wrong volume;
  - a short extrusion whose depth is less than what passes through it.
"""

import importlib.util
import json
import math
import random
import sys


def load(path):
    spec = importlib.util.spec_from_file_location("sidecar", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def rot(ax, ay, az):
    ax, ay, az = math.radians(ax), math.radians(ay), math.radians(az)
    cx, sx, cy, sy, cz, sz = math.cos(ax), math.sin(ax), math.cos(ay), math.sin(ay), math.cos(az), math.sin(az)
    rx = [[1, 0, 0], [0, cx, -sx], [0, sx, cx]]
    ry = [[cy, 0, sy], [0, 1, 0], [-sy, 0, cy]]
    rz = [[cz, -sz, 0], [sz, cz, 0], [0, 0, 1]]
    return mul(rz, mul(ry, rx))


def mul(a, b):
    return [[sum(a[i][k] * b[k][j] for k in range(3)) for j in range(3)] for i in range(3)]


def apply(r, v):
    return [sum(r[i][k] * v[k] for k in range(3)) for i in range(3)]


def flat(r):
    return [r[i][j] for i in range(3) for j in range(3)]


GIRDER = {"start": [0.0, 0.0, 0.0], "edges": [
    {"to": [40.0, 0.0, 0.0]}, {"to": [40.0, 6.0, 0.0]}, {"to": [6.0, 6.0, 0.0]},
    {"to": [6.0, 40.0, 0.0]}, {"to": [0.0, 40.0, 0.0]}, {"to": [0.0, 0.0, 0.0]}]}
MARGIN = 1e-4


def fixture(seed):
    rng = random.Random(seed)
    solids = []

    def add(name, spec, frame, local, place):
        """A part placed at `local` in a frame (rotation, origin), turned by `place`."""
        r, origin = frame
        position = [a + b for a, b in zip(origin, apply(r, local))]
        solids.append(dict(spec, id="%s-%d" % (name, len(solids)), label=name, matrix=flat(mul(r, place)),
                           position=position))

    ident = rot(0, 0, 0)
    # --- shafts: a cylinder is along its own y --------------------------------
    for s, turn in enumerate([(0, 0, 0), (0, 0, 90), (90, 0, 0), (rng.uniform(0, 90), rng.uniform(0, 90), 0)]):
        frame = (rot(*turn), [0.0, 0.0, 3000.0 * s])
        length, radius = 400.0, 10.0
        add("shaft", {"shape": "cylinder", "dims": {"radius": radius, "height": length}}, frame, [0, 0, 0], ident)
        collar = {"shape": "box", "dims": {"width": 30.0, "height": 10.0, "depth": 30.0}}
        half = length / 2 - 5.0  # a collar's centre when its face is at the end
        spots = [rng.uniform(-150, 150) for _ in range(6)]
        spots += [half, -half, half - MARGIN, half - 2 * MARGIN, half + MARGIN / 10, half - 0.01, half + 0.01,
                  half + 5.0, -half - 5.0, half + 12.0]
        for y in spots:
            add("collar", collar, frame, [0, y, 0], ident)
        for y in [rng.uniform(-150, 150) for _ in range(3)]:
            add("collar-off", collar, frame, [rng.choice([6.0, -8.0]), y, 0], ident)
        for y in [rng.uniform(-150, 150) for _ in range(3)]:
            add("collar-turned", collar, frame, [0, y, 0], rot(0, 37, 0))
        for y in [rng.uniform(-150, 150) for _ in range(3)] + [half + 3.0]:
            add("collar-leaning", collar, frame, [0, y, 0], rot(20, 0, 0))
        pin = {"shape": "cylinder", "dims": {"radius": 2.0, "height": 40.0}}
        for y in [rng.uniform(-150, 150) for _ in range(4)] + [length / 2, length / 2 - 1.0]:
            add("cross-pin", pin, frame, [0, y, 0], rot(90, 0, 0))
        for y in [rng.uniform(-150, 150) for _ in range(4)]:
            add("lean-pin", pin, frame, [4.0, y, 0], rot(0, 0, 35))
        # Thin pins across the shaft at different distances from its axis, their
        # boxes inside |x| <= radius: a round section is not a slab, and each shares
        # a different chord of it.
        thin = {"shape": "cylinder", "dims": {"radius": 0.5, "height": 40.0}}
        for x in (0.0, 3.0, 6.0, 8.5, -8.5):
            add("chord-pin", thin, frame, [x, rng.uniform(-150, 150), 0], rot(90, 0, 0))

    # --- girders: an extrusion is along its own z -----------------------------
    for g, (turn, mirrored) in enumerate([((0, 0, 0), False), ((0, 90, 0), False), ((0, 0, 0), True),
                                          ((rng.uniform(0, 60), 0, rng.uniform(0, 60)), False)]):
        frame = (rot(*turn), [2000.0, 0.0, 3000.0 * g])
        depth = 600.0
        girder = {"shape": "extrusion", "dims": {"depth": depth}, "outline": GIRDER}
        if mirrored:
            girder["mirrored"] = True
        add("girder", girder, frame, [0, 0, 0], ident)
        sx = -1.0 if mirrored else 1.0
        cleat = {"shape": "box", "dims": {"width": 20.0, "height": 20.0, "depth": 8.0}}
        end = depth / 2 - 4.0
        for z in [rng.uniform(-250, 250) for _ in range(6)] + [end, -end, end - MARGIN, end + MARGIN / 10,
                                                                 end + 0.01, end + 4.0, -end - 6.0]:
            add("cleat", cleat, frame, [sx * 3.0, 3.0, z], ident)
        pin = {"shape": "cylinder", "dims": {"radius": 1.5, "height": 30.0}}
        for z in [rng.uniform(-250, 250) for _ in range(4)] + [depth / 2, depth / 2 - 1.0]:
            add("girder-pin", pin, frame, [sx * 20.0, 3.0, z], ident)
        for z in [rng.uniform(-250, 250) for _ in range(4)]:
            add("girder-lean", pin, frame, [sx * 20.0, 3.0, z], rot(35, 0, 0))

    # --- a cone is NOT a prism --------------------------------------------------
    frame = (ident, [-3000.0, 0.0, 0.0])
    add("cone", {"shape": "cone", "dims": {"radius": 20.0, "radius_top": 5.0, "height": 300.0}}, frame, [0, 0, 0], ident)
    ring = {"shape": "box", "dims": {"width": 50.0, "height": 6.0, "depth": 50.0}}
    for y in [rng.uniform(-120, 120) for _ in range(6)]:
        add("cone-ring", ring, frame, [0, y, 0], ident)

    # --- a short extrusion with a longer bar through it -----------------------
    frame = (ident, [-6000.0, 0.0, 0.0])
    add("plate-ext", {"shape": "extrusion", "dims": {"depth": 10.0}, "outline": GIRDER}, frame, [0, 0, 0], ident)
    for x in (10.0, 20.0, 30.0):
        add("bar", {"shape": "box", "dims": {"width": 4.0, "height": 4.0, "depth": 40.0}}, frame, [x, 3.0, 0], ident)

    return {"solids": solids, "operations": [], "format": ""}


def run(sidecar, request, cached):
    sidecar._INTERFERENCE_CACHE = cached
    try:
        reply = sidecar._build(request)
    finally:
        sidecar._INTERFERENCE_CACHE = True
    if not reply.get("ok") or reply.get("skipped"):
        raise SystemExit(json.dumps({"error": "the fixture did not build: %s %s" % (reply.get("error"), reply.get("skipped"))}))
    return {"found": reply["interferences"], "truncated": reply["interferences_truncated"],
            "pairs": reply["interference_pairs"], "booleans": reply["interference_booleans"],
            "reused": reply["interference_reused"], "summarized": reply.get("interferences_summarized")}


def main():
    sidecar = load(sys.argv[1])
    runs = []
    for seed in sys.argv[2:]:
        request = fixture(int(seed))
        runs.append({"seed": int(seed), "parts": len(request["solids"]),
                     "uncached": run(sidecar, request, False), "cached": run(sidecar, request, True)})
    json.dump({"runs": runs}, sys.stdout)


if __name__ == "__main__":
    main()
