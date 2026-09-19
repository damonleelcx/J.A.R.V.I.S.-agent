import sys, math, time
from build123d import *
from OCP.ChFi3d import ChFi3d
from OCP.ChFiDS import ChFiDS_TypeOfConcavity
from OCP.TopExp import TopExp
from OCP.TopTools import TopTools_IndexedDataMapOfShapeListOfShape
from OCP.TopAbs import TopAbs_EDGE, TopAbs_FACE
from OCP.TopoDS import TopoDS
from OCP.BRep import BRep_Tool


def classify(shape):
    m = TopTools_IndexedDataMapOfShapeListOfShape()
    TopExp.MapShapesAndAncestors_s(shape.wrapped, TopAbs_EDGE, TopAbs_FACE, m)
    out = {}
    for e in shape.edges():
        faces = m.FindFromKey(e.wrapped)
        fl = [TopoDS.Face_s(f) for f in faces]
        uniq = []
        for f in fl:
            if not any(f.IsSame(u) for u in uniq):
                uniq.append(f)
        if len(uniq) != 2:
            k = "seam/free(%d)" % len(uniq)
        else:
            t = ChFi3d.DefineConnectType_s(e.wrapped, uniq[0], uniq[1], 1e-6, True)
            k = str(t).split('.')[-1]
        out[k] = out.get(k, 0) + 1
    return out

box = Box(100, 20, 50)
print("box", len(box.edges()), classify(box))
L = extrude(make_face(Polyline((0,0),(60,0),(60,10),(10,10),(10,40),(0,40),(0,0))), 30)
print("L", len(L.edges()), classify(L))
plate = Box(60, 6, 60)
for x in (-15, 15):
    for z in (-15, 15):
        plate = plate - Pos(x, 0, z) * (Plane.XZ * Cylinder(2, 24))
print("plate", len(plate.edges()), classify(plate))
inner = sum(len(f.inner_wires().edges()) for f in plate.faces())
print("inner-wire edges", inner)
rib = Pos(0, 3 + 10, 0) * Box(10, 20, 30)
fused = Box(60, 6, 60) + rib
print("fused", len(fused.edges()), classify(fused))
cyl = Plane.XZ * Cylinder(20, 40)
print("cyl", len(cyl.edges()), classify(cyl))

# shell
b = Box(100, 60, 40)
top = b.faces().sort_by(Axis.Y)[-1]
s = offset(b, amount=-5, openings=[top])
print("shell box vol", s.volume, "want", 100*60*40 - 90*55*30)
c = Plane.XZ * Cylinder(30, 80)
top = c.faces().sort_by(Axis.Y)[-1]
s = offset(c, amount=-4, openings=[top])
print("shell cyl vol", s.volume, "want", math.pi*30**2*80 - math.pi*26**2*76)
s = offset(b, amount=-5)
print("closed shell", s.volume, len(s.solids()), "want", 100*60*40 - 90*50*30)
f = Plane.XZ * Rectangle(50, 30)
th = thicken(f, 4)
print("thicken", th.volume, "want", 6000, th.bounding_box().min, th.bounding_box().max)
