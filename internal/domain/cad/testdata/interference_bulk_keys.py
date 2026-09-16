"""The interference check's narrow phase one ARRAY at a time, against the per-pair
loop, for interference_bulk_keys_kernel_test.go (interference approach; sidecar.py,
_bulk_keys, _bulk_interferences and _BULK_NARROW_PHASE).

Usage: interference_bulk_keys.py <sidecar.py> [--fast]

--fast runs three fixtures instead of eight. ‼️ It exists because the whole set takes
about eleven minutes, which is too slow to drill a dozen mutations against, and a
drill that is not run is a claim. The three are chosen to reach every branch the
array path has: tied pins (one group per pair of definitions, no marks), rails and
turned crossbars (pairs handed back to the loop), and a turned barrel of rivets
(slides, carried axes, and keys that differ from the loop's only in a zero's sign).

For each fixture, built through _build with the check captured:
  - _interferences with _BULK_NARROW_PHASE off and on: the list, the flag, the box
    tests and every count, as the reply writes them, compared as JSON text;
  - the key of EVERY pair of solids, not only the pairs whose boxes meet, from
    _pair_key (build123d's Locations, the reference) and from _bulk_keys, compared
    by == , which is what the check's cache compares them by, and by repr as well;
  - how many groups the array path took, how many distinct rows it built a key for,
    and how many pairs it handed back to the loop, so a fixture that never reached
    the array path cannot pass saying nothing;
  - the same answer with every ninth solid's placement dropped (a feature-changed
    solid, whose key is None and which pays a boolean of its own), with every
    seventh solid sharing its neighbour's Location OBJECT, with every fifth
    placement made a chain of datums, and with the boolean budget squeezed so the
    truncation path is compared too.

‼️ == and not repr. Two pairs whose translation rounds to -0.0 and to 0.0 are ONE
dict key — they compare equal and hash equal, so the shipped check already measures
them as one clash — but two reprs. The array path hands out one tuple per key, so
some pairs get an equal key carrying the other zero's sign. repr_differ counts them
and is reported; unequal is what must be zero, because equality is what the cache,
and therefore the answer, is built on.

Fixtures: interference_prisms.py's four seeds, interference_cache.py's rails and
turned crossbars, interference_list_limit.py's tied pins, 150 blocks whose clashes
are more than the list's bound, and a barrel of rivets through stringers and skins.
"""

import hashlib
import importlib.util
import json
import math
import os
import sys

# It loads the sidecar and four sibling fixtures by path; none of them should leave
# a __pycache__ in the tree.
sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))


def load(path, name="sidecar"):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def said(out, loop, array):
    """Both answers as a length and a digest, with an excerpt around the first
    difference — the answers themselves are up to 1.7 MB each and there are five
    per fixture, which is not something to hand a Go test."""
    out["loop_bytes"], out["array_bytes"] = len(loop), len(array)
    out["loop_sha1"] = hashlib.sha1(loop.encode()).hexdigest()[:12]
    out["array_sha1"] = hashlib.sha1(array.encode()).hexdigest()[:12]
    out["same_answer"] = loop == array
    if loop != array:
        k = next((i for i in range(min(len(loop), len(array))) if loop[i] != array[i]),
                 min(len(loop), len(array)))
        out["at"] = k
        out["loop_around"] = loop[max(0, k - 150):k + 150]
        out["array_around"] = array[max(0, k - 150):k + 150]


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


def answer(sidecar, args, bulk):
    """The whole check, with the array narrow phase off or on, as JSON text."""
    sidecar._BULK_NARROW_PHASE = bulk
    try:
        return json.dumps(sidecar._interferences(*args))
    finally:
        sidecar._BULK_NARROW_PHASE = True


def both(out, sidecar, args):
    """The loop's answer and the array's, on WARMED solids.

    ‼️ A pair's FIRST boolean in a process is not always its later ones. Measured
    2026-09-16 on prisms seed 1 with a feature-changed solid, where nearly every one
    of 37,675 pairs pays a boolean of its own: the loop's first run and its second
    differ in the last two bits of some volumes (5336.596433397901 against
    5336.596433397913), and every run after the first is identical, the array path's
    included. So this runs the loop once and throws that answer away before comparing
    — and records whether it differed, because it is a property of the shipped check
    worth knowing rather than something to hide behind a warm-up.
    """
    warm = answer(sidecar, args, False)
    loop = answer(sidecar, args, False)
    out["first_run_differs"] = warm != loop
    said(out, loop, answer(sidecar, args, True))


def shapes_of(sidecar, placed):
    shape_ids, slabs = {}, {}
    for p in placed:
        if p is not None and p[0] not in shape_ids:
            shape_ids[p[0]] = len(shape_ids)
            slabs[p[0]] = sidecar._slabs(p[0], p[2])
    return shape_ids, slabs


def every_pair(n):
    return [(i, j) for i in range(n) for j in range(i + 1, n)]


