"""FORGE's CAD kernel, as a long-running process.

# Why a sidecar and not a call per build

The 2026-09-05 spike measured the costs separately and they are nothing alike:
importing build123d takes 2.5 s and building the part takes 46 ms. A process per
export would pay the 2.5 s every time and put a CAD kernel outside the range
where it can sit inside a conversational turn. Imported once and kept warm, the
kernel is faster than the network hop that asked for it.

# The contract

One JSON object per line in, one per line out, in order. The first line this
writes is a ready banner, AFTER the import has finished, so the caller knows the
difference between "still starting" and "wedged".

Every reply carries "ok". A build that fails answers ok=false with a reason and
the process stays up: an invalid solid is a normal outcome here — OCCT refuses to
build nonsense rather than producing something wrong, which is the behaviour this
was chosen for — and it must not take the kernel down with it.

# What this does NOT do

It does not decide anything about the shape. Dimensions arrive resolved, the
rotation arrives as a matrix, and the placement is translate ∘ rotate exactly as
the renderer does it (internal/domain/geometry/solid.go). Any convention decided
here would be a second opinion about what the document means.
"""
import base64
import json
import math
import os
import sys
import tempfile
import traceback

PROTOCOL = 1

try:
    from build123d import (
        Box, Cylinder, Cone, Sphere, Rectangle, Plane, Location, Vector,
        Compound, Axis, Polyline, export_step, extrude, fillet, chamfer,
        make_face, revolve, sweep, Transition, Line, ThreePointArc, Wire, Face,
    )
except Exception as exc:  # pragma: no cover - reported to the caller, not raised
    sys.stdout.write(json.dumps({
        "ready": False,
        "error": "build123d could not be imported: %s" % exc,
    }) + "\n")
    sys.stdout.flush()
    sys.exit(1)


def _wire(curve):
    """One outline or path, exactly as FORGE resolved it.

    An edge is a straight line, or — when it carries "via" — a circular arc
    through that point. THREE POINTS and not a radius, because a radius plus two
    endpoints does not determine an arc in three dimensions: it determines one
    per plane through the chord, and build123d's RadiusArc picked a different
    plane from the one intended (measured 2026-09-05; the swept solid missed its
    own volume by 5% with the section visibly distorted). Three points fix the
    plane, so there is nothing left to guess.

    Corner radii are worked out in Go — where the corner, its neighbours and the
    refusals all live — and arrive here already turned into arcs. Nothing in this
    file decides where a rounded corner goes.
    """
    at = Vector(*curve["start"])
    edges = []
    for e in curve.get("edges") or []:
        to = Vector(*e["to"])
        via = e.get("via")
        if via is None:
            edges.append(Line(at, to))
        else:
            edges.append(ThreePointArc(at, Vector(*via), to))
        at = to
    if not edges:
        raise ValueError("a drawing with no edges cannot be built")
    return Wire(edges)


def _faces(solid):
    """The section, as one face per SOLID area.

    A hole in the SECTION and a hole through the SOLID are different things and
    both exist. A bolt hole through a plate is a cylinder cut out with a feature,
    placed in space. A bore that follows a bent tube round every corner cannot be
    cut by any tool this vocabulary can describe, and is a loop in the drawing.

    # Why a LIST, when a section is usually one face

    Because a loop can sit inside another loop, and then it is an ISLAND: solid
    material standing in a void — the post in an annular slot, the bar of a
    letter A, a lug in the bottom of a pocket. Ordinarily there is exactly one
    face here and this reads as it always did.

    OCCT cannot be told that with one Face. `Face(outer, inners)` treats every
    inner wire as a hole, so an island handed to it would be cut away rather than
    left standing. Each solid area is therefore its own face, and the caller
    fuses the solids they produce.

    # Why the nesting is not worked out here

    It arrives in "hole_parents", computed in Go where the tessellator computes
    the same thing (see loopParents). Containment is a question about polygons,
    this file holds the drawing as CURVES, and a kernel that flattened them again
    at a fineness of its own choosing could nest differently from the picture on
    screen — which is the one thing the section frame is also sent to prevent.
    """
    outer = _wire(solid["outline"])
    holes = solid.get("holes") or []
    if not holes:
        return [make_face(outer)]

    # -1 means "directly inside the outline", which is what every hole is unless
    # the document says otherwise — including every document written before this
    # existed, which sends no parents at all.
    parents = solid.get("hole_parents") or [-1] * len(holes)
    if len(parents) != len(holes):
        parents = [-1] * len(holes)

    def depth(i):
        d, seen = 0, set()
        while i >= 0 and i not in seen:
            seen.add(i)
            d += 1
            i = parents[i]
        return d

    faces = []
    # The outline itself, with the holes DIRECTLY inside it.
    faces.append(_solid_face(outer, [h for i, h in enumerate(holes) if parents[i] == -1]))
    # Then every island: a hole at an even depth is not a hole at all.
    for i, hole in enumerate(holes):
        if depth(i) % 2 == 0:
            faces.append(_solid_face(_wire(hole),
                                     [h for j, h in enumerate(holes) if parents[j] == i]))
    return faces


