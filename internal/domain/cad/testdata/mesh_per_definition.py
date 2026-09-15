"""The sidecar's mesh per definition against the per-solid mesh it replaced, for
mesh_per_definition_kernel_test.go.

Usage: mesh_per_definition.py <sidecar.py>

Builds one fixture twice through _build: as shipped, and with
_MESH_PER_DEFINITION off. Every instance is moved by its matrix and compared with
the same part's per-solid mesh. Prints one JSON object.

# What "the same mesh" means here

The same nodes in the same order, the same number of triangles, and the same
closed surface: equal area and equal SIGNED volume. Not the same index list.
OCCT triangulates a shape placed and unplaced to identical nodes (measured to
1e-14 mm) but may join them with different diagonals wherever two are equally
good — a flat face split corner-to-corner one way or the other — which is the
same surface. Signed volume is what catches the failure that matters: a copy
wound inside out has the negative of the volume.
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


# An L, drawn off its own origin: its mirror is a different set of points, so a
# lost reflection cannot hide the way a mirrored box would.
ELL = {"start": [0.0, 0.0, 0.0], "edges": [
    {"to": [20.0, 0.0, 0.0]}, {"to": [20.0, 5.0, 0.0]}, {"to": [5.0, 5.0, 0.0]},
    {"to": [5.0, 15.0, 0.0]}, {"to": [0.0, 15.0, 0.0]}, {"to": [0.0, 0.0, 0.0]}]}


def fixture():
    """Row-major rotation matrices, as geometry.Solid sends them."""
    box = {"width": 12.0, "height": 5.0, "depth": 30.0}
    pin = {"radius": 2.0, "height": 18.0}
    solids = []
    for i, deg in enumerate((0, 30, 90, 145)):
        solids.append({"id": "block-%d" % (i + 1), "label": "Block", "shape": "box", "dims": box,
                       "matrix": turn_z(deg), "position": [40.0 * i, 10.0, -5.0]})
    for i in range(3):
        solids.append({"id": "pin-%d" % (i + 1), "label": "Pin", "shape": "cylinder", "dims": pin,
                       "matrix": turn_x(90), "position": [0.0, 60.0 + 15 * i, 7.5 * i]})
    for i, mirrored in enumerate((False, False, True, True)):
        solid = {"id": "ell-%d" % (i + 1), "label": "Ell", "shape": "extrusion", "dims": {"depth": 5.0},
                 "outline": ELL, "matrix": turn_z(20), "position": [-60.0, 30.0 * i, 0.0]}
        if mirrored:
            solid["mirrored"] = True
        solids.append(solid)
    # Two plates of one shape; one is cut, so it is no longer a copy of it.
    plate = {"width": 60.0, "height": 8.0, "depth": 60.0}
    solids.append({"id": "plate-cut", "label": "Plate", "shape": "box", "dims": plate,
                   "matrix": turn_z(0), "position": [0.0, -80.0, 0.0]})
    solids.append({"id": "plate-whole", "label": "Plate", "shape": "box", "dims": plate,
                   "matrix": turn_z(0), "position": [100.0, -80.0, 0.0]})
    solids.append({"id": "drill", "label": "Drill", "shape": "cylinder", "dims": {"radius": 6.0, "height": 40.0},
                   "matrix": turn_z(0), "position": [0.0, -80.0, 0.0]})
    operations = [{"id": "hole", "op": "cut", "of": "plate-cut", "with": ["drill"]}]
    return {"solids": solids, "operations": operations, "format": "mesh"}


def area_and_signed_volume(vertices, triangles):
    area = volume = 0.0
    for i in range(0, len(triangles), 3):
        a, b, c = (vertices[3 * triangles[i + k]:3 * triangles[i + k] + 3] for k in range(3))
        ab = [b[j] - a[j] for j in range(3)]
        ac = [c[j] - a[j] for j in range(3)]
        cross = [ab[1] * ac[2] - ab[2] * ac[1], ab[2] * ac[0] - ab[0] * ac[2], ab[0] * ac[1] - ab[1] * ac[0]]
        area += 0.5 * math.sqrt(sum(x * x for x in cross))
        volume += (a[0] * (b[1] * c[2] - b[2] * c[1]) - a[1] * (b[0] * c[2] - b[2] * c[0])
                   + a[2] * (b[0] * c[1] - b[1] * c[0])) / 6.0
    return area, volume


def differs(got, want, tolerance=1e-9):
    return abs(got - want) > tolerance * max(1.0, abs(want))


def main():
    sidecar = load(sys.argv[1])
    request = fixture()

    sidecar._MESH_PER_DEFINITION = False
    before = sidecar._build(request)
    sidecar._MESH_PER_DEFINITION = True
    after = sidecar._build(request)

    mismatches = []
    for reply, name in ((before, "per solid"), (after, "per definition")):
        if not reply.get("ok") or reply.get("mesh_error") or reply.get("skipped"):
            mismatches.append("%s build failed: %s" % (
                name, reply.get("error") or reply.get("mesh_error") or reply.get("skipped")))
    if mismatches:
        json.dump({"parts": 0, "instanced": 0, "separate": 0, "definitions": 0, "compared": 0,
                   "mismatches": mismatches}, sys.stdout)
        return

    old = {m["id"]: m for m in before["mesh"]}
    new = {m["id"]: m for m in after["mesh"]}
    definitions = after.get("mesh_definitions") or []
    for inst in after.get("mesh_instances") or []:
        d, m = definitions[inst["definition"]], inst["matrix"]
        src, moved = d["vertices"], []
        for i in range(0, len(src), 3):
            x, y, z = src[i], src[i + 1], src[i + 2]
            moved.extend((m[0] * x + m[4] * y + m[8] * z + m[12],
                          m[1] * x + m[5] * y + m[9] * z + m[13],
                          m[2] * x + m[6] * y + m[10] * z + m[14]))
        new[inst["id"]] = {"vertices": moved, "triangles": d["triangles"]}

    compared = 0
    for part_id, want in old.items():
        got = new.get(part_id)
        if got is None:
            mismatches.append("%s: had a mesh per solid and has none per definition" % part_id)
            continue
        compared += 1
        if len(got["vertices"]) != len(want["vertices"]) or len(got["triangles"]) != len(want["triangles"]):
            mismatches.append("%s: %d nodes and %d triangles, per solid %d and %d" % (
                part_id, len(got["vertices"]) // 3, len(got["triangles"]) // 3,
                len(want["vertices"]) // 3, len(want["triangles"]) // 3))
            continue
        worst = max((abs(a - b) for a, b in zip(got["vertices"], want["vertices"])), default=0.0)
        if worst > 1e-6:
            mismatches.append("%s: nodes differ by up to %g mm" % (part_id, worst))
            continue
        got_area, got_volume = area_and_signed_volume(got["vertices"], got["triangles"])
        want_area, want_volume = area_and_signed_volume(want["vertices"], want["triangles"])
        if differs(got_area, want_area) or differs(got_volume, want_volume):
            mismatches.append("%s: area %.9g and signed volume %.9g, per solid %.9g and %.9g" % (
                part_id, got_area, got_volume, want_area, want_volume))
    for part_id in new:
        if part_id not in old:
            mismatches.append("%s: has a mesh per definition and had none per solid" % part_id)

    instanced = len(after.get("mesh_instances") or [])
    json.dump({"parts": len(old), "instanced": instanced, "separate": len(after["mesh"]),
               "definitions": len(definitions), "compared": compared,
               "mismatches": mismatches}, sys.stdout)


if __name__ == "__main__":
    main()
