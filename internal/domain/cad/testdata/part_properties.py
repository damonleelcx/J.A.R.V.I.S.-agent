"""Each part's volume and centre measured once per shape, against measuring every
solid, for mass_properties_kernel_test.go.

Usage: part_properties.py <sidecar.py>

Builds mesh_per_definition.py's fixture (turned blocks, turned pins, a mirrored L
extrusion and a plate cut by a drill) through _build twice with properties on:
with _PROPERTIES_PER_DEFINITION off (every solid measured) and on (as shipped).
Prints one JSON object.
"""

import importlib.util
import json
import math
import os
import sys


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def main():
    sidecar = load("sidecar", sys.argv[1])
    fixture = load("fixture", os.path.join(os.path.dirname(os.path.abspath(__file__)), "mesh_per_definition.py"))
    request = fixture.fixture()
    request["format"] = ""
    request["properties"] = True

    sidecar._PROPERTIES_PER_DEFINITION = False
    direct = sidecar._build(request)
    sidecar._PROPERTIES_PER_DEFINITION = True
    shared = sidecar._build(request)

    mismatches = []
    for reply, name in ((direct, "direct"), (shared, "per shape")):
        if not reply.get("ok") or reply.get("skipped") or reply.get("features_failed"):
            mismatches.append("%s build failed: %s %s" % (name, reply.get("error"), reply.get("skipped")))
    if mismatches:
        json.dump({"parts": 0, "compared": 0, "mismatches": mismatches}, sys.stdout)
        return

    want = {p["id"]: p for p in direct["part_properties"]}
    got = {p["id"]: p for p in shared["part_properties"]}
    compared, worst = 0, 0.0
    for part_id, w in want.items():
        g = got.get(part_id)
        if g is None:
            mismatches.append("%s: measured directly and missing per shape" % part_id)
            continue
        compared += 1
        if abs(g["volume"] - w["volume"]) > 1e-9 * max(1.0, abs(w["volume"])):
            mismatches.append("%s: volume %.12g per shape, %.12g directly" % (part_id, g["volume"], w["volume"]))
        if g["centroid"] is None or w["centroid"] is None:
            mismatches.append("%s: no centre (per shape %s, directly %s)" % (part_id, g["centroid"], w["centroid"]))
            continue
        d = math.dist(g["centroid"], w["centroid"])
        worst = max(worst, d)
        if d > 1e-6:
            mismatches.append("%s: centre %s per shape, %s directly (%.3g mm apart)" % (
                part_id, [round(v, 6) for v in g["centroid"]], [round(v, 6) for v in w["centroid"]], d))
        if g["bounds"] != w["bounds"]:
            mismatches.append("%s: box %s per shape, %s directly" % (part_id, g["bounds"], w["bounds"]))
    extra = sorted(set(got) - set(want))
    if extra:
        mismatches.append("measured per shape and not directly: %s" % ", ".join(extra))
    today = as_today(sidecar, fixture)
    json.dump({"parts": len(want), "compared": compared, "worst_mm": worst, "mismatches": mismatches,
               "today": today}, sys.stdout)


def moved_point_as_it_was(point, location):
    """_moved_point as it was before 2026-09-17 (kernel last walls): a generator over
    rows. The shipped one must give these bits, not only a point within 1e-6."""
    if point is None:
        return None
    t = location.wrapped.Transformation()
    x, y, z = point
    return tuple(t.Value(r, 1) * x + t.Value(r, 2) * y + t.Value(r, 3) * z + t.Value(r, 4) for r in (1, 2, 3))


def as_today(sidecar, fixture):
    """The shipped part properties against the path they replaced, to the BIT.

    # Added 2026-09-17 (kernel last walls)

    The comparison above is against measuring every solid from scratch, and centres
    can only agree with that to ~1e-9 mm (a moved centre is not a measured one). This
    one is against the per-definition path as it stood before this change — every
    box read through build123d's bounding_box (_PROPERTIES_BOX_DIRECT off) and every
    centre moved by the old _moved_point — and requires the whole part_properties
    list to be EQUAL: every id, volume, centre and box, bit for bit. On this file's
    fixture, and on placed_copies.py's (every placed shape kind at quarter turns, -0.0
    beside 0 and random turns, and a part a feature changed) at one and at eight
    copies of each placement.

    It also counts, in the shipped run, the boxes read through build123d's
    _box_of and the BRepTools.Clean calls: a direct read that was quietly not
    taken would still be equal, so the counts are what shows it is the path taken.
    Only a part a feature changed (no placement) should go through _box_of, and
    Clean should run once per definition.
    """
    placed_copies = load("placed_copies", os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                                        "placed_copies.py"))
    requests = [("mesh_per_definition", fixture.fixture())]
    requests += [("placed_copies x%d" % n, placed_copies.fixture(n)) for n in (1, 8)]
    shipped_direct, shipped_moved = sidecar._PROPERTIES_BOX_DIRECT, sidecar._moved_point
    out = {"runs": [], "differences": []}
    for name, request in requests:
        request = dict(request, format="", properties=True, skip_interferences=True)
        sidecar._PROPERTIES_BOX_DIRECT = False
        sidecar._moved_point = moved_point_as_it_was
        try:
            before = sidecar._build(request)
        finally:
            sidecar._PROPERTIES_BOX_DIRECT = shipped_direct
            sidecar._moved_point = shipped_moved
        tally = {"box_of": 0, "clean": 0}
        box_of, clean = sidecar._box_of, sidecar.BRepTools

        def counting_box_of(solid):
            tally["box_of"] += 1
            return box_of(solid)

        class CountingClean:
            @staticmethod
            def Clean_s(*a, **k):
                tally["clean"] += 1
                return clean.Clean_s(*a, **k)

        sidecar._box_of, sidecar.BRepTools = counting_box_of, CountingClean
        try:
            after = sidecar._build(request)
        finally:
            sidecar._box_of, sidecar.BRepTools = box_of, clean
        for reply, which in ((before, "before"), (after, "shipped")):
            if not reply.get("ok") or reply.get("skipped") or reply.get("features_failed"):
                out["differences"].append("%s %s build failed: %s %s" % (
                    name, which, reply.get("error"), reply.get("skipped")))
        run = {"name": name, "parts": after.get("parts", 0), "box_of": tally["box_of"],
               "clean": tally["clean"], "shape_builds": after.get("shape_builds", 0),
               "changed_by_a_feature": len(request.get("operations") or []),
               "equal": before.get("part_properties") == after.get("part_properties")}
        out["runs"].append(run)
        a, b = before.get("part_properties") or [], after.get("part_properties") or []
        if len(a) != len(b):
            out["differences"].append("%s: %d parts measured before, %d shipped" % (name, len(a), len(b)))
        for x, y in zip(a, b):
            if x != y:
                out["differences"].append("%s: %s is %s shipped, %s before" % (name, x["id"], y, x))
                if len(out["differences"]) > 20:
                    break
    return out


if __name__ == "__main__":
    main()
