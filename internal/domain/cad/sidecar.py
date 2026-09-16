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
import time
import traceback

PROTOCOL = 1

try:
    from build123d import (
        Box, Cylinder, Cone, Sphere, Rectangle, Plane, Location, Vector,
        Compound, Axis, Polyline, PrecisionMode, extrude, fillet, chamfer, loft,
        make_face, revolve, sweep, Transition, Line, ThreePointArc, Wire, Face,
        import_step,
    )
    # The STEP writer's own pieces, used directly rather than through build123d's
    # export_step, which only accepts a Compound(children=...) — see _step_document.
    from OCP.APIHeaderSection import APIHeaderSection_MakeHeader
    from OCP.IFSelect import IFSelect_ReturnStatus
    from OCP.Interface import Interface_Static
    from OCP.Message import Message, Message_Gravity
    from OCP.STEPCAFControl import STEPCAFControl_Controller, STEPCAFControl_Writer
    from OCP.STEPControl import STEPControl_Controller, STEPControl_StepModelType
    from OCP.TCollection import TCollection_ExtendedString, TCollection_HAsciiString
    from OCP.TDataStd import TDataStd_Name
    from OCP.TDocStd import TDocStd_Document
    from OCP.TopLoc import TopLoc_Location
    from OCP.XCAFApp import XCAFApp_Application
    from OCP.XCAFDoc import XCAFDoc_DocumentTool
    from OCP.XSControl import XSControl_WorkSession
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
    if kind == "section":
        # A drawing with no thickness. It is not a solid and has no volume, and
        # that is correct: it exists to be blended with other sections by a loft,
        # which consumes it. On its own it is a face, and the kernel is content
        # to carry faces in an assembly — see the volume note in _build.
        return _fused(_faces(solid))
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
    if kind == "step":
        # A solid that was built somewhere else and arrived as STEP.
        #
        # This is how a model-written script becomes a PART. The script runs in
        # its own sandboxed process (script.py) and hands back STEP, which is
        # imported here so the result is an ordinary solid: it can be cut,
        # filleted, fused and exported exactly like a box, and everything
        # downstream — the panel, compare, prototype_edit, the repair pass —
        # keeps working because the document still describes parts.
        #
        # ‼️ It belongs HERE, in the shape dispatch. It was first written into
        # _apply (the feature function), after a return, where it could never
        # run — so this function refused every scripted part as "unsupported
        # shape 'step'" and every export and built mesh left it out, while the
        # turn's own check (RunScript) said it built. No test built a scripted
        # part past RunScript. See
        # docs/bugfix/2026-09-10-scripted-parts-never-exported.md;
        # fence: TestKernel_AScriptedPartIsExportedAndMeshed.
        text = solid.get("step") or ""
        if not text:
            raise ValueError("this part carries no STEP to import")
        with tempfile.NamedTemporaryFile("w", suffix=".step", delete=False) as fh:
            fh.write(text)
            path = fh.name
        try:
            imported = import_step(path)
        finally:
            try:
                os.unlink(path)
            except Exception:
                pass
        # Returned centred as every other shape is: the caller applies the
        # part's own position and rotation on top, so a scripted part is placed
        # by the same rule as a box.
        return imported
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

    if kind == "loft":
        # The target is the first station and the named parts are the rest, in
        # the order they are named. Order is the shape: the same three sections
        # in a different order are a different solid, so they are NOT sorted by
        # position here — a document that lists them out of order is describing
        # something, and silently reordering would build a shape nobody wrote.
        sections = [target] + [shapes[i] for i in (op.get("with") or [])]
        faces = []
        for shape in sections:
            found = shape.faces()
            if not found:
                raise ValueError("a loft station has no face to blend; a station must be a "
                                 "\"section\", which is an outline with no thickness")
            # A solid used as a station contributes its faces and would blend to
            # the wrong one. Refused by name rather than guessed at.
            if len(found) > 1:
                raise ValueError("a loft station has %d faces; a station must be a \"section\", "
                                 "which is a single outline with no thickness" % len(found))
            faces.append(found[0])
        if len(faces) < 2:
            raise ValueError("a loft needs at least two stations to blend between")
        # ruled=False is a SMOOTH surface through every station, which is what a
        # sculpted body means by "loft" and why lofting beats extruding. ruled
        # joins them with straight sides, for a shape that really is faceted —
        # a hopper, a transition duct — where a smooth blend would round corners
        # that exist. The document says which; the default is smooth.
        blended = loft(faces, ruled=bool(op.get("ruled")))
        # ‼️ A loft of COPLANAR stations succeeds and encloses nothing.
        #
        # A section lies in its own XY plane, so two stations offset along X or Y
        # are side by side in ONE plane and there is no length to blend along.
        # OCCT does not refuse this: it returns a shape with no volume, which
        # exports as an empty solid and tessellates to nothing. Caught here,
        # where the reason can be named — the alternative is a person looking at
        # an empty viewport with a build that reported success.
        if float(getattr(blended, "volume", 0.0)) <= 0.0:
            raise ValueError("the loft enclosed no volume; its stations are in one plane. "
                             "A section lies in its own XY plane, so stations are separated "
                             "along local Z — offsetting them along X or Y places them side "
                             "by side with no length to blend along")
        shapes[op["of"]] = blended
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
    # The unit is stated because the numbers are the KERNEL's, in millimetres,
    # and the reader may have written the model in cm or inches. Printing a bare
    # number would offer them "10" for a radius they typed as "1".
    # docs/bugfix/2026-09-13-feature-radii-were-sent-in-the-documents-units.md
    return ("%s; the geometry cannot take a %s of %g mm here. The largest that DOES build on these "
            "edges is %g mm, found by asking the kernel" % (reason, what, op["radius"], fits))


