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
    json.dump({"parts": len(want), "compared": compared, "worst_mm": worst, "mismatches": mismatches}, sys.stdout)


if __name__ == "__main__":
    main()