def _solid_face(outer_wire, inner_loops):
    """One face: a boundary and the voids directly inside it."""
    if not inner_loops:
        return make_face(outer_wire)
    return Face(outer_wire, [_wire(h) if isinstance(h, dict) else h for h in inner_loops])


def _fused(solids):
    """One solid from the areas a section describes.

    Ordinarily a section has ONE area and this returns it untouched — no boolean
    is performed, so the common case pays nothing and cannot be changed by this.
    More than one means the section had an island in it, and the areas are
    disjoint by construction (their loops do not cross, which Go checks), so the
    fuse is a union of things that do not touch and OCCT does it exactly.
    """
    built = list(solids)
    if not built:
        raise ValueError("the section described no area at all")
    out = built[0]
    for other in built[1:]:
        out = out + other
    return out


def _placement(solid):
    """The part's frame, from the matrix the caller computed.

    x_dir and z_dir are the matrix's first and third COLUMNS — where the frame's
    own x and z axes end up. Reading rows instead would apply the inverse
    rotation, which is wrong in a way that looks plausible for symmetric parts
    and only shows on the asymmetric ones.
    """
    m = solid["matrix"]
    x_dir = Vector(m[0], m[3], m[6])
    z_dir = Vector(m[2], m[5], m[8])
    origin = Vector(*solid["position"])
    return Location(Plane(origin=origin, x_dir=x_dir, z_dir=z_dir))


