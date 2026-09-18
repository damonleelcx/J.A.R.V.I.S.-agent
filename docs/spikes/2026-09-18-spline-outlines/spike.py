import math
from build123d import *


def wire(pts, z):
    es = []
    n = len(pts)
    for i in range(n):
        a = pts[i]
        b = pts[(i + 1) % n]
        A = Vector(a[0], a[1], z)
        B = Vector(b[0], b[1], z)
        if len(b) > 2:
            es.append(ThreePointArc(A, Vector(b[2], b[3], z), B))
        else:
            es.append(Line(A, B))
    return Face(Wire(es))


rect = [(-20, 0), (20, 0), (20, 20), (-20, 20)]
bul = [(-20, 0), (20, 0), (20, 20), (-20, 20, 0, 25)]
lens = [(-20, 0, 0, -8), (20, 0, 0, 8)]
h = 30
for name, a, b, ruled in [("rect-bul smooth", rect, bul, False), ("rect-bul ruled", rect, bul, True),
                          ("rect-lens", rect, lens, False), ("lens-lens", lens, lens, False),
                          ("bul-bul-rect 3", None, None, False)]:
    try:
        if a is None:
            s = loft([wire(rect, 0), wire(bul, h), wire(rect, 2 * h)])
        else:
            s = loft([wire(a, 0), wire(b, h)], ruled=ruled)
        print(name, s.volume, s.is_valid, len(s.faces()))
    except Exception as e:
        print(name, "ERR", e)
print("lens extr", extrude(wire(lens, 0), 10).volume)
print("--- correspondence")
bulcw = [(-20, 20), (20, 20, 0, 25), (20, 0), (-20, 0)]
bulrot = [(20, 0), (20, 20), (-20, 20, 0, 25), (-20, 0)]
for name, b in [("cw", bulcw), ("rotated start", bulrot)]:
    try:
        s = loft([wire(rect, 0), wire(b, h)])
        print(name, s.volume, s.is_valid)
    except Exception as e:
        print(name, "ERR", e)
