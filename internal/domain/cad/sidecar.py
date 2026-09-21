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
import copy
import gc
import heapq
import json
import math
import os
import struct
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
        import_step, offset, thicken,
    )
    # Edge rules decided by OCCT itself (looks designed, stage B2; see _EDGE_RULES).
    from OCP.BRepBuilderAPI import BRepBuilderAPI_MakeVertex
    from OCP.BRepExtrema import BRepExtrema_DistShapeShape
    from OCP.ChFi3d import ChFi3d
    from OCP.ChFiDS import ChFiDS_TypeOfConcavity
    from OCP.TopAbs import TopAbs_EDGE, TopAbs_FACE
    from OCP.TopExp import TopExp
    from OCP.TopoDS import TopoDS
    from OCP.TopTools import TopTools_IndexedDataMapOfShapeListOfShape, TopTools_IndexedMapOfShape
    # Per-vertex normals from the surface itself (stage A5; see _tessellate_once).
    from OCP.BRep import BRep_Tool
    from OCP.BRepLib import BRepLib_ToolTriangulatedShape
    from OCP.BRepMesh import BRepMesh_IncrementalMesh
    from OCP.TopAbs import TopAbs_Orientation
    # A located copy without the B-rep copy Shape.moved makes and discards; see _located.
    from build123d.topology.shape_core import Shape, downcast, shapetype
    from OCP.gp import gp_Ax3, gp_Pnt, gp_Trsf
    # A placed copy's box read with the one OCCT call build123d makes; see _properties.
    from OCP.Bnd import Bnd_Box
    from OCP.BRepBndLib import BRepBndLib
    from OCP.BRepTools import BRepTools
    # What a manufacturability check measures, and a named section's properties
    # (issue 6): a ray through the material, a face's own normal, a surface's and a
    # curve's kind, and the area and inertia of a planar cut. See _manufacturability.
    from OCP.BRepAdaptor import BRepAdaptor_Curve, BRepAdaptor_Surface
    from OCP.BRepGProp import BRepGProp, BRepGProp_Face
    from OCP.BRepIntCurveSurface import BRepIntCurveSurface_Inter
    from OCP.GeomAbs import GeomAbs_Circle, GeomAbs_Cylinder
    from OCP.GProp import GProp_GProps
    from OCP.gp import gp_Dir, gp_Lin, gp_Vec
    # The STEP writer's own pieces, used directly rather than through build123d's
    # export_step, which only accepts a Compound(children=...) — see _step_document.
    from OCP.APIHeaderSection import APIHeaderSection_MakeHeader
    from OCP.IFSelect import IFSelect_ReturnStatus
    from OCP.Interface import Interface_HArray1OfHAsciiString, Interface_Static
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

# numpy is a hard dependency of build123d 0.11.1 (its Requires line names it, and
# scipy, scikit-learn, ezdxf and svgpathtools all pull it in), so a kernel that
# imported build123d above has it. It is still asked for separately and guarded,
# because nothing else in this file needs it: the interference check's narrow phase
# is faster one array at a time than one pair at a time (see _bulk_keys), and
# without numpy that path is simply not taken and the per-pair loop answers instead.
try:
    import numpy as _np
except Exception:  # pragma: no cover - the per-pair path answers exactly the same
    _np = None

# manifold3d builds the MESH-ONLY parts (see _lattice_mesh) and nothing else: it
# never touches a solid OCCT built. Guarded, so a kernel without it still builds
# every exact part and refuses each mesh-only part by name.
try:
    import manifold3d as _m3
except Exception:  # pragma: no cover - reported per part, never fatal
    _m3 = None


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


# Place each occurrence without the work build123d does and throws away: a B-rep
# copy per located copy (_located), and a Plane per placement (_placement).
#
# # Why (next scale walls)
#
# Measured 2026-09-15 on the airframe barrel at 90,880 occurrences
# (docs/spikes/2026-09-15-next-scale-walls): the shapes phase was 10.0 s of
# _placement (110 us each) and 10.0 s of `location * shape`, 5.9 s of it
# BRepBuilderAPI_Copy. Shape.moved deep-copies the shape, which copies its whole
# B-rep, then replaces the copy's TopoDS_Shape with the original's, moved — so the
# copy is made and dropped once per occurrence. K1's sharing never used it.
#
# Both replacements produce the SAME objects, not close ones: the same class and
# attributes, the same TShape, and a location equal bit for bit (fence:
# TestKernel_APlacedCopyIsTheCopyBuild123dMade). Off, the build123d calls run as
# before; the switch exists so that reference stays available.
_PLACE_WITHOUT_COPYING = True


# # An occurrence's origin as a gp_Pnt, without a build123d Vector (kernel last walls)
#
# Priced by #146 (docs/spikes/2026-09-17-one-million-after, micro_placement.py):
# `Vector(*position).to_pnt()` is 4.3 us, `gp_Pnt(*position)` 0.8 us, same bits. Read
# from build123d 0.11.1 (geometry.py): given three ints or floats, Vector.__init__
# builds `gp_Vec(x, y, z)` from them unchanged and to_pnt() is `gp_Pnt(vec.XYZ())`, so
# the three doubles in the point are the three the caller passed, either way. Taken
# only for exactly three ints or floats — Vector pads a shorter list with zeros and
# accepts other things, and anything else still goes through it.
# _PLACE_POINT_DIRECT = False restores the Vector; testdata/placed_copies.py compares
# every placement against build123d's own, bit for bit.
_PLACE_POINT_DIRECT = True


def _origin_point(position):
    """`Vector(*position).to_pnt()`."""
    if _PLACE_POINT_DIRECT and len(position) == 3:
        x, y, z = position
        if type(x) in (float, int) and type(y) in (float, int) and type(z) in (float, int):
            return gp_Pnt(x, y, z)
    return Vector(*position).to_pnt()


def _placement(solid, frames=None):
    """The part's frame, from the matrix the caller computed.

    x_dir and z_dir are the matrix's first and third COLUMNS — where the frame's
    own x and z axes end up. Reading rows instead would apply the inverse
    rotation, which is wrong in a way that looks plausible for symmetric parts
    and only shows on the asymmetric ones.

    With `frames` (a dict for one build), each distinct matrix builds its Plane
    ONCE, and every occurrence with that matrix repeats only the last step
    Location(plane) takes: an axis system at the occurrence's origin with the
    Plane's own directions, set as a transformation and inverted. The directions a
    Plane holds do not depend on its origin, so the transformation is the one the
    Plane would give, to the bit. Keyed by repr, which tells -0.0 from 0.
    """
    m = solid["matrix"]
    position = solid["position"]
    if frames is None or not _PLACE_WITHOUT_COPYING:
        origin = Vector(*position)
        return Location(Plane(origin=origin, x_dir=Vector(m[0], m[3], m[6]), z_dir=Vector(m[2], m[5], m[8])))
    key = repr(m)
    axes = frames.get(key)
    if axes is None:
        plane = Plane(origin=Vector(*position), x_dir=Vector(m[0], m[3], m[6]), z_dir=Vector(m[2], m[5], m[8]))
        axes = frames[key] = (plane.z_dir.to_dir(), plane.x_dir.to_dir())
    trsf = gp_Trsf()
    trsf.SetTransformation(gp_Ax3(_origin_point(position), axes[0], axes[1]))
    trsf.Invert()
    return _location_of(TopLoc_Location(trsf))


# # A Location around a TopLoc_Location, without parsing nine keyword arguments
#
# Measured 2026-09-15 (docs/spikes/2026-09-15-last-hot-spots): `Location(top_loc)` is
# 2.4 us of _placement's 10.6, and 181,761 of them were 0.9 s of own time at 90,880
# occurrences — because Location.__init__ pops nine kwargs, runs four isinstance
# checks and builds a gp_Trsf it then throws away, whatever it was given.
#
# Read from build123d 0.11.1 (geometry.py): given a TopLoc_Location, __init__ sets
# exactly two instance attributes — `location_index = 0` and `_wrapped = top_loc` —
# and nothing else; `wrapped` is a read-only property over `_wrapped`. So the object
# below is that object, attribute for attribute, with the discarded gp_Trsf and the
# keyword parsing skipped. Its class is Location, as the constructor's is.
#
# ‼️ It is NOT a general replacement for Location(): it is correct only for a
# TopLoc_Location argument, which is the only thing _placement passes.
# _LOCATION_WITHOUT_INIT = False restores the constructor as the reference
# testdata/placed_copies.py compares every placement against.
_LOCATION_WITHOUT_INIT = True


def _location_of(top_loc):
    """build123d's `Location(top_loc)`, without its keyword parsing."""
    if not _LOCATION_WITHOUT_INIT:
        return Location(top_loc)
    out = Location.__new__(Location)
    out.location_index = 0
    out._wrapped = top_loc
    return out


# # A placed copy's attributes without a deepcopy per occurrence (last hot spots)
#
# Profiled 2026-09-15 on the airframe barrel (docs/spikes/2026-09-15-last-hot-spots):
# _located was 2.4 s of the 90,880-occurrence shapes phase, 14 us an occurrence, and
# most of it was the `copy.deepcopy` loop below — 1,265,934 deepcopy calls at 90k, 12
# an occurrence. The single worst attribute is a shape's `rotation`: Location's own
# __deepcopy__ IGNORES the memo and rebuilds a Location from its transformation
# (build123d 0.11.1, geometry.py), which is 0.3 s of own time and 1.1 s cumulative at
# 90k — paid once per occurrence to produce, every time, the same thing.
#
# A definition's attributes do not change between its occurrences, so what deepcopy
# will DO to each of them is decided once per definition and replayed per occurrence:
#
#   - an immutable atomic (int, float, str, bool, None, bytes, complex) is what
#     copy._deepcopy_atomic returns: the same object, assigned straight across;
#   - a Location (a Box's `rotation` is a Rotation, which is one) becomes
#     `Location(value.wrapped.Transformation())` — exactly Location.__deepcopy__,
#     including that a Rotation comes back a plain Location, and a FRESH object per
#     copy, never the definition's own;
#   - an empty dict or list becomes a new empty one of the same type, which is what
#     _deepcopy_dict and _deepcopy_list give;
#   - anything else is ASKED, once, whether deepcopy returns it unchanged
#     (`copy.deepcopy(value, {}) is value`) and assigned across when it does. That is
#     how `align` — a tuple of Align enum members on every cylinder, cone and sphere —
#     is carried without a copy per occurrence, without this code having to know the
#     type; a tuple holding something mutable answers no and is copied;
#   - ‼️ anything else falls back to `copy.deepcopy(value, memo)`, so this is an
#     optimization of the cases it recognizes and never a claim about the rest. A
#     definition carrying an attribute not listed above is copied exactly as before,
#     and _located_fallbacks counts it so a fence can see it happen.
#
# The attributes are set in the same order, so the copy's __dict__ is ordered as
# build123d's is, and `joints` is re-parented at the same point in the loop.
#
# _PLACE_WITHOUT_DEEPCOPY = False restores the deepcopy loop, kept as the reference
# testdata/placed_copies.py compares every attribute of every copy against.
_PLACE_WITHOUT_DEEPCOPY = True
# Attributes the plan below copies by assignment, because deepcopy returns them
# unchanged. copy._deepcopy_atomic's types, less the ones a shape cannot hold.
_ATOMIC = (int, float, bool, str, bytes, complex, type(None))
# How many attributes the plan did not recognize and handed to copy.deepcopy. A
# count, so a fence can show the fast path is the path taken (and a drill that
# breaks the classification shows up as this rising).
_located_fallbacks = 0

# # The moved shape's cast chosen once per definition (kernel last walls)
#
# Priced by #146 (micro_placement.py): downcast(Moved) 4.6 us, the definition's own
# cast 1.7 us. downcast() looks up Shape.downcast_LUT[shapetype(obj)] per call, and a
# moved shape's ShapeType is its definition's — Moved changes the location, never the
# type — so the cast is looked up once per definition and kept in the build's `plans`
# dict (under ("cast", id(shape)), beside the attribute plans and for the same reason:
# never module-level). The same function is applied to the same TopoDS_Shape, so the
# result is the object downcast() returns. _LOCATED_OWN_CAST = False restores downcast().
_LOCATED_OWN_CAST = True


def _attribute_plan(shape):
    """What deepcopy does to each of a definition's attributes, worked out once.

    `how` is "parent" (topo_parent, carried not copied), "moved" (the shape itself,
    which the caller seeds into the memo), "same" (an atomic), "location", "dict",
    "list", or "deep" (anything else: copy.deepcopy).
    """
    plan = []
    for key, value in shape.__dict__.items():
        if key == "topo_parent":
            plan.append((key, "parent", value))
        elif key == "_wrapped":
            # deepcopy of this hits memo[id(shape.wrapped)], which IS the moved shape.
            plan.append((key, "moved", None))
        elif isinstance(value, _ATOMIC):
            plan.append((key, "same", value))
        elif isinstance(value, Location):
            plan.append((key, "location", value))
        elif type(value) is dict and not value:
            plan.append((key, "dict", value))
        elif type(value) is list and not value:
            plan.append((key, "list", value))
        elif copy.deepcopy(value, {}) is value:
            # ‼️ ASKED, not assumed. deepcopy returns the object itself for anything
            # it treats as immutable, and that reaches further than the atomics
            # above: a Cylinder, Cone and Sphere each carry `align`, a tuple of Align
            # enum members, and _deepcopy_tuple hands back the ORIGINAL tuple when
            # every element deepcopies to itself, which an Enum member does. Rather
            # than list more types and be wrong about the next one, the plan runs the
            # real deepcopy once per definition and assigns across only when the
            # answer is the same object. A tuple holding something mutable fails this
            # and is copied below, as it must be.
            plan.append((key, "same", value))
        else:
            plan.append((key, "deep", value))
    return plan


def _located(shape, location, plans=None):
    """`location * shape` — Shape.moved — without the B-rep copy it discards, and
    without a deepcopy of every attribute per occurrence.

    Shape.__deepcopy__ copies every attribute and, for the TopoDS_Shape, makes a
    BRepBuilderAPI_Copy; moved then overwrites that copy with the original moved.
    This is the same loop with the moved shape put where the copy would have gone,
    and with what the loop does to each attribute decided once per definition
    (see _attribute_plan).

    ‼️ `plans` is a dict for ONE build, keyed by id(shape). It is never module-level:
    this process outlives a request, CPython reuses the id of a collected object, and
    a plan carries the definition's own attribute VALUES — so a stale hit would place
    another definition's attributes on a copy, silently. Within one build every
    definition is held alive by `built_once`, so the ids are stable and distinct.
    Without it, the plan is worked out per occurrence, which is correct and slower.
    """
    global _located_fallbacks
    if not _PLACE_WITHOUT_COPYING:
        return location * shape
    cls = shape.__class__
    out = cls.__new__(cls)
    if _LOCATED_OWN_CAST and plans is not None:
        cast = plans.get(("cast", id(shape)))
        if cast is None:
            cast = plans[("cast", id(shape))] = Shape.downcast_LUT[shapetype(shape.wrapped)]
        moved = cast(shape.wrapped.Moved(location.wrapped))
    else:
        moved = downcast(shape.wrapped.Moved(location.wrapped))
    memo = {id(shape): out, id(shape.wrapped): moved}
    if not _PLACE_WITHOUT_DEEPCOPY:
        for key, value in shape.__dict__.items():
            if key == "topo_parent":
                out.topo_parent = value
            else:
                setattr(out, key, copy.deepcopy(value, memo))
            if key == "joints":
                for joint in out.joints.values():
                    joint.parent = out
        out.wrapped = moved
        return out
    plan = None if plans is None else plans.get(id(shape))
    if plan is None:
        plan = _attribute_plan(shape)
        if plans is not None:
            plans[id(shape)] = plan
    for key, how, value in plan:
        if how == "same":
            setattr(out, key, value)
        elif how == "moved":
            setattr(out, key, moved)
        elif how == "location":
            # Location.__deepcopy__ is `Location(self.wrapped.Transformation())`,
            # which takes __init__'s gp_trsf branch and ends at
            # `_wrapped = TopLoc_Location(trsf)` with that same transformation. So
            # this is that object, without the keyword parsing (see _location_of).
            setattr(out, key, _location_of(TopLoc_Location(value.wrapped.Transformation())))
        elif how == "dict":
            setattr(out, key, {})
        elif how == "list":
            setattr(out, key, [])
        elif how == "parent":
            out.topo_parent = value
        else:
            _located_fallbacks += 1
            setattr(out, key, copy.deepcopy(value, memo))
        if key == "joints":
            for joint in out.joints.values():
                joint.parent = out
    out.wrapped = moved
    return out


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


def _edge_faces(shape):
    """Every edge of a shape mapped to the faces it bounds (OCCT's ancestor map)."""
    faces = TopTools_IndexedDataMapOfShapeListOfShape()
    TopExp.MapShapesAndAncestors_s(shape.wrapped, TopAbs_EDGE, TopAbs_FACE, faces)
    return faces


def _two_faces(faces, edge):
    """The two distinct faces an edge joins, or None for a seam or a free edge.

    A seam — the line where a cylinder's one face closes on itself — lists the
    same face twice, so faces are told apart by IsSame and not counted.
    """
    found = []
    for f in faces.FindFromKey(edge.wrapped):
        f = TopoDS.Face_s(f)
        if not any(f.IsSame(g) for g in found):
            found.append(f)
    return found if len(found) == 2 else None


