import json, sys, time
from build123d import *
data = json.load(open(sys.argv[1]))
mode = sys.argv[2] if len(sys.argv) > 2 else "smooth"
for name, d in sorted(data.items()):
    faces = []
    for prof, pos in zip(d["profiles"], d["positions"]):
        pl = Plane(origin=(pos[0], 0, 0), x_dir=(0, 0, -1), z_dir=(1, 0, 0))
        pts = [pl.from_local_coords((p["x"], p["y"], 0)) for p in prof]
        w = Wire.make_polygon(pts)
        r = prof[0].get("radius", 0)
        if r > 0 and "noround" not in mode:
            w = w.fillet_2d(r, w.vertices())
        faces.append(Face(w))
    ys = [max(p["y"] for p in prof) for prof in d["profiles"]]
    zs = [max(abs(p["x"]) for p in prof) for prof in d["profiles"]]
    t = time.time()
    body = loft(faces, ruled=("ruled" in mode))
    bb = body.bounding_box()
    print("%-18s %s top stated %.0f built %.0f (+%.1f%%) half-width stated %.0f built %.0f (+%.1f%%) faces %d loft %.2fs" % (
        name, mode, max(ys), bb.max.Y, 100 * (bb.max.Y / max(ys) - 1), max(zs), bb.max.Z, 100 * (bb.max.Z / max(zs) - 1),
        len(body.faces()), time.time() - t), flush=True)
