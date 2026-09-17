"""One _build with the cycle collector running or paused, timing collections through
gc.callbacks and counting what gc.collect() finds afterwards
(docs/spikes/2026-09-17-one-million-after, section 5). Variant names: on, gcoff, on-check,
gcoff-check (the -check variants run the real interference check; the others replace it).

Usage: <cad venv python> gc_probe.py <sidecar.py> <barrel-N.json> <variant>
"""
import gc, importlib.util, json, sys, time, psutil
sidecar, req, variant = sys.argv[1:4]
spec = importlib.util.spec_from_file_location("sidecar", sidecar)
sc = importlib.util.module_from_spec(spec); spec.loader.exec_module(sc)
full = "check" in variant
if not full:
    sc._interferences = lambda s, i, l, placed=None: ([], False, 0, {"pairs": 0, "booleans": 0, "reused": 0, "found": 0, "summarized": False, "buried": 0})
request = json.loads(open(req, "rb").read()); request["format"] = ""
gcs = [0.0, 0, 0.0]
def cb(phase, info):
    if phase == "start": gcs[2] = time.perf_counter()
    else: gcs[0] += time.perf_counter() - gcs[2]; gcs[1] += 1
gc.callbacks.append(cb)
if "gcoff" in variant: gc.disable()
psutil.cpu_percent(None)
t = time.perf_counter(); r = sc._build(request); wall = time.perf_counter() - t
cpu = psutil.cpu_percent(None)
mem = psutil.Process().memory_info()
t = time.perf_counter(); unreachable = gc.collect(); coll = time.perf_counter() - t
print(json.dumps({"variant": variant, "req": req, "build_s": wall, "phases": r.get("phases"), "gc_s": gcs[0], "gc_n": gcs[1],
                  "peak_gb": getattr(mem, "peak_wset", mem.rss) / 1e9, "unreachable_after": unreachable, "collect_after_s": coll,
                  "found": r.get("interferences_found"), "volume": r.get("volume"), "cpu": cpu}))
