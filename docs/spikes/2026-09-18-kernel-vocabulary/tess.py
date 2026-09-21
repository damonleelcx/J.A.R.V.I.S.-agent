from build123d import *
for R in (5, 50, 500):
    for d in (R/1000, R/100, R/10, R):
        for a in (0.1, 0.5, 1.0):
            c = Cylinder(R, 2*R)
            v, t = c.tessellate(d, a)
            print("R", R, "defl", d, "ang", a, "verts", len(v), "tris", len(t))
