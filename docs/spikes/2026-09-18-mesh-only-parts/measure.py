"""Mesh-only lattices: wall thickness calibration, triangles and build time.

Run with the kernel's Python (manifold3d installed), from the repository root:

    python docs/spikes/2026-09-18-mesh-only-parts/measure.py calibrate
    python docs/spikes/2026-09-18-mesh-only-parts/measure.py sizes

`calibrate` builds each pattern with the gradient factor forced to 1 and reports the
factor that makes the measured wall equal the asked thickness. The measured wall is
2 * volume / (sheet area), the sheet area being the mesh's area less the faces the
box clipped (their share estimated as the fill fraction of the box's own surface).

`sizes` builds a gyroid, a diamond and a primitive lattice in a 60 x 40 x 30 box at
several cell sizes with the factors the sidecar uses, and prints triangles, build
seconds and the estimate Go refuses by (geometry.latticeTriangleEstimate).
"""
import os
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "internal", "domain", "cad"))
import sidecar  # noqa: E402

BOX = (60.0, 40.0, 30.0)
# geometry/lattice.go's TrianglesCoefficient, copied for the report only.
ESTIMATE = {"gyroid": 45.0, "diamond": 54.0, "primitive": 36.0}


def solid(pattern, cell, thickness):
    edge = min(cell / 8.0, thickness / 1.5)
    return {"id": "l", "label": "l", "shape": "lattice", "mesh_only": True, "lattice": pattern,
            "dims": {"width": BOX[0], "height": BOX[1], "depth": BOX[2],
                     "cell": cell, "thickness": thickness, "edge": edge},
            "matrix": [1, 0, 0, 0, 1, 0, 0, 0, 1], "position": [0, 0, 0]}


def built(s):
    m3 = sidecar._m3
    level, g = sidecar._LATTICE_PATTERNS[s["lattice"]]
    verts, tris = sidecar._lattice_mesh(s)
    mesh = m3.Mesh(vert_properties=verts.astype("float32"), tri_verts=tris.astype("uint32"))
    return m3.Manifold(mesh), len(tris)


def wall(m):
    vol, area = m.volume(), m.surface_area()
    box_v = BOX[0] * BOX[1] * BOX[2]
    box_a = 2 * (BOX[0] * BOX[1] + BOX[1] * BOX[2] + BOX[0] * BOX[2])
    return 2 * vol / (area - (vol / box_v) * box_a)


def calibrate():
    sidecar._LATTICE_BUDGET = 10 ** 8  # a calibration, not a request
    saved = dict(sidecar._LATTICE_PATTERNS)
    for name, (f, _) in saved.items():
        sidecar._LATTICE_PATTERNS[name] = (f, 1.0)
        for cell, t in ((10.0, 1.0), (20.0, 2.0)):
            m, _ = built(solid(name, cell, t))
            vol, area = m.volume(), m.surface_area()
            box_v = BOX[0] * BOX[1] * BOX[2]
            box_a = 2 * (BOX[0] * BOX[1] + BOX[1] * BOX[2] + BOX[0] * BOX[2])
            sheet_area = area - (vol / box_v) * box_a
            measured = 2 * vol / sheet_area
            print("%-9s cell %4.1f t %3.1f  wall %.3f  factor %.3f  fill %.3f"
                  % (name, cell, t, measured, t / measured, vol / box_v))
        sidecar._LATTICE_PATTERNS[name] = saved[name]


def sizes():
    sidecar._LATTICE_BUDGET = 10 ** 8  # measured, not refused
    for name in sidecar._LATTICE_PATTERNS:
        for cell in (30.0, 20.0, 15.0, 10.0, 7.5):
            t = cell / 10.0
            s = solid(name, cell, t)
            start = time.perf_counter()
            try:
                m, n = built(s)
                took = time.perf_counter() - start
                d = s["dims"]
                ratio = n * d["cell"] * d["edge"] ** 2 / (BOX[0] * BOX[1] * BOX[2])
                estimate = int(ESTIMATE[name] * BOX[0] * BOX[1] * BOX[2] / (d["cell"] * d["edge"] ** 2))
                verdict = "refused by Go" if estimate > 200000 else "built"
                print("%-9s cell %4.1f t %4.2f  triangles %8d  %.2f s  wall %.3f  n*cell*edge^2/V %.2f"
                      "  Go estimate %8d (%s)"
                      % (name, cell, t, n, took, wall(m), ratio, estimate, verdict))
            except Exception as exc:
                print("%-9s cell %4.1f t %4.2f  refused: %s" % (name, cell, t, exc))


if __name__ == "__main__":
    {"calibrate": calibrate, "sizes": sizes}[sys.argv[1]]()
