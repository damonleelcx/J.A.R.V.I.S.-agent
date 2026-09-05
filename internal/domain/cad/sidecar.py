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
import os
import sys
import tempfile
import traceback

PROTOCOL = 1

try:
    from build123d import (
        Box, Cylinder, Cone, Sphere, Rectangle, Plane, Location, Vector,
        Compound, export_step,
    )
except Exception as exc:  # pragma: no cover - reported to the caller, not raised
    sys.stdout.write(json.dumps({
        "ready": False,
        "error": "build123d could not be imported: %s" % exc,
    }) + "\n")
    sys.stdout.flush()
    sys.exit(1)


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
    if kind in ("cylinder", "tube", "cone"):
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
    if kind == "plane":
        # A face, not a solid, and deliberately so: a plane has no thickness and
        # will not print, machine, or hold a volume. It is exported because it is
        # part of what was drawn, and the label says what it is.
        return Plane.XZ * Rectangle(d["width"], d["depth"])
    raise ValueError("unsupported shape %r" % kind)


def _build(request):
    solids = request.get("solids") or []
    if not solids:
        return {"ok": False, "error": "no parts to build"}

    built, names, skipped = [], [], []
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

    if not built:
        return {"ok": False, "error": "no part could be built", "skipped": skipped}

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
