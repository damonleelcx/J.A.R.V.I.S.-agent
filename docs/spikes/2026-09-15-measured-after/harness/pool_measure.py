"""forge-worker's memory with FORGE_CAD_POOL=1 and 2, in a container limited like its pod.

#110's docs/spikes/2026-09-15-ceiling-on-linux/harness/pool_measure.py, re-run here with
two changes it named as what would revisit its decision: the worker's CPU limit at 2 as
well as at 1, and the A1 worker binary (which holds a kernel) rather than a branch where
the setting would do nothing. Container and schema names are this spike's, so nothing
here touches #110's data or another agent's containers.

Per run: create N build goals of a given size in schema forge_meas_pool with forgectl
(planning answered by #90's stub.go), start forge-worker (--cpus 1|2 --memory 2g, the
limits in deploy/k8s/31-worker.yaml) with the pool and concurrency asked, wait until no
goal is active, and record the cgroup's memory.peak and memory.events (oom_kill),
cpu.stat (throttling), per-process RSS/VmHWM from a PID-namespace sampler, and host CPU.

    python pool_measure.py --label w-p2-cpu1-r1 --pool 2 --conc 2 --size 4000 --copies 2 \
        --cpus 1 --bin BIN --out data/worker.jsonl
"""

import argparse
import json
import os
import subprocess
import threading
import time

import psutil

IMAGE = "forge-linux-test"
NET = "forge-meas-net"
STUB = "forge-meas-stub"
WORKER = "forge-meas-worker"
SAMPLER = "forge-meas-wsampler"
SCHEMA = "forge_meas_pool"
DB = ("postgres://forge:forge_dev_pw@host.docker.internal:55840/forge"
      "?sslmode=disable&search_path=" + SCHEMA)
OWNER = "mem@meas.local"

SAMPLER_SH = r'''
while :; do
  t=$(date +%s.%N)
  for d in /proc/[0-9]*; do
    n=$(cat $d/comm 2>/dev/null) || continue
    case "$n" in
      forge-worker*|python*) awk -v t=$t -v p=${d#/proc/} -v n=$n '/^VmRSS:|^VmHWM:/{a[$1]=$2} END{print "P",t,p,n,a["VmRSS:"]+0,a["VmHWM:"]+0}' $d/status 2>/dev/null;;
    esac
  done
  c=/sys/fs/cgroup/docker/$CID
  echo "C $t $(cat $c/memory.current 2>/dev/null) $(awk '$1=="anon"||$1=="file"{printf "%s ", $2}' $c/memory.stat 2>/dev/null)"
  sleep 0.25
done
'''


def sh(*cmd, check=True):
    r = subprocess.run(list(cmd), capture_output=True, text=True, encoding="utf-8", errors="replace")
    if check and r.returncode != 0:
        raise SystemExit("%s: %s" % (" ".join(cmd), r.stdout + r.stderr))
    return r


def psql(q):
    return sh("docker", "exec", "forge-pg", "psql", "-U", "forge", "-d", "forge", "-Atc",
              "set search_path=%s; %s" % (SCHEMA, q)).stdout.strip().splitlines()[1:]


def env(args):
    e = {
        "FORGE_ENV": "development",
        "FORGE_DATABASE_URL": DB,
        "FORGE_SESSION_SECRET": "measured-after-" + "x" * 40,
        "FORGE_LLM_API_KEY": "stub", "FORGE_LLM_BASE_URL": "http://%s:55890/v1" % STUB,
        "FORGE_LLM_CONVERSE_MODEL": "stub-converse", "FORGE_LLM_VISION_MODEL": "stub-vision",
        "FORGE_LLM_PLANNER_MODEL": "stub-planner", "FORGE_LLM_EXECUTOR_MODEL": "stub-executor",
        "FORGE_LLM_VERIFIER_MODEL": "other-verifier",
        "FORGE_DATA_BOUNDARY": "no_training",
        "FORGE_POLL_INTERVAL": "500ms", "FORGE_WORKSPACE_ROOT": "/tmp/ws",
        "FORGE_CAD_PYTHON": "/usr/local/bin/python",
        "FORGE_CAD_POOL": str(args.pool), "FORGE_WORKER_CONCURRENCY": str(args.conc),
        "FORGE_ALLOW_SCRIPTS": "false", "FORGE_LOG_FORMAT": "json", "HOME": "/tmp",
    }
    out = []
    for k, v in e.items():
        out += ["-e", "%s=%s" % (k, v)]
    return out


