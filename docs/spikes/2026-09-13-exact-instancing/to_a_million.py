"""Spike 2: does the XDE assembly path stay linear to a million, and is a shared
definition's mesh computed once for every copy?"""
import os, sys, time, tempfile, resource
from build123d import Box, Cylinder, Pos, Location
from OCP.TDocStd import TDocStd_Document
from OCP.TCollection import TCollection_ExtendedString
from OCP.XCAFDoc import XCAFDoc_DocumentTool
from OCP.STEPCAFControl import STEPCAFControl_Writer
from OCP.STEPControl import STEPControl_AsIs
from OCP.TopLoc import TopLoc_Location
from OCP.gp import gp_Trsf, gp_Vec
from OCP.IFSelect import IFSelect_RetDone
from OCP.BRepMesh import BRepMesh_IncrementalMesh
from OCP.BRep import BRep_Tool
from OCP.TopExp import TopExp_Explorer
from OCP.TopAbs import TopAbs_FACE
from OCP.TopoDS import TopoDS

base = Box(20, 10, 4) - Pos(5, 0, 0) * Cylinder(2, 10)

def rss_mb():
    r = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    return r / (1024 * 1024) if sys.platform == "darwin" else r / 1024

tmp = tempfile.mkdtemp()
print("A. XDE assembly, one definition, N instances (build = add components; export = STEPCAF write)")
print("   %-9s %-9s %-9s %-14s %-9s %s" % ("N", "build s", "export s", "STEP bytes", "b/copy", "peak RSS MB"))
for n in (10000, 100000, 1000000):
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
    tb = time.perf_counter() - t0
    path = os.path.join(tmp, "xde%d.step" % n)
    w = STEPCAFControl_Writer()
    t0 = time.perf_counter()
    ok = w.Transfer(doc, STEPControl_AsIs) and w.Write(path) == IFSelect_RetDone
    te = time.perf_counter() - t0
    size = os.path.getsize(path)
    print("   %-9d %-9.3f %-9.3f %-14d %-9.0f %.0f ok=%s" % (n, tb, te, size, size / n, rss_mb(), ok))
    os.unlink(path)
    sys.stdout.flush()
    del doc, tool, w

print("\nB. Is a definition's triangulation shared by its located copies?")
d = Box(20, 10, 4) - Pos(5, 0, 0) * Cylinder(2, 10)
t0 = time.perf_counter(); BRepMesh_IncrementalMesh(d.wrapped, 0.05, False, 0.5, True); t_mesh = time.perf_counter() - t0
copy = d.moved(Location((500, 0, 0)))
def tris(shape):
    total, have, exp = 0, 0, TopExp_Explorer(shape.wrapped, TopAbs_FACE)
    while exp.More():
        f = TopoDS.Face_s(exp.Current()); loc = TopLoc_Location()
        tri = BRep_Tool.Triangulation_s(f, loc)
        if tri is not None:
            have += 1; total += tri.NbTriangles()
        exp.Next()
    return have, total
print("   meshing the definition took %.4f s" % t_mesh)
print("   definition: faces with triangulation, triangles =", tris(d))
print("   located copy (never meshed itself):", tris(copy), "-> shared if equal and nonzero")