def _connection(faces, edge):
    """OCCT's own answer to "is this edge convex": ChFi3d::DefineConnectType, the
    question its fillet builder asks. None for a seam or a free edge."""
    pair = _two_faces(faces, edge)
    if pair is None:
        return None
    return ChFi3d.DefineConnectType_s(edge.wrapped, pair[0], pair[1], 1e-6, True)


def _inner_edges(shape):
    """The edges on some face's INNER boundary: the rims of holes through it."""
    inner = TopTools_IndexedMapOfShape()
    for face in shape.faces():
        for wire in face.inner_wires():
            for edge in wire.edges():
                inner.Add(edge.wrapped)
    return inner


def _on_surface(shape, points, tol):
    """Every point lies on the shape's boundary (within tol)."""
    for p in points:
        vertex = BRepBuilderAPI_MakeVertex(gp_Pnt(p.X, p.Y, p.Z)).Vertex()
        d = BRepExtrema_DistShapeShape(vertex, shape.wrapped)
        if not d.IsDone() or d.Value() > tol:
            return False
    return True


def _joins(shape, op, history):
    """The seam a fuse left: edges on the surface of the part as it was placed AND
    on the surface of a part an earlier fuse welded into it. Sampled at three
    points along each edge, so an edge that only touches the other part at an
    end is not taken for one that runs along it."""
    history = history or {}
    placed = history.get("placed") or {}
    target = placed.get(op["of"])
    tools = [placed[t] for t in (history.get("fused") or {}).get(op["of"], []) if t in placed]
    if target is None or not tools:
        return []
    tol = max(1e-6, 1e-7 * float(shape.bounding_box().diagonal))
    out = []
    for edge in shape.edges():
        points = [edge.position_at(u) for u in (0.25, 0.5, 0.75)]
        if _on_surface(target, points, tol) and any(_on_surface(t, points, tol) for t in tools):
            out.append(edge)
    return out


def _by_connection(kind):
    def select(shape, op, history):
        faces = _edge_faces(shape)
        return [e for e in shape.edges() if _connection(faces, e) == kind]
    return select


def _outer(shape, op, history):
    faces, inner = _edge_faces(shape), _inner_edges(shape)
    return [e for e in shape.edges()
            if not inner.Contains(e.wrapped) and _two_faces(faces, e) is not None]


def _holes(shape, op, history):
    inner = _inner_edges(shape)
    return [e for e in shape.edges() if inner.Contains(e.wrapped)]


def _longer(shape, op, history):
    # Strictly longer: an edge exactly edge_length long is not "longer than" it.
    limit = float(op.get("edge_length") or 0.0)
    return [e for e in shape.edges() if float(e.length) > limit * (1 + 1e-9)]


# Which edges a fillet or chamfer touches, by RULE and never by index.
#
# An index selects a different edge the moment a parameter changes, which is the
# failure mode that makes naive parametric scripts break on their second run
# (docs/spikes/2026-09-05-parametric-cad-kernel/). Y is up in this system, so
# "vertical" is the Y axis.
#
# ‼️ The names are geometry.EdgeRules', exactly: Go validates a document against
# that table and teaches the contract from it, and this is the third reader of
# the same list. TestTheKernelSelectsEdgesByExactlyTheRulesGoValidates reads this
# dict's keys out of this file and compares them (looks designed, stage B2).
_EDGE_RULES = {
    "all": lambda shape, op, history: shape.edges(),
    "vertical": lambda shape, op, history: shape.edges().filter_by(Axis.Y),
    "horizontal": lambda shape, op, history: (shape.edges().filter_by(Axis.X)
                                              + shape.edges().filter_by(Axis.Z)),
    "top": lambda shape, op, history: shape.edges().group_by(Axis.Y)[-1],
    "bottom": lambda shape, op, history: shape.edges().group_by(Axis.Y)[0],
    "convex": _by_connection(ChFiDS_TypeOfConcavity.ChFiDS_Convex),
    "concave": _by_connection(ChFiDS_TypeOfConcavity.ChFiDS_Concave),
    "outer": _outer,
    "holes": _holes,
    "longer": _longer,
    "joins": _joins,
}


def _edges(shape, op, history=None):
    """The edges op's rule selects on shape. An unknown name never reaches here:
    Go refuses it against the same table."""
    return _EDGE_RULES[op.get("edges") or "all"](shape, op, history)


# The faces a shell may leave open: geometry.OpenFaceRules, exactly, for the
# reason _EDGE_RULES gives. Each is (axis, which end), in the assembly's frame.
_OPEN_FACES = {
    "top": (Axis.Y, -1),
    "bottom": (Axis.Y, 0),
    "right": (Axis.X, -1),
    "left": (Axis.X, 0),
    "front": (Axis.Z, -1),
    "back": (Axis.Z, 0),
}


def _apply(op, shapes, history=None, report=None):
    """One operation, in place in the shapes dict.

    Order is the document's. A feature reads what the features before it left
    behind, which is what makes "cut the holes, then round what is left" mean
    something different from the other way round.

    history holds each part as it was placed ("placed") and which parts earlier
    fuses welded into which ("fused"), for the "joins" edge rule. report gathers
    what a build reply says about the features beyond success and failure: how
    many edges each round selected ("edges") and every round built smaller or
    left partly square ("reduced").
    """
    target = shapes[op["of"]]
    kind = op["op"]

    if kind == "cut" and len(op.get("with") or []) > 1:
        # ONE boolean with every tool, not one per tool (looks designed, stage B6).
        # Cut one at a time, each cut re-splits a target that already carries every
        # earlier hole: a 1,000-hole perforation took 288 s this way and 1.6-8.1 s
        # as one boolean, with the same exact volume (measured 2026-09-18,
        # docs/spikes/2026-09-18-kernel-vocabulary). geometry.MaxCutTools bounds N.
        # Fence: TestKernel_APerforationIsCutAsOneBoolean.
        shapes[op["of"]] = target.cut(*[shapes[t] for t in op["with"]])
        return
    if kind in ("cut", "fuse"):
        for tool_id in op.get("with") or []:
            tool = shapes[tool_id]
            target = (target - tool) if kind == "cut" else (target + tool)
        shapes[op["of"]] = target
        if kind == "fuse" and history is not None:
            history.setdefault("fused", {}).setdefault(op["of"], []).extend(op.get("with") or [])
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

    if kind == "shell":
        shapes[op["of"]] = _shelled(target, op)
        return
    if kind == "thicken":
        shapes[op["of"]] = _thickened(target, op)
        return

    selected = _edges(target, op, history)
    if report is not None:
        report.setdefault("edges", {})[op["id"]] = len(selected)
    if not selected:
        # Nothing to round is not a failure: a rule can legitimately select no
        # edge (a sphere has none vertical). Reported so a person is not left
        # wondering why the fillet they asked for is not there.
        raise ValueError("the %s selected no %s edges" % (kind, op.get("edges") or "all"))
    shapes[op["of"]] = _rounded(kind, target, selected, op, report)


def _shelled(target, op):
    """A solid hollowed to walls of op["thickness"], measured inward, open at the
    faces op["open"] names (looks designed, stage B4).

    Inward, so the outside stays the size the document says: a 100 mm housing
    shelled 3 mm is still 100 mm across. At least one face is open — Go refuses
    a shell with none, because build123d 0.11.1 returns a closed shell as one
    solid whose volume is the CAVITY's (measured 2026-09-18).
    """
    faces = []
    for name in op.get("open") or []:
        axis, end = _OPEN_FACES[name]
        for face in target.faces().group_by(axis)[end]:
            if not any(face.wrapped.IsSame(f.wrapped) for f in faces):
                faces.append(face)
    if not faces:
        raise ValueError("the shell found no face to leave open")
    result = offset(target, amount=-float(op["thickness"]), openings=faces)
    volume = float(getattr(result, "volume", 0.0))
    # ‼️ A wall thicker than the part is thin does not raise: OCCT returns a
    # shape, sometimes an empty one and sometimes the part unchanged. Either is a
    # shell nobody asked for, so it is refused with the reason.
    if volume <= 0.0 or volume >= float(target.volume) * (1 - 1e-9):
        raise ValueError("a %g mm wall does not fit inside this part, so it cannot be shelled; "
                         "use a thinner wall" % op["thickness"])
    return result


def _thickened(target, op):
    """A surface grown into a solid skin op["thickness"] thick, CENTRED on the
    surface as an extrusion is centred on its outline (looks designed, stage B4)."""
    faces = target.faces()
    if len(faces) != 1:
        raise ValueError("a thicken needs one surface; this part has %d faces" % len(faces))
    result = thicken(faces[0], amount=float(op["thickness"]) / 2.0, both=True)
    if float(getattr(result, "volume", 0.0)) <= 0.0:
        raise ValueError("the surface could not be thickened")
    return result


# The sizes a fillet or chamfer is retried at when OCCT refuses the one asked for
# (looks designed, stage B1). A refused round used to drop the whole feature and
# leave every edge it named square; half and a quarter of the size are almost
# always a rounded edge somebody would rather have. Never silently: every edge
# group built smaller, and every one left square, is REPORTED ("features_reduced"),
# because a rounded edge a reader thinks is R5 and is R1.25 is a claim about the
# part.
_ROUND_RETRY = (1.0, 0.5, 0.25)
# Past this many edge groups a feature is retried as ONE group. Each group costs
# up to three OCCT attempts on a path that has already failed, and a perforated
# panel's "holes" rule can select thousands of rims.
_ROUND_GROUP_LIMIT = 64


def _round(kind, edges, size):
    if kind == "fillet":
        return fillet(edges, radius=size)
    return chamfer(edges, length=size)


def _edge_groups(edges):
    """The selected edges as connected chains: edges sharing a vertex are one group.

    A group is what one refused fillet takes down with it. Filleted separately, a
    fin too thin for the radius loses its own edges and not the plate's corners.
    """
    index = TopTools_IndexedMapOfShape()
    parent = list(range(len(edges)))

    def find(i):
        while parent[i] != i:
            parent[i] = parent[parent[i]]
            i = parent[i]
        return i

    owner = {}
    for i, edge in enumerate(edges):
        for v in edge.vertices():
            k = index.Add(v.wrapped)
            if k in owner:
                a, b = find(owner[k]), find(i)
                if a != b:
                    parent[b] = a
            else:
                owner[k] = i
    groups = {}
    for i in range(len(edges)):
        groups.setdefault(find(i), []).append(edges[i])
    return list(groups.values())


def _edge_key(edge):
    c = edge.center()
    return (round(float(c.X), 5), round(float(c.Y), 5), round(float(c.Z), 5),
            round(float(edge.length), 5))


def _where(edges):
    c = edges[0].center()
    return "%d edge%s near (%g, %g, %g)" % (len(edges), "" if len(edges) == 1 else "s",
                                             round(float(c.X), 1), round(float(c.Y), 1),
                                             round(float(c.Z), 1))


def _rounded(kind, target, selected, op, report):
    """Round the selected edges, and when OCCT refuses, save what can be saved.

    First all at once at the size asked for — the path every round that fits
    takes, unchanged. When that is refused, each connected group of edges is
    rounded on its own, at the size asked for, then half, then a quarter; a group
    no size fits is left square and the rest are still rounded. Each group is
    found again on the shape the groups before it left, by where it is: a group
    whose edges an earlier round consumed is reported as left square.

    Raises only when NO group could be rounded, so the feature is reported as
    failed exactly as before, with the largest size that fits.
    """
    size = float(op["radius"])
    try:
        return _round(kind, selected, size)
    except Exception:
        pass
    groups = _edge_groups(list(selected))
    if len(groups) > _ROUND_GROUP_LIMIT:
        groups = [list(selected)]
    shape, reduced, square, applied = target, [], [], 0
    for group in groups:
        keys = {_edge_key(e) for e in group}
        here = [e for e in shape.edges() if _edge_key(e) in keys]
        if len(here) != len(group):
            square.append("%s (an earlier group's round changed them)" % _where(group))
            continue
        done = False
        for factor in _ROUND_RETRY:
            try:
                shape = _round(kind, here, size * factor)
            except Exception:
                continue
            done = True
            applied += 1
            if factor != 1.0:
                reduced.append("%s at %g mm (%g× the %g mm asked for)"
                               % (_where(group), size * factor, factor, size))
            break
        if not done:
            square.append("%s left square: no %s of %s mm builds there"
                          % (_where(group), kind,
                             ", ".join("%g" % (size * f) for f in _ROUND_RETRY)))
    if applied == 0:
        raise ValueError("no %s of %s mm builds on any of these %d edge group(s)"
                         % (kind, ", ".join("%g" % (size * f) for f in _ROUND_RETRY), len(groups)))
    if (reduced or square) and report is not None:
        lines = reduced + square
        more = ""
        if len(lines) > 6:
            lines, more = lines[:6], "; and %d more" % (len(lines) - 6)
        report.setdefault("reduced", []).append(
            "%s: the %s of %g mm did not build on all %d edge group(s), so FORGE rounded what it "
            "could and says where — %s%s" % (op["id"], kind, size, len(groups), "; ".join(lines), more))
    return shape


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


def _with_a_way_out(op, shapes, reason, history=None):
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
        selected = _edges(shapes[op["of"]], op, history)
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

# # The angular limit (looks designed, stage A5)
#
# The largest angle, in radians, one facet may turn through on a curved face. It
# was always there — build123d's tessellate() defaults to 0.1 — but implicit, never
# reported, and never coarsened: measured 2026-09-18 (build123d 0.11.1), a
# cylinder of radius 5, 50 or 500 mm is 500 triangles at every deflection from
# R/1000 to R/10, because 0.1 rad is the limit that binds. So the budget search
# above, which coarsened only the deflection, could not shrink a mesh of many
# curved parts at all: six tries, the same triangles, and "could not be
# tessellated". Now it is named, sent back as mesh_angular, and coarsened with
# the deflection — never past _MESH_ANGLE_MAX, a facet of 45°, beyond which a
# cylinder is an octagon and the smooth normals below cannot hide it.
_MESH_ANGLE = 0.1
_MESH_ANGLE_MAX = 0.785

# Normals are rounded to this many decimals on the wire: 1e-4 of a unit vector
# is far below what shading can show, and a full double is 18 characters of JSON
# per component (measured size in docs/spikes/2026-09-18-kernel-vocabulary).
_NORMAL_DECIMALS = 4
# _MESH_NORMALS = False sends meshes exactly as before this stage.
_MESH_NORMALS = True


def _face_normals(face, poly, trsf, reverse):
    """One unit normal per node of face's triangulation, from the SURFACE.

    OCCT evaluates the face's own surface at each node's UV
    (BRepLib_ToolTriangulatedShape::ComputeNormals), so a node on a cylinder
    gets the cylinder's normal there, not an average of the facets round it —
    which is what makes a curved face shade smoothly. Nodes belong to ONE face
    (tessellate() gives every face its own), so where two faces meet at a hard
    edge each keeps its own normal and the edge stays sharp.
    """
    n = poly.NbNodes()
    try:
        BRepLib_ToolTriangulatedShape.ComputeNormals_s(face.wrapped, poly)
        out = []
        for i in range(1, n + 1):
            d = poly.Normal(i).Transformed(trsf)
            s = -1.0 if reverse else 1.0
            out.extend((s * d.X(), s * d.Y(), s * d.Z()))
        return out
    except Exception:
        # A face OCCT cannot evaluate (a degenerate patch): the facets' own
        # normals averaged per node, which is flat shading where that is all
        # there is to go on.
        acc = [0.0] * (3 * n)
        nodes = [poly.Node(i).Transformed(trsf) for i in range(1, n + 1)]
        for t in poly.Triangles():
            a, b, c = t.Value(1) - 1, t.Value(2) - 1, t.Value(3) - 1
            if reverse:
                b, c = c, b
            pa, pb, pc = nodes[a], nodes[b], nodes[c]
            ux, uy, uz = pb.X() - pa.X(), pb.Y() - pa.Y(), pb.Z() - pa.Z()
            vx, vy, vz = pc.X() - pa.X(), pc.Y() - pa.Y(), pc.Z() - pa.Z()
            nx, ny, nz = uy * vz - uz * vy, uz * vx - ux * vz, ux * vy - uy * vx
            for k in (a, b, c):
                acc[3 * k] += nx
                acc[3 * k + 1] += ny
                acc[3 * k + 2] += nz
        for k in range(n):
            x, y, z = acc[3 * k], acc[3 * k + 1], acc[3 * k + 2]
            m = math.sqrt(x * x + y * y + z * z) or 1.0
            acc[3 * k], acc[3 * k + 1], acc[3 * k + 2] = x / m, y / m, z / m
        return acc


