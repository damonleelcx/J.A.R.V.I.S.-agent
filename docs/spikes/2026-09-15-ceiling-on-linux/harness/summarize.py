"""Markdown tables from results.jsonl (ceil_measure.py) and pool.jsonl (pool_measure.py).

    python summarize.py data/results.jsonl data/pool.jsonl
"""

import json
import sys


def s(ms):
    return "" if ms is None else "%.1f" % (ms / 1000)


def ceiling(path):
    rows = [json.loads(l) for l in open(path, encoding="utf-8") if l.strip()]
    order = {}
    for r in rows:
        order.setdefault((r["design"].split("-")[0], int(r["design"].split("-")[1])), []).append(r)
    print("| design | run | status | wall s | shapes | assembly | interference | mesh | kernel VmHWM MiB | forged VmHWM MiB "
          "| cgroup peak MiB (anon) | throttled s | host CPU % | VM CPU % |")
    print("|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
    for (fam, n), rs in sorted(order.items()):
        for r in sorted(rs, key=lambda r: (r["label"] != "shipped", r["run"])):
            lg = r.get("log") or {}
            aft = r.get("after") or {}
            thr = (aft.get("cpu_stat") or {}).get("throttled_usec", 0) - ((r.get("before") or {}).get("cpu_stat") or {}).get("throttled_usec", 0)
            parts = lg.get("parts") or n
            label = r["run"] if r["label"] == "shipped" else "lifted"
            print("| %s %s | %s | %s | %.1f | %s | %s | %s | %s | %s | %s | %s (%s) | %.1f | %s | %s |" % (
                fam, "{:,}".format(parts), label, r["status"], r["wall_s"], s(lg.get("kernel_shapes_ms")),
                s(lg.get("kernel_assembly_ms")), s(lg.get("kernel_interferences_ms")), s(lg.get("kernel_mesh_ms")),
                r.get("kernel_hwm_mib"), r.get("forged_hwm_mib"), aft.get("memory_peak_mib"),
                r.get("cgroup_anon_peak_mib_sampled"), thr / 1e6, r.get("host_cpu_mean_pct"), r.get("docker_vm_cpu_pct")))


def pool(path):
    rows = [json.loads(l) for l in open(path, encoding="utf-8") if l.strip()]
    print("| run | pool | builds at once | occurrences | s | goals | cgroup memory.peak MiB | anon peak MiB | sum RSS peak MiB "
          "| kernel VmHWM MiB | worker RSS MiB | oom_kill | OOMKilled | host CPU % |")
    print("|---|---|---|---:|---:|---|---:|---:|---:|---|---:|---:|---|---:|")
    for r in rows:
        ok = sum(1 for g in r["goals"] if g.split("|")[1] == "succeeded")
        print("| %s | %s | %s | %s | %s | %d/%d succeeded | %s | %s | %s | %s | %s | %s | %s | %s |" % (
            r["label"], r["pool"], r["copies"], "{:,}".format(r["size"]), r["seconds"], ok, len(r["goals"]),
            r.get("memory_peak_mib"), r.get("cgroup_anon_peak_mib_sampled"), r.get("sum_rss_peak_mib"),
            ", ".join(map(str, r.get("kernel_hwm_mib") or [])), r.get("worker_rss_peak_mib"),
            (r.get("memory_events") or {}).get("oom_kill"), r["oom_killed_running_exit"].split()[0],
            r.get("host_cpu_mean_pct")))


if __name__ == "__main__":
    for p in sys.argv[1:]:
        (pool if "pool" in p else ceiling)(p)
        print()