# --- tessellation ----------------------------------------------------------
#
# # Why the kernel draws as well as exports
#
# The viewport had no boolean operations, so it drew the PRIMITIVES: a bolt hole
# was a cylinder standing in a plate rather than a void through it, a fillet was
# invisible, and a fuse of two bodies was two bodies. The STEP file exported from
# the same document was correct — the divergence existed only on screen, which is
# the one place a person judges the result.
#
# The solid is already built here, with every cut, fuse and fillet applied. All
# that was missing was handing back its surface. So this is not a new capability
# so much as the one that was already paid for and never collected.
#
# # Per solid, not one blob
#
# Each surviving part is tessellated separately and carries its id. The Parts
# panel selects and colours by part, and a single merged mesh would make that
# impossible — while a tool consumed by a cut correctly has no mesh at all,
# because it is no longer a body.

# A triangle budget, not a tolerance, is what a caller can reason about: nobody
# knows what deflection 0.05 costs, and everybody knows what two million
# triangles costs. The tolerance is searched to fit the budget and REPORTED.
_MESH_BUDGET = 400000
_MESH_COARSEN = 2.5
_MESH_TRIES = 6


def _tessellate_once(solids, deflection):
    meshes, total = [], 0
    for solid in solids:
        verts, tris = solid.tessellate(deflection)
        flat = []
        for v in verts:
            flat.extend((float(v.X), float(v.Y), float(v.Z)))
        idx = []
        for t in tris:
            idx.extend((int(t[0]), int(t[1]), int(t[2])))
        meshes.append({"vertices": flat, "triangles": idx})
        total += len(tris)
    return meshes, total


# # Once per definition (Phase 4, stage K4)
#
# A thousand copies of one bolt were tessellated a thousand times and sent as a
# thousand identical triangle lists, each already moved to its place. A copy is
# the shape K1 built once, placed; so the shape is tessellated once, in its own
# frame, and each copy is sent as the matrix that places it. The triangle budget
# counts a definition's triangles once, which is what they cost to draw.
#
# A solid a feature changed is not a copy of anything any more, and keeps the
# mesh it always had: its own, in assembly coordinates, under "mesh".
#
# _MESH_PER_DEFINITION turns this off. It exists so the per-solid tessellation it
# replaced stays available as the reference an instanced mesh must reproduce
# (testdata/mesh_per_definition.py); nothing in production turns it off.
_MESH_PER_DEFINITION = True


def _column_major(location):
    """A placement as the 4x4 matrix WebGL reads: columns first, translation last."""
    t = location.wrapped.Transformation()
    return [t.Value(1, 1), t.Value(2, 1), t.Value(3, 1), 0.0,
            t.Value(1, 2), t.Value(2, 2), t.Value(3, 2), 0.0,
            t.Value(1, 3), t.Value(2, 3), t.Value(3, 3), 0.0,
            t.Value(1, 4), t.Value(2, 4), t.Value(3, 4), 1.0]


def _tessellate(solids, ids, names, request, placed=None):
    """The surface of every kept solid.

    placed holds, for each solid, (shape key, location, the unplaced shape) when
    it is an untouched copy of a shape built once, or None when a feature changed
    it. Copies share one tessellation of their shape; the rest are meshed as placed.
    """
    # A deflection in millimetres, from the model's own size rather than a
    # constant: 0.1 mm is invisible on a bracket and catastrophic on a car body,
    # and the same number cannot serve both. The ASSEMBLY's size, for copies too:
    # a bolt tessellated to its own size would come out finer than the body it
    # sits in.
    # One pass, never Compound(children=...): see the note on the assembly in _build.
    box = Compound(list(solids)).bounding_box()
    span = max(float(box.max.X - box.min.X),
               float(box.max.Y - box.min.Y),
               float(box.max.Z - box.min.Z), 1.0)
    deflection = float(request.get("deflection") or (span / 2000.0))

    index, shapes, instances, own = {}, [], [], []
    for i, solid in enumerate(solids):
        p = placed[i] if (_MESH_PER_DEFINITION and placed) else None
        if p is None:
            own.append(i)
            continue
        key, location, shape = p
        if key not in index:
            index[key] = len(shapes)
            shapes.append(shape)
        instances.append((i, index[key], location))

    simplified = False
    for _ in range(_MESH_TRIES):
        try:
            definitions, shared = _tessellate_once(shapes, deflection)
            meshes, separate = _tessellate_once([solids[i] for i in own], deflection)
        except Exception as exc:
            reason = str(exc).strip() or type(exc).__name__
            return {"mesh_error": "the solid could not be tessellated: %s" % reason}
        total = shared + separate
        if total <= _MESH_BUDGET:
            break
        # Over budget. Coarsened rather than truncated: half a model is a lie
        # about the shape, and a coarser one is the same shape less finely.
        deflection *= _MESH_COARSEN
        simplified = True
    else:
        return {"mesh_error": "this assembly could not be tessellated within %d triangles"
                             % _MESH_BUDGET}

    for mesh, i in zip(meshes, own):
        mesh["id"] = ids[i]
        mesh["label"] = names[i]
    out = {"mesh": meshes,
           "mesh_triangles": total,
           "mesh_deflection": deflection,
           "mesh_simplified": simplified}
    if instances:
        out["mesh_definitions"] = definitions
        out["mesh_instances"] = [{"id": ids[i], "label": names[i], "definition": d,
                                  "matrix": _column_major(location)}
                                 for i, d, location in instances]
    return out


