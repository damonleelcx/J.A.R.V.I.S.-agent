"""#146's bounded barrel harness (docs/spikes/2026-09-17-one-million-after/measure_bounded.py),
copied unchanged except that each run is measure_hashed.py here, which also hashes the
whole reply, and the line printed per run shows the hashes (kernel last walls).

Bounds, as #146's: a hard wall-clock cap per run (default 3,600 s), the run killed when
the host's used memory passes --host-mem-pct (80%) or its own tree passes --rss-cap-gb,
no run started under --min-free-gb available, load sampled every 5 s.

Usage:
  <cad venv python> measure_bounded.py --dir D --sidecar FROZEN.py
        [--bays 9,30,100] [--modes full,mesh] [--repeat 2] [--cap 3600]
"""
import argparse
import json
import os
import subprocess
import sys
import time

import psutil

HERE = os.path.dirname(os.path.abspath(__file__))
MEASURE = os.path.join(HERE, "measure_hashed.py")


def busiest(n=5):
    procs = []
    for p in psutil.process_iter(["name"]):
        try:
            p.cpu_percent(None)
            procs.append(p)
        except psutil.Error:
            pass
    time.sleep(1.0)
    out = []
    for p in procs:
        try:
            out.append((p.cpu_percent(None) / psutil.cpu_count(), p.info["name"]))
        except psutil.Error:
            pass
    out.sort(reverse=True)
    return ["%s %.1f%%" % (name, c) for c, name in out[:n]]


def kill_tree(proc):
    try:
        for p in psutil.Process(proc.pid).children(recursive=True):
            try:
                p.kill()
            except psutil.Error:
                pass
    except psutil.Error:
        pass
    proc.kill()


def one_run(args, bays, mode, run, results):
    path = os.path.join(args.dir, "barrel-%d.json" % bays)
    vm = psutil.virtual_memory()
    if vm.available / 1e9 < args.min_free_gb:
        return {"bays": bays, "mode": mode, "run": run, "ok": False,
                "stopped": "not started: %.1f GB available < %.1f" % (vm.available / 1e9, args.min_free_gb)}
    top = busiest()
    psutil.cpu_percent(None)
    started = time.time()
    proc = subprocess.Popen([sys.executable, os.path.abspath(MEASURE), "--child", mode, path, args.dir, str(bays),
                             os.path.abspath(args.sidecar), "1" if args.write_reply and mode == "full" else "0"],
                            stdout=subprocess.PIPE, text=True)
    watched = psutil.Process(proc.pid)
    peak, host_peak, stopped = 0.0, 0.0, ""
    cpus, last_sample = [], time.time()
    # Bounded: the loop ends when the child exits or the cap kills it.
    while proc.poll() is None:
        try:
            tree = [watched] + watched.children(recursive=True)
            peak = max([peak] + [p.memory_info().rss / 1e9 for p in tree])
        except psutil.Error:
            pass
        vm = psutil.virtual_memory()
        host_peak = max(host_peak, vm.percent)
        if time.time() - last_sample >= 5:
            cpus.append(psutil.cpu_percent(None))
            last_sample = time.time()
        if peak > args.rss_cap_gb:
            stopped = "run tree RSS over %.0f GB" % args.rss_cap_gb
        elif vm.percent > args.host_mem_pct:
            stopped = "host memory %.0f%% over %.0f%%" % (vm.percent, args.host_mem_pct)
        elif time.time() - started > args.cap:
            stopped = "over %d s" % args.cap
        if stopped:
            kill_tree(proc)
            break
        time.sleep(0.25)
    out = proc.communicate()[0] or ""
    wall = time.time() - started
    line = next((l[len("RESULT "):] for l in out.splitlines() if l.startswith("RESULT ")), None)
    res = json.loads(line) if line else {"mode": mode, "tag": str(bays), "ok": False,
                                         "error": stopped or "child exited %s" % proc.returncode}
    res.update({"bays": bays, "run": run, "child_wall_s": wall, "polled_peak_rss_gb": peak,
                "stopped": stopped, "host_cpu_mean_pct": sum(cpus) / len(cpus) if cpus else None,
                "host_cpu_max_pct": max(cpus) if cpus else None, "host_mem_peak_pct": host_peak,
                "busiest_at_start": top, "sidecar": os.path.abspath(args.sidecar),
                "started": time.strftime("%Y-%m-%dT%H:%M:%S", time.localtime(started))})
    with open(results, "a") as fh:
        fh.write(json.dumps(res) + "\n")
    ph = res.get("phases") or {}
    print("bays=%d %s run %d: ok=%s build %.1fs [shapes %.1f assembly %.1f interf %.1f props %.1f mesh %.1f] "
          "peak %.2f GB host cpu %.0f%% (max %.0f%%) host mem peak %.0f%% %s answer %s props %s" % (
              bays, mode, run, res.get("ok"), res.get("build_s") or 0, ph.get("shapes", 0), ph.get("assembly", 0),
              ph.get("interferences", 0), ph.get("properties", 0), ph.get("mesh", 0),
              res.get("peak_rss_gb") or peak, res["host_cpu_mean_pct"] or 0, res["host_cpu_max_pct"] or 0,
              host_peak, stopped, (res.get("answer_sha256") or "-")[:12],
              (res.get("properties_sha256") or "-")[:12]))
    sys.stdout.flush()
    return res


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True)
    ap.add_argument("--sidecar", required=True)
    ap.add_argument("--bays", default="9,30,100")
    ap.add_argument("--modes", default="full,mesh")
    ap.add_argument("--repeat", type=int, default=2)
    ap.add_argument("--cap", type=int, default=3600)
    ap.add_argument("--rss-cap-gb", type=float, default=24.0)
    ap.add_argument("--host-mem-pct", type=float, default=80.0)
    ap.add_argument("--min-free-gb", type=float, default=16.0)
    ap.add_argument("--write-reply", action="store_true")
    ap.add_argument("--results", default="results.jsonl")
    args = ap.parse_args()
    results = os.path.join(args.dir, args.results)
    for bays in [int(b) for b in args.bays.split(",")]:
        # Interleaved by run, not blocked by mode: full 1, mesh 1, full 2, mesh 2.
        for run in range(1, args.repeat + 1):
            for mode in args.modes.split(","):
                res = one_run(args, bays, mode, run, results)
                if res.get("stopped"):
                    print("STOPPED: %s; no further runs" % res["stopped"])
                    return


if __name__ == "__main__":
    main()