def _tessellate_solid(solid, deflection, angle):
    """build123d's Shape.tessellate, node for node and triangle for triangle, plus
    each node's normal (stage A5). Written out rather than called so the normals
    come from the same triangulation the triangles do.

    ‼️ Cleaned, then meshed, every time. build123d's mesh() meshes only "if none
    exists": it keeps any triangulation already FINER than asked. So the budget
    search's second try, at 2.5x the deflection, found the first try's mesh finer
    than that and kept it — measured 2026-09-18, a box with a bore is 520
    triangles at deflection 0.01 and still 520 when asked again at 10, 68 once
    cleaned. Coarsening never coarsened anything; a model over the budget failed
    after six identical tries while mesh_simplified said it had been simplified.
    Fence: TestKernel_AMeshOverTheBudgetIsReallyCoarsened.
    """
    BRepTools.Clean_s(solid.wrapped)
    BRepMesh_IncrementalMesh(solid.wrapped, deflection, True, angle, True)
    flat, idx, normals, offset_ = [], [], [], 0
    for face in solid.faces():
        loc = TopLoc_Location()
        poly = BRep_Tool.Triangulation_s(face.wrapped, loc)
        if poly is None:
            continue
        trsf = loc.Transformation()
        reverse = face.wrapped.Orientation() == TopAbs_Orientation.TopAbs_REVERSED
        for i in range(1, poly.NbNodes() + 1):
            p = poly.Node(i).Transformed(trsf)
            flat.extend((float(p.X()), float(p.Y()), float(p.Z())))
        for t in poly.Triangles():
            a, b, c = t.Value(1) + offset_ - 1, t.Value(2) + offset_ - 1, t.Value(3) + offset_ - 1
            idx.extend((a, c, b) if reverse else (a, b, c))
        if _MESH_NORMALS:
            normals.extend(round(v, _NORMAL_DECIMALS) for v in _face_normals(face, poly, trsf, reverse))
        offset_ += poly.NbNodes()
    return flat, idx, normals


def _tessellate_once(solids, deflection, angle=_MESH_ANGLE):
    meshes, total = [], 0
    for solid in solids:
        flat, idx, normals = _tessellate_solid(solid, deflection, angle)
        mesh = {"vertices": flat, "triangles": idx}
        if _MESH_NORMALS:
            # One normal per vertex, in the order of "vertices", in the same frame:
            # a definition's in its own, a part's in the assembly's. Additive: a
            # reader that does not know the field draws exactly what it drew before.
            mesh["normals"] = normals
        meshes.append(mesh)
        total += len(idx) // 3
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
    angle = float(request.get("angular") or _MESH_ANGLE)
    for _ in range(_MESH_TRIES):
        try:
            definitions, shared = _tessellate_once(shapes, deflection, angle)
            meshes, separate = _tessellate_once([solids[i] for i in own], deflection, angle)
        except Exception as exc:
            reason = str(exc).strip() or type(exc).__name__
            return {"mesh_error": "the solid could not be tessellated: %s" % reason}
        total = shared + separate
        if total <= _MESH_BUDGET:
            break
        # Over budget. Coarsened rather than truncated: half a model is a lie
        # about the shape, and a coarser one is the same shape less finely. The
        # angle too, up to its bound: on curved faces it is the limit that binds
        # (see _MESH_ANGLE).
        deflection *= _MESH_COARSEN
        angle = min(angle * _MESH_COARSEN, _MESH_ANGLE_MAX)
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
           # The angular limit the mesh was made to, in radians (stage A5).
           "mesh_angular": angle,
           "mesh_simplified": simplified}
    if instances:
        out["mesh_definitions"] = definitions
        out["mesh_instances"] = [{"id": ids[i], "label": names[i], "definition": d,
                                  "matrix": _column_major(location)}
                                 for i, d, location in instances]
    return out


# --- mesh-only parts (stage E1 of the "looks designed" work) ----------------
#
# damon's decision, 2026-09-18: a part DECLARED mesh-only (shape "lattice") may
# break the rule that every part is an exact solid. It is decorative: labelled
# mesh-only, never in a STEP file, never weighed, never checked for interference,
# and never a feature's target or tool (PRD VIS-06: a render must never imply
# manufacturability). Every other part is still an OCCT solid.
#
# So such a part never reaches _shape or any OCCT boolean. It is taken out of the
# request before anything is built (_split_mesh_only), and on a mesh request it is
# built here, with manifold3d (Apache-2.0; wheels for linux aarch64 and x86_64,
# macOS arm64, CPython 3.13 and 3.14): a triply periodic minimal surface sampled
# as a level set, thickened to a sheet, and clipped to its box by manifold's own
# boolean. Every other reply names it in "mesh_only" and says what it left out.
#
# The level function of each pattern. k = 2 pi / cell. The sheet is where |g| is
# under `w`, and w is chosen so the wall comes out about `thickness` thick:
# |g| / |grad g| is the distance to the surface near it, and |grad g| on the
# surface averages about k * G for the pattern's G (measured, see
# docs/spikes/2026-09-18-mesh-only-parts). The names are Go's table
# (geometry/lattice.go, latticePatterns); a name missing here is refused by name.
_LATTICE_PATTERNS = {
    "gyroid": (lambda x, y, z: (math.sin(x) * math.cos(y) + math.sin(y) * math.cos(z)
                                + math.sin(z) * math.cos(x)), 1.51),
    "diamond": (lambda x, y, z: (math.sin(x) * math.sin(y) * math.sin(z)
                                 + math.sin(x) * math.cos(y) * math.cos(z)
                                 + math.cos(x) * math.sin(y) * math.cos(z)
                                 + math.cos(x) * math.cos(y) * math.sin(z)), 1.43),
    "primitive": (lambda x, y, z: math.cos(x) + math.cos(y) + math.cos(z), 1.31),
}

# The most triangles one mesh-only part may have. Go refuses past its own estimate
# first (geometry.MaxLatticeTriangles, the same number); this is the kernel's own
# count of what it actually built, so an estimate that ran low still cannot send
# more than this.
_LATTICE_BUDGET = 200000


def _is_mesh_only(solid):
    return bool(solid.get("mesh_only"))


def _split_mesh_only(solids):
    """(exact solids, mesh-only solids), each in the request's order."""
    exact, mesh_only = [], []
    for s in solids:
        (mesh_only if _is_mesh_only(s) else exact).append(s)
    return exact, mesh_only


def _lattice_mesh(solid):
    """A mesh-only lattice in its own frame, centred on the origin: (vertices, triangles)
    as flat lists. Raises with a sentence when it cannot be built."""
    if _m3 is None:
        raise RuntimeError("this kernel has no manifold3d, which builds mesh-only parts")
    pattern = solid.get("lattice") or ""
    if pattern not in _LATTICE_PATTERNS:
        raise RuntimeError("%r is not a lattice pattern this kernel knows" % pattern)
    level, gradient = _LATTICE_PATTERNS[pattern]
    d = solid["dims"]
    w, h, dp = d["width"], d["height"], d["depth"]
    cell, thickness, edge = d["cell"], d["thickness"], d["edge"]
    k = 2.0 * math.pi / cell
    half = gradient * k * thickness / 2.0

    def sheet(x, y, z):
        return half - abs(level(k * x, k * y, k * z))

    # Sampled a little past the box, so the sheet is cut by the box and not by the
    # sampling grid, which would leave it open.
    pad = edge
    bounds = [-w / 2 - pad, -h / 2 - pad, -dp / 2 - pad, w / 2 + pad, h / 2 + pad, dp / 2 + pad]
    surface = _m3.Manifold.level_set(sheet, bounds, edge, 0.0)
    region = _m3.Manifold.cube([w, h, dp], True)
    built = surface ^ region
    tris = built.num_tri()
    if tris == 0:
        raise RuntimeError("the lattice came out empty: its walls are thinner than the sampling can see")
    if tris > _LATTICE_BUDGET:
        raise RuntimeError("the lattice is %d triangles, past the %d a mesh-only part may have"
                           % (tris, _LATTICE_BUDGET))
    mesh = built.to_mesh()
    return (_np.asarray(mesh.vert_properties, dtype=float)[:, :3],
            _np.asarray(mesh.tri_verts, dtype=_np.int64))


def _mesh_only_parts(mesh_only):
    """Build every mesh-only part as a placed mesh: (mesh entries, triangles, refused).

    Each distinct lattice is sampled once; a copy is its vertices moved. Placed the
    way _placement places a solid: mirror the part's own x, rotate by the matrix
    (row-major), translate to the position."""
    out, total, refused, cache = [], 0, [], {}
    for part in mesh_only:
        name = part.get("label") or part.get("id")
        key = (part.get("lattice"), tuple(sorted((part.get("dims") or {}).items())))
        if key not in cache:
            try:
                cache[key] = (_lattice_mesh(part), None)
            except Exception as exc:
                cache[key] = (None, str(exc).strip() or type(exc).__name__)
        built, reason = cache[key]
        if built is None:
            refused.append("%s: %s" % (name, reason))
            continue
        verts, tris = built
        if part.get("mirrored"):
            verts = verts * _np.array([-1.0, 1.0, 1.0])
            tris = tris[:, ::-1]
        m = _np.asarray(part["matrix"], dtype=float).reshape(3, 3)
        placed = verts @ m.T + _np.asarray(part["position"], dtype=float)
        out.append({"id": part.get("id"), "label": name, "mesh_only": True,
                    "vertices": placed.ravel().tolist(),
                    "triangles": tris.ravel().astype(int).tolist()})
        total += len(tris)
    return out, total, refused


def _mesh_only_names(mesh_only):
    return [s.get("label") or s.get("id") for s in mesh_only]


def _only_mesh_only(request, mesh_only):
    """The reply for a request whose every part is mesh-only: a mesh when one was
    asked for, and a refusal by name for anything exact — there is no solid to
    write, weigh or check."""
    names = _mesh_only_names(mesh_only)
    if request.get("format") != "mesh" or request.get("properties"):
        return {"ok": False, "error": "every part is mesh-only (%s), so there is no exact solid to "
                                      "build, export or measure" % ", ".join(names[:3]),
                "mesh_only": names}
    start = time.perf_counter()
    meshes, triangles, refused = _mesh_only_parts(mesh_only)
    if not meshes:
        return {"ok": False, "error": "no mesh-only part could be built", "skipped": refused,
                "mesh_only": names}
    return {"ok": True, "parts": 0, "volume": 0.0, "bounds": [0.0] * 6, "shape_builds": 0,
            "interferences": [], "interferences_found": 0, "interferences_buried": 0,
            "interference_box_tests": 0, "interference_pairs": 0, "interference_booleans": 0,
            "interference_reused": 0, "skipped": refused, "features_failed": [],
            "phases": {"mesh": time.perf_counter() - start}, "mesh_only": names,
            "mesh": meshes, "mesh_triangles": triangles, "mesh_only_triangles": triangles,
            "mesh_deflection": 0.0, "mesh_simplified": False}


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
# How many clashes a reply LISTS. Every clash is still found and counted
# ("interferences_found"); past this many, the reply lists the worst and says it is
# a summary ("interferences_summarized"), and is never cut silently.
#
# # Why (next scale walls)
#
# The 1,008,160-occurrence airframe barrel finds 1,760,000 clashes. Listed in full
# (measured 2026-09-15, docs/spikes/2026-09-15-next-scale-walls) the reply was 312 MB,
# all but 541 bytes of it the list; Go decoded it in 2.5 s, allocating 921 MiB and
# holding 299 MiB; and 768,000 of the clashes were buried, which the turn's repair
# would have sent a model as 108 MB of problem lines. No reader uses more than the
# head: the turn names three, and the list is sorted worst first, so a bound never
# hides the biggest.
#
# 10,000 is a chosen count, not a measured optimum: above every fence's clash count
# (2,400 at most), and about 1.8 MB of reply at the barrel's 177 bytes a clash.
_INTERFERENCE_LIST_LIMIT = 10000
# The share of the smaller solid inside the other at which a clash is BURIED, and
# drives a repair. geometry.BuriedFraction, the same number and the same comparison
# (>=), counted here because only here are all the clashes still held.
#
# # Why the kernel counts them (repair judged by the kernel total)
#
# A repair is kept only when the kernel finds fewer buried clashes after it than
# before. Past _INTERFERENCE_LIST_LIMIT the list is the worst 10,000, so on the 1M
# barrel's 768,000 buried rivets it holds 10,000 buried clashes whatever a repair
# does: counted from the list, a repair could never be kept and a worse one never
# seen (docs/bugfix/2026-09-15-an-overlap-repair-was-judged-by-a-list-that-could-not-shrink.md).
#
# ‼️ Change it only together with geometry.BuriedFraction. A count taken at a
# different line than the list is read at would judge a repair by clashes the turn
# does not call buried.
_BURIED_FRACTION = 0.5


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


# # A moved box without a min() per corner (repair bound and check profile)
#
# _measures was 11 s of the 1M check (docs/spikes/2026-09-15-check-profile): a
# moved box per occurrence, each 24 corner sums read through nested lists and folded
# with 24 min() and max() calls. _moved_box_direct evaluates the same sums —
# (a*x + b*y) + c*z + d, each product the same float — in the same corner order, and
# takes min() and max() of the eight at once. min and max return the FIRST of tied
# values either way, so even a -0.0 beside a 0.0 comes out the same. The box is the
# same tuple, bit for bit. _MOVED_BOX_DIRECT = False restores the loop, kept as the
# reference testdata/interference_pair_keys.py compares every box against.
_MOVED_BOX_DIRECT = True


def _moved_box_direct(box, e):
    """_moved_box from a placement's _entries."""
    (x0, y0, z0), (x1, y1, z1) = box
    lo, hi = [], []
    for r in (0, 4, 8):
        a, b, c, d = e[r], e[r + 1], e[r + 2], e[r + 3]
        ax0, ax1, by0, by1, cz0, cz1 = a * x0, a * x1, b * y0, b * y1, c * z0, c * z1
        corners = (ax0 + by0 + cz0 + d, ax0 + by0 + cz1 + d, ax0 + by1 + cz0 + d, ax0 + by1 + cz1 + d,
                   ax1 + by0 + cz0 + d, ax1 + by0 + cz1 + d, ax1 + by1 + cz0 + d, ax1 + by1 + cz1 + d)
        lo.append(min(corners))
        hi.append(max(corners))
    return (tuple(lo), tuple(hi))


def _measures(solids, placed=None, rotations=None, frames=None):
    """Every kept solid's box and volume.

    # Once per definition (Phase 5, stage V1)

    Reading an OCCT solid's bounds and volume costs about 0.2 ms, and after K2b
    that per-solid read was most of what the interference phase spent: 2 s of a
    10,000-part build. A copy is its shape placed, so the shape is measured once
    and each copy's box is that box moved by its placement; volume does not change
    under a rigid motion. A part a feature changed (placed entry None) is measured
    as the solid it is.

    rotations, when a list as long as solids, receives each placed solid's rotation
    entries as bytes, interned so that equal rotations are one object: read here for
    the box anyway, and what _pair_keys memoizes relative rotations by (repair bound
    and check profile). Filled only on the direct path (_MOVED_BOX_DIRECT).

    frames, when a list, receives every solid's three TRANSLATION entries, appended
    flat and in solid order (zeros for a solid with no placement). The twelve entries
    are read here for the box anyway, and the array narrow phase needs nothing else
    per solid to compute a pair's relative translation (see _bulk_keys), so reading
    them a second time there would be twelve OCCT calls an occurrence for nothing.
    """
    boxes, volumes, local, interned = [], [], {}, {}
    for i, solid in enumerate(solids):
        p = placed[i] if placed else None
        if p is None:
            boxes.append(_box_of(solid))
            volumes.append(_volume_of(solid))
            if frames is not None:
                frames.append(0.0)
                frames.append(0.0)
                frames.append(0.0)
            continue
        key, location, shape = p
        if key not in local:
            local[key] = (_box_of(shape), _volume_of(shape))
        box, volume = local[key]
        if _MOVED_BOX_DIRECT:
            e = _entries(location.wrapped)
            if rotations is not None:
                bits = _PACK_ROTATION(e[0], e[1], e[2], e[4], e[5], e[6], e[8], e[9], e[10])
                rotations[i] = interned.setdefault(bits, bits)
            if frames is not None:
                frames.append(e[3])
                frames.append(e[7])
                frames.append(e[11])
            boxes.append(None if box is None else _moved_box_direct(box, e))
        else:
            boxes.append(_moved_box(box, location))
            if frames is not None:
                e = _entries(location.wrapped)
                frames.append(e[3])
                frames.append(e[7])
                frames.append(e[11])
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
    # The same twelve reads and the same sums in the same order as a loop over rows,
    # without a generator per call (kernel last walls: 7 us an occurrence).
    v = location.wrapped.Transformation().Value
    x, y, z = point
    return (v(1, 1) * x + v(1, 2) * y + v(1, 3) * z + v(1, 4),
            v(2, 1) * x + v(2, 2) * y + v(2, 3) * z + v(2, 4),
            v(3, 1) * x + v(3, 2) * y + v(3, 3) * z + v(3, 4))


