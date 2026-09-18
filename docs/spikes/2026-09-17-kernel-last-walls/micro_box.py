"""Where one occurrence's box goes (docs/spikes/2026-09-17-kernel-last-walls, item 1).

Builds the first N solids of a barrel once, then times per placed solid: _box_of
(build123d's bounding_box, what part properties used), BRepTools.Clean alone, and the
one OCCT call inside it (BRepBndLib.AddOptimal) with build123d's arguments and without
triangulation, and checks the direct read gives _box_of's numbers.

Usage: <cad venv python> micro_box.py <sidecar.py> <barrel-N.json> [N]
"""
import importlib.util, json, sys, time

spec = importlib.util.spec_from_file_location("sidecar", sys.argv[1])
sc = importlib.util.module_from_spec(spec); spec.loader.exec_module(sc)
req = json.load(open(sys.argv[2]))
n = int(sys.argv[3]) if len(sys.argv) > 3 else 20000
req["solids"] = req["solids"][:n]
req["format"] = ""; req["properties"] = True; req["skip_interferences"] = True
got = {}
orig = sc._properties
def cap(solids, ids, placed=None):
    got["solids"], got["ids"], got["placed"] = solids, ids, placed
    return orig(solids, ids, placed)
sc._properties = cap
r = sc._build(req)
print("build", r["phases"])
solids, placed = got["solids"], got["placed"]
from OCP.Bnd import Bnd_Box
from OCP.BRepBndLib import BRepBndLib
from OCP.BRepTools import BRepTools

def t(name, f):
    t0 = time.perf_counter()
    out = [f(s) for s in solids]
    dt = time.perf_counter() - t0
    print("%-28s %.2f us" % (name, dt / len(solids) * 1e6))
    return out

def raw(s):
    b = Bnd_Box(); BRepBndLib.AddOptimal_s(s.wrapped, b)
    return b.Get()
def raw_nt(s):
    b = Bnd_Box(); BRepBndLib.AddOptimal_s(s.wrapped, b, False, False)
    return b.Get()
def clean(s):
    BRepTools.Clean_s(s.wrapped)
for rep in range(2):
    a = t("_box_of", sc._box_of)
    t("Clean_s only", clean)
    b = t("AddOptimal_s", raw)
    c = t("AddOptimal_s no tri", raw_nt)
    ok = all(x == ((y[0], y[1], y[2]), (y[3], y[4], y[5])) for x, y in zip(a, c))
    print("no-tri identical to _box_of:", ok)