def ensure_stub(args):
    if sh("docker", "inspect", "-f", "{{.State.Running}}", STUB, check=False).stdout.strip() == "true":
        return
    sh("docker", "rm", "-f", STUB, check=False)
    sh("docker", "run", "-d", "--name", STUB, "--network", NET, "-v", "%s:/opt/forge:ro" % args.bin,
       "--entrypoint", "/opt/forge/stub", IMAGE, "0.0.0.0:55890")
    time.sleep(1)


def cgroup():
    r = sh("docker", "exec", WORKER, "sh", "-c",
           "cat /sys/fs/cgroup/memory.peak; echo ---; cat /sys/fs/cgroup/memory.events; echo ---; cat /sys/fs/cgroup/cpu.stat",
           check=False).stdout
    parts = r.split("---")
    try:
        return {"memory_peak_mib": int(parts[0].strip()) >> 20,
                "memory_events": dict(l.split() for l in parts[1].strip().splitlines()),
                "cpu_stat": {k: int(v) for k, v in (l.split() for l in parts[2].strip().splitlines())}}
    except Exception as e:
        return {"cgroup_error": "%s: %r" % (e, r[:400])}


def mib(s):
    s = s.strip()
    for unit, f in (("GiB", 1024), ("MiB", 1), ("KiB", 1 / 1024), ("B", 1 / (1 << 20))):
        if s.endswith(unit):
            return float(s[: -len(unit)]) * f
    return None


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--label", required=True)
    p.add_argument("--pool", type=int, required=True)
    p.add_argument("--conc", type=int, required=True)
    p.add_argument("--size", type=int, required=True)
    p.add_argument("--copies", type=int, required=True)
    p.add_argument("--shape", default="")
    p.add_argument("--bin", required=True)
    p.add_argument("--cpus", default="1")
    p.add_argument("--memory", default="2g")
    p.add_argument("--out", required=True)
    args = p.parse_args()
    scratch = os.path.dirname(os.path.abspath(args.out))
    ensure_stub(args)
    sh("docker", "rm", "-f", WORKER, SAMPLER, check=False)

    for c in range(1, args.copies + 1):
        sh("docker", "run", "--rm", "--name", "forge-meas-goalnew", "--network", NET,
           "-v", "%s:/opt/forge:ro" % args.bin, *env(args), "--entrypoint", "/opt/forge/forgectl-a1", IMAGE,
           "goal", "new", "--build", "--title", "mem %s #%d" % (args.label, c),
           "--statement", ("a tree occurrences=%d %s" % (args.size, args.shape)).strip(),
           "--owner", OWNER, "--start")

    t0 = time.time()
    sh("docker", "run", "-d", "--name", WORKER, "--network", NET, "--cpus", args.cpus,
       "--memory", args.memory, "--memory-swap", args.memory, "--user", "10001:10001", "-w", "/tmp",
       "-v", "%s:/opt/forge:ro" % args.bin, *env(args), "--entrypoint", "/opt/forge/forge-worker-a1", IMAGE)
    cid = sh("docker", "inspect", "-f", "{{.Id}}", WORKER).stdout.strip()
    samp = subprocess.Popen(["docker", "run", "--rm", "--name", SAMPLER, "--pid", "container:" + WORKER,
                             "--cgroupns", "host", "-e", "CID=" + cid,
                             "--entrypoint", "bash", IMAGE, "-c", SAMPLER_SH],
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    stats = subprocess.Popen(["docker", "stats", "--format", "{{json .}}", WORKER],
                             stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    proc_lines, stat_lines, host = [], [], []
    done = threading.Event()

    def rd(pp, sink):
        for line in pp.stdout:
            sink.append(line.strip())

    def hostcpu():
        psutil.cpu_percent(None)
        while not done.is_set():
            done.wait(1.0)
            host.append(psutil.cpu_percent(None))

    for th in (threading.Thread(target=rd, args=(samp, proc_lines), daemon=True),
               threading.Thread(target=rd, args=(stats, stat_lines), daemon=True),
               threading.Thread(target=hostcpu, daemon=True)):
        th.start()

    first = None
    timed_out = False
    while True:
        time.sleep(2)
        active = psql("select count(*) from forge_goals where title like 'mem %s #%%' and status = 'active'"
                      % args.label)
        if first is None:
            first = cgroup()
        if active and active[0] == "0":
            break
        if sh("docker", "inspect", "-f", "{{.State.Running}}", WORKER, check=False).stdout.strip() != "true":
            break
        if time.time() - t0 > 1800:
            timed_out = True
            break
    secs = round(time.time() - t0, 1)
    time.sleep(2)
    done.set()
    cg = cgroup()
    insp = sh("docker", "inspect", "-f", "{{.State.OOMKilled}} {{.State.Running}} {{.State.ExitCode}}", WORKER,
              check=False).stdout.strip()
    samp.kill(); stats.kill()
    logs = sh("docker", "logs", WORKER, check=False)
    with open(os.path.join(scratch, "worker-%s.log" % args.label), "w", encoding="utf-8") as fh:
        fh.write(logs.stdout + logs.stderr)
    sh("docker", "rm", "-f", WORKER, SAMPLER, check=False)

    by_t, worker_rss, kern_hwm, kern_pids = {}, 0, {}, set()
    cg_cur = cg_anon = cg_file = 0
    for line in proc_lines:
        f = line.split()
        if len(f) >= 5 and f[0] == "C":
            cg_cur, cg_anon, cg_file = max(cg_cur, int(f[2])), max(cg_anon, int(f[3])), max(cg_file, int(f[4]))
            continue
        if len(f) < 6 or f[0] != "P":
            continue
        ts, pid, comm, rss, hwm = f[1], f[2], f[3], int(f[4]), int(f[5])
        by_t.setdefault(ts, [0, 0])
        if comm.startswith("forge-worker"):
            worker_rss = max(worker_rss, rss)
            by_t[ts][0] += rss
        else:
            kern_pids.add(pid)
            kern_hwm[pid] = max(kern_hwm.get(pid, 0), hwm)
            by_t[ts][1] += rss
    total_peak = max((a + b for a, b in by_t.values()), default=0)
    kern_peak = max((b for _, b in by_t.values()), default=0)
    mem, cpu = [], []
    for line in stat_lines:
        try:
            j = json.loads(line[line.index('{'):])
        except Exception:
            continue
        m = mib(j.get("MemUsage", "").split("/")[0])
        if m is not None:
            mem.append(m)
        try:
            cpu.append(float(j.get("CPUPerc", "0").rstrip("%")))
        except ValueError:
            pass
    goals = psql("select g.title||'|'||g.status||'|'||coalesce(extract(epoch from g.ended_at - g.started_at)::int::text,'') "
                 "from forge_goals g where g.title like 'mem %s #%%' order by 1" % args.label)
    tasks = psql("select t.status||'|'||coalesce(t.error_code,'') from forge_tasks t join forge_goals g on g.id = t.goal_id "
                 "where g.title like 'mem %s #%%' order by g.title, t.idempotency_key" % args.label)
    row = {
        "label": args.label, "pool": args.pool, "conc": args.conc, "size": args.size, "copies": args.copies,
        "shape": args.shape, "cpus": args.cpus, "memory": args.memory, "seconds": secs,
        "timed_out": timed_out, **cg,
        "throttled_s": round((cg.get("cpu_stat") or {}).get("throttled_usec", 0) / 1e6, 1),
        "oom_killed_running_exit": insp,
        "docker_stats_mem_peak_mib": round(max(mem), 1) if mem else None, "docker_stats_samples": len(mem),
        "docker_stats_cpu_mean_pct": round(sum(cpu) / len(cpu), 1) if cpu else None,
        "cgroup_current_peak_mib_sampled": cg_cur >> 20, "cgroup_anon_peak_mib_sampled": cg_anon >> 20,
        "cgroup_file_peak_mib_sampled": cg_file >> 20,
        "sum_rss_peak_mib": total_peak >> 10, "kernel_rss_sum_peak_mib": kern_peak >> 10,
        "worker_rss_peak_mib": worker_rss >> 10,
        "kernel_processes": len(kern_pids),
        "kernel_hwm_mib": sorted((v >> 10 for v in kern_hwm.values()), reverse=True),
        "host_cpu_mean_pct": round(sum(host) / len(host), 1) if host else None,
        "host_cpu_max_pct": max(host) if host else None,
        "goals": goals, "tasks": tasks, "at": time.strftime("%Y-%m-%dT%H:%M:%S"),
    }
    with open(args.out, "a") as fh:
        fh.write(json.dumps(row) + "\n")
    print(json.dumps(row), flush=True)


if __name__ == "__main__":
    main()
