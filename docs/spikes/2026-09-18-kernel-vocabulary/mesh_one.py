"""One mesh build in a fresh process. Usage: mesh_one.py <sidecar.py> <label> <case> <normals 0|1>"""
import importlib.util, json, sys, time

try:
    import psutil
except Exception:
    psutil = None

path, label, case, normals = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4] == "1"
spec = importlib.util.spec_from_file_location("sidecar", path)
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)
if hasattr(sc, "_MESH_NORMALS"):
    sc._MESH_NORMALS = normals
I = [1, 0, 0, 0, 1, 0, 0, 0, 1]


def copies():
    solids = []
    for i in range(4096):
        d, x, z = i % 16, (i % 64) * 40.0, (i // 64) * 40.0
        if d < 6:
            s = {"shape": "cylinder", "dims": {"radius": 5.0 + d, "height": 20.0}}
        elif d < 11:
            s = {"shape": "box", "dims": {"width": 10.0 + d, "height": 8.0, "depth": 12.0}}
        else:
            s = {"shape": "sphere", "dims": {"radius": 4.0 + d}}
        s.update({"id": "p%d" % i, "label": "p%d" % i, "position": [x, 0.0, z], "matrix": I})
        solids.append(s)
    return solids


def distinct(n):
    return [{"id": "p%d" % i, "label": "p%d" % i, "shape": "cylinder",
             "dims": {"radius": 5.0 + i * 0.001, "height": 20.0},
             "position": [(i % 64) * 40.0, 0.0, (i // 64) * 40.0], "matrix": I} for i in range(n)]


solids = copies() if case == "copies4096" else distinct(int(case.replace("distinct", "")))
load = psutil.cpu_percent(interval=0.5) if psutil else -1
t0 = time.perf_counter()
rep = sc._build({"format": "mesh", "skip_interferences": True, "solids": solids})
body = json.dumps(rep)
dt = time.perf_counter() - t0
print(json.dumps({"variant": label, "case": case, "ok": rep.get("ok"), "reply_bytes": len(body),
                  "triangles": rep.get("mesh_triangles"), "angular": rep.get("mesh_angular"),
                  "has_normals": '"normals"' in body,
                  "mesh_s": round(rep["phases"].get("mesh", 0), 3), "total_s": round(dt, 3),
                  "cpu_percent_before": load}), flush=True)