def _shape(solid):
    """One primitive, centred on the origin in its own frame.

    build123d builds a cylinder along +Z and this system draws it along +Y
    (mesh.go: the rings are at ±height/2 on y). The correction is applied here,
    once, as a rotation of the local frame rather than by rebuilding the
    primitive — see Plane.XZ, whose normal is -Y and whose x stays x.
    """
    kind = solid["shape"]
    d = solid["dims"]

    if kind == "box":
        return Box(d["width"], d["height"], d["depth"])
    if kind == "sphere":
        return Sphere(d["radius"])
    # No "tube": it was retired from the vocabulary and Go resolves it before
    # anything reaches here (internal/domain/geometry/retired.go). Accepting it
    # anyway would build the right solid for the wrong reason and hide the day
    # the resolution stopped happening — this way the kernel refuses the part by
    # name, loudly, which is the failure that gets fixed.
    if kind in ("cylinder", "cone"):
        # A cone is a cylinder whose top radius is zero; both arrive already
        # reduced that way, so there is one code path and no second opinion
        # about what "cone" means.
        top = d.get("radius_top", d["radius"])
        if top == d["radius"]:
            body = Cylinder(d["radius"], d["height"])
        elif top == 0:
            body = Cone(d["radius"], 0, d["height"])
        else:
            body = Cone(d["radius"], top, d["height"])
        # +Z to +Y.
        return Plane.XZ * body
    if kind == "extrusion":
        # A closed outline in the part's own XY plane, swept along local Z and
        # CENTRED on it: amount is half the depth in both directions, so an
        # extrusion behaves like a box's height and not like a face that grew in
        # one direction only.
        #
        # The outline's own coordinates are used as given. They are deliberately
        # not re-centred — see internal/domain/geometry/profile.go for why — so
        # the part's position places the outline's ORIGIN and a hole placed
        # against a drawn corner stays against it.
        return _fused(extrude(f, amount=d["depth"] / 2.0, both=True)
                      for f in _faces(solid))
    if kind == "revolve":
        # The outline turned a full circle about its own axis. Every point is on
        # one side of that axis — checked in Go, where the offending coordinate
        # can be named; OCCT refuses the crossing case with "BRep_API: command
        # not done", which tells a reader nothing.
        #
        # A full turn only. A sector is a revolve with something cut out of it,
        # which needs no vocabulary of its own — the same reasoning that makes a
        # hole a cut rather than a new kind of part.
        axis = Axis.X if solid.get("axis") == "x" else Axis.Y
        return _fused(revolve(f, axis, 360) for f in _faces(solid))
    if kind == "sweep":
        # The same outline, carried along a path instead of a straight line.
        #
        # Three things here are DECIDED IN GO and merely obeyed, because each of
        # them has more than one defensible answer and two builders answering
        # separately is how an exported solid ends up rotated from the drawn one:
        #
        #   - where the section starts and which way up it is. It arrives as
        #     "section_frame", the same column convention as the placement
        #     matrix, so nothing here has an opinion about how a sweep is framed.
        #   - that the path is used as absolute coordinates in the part's own
        #     frame. OCCT otherwise sweeps from wherever the section already is
        #     and ignores where the path starts (measured 2026-09-05), so the
        #     section is placed AT the path's first point.
        #   - that corners are mitred. Transition.RIGHT is not a preference: on
        #     an elbow whose correct volume is 5000 mm³, OCCT's default
        #     TRANSFORMED returned 1600 and ROUND rounds the corner into a shape
        #     the drawing did not describe. Measured 2026-09-05, build123d 0.11.1.
        #
        # Every path that cannot be swept — a repeated point, a reversal, a bend
        # too tight for its own outline — is refused in Go, where the offending
        # points can be named. OCCT refuses the first with "BRep_API: command not
        # done", the second with an EMPTY message, and the third not at all.
        path = solid["path"]
        m = solid["section_frame"]
        start = Plane(origin=Vector(*path["start"]),
                      x_dir=Vector(m[0], m[3], m[6]),
                      z_dir=Vector(m[2], m[5], m[8]))
        route = _wire(path)
        return _fused(sweep(start * f, route, transition=Transition.RIGHT)
                      for f in _faces(solid))
    if kind == "plane":
        # A face, not a solid, and deliberately so: a plane has no thickness and
        # will not print, machine, or hold a volume. It is exported because it is
        # part of what was drawn, and the label says what it is.
        return Plane.XZ * Rectangle(d["width"], d["depth"])
    raise ValueError("unsupported shape %r" % kind)


def _edges(shape, rule):
    """Which edges a fillet or chamfer touches, by RULE and never by index.

    An index selects a different edge the moment a parameter changes, which is
    the failure mode that makes naive parametric scripts break on their second
    run (docs/spikes/2026-09-05-parametric-cad-kernel/). Y is up in this system,
    so "vertical" is the Y axis.

    The names are validated in Go against the same closed table; an unknown one
    never reaches here.
    """
    edges = shape.edges()
    if rule == "vertical":
        return edges.filter_by(Axis.Y)
    if rule == "horizontal":
        return edges.filter_by(Axis.X) + edges.filter_by(Axis.Z)
    if rule == "top":
        return edges.group_by(Axis.Y)[-1]
    if rule == "bottom":
        return edges.group_by(Axis.Y)[0]
    return edges


def _apply(op, shapes):
    """One operation, in place in the shapes dict.

    Order is the document's. A feature reads what the features before it left
    behind, which is what makes "cut the holes, then round what is left" mean
    something different from the other way round.
    """
    target = shapes[op["of"]]
    kind = op["op"]

    if kind in ("cut", "fuse"):
        for tool_id in op.get("with") or []:
            tool = shapes[tool_id]
            target = (target - tool) if kind == "cut" else (target + tool)
        shapes[op["of"]] = target
        return

    selected = _edges(target, op.get("edges") or "all")
    if not selected:
        # Nothing to round is not a failure: a rule can legitimately select no
        # edge (a sphere has none vertical). Reported so a person is not left
        # wondering why the fillet they asked for is not there.
        raise ValueError("the %s selected no %s edges" % (kind, op.get("edges") or "all"))
    if kind == "fillet":
        shapes[op["of"]] = fillet(selected, radius=op["radius"])
    else:
        shapes[op["of"]] = chamfer(selected, length=op["radius"])


# How many times the search below is allowed to call the kernel.
#
# Ten halvings of the requested radius resolve it to within 0.1% of itself, which
# is far finer than any radius a person types. The bound exists because this runs
# on a path that has ALREADY failed and a person is waiting for a message: a
# search that took a minute to produce a better sentence would be a worse
# outcome than the sentence.
_FIT_STEPS = 10