# --- interference ----------------------------------------------------------
#
# Two solids occupying the same material. Nothing in FORGE could see this before
# 2026-09-12: geometry/assembly.go says so in plain words (no interference test,
# no clearance, no kinematics), and a live car build measured that day came back
# with document_faults=0, a clean kernel build, a passing visual check and the
# master cylinder entirely inside the engine block.
# docs/spikes/2026-09-12-car-ceiling/README.md
#
# # Why it is computed HERE, on the kept solids, and nowhere else
#
# This is Stage 7's lesson again: look at the solid that was built, not the one
# that was described. A check written in Go over geometry.Tessellate would be
# wrong in both directions, because the tessellator performs no boolean:
#
#   - every bolt hole would report as an interference, because the cut tool is
#     still a solid cylinder standing in the plate. That is the exact false
#     positive look.go had to be taught to ignore, and a checker that complains
#     about correct models drives repairs that damage them.
#   - a part sitting inside a HOLLOW enclosure would report too, when the real
#     answer is zero. Measured: a 5mm cube inside a box shelled to 20mm returns
#     common volume 0.0 here, and would return "70% buried" from bounding boxes.
#
# By the time _build reaches this point the tools have been consumed and the
# booleans are done, so the solids are what the person will export. Both classes
# vanish by construction rather than by an exception list somebody has to
# maintain.
#
# # Broad phase, then narrow phase
#
# An exact common() is an OCCT boolean and is not free; pairs grow as n squared.
# Bounding boxes are nearly free and can only ever be too generous, so they are
# safe as a pre-filter: a pair whose boxes miss cannot share material. Only the
# survivors pay for a boolean.
#
# Measured 2026-09-12 on this machine, parts scattered through a car-sized
# envelope so the overlap rate is higher than a real assembly's:
#
#   parts  pairs   booleans paid for   time
#      10     45                   2   0.006 s
#      28    378                  19   0.047 s
#      60   1770                  62   0.141 s
#     120   7140                 294   0.661 s
#
# Sub-second at four times the largest model this system has built, against turns
# that take forty to a hundred seconds. That is why it runs on every build rather
# than on request: a check a caller has to remember to ask for is a check that is
# off in the one deployment that needed it.
_INTERFERENCE_PAIR_BUDGET = 2000
# Below this, it is tolerance noise rather than interference. Two faces that
# touch have zero common volume in theory and a sliver of it in floating point,
# and a check that reports those would fire on every correctly assembled model.
# Relative, with an absolute floor, because "1 mm3" means something very
# different to a bracket and to a bridge.
_INTERFERENCE_MIN_VOLUME = 1.0
_INTERFERENCE_MIN_FRACTION = 0.001


def _box_of(solid):
    """A solid's axis-aligned bounds as (lo, hi) triples, or None."""
    try:
        b = solid.bounding_box()
        return ((float(b.min.X), float(b.min.Y), float(b.min.Z)),
                (float(b.max.X), float(b.max.Y), float(b.max.Z)))
    except Exception:
        # A solid whose bounds cannot be read is left out of the broad phase
        # rather than paired with everything: it is already in trouble, and
        # the parts around it should not be reported because of it.
        return None


def _boxes(solids):
    """Each solid's axis-aligned bounds, as (lo, hi) triples."""
    return [_box_of(s) for s in solids]


def _volume_of(solid):
    try:
        return float(getattr(solid, "volume", 0.0))
    except Exception:
        return 0.0


