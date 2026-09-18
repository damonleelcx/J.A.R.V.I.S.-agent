"""The kernel process's memory while it waits, after each STEP export, through its real
main() loop (docs/spikes/2026-09-17-kernel-last-walls). Linux only (/proc).

Starts `python3 <sidecar.py>` as the worker does, waits for its ready line, and sends the
export job's request (format "step", skip_interferences) --exports times. After each reply
it waits --settle seconds and reads the child's VmRSS and VmHWM, which is what #145's
per-second sampler saw as "kernel RSS after".

Usage: python3 sidecar_idle_rss.py <sidecar.py> <barrel-N.json> [--exports 4] [--settle 2]
"""
import argparse
import json
import subprocess
import sys
import time


def status(pid):
    out = {}
    with open("/proc/%d/status" % pid) as fh:
        for line in fh:
            if line.startswith(("VmRSS:", "VmHWM:")):
                key, value = line.split(":")
                out[key.strip().lower() + "_mib"] = round(int(value.split()[0]) / 1024, 1)
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("sidecar")
    ap.add_argument("barrel")
    ap.add_argument("--exports", type=int, default=4)
    ap.add_argument("--settle", type=float, default=2.0)
    args = ap.parse_args()
    with open(args.barrel, "rb") as fh:
        doc = json.loads(fh.read())
    doc.update(format="step", skip_interferences=True)
    line = (json.dumps(doc) + "\n").encode()
    del doc
    proc = subprocess.Popen([sys.executable, args.sidecar], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
    try:
        ready = proc.stdout.readline()
        print(json.dumps(dict(status(proc.pid), step="ready", sidecar=args.sidecar, banner=ready.decode().strip())),
              flush=True)
        for n in range(1, args.exports + 1):
            t = time.time()
            proc.stdin.write(line)
            proc.stdin.flush()
            reply = proc.stdout.readline()
            took = time.time() - t
            ok = json.loads(reply).get("ok")
            size = len(reply)
            del reply
            time.sleep(args.settle)
            print(json.dumps(dict(status(proc.pid), step="export %d idle" % n, ok=ok, reply_mb=round(size / 1e6, 1),
                                  round_trip_s=round(took, 1))), flush=True)
    finally:
        proc.stdin.close()
        proc.wait(timeout=60)


if __name__ == "__main__":
    main()