def _largest_that_fits(kind, selected, requested):
    """The biggest radius this geometry actually takes, at or below `requested`.

    # The problem this solves

    OCCT refuses a fillet the geometry cannot carry — correctly, because the
    alternative is a self-intersecting solid — and it refuses it with an EMPTY
    message: a Standard_Failure carrying no text (measured 2026-09-05 against
    build123d 0.11.1). So the person who asked for R12 on a 15 mm web was told
    "Standard_Failure" and had to guess. Every guess is another round trip
    through the model, and the number they are guessing at is one the kernel
    already knows.

    # Why bisection and not a formula

    There IS a closed form for the largest fillet on one convex edge between two
    planes, and it is useless here: `edges` is a RULE that selects many edges at
    once (see _edges), the limit is whichever of them is tightest, and edges
    interact — two fillets that each fit alone will not fit together when their
    faces meet. The only authority on "does this radius work" is the kernel, so
    it is asked.

    # Why not build123d's own Shape.max_fillet

    OCCT's refusal literally recommends it ("use max_fillet() to find the largest
    valid fillet radius") and it was read before this was written. Three reasons
    it is not what is called here, in order of weight:

      - it searches 0 to 2x the bounding box DIAGONAL and gives up after 10
        iterations with a RuntimeError. On the 60 mm plate this file's own test
        uses, that window is 170 mm wide and 10 halvings land 0.17 mm short of
        its own 0.1 tolerance — so the common case is an exception where a
        number was wanted. Searching only up to the radius somebody ASKED for
        needs no such luck: the answer is always inside the window, by
        construction.
      - it has no chamfer. A chamfer that is too long fails the same way and the
        person needs the same sentence, and two implementations of one idea is
        how the two drift.
      - it raises when nothing works, where "these edges take no fillet at all"
        is a fact worth reporting differently rather than an error.

    # Why it returns a radius that was BUILT, never one that was computed

    The value goes into a message telling somebody what to type next, and a
    suggestion that then fails is worse than no suggestion. So the answer is
    always the lower bound of the bisection — a radius this function has watched
    the kernel accept — rounded DOWN. None is returned when even the smallest
    step fails, which is a different fact and is reported as one: the edges
    cannot be rounded at all, and no radius is the answer.
    """
    def works(r):
        try:
            if kind == "fillet":
                fillet(selected, radius=r)
            else:
                chamfer(selected, length=r)
            return True
        except Exception:
            return False

    lo, hi = 0.0, float(requested)
    for _ in range(_FIT_STEPS):
        mid = (lo + hi) / 2
        if works(mid):
            lo = mid
        else:
            hi = mid
    if lo <= 0:
        return None
    # Down to three decimals, and down rather than to-nearest: rounding up would
    # hand back a number one ulp past the last one that was proven to build.
    return math.floor(lo * 1000) / 1000


def _with_a_way_out(op, shapes, reason):
    """Add the largest radius that would have worked, when that is the problem.

    Only for a fillet or chamfer whose EDGES were found — "the fillet selected no
    vertical edges" is a different failure with a different remedy, and running a
    ten-step search to re-discover it would waste the person's time to tell them
    something they were already told.

    The search itself is defended: it is a diagnostic on an already-failed path,
    and a diagnostic that raises would replace a real refusal with a confusing
    one.
    """
    kind = op.get("op")
    if kind not in ("fillet", "chamfer"):
        return reason
    try:
        selected = _edges(shapes[op["of"]], op.get("edges") or "all")
        if not selected:
            return reason
        fits = _largest_that_fits(kind, selected, op["radius"])
    except Exception:
        return reason
    what = "radius" if kind == "fillet" else "length"
    if fits is None:
        return ("%s; these edges take no %s at all, so this one cannot be rounded here — "
                "remove the %s, or change the shape it is applied to" % (reason, kind, kind))
    return ("%s; the geometry cannot take a %s of %g here. The largest that DOES build on these "
            "edges is %g, found by asking the kernel" % (reason, what, op["radius"], fits))


