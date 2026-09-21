"""Which end of a truncated cone is the top, for truncated_cone_kernel_test.go.

Usage: truncated_cone.py <sidecar.py>

Builds one frustum (radius 20 at the bottom, radius_top 5 at the top, height 10)
four times through _build: upright; turned a half turn about X; laid down a
quarter turn about Z; and mirrored. For each it reports the centre of volume and
the area of the cross section cut at four stations ALONG THE PART'S OWN AXIS, at
+-3 and +-4.9 mm from its centre, measured in world coordinates.

Why the stations are in the part's own frame: the answer is then the same four
numbers for every copy however it is turned, so one expectation fences all four
and a copy that came out end-for-end cannot borrow another copy's turn to hide
in. A plain cylinder is symmetric along its axis and can satisfy any of this
with either end up, which is why the fixture is a frustum.

Prints one JSON object: {"parts": [{"id", "volume", "centroid", "axis",
"stations": [{"t", "area"}]}]}.
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


def rotation(ax, ay, az):
    """The same row-major matrix the Go side computes from a part's rotation."""
    ax, ay, az = math.radians(ax), math.radians(ay), math.radians(az)
    cx, sx, cy, sy, cz, sz = math.cos(ax), math.sin(ax), math.cos(ay), math.sin(ay), math.cos(az), math.sin(az)
    rx = [[1, 0, 0], [0, cx, -sx], [0, sx, cx]]
    ry = [[cy, 0, sy], [0, 1, 0], [-sy, 0, cy]]
    rz = [[cz, -sz, 0], [sz, cz, 0], [0, 0, 1]]
    mul = lambda a, b: [[sum(a[i][k] * b[k][j] for k in range(3)) for j in range(3)] for i in range(3)]
    r = mul(rz, mul(ry, rx))
    return [r[i][j] for i in range(3) for j in range(3)]


def apply(m, v):
    return [sum(m[i * 3 + k] * v[k] for k in range(3)) for i in range(3)]


RADIUS, TOP, HEIGHT = 20.0, 5.0, 10.0
STATIONS = (4.9, 3.0, -3.0, -4.9)

# Each copy: its turn, and where it stands. The positions are far enough apart
# that the four never touch, so nothing here depends on the clash check.
COPIES = [
    ("upright", (0.0, 0.0, 0.0), [0.0, 0.0, 0.0], False),
    ("turned", (180.0, 0.0, 0.0), [100.0, 0.0, 0.0], False),
    ("laid", (0.0, 0.0, 90.0), [200.0, 0.0, 0.0], False),
    ("mirrored", (0.0, 0.0, 0.0), [300.0, 0.0, 0.0], True),
]


def request():
    solids = []
    for name, turn, position, mirrored in COPIES:
        solid = {"id": name, "label": name.title(), "shape": "cylinder",
                 "dims": {"radius": RADIUS, "radius_top": TOP, "height": HEIGHT},
                 "matrix": rotation(*turn), "position": position}
        if mirrored:
            solid["mirrored"] = True
        solids.append(solid)
    return {"solids": solids, "operations": [], "format": "mesh", "properties": True}


def run(sidecar):
    from build123d import Plane, Vector

    captured = {}
    real = sidecar._interferences

    def capture(solids, ids, labels, placed=None):
        captured.update(solids=list(solids), ids=list(ids))
        return real(solids, ids, labels, placed)

    sidecar._interferences = capture
    try:
        reply = sidecar._build(request())
    finally:
        sidecar._interferences = real
    if not reply.get("ok") or reply.get("skipped") or reply.get("features_failed"):
        raise SystemExit(json.dumps({"error": "the fixture did not build: %s %s %s" % (
            reply.get("error"), reply.get("skipped"), reply.get("features_failed"))}))

    properties = {p["id"]: p for p in (reply.get("part_properties") or [])}
    solids = dict(zip(captured["ids"], captured["solids"]))
    out = []
    for name, turn, position, _ in COPIES:
        m = rotation(*turn)
        axis = apply(m, [0.0, 1.0, 0.0])
        solid = solids[name]
        stations = []
        for t in STATIONS:
            centre = apply(m, [0.0, t, 0.0])
            origin = [position[i] + centre[i] for i in range(3)]
            face = Plane(origin=Vector(*origin), z_dir=Vector(*axis)).intersect(solid)
            area = 0.0 if face is None else sum(f.area for f in face.faces())
            stations.append({"t": t, "area": area})
        p = properties.get(name) or {}
        out.append({"id": name, "volume": p.get("volume"), "centroid": p.get("centroid"),
                    "axis": axis, "position": position, "stations": stations})
    return {"parts": out}


def main(argv):
    sidecar = load(argv[1])
    print(json.dumps(run(sidecar)))


if __name__ == "__main__":
    main(sys.argv)