# # A placed copy's box read directly, not through build123d (kernel last walls)
#
# Measured 2026-09-17 (docs/spikes/2026-09-17-kernel-last-walls): part properties
# were the mesh path's largest phase at 1M occurrences (50 s), and _box_of was
# 22-24 us of each occurrence's ~30 — of which the OCCT box itself
# (BRepBndLib.AddOptimal) is 6.3 us and BRepTools.Clean 3 us. The rest is
# build123d's BoundBox: keyword parsing, a Clean per call, three Vectors built and
# read back.
#
# The box is still read from the PLACED solid, never computed from the definition's
# box: a turned solid's envelope is not its definition's box moved (V3 above), and
# even a copy that is only translated, or turned by a quarter, does not get the same
# BITS that way — measured on boxes, cylinders, cones, spheres and extrusions at all
# 24 quarter-turns: 1,478 of 3,600 differ by up to 9.1e-13 mm, because the primitive
# carries its own location and OCCT composes the two translations before it moves a
# vertex. So what is skipped is only the wrapper around the same call:
#
#   - BRepBndLib.AddOptimal_s(shape, box) with build123d's arguments (defaults:
#     triangulation used if present, shape tolerance not), on the same TopoDS_Shape;
#   - the Clean before it, once per DEFINITION instead of once per copy: a copy's
#     faces and edges are its definition's (the same TShapes, placed), and Clean
#     empties those shared representations, so every later copy's Clean found
#     nothing to remove;
#   - a void box as zeros and any exception as None, as _box_of answers.
#
# _PROPERTIES_BOX_DIRECT = False restores _box_of per solid; the fence
# (testdata/part_properties.py) compares the shipped path against measuring every
# solid through build123d, bounds bit for bit.
_PROPERTIES_BOX_DIRECT = True
_ZERO_BOX = ((0.0, 0.0, 0.0), (0.0, 0.0, 0.0))


def _placed_box(solid):
    """_box_of for a copy whose definition has been Cleaned: the same OCCT call."""
    try:
        if solid._wrapped is None:
            return _ZERO_BOX
        b = Bnd_Box()
        BRepBndLib.AddOptimal_s(solid._wrapped, b)
        if b.IsVoid():
            return _ZERO_BOX
        x0, y0, z0, x1, y1, z1 = b.Get()
        return ((x0, y0, z0), (x1, y1, z1))
    except Exception:
        return None


def _properties(solids, ids, placed=None):
    """Each kept solid's id, volume (mm3), centre of volume (mm) and box (mm)."""
    out, local = [], {}
    for i, solid in enumerate(solids):
        p = placed[i] if (_PROPERTIES_PER_DEFINITION and placed) else None
        if p is None:
            volume, centre = _volume_of(solid), _centre_of(solid)
            box = _box_of(solid)
        else:
            key, location, shape = p
            if key not in local:
                if _PROPERTIES_BOX_DIRECT:
                    try:
                        BRepTools.Clean_s(shape.wrapped)
                    except Exception:
                        pass
                local[key] = (_volume_of(shape), _centre_of(shape))
            volume, centre = local[key]
            centre = _moved_point(centre, location)
            box = _placed_box(solid) if _PROPERTIES_BOX_DIRECT else _box_of(solid)
        out.append({"id": ids[i], "volume": volume,
                    "centroid": list(centre) if centre is not None else None,
                    "bounds": list(box[0]) + list(box[1]) if box is not None else None})
    return out


# # What the kernel MEASURES for a manufacturability check, and what it does not
#
# addresses issue 6: "the kernel builds geometry but never evaluates it". The
# interference check (Phase 5, stages V1-V2) is the first geometric check here;
# this is the second, and it is per PART rather than per pair.
#
# The split is the same one interference uses and for the same reason: the kernel
# MEASURES and Go JUDGES. Nothing here knows what a milling cutter can reach or
# what a moulding needs — that is one table in geometry/manufacturability.go, with
# its citations, and this file must never hold a second copy of a limit. What
# comes back is five numbers a formula can predict, per part, plus the counts
# saying how much was looked at.
#
# ‼️ Each number says exactly what it measures, because each is a PROXY and the
# rule that reads it inherits the proxy's blind spots (they are written out in
# geometry/manufacturability.go, beside the rule that reads each one):
#
#   - min_wall: a ray cast INWARD from the middle of every face, to the first
#     surface it meets. On a plate it is the plate's thickness, on a rod its
#     diameter, on a tube its annular wall — all three checked against the formula
#     answer. It is the thinnest place the FACE CENTRES see, not the thinnest place
#     there is: a wall that is thin only at a corner is not sampled.
#   - min_feature: the smallest of each edge's length, or a circular edge's
#     DIAMETER. A 2 mm hole is a 2 mm feature, not a 6.28 mm edge.
#   - internal_radius: 0.0 when the part has any CONCAVE edge (OCCT's own answer,
#     ChFi3d.DefineConnectType_s, the same call the "concave" edge rule uses);
#     otherwise the smallest radius of a concave cylindrical face, which is what a
#     fillet leaves behind. None when the part has neither — a box has no internal
#     corner, and a rule about corners must not invent one.
#   - min_draft and max_overhang: angles in DEGREES against +Y, which is up in this
#     system (see _OPEN_FACES). Sampled at a 3x3 grid in each face's own parameter
#     space, so a curved face is answered by where it is worst rather than by its
#     middle. Draft skips faces square to the pull (a top and a bottom have no
#     draft to give). Overhang skips samples at the part's own lowest point, which
#     is where it rests.
#
# ‼️ The pull and build direction is the ASSEMBLY's +Y, not a direction anybody
# chose per part: no FORGE document states one. A real moulding or print decides it,
# and that decision changes every draft and overhang number here. Go says so in the
# turn rather than letting the numbers read as a verdict about a real process plan.
_MANUFACTURABILITY = True
# The parameters each face's normal is sampled at, in each direction: a 3x3 grid,
# away from the edges where a trimmed surface's normal is least representative.
_MFG_SAMPLES = (0.17, 0.5, 0.83)
# Below this, an angle is the same as zero: OCCT returns a planar face's normal to
# about 1e-12, and a wall that is 1e-9 degrees off vertical is a vertical wall.
_MFG_ANGLE_EPSILON = 1e-6
# A face whose normal is within this of the pull direction is a top or a bottom,
# not a wall, and has no draft angle to report.
_MFG_SQUARE_TO_PULL = 89.0
# # How many FACES one build may measure, and why 600
#
# Measured 2026-09-20 on this kernel, interleaved with the same builds run without
# the pass (docs/spikes/2026-09-20-manufacturability-cost):
#
#   - 512 parts that are COPIES of one shape: 6 faces measured, 511 answered from
#     the cache, 32 ms. 4,096 copies: 6 faces, 4,095 reused, 145 ms. The usual
#     FORGE model is this one — a definition built once and placed — and the pass
#     is then roughly free.
#   - 512 DISTINCT shapes: 3,072 faces, no reuse, 10.7 s. 4,096 distinct shapes did
#     not finish inside the kernel's own 30 s build limit at all.
#
# So the cost is per distinct FACE, at about 3.5 ms of it, and the rule that costs
# most is the concave-edge test: OCCT's ChFi3d::DefineConnectType on every edge was
# 6.9 s of a 9.0 s profile over 1,200 faces, against 0.47 s for the wall rays.
#
# 600 faces is about 2.1 s at that rate — a fourteenth of the kernel's 30 s build
# limit, and the same order as the interference check's own worst case. It is a
# CHOSEN bound from a measured rate, not an optimum: no model was measured being
# read with and without a truncated check. A hundred distinct six-faced parts fit
# inside it; past that the reply says how many were measured and Go says the rest
# are not known to be makeable, which is the one thing that must never be silent.
_MANUFACTURABILITY_BUDGET = 600


def _face_samples(face):
    """(point, outward normal) at a 3x3 grid over one face's own parameters.

    ‼️ The normal is OCCT's own, and is not reversed here: BRepGProp_Face.Normal
    already reverses it for a REVERSED face, so doing it again here pointed every
    such face's normal INTO the solid. Measured: every cylinder in a model then
    reported a 90 degree overhang on its own top face."""
    s = BRepGProp_Face(face.wrapped)
    u0, u1, v0, v1 = s.Bounds()
    out = []
    for su in _MFG_SAMPLES:
        for sv in _MFG_SAMPLES:
            p, n = gp_Pnt(), gp_Vec()
            try:
                s.Normal(u0 + (u1 - u0) * su, v0 + (v1 - v0) * sv, p, n)
            except Exception:
                continue
            if n.Magnitude() == 0:
                continue
            n.Normalize()
            out.append((p, n))
    return out


def _ray_thickness(shape, inter, point, normal):
    """How far it is through the material from a point on the surface, inward."""
    try:
        inter.Init(shape.wrapped, gp_Lin(point, gp_Dir(normal.Reversed())), 1e-7)
    except Exception:
        return None
    best = None
    while inter.More():
        w = inter.W()
        # Strictly past the face the ray left from: its own surface is a hit at 0.
        if w > 1e-6 and (best is None or w < best):
            best = w
        inter.Next()
    return best