def _build(request):
    solids = request.get("solids") or []
    if not solids:
        return {"ok": False, "error": "no parts to build"}

    built, names, ids, skipped = [], [], [], []
    for s in solids:
        try:
            shape = _shape(s)
        except Exception as exc:
            # One bad part does not lose the rest. Named, so the caller can say
            # which — a file quietly missing a part is worse than one that says
            # it is missing it.
            #
            # The TYPE is included because OCCT's own refusals arrive with an
            # empty message: a negative radius raises Standard_Failure carrying
            # no text at all, and "Plate: " tells a reader nothing. Measured
            # 2026-09-05 against build123d 0.11.1.
            reason = str(exc).strip() or type(exc).__name__
            skipped.append("%s: %s" % (s.get("label") or s.get("id"), reason))
            continue
        built.append(_placement(s) * shape)
        names.append(s.get("label") or s.get("id"))
        ids.append(s.get("id"))

    if not built:
        return {"ok": False, "error": "no part could be built", "skipped": skipped}

    # --- features -----------------------------------------------------------
    #
    # Applied to the placed solids, in the document's order. A tool is CONSUMED:
    # it does not also appear as a solid of its own, or the hole would be filled
    # by the thing that made it.
    shapes = dict(zip(ids, built))
    consumed, failed = set(), []
    for op in request.get("operations") or []:
        missing = [n for n in [op["of"]] + list(op.get("with") or []) if n not in shapes]
        if missing:
            # A feature naming a part that could not be built. Reported rather
            # than skipped silently: the assembly is missing an operation
            # somebody asked for.
            failed.append("%s: %s could not be built, so this was not applied"
                          % (op["id"], ", ".join(missing)))
            continue
        try:
            _apply(op, shapes)
        except Exception as exc:
            reason = str(exc).strip() or type(exc).__name__
            failed.append("%s: %s" % (op["id"], _with_a_way_out(op, shapes, reason)))
            continue
        consumed.update(op.get("with") or [])

    kept, kept_names = [], []
    for part_id, name in zip(ids, names):
        if part_id in consumed:
            continue
        kept.append(shapes[part_id])
        kept_names.append(name)
    if not kept:
        return {"ok": False, "error": "every part was consumed as a tool, leaving nothing to export",
                "skipped": skipped, "features_failed": failed}
    built, names = kept, kept_names

    # A compound, not a fused union. Fusing would MERGE parts that touch, and a
    # bracket and the plate it sits on would come back as one body with the seam
    # gone — a claim about assembly that nothing in the document made. The parts
    # stay separate and named, which is what an assembly is.
    assembly = Compound(children=built)
    for child, name in zip(assembly.children, names):
        child.label = name

    # The extent, which is the only thing in this reply that can show a part is
    # ORIENTED wrongly. Volume cannot: it is the same however the solid is
    # turned, so a cylinder built along the wrong axis produces an identical
    # number and an identical-looking file. Reported so the caller can assert on
    # it — see TestKernel_ACylinderPointsTheWayThisSystemDrawsIt.
    box = assembly.bounding_box()
    out = {
        "ok": True,
        "parts": len(built),
        "volume": float(getattr(assembly, "volume", 0.0)),
        "bounds": [float(box.min.X), float(box.min.Y), float(box.min.Z),
                   float(box.max.X), float(box.max.Y), float(box.max.Z)],
        "skipped": skipped,
        "features_failed": failed,
    }

    fmt = request.get("format")
    if fmt == "step":
        # export_step writes a file; there is no in-memory form in build123d.
        # Deleted immediately after reading, and created with mkstemp so a
        # concurrent build cannot collide with it.
        fd, path = tempfile.mkstemp(suffix=".step")
        os.close(fd)
        try:
            export_step(assembly, path)
            with open(path, "rb") as fh:
                out["step"] = base64.b64encode(fh.read()).decode("ascii")
        finally:
            try:
                os.unlink(path)
            except OSError:
                pass
    return out


def main():
    sys.stdout.write(json.dumps({"ready": True, "protocol": PROTOCOL}) + "\n")
    sys.stdout.flush()

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            request = json.loads(line)
        except Exception as exc:
            reply = {"ok": False, "error": "unreadable request: %s" % exc}
        else:
            try:
                reply = _build(request)
            except Exception as exc:
                # Reported, never raised. An invalid solid is a normal outcome —
                # OCCT refusing to build nonsense is why this kernel was chosen —
                # and it must not take the process down with it.
                reply = {"ok": False, "error": "%s: %s" % (type(exc).__name__, exc),
                         "trace": traceback.format_exc()[-2000:]}
        sys.stdout.write(json.dumps(reply) + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
