"""What an interference list of N entries costs, so the 10,000 bound is chosen on
numbers (docs/spikes/2026-09-15-last-hot-spots).

_INTERFERENCE_LIST_LIMIT is 10,000 because "the largest clash count any fence
produces is 2,400" and 10,000 entries is about 1.8 MB (#113, next scale walls). That
is a per-entry cost times a CHOSEN count, not a measured optimum, and #113 said so.

This measures the per-entry cost directly. The barrel is built ONCE with the bound
lifted, which gives the whole worst-first list; the reply is then serialized with
its first N entries for each N. Slicing is exactly what the bound does — the list is
already worst-first and heapq.nsmallest returns that same order — so no build is
repeated to change N.

For each N it reports the reply's bytes, the sidecar's own encode time (best of
three, because json.dumps at 90 MB is the kind of thing a background process moves),
and the bytes the list itself accounts for. It also writes reply-full-<N>.json, which
cad's TestScaleUp_MeasureTheInterferenceReply reads to time Go's decode and
allocations on exactly this path.

Usage: <cad venv python> measure_list_bound.py <sidecar.py> <barrel-N.json> <out dir>
                                                [--limits 1000,10000,50000,100000]
"""
import importlib.util
import json
import os
import sys
import time

import psutil

sidecar_path, request_path, out_dir = sys.argv[1:4]
limits = [1000, 10000, 50000, 100000]
for k, arg in enumerate(sys.argv[4:]):
    if arg == "--limits":
        limits = [int(x) for x in sys.argv[5 + k].split(",")]

spec = importlib.util.spec_from_file_location("sidecar", sidecar_path)
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)

with open(request_path, "rb") as fh:
    request = json.loads(fh.read())
request["format"] = ""

# The whole list, in the order the bound would cut: the check as shipped, with only
# how many it WRITES DOWN lifted.
sc._INTERFERENCE_LIST_LIMIT = 10 ** 9
psutil.cpu_percent(None)
t = time.perf_counter()
reply = sc._build(request)
build_s = time.perf_counter() - t
cpu = psutil.cpu_percent(None)
if not reply.get("ok"):
    raise SystemExit("the build failed: %s" % reply.get("error"))
whole = reply["interferences"]
del request


def dumps_best(obj, tries=3):
    best, size = None, 0
    for _ in range(tries):
        t0 = time.perf_counter()
        line = json.dumps(obj)
        took = time.perf_counter() - t0
        size = len(line)
        best = took if best is None else min(best, took)
        del line
    return best, size


rows = []
# The reply with no list at all, so the list's own share is a subtraction rather
# than an estimate from an average entry.
bare = dict(reply)
bare["interferences"] = []
_, bare_bytes = dumps_best(bare)

for n in sorted(set(limits + [len(whole)])):
    if n > len(whole):
        continue
    cut = dict(reply)
    cut["interferences"] = whole[:n]
    cut["interferences_summarized"] = n < len(whole)
    encode_s, size = dumps_best(cut)
    rows.append({"listed": n, "reply_bytes": size, "list_bytes": size - bare_bytes,
                 "bytes_per_entry": (size - bare_bytes) / n if n else 0.0,
                 "encode_s": encode_s})
    with open(os.path.join(out_dir, "reply-full-%d.json" % n), "w") as fh:
        fh.write(json.dumps(cut))
    del cut

mem = psutil.Process().memory_info()
out = {"request": os.path.basename(request_path), "parts": reply.get("parts"),
       "found": reply.get("interferences_found"), "listed_whole": len(whole),
       "buried": reply.get("interferences_buried"), "build_s": build_s,
       "shipped_limit": 10000, "bare_reply_bytes": bare_bytes, "rows": rows,
       "peak_rss_gb": getattr(mem, "peak_wset", mem.rss) / 1e9, "system_cpu_pct": cpu,
       "started": time.strftime("%Y-%m-%dT%H:%M:%S")}
with open(os.path.join(out_dir, "list-bound.jsonl"), "a") as fh:
    fh.write(json.dumps(out) + "\n")
print(json.dumps(out, indent=1))