def _moved_box(box, location):
    """A local box moved by a placement: the box around its eight moved corners.

    Never tighter than the placed solid's own box — a turned box's box is larger
    than the box — so it can only let an extra pair through to the exact boolean,
    never keep a real one out.
    """
    if box is None:
        return None
    t = location.wrapped.Transformation()
    m = [[t.Value(r, c) for c in (1, 2, 3, 4)] for r in (1, 2, 3)]
    lo, hi = [math.inf] * 3, [-math.inf] * 3
    for x in (box[0][0], box[1][0]):
        for y in (box[0][1], box[1][1]):
            for z in (box[0][2], box[1][2]):
                for r in range(3):
                    v = m[r][0] * x + m[r][1] * y + m[r][2] * z + m[r][3]
                    lo[r] = min(lo[r], v)
                    hi[r] = max(hi[r], v)
    return (tuple(lo), tuple(hi))


def _measures(solids, placed=None):
    """Every kept solid's box and volume.

    # Once per definition (Phase 5, stage V1)

    Reading an OCCT solid's bounds and volume costs about 0.2 ms, and after K2b
    that per-solid read was most of what the interference phase spent: 2 s of a
    10,000-part build. A copy is its shape placed, so the shape is measured once
    and each copy's box is that box moved by its placement; volume does not change
    under a rigid motion. A part a feature changed (placed entry None) is measured
    as the solid it is.
    """
    boxes, volumes, local = [], [], {}
    for i, solid in enumerate(solids):
        p = placed[i] if placed else None
        if p is None:
            boxes.append(_box_of(solid))
            volumes.append(_volume_of(solid))
            continue
        key, location, shape = p
        if key not in local:
            local[key] = (_box_of(shape), _volume_of(shape))
        box, volume = local[key]
        boxes.append(_moved_box(box, location))
        volumes.append(volume)
    return boxes, volumes


# # Each part's volume, centre and box, on request (Phase 5, stage V3)
#
# Mass, centre of gravity and envelope roll up through the tree in Go
# (geometry/mass.go), which knows each part's material. What only the kernel can
# say is where a solid's volume IS. Sent when asked for, not always: 30,000 parts
# are about 3 MB of numbers that every other reader would carry for nothing.
#
# A copy's volume and centre are its shape's, the centre moved by the copy's
# placement — measured once per shape, like the interference check's (see
# _measures). Its box is read from the placed solid itself: a turned box's moved
# box is larger than the box, which a broad phase can afford and an envelope
# cannot.
#
# _PROPERTIES_PER_DEFINITION turns the per-shape path off; it exists so the direct
# measurement stays available as the reference the fence compares against
# (testdata/part_properties.py).
_PROPERTIES_PER_DEFINITION = True


def _centre_of(solid):
    """A solid's centre of volume as (x, y, z), or None."""
    try:
        from build123d import CenterOf

        c = solid.center(CenterOf.MASS)
        return (float(c.X), float(c.Y), float(c.Z))
    except Exception:
        return None


def _moved_point(point, location):
    """A point moved by a placement."""
    if point is None:
        return None
    t = location.wrapped.Transformation()
    x, y, z = point
    return tuple(t.Value(r, 1) * x + t.Value(r, 2) * y + t.Value(r, 3) * z + t.Value(r, 4) for r in (1, 2, 3))


def _properties(solids, ids, placed=None):
    """Each kept solid's id, volume (mm3), centre of volume (mm) and box (mm)."""
    out, local = [], {}
    for i, solid in enumerate(solids):
        p = placed[i] if (_PROPERTIES_PER_DEFINITION and placed) else None
        if p is None:
            volume, centre = _volume_of(solid), _centre_of(solid)
        else:
            key, location, shape = p
            if key not in local:
                local[key] = (_volume_of(shape), _centre_of(shape))
            volume, centre = local[key]
            centre = _moved_point(centre, location)
        box = _box_of(solid)
        out.append({"id": ids[i], "volume": volume,
                    "centroid": list(centre) if centre is not None else None,
                    "bounds": list(box[0]) + list(box[1]) if box is not None else None})
    return out


def _boxes_miss(a, b):
    if a is None or b is None:
        return True
    for i in range(3):
        if a[1][i] <= b[0][i] or b[1][i] <= a[0][i]:
            return True
    return False


# A box longer than this many grid cells on any axis is tested against every box
# instead of being filed in every cell it crosses. See _candidate_pairs.
_GRID_LARGE = 4.0


def _cell(value, size):
    return int(math.floor(value / size))


