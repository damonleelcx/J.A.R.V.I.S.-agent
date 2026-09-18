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


def is_location(value):
    """A build123d Location (or a Rotation, which is one)."""
    return hasattr(value, "wrapped") and hasattr(value.wrapped, "Transformation")


def location_object(value):
    """A Location as the OBJECT it is, not only as the numbers it holds.

    # Why the numbers are not enough (fences that can fail)

    ‼️ Added 2026-09-15. Until now this file compared every placement through
    `transformation()` alone, and compared a `__dict__` only for the placed SOLID.
    So `_location_of` — which builds build123d's `Location(top_loc)` by hand,
    setting exactly the two attributes the constructor sets — was fenced only on
    the transformation it carries, and nothing inspected the object itself. #121
    drilled that gap and its drill stayed green: deleting `out.location_index = 0`
    changed no number anywhere, and the fence reported 0 differences on 174 solids
    (docs/spikes/2026-09-15-last-hot-spots, "Drills").

    A Location with `location_index` missing is not the object build123d's
    constructor returns, and anything that later reads that attribute — or
    deepcopies, pickles or repr's the Location — sees a different object while
    every transformation still matches to the bit. So the ATTRIBUTES are compared,
    by name and by value, against what the real constructor set in the reference
    run.
    """
    return [type(value).__name__, sorted(value.__dict__),
            value.__dict__.get("location_index"), transformation(value)]


def attribute(value):
    """An attribute as something comparable: a location or rotation as the object
    it is (see location_object), which includes its numbers."""
    if is_location(value):
        return location_object(value)
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


def shape_keys(sidecar, problems):
    """What the shape key separates and what it must NOT separate.

    # Why this cannot be left to the comparison below (fences that can fail)

    ‼️ Added 2026-09-15. Every comparison in compare() runs the SAME _shape_key on
    both sides, so a key that drops a field, or that stops treating two spellings
    of one shape as one shape, is invisible to it: both runs share a definition
    they should not, and agree perfectly about it. The key therefore has to be
    asserted directly.

    Two properties, both of which the key had before it became a tuple
    (docs/spikes/2026-09-15-last-hot-spots, recommendation 2):

      - every distinct kind in the fixture is a distinct definition — "mirrored"
        included, which is what makes `ell` and `ell-m` two shapes and not one;
      - "dims" is a MAP on the Go side (geometry/solid.go), so the same dimensions
        written in a different key order are the same shape. json.dumps(sort_keys=True)
        gave that for free; a tuple has to sort the pairs to keep it.
    """
    solids = fixture(1)["solids"]

    def kind_of(part_id):
        """Which shape a fixture part is an occurrence of.

        The seven kinds are placed as "<kind>-<matrix>-<copy>"; the three parts the
        feature uses are named outright. ‼️ plate-cut and plate-whole are ONE kind
        deliberately — the same box, one of which an operation later cuts — so the
        key MUST cover both, and a check that expected them to differ would be
        asserting the opposite of what building once per definition means.
        """
        if part_id in ("plate-cut", "plate-whole"):
            return "plate"
        if part_id == "drill":
            return "drill"
        return part_id.rsplit("-", 2)[0]

    keys = {}
    for solid in solids:
        keys.setdefault(sidecar._shape_key(solid), set()).add(kind_of(solid["id"]))
    kinds = {kind_of(solid["id"]) for solid in solids}
    if len(keys) != len(kinds):
        problems.append("the fixture has %d kind(s) but %d shape key(s)" % (len(kinds), len(keys)))
    for names in keys.values():
        if len(names) > 1:
            problems.append("one shape key covers %s, which are different shapes"
                            % " and ".join(sorted(names)))
    box = next(s for s in solids if s["shape"] == "box")
    # ‼️ Reversed, not sorted: sorting this fixture's dims happens to reproduce the
    # order they are written in, so a "reordered" copy built by sorting is the SAME
    # dict and the check tests nothing. Found by drilling it — the mutation that
    # drops the sort stayed green against the sorted version of this check.
    swapped = dict(reversed(list(box["dims"].items())))
    if list(swapped) == list(box["dims"]):
        problems.append("the dims order check tests nothing: %s's dims read the same either way"
                        % box["id"])
    reordered = dict(box, dims=swapped)
    if sidecar._shape_key(box) != sidecar._shape_key(reordered):
        problems.append("the same dims written in another key order is a different shape key: "
                        "%s against %s" % (sidecar._shape_key(reordered), sidecar._shape_key(box)))
    try:
        hash(sidecar._shape_key(box))
    except TypeError as exc:
        problems.append("a shape key cannot be used as a dict key: %s" % exc)
    return len(keys)


