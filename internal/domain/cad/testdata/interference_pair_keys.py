"""Every pair's clash key through build123d and without it, and the whole check
both ways, for interference_pair_keys_kernel_test.go (repair bound and check
profile; sidecar.py, _pair_keys and _PAIR_KEY_DIRECT).

Usage: interference_pair_keys.py <sidecar.py>

For each fixture, built through _build with the check captured:
  - the key of EVERY pair of solids, not only the pairs whose boxes meet, from
    _pair_key (build123d's Locations, the reference) and from _pair_keys (the
    direct path), compared by repr, so -0.0 against 0.0 would count: with the
    rotation memo, slide on and off, and without the memo;
  - on the barrel, the memo again with every seventh solid sharing its neighbour's
    Location object and with every fifth placement made a chain of datums;
  - every moved box, with _MOVED_BOX_DIRECT off and on;
  - _interferences with _PAIR_KEY_DIRECT off and on: the list, the flag, the box
    tests and the counts, as the reply writes them.

Fixtures: interference_prisms.py's four seeds, interference_cache.py's rails and
turned crossbars, interference_list_limit.py's tied pins, 150 blocks whose 11,175
clashes are more than the list's bound, and a barrel of rivets through stringers
and skins turned round an axis.

And a count, not a time: how many build123d Locations the check constructs on the
barrel at 1 and 8 bays, both ways.
"""

import importlib.util
import json
import math
import os
import sys

# It loads the sidecar and three sibling fixtures by path; none of them should leave
# a __pycache__ in the tree.
sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))


def load(path, name="sidecar"):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def blocks():
    ident = [1, 0, 0, 0, 1, 0, 0, 0, 1]
    return {"solids": [{"id": "block-%03d" % n, "label": "Block", "shape": "box",
                        "dims": {"width": 10.0, "height": 10.0, "depth": 10.0}, "matrix": ident,
                        "position": [0.02 * n, 0.0, 0.0]} for n in range(150)],
            "operations": [], "format": ""}


def barrel(bays, sectors=8):
    """Stringers the barrel's length, a skin panel a bay a sector, and rivets through
    both, turned round x by sector, as the airframe barrel is, and a rivet at each
    stringer end, half over."""
    solids = []
    for s in range(sectors):
        a = 2 * math.pi * s / sectors
        c, si = math.cos(a), math.sin(a)
        turn = [1, 0, 0, 0, c, -si, 0, si, c]

        def at(x, y, z):
            return [x, c * y - si * z, si * y + c * z]

        length = 500.0 * bays
        solids.append({"id": "stringer-%d" % s, "label": "Stringer", "shape": "box",
                       "dims": {"width": length, "height": 20.0, "depth": 20.0}, "matrix": turn,
                       "position": at(length / 2, 300.0, 0.0)})
        for b in range(bays):
            solids.append({"id": "bay-%d/sector-%d/skin" % (b, s), "label": "Skin", "shape": "box",
                           "dims": {"width": 498.0, "height": 2.0, "depth": 146.0}, "matrix": turn,
                           "position": at(250.0 + 500.0 * b, 311.0, 0.0)})
            for r in range(24):
                solids.append({"id": "bay-%d/sector-%d/rivet-%d" % (b, s, r), "label": "Rivet",
                               "shape": "cylinder", "dims": {"radius": 2.4, "height": 14.0, "radius_top": 2.4},
                               "matrix": turn, "position": at(15.0 + 500.0 * b + 20.0 * r, 305.0, 5.0 - 10.0 * (r % 2))})
        solids.append({"id": "sector-%d/end-rivet" % s, "label": "Rivet", "shape": "cylinder",
                       "dims": {"radius": 2.4, "height": 14.0, "radius_top": 2.4}, "matrix": turn,
                       "position": at(length, 305.0, 0.0)})
    return {"solids": solids, "operations": [], "format": ""}


def captured(sidecar, request):
    got = {}
    real = sidecar._interferences

    def capture(solids, ids, labels, placed=None):
        got["args"] = (solids, ids, labels, placed)
        return real(solids, ids, labels, placed)

    sidecar._interferences = capture
    try:
        reply = sidecar._build(request)
    finally:
        sidecar._interferences = real
    if not reply.get("ok") or reply.get("skipped") or reply.get("features_failed"):
        raise SystemExit(json.dumps({"error": "a fixture did not build: %s %s %s" % (
            reply.get("error"), reply.get("skipped"), reply.get("features_failed"))}))
    return got["args"]


def check(sidecar, args, direct):
    sidecar._PAIR_KEY_DIRECT = sidecar._MOVED_BOX_DIRECT = direct
    try:
        listed, truncated, box_tests, stats = sidecar._interferences(*args)
    finally:
        sidecar._PAIR_KEY_DIRECT = sidecar._MOVED_BOX_DIRECT = True
    return {"interferences": listed, "truncated": truncated, "box_tests": box_tests, "stats": stats}


def shapes_of(sidecar, placed):
    shape_ids, slabs = {}, {}
    for p in placed:
        if p is not None and p[0] not in shape_ids:
            shape_ids[p[0]] = len(shape_ids)
            slabs[p[0]] = sidecar._slabs(p[0], p[2])
    return shape_ids, slabs


