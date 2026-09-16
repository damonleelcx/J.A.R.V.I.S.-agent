"""Machine load, once every 10 s, until killed: CPU %, memory, and the five
processes using the most CPU in that interval (docs/spikes/2026-09-15-check-profile).

Usage: <python with psutil> load_log.py <out.log>
"""
import sys
import time

import psutil

out = sys.argv[1]
psutil.cpu_percent(None)
procs = {}
while True:
    for p in psutil.process_iter(["name"]):
        if p.pid not in procs:
            try:
                p.cpu_percent(None)
            except psutil.Error:
                continue
            procs[p.pid] = p
    time.sleep(10)
    cpu = psutil.cpu_percent(None)
    top = []
    for pid, p in list(procs.items()):
        try:
            top.append((p.cpu_percent(None) / psutil.cpu_count(), p.info["name"]))
        except psutil.Error:
            del procs[pid]
    top.sort(reverse=True)
    mem = psutil.virtual_memory()
    with open(out, "a") as fh:
        fh.write("%s cpu %5.1f%% mem %4.1f GB used | %s\n" % (
            time.strftime("%Y-%m-%dT%H:%M:%S"), cpu, mem.used / 1e9,
            ", ".join("%s %.1f%%" % (n, c) for c, n in top[:5])))
