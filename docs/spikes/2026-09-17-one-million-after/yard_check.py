"""The interference check on a yard of cylinders and extrusions placed by patterns
(docs/spikes/2026-09-17-one-million-after, item 4).

_build runs as shipped with the check's arguments captured (yard-<side>.json from
TestScaleUp_MeasurePrismYard), then the check runs on those same solids:

  warm     -- shipped switches, once, discarded: #128 found a pair's FIRST OCCT boolean
              on a cold process can differ from its later ones in the last bits;
  shipped  -- _BULK_NARROW_PHASE on (arrays), _INTERFERENCE_SLIDE on;
  loop     -- _BULK_NARROW_PHASE off: the per-pair keying #128 replaced, the reference;
  shipped2 -- shipped again, interleaved after the loop;
  noslide  -- (optional, --noslide) _INTERFERENCE_SLIDE off and the boolean budget raised
              so it is not truncated: what the yard would pay with no reuse along prisms.

The shipped and loop answers are serialized as JSON and compared as text.

Usage: <cad venv python> yard_check.py <sidecar.py> <yard-N.json> <out.jsonl> <tag> [--noslide]
"""
import hashlib
import importlib.util
import json
import sys
import time

import psutil

sidecar_path, request_path, out_path, tag = sys.argv[1:5]
noslide = "--noslide" in sys.argv[5:]
spec = importlib.util.spec_from_file_location("sidecar", sidecar_path)
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)

cap = {}


def capture(solids, ids, labels, placed=None):
    cap["a"] = (solids, ids, labels, placed)
    return [], False, 0, {"pairs": 0, "booleans": 0, "reused": 0, "found": 0, "summarized": False, "buried": 0}


real = sc._interferences
sc._interferences = capture
with open(request_path, "rb") as fh:
    request = json.loads(fh.read())
request["format"] = ""
t0 = time.perf_counter()
reply = sc._build(request)
row = {"tag": tag, "parts": reply.get("parts"), "build_without_check_s": round(time.perf_counter() - t0, 3),
       "phases": reply.get("phases")}
del request, reply
sc._interferences = real
args = cap["a"]


def run(name, bulk=True, slide=True, budget=None):
    sc._BULK_NARROW_PHASE, sc._INTERFERENCE_SLIDE = bulk, slide
    saved = sc._INTERFERENCE_PAIR_BUDGET
    if budget is not None:
        sc._INTERFERENCE_PAIR_BUDGET = budget
    psutil.cpu_percent(None)
    t = time.perf_counter()
    got = real(*args)
    row[name + "_s"] = round(time.perf_counter() - t, 3)
    row[name + "_cpu"] = psutil.cpu_percent(None)
    sc._BULK_NARROW_PHASE, sc._INTERFERENCE_SLIDE, sc._INTERFERENCE_PAIR_BUDGET = True, True, saved
    clashes, truncated, box_tests, stats = got
    row[name + "_stats"] = dict(stats, truncated=truncated, box_tests=box_tests, listed=len(clashes))
    return json.dumps(got)


run("warm")
a = run("shipped")
b = run("loop", bulk=False)
c = run("shipped2")
row["identical_shipped_loop"] = a == b
row["identical_shipped_shipped2"] = a == c
row["sha1"] = {"shipped": hashlib.sha1(a.encode()).hexdigest()[:12], "loop": hashlib.sha1(b.encode()).hexdigest()[:12],
               "shipped2": hashlib.sha1(c.encode()).hexdigest()[:12]}
if a != b:
    k = next((i for i in range(min(len(a), len(b))) if a[i] != b[i]), min(len(a), len(b)))
    row["first_difference_at"] = k
    row["shipped_around"] = a[max(0, k - 160):k + 160]
    row["loop_around"] = b[max(0, k - 160):k + 160]
if noslide:
    run("noslide", slide=False, budget=10 ** 7)
mem = psutil.Process().memory_info()
row["peak_rss_gb"] = round(getattr(mem, "peak_wset", mem.rss) / 1e9, 3)
with open(sidecar_path, "rb") as fh:
    row["sidecar_sha1"] = hashlib.sha1(fh.read()).hexdigest()[:7]
row["started"] = time.strftime("%Y-%m-%dT%H:%M:%S")
with open(out_path, "a") as fh:
    fh.write(json.dumps(row) + "\n")
print(json.dumps(row, indent=1))
