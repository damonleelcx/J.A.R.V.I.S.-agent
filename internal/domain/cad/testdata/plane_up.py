"""Which way a plane faces in the kernel, for plane_up_kernel_test.go.

Usage: plane_up.py <sidecar.py>

A plane is ONE-SIDED and FACES UP: normal +Y, wound to agree. The convention and
the reasons for it live on `func plane` in internal/domain/geometry/mesh.go.

Builds one 10 x 4 plane through _build four times — upright; turned a half turn
about X; laid down a quarter turn about Z; and mirrored — and reports two
independent readings of which way it faces:

  - the MESH the stage is drawn from. Reported in the DEFINITION's own frame,
    which is the frame the convention is written in and is the same for all four
    copies, so no matrix convention has to be agreed with the Go side to read it:
    every vertex normal, and every triangle's own (B-A)x(C-A).
  - the B-rep FACE, measured in world coordinates on the placed solid. This is
    what STEP carries and what a `thicken` grows from, and it is read per copy so
    a kernel that faced the right way only when nothing had been turned is caught.

Also reports the upright copy's bounds, because the frame that turns +Z to +Y is
not free to spin the local x with it the way a cylinder's may: a plane is a
rectangle and width must still be along X, depth along Z.

Prints one JSON object: {"definition": {"normals", "windings"}, "bounds",
"parts": [{"id", "up", "face_normal"}]}.
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


WIDTH, DEPTH = 10.0, 4.0

# Each copy: its turn, where it stands, and whether it is mirrored. A mirror in x
# leaves +Y alone but reverses handedness, so it is the copy that catches a kernel
# that re-winds a reflection without carrying its normals round with it.
COPIES = [
    ("upright", (0.0, 0.0, 0.0), [0.0, 0.0, 0.0], False),
    ("turned", (180.0, 0.0, 0.0), [100.0, 0.0, 0.0], False),
    ("laid", (0.0, 0.0, 90.0), [200.0, 0.0, 0.0], False),
    ("mirrored", (0.0, 0.0, 0.0), [300.0, 0.0, 0.0], True),
]


def request():
    solids = []
    for name, turn, position, mirrored in COPIES:
        solid = {"id": name, "label": name.title(), "shape": "plane",
                 "dims": {"width": WIDTH, "depth": DEPTH},
                 "matrix": rotation(*turn), "position": position}
        if mirrored:
            solid["mirrored"] = True
        solids.append(solid)
    return {"solids": solids, "operations": [], "format": "mesh", "properties": True}


def cross(a, b):
    return [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]]


def unit(v):
    length = math.sqrt(sum(c * c for c in v))
    return [0.0, 0.0, 0.0] if length == 0 else [c / length for c in v]


def run(sidecar):
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

    definitions = reply.get("mesh_definitions") or []
    used = {}
    for inst in reply.get("mesh_instances") or []:
        used.setdefault(inst["definition"], []).append(inst["id"])
    out_defs = []
    for i, d in enumerate(definitions):
        vertices, triangles, normals = d["vertices"], d["triangles"], d["normals"]
        if len(triangles) != 6:
            raise SystemExit(json.dumps({"error": "a plane meshed to %d triangle(s), want 2"
                                                  % (len(triangles) // 3)}))
        at = lambda k: vertices[k * 3:k * 3 + 3]
        windings = []
        for j in range(0, len(triangles), 3):
            a, b, c = at(triangles[j]), at(triangles[j + 1]), at(triangles[j + 2])
            windings.append(unit(cross([b[k] - a[k] for k in range(3)],
                                       [c[k] - a[k] for k in range(3)])))
        out_defs.append({"used_by": sorted(used.get(i, [])),
                         "normals": [normals[k:k + 3] for k in range(0, len(normals), 3)],
                         "windings": windings})

    solids = dict(zip(captured["ids"], captured["solids"]))
    parts = []
    for name, turn, position, _ in COPIES:
        faces = solids[name].faces()
        if len(faces) != 1:
            raise SystemExit(json.dumps({"error": "%s is %d face(s), want the 1 a plane is"
                                                  % (name, len(faces))}))
        n = faces[0].normal_at()
        parts.append({"id": name,
                      "up": apply(rotation(*turn), [0.0, 1.0, 0.0]),
                      "face_normal": [n.X, n.Y, n.Z]})

    upright = [p for p in reply["part_properties"] if p["id"] == "upright"][0]
    return {"definitions": out_defs, "bounds": upright["bounds"], "parts": parts}


def main(argv):
    sidecar = load(argv[1])
    print(json.dumps(run(sidecar)))


if __name__ == "__main__":
    main(sys.argv)