def keys_of(sidecar, solids, placed, out):
    """Every pair's key from the array path against _pair_key's.

    ‼️ Every pair, not only the candidates: _bulk_keys is asked about whatever list
    of pairs it is given, so the fence gives it all of them, exactly as the per-pair
    fence does. A key that changed in one bit could split one clash into two
    booleans or join two into one.
    """
    shape_ids, slabs = shapes_of(sidecar, placed)
    rotations, frames = [None] * len(solids), []
    sidecar._measures(solids, placed, rotations, frames)
    pairs = every_pair(len(solids))
    stats = {}
    ids, keys, _, _ = sidecar._bulk_keys(placed, shape_ids, slabs, rotations, pairs,
                                         frames, stats)
    ids = ids.tolist()
    unequal, repr_differ, slid, carried = 0, 0, 0, 0
    for k, (i, j) in enumerate(pairs):
        want, got = sidecar._pair_key(placed, shape_ids, i, j, slabs), keys[ids[k]]
        if want != got:
            unequal += 1
            if not out.get("first"):
                out["first"] = "%s and %s: %r against %r" % (i, j, want, got)
        elif repr(want) != repr(got):
            repr_differ += 1
        if want is not None:
            m = [want[2][4 * r + 3] for r in range(3)]
            slid += any(x == math.inf for x in m)
            carried += any(x == -math.inf for x in m)
    out.update({"compared": len(pairs), "unequal": unequal, "repr_differ": repr_differ,
                "slid": slid, "carried": carried, "groups": stats["groups"],
                "rows": stats["rows"], "distinct_keys": stats["keys"],
                "scalar_pairs": stats["scalar_pairs"]})


def unplaced(placed):
    """Every ninth solid measured as the solid it is, not as a placed copy — what a
    feature applied to one occurrence leaves behind. Its key is None, and the check
    pays a boolean for every pair it is in."""
    out = list(placed)
    for k in range(0, len(out), 9):
        out[k] = None
    return out


def shared(placed):
    """Every seventh placed solid at its neighbour's Location OBJECT: one datum for
    two solids, which TopLoc_Location cancels to the exact identity."""
    out = list(placed)
    for k in range(0, len(out) - 1, 7):
        if out[k] is not None and out[k + 1] is not None:
            out[k + 1] = (out[k + 1][0], out[k][1], out[k + 1][2])
    return out


def chained(placed):
    """Every fifth placed solid's Location made a chain of three datums."""
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


def compare(sidecar, name, request):
    solids, ids, labels, placed = captured(sidecar, request)
    out = {"name": name, "parts": len(solids), "first": "", "variants": []}
    both(out, sidecar, (solids, ids, labels, placed))
    keys_of(sidecar, solids, placed, out)
    for label, make in (("a feature-changed solid", unplaced),
                        ("shared locations", shared),
                        ("chained locations", chained)):
        v = {"name": label, "first": ""}
        both(v, sidecar, (solids, ids, labels, make(placed)))
        keys_of(sidecar, solids, make(placed), v)
        out["variants"].append(v)
    # ‼️ The boolean budget squeezed, so the truncation path is compared too: which
    # pair the search stops at is decided by the order the booleans are paid in, and
    # the array path works that order out from first occurrences rather than by
    # walking the pairs. The shipped budget is put back.
    was = sidecar._INTERFERENCE_PAIR_BUDGET
    squeezed = {"name": "boolean budget of 5", "first": ""}
    try:
        sidecar._INTERFERENCE_PAIR_BUDGET = 5
        both(squeezed, sidecar, (solids, ids, labels, placed))
        a = answer(sidecar, (solids, ids, labels, placed), False)
    finally:
        sidecar._INTERFERENCE_PAIR_BUDGET = was
    squeezed["truncated"] = json.loads(a)[1]
    squeezed["booleans"] = json.loads(a)[3]["booleans"]
    out["variants"].append(squeezed)
    return out


def main():
    sidecar = load(sys.argv[1])
    if sidecar._np is None:
        json.dump({"error": "numpy is not importable in this kernel's python, so the "
                            "array narrow phase cannot be measured"}, sys.stdout)
        return
    fast = "--fast" in sys.argv[2:]
    cache = load(os.path.join(HERE, "interference_cache.py"), "cache")
    limit = load(os.path.join(HERE, "interference_list_limit.py"), "limit")
    pairkeys = load(os.path.join(HERE, "interference_pair_keys.py"), "pairkeys")
    fixtures = []
    if not fast:
        prisms = load(os.path.join(HERE, "interference_prisms.py"), "prisms")
        fixtures += [("prisms seed %d" % s, prisms.fixture(s)) for s in (1, 2, 3, 4)]
    fixtures += [("rails and crossbars", cache.fixture()), ("tied pins", limit.fixture())]
    if not fast:
        fixtures.append(("150 blocks", pairkeys.blocks()))
    fixtures.append(("barrel, 2 bays", pairkeys.barrel(2)))
    json.dump({"fixtures": [compare(sidecar, name, request) for name, request in fixtures]},
              sys.stdout)


if __name__ == "__main__":
    main()