def _candidate_pairs(boxes):
    """Every pair whose boxes overlap, and how many box tests it took to find them.

    # A grid over all three axes (Phase 5, stage V1)

    Comparing every box with every other is n(n-1)/2 tests: 50 million at 10,000
    parts. K2b swept along one axis, which is a handful of tests per part on an
    assembly long in one direction and about sqrt(n) per part on one spread over a
    plane — 495,000 tests for a 100 x 100 grid
    (docs/spikes/2026-09-15-interference-broad-phase). A uniform grid does not
    care which way the parts spread: each box is filed in the cells it crosses,
    and only boxes sharing a cell are tested. The cell is the median box's longest
    side, so a typical part crosses one or two cells a side.

    It is only a narrower pre-filter. A pair that shares a cell is still tested on
    all three axes with _boxes_miss, so the pairs that come out are exactly the
    pairs comparing everything would find, and the exact boolean after it is
    unchanged.

    # Each pair is tested in one cell only

    Two boxes can share many cells. A pair is tested in the cell holding the corner
    where their overlap would begin — the larger of their two low corners, which is
    inside both boxes whenever they overlap, and so in a cell both were filed in.
    That is exactly one cell, so a pair is never tested, or counted, twice.

    # Boxes much larger than a cell

    A chassis rail filed in every cell it crosses would cost more cells than it has
    neighbours. A box longer than _GRID_LARGE cells is kept out of the grid and
    tested against every box instead; an assembly has few of them.

    # Why the pairs are sorted back into index order

    The pair budget stops the narrow phase part-way through a dense model. Which
    pairs were measured before it stopped depends on the order they arrive in, so
    they arrive in the order comparing every pair would meet them: a truncated
    answer is the same truncated answer it was before any broad phase existed.
    """
    present = [k for k, b in enumerate(boxes) if b is not None]
    if len(present) < 2:
        return [], 0
    longest = sorted(max(boxes[k][1][a] - boxes[k][0][a] for a in range(3)) for k in present)
    cell = max(longest[len(longest) // 2], 1e-6)
    cells, large = {}, []
    for k in present:
        lo, hi = boxes[k]
        if max(hi[a] - lo[a] for a in range(3)) > _GRID_LARGE * cell:
            large.append(k)
            continue
        for cx in range(_cell(lo[0], cell), _cell(hi[0], cell) + 1):
            for cy in range(_cell(lo[1], cell), _cell(hi[1], cell) + 1):
                for cz in range(_cell(lo[2], cell), _cell(hi[2], cell) + 1):
                    cells.setdefault((cx, cy, cz), []).append(k)
    pairs, tests = [], 0
    for home, members in cells.items():
        for m, a in enumerate(members):
            for b in members[m + 1:]:
                if (_cell(max(boxes[a][0][0], boxes[b][0][0]), cell) != home[0]
                        or _cell(max(boxes[a][0][1], boxes[b][0][1]), cell) != home[1]
                        or _cell(max(boxes[a][0][2], boxes[b][0][2]), cell) != home[2]):
                    continue
                tests += 1
                if not _boxes_miss(boxes[a], boxes[b]):
                    pairs.append((a, b) if a < b else (b, a))
    done = set()
    for a in large:
        done.add(a)
        for b in present:
            if b in done:
                continue
            tests += 1
            if not _boxes_miss(boxes[a], boxes[b]):
                pairs.append((a, b) if a < b else (b, a))
    pairs.sort()
    return pairs, tests


# # A clash is measured once per pose (Phase 5, stage V1)
#
# A thousand plates each with the same bolt through the same hole are a thousand
# identical booleans. The common volume of two copies depends only on which two
# shapes they are and where one sits relative to the other, so it is measured
# once per (shape, shape, relative pose) and reused. The budget counts booleans
# PAID FOR, not pairs answered, so a repetitive assembly is checked in full
# where the 2,000-pair budget used to stop it part-way.
#
# A part a feature changed is not a copy of anything and is always measured.
# _INTERFERENCE_CACHE turns reuse off; it exists so the uncached answer stays
# available as the reference the cached one must reproduce
# (testdata/interference_cache.py).
_INTERFERENCE_CACHE = True


def _pose(location):
    """A placement as twelve numbers, rounded so float noise does not split one
    pose into two: rotation to 1e-9, translation to 1e-6 mm."""
    t = location.wrapped.Transformation()
    return tuple(round(t.Value(r, c), 6 if c == 4 else 9) for r in (1, 2, 3) for c in (1, 2, 3, 4))


def _pair_key(placed, shape_ids, i, j):
    """Which two shapes, and where the second sits in the first's frame — the same
    for the pair in either order — or None when either was changed by a feature."""
    pi, pj = placed[i], placed[j]
    if pi is None or pj is None:
        return None
    a, b = shape_ids[pi[0]], shape_ids[pj[0]]
    forward = (a, b, _pose(pi[1].inverse() * pj[1]))
    backward = (b, a, _pose(pj[1].inverse() * pi[1]))
    return min(forward, backward)


def _interferences(solids, ids, labels, placed=None):
    """Pairs of kept solids that share material, worst first.

    placed holds, for each solid, (shape key, location, unplaced shape) when it is
    an untouched copy of a shape built once, or None — see _tessellate.

    Returns the list, whether the budget stopped the search (so a caller never
    reads a truncated answer as a clean one), how many box tests the broad phase
    made, and {"pairs", "booleans", "reused"}: the pairs whose boxes overlap, the
    booleans paid for, and the answers reused from an identical pose.
    """
    boxes, volumes = _measures(solids, placed)
    pairs, box_tests = _candidate_pairs(boxes)

    cached = _INTERFERENCE_CACHE and placed is not None
    shape_ids, cache = {}, {}
    if cached:
        for p in placed:
            if p is not None and p[0] not in shape_ids:
                shape_ids[p[0]] = len(shape_ids)

    found, truncated, booleans, reused = [], False, 0, 0
    for i, j in pairs:
        key = _pair_key(placed, shape_ids, i, j) if cached else None
        if key is not None and key in cache:
            shared = cache[key]
            reused += 1
        else:
            if booleans >= _INTERFERENCE_PAIR_BUDGET:
                truncated = True
                break
            booleans += 1
            try:
                shared = float(getattr(solids[i] & solids[j], "volume", 0.0))
            except Exception:
                # OCCT refusing a boolean is not evidence of interference, and
                # guessing either way would be worse than saying nothing about
                # this pair. The parts are still reported by every other check.
                shared = None
            if key is not None:
                cache[key] = shared
        if shared is None:
            continue
        if volumes[i] <= 0 or volumes[j] <= 0 or shared <= 0:
            # A face has no volume, and "what fraction of it is buried" has
            # no answer. Sections exist to be lofted and are consumed; one
            # that survives is reported by the volume note in _build.
            continue
        smaller = min(volumes[i], volumes[j])
        if shared < _INTERFERENCE_MIN_VOLUME or shared / smaller < _INTERFERENCE_MIN_FRACTION:
            continue
        # The SMALLER solid is reported first, because the fraction is its
        # share and the sentence built from this reads "a is N% inside b".
        # Reporting them in build order would produce "the chassis is 100%
        # inside the master cylinder", which is true of no number here.
        lo, hi = (i, j) if volumes[i] <= volumes[j] else (j, i)
        found.append({"a": ids[lo], "b": ids[hi],
                      "a_label": labels[lo], "b_label": labels[hi],
                      "volume": shared, "fraction": shared / smaller})

    found.sort(key=lambda f: f["fraction"], reverse=True)
    return found, truncated, box_tests, {"pairs": len(pairs), "booleans": booleans, "reused": reused}


# The fields that decide what a solid IS, before it is placed. Everything else a
# solid carries is its identity (id, label) or its place (matrix, position).
#
# Phase 4, stage K1 of docs/plan-2026-09-13-millions-of-parts.md: each distinct
# shape is built ONCE per request and every occurrence of it is a located copy.
# A located copy shares the underlying B-rep (TShape) - measured in
# docs/spikes/2026-09-13-exact-instancing - so it is exact, and a feature applied
# to one occurrence makes a new shape rather than changing the shared one.
# "mirrored" is part of the key: a reflected solid is a different solid.
_SHAPE_KEYS = ("shape", "dims", "outline", "holes", "hole_parents", "path",
               "section_frame", "axis", "step", "mirrored")


def _shape_key(solid):
    return json.dumps({k: solid.get(k) for k in _SHAPE_KEYS}, sort_keys=True, separators=(",", ":"))


def _lap(phases, name, since):
    """Record the seconds since `since` as phase `name`, and return now."""
    now = time.perf_counter()
    phases[name] = now - since
    return now


def _step_document(built, names):
    """An XDE assembly of the kept solids: one shape label per distinct solid, one
    located, named component per occurrence.

    Phase 4, stage K2 of docs/plan-2026-09-13-millions-of-parts.md. This replaces
    Compound(children=...) + export_step. build123d rebuilds the whole
    TopoDS_Compound every time a child is attached, so N children cost ~N²/2
    copies: 13.8 s to assemble 10,000 occurrences, against 0.26 s this way, and
    the file is the same — 1 B-rep per definition, N instances, every name kept,
    exact volume (measured: docs/spikes/2026-09-14-xde-assembly-export).
    Fence: TestKernel_ExportingManyOccurrencesGrowsLinearly.

    Sharing needs no bookkeeping here. K1's located copies share one TShape, and
    XCAF's AddShape returns the label it already holds for a shape it has seen, so
    N copies write ONE B-rep (measured 2026-09-14: keying labels by K1's shape key,
    or giving every occurrence its own key, wrote the same file). A part a feature
    changed is a new TShape and becomes a definition of its own. Each label holds
    the solid with its location stripped, and each component carries the
    occurrence's full location, so the placement is exact whatever location the
    shared shape itself was built with.

    Names match what export_step wrote, so a file does not change with the
    path: the top product is COMPOUND, each instance carries its part's name,
    and a definition's product carries the name of the last occurrence placed.
    """
    doc = TDocStd_Document(TCollection_ExtendedString("XmlOcaf"))
    application = XCAFApp_Application.GetApplication_s()
    application.NewDocument(TCollection_ExtendedString("MDTV-XCAF"), doc)
    application.InitDocument(doc)
    # Millimetres: the kernel's numbers always are (see the unit note in the request).
    XCAFDoc_DocumentTool.SetLengthUnit_s(doc, 0.001)
    tool = XCAFDoc_DocumentTool.ShapeTool_s(doc.Main())
    root = tool.NewShape()
    _label_name(root, "COMPOUND")
    for solid, name in zip(built, names):
        label = tool.AddShape(solid.wrapped.Located(TopLoc_Location()), False, False)
        _label_name(label, name)
        _label_name(tool.AddComponent(root, label, solid.wrapped.Location()), name)
    tool.UpdateAssemblies()
    return doc


def _label_name(label, name):
    TDataStd_Name.Set_s(label, TCollection_ExtendedString(name or ""))


def _write_step(doc, path):
    """Write an XDE document as STEP with the settings export_step used, so the
    file's header, curves and precision do not change with K2."""
    # OCCT prints to the console by default, and this process's stdout is the
    # protocol: a stray line there is an unreadable reply.
    for printer in Message.DefaultMessenger_s().Printers():
        printer.SetTraceLevel(Message_Gravity.Message_Fail)
    writer = STEPCAFControl_Writer(XSControl_WorkSession(), False)
    writer.SetColorMode(True)
    writer.SetLayerMode(True)
    writer.SetNameMode(True)
    header = APIHeaderSection_MakeHeader(writer.Writer().Model())
    if not header.IsDone():
        header = APIHeaderSection_MakeHeader(0)
        header.Apply(writer.Writer().Model())
    header.SetOriginatingSystem(TCollection_HAsciiString("build123d"))
    STEPCAFControl_Controller.Init_s()
    STEPControl_Controller.Init_s()
    Interface_Static.SetIVal_s("write.surfacecurve.mode", 1)
    Interface_Static.SetIVal_s("write.precision.mode", PrecisionMode.AVERAGE.value)
    writer.Transfer(doc, STEPControl_StepModelType.STEPControl_AsIs)
    if writer.Write(path) != IFSelect_ReturnStatus.IFSelect_RetDone:
        raise RuntimeError("the STEP writer could not write the file")


def _build(request):
    solids = request.get("solids") or []
    if not solids:
        return {"ok": False, "error": "no parts to build"}

    # Seconds per phase, reported so a slow build says where its time went and a
    # test can fence one phase without timing the whole build (Phase 4, stage K2).
    phases = {}
    mark = time.perf_counter()
    built, names, ids, skipped = [], [], [], []
    # For each built solid, the key of the shape it is a copy of and where it was
    # placed, so a mesh can be tessellated once per shape (Phase 4, stage K4).
    keys, locations = [], []
    # shape key -> (the built, mirrored shape, or None; why it could not be built)
    built_once = {}
    # Counted where _shape is CALLED, not read back as len(built_once): the cache's
    # own size cannot show the cache being bypassed, because a rebuilt copy lands
    # on the same key and the dict still holds one entry. A mutation drill that
    # rebuilt every occurrence stayed green against len(built_once).
    shape_builds = 0
    for s in solids:
        key = _shape_key(s)
        if key in built_once:
            shape, reason = built_once[key]
            if shape is None:
                skipped.append("%s: %s" % (s.get("label") or s.get("id"), reason))
                continue
            location = _placement(s)
            built.append(location * shape)
            names.append(s.get("label") or s.get("id"))
            ids.append(s.get("id"))
            keys.append(key)
            locations.append(location)
            continue
        shape_builds += 1
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
            built_once[key] = (None, reason)
            skipped.append("%s: %s" % (s.get("label") or s.get("id"), reason))
            continue
        if s.get("mirrored"):
            # Part.Mirrored: reflect the solid's OWN x, then place it — the order the
            # document, the Go mesh and the browser all use. Confirmed against this
            # kernel before it was written: an L drawn from x 0..40 mirrors to -40..0
            # with its volume unchanged, and mirror-then-place matched "reflect local
            # x, rotate, translate" to the micron. A reflection is not in "matrix",
            # which is read as a rotation, so it travels as its own flag.
            #
            # ‼️ The METHOD, not the mirror() operation. Measured against build123d
            # 0.11.1: a part mirrored with the operation is a valid 6000 mm³ solid on its
            # own, but the assembly Compound built from it reports volume 0 — so the
            # file's volume, and every number summed from it, came out wrong. The
            # method, transform_geometry and OCCT's copying transforms all give 6000;
            # all five give the same STEP and the same overlap with a neighbour.
            # Fence: TestKernel_MirrorsAPartBeforePlacingIt.
            # Phase 1, stage D1c of docs/plan-2026-09-13-millions-of-parts.md.
            shape = shape.mirror(Plane.YZ)
        built_once[key] = (shape, None)
        location = _placement(s)
        built.append(location * shape)
        names.append(s.get("label") or s.get("id"))
        ids.append(s.get("id"))
        keys.append(key)
        locations.append(location)

    if not built:
        return {"ok": False, "error": "no part could be built", "skipped": skipped}
    mark = _lap(phases, "shapes", mark)

    # --- features -----------------------------------------------------------
    #
    # Applied to the placed solids, in the document's order. A tool is CONSUMED:
    # it does not also appear as a solid of its own, or the hole would be filled
    # by the thing that made it.
    shapes = dict(zip(ids, built))
    placed = dict(zip(ids, zip(keys, locations)))
    # Every part an operation was applied TO, whether or not it succeeded. Such a
    # solid is no longer a copy of its shape, so its mesh is its own. Marked on the
    # attempt, not the success: a failure part-way through cannot then leave a
    # changed solid drawn as the shape it started from.
    consumed, failed, touched = set(), [], set()
    for op in request.get("operations") or []:
        missing = [n for n in [op["of"]] + list(op.get("with") or []) if n not in shapes]
        if missing:
            # A feature naming a part that could not be built. Reported rather
            # than skipped silently: the assembly is missing an operation
            # somebody asked for.
            failed.append("%s: %s could not be built, so this was not applied"
                          % (op["id"], ", ".join(missing)))
            continue
        touched.add(op["of"])
        try:
            _apply(op, shapes)
        except Exception as exc:
            reason = str(exc).strip() or type(exc).__name__
            failed.append("%s: %s" % (op["id"], _with_a_way_out(op, shapes, reason)))
            continue
        consumed.update(op.get("with") or [])

    kept, kept_names, kept_ids, kept_placed = [], [], [], []
    for part_id, name in zip(ids, names):
        if part_id in consumed:
            continue
        kept.append(shapes[part_id])
        kept_names.append(name)
        # The id travels with the solid so a tessellation can be attributed back
        # to the part the viewport lists. Without it a mesh is one anonymous
        # blob and selecting "Cabin" in the Parts panel can highlight nothing.
        kept_ids.append(part_id)
        if part_id in touched:
            kept_placed.append(None)
        else:
            key, location = placed[part_id]
            kept_placed.append((key, location, built_once[key][0]))
    if not kept:
        return {"ok": False, "error": "every part was consumed as a tool, leaving nothing to export",
                "skipped": skipped, "features_failed": failed}
    built, names, ids = kept, kept_names, kept_ids
    mark = _lap(phases, "features", mark)

    # A compound, not a fused union. Fusing would MERGE parts that touch, and a
    # bracket and the plate it sits on would come back as one body with the seam
    # gone — a claim about assembly that nothing in the document made. The parts
    # stay separate and named, which is what an assembly is.
    #
    # Built in ONE pass, never Compound(children=...): attaching children rebuilds
    # the compound per child, which is quadratic (Phase 4, stage K2; see
    # _step_document). The names travel in the STEP document, the one place a
    # child's label was ever read.
    assembly = Compound(built)

    # The extent, which is the only thing in this reply that can show a part is
    # ORIENTED wrongly. Volume cannot: it is the same however the solid is
    # turned, so a cylinder built along the wrong axis produces an identical
    # number and an identical-looking file. Reported so the caller can assert on
    # it — see TestKernel_ACylinderPointsTheWayThisSystemDrawsIt.
    box = assembly.bounding_box()
    volume = float(getattr(assembly, "volume", 0.0))
    mark = _lap(phases, "assembly", mark)
    # Computed on the KEPT solids, which is the whole reason it is trustworthy —
    # see the note above _interferences. Always, not on request: a check that a
    # caller has to remember to ask for is a check that is off in the one
    # deployment that needed it, and the broad phase makes the usual case free.
    clashes, clash_truncated, box_tests, clash_pairs = _interferences(built, ids, names, kept_placed)
    mark = _lap(phases, "interferences", mark)
    properties = None
    if request.get("properties"):
        properties = _properties(built, ids, kept_placed)
        mark = _lap(phases, "properties", mark)
    out = {
        "shape_builds": shape_builds,
        "ok": True,
        "parts": len(built),
        "interferences": clashes,
        "interferences_truncated": clash_truncated,
        "interference_box_tests": box_tests,
        "interference_pairs": clash_pairs["pairs"],
        "interference_booleans": clash_pairs["booleans"],
        "interference_reused": clash_pairs["reused"],
        "volume": volume,
        "phases": phases,
        "bounds": [float(box.min.X), float(box.min.Y), float(box.min.Z),
                   float(box.max.X), float(box.max.Y), float(box.max.Z)],
        "skipped": skipped,
        "features_failed": failed,
    }
    if properties is not None:
        out["part_properties"] = properties

    fmt = request.get("format")
    if fmt == "mesh":
        out.update(_tessellate(built, ids, names, request, kept_placed))
        mark = _lap(phases, "mesh", mark)
    if fmt == "step":
        # The writer writes a file; its stream form is not used here.
        # Deleted immediately after reading, and created with mkstemp so a
        # concurrent build cannot collide with it.
        fd, path = tempfile.mkstemp(suffix=".step")
        os.close(fd)
        try:
            _write_step(_step_document(built, names), path)
            with open(path, "rb") as fh:
                out["step"] = base64.b64encode(fh.read()).decode("ascii")
        finally:
            try:
                os.unlink(path)
            except OSError:
                pass
        # "phases" in out is this same dict, so a lap recorded now is in the reply.
        mark = _lap(phases, "export", mark)
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
