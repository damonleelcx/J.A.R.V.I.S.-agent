"""Placed copies as build123d makes them, against the sidecar's own, for
placed_copies_kernel_test.go.

Usage: placed_copies.py <sidecar.py>

Builds one fixture through _build twice in each format ("", "mesh" with part
properties, "step"): with _PLACE_WITHOUT_COPYING and _ASSEMBLY_VOLUME_PER_DEFINITION
off — `location * shape`, a Plane per placement and Compound.volume, as build123d
does it — and on. Every kept solid is captured on its way into the interference
check and compared object by object. Then counts, at one and at eight copies of
each placement, what each run asked build123d for.

Prints one JSON object: "problems" lists every difference found, as sentences.
"""

import importlib.util
import json
import math
import random
import re
import sys


def load(path):
    spec = importlib.util.spec_from_file_location("sidecar", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def rotation(ax, ay, az):
    cx, sx, cy, sy, cz, sz = math.cos(ax), math.sin(ax), math.cos(ay), math.sin(ay), math.cos(az), math.sin(az)
    rx = [[1, 0, 0], [0, cx, -sx], [0, sx, cx]]
    ry = [[cy, 0, sy], [0, 1, 0], [-sy, 0, cy]]
    rz = [[cz, -sz, 0], [sz, cz, 0], [0, 0, 1]]
    mul = lambda a, b: [[sum(a[i][k] * b[k][j] for k in range(3)) for j in range(3)] for i in range(3)]
    r = mul(rz, mul(ry, rx))
    return [r[i][j] for i in range(3) for j in range(3)]


ELL = {"start": [0.0, 0.0, 0.0], "edges": [
    {"to": [20.0, 0.0, 0.0]}, {"to": [20.0, 5.0, 0.0]}, {"to": [5.0, 5.0, 0.0]},
    {"to": [5.0, 15.0, 0.0]}, {"to": [0.0, 15.0, 0.0]}, {"to": [0.0, 0.0, 0.0]}]}
RING = {"start": [2.0, -3.0, 0.0], "edges": [{"to": [6.0, -3.0, 0.0]}, {"to": [6.0, 3.0, 0.0]},
                                              {"to": [2.0, 3.0, 0.0]}, {"to": [2.0, -3.0, 0.0]}]}


def fixture(copies):
    """Every shape kind that is placed as a copy, at matrices that include exact
    quarter turns, -0.0 beside 0, and seeded random turns; `copies` placements of
    each, at different positions. A plate cut by a drill is not a copy."""
    rng = random.Random(1509)
    kinds = [
        ("box", {"shape": "box", "dims": {"width": 12.0, "height": 5.0, "depth": 30.0}}),
        ("pin", {"shape": "cylinder", "dims": {"radius": 2.0, "height": 18.0}}),
        ("cone", {"shape": "cone", "dims": {"radius": 3.0, "radius_top": 1.0, "height": 9.0}}),
        ("ball", {"shape": "sphere", "dims": {"radius": 4.0}}),
        ("ell", {"shape": "extrusion", "dims": {"depth": 5.0}, "outline": ELL}),
        ("ell-m", {"shape": "extrusion", "dims": {"depth": 5.0}, "outline": ELL, "mirrored": True}),
        ("ring", {"shape": "revolve", "dims": {}, "outline": RING}),
    ]
    matrices = [[1, 0, 0, 0, 1, 0, 0, 0, 1], [1.0, -0.0, 0.0, 0.0, 1.0, -0.0, -0.0, 0.0, 1.0],
                [0, -1, 0, 1, 0, 0, 0, 0, 1], [1, 0, 0, 0, 0, -1, 0, 1, 0]]
    matrices += [rotation(rng.uniform(-3.2, 3.2), rng.uniform(-3.2, 3.2), rng.uniform(-3.2, 3.2)) for _ in range(4)]
    solids = []
    for name, spec in kinds:
        for m, matrix in enumerate(matrices):
            for c in range(copies):
                solid = dict(spec, id="%s-%d-%d" % (name, m, c), label=name.title(), matrix=matrix,
                             position=[rng.uniform(-900, 900), rng.choice([0.0, 1911.350279284634, -0.0]),
                                       rng.uniform(-1, 1) * 10 ** rng.randint(-2, 3)])
                solids.append(solid)
    plate = {"width": 60.0, "height": 8.0, "depth": 60.0}
    solids.append({"id": "plate-cut", "label": "Plate", "shape": "box", "dims": plate,
                   "matrix": matrices[2], "position": [2000.0, 0.0, 0.0]})
    solids.append({"id": "drill", "label": "Drill", "shape": "cylinder", "dims": {"radius": 3.0, "height": 20.0},
                   "matrix": matrices[2], "position": [2000.0, 0.0, 0.0]})
    solids.append({"id": "plate-whole", "label": "Plate", "shape": "box", "dims": plate,
                   "matrix": matrices[2], "position": [2100.0, 0.0, 0.0]})
    return {"solids": solids, "operations": [{"id": "hole", "op": "cut", "of": "plate-cut", "with": ["drill"]}]}


def transformation(location_or_shape):
    loc = location_or_shape.wrapped
    loc = loc.Location() if hasattr(loc, "Location") and not hasattr(loc, "Transformation") else loc
    t = loc.Transformation()
    return [t.Value(r, c) for r in (1, 2, 3) for c in (1, 2, 3, 4)]


def run(sidecar, fast, fmt, request):
    sidecar._PLACE_WITHOUT_COPYING = fast
    sidecar._ASSEMBLY_VOLUME_PER_DEFINITION = fast
    # Added 2026-09-15 (last hot spots): off, a copy's attributes come from
    # copy.deepcopy and its placement from Location.__init__, which is build123d's
    # own path and the reference every comparison below is against.
    sidecar._PLACE_WITHOUT_DEEPCOPY = fast
    sidecar._LOCATION_WITHOUT_INIT = fast
    captured = {}
    real = sidecar._interferences

    def capture(solids, ids, labels, placed=None):
        captured.update(solids=list(solids), ids=list(ids), placed=list(placed or []))
        return real(solids, ids, labels, placed)

    sidecar._interferences = capture
    try:
        reply = sidecar._build(dict(request, format=fmt, properties=fmt == "mesh"))
    finally:
        sidecar._interferences = real
        sidecar._PLACE_WITHOUT_COPYING = True
        sidecar._ASSEMBLY_VOLUME_PER_DEFINITION = True
        sidecar._PLACE_WITHOUT_DEEPCOPY = True
        sidecar._LOCATION_WITHOUT_INIT = True
    if not reply.get("ok") or reply.get("skipped") or reply.get("features_failed"):
        raise SystemExit(json.dumps({"error": "the fixture did not build: %s %s %s" % (
            reply.get("error"), reply.get("skipped"), reply.get("features_failed"))}))
    return reply, captured


def attribute(value):
    """An attribute as something comparable: a location or rotation by its numbers."""
    if hasattr(value, "wrapped") and hasattr(value.wrapped, "Transformation"):
        return [type(value).__name__, transformation(value)]
    if isinstance(value, (int, float, str, bool, type(None))):
        return value
    if isinstance(value, (list, tuple)):
        return [type(value).__name__] + [attribute(v) for v in value]
    if isinstance(value, dict):
        return {k: attribute(v) for k, v in value.items()}
    return repr(value)


NAUO_ID = re.compile(r"NEXT_ASSEMBLY_USAGE_OCCURRENCE\('\d+'")
WRAP = re.compile(r"\r?\n[ ]+")


def step_body(reply):
    import base64

    text = base64.b64decode(reply["step"]).decode("latin-1")
    text = text[text.index("DATA;"):]
    return NAUO_ID.sub("NEXT_ASSEMBLY_USAGE_OCCURRENCE('#'", WRAP.sub("", text))


def compare(sidecar, problems):
    request = fixture(1)
    checked = 0
    for fmt in ("", "mesh", "step"):
        ref, ref_solids = run(sidecar, False, fmt, request)
        new, new_solids = run(sidecar, True, fmt, request)
        if ref["parts"] != new["parts"] or ref_solids["ids"] != new_solids["ids"]:
            problems.append("format %r: %s parts against %s" % (fmt, new["parts"], ref["parts"]))
            continue
        if ref["bounds"] != new["bounds"]:
            problems.append("format %r: bounds %s, build123d's %s" % (fmt, new["bounds"], ref["bounds"]))
        if abs(ref["volume"] - new["volume"]) > 1e-9 * abs(ref["volume"]):
            problems.append("format %r: volume %.17g, Compound.volume %.17g" % (fmt, new["volume"], ref["volume"]))
        if ref["interferences"] != new["interferences"] or ref["interferences_found"] != new["interferences_found"]:
            problems.append("format %r: the interferences differ" % fmt)
        for i, (a, b) in enumerate(zip(ref_solids["solids"], new_solids["solids"])):
            what = "format %r, %s" % (fmt, ref_solids["ids"][i])
            checked += 1
            if type(a) is not type(b):
                problems.append("%s: a %s, build123d made a %s" % (what, type(b).__name__, type(a).__name__))
            if sorted(a.__dict__) != sorted(b.__dict__):
                problems.append("%s: attributes %s, build123d's %s" % (what, sorted(b.__dict__), sorted(a.__dict__)))
            for key in a.__dict__:
                if key in ("_wrapped", "topo_parent"):
                    continue
                if attribute(a.__dict__[key]) != attribute(b.__dict__.get(key)):
                    problems.append("%s: attribute %s is %r, build123d's %r" % (
                        what, key, attribute(b.__dict__.get(key)), attribute(a.__dict__[key])))
            if a.wrapped.ShapeType() != b.wrapped.ShapeType() or a.wrapped.Orientation() != b.wrapped.Orientation():
                problems.append("%s: shape type or orientation differs" % what)
            if transformation(a) != transformation(b):
                problems.append("%s: placed at %s, build123d at %s" % (what, transformation(b), transformation(a)))
            p, q = ref_solids["placed"][i], new_solids["placed"][i]
            if (p is None) != (q is None):
                problems.append("%s: a copy in one run and not the other" % what)
            elif p is not None:
                if p[0] != q[0] or transformation(p[1]) != transformation(q[1]):
                    problems.append("%s: the placement recorded differs" % what)
                # ‼️ B-reps are compared WITHIN a run. Each run builds its own
                # definitions, so no B-rep of one run is a partner of the other's; the
                # first version of this fence compared across runs and reported every
                # copy (345 differences, all of them that).
                if not a.wrapped.IsPartner(p[2].wrapped):
                    problems.append("%s: build123d's copy does not share its definition's B-rep" % what)
                if not b.wrapped.IsPartner(q[2].wrapped):
                    problems.append("%s: does not share its definition's B-rep" % what)
                # A copy's own attributes, not its definition's: a copy that shared the
                # definition's dict, list or Location would change every copy through one.
                for key, value in q[2].__dict__.items():
                    if key in ("_wrapped", "topo_parent"):
                        continue
                    if (isinstance(value, (dict, list)) or hasattr(value, "wrapped")) \
                            and b.__dict__.get(key) is value:
                        problems.append("%s: attribute %s is its definition's own object, not a copy" % (what, key))
        if fmt == "mesh":
            for field in ("mesh", "mesh_definitions", "mesh_instances", "mesh_triangles", "mesh_deflection"):
                if ref.get(field) != new.get(field):
                    problems.append("mesh: %s differs" % field)
            if len(ref["part_properties"]) != len(new["part_properties"]):
                problems.append("mesh: part properties differ in number")
            for x, y in zip(ref["part_properties"], new["part_properties"]):
                if x != y:
                    problems.append("mesh: part properties of %s differ: %s against %s" % (x["id"], y, x))
                    break
        if fmt == "step" and step_body(ref) != step_body(new):
            problems.append("step: the file differs below its header (instance ids blanked, lines joined)")
    return checked


def counts(sidecar, fast, copies):
    """What one build of the fixture asked build123d for."""
    import build123d
    import build123d.topology.shape_core as core

    import copy as copy_module

    tally = {"brep_copies": 0, "planes": 0, "volume_integrals": 0, "occurrences": 0,
             # Added 2026-09-15 (last hot spots): the two costs _located and
             # _placement paid per occurrence — a deepcopy of every attribute, and a
             # Location built through __init__'s nine keyword arguments.
             "deepcopies": 0, "locations": 0, "fallbacks": 0}
    copier, plane_init, lut = core.BRepBuilderAPI_Copy, build123d.Plane.__init__, dict(core.Shape.shape_properties_LUT)
    deepcopy_real, location_init = copy_module.deepcopy, build123d.Location.__init__

    def counting_deepcopy(*a, **k):
        tally["deepcopies"] += 1
        return deepcopy_real(*a, **k)

    def counting_location(self, *a, **k):
        tally["locations"] += 1
        return location_init(self, *a, **k)

    def copy(*a, **k):
        tally["brep_copies"] += 1
        return copier(*a, **k)

    def init(self, *a, **k):
        tally["planes"] += 1
        return plane_init(self, *a, **k)

    def integrator(fn):
        def wrapped(*a, **k):
            tally["volume_integrals"] += 1
            return fn(*a, **k)
        return wrapped

    request = fixture(copies)
    tally["occurrences"] = len(request["solids"])
    core.BRepBuilderAPI_Copy = copy
    build123d.Plane.__init__ = init
    copy_module.deepcopy = counting_deepcopy
    build123d.Location.__init__ = counting_location
    for k, fn in lut.items():
        if fn is not None:
            core.Shape.shape_properties_LUT[k] = integrator(fn)
    sidecar._PLACE_WITHOUT_COPYING = fast
    sidecar._ASSEMBLY_VOLUME_PER_DEFINITION = fast
    sidecar._PLACE_WITHOUT_DEEPCOPY = fast
    sidecar._LOCATION_WITHOUT_INIT = fast
    # ‼️ Counted as a DIFFERENCE: the module counter is process-wide and the
    # comparison runs before this, so its absolute value says nothing.
    fallbacks_before = sidecar._located_fallbacks
    real = sidecar._interferences
    # Only the phases measured: the interference check measures its own volumes.
    sidecar._interferences = lambda solids, ids, labels, placed=None: (
        [], False, 0, {"pairs": 0, "booleans": 0, "reused": 0})
    try:
        reply = sidecar._build(dict(request, format=""))
    finally:
        core.BRepBuilderAPI_Copy = copier
        build123d.Plane.__init__ = plane_init
        copy_module.deepcopy = deepcopy_real
        build123d.Location.__init__ = location_init
        core.Shape.shape_properties_LUT.update(lut)
        sidecar._interferences = real
        sidecar._PLACE_WITHOUT_COPYING = True
        sidecar._ASSEMBLY_VOLUME_PER_DEFINITION = True
        sidecar._PLACE_WITHOUT_DEEPCOPY = True
        sidecar._LOCATION_WITHOUT_INIT = True
    tally["fallbacks"] = sidecar._located_fallbacks - fallbacks_before
    tally["parts"] = reply.get("parts")
    tally["shape_builds"] = reply.get("shape_builds")
    return tally


def main():
    sidecar = load(sys.argv[1])
    problems = []
    checked = compare(sidecar, problems)
    out = {"checked": checked, "problems": problems[:40], "problem_count": len(problems),
           "counts": {"reference": {"1": counts(sidecar, False, 1), "8": counts(sidecar, False, 8)},
                      "shipped": {"1": counts(sidecar, True, 1), "8": counts(sidecar, True, 8)}}}
    json.dump(out, sys.stdout)


if __name__ == "__main__":
    main()
