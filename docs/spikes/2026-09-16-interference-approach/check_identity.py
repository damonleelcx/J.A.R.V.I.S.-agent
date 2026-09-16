"""The whole interference answer, the array way and the loop way, back to back
(docs/spikes/2026-09-16-interference-approach).

_build runs as shipped with the check's arguments captured, and then the check runs
four times on those same solids -- loop, array, loop, array, INTERLEAVED, because
this laptop's own speed drifts by more than the effect. Both answers are serialized
as JSON and compared as text: the list, the flag, the box tests and every count.

Usage: <cad venv python> check_identity.py <sidecar.py> <barrel-N.json> [out.jsonl] [tag]
"""
import hashlib
import importlib.util
import json
import os
import sys
import time

import psutil

sidecar_path, request_path = sys.argv[1:3]
out_path = sys.argv[3] if len(sys.argv) > 3 else ""
tag = sys.argv[4] if len(sys.argv) > 4 else "x"
spec = importlib.util.spec_from_file_location("sidecar", sidecar_path)
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)

cap = {}


def capture(solids, ids, labels, placed=None):
    cap["a"] = (solids, ids, labels, placed)
    return [], False, 0, {"pairs": 0, "booleans": 0, "reused": 0, "found": 0,
                          "summarized": False, "buried": 0}


real = sc._interferences
sc._interferences = capture
with open(request_path, "rb") as fh:
    request = json.loads(fh.read())
request["format"] = ""
sc._build(request)
del request
sc._interferences = real
args = cap["a"]
psutil.cpu_percent(None)

row = {"tag": tag, "parts": len(args[0])}
out = {}
# Interleaved: loop, array, loop, array.
for rep in (1, 2):
    for bulk in (False, True):
        sc._BULK_NARROW_PHASE = bulk
        t0 = time.perf_counter()
        got = real(*args)
        s = time.perf_counter() - t0
        name = ("array" if bulk else "loop")
        row["%s_r%d_s" % (name, rep)] = round(s, 3)
        row["%s_r%d_cpu" % (name, rep)] = psutil.cpu_percent(None)
        out[name] = json.dumps(got)
sc._BULK_NARROW_PHASE = True

row["loop_bytes"] = len(out["loop"])
row["array_bytes"] = len(out["array"])
row["identical"] = out["loop"] == out["array"]
row["sha1_loop"] = hashlib.sha1(out["loop"].encode()).hexdigest()[:12]
row["sha1_array"] = hashlib.sha1(out["array"].encode()).hexdigest()[:12]
row["stats"] = json.loads(out["array"])[3] if out["array"].startswith("[") else None
if not row["identical"]:
    a, b = out["loop"], out["array"]
    k = next(i for i in range(min(len(a), len(b))) if a[i] != b[i])
    row["first_difference_at"] = k
    row["loop_around"] = a[max(0, k - 120):k + 120]
    row["array_around"] = b[max(0, k - 120):k + 120]
mem = psutil.Process().memory_info()
row["peak_rss_gb"] = round(getattr(mem, "peak_wset", mem.rss) / 1e9, 3)
with open(sidecar_path, "rb") as fh:
    row["sidecar_sha1"] = hashlib.sha1(fh.read()).hexdigest()[:7]
row["started"] = time.strftime("%Y-%m-%dT%H:%M:%S")
if out_path:
    with open(out_path, "a") as fh:
        fh.write(json.dumps(row) + "\n")
print(json.dumps(row, indent=1))
