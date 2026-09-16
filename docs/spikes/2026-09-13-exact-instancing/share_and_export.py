"""Spike: can FORGE place one EXACT solid N times cheaply, and export it exactly?

Question 1: does a located copy share the underlying B-rep (TShape) — exact, not tessellated?
Question 2: is the copy's geometry exact (volume, bounds) at its new place?
Question 3: how do build time and STEP file size grow with N — linearly (every copy
            written in full) or sub-linearly (one definition, N placements)?
Question 4: does an XDE/OCAF assembly export (shared product + N instances) differ
            from exporting a flat compound?
"""
import os, sys, time, tempfile
from build123d import Box, Cylinder, Pos, Rot, Compound, Location, export_step

def definition():
    # A small but non-trivial part: a bracket with a hole, about 1 MB of topology? no — a few dozen faces.
    return Box(20, 10, 4) - Pos(5, 0, 0) * Cylinder(2, 10)

base = definition()
print("definition faces=%d volume=%.4f" % (len(base.faces()), base.volume))

# Q1 + Q2
a = base.moved(Location((100, 0, 0)))
b = base.moved(Location((0, 250, 0), (0, 0, 90)))
shares = a.wrapped.TShape() == base.wrapped.TShape() and b.wrapped.TShape() == base.wrapped.TShape()
print("Q1 located copies share the TShape (exact, no duplication):", shares)
print("Q1 IsPartner(base):", a.wrapped.IsPartner(base.wrapped), " IsSame(base):", a.wrapped.IsSame(base.wrapped))
print("Q2 copy volume=%.4f (base %.4f)  copy bbox min=(%.2f,%.2f,%.2f)" % (
    a.volume, base.volume, a.bounding_box().min.X, a.bounding_box().min.Y, a.bounding_box().min.Z))

def flat_step(n, path):
    side = int(n ** 0.5) + 1
    kids = [base.moved(Location((i % side * 30.0, (i // side) * 30.0, 0))) for i in range(n)]
    t0 = time.perf_counter()
    comp = Compound(children=kids)
    t_build = time.perf_counter() - t0
    t0 = time.perf_counter()
    export_step(comp, path)
    t_export = time.perf_counter() - t0
    return t_build, t_export, os.path.getsize(path), sum(k.volume for k in kids[:3])

def deep_copy_step(n, path):
    side = int(n ** 0.5) + 1
    t0 = time.perf_counter()
    kids = [Pos(i % side * 30.0, (i // side) * 30.0, 0) * definition() for i in range(n)]
    t_build = time.perf_counter() - t0
    t0 = time.perf_counter()
    export_step(Compound(children=kids), path)
    return t_build, time.perf_counter() - t0, os.path.getsize(path)

tmp = tempfile.mkdtemp()
print("\nQ3 shared-TShape compound, export_step:")
print("  %-7s %-10s %-10s %-12s %s" % ("N", "build s", "export s", "STEP bytes", "bytes/copy"))
for n in (1, 10, 100, 1000, 10000):
    tb, te, size, _ = flat_step(n, os.path.join(tmp, "flat%d.step" % n))
    print("  %-7d %-10.3f %-10.3f %-12d %.0f" % (n, tb, te, size, size / n))

print("\nQ3b rebuilt-every-time (no sharing), for contrast:")
for n in (1, 100, 1000):
    tb, te, size = deep_copy_step(n, os.path.join(tmp, "deep%d.step" % n))
    print("  N=%-6d build %.3fs export %.3fs STEP %d bytes (%.0f/copy)" % (n, tb, te, size, size / n))

# Q4: XDE assembly with one shared label referenced N times
try:
    from OCP.TDocStd import TDocStd_Document
    from OCP.TCollection import TCollection_ExtendedString
    from OCP.XCAFDoc import XCAFDoc_DocumentTool
    from OCP.STEPCAFControl import STEPCAFControl_Writer
    from OCP.STEPControl import STEPControl_AsIs
    from OCP.TopLoc import TopLoc_Location
    from OCP.gp import gp_Trsf, gp_Vec
    from OCP.IFSelect import IFSelect_RetDone

    def xde_step(n, path):
        doc = TDocStd_Document(TCollection_ExtendedString("XmlOcaf"))
        tool = XCAFDoc_DocumentTool.ShapeTool_s(doc.Main())
        root = tool.NewShape()
        part = tool.AddShape(base.wrapped, False)
        side = int(n ** 0.5) + 1
        t0 = time.perf_counter()
        for i in range(n):
            tr = gp_Trsf(); tr.SetTranslation(gp_Vec(i % side * 30.0, (i // side) * 30.0, 0))
            tool.AddComponent(root, part, TopLoc_Location(tr))
        tool.UpdateAssemblies()
        t_build = time.perf_counter() - t0
        w = STEPCAFControl_Writer()
        t0 = time.perf_counter()
        ok = w.Transfer(doc, STEPControl_AsIs)
        status = w.Write(path)
        return t_build, time.perf_counter() - t0, os.path.getsize(path), ok and status == IFSelect_RetDone

    print("\nQ4 XDE assembly (one product definition, N instances):")
    for n in (1, 10, 100, 1000, 10000):
        tb, te, size, ok = xde_step(n, os.path.join(tmp, "xde%d.step" % n))
        print("  N=%-6d build %.3fs export %.3fs STEP %-10d bytes (%.0f/copy) ok=%s" % (n, tb, te, size, size / n, ok))
except Exception as exc:
    print("\nQ4 XDE path unavailable:", type(exc).__name__, exc)