def keyed(sidecar, solids, placed, with_slabs, memo, out, counted, differ, marks=False):
    """Every pair's key against _pair_key; memo says whether _pair_keys gets the
    rotations _measures reads (the memo) or not."""
    shape_ids, slabs = shapes_of(sidecar, placed)
    slabs = slabs if with_slabs else {}
    rotations = [None] * len(solids)
    sidecar._measures(solids, placed, rotations)
    direct = sidecar._pair_keys(placed, shape_ids, slabs, rotations if memo else None)
    n = len(solids)
    for i in range(n):
        for j in range(i + 1, n):
            want, got = sidecar._pair_key(placed, shape_ids, i, j, slabs), direct(i, j)
            out[counted] = out.get(counted, 0) + 1
            if repr(want) != repr(got):
                out[differ] = out.get(differ, 0) + 1
                if not out.get("first"):
                    out["first"] = "%s and %s: %r against %r" % (i, j, want, got)
            if marks and want is not None:
                m = [want[2][4 * r + 3] for r in range(3)]
                out["slid"] += any(x == math.inf for x in m)
                out["carried"] += any(x == -math.inf for x in m)
    out.setdefault(differ, 0)
    return len(direct.memo)


def shared(sidecar, placed):
    """Every seventh placed solid put at its neighbour's Location OBJECT: one datum
    for two solids, which TopLoc_Location cancels to the exact identity."""
    out = list(placed)
    for k in range(0, len(out) - 1, 7):
        if out[k] is not None and out[k + 1] is not None:
            out[k + 1] = (out[k + 1][0], out[k][1], out[k + 1][2])
    return out


def chained(sidecar, placed):
    """Every fifth placed solid's Location made a chain of three datums: itself, a
    turn about z and the turn back."""
    from build123d import Location
    from OCP.gp import gp_Ax1, gp_Dir, gp_Pnt, gp_Trsf
    from OCP.TopLoc import TopLoc_Location

    there, back = gp_Trsf(), gp_Trsf()
    there.SetRotation(gp_Ax1(gp_Pnt(0, 0, 0), gp_Dir(0, 0, 1)), 0.3)
    back.SetRotation(gp_Ax1(gp_Pnt(0, 0, 0), gp_Dir(0, 0, 1)), -0.3)
    out = list(placed)
    for k in range(0, len(out), 5):
        if out[k] is not None:
            loc = out[k][1].wrapped * TopLoc_Location(there) * TopLoc_Location(back)
            out[k] = (out[k][0], Location(loc), out[k][2])
    return out


def compare(sidecar, name, request, variants=False):
    args = captured(sidecar, request)
    solids, _, _, placed = args
    out = {"name": name, "parts": len(solids), "first": "", "slid": 0, "carried": 0}
    out["memoized"] = keyed(sidecar, solids, placed, True, True, out, "compared", "differ", marks=True)
    keyed(sidecar, solids, placed, False, True, out, "unslid_compared", "unslid_differ")
    keyed(sidecar, solids, placed, True, False, out, "plain_compared", "plain_differ")
    out["variants"] = []
    if variants:
        for label, make in (("shared locations", shared), ("chained locations", chained)):
            v = {"name": label}
            v["memoized"] = keyed(sidecar, solids, make(sidecar, placed), True, True, v, "compared", "differ")
            out["variants"].append(v)
    shape_ids, slabs = shapes_of(sidecar, placed)
    # Every placed solid's moved box, both ways (_MOVED_BOX_DIRECT), by repr.
    boxes = {}
    for direct in (False, True):
        sidecar._MOVED_BOX_DIRECT = direct
        try:
            boxes[direct] = sidecar._measures(solids, placed)
        finally:
            sidecar._MOVED_BOX_DIRECT = True
    out["boxes_compared"] = sum(1 for p in placed if p is not None)
    out["boxes_differ"] = sum(1 for a, b in zip(boxes[False][0], boxes[True][0]) if repr(a) != repr(b))
    out["volumes_differ"] = sum(1 for a, b in zip(boxes[False][1], boxes[True][1]) if repr(a) != repr(b))
    out["reference"] = check(sidecar, args, False)
    out["direct"] = check(sidecar, args, True)
    return out


def locations_built(sidecar, request):
    import build123d.geometry as geometry

    args = captured(sidecar, request)
    count = [0]
    original = geometry.Location.__init__

    def counting(self, *a, **k):
        count[0] += 1
        original(self, *a, **k)

    out = {"parts": len(args[0])}
    for direct in (True, False):
        geometry.Location.__init__ = counting
        count[0] = 0
        try:
            result = check(sidecar, args, direct)
        finally:
            geometry.Location.__init__ = original
        out["direct_locations" if direct else "reference_locations"] = count[0]
        out["pairs"] = result["stats"]["pairs"]
        out["found"] = result["stats"]["found"]
        out["booleans"] = result["stats"]["booleans"]
    return out


def main():
    sidecar = load(sys.argv[1])
    prisms = load(os.path.join(HERE, "interference_prisms.py"), "prisms")
    cache = load(os.path.join(HERE, "interference_cache.py"), "cache")
    limit = load(os.path.join(HERE, "interference_list_limit.py"), "limit")
    fixtures = [("prisms seed %d" % s, prisms.fixture(s)) for s in (1, 2, 3, 4)]
    fixtures += [("rails and crossbars", cache.fixture()), ("tied pins", limit.fixture()),
                 ("150 blocks", blocks())]
    runs = [compare(sidecar, name, request) for name, request in fixtures]
    runs.append(compare(sidecar, "barrel, 2 bays", barrel(2), variants=True))
    json.dump({"fixtures": runs,
               "scaling": [dict(locations_built(sidecar, barrel(b)), bays=b) for b in (1, 8)]}, sys.stdout)


if __name__ == "__main__":
    main()