def compare(sidecar, problems):
    request = fixture(1)
    checked = 0
    # id(Location) -> (what holds it, the object), for the freshness check below.
    shared_locations = {}
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
            # Added 2026-09-17 (kernel last walls): the moved shape is cast once per
            # definition instead of by downcast() per copy, so the Python CLASS of the
            # TopoDS object is compared too, not only the ShapeType it reports.
            if type(a.wrapped) is not type(b.wrapped):
                problems.append("%s: its B-rep is a %s, build123d's a %s" % (
                    what, type(b.wrapped).__name__, type(a.wrapped).__name__))
            if transformation(a) != transformation(b):
                problems.append("%s: placed at %s, build123d at %s" % (what, transformation(b), transformation(a)))
            p, q = ref_solids["placed"][i], new_solids["placed"][i]
            if (p is None) != (q is None):
                problems.append("%s: a copy in one run and not the other" % what)
            elif p is not None:
                if p[0] != q[0]:
                    problems.append("%s: the shape key recorded differs" % what)
                # The placement as the OBJECT build123d's constructor returns, not
                # only as its twelve numbers: see location_object. This is what
                # catches a _location_of that sets the wrong attributes.
                if location_object(p[1]) != location_object(q[1]):
                    problems.append("%s: the placement is %s, build123d's %s" % (
                        what, location_object(q[1]), location_object(p[1])))
                # ‼️ A placement and a copy's own Location are FRESH per occurrence,
                # never the definition's and never another copy's — _located's plan
                # carries the definition's attribute VALUES, so a shared Location
                # would let one occurrence's placement be changed through another's.
                # Recorded with the object itself, never by id() alone: CPython
                # reuses the id of a collected object, which is the same trap the
                # per-definition plan had to avoid (see sidecar.py, _located).
                for kind, loc in [("placement", q[1])] + [
                        (k, v) for k, v in b.__dict__.items() if is_location(v)]:
                    owner = shared_locations.get(id(loc))
                    if owner is not None and owner[1] is loc:
                        problems.append("%s: its %s is the same Location object as %s's" % (
                            what, kind, owner[0]))
                    shared_locations[id(loc)] = ("%s %s" % (what, kind), loc)
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


def deep_fallback(sidecar, problems):
    """_located's `deep` fallback, on a definition carrying an attribute it does not
    recognize.

    # Why (fence gap named in #134)

    ‼️ Added 2026-09-17 (kernel last walls). No shape the sidecar builds carries an
    attribute _attribute_plan classifies as "deep" — build123d's own are atomics,
    Locations, empty dicts and lists, and `align` (which deepcopy returns as itself) —
    so the fallback `copy.deepcopy(value, memo)` never ran in any fixture and a drill
    that broke it stayed green. It is the branch that keeps _located correct for the
    NEXT attribute build123d adds, so it is exercised here on purpose: every
    definition's _shape is given `forge_trace = ["kept", {"n": 1}, <the shape>]`,
    which is a non-empty list, so it is neither "same", "dict" nor "list", and the
    fixture is built build123d's way (`location * shape`) and the sidecar's way.

    Each copy's forge_trace must be what build123d's deepcopy made: equal values; a
    fresh list and a fresh dict, never the definition's or another copy's; and its
    last element the COPY ITSELF — deepcopy's memo maps the definition to the copy
    being made, so a fallback that dropped the memo would put a stray deep copy of
    the definition there instead. And the fallback must be the path taken: once per
    located occurrence.
    """
    real_shape = sidecar._shape

    def traced(solid):
        shape = real_shape(solid)
        shape.forge_trace = ["kept", {"n": 1}, shape]
        return shape

    request = fixture(1)
    sidecar._shape = traced
    try:
        ref, ref_solids = run(sidecar, False, "", request)
        before = sidecar._located_fallbacks
        new, new_solids = run(sidecar, True, "", request)
        fallbacks = sidecar._located_fallbacks - before
    finally:
        sidecar._shape = real_shape
    located = len(request["solids"])
    if fallbacks != located:
        problems.append("the deep fallback ran %d time(s) for %d located occurrence(s); once each"
                        % (fallbacks, located))
    seen = {}
    checked = 0
    for i, (a, b) in enumerate(zip(ref_solids["solids"], new_solids["solids"])):
        what = "deep fallback, %s" % ref_solids["ids"][i]
        q = new_solids["placed"][i]
        ta, tb = a.__dict__.get("forge_trace"), b.__dict__.get("forge_trace")
        if q is None:
            # A part a feature changed is a new solid, not a copy: neither run gives
            # it the attribute, and both must agree on that.
            if (ta is None) != (tb is None):
                problems.append("%s: carries forge_trace in one run and not the other" % what)
            continue
        if ta is None or tb is None:
            problems.append("%s: forge_trace missing (build123d's %r, the sidecar's %r)" % (what, ta, tb))
            continue
        checked += 1
        if ta[2] is not a:
            problems.append("%s: build123d's own copy does not point at itself; the fixture is wrong" % what)
        if type(tb) is not list or len(tb) != 3 or tb[0] != ta[0] or tb[1] != ta[1]:
            problems.append("%s: forge_trace is %r, build123d's %r" % (what, tb, ta))
            continue
        definition = q[2].__dict__.get("forge_trace")
        if tb is definition or tb[1] is definition[1]:
            problems.append("%s: forge_trace is its definition's own object, not a copy" % what)
        if tb[2] is not b:
            problems.append("%s: forge_trace's shape is %s, not the copy itself (build123d's is its copy)"
                            % (what, "the definition" if tb[2] is q[2] else "another object"))
        for part, obj in (("list", tb), ("dict", tb[1])):
            owner = seen.get(id(obj))
            if owner is not None and owner[1] is obj:
                problems.append("%s: its forge_trace %s is the same object as %s's" % (what, part, owner[0]))
            seen[id(obj)] = (what, obj)
    if checked < 50:
        problems.append("deep fallback: %d placed copies checked; the fixture has 57" % checked)
    return {"fallbacks": fallbacks, "located": located, "checked": checked, "problems": problems[:40],
            "problem_count": len(problems)}


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
    distinct_keys = shape_keys(sidecar, problems)
    checked = compare(sidecar, problems)
    deep = deep_fallback(sidecar, [])
    out = {"checked": checked, "distinct_keys": distinct_keys, "deep": deep,
           "problems": problems[:40], "problem_count": len(problems),
           "counts": {"reference": {"1": counts(sidecar, False, 1), "8": counts(sidecar, False, 8)},
                      "shipped": {"1": counts(sidecar, True, 1), "8": counts(sidecar, True, 8)}}}
    json.dump(out, sys.stdout)


if __name__ == "__main__":
    main()
