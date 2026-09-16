"""Markdown tables from this spike's data files.

    python summarize.py data/forged.jsonl data/worker.jsonl data/results.jsonl

  forged.jsonl  -- mesh_pool_measure.py: N concurrent mesh requests at forged's limits.
  worker.jsonl  -- pool_measure.py: two build goals at forge-worker's limits.
  results.jsonl -- #89's measure.py (the 1M and STEP rows), summarised by mode and size.
"""

import json
import sys


def rows(path):
    return [json.loads(l) for l in open(path, encoding="utf-8") if l.strip()]


def forged(path):
    print("| run | pool | requests at once | CPUs | slowest | fastest | total wall | ok | "
          "kernel processes | kernel VmHWM MiB | forged VmHWM MiB | cgroup memory.peak MiB | anon MiB | "
          "throttled s | oom_kill | host CPU % |")
    print("|---|---:|---:|---:|---:|---:|---:|---|---:|---|---:|---:|---:|---:|---:|---:|")
    for r in rows(path):
        aft = r.get("after") or {}
        print("| %s | %s | %s | %s | %.1f | %.1f | %.1f | %d/%d | %s | %s | %s | %s | %s | %s | %s | %s |" % (
            r["label"], r["pool"], r["conc"], r.get("cpus"), r.get("slowest_request_s") or 0,
            r.get("fastest_request_s") or 0, r.get("total_wall_s") or 0,
            r.get("ok_requests") or 0, r["conc"], r.get("kernel_processes"),
            ", ".join(map(str, r.get("kernel_hwm_each_mib") or [])), r.get("forged_hwm_mib"),
            aft.get("memory_peak_mib"), r.get("cgroup_anon_peak_mib_sampled"), r.get("throttled_s"),
            (aft.get("memory_events") or {}).get("oom_kill"), r.get("host_cpu_mean_pct")))
    print()
    print("Per request, seconds from the moment the first of the batch was sent:")
    print()
    print("| run | request | status | start | end | wall | instances |")
    print("|---|---:|---:|---:|---:|---:|---:|")
    for r in rows(path):
        for q in r.get("requests") or []:
            if not q:
                continue
            print("| %s | %s | %s | %s | %s | %s | %s |" % (
                r["label"], q.get("i"), q.get("status"), q.get("start_offset_s"),
                q.get("end_offset_s"), q.get("wall_s"), q.get("instances")))


def worker(path):
    print("| run | pool | builds at once | CPUs | occurrences | s | goals | cgroup memory.peak MiB | anon MiB | "
          "sum RSS peak MiB | kernel VmHWM MiB | worker RSS MiB | throttled s | oom_kill | OOMKilled | host CPU % |")
    print("|---|---:|---:|---:|---:|---:|---|---:|---:|---:|---|---:|---:|---:|---|---:|")
    for r in rows(path):
        ok = sum(1 for g in r["goals"] if g.split("|")[1] == "succeeded")
        print("| %s | %s | %s | %s | %s | %s | %d/%d succeeded | %s | %s | %s | %s | %s | %s | %s | %s | %s |" % (
            r["label"], r["pool"], r["copies"], r.get("cpus"), "{:,}".format(r["size"]), r["seconds"],
            ok, len(r["goals"]), r.get("memory_peak_mib"), r.get("cgroup_anon_peak_mib_sampled"),
            r.get("sum_rss_peak_mib"), ", ".join(map(str, r.get("kernel_hwm_mib") or [])),
            r.get("worker_rss_peak_mib"), r.get("throttled_s"),
            (r.get("memory_events") or {}).get("oom_kill"), r["oom_killed_running_exit"].split()[0],
            r.get("host_cpu_mean_pct")))


def measured(path):
    """#89's measure.py rows: the 1M full/mesh timings and the STEP re-measure."""
    print("| occurrences | mode | run | ok | build s | shapes | assembly | interference | properties | mesh | "
          "export | peak GB | CPU % | stopped | other processes |")
    print("|---:|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|")
    for r in rows(path):
        ph = r.get("phases") or {}
        def p(k):
            v = ph.get(k)
            return "" if v is None else "%.1f" % v
        print("| %s | %s | %s | %s | %.1f | %s | %s | %s | %s | %s | %s | %.2f | %s | %s | %s |" % (
            "{:,}".format(r.get("bays", 0) * 0 + (r.get("parts") or 0)) if r.get("parts") else
            "bays %s" % r.get("bays"), r.get("mode"), r.get("run"), r.get("ok"), r.get("build_s") or 0,
            p("shapes"), p("assembly"), p("interferences"), p("properties"), p("mesh"), p("export"),
            r.get("peak_rss_gb") or r.get("polled_peak_rss_gb") or 0, r.get("system_cpu_pct"),
            r.get("stopped") or "", ", ".join(r.get("other_processes") or [])))


if __name__ == "__main__":
    for p in sys.argv[1:]:
        if "forged" in p:
            forged(p)
        elif "worker" in p:
            worker(p)
        else:
            measured(p)
        print()