def _cylinder_radius_if_concave(face, samples):
    """A cylindrical face's radius when the material is OUTSIDE it — a bore, or the
    fillet left in an internal corner. None for anything else.

    The samples are the caller's: taking them again here doubled the sampling cost
    of every build for nine numbers already in hand."""
    if not samples:
        return None
    try:
        a = BRepAdaptor_Surface(face.wrapped)
        if a.GetType() != GeomAbs_Cylinder:
            return None
        cyl = a.Cylinder()
    except Exception:
        return None
    p, n = samples[len(samples) // 2]
    axis = cyl.Axis()
    o, d = axis.Location(), axis.Direction()
    v = gp_Vec(gp_Pnt(o.X(), o.Y(), o.Z()), p)
    along = v.Dot(gp_Vec(d.X(), d.Y(), d.Z()))
    radial = gp_Vec(v.X() - along * d.X(), v.Y() - along * d.Y(), v.Z() - along * d.Z())
    # Outward normal pointing back at the axis: the solid is on the outside.
    if radial.Dot(n) >= 0:
        return None
    return float(cyl.Radius())


def _edge_feature(edge):
    """The smallest thing this edge describes: a circle's DIAMETER, or a length."""
    try:
        a = BRepAdaptor_Curve(edge.wrapped)
        if a.GetType() == GeomAbs_Circle:
            return 2.0 * float(a.Circle().Radius())
    except Exception:
        pass
    try:
        return float(edge.length)
    except Exception:
        return None


def _part_manufacturability(shape, inter):
    """The five measurements, on one built solid. See the note above."""
    faces = list(shape.faces())
    if not faces:
        return None, 0
    min_wall = min_feature = min_draft = max_overhang = None
    concave_radius = None
    sharp = False
    # ‼️ The floor is the lowest SAMPLE, not the bounding box's bottom. OCCT's box
    # around a curved solid is a little larger than the solid — a 100 mm rod
    # measured 1e-7 mm below its own end face — so a floor taken from the box left
    # the flat face the part rests on a hair above it, and every cylinder in the
    # model reported a 90 degree overhang it does not have. Taken from the samples,
    # the resting face IS the floor, exactly.
    sampled = [(face, _face_samples(face)) for face in faces]
    floor = None
    for _face, samples in sampled:
        for p, _n in samples:
            if floor is None or p.Y() < floor:
                floor = p.Y()
    if floor is None:
        return None, len(faces)
    try:
        tol = max(1e-9, 1e-9 * float(shape.bounding_box().diagonal))
    except Exception:
        tol = 1e-9
    for face, samples in sampled:
        if samples:
            p, n = samples[len(samples) // 2]
            d = _ray_thickness(shape, inter, p, n)
            if d is not None and (min_wall is None or d < min_wall):
                min_wall = d
        for p, n in samples:
            y = n.Y()
            if y > 1.0:
                y = 1.0
            elif y < -1.0:
                y = -1.0
            from_pull = math.degrees(math.acos(y))
            draft = abs(90.0 - from_pull)
            if draft < _MFG_SQUARE_TO_PULL and (min_draft is None or draft < min_draft):
                min_draft = draft
            # A sample that faces downward and is not where the part rests.
            if n.Y() < 0 and p.Y() > floor + tol:
                overhang = 90.0 - (180.0 - from_pull)
                if max_overhang is None or overhang > max_overhang:
                    max_overhang = overhang
        if not sharp:
            r = _cylinder_radius_if_concave(face, samples)
            if r is not None and (concave_radius is None or r < concave_radius):
                concave_radius = r
    edge_faces = _edge_faces(shape)
    for edge in shape.edges():
        size = _edge_feature(edge)
        if size is not None and (min_feature is None or size < min_feature):
            min_feature = size
        if not sharp and _connection(edge_faces, edge) == ChFiDS_TypeOfConcavity.ChFiDS_Concave:
            sharp = True
    internal = 0.0 if sharp else concave_radius
    out = {
        "min_wall": min_wall,
        "min_feature": min_feature,
        "internal_radius": internal,
        "min_draft": None if min_draft is None else _snap(min_draft),
        "max_overhang": None if max_overhang is None else _snap(max_overhang),
        "faces": len(faces),
    }
    return out, len(faces)


def _snap(angle):
    """An angle within floating-point noise of zero IS zero: a vertical wall that
    measures 4e-15 degrees of draft must report no draft, not a draft too small to
    matter — the second reads as a number somebody chose."""
    return 0.0 if abs(angle) < _MFG_ANGLE_EPSILON else angle


def _manufacturability(solids, ids, placed=None):
    """Each kept solid's five measurements, bounded by _MANUFACTURABILITY_BUDGET
    faces and reusing a definition already measured at the same rotation.

    Every measurement here is invariant under TRANSLATION — the four shape ones by
    construction, and the two angles because the pull direction is a direction and
    the floor is the part's own lowest point. So a copy of a shape already measured
    at the same rotation is the same answer, and the 4,096th bolt costs a dict
    lookup. It is NOT invariant under rotation: turning a part on its side changes
    every draft and overhang, which is the whole point of measuring them."""
    inter = BRepIntCurveSurface_Inter()
    out, seen = [], {}
    faces_used, reused, checked = 0, 0, 0
    truncated = False
    for i, solid in enumerate(solids):
        key = None
        if placed and placed[i] is not None:
            shape_key, location, _shape_of = placed[i]
            try:
                key = (shape_key, _rounded_rotation(_rotation(location.wrapped.Transformation().Value)))
            except Exception:
                key = None
        if key is not None and key in seen:
            measure = seen[key]
            reused += 1
        elif truncated or faces_used >= _MANUFACTURABILITY_BUDGET:
            # ‼️ Stopped, and SAID: every part past the budget is absent from the
            # list, and Go names them unchecked rather than reading a short list as
            # a clean model. The same rule as the interference pair budget (V2).
            truncated = True
            continue
        else:
            try:
                measure, cost = _part_manufacturability(solid, inter)
            except Exception as exc:
                measure, cost = {"unchecked": str(exc).strip() or type(exc).__name__}, 0
            faces_used += cost
            if measure is None:
                measure = {"unchecked": "the kernel found no faces to measure"}
            if key is not None:
                seen[key] = measure
        checked += 1
        row = dict(measure)
        row["id"] = ids[i]
        out.append(row)
    return out, truncated, {"checked": checked, "parts": len(solids),
                            "faces": faces_used, "reused": reused}


# # A named section, and what it is honestly worth (issue 6, strength)
#
# There is no FEA here and this is not one. What a section gives is the geometry
# half of a beam calculation — area, centroid, second moments of area about the
# section's own centroid, and the section moduli that follow — which is cheap,
# exact, and checkable against b*h^3/12 on a rectangle.
#
# It is NOT a stress. A stress needs a load, a load path, boundary conditions and a
# material's yield; a second moment of area needs none of those and claims none of
# them. Go reports the numbers as section PROPERTIES and never as a verdict, and
# the PR beside this says what a real check would take.
#
# The cut is a plane normal to one of the three axes at a stated coordinate,
# intersected with one part. A plane that misses the part returns no area, which is
# reported as a section that could not be measured and never as a section of zero.
def _section(shape, axis, at):
    """Area, centroid, second moments and moduli of one planar cut, or a reason."""
    try:
        box = shape.bounding_box()
    except Exception as exc:
        return None, str(exc).strip() or type(exc).__name__
    lo = (box.min.X, box.min.Y, box.min.Z)[axis]
    hi = (box.max.X, box.max.Y, box.max.Z)[axis]
    if at < lo or at > hi:
        return None, ("the plane at %g is outside the part, which runs from %g to %g on that axis"
                      % (at, lo, hi))
    normal = [0.0, 0.0, 0.0]
    normal[axis] = 1.0
    # ‼️ Centred on the PART, not on the world origin. A cutting face at the origin
    # missed every part that is not there — a cone 600 mm down the z axis came back
    # "the plane met no material", which reads exactly like a plane outside the
    # part and is not.
    centre = box.center()
    origin = [float(centre.X), float(centre.Y), float(centre.Z)]
    origin[axis] = at
    # Twice the diagonal, so the cutting face covers the part however it is turned.
    size = float(box.diagonal) * 2.0 + 1.0
    try:
        cut = shape.intersect(Plane(origin=tuple(origin), z_dir=tuple(normal)) * Rectangle(size, size))
    except Exception as exc:
        return None, str(exc).strip() or type(exc).__name__
    if cut is None:
        return None, "the plane met no material"
    if not hasattr(cut, "wrapped"):
        cut = Compound(list(cut))
    props = GProp_GProps()
    try:
        BRepGProp.SurfaceProperties_s(cut.wrapped, props)
    except Exception as exc:
        return None, str(exc).strip() or type(exc).__name__
    area = float(props.Mass())
    if area <= 0:
        return None, "the plane met no material"
    c = props.CentreOfMass()
    centroid = (float(c.X()), float(c.Y()), float(c.Z()))
    about = GProp_GProps(gp_Pnt(*centroid))
    BRepGProp.SurfaceProperties_s(cut.wrapped, about)
    m = about.MatrixOfInertia()
    # The two axes the section lies IN. The diagonal entry for an in-plane axis is
    # that axis's second moment of area, because the out-of-plane coordinate is
    # zero over the whole face: for a cut normal to x, Value(2,2) is the integral of
    # z^2 and Value(3,3) the integral of y^2.
    other = [a for a in (0, 1, 2) if a != axis]
    moments = [float(m.Value(a + 1, a + 1)) for a in other]
    try:
        cb = cut.bounding_box()
    except Exception as exc:
        return None, str(exc).strip() or type(exc).__name__
    fibres, moduli = [], []
    for n, a in enumerate(other):
        # Bending about `a` stresses the material furthest away along the OTHER
        # in-plane axis, so that is the extreme fibre the modulus divides by.
        away = other[1 - n]
        lo_a = (cb.min.X, cb.min.Y, cb.min.Z)[away]
        hi_a = (cb.max.X, cb.max.Y, cb.max.Z)[away]
        fibre = max(abs(hi_a - centroid[away]), abs(centroid[away] - lo_a))
        if fibre <= 0:
            # A section with area cannot be flat in an in-plane direction; if OCCT
            # says it is, the cut is degenerate and no modulus is claimed from it.
            return None, "the cut has area but no extent across it, so no section modulus follows"
        fibres.append(fibre)
        moduli.append(moments[n] / fibre)
    # The keys are geometry.SectionProperties' own json tags, exactly: the reply is
    # decoded straight into that type and a name invented here would arrive as a zero.
    return {"area_mm2": area, "centroid_mm": list(centroid),
            "axes": [_AXIS_NAMES[a] for a in other], "second_moments_mm4": moments,
            "extreme_fibres_mm": fibres, "section_moduli_mm3": moduli}, None


_AXIS_NAMES = ("x", "y", "z")


def _sections(shapes, request):
    """Every section the request named, each answered or refused by name."""
    out = []
    for s in request.get("sections") or []:
        row = {"id": s.get("id"), "part": s.get("part"), "axis": s.get("axis"), "at": s.get("at")}
        shape = shapes.get(s.get("part"))
        if shape is None:
            row["unmeasured"] = "no part with that id survived the build"
            out.append(row)
            continue
        axis = {"x": 0, "y": 1, "z": 2}.get(s.get("axis"))
        if axis is None:
            row["unmeasured"] = "the axis must be x, y or z"
            out.append(row)
            continue
        got, why = _section(shape, axis, float(s.get("at") or 0.0))
        if got is None:
            row["unmeasured"] = why
        else:
            row.update(got)
        out.append(row)
    return out


def _boxes_miss(a, b):
    if a is None or b is None:
        return True
    for i in range(3):
        if a[1][i] <= b[0][i] or b[1][i] <= a[0][i]:
            return True
    return False


# A box longer than this many cells of a level on an axis is filed one level up
# on that axis. See _candidate_pairs.
_GRID_LARGE = 4.0
# Each level's cells are 2**_GRID_LEVEL_SHIFT (4) times the level below's, on each
# axis separately. A power of two, so a level's cell index is the finest level's
# index shifted right, which is exact integer arithmetic.
_GRID_LEVEL_SHIFT = 2


def _cell(value, size):
    return int(math.floor(value / size))


def _grid_level(extent, cell):
    """The coarsest-needed level on one axis: the first whose cells are at least
    extent / _GRID_LARGE long, so a box crosses at most five of them."""
    level = 0
    while extent > _GRID_LARGE * math.ldexp(cell, _GRID_LEVEL_SHIFT * level) and level < 512:
        level += 1
    return level


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

    # Boxes much larger than a cell: a level for each axis (large-box index)

    A chassis rail filed in every cell it crosses would cost more cells than it has
    neighbours. Until 2026-09-15 a box longer than _GRID_LARGE cells was kept out of
    the grid and tested against EVERY box, on the grounds that an assembly has few
    of them. An airframe barrel does not: every skin panel, frame segment and
    stringer is one, 16,160 of them at 1,008,160 occurrences, and 16.2 billion box
    tests priced the check at ~2,650 s — the build hit its 40-minute cap
    (docs/spikes/2026-09-15-one-million-occurrences).

    ‼️ One coarser cubic grid does not fix that. A 50 m stringer needs a 50 m cell,
    and every rivet in the barrel shares that cell with every stringer: 80 million
    tests at 1M. Long parts are long on ONE axis and thin on the others, so the
    level is chosen per axis. Level l's cells are 4**l finest cells long on that
    axis, and a box's level on an axis is the first whose cells it crosses at most
    five of: a rivet is (0, 0, 0), a stringer (5, 0, 0) — cells 14 m along the
    barrel and 14 mm across it — and a skin panel (2, 0, 1). Boxes with the same
    levels form a group; (0, 0, 0) is the grid as it was, box for box.

    A pair from groups A and B is tested in the grid whose level on each axis is
    the larger of the two: both boxes cross at most five of its cells a side, and
    only boxes sharing a cell are tested. Every pair of groups is one such pass —
    the fewer boxes filed, the other group looking up the cells it crosses — and a
    pair belongs to exactly one pass. Cost is a few cells per box per group, not
    one test per box per large box (docs/spikes/2026-09-15-large-box-index).

    # Each pair is tested in one cell only, at every level

    The home cell above holds at any level. Cell indices are computed once, at the
    finest level, as integers, and a coarser index is that integer shifted right —
    floor(floor(x / c) / 4**l) is floor(x / (c * 4**l)) — so the home cell of a pair
    at any level is the larger of their two low-corner indices, shifted. It is in
    both boxes' ranges whenever they overlap (floor is monotone), and it is one
    cell, so a pair is tested, and counted, once.

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
    # Each box's low and high cell at the finest level, and its group.
    lows, highs, groups = {}, {}, {}
    for k in present:
        lo, hi = boxes[k]
        lows[k] = (_cell(lo[0], cell), _cell(lo[1], cell), _cell(lo[2], cell))
        highs[k] = (_cell(hi[0], cell), _cell(hi[1], cell), _cell(hi[2], cell))
        level = (_grid_level(hi[0] - lo[0], cell), _grid_level(hi[1] - lo[1], cell),
                 _grid_level(hi[2] - lo[2], cell))
        groups.setdefault(level, []).append(k)

    def file(members, sx, sy, sz):
        cells = {}
        for k in members:
            q, r = lows[k], highs[k]
            for cx in range(q[0] >> sx, (r[0] >> sx) + 1):
                for cy in range(q[1] >> sy, (r[1] >> sy) + 1):
                    for cz in range(q[2] >> sz, (r[2] >> sz) + 1):
                        cells.setdefault((cx, cy, cz), []).append(k)
        return cells

    pairs, tests = [], 0
    order = sorted(groups)
    for n, level_a in enumerate(order):
        for level_b in order[n:]:
            sx = _GRID_LEVEL_SHIFT * max(level_a[0], level_b[0])
            sy = _GRID_LEVEL_SHIFT * max(level_a[1], level_b[1])
            sz = _GRID_LEVEL_SHIFT * max(level_a[2], level_b[2])
            if level_a == level_b:
                for home, members in file(groups[level_a], sx, sy, sz).items():
                    for m, a in enumerate(members):
                        qa = lows[a]
                        for b in members[m + 1:]:
                            qb = lows[b]
                            if (max(qa[0], qb[0]) >> sx != home[0]
                                    or max(qa[1], qb[1]) >> sy != home[1]
                                    or max(qa[2], qb[2]) >> sz != home[2]):
                                continue
                            tests += 1
                            if not _boxes_miss(boxes[a], boxes[b]):
                                pairs.append((a, b) if a < b else (b, a))
                continue
            filed, looking = groups[level_a], groups[level_b]
            if len(filed) > len(looking):
                filed, looking = looking, filed
            cells = file(filed, sx, sy, sz)
            for a in looking:
                qa, ra = lows[a], highs[a]
                for cx in range(qa[0] >> sx, (ra[0] >> sx) + 1):
                    for cy in range(qa[1] >> sy, (ra[1] >> sy) + 1):
                        for cz in range(qa[2] >> sz, (ra[2] >> sz) + 1):
                            members = cells.get((cx, cy, cz))
                            if members is None:
                                continue
                            for b in members:
                                qb = lows[b]
                                if (max(qa[0], qb[0]) >> sx != cx
                                        or max(qa[1], qb[1]) >> sy != cy
                                        or max(qa[2], qb[2]) >> sz != cz):
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

# # A clash inside a box is the same clash wherever it slides (large-box index)
#
# A rivet row along a stringer is a hundred different poses against ONE stringer,
# so the pose cache above reused nothing there: an airframe barrel's booleans grew
# ~96 a bay and the 2,000 budget truncated it from ~20 bays (316,976 of 528,000
# clashes at 300k; docs/spikes/2026-09-15-one-million-occurrences).
#
# ‼️ "Key the pose modulo the stringer's translational invariance" is WRONG: a
# stringer is finite, and a rivet hanging over its end shares less of it. What is
# true is narrower. A box is the intersection of three slabs, one per local axis.
# If the other solid lies wholly inside the slab of axis u, then its common volume
# with the box is its common volume with the other two slabs alone, and those do
# not change when it moves along u. So two placements that differ only along u,
# and are BOTH inside that slab, share the same volume. The key marks the axis
# (its translation becomes inf, which no real pose has) only when containment is
# shown — with the other solid's own box, which is never smaller than the solid,
# and a margin — so a pose near an end keeps its full key and is measured.
#
# The same holds from the other side. When THIS frame's box lies inside the other
# box's slab on the other's axis v, and v lies along this frame's axis w, moving
# the other along w is moving it along its own v: marked -inf ("carried"). Without
# it a skin panel keyed a barrel-long stringer by where along the stringer the
# panel sits — a boolean a bay again.
#
# Why marks can be combined: each containment is an interval on ONE translation
# component in this frame (a rotation that lines v up with w leaves the other
# components out of it). Two placements with equal keys differ only in marked
# components, each inside its interval in both; intervals are convex, so moving
# one component at a time from one to the other stays inside every interval, and
# no move changes the volume. The sign says which containment marked a component,
# so both placements slide for the same reason.
#
# Boxes, and since 2026-09-15 (next scale walls) the one slab of a cylinder and of
# an extrusion. The proof never used that the frame's solid is a box, only that it
# is a slab S intersected with something invariant along S's axis: P inside S gives
# P ∩ (S ∩ R) = P ∩ R, and R does not change when P moves along the axis. A box is
# S ∩ (two slabs); a cylinder is |y| <= h/2 ∩ (a round prism along y); an extrusion
# is |z| <= d/2 ∩ (its outline's prism along z). So a pin slides along a shaft it
# lies within the length of, a cleat along an extruded girder, and a collar is
# carried along the shaft through it. The prism's cross-section axes are NOT slabs
# and are never marked (see _slabs). Slabs are known exactly from dims, checked
# against measured bounds before they are trusted. _INTERFERENCE_SLIDE turns this
# off; it exists so a measurement can say what sliding saves.
_INTERFERENCE_SLIDE = True
# Containment must hold by this much (mm): far above bounds noise and the 1e-6 mm
# the pose is rounded to, far below any clash worth reporting.
_SLIDE_MARGIN = 1e-4


def _pose(location):
    """A placement as twelve numbers, rounded so float noise does not split one
    pose into two: rotation to 1e-9, translation to 1e-6 mm."""
    t = location.wrapped.Transformation()
    return tuple(round(t.Value(r, c), 6 if c == 4 else 9) for r in (1, 2, 3) for c in (1, 2, 3, 4))


def _slabs(key, shape):
    """A shape's half-lengths along the own axes it is a SLAB on — None on an axis
    it is not, or None altogether — and its own box, for _slid.

    - A box centred on its origin is three slabs.
    - A cylinder (a "cylinder" or "cone" whose top radius is its radius, which is
      when _shape builds a Cylinder) is a round prism along its own y, cut by ONE
      slab, |y| <= height / 2. Added 2026-09-15 (next scale walls).
    - An extrusion is its outline's prism along its own z, cut by |z| <= depth / 2:
      _shape extrudes half the depth both ways. Holes and islands are prisms too.
      Added 2026-09-15 (next scale walls).

    Nothing else is claimed: a cone, a sphere, a revolve, a sweep and an imported
    STEP are not a prism cut by a slab, and are keyed by their full pose.
    """
    box = _box_of(shape)
    half = None
    try:
        # The key is a tuple, not JSON (see _shape_key): "shape" is its first field
        # raw, and "dims" its second as the sorted (name, value) pairs. Read by
        # position because _SHAPE_KEYS defines the order, and turned back into a
        # dict once per DEFINITION, not once per pair.
        kind, d = key[0], dict(key[1] or ())
        if box is None:
            half = None
        elif kind == "box":
            half = (float(d["width"]) / 2, float(d["height"]) / 2, float(d["depth"]) / 2)
        elif kind in ("cylinder", "cone") and d.get("radius_top", d["radius"]) == d["radius"]:
            half = (None, float(d["height"]) / 2, None)
        elif kind == "extrusion":
            half = (None, None, float(d["depth"]) / 2)
        # Trusted only if the solid really is that shape. Bounds are never tighter
        # than the solid, so bounds within a micron of a slab on both sides are the
        # slab. Checked on the slab axes only: a prism's other bounds are its outline's.
        if half is not None and any(half[r] is not None and (abs(box[0][r] + half[r]) > 1e-3
                                                             or abs(box[1][r] - half[r]) > 1e-3)
                                    for r in range(3)):
            half = None
    except (ValueError, KeyError, TypeError):
        half = None
    return half, box


def _inside(relative, frame, other):
    """The axes of the frame's box whose slab the other solid lies wholly inside,
    by _SLIDE_MARGIN. relative places the other in the frame."""
    half, box = frame[0], other[1]
    axes = []
    if half is None or box is None:
        return axes
    t = relative.wrapped.Transformation()
    for r in range(3):
        if half[r] is None:
            # Not a slab on this axis (a prism's cross-section): nothing slides.
            continue
        mid, reach = t.Value(r + 1, 4), 0.0
        for c in range(3):
            v = t.Value(r + 1, c + 1)
            mid += v * (box[0][c] + box[1][c]) / 2
            reach += abs(v) * (box[1][c] - box[0][c]) / 2
        if mid - reach >= _SLIDE_MARGIN - half[r] and mid + reach <= half[r] - _SLIDE_MARGIN:
            axes.append(r)
    return axes


def _carried(pose, axes):
    """The frame's axes that the other solid's own axes `axes` lie along.

    A column of pose's rotation is one of the other's axes in this frame; it lies
    along the frame's axis w when that entry is ±1 and the rest round to zero — the
    same 1e-9 the pose is compared to."""
    out = []
    for v in axes:
        for w in range(3):
            if (abs(pose[4 * w + v]) >= 1 - 1e-9
                    and all(abs(pose[4 * o + v]) <= 1e-9 for o in range(3) if o != w)):
                out.append(w)
    return out


def _slid(pose, inside, carried):
    """pose with the translation along each axis the volume cannot depend on marked:
    inf where the other solid is inside this frame's box on that axis, -inf where
    this frame's solid is inside the other's box along it. Marked by WHICH reason,
    so two equal keys slide for the same reason and the proof never mixes them."""
    if not inside and not carried:
        return pose
    out = list(pose)
    for r in carried:
        out[4 * r + 3] = -math.inf
    for r in inside:
        out[4 * r + 3] = math.inf
    return tuple(out)


def _marks(pose):
    return sum(1 for r in range(3) if math.isinf(pose[4 * r + 3]))


def _pair_key(placed, shape_ids, i, j, slabs=None):
    """Which two shapes, and where the second sits in the first's frame — the same
    for the pair in either order — or None when either was changed by a feature.

    With slabs (shape key -> _slabs), each translation the volume cannot depend on
    is marked (see _INTERFERENCE_SLIDE), and the frame that marks more of them is
    the key: a rivet is keyed in its stringer's frame, not the stringer in the
    rivet's."""
    pi, pj = placed[i], placed[j]
    if pi is None or pj is None:
        return None
    a, b = shape_ids[pi[0]], shape_ids[pj[0]]
    ahead, back = pi[1].inverse() * pj[1], pj[1].inverse() * pi[1]
    forward = (a, b, _pose(ahead))
    backward = (b, a, _pose(back))
    if not slabs:
        return min(forward, backward)
    inside_i = _inside(ahead, slabs[pi[0]], slabs[pj[0]])
    inside_j = _inside(back, slabs[pj[0]], slabs[pi[0]])
    forward = (a, b, _slid(forward[2], inside_i, _carried(forward[2], inside_j)))
    backward = (b, a, _slid(backward[2], inside_j, _carried(backward[2], inside_i)))
    marked_f, marked_b = _marks(forward[2]), _marks(backward[2])
    if marked_f != marked_b:
        return forward if marked_f > marked_b else backward
    return min(forward, backward)


# # A pair's key without a build123d Location per step (repair bound and check profile)
#
# Profiled 2026-09-15 on the airframe barrel (docs/spikes/2026-09-15-check-profile):
# the check was 121 s of the 1M build, and ~80% of it was _pair_key, run once for
# each of 2,191,348 candidate pairs to find 15 booleans' worth of distinct clashes.
# Each key built four build123d Locations (two inverses, two products), each
# Location.__init__ parsing nine keyword arguments and four isinstance checks
# around one OCCT call; read each relative transformation's twelve entries twice
# (once for the pose, once more for containment); and ran _carried and _marks as
# generator expressions.
#
# _pair_keys makes the SAME OCCT calls on the same TopLoc_Locations — Inverted(),
# then the product, then Transformation() — without a Python Location around any of
# them, and reads each transformation's entries once. A placement's inverse is taken
# once per solid rather than once per pair it is in; TopLoc_Location is immutable,
# so the inverse reused is the inverse that would have been taken. The arithmetic of
# the pose, the rounding, the containment test and the marks is written out in the
# order the originals do it, so every key is the same tuple, bit for bit.
#
# _PAIR_KEY_DIRECT = False restores _pair_key through build123d, kept as the
# reference the fence compares every key against (testdata/interference_pair_keys.py).
_PAIR_KEY_DIRECT = True


#
# # And the relative ROTATION once per pair of rotations
#
# With the Locations gone, what was left of a key was reading and rounding: 24
# Value() calls and 24 round() calls a pair, about 15 µs of 22. Measured on the 90k
# barrel: 197,356 pairs have 80 distinct placement rotations and 224 distinct pairs
# of them, and in every pair, both ways, the relative rotation's bits were the same
# for every pair with the same two rotations.
#
# Why that holds, from OCCT's gp_Trsf: a product's vectorial part is computed from
# the factors' matrices, forms and scale factors, and an inverse's from its own —
# never from a translation. TopLoc_Location composes a chain item by item, so the
# argument covers a placement that is ONE datum at power 1, which is what every
# placement _placement makes. So the key memoizes the relative rotation, raw and
# rounded, by the two placements' (form, rotation bits), and reads and rounds only
# the three translations per direction.
#
# ‼️ Guarded, not assumed: a placement that is a chain, a power other than 1, or a
# scale factor other than 1 is keyed without the memo; so is a pair whose two
# placements are one datum, where TopLoc_Location cancels the powers to the exact
# identity and a product of the two would not. The memo holds at most
# _ROTATION_MEMO_LIMIT pairs of rotations (65,536 at ~1 KB each); past that a pair
# is keyed without it.
_ROTATION_MEMO_LIMIT = 1 << 16
_PACK_ROTATION = struct.Struct("<9d").pack


def _entries(location):
    """A TopLoc_Location's transformation as its twelve entries, row by row, each
    row's rotation then its translation — _pose's order, unrounded."""
    v = location.Transformation().Value
    return (v(1, 1), v(1, 2), v(1, 3), v(1, 4), v(2, 1), v(2, 2), v(2, 3), v(2, 4),
            v(3, 1), v(3, 2), v(3, 3), v(3, 4))


def _rotation(v):
    """A transformation's rotation entries, row by row, from its Value."""
    return (v(1, 1), v(1, 2), v(1, 3), v(2, 1), v(2, 2), v(2, 3), v(3, 1), v(3, 2), v(3, 3))


def _rounded_rotation(r):
    """_pose's rounding of a rotation: 1e-9."""
    return (round(r[0], 9), round(r[1], 9), round(r[2], 9), round(r[3], 9), round(r[4], 9),
            round(r[5], 9), round(r[6], 9), round(r[7], 9), round(r[8], 9))


def _pose_of(q, t):
    """_pose from a rounded rotation and a translation, which is rounded to 1e-6 here."""
    return (q[0], q[1], q[2], round(t[0], 6), q[3], q[4], q[5], round(t[1], 6),
            q[6], q[7], q[8], round(t[2], 6))


def _inside_split(rot, t, half, box):
    """_inside on a rotation and a translation: the same sums, in the same order."""
    axes = []
    if half is None or box is None:
        return axes
    lo, hi = box
    for r in range(3):
        if half[r] is None:
            continue
        row = 3 * r
        mid, reach = t[r], 0.0
        for c in range(3):
            v = rot[row + c]
            mid += v * (lo[c] + hi[c]) / 2
            reach += abs(v) * (hi[c] - lo[c]) / 2
        if mid - reach >= _SLIDE_MARGIN - half[r] and mid + reach <= half[r] - _SLIDE_MARGIN:
            axes.append(r)
    return axes


def _carried_fast(pose, axes):
    """_carried without generators."""
    out = []
    for v in axes:
        for w in range(3):
            if abs(pose[4 * w + v]) >= 1 - 1e-9:
                clear = True
                for o in range(3):
                    if o != w and not abs(pose[4 * o + v]) <= 1e-9:
                        clear = False
                        break
                if clear:
                    out.append(w)
    return out


# # And containment once per group of pairs (last hot spots)
#
# Profiled 2026-09-15 (docs/spikes/2026-09-15-last-hot-spots): with the Locations
# gone, _inside_split was the single largest thing left in the check — 394,712 calls
# (two a pair) and 0.86 s of own time of the 2.69 s the 90k keying loop takes under a
# profiler, 3.79 million abs() among them.
#
# It need not be per pair. _inside_split's `mid` is the translation's own entry plus
# three products of the relative ROTATION with the other solid's box centre, and its
# `reach` is three products of that rotation with the box's half-extents. Neither
# depends on the translation, which enters only as mid's first term. The barrel's
# 197,356 candidate pairs at 90k are 536 groups of (two definitions, two rotations),
# so the products are taken 536 times instead of 394,712, and a pair adds three
# floats.
#
# ‼️ The additions keep the ORDER the loop had — ((t[r] + p0) + p1) + p2, which is
# what `mid = t[r]` then three `mid +=` gives — and `reach` stays a term of its own.
# Folding reach into the bound (mid >= low + reach) is a different float from
# mid - reach >= low, and would move the answer at the boundary.
#
# Only for pairs the rotation memo already covers (both placements one datum, power
# 1, scale 1); any other pair is measured by _inside_split as before.
# _CONTAINMENT_PER_GROUP = False restores it, kept as the reference the fence
# compares every key against.
_CONTAINMENT_PER_GROUP = True


def _containment_plan(rot, half, box):
    """_inside_split's per-(rotation, box) arithmetic, taken once for a group.

    Returns [(axis, p0, p1, p2, reach, low, high)] for each axis the frame is a slab
    on, or None when nothing on this side can slide (which is _inside_split's []).
    """
    if half is None or box is None:
        return None
    lo, hi = box
    plan = []
    for r in range(3):
        if half[r] is None:
            continue
        row = 3 * r
        reach = 0.0
        p = []
        for c in range(3):
            v = rot[row + c]
            p.append(v * (lo[c] + hi[c]) / 2)
            reach += abs(v) * (hi[c] - lo[c]) / 2
        plan.append((r, p[0], p[1], p[2], reach, _SLIDE_MARGIN - half[r], half[r] - _SLIDE_MARGIN))
    return plan


def _inside_planned(plan, t):
    """_inside_split from a _containment_plan and a translation."""
    axes = []
    for r, p0, p1, p2, reach, low, high in plan:
        mid = ((t[r] + p0) + p1) + p2
        if mid - reach >= low and mid + reach <= high:
            axes.append(r)
    return axes


def _key_tail(a, b, qa, qb, ta, tb, inside_i, inside_j):
    """A pair's key from the two relative poses and what each frame contains.

    The last half of _pair_key, taken out so that the array narrow phase builds its
    keys with THIS code and not with a second copy of it: _bulk_keys works out ta,
    tb and the containment one array at a time, and then calls this once per
    distinct row rather than once per pair.

    ‼️ A translation that is already marked (±inf) may be passed in: _slid writes
    exactly those slots with exactly those values, and round(inf, 6) is inf, so the
    pose is the same either way. That is what lets _bulk_keys mark before it dedupes,
    which is the whole reason a rivet row along a stringer is one row and not one a
    rivet.
    """
    if not inside_i and not inside_j:
        # Nothing marked on either side: both poses are _slid's input unchanged,
        # and a real pose's translations are finite, so both mark nothing.
        #
        # min() settles on the first element of the tuple whenever the two
        # definitions differ, so the pose of the direction that cannot win is
        # never built: 3 round() calls and a tuple saved on the 82% of the
        # barrel's pairs that are two different definitions (last hot spots).
        if a < b:
            return (a, b, _pose_of(qa, ta))
        if a > b:
            return (b, a, _pose_of(qb, tb))
        return min((a, b, _pose_of(qa, ta)), (b, a, _pose_of(qb, tb)))
    pose_f, pose_b = _pose_of(qa, ta), _pose_of(qb, tb)
    carried_f = _carried_fast(pose_f, inside_j) if inside_j else []
    carried_b = _carried_fast(pose_b, inside_i) if inside_i else []
    forward = (a, b, _slid(pose_f, inside_i, carried_f))
    backward = (b, a, _slid(pose_b, inside_j, carried_b))
    pf, pb = forward[2], backward[2]
    marked_f = math.isinf(pf[3]) + math.isinf(pf[7]) + math.isinf(pf[11])
    marked_b = math.isinf(pb[3]) + math.isinf(pb[7]) + math.isinf(pb[11])
    if marked_f != marked_b:
        return forward if marked_f > marked_b else backward
    return min(forward, backward)


def _pair_keys(placed, shape_ids, slabs, rotations=None):
    """A function (i, j) -> _pair_key(placed, shape_ids, i, j, slabs), the same key.

    rotations is _measures' per-solid rotation bits, or None for no memo."""
    if not _PAIR_KEY_DIRECT:
        return lambda i, j: _pair_key(placed, shape_ids, i, j, slabs)
    # Indexed by solid: lists, not dicts, at a million solids.
    inverses, rotation_of = [None] * len(placed), [None] * len(placed)
    rotation_ids, memo = {}, {}
    # (rotation i, rotation j, definition a, definition b) -> the two containment
    # plans for that group of pairs (see _containment_plan).
    plans = {}

    def rotation_id(i, location):
        r = rotation_of[i]
        if r is None:
            r = -1
            bits = rotations[i] if rotations is not None else None
            if bits is not None and location.FirstPower() == 1 and location.NextLocation().IsIdentity():
                t = location.Transformation()
                if t.ScaleFactor() == 1.0:
                    r = rotation_ids.setdefault((int(t.Form()), bits), len(rotation_ids))
            rotation_of[i] = r
        return r

    def key(i, j):
        pi, pj = placed[i], placed[j]
        if pi is None or pj is None:
            return None
        li, lj = pi[1].wrapped, pj[1].wrapped
        inv_i = inverses[i]
        if inv_i is None:
            inv_i = inverses[i] = li.Inverted()
        inv_j = inverses[j]
        if inv_j is None:
            inv_j = inverses[j] = lj.Inverted()
        va, vb =(inv_i * lj).Transformation().Value, (inv_j * li).Transformation().Value
        ta, tb = (va(1, 4), va(2, 4), va(3, 4)), (vb(1, 4), vb(2, 4), vb(3, 4))
        ri, rj = rotation_id(i, li), rotation_id(j, lj)
        memoizable = ri >= 0 and rj >= 0 and not (ri == rj and li.IsEqual(lj))
        m = memo.get((ri, rj)) if memoizable else None
        if m is None:
            ra, rb = _rotation(va), _rotation(vb)
            m = (ra, _rounded_rotation(ra), rb, _rounded_rotation(rb))
            if memoizable and len(memo) < _ROTATION_MEMO_LIMIT:
                memo[(ri, rj)] = m
        ra, qa, rb, qb = m
        a, b = shape_ids[pi[0]], shape_ids[pj[0]]
        if not slabs:
            inside_i = inside_j = ()
        else:
            frame_i, frame_j = slabs[pi[0]], slabs[pj[0]]
            if _CONTAINMENT_PER_GROUP and memoizable:
                # One pair of definitions at one pair of rotations is one group.
                g = plans.get((ri, rj, a, b))
                if g is None:
                    g = (_containment_plan(ra, frame_i[0], frame_j[1]),
                         _containment_plan(rb, frame_j[0], frame_i[1]))
                    if len(plans) < _ROTATION_MEMO_LIMIT:
                        plans[(ri, rj, a, b)] = g
                # ‼️ Not named pb: that is the backward POSE a few lines below.
                plan_a, plan_b = g
                inside_i = _inside_planned(plan_a, ta) if plan_a else []
                inside_j = _inside_planned(plan_b, tb) if plan_b else []
            else:
                inside_i = _inside_split(ra, ta, frame_i[0], frame_j[1])
                inside_j = _inside_split(rb, tb, frame_j[0], frame_i[1])
        return _key_tail(a, b, qa, qb, ta, tb, inside_i, inside_j)

    key.memo = memo
    # The groups of pairs containment was taken once for, so a fence can show the
    # grouped path is the path taken (last hot spots).
    key.plans = plans
    return key


# # The narrow phase one ARRAY at a time (interference approach)
#
# #121 took the last micro-optimisation out of the per-pair loop and moved the check
# 0.94-0.98x, and said the rest needed a different approach. This is it.
#
# Profiled 2026-09-16 on the 90,880-occurrence barrel (docs/spikes/2026-09-16-
# interference-approach): of the keying loop, 47% was two OCCT products and six
# Value() calls a pair. A product's translation is, from gp_Trsf::Multiply,
#     loc(A * B) = loc_A + (matrix_A . loc_B) * scale_A
# and for A = inv(L_i) at scale 1 that is  -(R_i^T . t_i) + R_i^T . t_j, with each
# row's sum taken left to right as gp_XYZ::Multiply writes it. Measured, not
# assumed: on the barrel's 197,356 candidate pairs, all 1,184,136 translation
# entries come out of numpy BIT for BIT as OCCT's own product gives them, and all
# 272,640 inverse entries as OCCT's own Inverted() gives them.
#
# So the two things a pair costs that need no rounding — its relative translation
# and its containment test — are computed one array at a time, per GROUP of pairs
# that share two definitions and two rotations (the barrel's 2,191,348 pairs at 1M
# are 536 such groups). Everything that rounds, compares or builds a tuple stays in
# _key_tail, the shipped scalar tail, and is run once per DISTINCT row rather than
# once per pair: 1,232 rows for 197,356 pairs at 90,880 occurrences.
#
# ‼️ The rows are deduped AFTER the slide marks are written into the translations,
# which is what collapses a rivet row along a stringer to one row -- a PERFORMANCE
# device and not a correctness one, and its drill says so: deleting the backward
# half of it left every key and the whole answer unchanged and only made the rows
# finer (scripts/drill-fences.sh records it with the three others that stayed
# green).
#
# ‼️ The rows are deduped on the RAW translations, never on a rounded one, because
# numpy's round is a scale-rint-unscale and Python's is correctly-rounded decimal:
# they are not the same function, and only Python's may decide a key.
#
# ‼️ The keys this builds are EQUAL to the per-pair path's, and are not always the
# same repr: two pairs whose translation rounds to -0.0 and to 0.0 are one dict key
# (they compare equal and hash equal, so the shipped check already measures them as
# one clash) but two reprs. The barrel's 197,356 pairs are 19 distinct key reprs and
# 15 distinct keys; the array path hands out one tuple per key, so 10,243 pairs get
# an equal key with the other zero's sign. Equality is the property the cache uses
# and the property the fence checks.
#
# _BULK_NARROW_PHASE = False restores the per-pair loop, kept as the reference the
# fence compares the whole check's answer against.
_BULK_NARROW_PHASE = True
# A group smaller than this is keyed a pair at a time. The array path pays a fixed
# few dozen numpy calls per group whatever its size, and below about this many pairs
# those cost more than the loop they replace. Not a limit on any answer — both paths
# give the same key, and which one runs changes nothing the caller sees.
_BULK_MIN_GROUP = 16
# How many times the dedupe takes the first row still unmatched and matches every
# row equal to it before handing what is left to numpy's sort. A repetitive assembly
# needs a handful of passes; a model with many distinct poses would need one per
# distinct row, which is what the sort is there to avoid.
_BULK_DEDUPE_ROUNDS = 32


def _distinct_rows(cols):
    """(ids, representatives) for rows made of equal-length 1-D arrays.

    ids[k] says which distinct row k is, and representatives[r] is the index of one
    row of kind r. Float equality is what decides, so -0.0 and 0.0 are one row —
    which is what _key_tail's tuples do too (see the ‼️ above).
    """
    ids = _np.empty(len(cols[0]), dtype=_np.int64)
    left = _np.arange(len(cols[0]))
    reps, nxt = [], 0
    for _ in range(_BULK_DEDUPE_ROUNDS):
        if left.size == 0:
            break
        first = left[0]
        same = cols[0][left] == cols[0][first]
        for c in cols[1:]:
            same &= c[left] == c[first]
        ids[left[same]] = nxt
        reps.append(int(first))
        nxt += 1
        left = left[~same]
    if left.size:
        rows = _np.stack([c[left].astype(float) for c in cols], axis=1)
        uniq, first_at, inv = _np.unique(rows, axis=0, return_index=True,
                                         return_inverse=True)
        ids[left] = nxt + inv.reshape(-1)
        reps.extend(int(left[t]) for t in first_at)
    return ids, reps


def _bulk_keys(placed, shape_ids, slabs, rotations, pairs, frames, stats=None):
    """Every candidate pair's key, as an index into a list of distinct keys.

    Returns (ids, keys, ii, jj): keys[ids[k]] is the key _pair_keys would give
    pairs[k], and is that key by ==, which is how the check's cache reads it; ii and
    jj are the pairs as arrays, which the caller needs anyway.

    A key of None (a solid a feature changed) is given an index of its own for each
    pair that has one, because the check pays a boolean for every one of them.
    """
    ids = _np.full(len(pairs), -1, dtype=_np.int64)
    keys, index = [], {}

    def slot(key):
        s = index.get(key, -1)
        if s < 0:
            s = index[key] = len(keys)
            keys.append(key)
        return s

    ij = _np.array(pairs, dtype=_np.int64)
    ii, jj = ij[:, 0].copy(), ij[:, 1].copy()
    tr = _np.asarray(frames, dtype=float).reshape(len(placed), 3)

    # --- per solid, once: its rotation, and a code for (rotation, definition) -----
    rid = _np.full(len(placed), -1, dtype=_np.int64)
    code = _np.full(len(placed), -1, dtype=_np.int64)
    rot_bits, codes, rot_tab = {}, {}, []
    for k in _np.unique(ij).tolist():
        p = placed[k]
        if p is None:
            continue
        loc = p[1].wrapped
        r = -1
        bits = rotations[k] if rotations is not None else None
        # The same guard _pair_keys' rotation_id uses: one datum, power 1, scale 1.
        if bits is not None and loc.FirstPower() == 1 and loc.NextLocation().IsIdentity():
            t = loc.Transformation()
            if t.ScaleFactor() == 1.0:
                fk = (int(t.Form()), bits)
                r = rot_bits.get(fk, -1)
                if r < 0:
                    r = rot_bits[fk] = len(rot_tab)
                    rot_tab.append(struct.unpack("<9d", bits))
        rid[k] = r
        if r >= 0:
            ck = (r, shape_ids[p[0]])
            c = codes.get(ck, -1)
            if c < 0:
                c = codes[ck] = len(codes)
            code[k] = c

    bulkable = (code[ii] >= 0) & (code[jj] >= 0)
    # ‼️ A pair of EQUAL placements goes to the per-pair path, which is where the
    # same-datum guard lives: TopLoc_Location cancels two copies of one datum to the
    # exact identity, which a product of two different datums is not. Equal entries
    # are a superset of one shared datum, so nothing that needs the guard escapes it.
    bulkable &= ~((rid[ii] == rid[jj]) & (tr[ii, 0] == tr[jj, 0])
                  & (tr[ii, 1] == tr[jj, 1]) & (tr[ii, 2] == tr[jj, 2]))

    nc = max(len(codes), 1)
    gid = _np.where(bulkable, code[ii] * nc + code[jj], -1)
    order = _np.argsort(gid, kind="stable")
    gsorted = gid[order]
    bounds = _np.r_[_np.flatnonzero(_np.r_[True, gsorted[1:] != gsorted[:-1]]),
                    len(gsorted)]

    rel = {}

    def relative(i, j):
        """The two relative rotations for a pair of rotations, from OCCT's own
        product on one representative pair — the memo _pair_keys already keeps."""
        k = (int(rid[i]), int(rid[j]))
        v = rel.get(k)
        if v is None:
            li, lj = placed[i][1].wrapped, placed[j][1].wrapped
            va = (li.Inverted() * lj).Transformation().Value
            vb = (lj.Inverted() * li).Transformation().Value
            ra, rb = _rotation(va), _rotation(vb)
            v = rel[k] = (ra, _rounded_rotation(ra), rb, _rounded_rotation(rb))
        return v

    def product(rot, src, dst):
        """-(R^T . t_src) + (R^T . t_dst): the entries gp_Trsf's own product gives,
        in the order gp_XYZ::Multiply and gp_Trsf::Invert write them."""
        out = _np.empty((len(src), 3))
        ts, td = tr[src], tr[dst]
        for r in range(3):
            a, b, c = rot[r], rot[3 + r], rot[6 + r]
            acc = a * ts[:, 0]
            acc = acc + b * ts[:, 1]
            acc = acc + c * ts[:, 2]
            back = a * td[:, 0]
            back = back + b * td[:, 1]
            back = back + c * td[:, 2]
            out[:, r] = -acc + back
        return out

    def inside_code(plan, t):
        """_inside_planned for a whole group: one bit per axis the frame contains."""
        out = _np.zeros(len(t), dtype=_np.int64)
        if not plan:
            return out
        for r, p0, p1, p2, reach, low, high in plan:
            mid = ((t[:, r] + p0) + p1) + p2
            out |= ((mid - reach >= low) & (mid + reach <= high)).astype(_np.int64) << r
        return out

    leftover, bulked, rows_total = [], 0, 0
    for s, e in zip(bounds[:-1], bounds[1:]):
        sel = order[s:e]
        if gsorted[s] < 0 or (e - s) < _BULK_MIN_GROUP:
            leftover.append(sel)
            continue
        bulked += 1
        gi, gj = ii[sel], jj[sel]
        i0, j0 = int(gi[0]), int(gj[0])
        ra, qa, rb, qb = relative(i0, j0)
        a, b = shape_ids[placed[i0][0]], shape_ids[placed[j0][0]]
        ta = product(rot_tab[int(rid[i0])], gi, gj)
        tb = product(rot_tab[int(rid[j0])], gj, gi)

        plan_a = plan_b = None
        if slabs:
            fi, fj = slabs[placed[i0][0]], slabs[placed[j0][0]]
            plan_a = _containment_plan(ra, fi[0], fj[1])
            plan_b = _containment_plan(rb, fj[0], fi[1])
        variant = inside_code(plan_a, ta) * 8 + inside_code(plan_b, tb)

        tam, tbm = ta.copy(), tb.copy()
        tmpl_a, tmpl_b = _pose_of(qa, (0.0, 0.0, 0.0)), _pose_of(qb, (0.0, 0.0, 0.0))
        marks = {}
        for v in _np.unique(variant).tolist():
            axes_i = [r for r in range(3) if (v // 8) & (1 << r)]
            axes_j = [r for r in range(3) if (v % 8) & (1 << r)]
            marks[v] = (axes_i, axes_j)
            if not axes_i and not axes_j:
                continue
            sub = variant == v
            # _carried_fast reads only the pose's ROTATION, which is the group's, so
            # the marks a pair gets depend on nothing but which axes it contains.
            mf = {r: -math.inf for r in _carried_fast(tmpl_a, axes_j)}
            mf.update({r: math.inf for r in axes_i})
            mb = {r: -math.inf for r in _carried_fast(tmpl_b, axes_i)}
            mb.update({r: math.inf for r in axes_j})
            for r, val in mf.items():
                tam[sub, r] = val
            for r, val in mb.items():
                tbm[sub, r] = val

        rows, reps = _distinct_rows([variant, tam[:, 0], tam[:, 1], tam[:, 2],
                                     tbm[:, 0], tbm[:, 1], tbm[:, 2]])
        rows_total += len(reps)
        local = _np.empty(len(reps), dtype=_np.int64)
        for t, rp in enumerate(reps):
            axes_i, axes_j = marks[int(variant[rp])]
            local[t] = slot(_key_tail(
                a, b, qa, qb,
                (float(tam[rp, 0]), float(tam[rp, 1]), float(tam[rp, 2])),
                (float(tbm[rp, 0]), float(tbm[rp, 1]), float(tbm[rp, 2])),
                list(axes_i), list(axes_j)))
        ids[sel] = local[rows]

    # The per-pair path, built only if some pair needs it: it allocates two lists as
    # long as the solids, which is 16 MB at a million occurrences.
    scalar = _pair_keys(placed, shape_ids, slabs, rotations) if leftover else None
    for k in (_np.concatenate(leftover).tolist() if leftover else ()):
        key = scalar(int(ii[k]), int(jj[k]))
        if key is None:
            ids[k] = len(keys)
            keys.append(None)
        else:
            ids[k] = slot(key)
    if stats is not None:
        stats.update({"groups": bulked, "rows": rows_total, "keys": len(keys),
                      "scalar_pairs": int(sum(len(x) for x in leftover)),
                      "rotation_pairs": len(rel)})
    return ids, keys, ii, jj


def _interferences(solids, ids, labels, placed=None):
    """Pairs of kept solids that share material, worst first.

    placed holds, for each solid, (shape key, location, unplaced shape) when it is
    an untouched copy of a shape built once, or None — see _tessellate.

    Returns the list, whether the budget stopped the search (so a caller never
    reads a truncated answer as a clean one), how many box tests the broad phase
    made, and {"pairs", "booleans", "reused"}: the pairs whose boxes overlap, the
    booleans paid for, and the answers reused from an identical pose (or one slid
    along a box it lies inside; see _INTERFERENCE_SLIDE).
    """
    cached = _INTERFERENCE_CACHE and placed is not None
    rotations = [None] * len(solids) if cached else None
    bulk = cached and _BULK_NARROW_PHASE and _np is not None
    frames = [] if bulk else None
    boxes, volumes = _measures(solids, placed, rotations, frames)
    pairs, box_tests = _candidate_pairs(boxes)

    shape_ids, cache, slabs = {}, {}, {}
    if cached:
        for p in placed:
            if p is not None and p[0] not in shape_ids:
                shape_ids[p[0]] = len(shape_ids)
                if _INTERFERENCE_SLIDE:
                    slabs[p[0]] = _slabs(p[0], p[2])

    if bulk and pairs:
        return _bulk_interferences(solids, ids, labels, placed, shape_ids, slabs,
                                   rotations, frames, volumes, pairs, box_tests)

    found, truncated, booleans, reused = [], False, 0, 0
    pair_key = _pair_keys(placed, shape_ids, slabs, rotations) if cached else None
    for i, j in pairs:
        key = pair_key(i, j) if cached else None
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
        # A tuple, not the reply's dict: at 1M there are 1.76 M of these and only
        # _INTERFERENCE_LIST_LIMIT of them are written down.
        found.append((shared / smaller, len(found), lo, hi, shared))

    # Worst first, ties in the order they were found — what a stable sort by fraction,
    # descending, gives — and only the first _INTERFERENCE_LIST_LIMIT of that order.
    worst = heapq.nsmallest(_INTERFERENCE_LIST_LIMIT, found, key=lambda f: (-f[0], f[1]))
    listed = [{"a": ids[lo], "b": ids[hi], "a_label": labels[lo], "b_label": labels[hi],
               "volume": shared, "fraction": fraction}
              for fraction, _, lo, hi, shared in worst]
    # Every buried clash found, not the listed ones: see _BURIED_FRACTION.
    buried = sum(1 for f in found if f[0] >= _BURIED_FRACTION)
    return listed, truncated, box_tests, {"pairs": len(pairs), "booleans": booleans, "reused": reused,
                                          "found": len(found), "summarized": len(found) > len(listed),
                                          "buried": buried}


def _bulk_interferences(solids, ids, labels, placed, shape_ids, slabs, rotations,
                        frames, volumes, pairs, box_tests):
    """_interferences' narrow phase, measured one array at a time.

    The same answer as the loop above, reached without a Python iteration per pair:
    the same booleans on the same pairs in the same order, the same list in the same
    order, the same counts. The loop is kept beside this rather than replaced by it,
    as the reference the fence compares the whole answer against
    (testdata/interference_bulk_keys.py, _BULK_NARROW_PHASE off and on).
    """
    of_pair, keys, ii, jj = _bulk_keys(placed, shape_ids, slabs, rotations, pairs, frames)

    # Which pair each distinct key is FIRST met at — numpy's unique returns exactly
    # that — and so the order the loop would pay its booleans in.
    slots, first_at = _np.unique(of_pair, return_index=True)
    first_of = dict(zip(slots.tolist(), first_at.tolist()))
    cache, shared_of = {}, _np.full(len(keys), _np.nan)
    booleans, truncated, reached = 0, False, len(pairs)
    for s in slots[_np.argsort(first_at, kind="stable")].tolist():
        key = keys[s]
        if key is not None and key in cache:
            v = cache[key]
            if v is not None:
                shared_of[s] = v
            continue
        if booleans >= _INTERFERENCE_PAIR_BUDGET:
            truncated = True
            reached = first_of[s]
            break
        booleans += 1
        i, j = pairs[first_of[s]]
        try:
            shared = float(getattr(solids[i] & solids[j], "volume", 0.0))
        except Exception:
            # OCCT refusing a boolean is not evidence of interference; see the loop.
            shared = None
        if key is not None:
            cache[key] = shared
        if shared is not None:
            shared_of[s] = shared
    # Every pair the loop reached was either a boolean or a reuse, and nothing else.
    reused = reached - booleans

    ii, jj = ii[:reached], jj[:reached]
    share = shared_of[of_pair[:reached]]
    vol = _np.asarray(volumes, dtype=float)
    vi, vj = vol[ii], vol[jj]
    # A refused boolean is a nan here and a None there: both are passed over. A face
    # has no volume and no answer to "how much of it is buried"; see the loop.
    keep = ~_np.isnan(share) & (vi > 0) & (vj > 0) & (share > 0)
    fraction = _np.zeros(reached)
    _np.divide(share, _np.minimum(vi, vj), out=fraction, where=keep)
    keep &= (share >= _INTERFERENCE_MIN_VOLUME) & (fraction >= _INTERFERENCE_MIN_FRACTION)

    at = _np.flatnonzero(keep)
    fr = fraction[at]
    buried = int((fr >= _BURIED_FRACTION).sum())
    # Worst first, ties in the order they were found: `at` is ascending in pair
    # index, which is the order the loop appends in, and a stable sort keeps it.
    worst = at[_np.argsort(-fr, kind="stable")[:_INTERFERENCE_LIST_LIMIT]]
    # The SMALLER solid first, because the fraction is its share; see the loop.
    swap = vol[ii[worst]] > vol[jj[worst]]
    lows = _np.where(swap, jj[worst], ii[worst]).tolist()
    highs = _np.where(swap, ii[worst], jj[worst]).tolist()
    listed = [{"a": ids[lo], "b": ids[hi], "a_label": labels[lo], "b_label": labels[hi],
               "volume": float(share[k]), "fraction": float(fraction[k])}
              for lo, hi, k in zip(lows, highs, worst.tolist())]
    return listed, truncated, box_tests, {"pairs": len(pairs), "booleans": booleans,
                                          "reused": reused, "found": len(at),
                                          "summarized": len(at) > len(listed),
                                          "buried": buried}


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


# # A tuple, not a JSON string (fences that can fail)
#
# Measured 2026-09-15 (docs/spikes/2026-09-15-last-hot-spots): json.dumps of these
# ten fields was 4.03 us of the 44.2 us an occurrence paid in the shapes phase, and
# json.encoder.iterencode was 0.446 s of own time at 90,880 occurrences. #121
# measured the tuple form at 1.28 us and left it unapplied, because the key was
# PARSED BACK as JSON by _slabs, which made it a contract between two functions
# rather than a local choice.
#
# Re-measured 2026-09-16 with that same micro-benchmark on a quiet machine (6-22%
# CPU), two passes, before and after interleaved: 4.20 and 4.02 us become 0.514 and
# 0.523 us. That is below #121's own repr-of-every-field tuple, which the same
# benchmark puts at 1.42-1.50 us, because a scalar field here is not stringified at
# all. ‼️ The end-to-end effect is much smaller than the micro number: the shapes
# phase of a 90,880-occurrence synthetic model goes 2.68/2.76 s to 2.59/2.55 s, about
# 1.65 us an occurrence rather than 3.6, and the whole build moves 4.20 s to 4.13 s,
# which is inside run-to-run noise. The change is worth having and it is cheap, but
# what is MEASURED is the micro-benchmark, not a build that got faster.
#
# The tuple below is that contract made explicit instead of textual, and it is
# cheaper again than a tuple of ten reprs, because:
#
#   - a scalar field goes in RAW. "shape", "axis" and "mirrored" are already
#     hashable, and "step" is a whole imported STEP file - repr() or json.dumps()
#     of that copies the text once per occurrence, where the tuple just holds it;
#   - ‼️ "dims" is CANONICALIZED, not repr'd. It is map[string]float64 on the Go
#     side (geometry/solid.go), so its key order is not part of the contract and
#     two solids equal in every dimension must land on ONE key however the map
#     was written - which is exactly what json.dumps(sort_keys=True) gave and a
#     plain repr() of a dict would silently lose, splitting one definition in two;
#   - the remaining fields are structs and slices on the Go side ("outline" and
#     "path" are *Curve, "holes" []Curve, "section_frame" *[9]float64), so their
#     order IS the contract and repr() of them is canonical already.
#
# It never crosses the JSON boundary, which is why no text form is kept: a key is
# a dict key in built_once, in _tessellate's `index`, in _measures' and _entries'
# `local`, in _assembly_volume's `counted` and in _interferences' `shape_ids` and
# `slabs`, and the reply refers to a definition by its INTEGER index
# (mesh_instances' "definition"), never by its key. It is never logged or
# formatted into a message either - every sentence a build returns names a part
# by its label or id (see `skipped`).
#
# Fences: TestKernel_APlacedCopyIsTheCopyBuild123dMade (every copy, mesh, STEP and
# part property is still build123d's) and
# TestKernel_APairKeyWithoutLocationsIsTheKeyBuild123dGave (every slab is still
# read from the key), with drills for a dropped field and for dims losing its
# canonical order.
def _shape_key(solid):
    dims = solid.get("dims")
    return (solid.get("shape"), None if dims is None else tuple(sorted(dims.items())),
            repr(solid.get("outline")), repr(solid.get("holes")),
            repr(solid.get("hole_parents")), repr(solid.get("path")),
            repr(solid.get("section_frame")), solid.get("axis"),
            solid.get("step"), solid.get("mirrored"))


# The assembly's volume from each definition's, once per definition (next scale walls).
#
# Compound.volume wraps every child in a Python Solid and integrates each one:
# 16.4 s of a 90,880-occurrence barrel's 20 s assembly phase, 7.4 s of it the
# wrappers (docs/spikes/2026-09-15-next-scale-walls). A copy is its shape moved,
# and a rigid motion does not change a volume, so a copy counts what its shape
# counts. A part a feature changed is measured as the solid it is.
#
# ‼️ Equal to about 1e-12, not to the bit: OCCT integrates a located solid in its
# placed coordinates, so the same solid moved reports a volume differing in the last
# digits. Fence: TestKernel_APlacedCopyIsTheCopyBuild123dMade, to 1e-9 of the whole.
# The switch keeps Compound.volume available as that reference.
_ASSEMBLY_VOLUME_PER_DEFINITION = True


def _counted_volume(shape):
    """What Compound.volume counts for one child: its own solids and shells, by the
    same rule, because it is the same call on a compound of that child alone."""
    return float(getattr(Compound([shape]), "volume", 0.0))


def _assembly_volume(assembly, built, placed):
    if not _ASSEMBLY_VOLUME_PER_DEFINITION:
        return float(getattr(assembly, "volume", 0.0))
    total, counted = 0.0, {}
    for solid, p in zip(built, placed):
        if p is None:
            total += _counted_volume(solid)
            continue
        if p[0] not in counted:
            counted[p[0]] = _counted_volume(p[2])
        total += counted[p[0]]
    return total


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
    Fence: TestKernel_ExportingManyOccurrencesGrowsLinearly, to 4,096; past it, the
    writer's own cost is fenced by TestKernel_ExportTimeGrowsLinearlyPastTheBuildCeiling
    (see _STEP_WRITE_PROPS).

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
    # ‼️ No application.NewDocument(format, doc) here any more (kernel last walls,
    # 2026-09-17). Through OCP that call cannot hand its document back: the C++
    # out-parameter is a Handle the binding does not write through, so it opened a
    # SECOND, empty document inside the application and left `doc` — the one this
    # function fills and the writer writes — unopened (doc.IsOpened() is False either
    # way). The application kept every one of them: NbDocuments() was 1, 2, 3 ... after
    # each export, for the life of the process, and none could be closed (Close needs
    # the handle, and GetDocument has the same out-parameter). InitDocument(doc) is
    # what makes `doc` an XDE document, and it still runs. Measured in the worker's
    # image: six 90,880-occurrence exports each wrote the same STEP body with and
    # without the call, and the application held 0 documents after them instead of 6.
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


# Whether the STEP writer looks for validation properties (area, volume, centroid
# attributes) on every label. Off: the sidecar sets none, so there is nothing to
# find, and looking is quadratic in the occurrences under one assembly.
#
# # Why
#
# Measured 2026-09-15 (docs/spikes/2026-09-15-step-export-scaling) on the airframe
# barrel: STEPCAFControl_Writer.Transfer took 0.6 s at 10k occurrences, 3.2 s at 30k
# and 20 s at 90k, while the XCAF document it transfers was built in time linear in
# the occurrences. OCCT 7.9's WritePropsForLabel visits an assembly's children as
# `for i = 1 .. label.NbChildren(): label.FindChild(i)`, and both calls walk the
# child list from its head, so N components under one assembly cost ~N² steps
# before a single property is written. With props mode off, Transfer is 1.0 s at
# 30k where it was 2.3–3.2 s, and the file is byte-identical below its header.
#
# ‼️ If the sidecar ever writes XCAFDoc_Area, XCAFDoc_Volume or XCAFDoc_Centroid
# attributes, turning this back on is not enough: they would be written at N² cost
# again. Fences: TestKernel_ExportTimeGrowsLinearlyPastTheBuildCeiling and
# TestKernel_ALargeExportIsTheFileThePropertyWalkWrote. Module-level so
# testdata/step_export_scaling.py can write the old file to compare against.
_STEP_WRITE_PROPS = False


def _step_mesh_only_note(mesh_only):
    """The FILE_DESCRIPTION line that says which mesh-only parts this file does not
    hold, or None. Geometry/lattice.go's MeshOnlyNote says the same to a person."""
    if not mesh_only:
        return None
    names = _mesh_only_names(mesh_only)
    shown = ", ".join(names[:3]) + (" and %d more" % (len(names) - 3) if len(names) > 3 else "")
    # STEP header strings are ISO 10303-21 text: plain ASCII, no apostrophes.
    text = ("FORGE: %d mesh-only part(s) are NOT in this file: %s. Mesh-only parts are "
            "decorative, not manufacturable and never structural." % (len(names), shown))
    return text.encode("ascii", "replace").decode("ascii").replace("'", " ")


def _write_step(doc, path, phases=None, note=None):
    """Write an XDE document as STEP with the settings export_step used, so the
    file's header, curves and precision do not change with K2.

    When given `phases`, records the writer's Transfer alone as "export_transfer".
    The whole export phase is too blunt to fence it: writing the file and encoding
    it are linear and, past the ceiling, hide a quadratic Transfer (measured at
    32,768 occurrences: 4.7 s with the property walk, 3.0 s without, and the two
    swapped places under load). Go reads only the phases it names, so this one
    reaches the scaling fence and nothing else."""
    # OCCT prints to the console by default, and this process's stdout is the
    # protocol: a stray line there is an unreadable reply.
    for printer in Message.DefaultMessenger_s().Printers():
        printer.SetTraceLevel(Message_Gravity.Message_Fail)
    writer = STEPCAFControl_Writer(XSControl_WorkSession(), False)
    writer.SetColorMode(True)
    writer.SetLayerMode(True)
    writer.SetNameMode(True)
    writer.SetPropsMode(_STEP_WRITE_PROPS)
    header = APIHeaderSection_MakeHeader(writer.Writer().Model())
    if not header.IsDone():
        header = APIHeaderSection_MakeHeader(0)
        header.Apply(writer.Writer().Model())
    header.SetOriginatingSystem(TCollection_HAsciiString("build123d"))
    if note:
        # Appended to FILE_DESCRIPTION, keeping what the writer put there, so a
        # file with no mesh-only part is the file it always was.
        lines = [header.DescriptionValue(i).ToCString() for i in range(1, header.NbDescription() + 1)]
        lines.append(note)
        described = Interface_HArray1OfHAsciiString(1, len(lines))
        for i, line in enumerate(lines, 1):
            described.SetValue(i, TCollection_HAsciiString(line))
        header.SetDescription(described)
    STEPCAFControl_Controller.Init_s()
    STEPControl_Controller.Init_s()
    Interface_Static.SetIVal_s("write.surfacecurve.mode", 1)
    Interface_Static.SetIVal_s("write.precision.mode", PrecisionMode.AVERAGE.value)
    transfer = time.perf_counter()
    writer.Transfer(doc, STEPControl_StepModelType.STEPControl_AsIs)
    if phases is not None:
        phases["export_transfer"] = time.perf_counter() - transfer
    if writer.Write(path) != IFSelect_ReturnStatus.IFSelect_RetDone:
        raise RuntimeError("the STEP writer could not write the file")


# # A build without Python's cycle collector (one million after)
#
# Measured 2026-09-17 on the airframe barrel (docs/spikes/2026-09-17-one-million-after):
# a build allocates a handful of Python objects per occurrence (a Location, a placed
# copy, its attributes, tuples and list entries) and keeps nearly all of them until it
# returns. CPython's generational collector is triggered by allocation counts, so it
# ran 501 times during a 90,880-occurrence build with the check replaced and spent
# 1.23-1.33 s of its 6.5-6.6 s traversing objects that were all still alive — and
# found nothing: `gc.collect()` straight after a build with the collector off finds
# 0 unreachable objects, with the same peak RSS to 1 MB.
#
# Reference counting still frees everything as before; only the CYCLE collector is
# paused, for the duration of one request, and restored however the build ends. The
# answer cannot depend on it (no code here reads gc state or relies on a finalizer
# running at a particular moment); the fence builds every format with it on and off
# and compares the replies whole.
#
# ‼️ What this gives up: a reference cycle created during a build is freed when the
# collector next runs after it, not during it. A build that made cycles per occurrence
# would hold them to its end. None of the measured fixtures makes any (the fence counts
# them), and the collector is restored before the next request is read.
#
# _BUILD_WITHOUT_GC = False keeps the collector running, as before.
_BUILD_WITHOUT_GC = True


def _build(request):
    """_build_collected with the cycle collector paused, when it was running."""
    if not _BUILD_WITHOUT_GC or not gc.isenabled():
        return _build_collected(request)
    gc.disable()
    try:
        return _build_collected(request)
    finally:
        gc.enable()


def _build_collected(request):
    # Mesh-only parts leave here, before anything is built: nothing below — OCCT,
    # the features, the volume, the properties, the interference check, the STEP
    # file — ever sees one (see _lattice_mesh).
    solids, mesh_only = _split_mesh_only(request.get("solids") or [])
    if not solids and mesh_only:
        return _only_mesh_only(request, mesh_only)
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
    # matrix -> the directions its Plane holds, for this build (see _placement).
    frames = {}
    # id(definition shape) -> what deepcopy does to each of its attributes, for THIS
    # build only (see _located): the ids are stable while built_once holds the
    # shapes, and a plan must never outlive the definition it was taken from.
    plans = {}
    for s in solids:
        key = _shape_key(s)
        if key in built_once:
            shape, reason = built_once[key]
            if shape is None:
                skipped.append("%s: %s" % (s.get("label") or s.get("id"), reason))
                continue
            location = _placement(s, frames)
            built.append(_located(shape, location, plans))
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
        location = _placement(s, frames)
        built.append(_located(shape, location, plans))
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
    # Each part as placed, before any feature, and what fuses welded into what:
    # the "joins" edge rule's evidence (looks designed, stage B2).
    history = {"placed": dict(shapes), "fused": {}}
    # How many edges each round selected, and every round built smaller or left
    # partly square (stage B1) — both said in the reply, never kept back.
    report = {"edges": {}, "reduced": []}
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
            _apply(op, shapes, history, report)
        except Exception as exc:
            reason = str(exc).strip() or type(exc).__name__
            failed.append("%s: %s" % (op["id"], _with_a_way_out(op, shapes, reason, history)))
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
    volume = _assembly_volume(assembly, built, kept_placed)
    mark = _lap(phases, "assembly", mark)
    # Computed on the KEPT solids, which is the whole reason it is trustworthy —
    # see the note above _interferences. Always, not on request: a check that a
    # caller has to remember to ask for is a check that is off in the one
    # deployment that needed it, and the broad phase makes the usual case free.
    #
    # ‼️ One caller opts out: the off-node STEP export job (cad.Kernel.ExportSTEPJob)
    # sends skip_interferences, because its file does not carry the answer and
    # its memory ceiling rests on STEP export measured without this check. Absent
    # means the check runs, so every other caller still cannot forget it.
    if request.get("skip_interferences"):
        # No lap either: a phase that did not run reports no time, so a reply
        # cannot be read as a check that ran and found nothing.
        clashes, clash_truncated, box_tests = [], False, 0
        clash_pairs = {"pairs": 0, "booleans": 0, "reused": 0}
    else:
        clashes, clash_truncated, box_tests, clash_pairs = _interferences(built, ids, names, kept_placed)
        mark = _lap(phases, "interferences", mark)
    properties = None
    if request.get("properties"):
        properties = _properties(built, ids, kept_placed)
        mark = _lap(phases, "properties", mark)
    # The manufacturability measurements and the named sections, on the SAME kept
    # solids the interference check ran on and for the same reason: a check of the
    # shape as DESCRIBED would measure the wall of a plate before its pocket was
    # cut. Asked for, unlike interference: it costs a ray per face and the export
    # job's ceiling was measured without it (see cad.Kernel.BuildEvaluated).
    manufacturability = mfg_stats = None
    mfg_truncated = False
    if request.get("manufacturability"):
        manufacturability, mfg_truncated, mfg_stats = _manufacturability(built, ids, kept_placed)
        mark = _lap(phases, "manufacturability", mark)
    sections = None
    if request.get("sections"):
        sections = _sections(dict(zip(ids, built)), request)
        mark = _lap(phases, "sections", mark)
    out = {
        "shape_builds": shape_builds,
        "ok": True,
        "parts": len(built),
        "interferences": clashes,
        "interferences_truncated": clash_truncated,
        # How many clashes were found, and whether "interferences" lists only the
        # worst of them (_INTERFERENCE_LIST_LIMIT). A replaced check that does not
        # count them lists every one it has.
        "interferences_found": clash_pairs.get("found", len(clashes)),
        "interferences_summarized": clash_pairs.get("summarized", False),
        # How many of the clashes found are buried, the number a repair is judged by.
        # A replaced check that does not count them counts the ones it lists.
        "interferences_buried": clash_pairs.get(
            "buried", sum(1 for c in clashes if c["fraction"] >= _BURIED_FRACTION)),
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
        # Stage B1: a round built smaller than asked, or on only some of its
        # edges, is applied AND said. Stage B2: how many edges each round's rule
        # selected, so a rule that picks the wrong edges can be seen.
        "features_reduced": report["reduced"],
        "feature_edges": report["edges"],
    }
    if properties is not None:
        out["part_properties"] = properties
    if manufacturability is not None:
        out["manufacturability"] = manufacturability
        out["manufacturability_truncated"] = mfg_truncated
        out["manufacturability_measured"] = mfg_stats["checked"]
        out["manufacturability_parts"] = mfg_stats["parts"]
        out["manufacturability_faces"] = mfg_stats["faces"]
        out["manufacturability_reused"] = mfg_stats["reused"]
    if sections is not None:
        out["sections"] = sections
    if mesh_only:
        # Named in every reply, so each reader can say what it left out.
        out["mesh_only"] = _mesh_only_names(mesh_only)

    fmt = request.get("format")
    if fmt == "mesh":
        out.update(_tessellate(built, ids, names, request, kept_placed))
        if mesh_only and "mesh" in out:
            meshes, triangles, refused = _mesh_only_parts(mesh_only)
            out["mesh"].extend(meshes)
            out["mesh_triangles"] += triangles
            out["mesh_only_triangles"] = triangles
            skipped.extend(refused)
        mark = _lap(phases, "mesh", mark)
    if fmt == "step":
        # The writer writes a file; its stream form is not used here.
        # Deleted immediately after reading, and created with mkstemp so a
        # concurrent build cannot collide with it.
        fd, path = tempfile.mkstemp(suffix=".step")
        os.close(fd)
        try:
            _write_step(_step_document(built, names), path, phases,
                        _step_mesh_only_note(mesh_only))
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


# # Memory handed back after a reply (kernel last walls)
#
# Measured 2026-09-17 on Linux (docs/spikes/2026-09-17-kernel-last-walls, the worker's
# forge-linux-test image, glibc 2.36): after a 90,880-occurrence STEP export the kernel
# held 1,185 MiB with the request and reply released, of which glibc's heap had only
# 127 MiB IN USE and 681 MiB FREE — freed by OCCT and Python, kept by the allocator
# because a few live chunks sit above it in the heap. That is #145's "memory does not
# come back after an export" (733 -> 1,385 -> 1,280 MiB). gc.collect() found 0 objects
# and returned nothing; malloc_trim(0) returned the free pages in 23-29 ms and the
# process fell to 506-543 MiB, export after export.
#
# So after each reply is written, the loop drops its request and reply and asks glibc
# for malloc_trim(0). It never touches memory in use: it gives back pages that are
# already free, and the next build takes them from the kernel again as it would have
# the first time. Only glibc has it: on musl, macOS or Windows _malloc_trim is None and
# _release_memory does nothing. It runs after the reply is flushed, so no caller waits
# on it; the next request is read at most a few tens of ms later.
#
# _RELEASE_AFTER_REPLY = False keeps the allocator's pages, as before.
_RELEASE_AFTER_REPLY = True
_malloc_trim_fn = False  # not looked up yet


def _malloc_trim():
    """glibc's malloc_trim, or None where there is none."""
    global _malloc_trim_fn
    if _malloc_trim_fn is False:
        _malloc_trim_fn = None
        if sys.platform.startswith("linux"):
            try:
                import ctypes

                fn = ctypes.CDLL("libc.so.6").malloc_trim
                fn.argtypes = [ctypes.c_size_t]
                fn.restype = ctypes.c_int
                _malloc_trim_fn = fn
            except (OSError, AttributeError):
                _malloc_trim_fn = None
    return _malloc_trim_fn


def _release_memory():
    """Hand the allocator's free pages back to the OS. True when it ran."""
    if not _RELEASE_AFTER_REPLY:
        return False
    trim = _malloc_trim()
    if trim is None:
        return False
    trim(0)
    return True


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
        # Nothing of this request is needed any more; see _release_memory.
        line = request = reply = None
        _release_memory()


if __name__ == "__main__":
    main()
