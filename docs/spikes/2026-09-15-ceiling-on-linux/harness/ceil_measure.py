"""The kernel build ceiling on Linux: forged in a container limited like its pod.

Linux counterpart of docs/spikes/2026-09-15-kernel-build-ceiling/measure.py. Per run:
start forged (--cpus 1 --memory 1g, the forged container's limits in
deploy/k8s/30-forged.yaml) afresh, sign in, warm the kernel with a four-part design,
then GET /v1/geometry/{id}/mesh while three samplers run:

  * a sampler container sharing forged's PID namespace reads VmRSS/VmHWM of forged
    and every python process every 250 ms, and /proc/stat (the Docker VM's CPU);
  * `docker stats` streams the container's memory and CPU;
  * psutil samples the Windows host's CPU.

At the end the container's cgroup memory.peak, memory.events (oom_kill) and
cpu.stat (throttling) are read, and the forged log lines for the request.
"""

import argparse
import json
import os
import subprocess
import threading
import time
import urllib.error
import urllib.request

import psutil

PORT = 18431
BASE = "http://127.0.0.1:%d" % PORT
EMAIL = "ceiling-linux@example.test"
PASSWORD = "ceiling-linux-password-1"
IMAGE = "forge-linux-test"
NAME = "forge-ceil-forged"
SAMPLER = "forge-ceil-sampler"
DB = "postgres://forge:forge_dev_pw@host.docker.internal:55840/forge?sslmode=disable&search_path=forge_ceil_linux"

SAMPLER_SH = r'''
while :; do
  t=$(date +%s.%N)
  for d in /proc/[0-9]*; do
    n=$(cat $d/comm 2>/dev/null) || continue
    case "$n" in
      forged*|python*) awk -v t=$t -v p=${d#/proc/} -v n=$n '/^VmRSS:|^VmHWM:/{a[$1]=$2} END{print "P",t,p,n,a["VmRSS:"]+0,a["VmHWM:"]+0}' $d/status 2>/dev/null;;
    esac
  done
  echo "S $t $(head -1 /proc/stat)"
  c=/sys/fs/cgroup/docker/$CID
  echo "C $t $(cat $c/memory.current 2>/dev/null) $(awk '$1=="anon"||$1=="file"{printf "%s ", $2}' $c/memory.stat 2>/dev/null)"
  sleep 0.25
done
'''


def sh(*cmd, check=True, **kw):
    r = subprocess.run(list(cmd), capture_output=True, text=True, encoding="utf-8", errors="replace", **kw)
    if check and r.returncode != 0:
        raise SystemExit("%s: %s" % (" ".join(cmd), r.stdout + r.stderr))
    return r


def http(method, path, body=None, token=None, timeout=3600):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def env(args):
    return {
        "FORGE_ENV": "development",
        "FORGE_HTTP_ADDR": "0.0.0.0:8080",
        "FORGE_PUBLIC_URL": "http://localhost:%d" % PORT,
        "FORGE_HTTP_WRITE_TIMEOUT": args.write_timeout,
        "FORGE_DATABASE_URL": DB,
        "FORGE_SESSION_SECRET": "ceiling-linux-" + "x" * 40,
        "FORGE_MAIL_TRANSPORT": "file",
        "FORGE_MAIL_OUTBOX_DIR": "/tmp/outbox",
        "FORGE_LLM_BASE_URL": "http://127.0.0.1:9/v1",
        "FORGE_LLM_API_KEY": "unused",
        "FORGE_DATA_BOUNDARY": "no_training",
        "FORGE_MEDIA_ENABLED": "false",
        "FORGE_LOG_FORMAT": "json",
        "FORGE_CAD_PYTHON": "/usr/local/bin/python",
        "FORGE_CAD_POOL": "1",
        "HOME": "/tmp",
    }


def envflags(e):
    out = []
    for k, v in e.items():
        out += ["-e", "%s=%s" % (k, v)]
    return out


def start(args):
    sh("docker", "rm", "-f", NAME, SAMPLER, check=False)
    sh("docker", "run", "-d", "--name", NAME, "--cpus", args.cpus, "--memory", args.memory,
       "--memory-swap", args.memory, "--user", "10001:10001", "-w", "/tmp",
       "-p", "127.0.0.1:%d:8080" % PORT, "-v", "%s:/opt/forge:ro" % args.bin,
       *envflags(env(args)), "--entrypoint", "/opt/forge/" + args.binary, IMAGE)
    for _ in range(600):
        time.sleep(0.2)
        st = sh("docker", "inspect", "-f", "{{.State.Running}}", NAME, check=False).stdout.strip()
        if st != "true":
            raise SystemExit("forged exited: " + sh("docker", "logs", NAME, check=False).stdout[-2000:])
        try:
            if http("GET", "/healthz", timeout=2)[0] == 200:
                return
        except Exception:
            pass
    raise SystemExit("forged did not become healthy")


def stop():
    sh("docker", "rm", "-f", NAME, SAMPLER, check=False)


def sign_in():
    status, body = http("POST", "/v1/auth/sign-in", {"email": EMAIL, "password": PASSWORD})
    if status != 200:
        raise SystemExit("sign-in: %s %s" % (status, body[:300]))
    j = json.loads(body)
    return j["token"], j["user"]["id"]


def ensure_stored(args, names):
    path = os.path.join(args.scratch, "stored.json")
    stored = json.load(open(path)) if os.path.exists(path) else {}
    missing = [n for n in names + ["tiny"] if n not in stored]
    if not missing:
        return stored
    start(args)
    try:
        http("POST", "/v1/auth/sign-up", {"email": EMAIL, "password": PASSWORD, "display_name": "Ceiling on Linux"})
        _, user = sign_in()
    finally:
        stop()
    tiny = os.path.join(args.docs, "tiny.json")
    if not os.path.exists(tiny):
        json.dump({"name": "Warm-up", "units": "mm", "not_verified": ["a kernel warm-up fixture"], "parts": [
            {"id": "b%d" % i, "shape": "box", "size": {"width": 10, "height": 10, "depth": 10},
             "position": [20 * i, 0, 0], "rotation": [0, 0, 0], "color": "#888888", "opacity": 1}
            for i in range(4)]}, open(tiny, "w"))
    for n in missing:
        r = sh("docker", "run", "--rm", "--name", "forge-ceil-store", "-v", "%s:/opt/forge:ro" % args.bin,
               "-v", "%s:/docs:ro" % args.docs, *envflags(env(args)), "--entrypoint", "/opt/forge/store", IMAGE,
               "-user", user, "-design", "/docs/%s.json" % n,
               "-generator", "docs/spikes/2026-09-15-ceiling-on-linux " + n)
        stored[n] = json.loads(r.stdout.strip().splitlines()[-1])["version_id"]
        json.dump(stored, open(path, "w"), indent=1)
    return stored


def cgroup(name):
    r = sh("docker", "exec", NAME, "sh", "-c",
           "cat /sys/fs/cgroup/memory.peak; echo ---; cat /sys/fs/cgroup/memory.events; echo ---; cat /sys/fs/cgroup/cpu.stat",
           check=False).stdout
    parts = r.split("---")
    out = {}
    try:
        out["memory_peak_mib"] = int(parts[0].strip()) >> 20
        out["memory_events"] = dict(l.split() for l in parts[1].strip().splitlines())
        out["cpu_stat"] = {k: int(v) for k, v in (l.split() for l in parts[2].strip().splitlines())}
    except Exception as e:
        out["cgroup_error"] = "%s: %r" % (e, r[:400])
    return out


def mib(s):
    s = s.strip()
    for unit, f in (("GiB", 1024), ("MiB", 1), ("KiB", 1 / 1024), ("B", 1 / (1 << 20))):
        if s.endswith(unit):
            return float(s[: -len(unit)]) * f
    return None


def measure(args, name, version, warm, run):
    start(args)
    row = {"label": args.label, "design": name, "run": run}
    try:
        token, _ = sign_in()
        t = time.perf_counter()
        status, body = http("GET", "/v1/geometry/%s/mesh" % warm, token=token)
        row["warmup_s"] = round(time.perf_counter() - t, 2)
        if status != 200:
            raise SystemExit("warm-up mesh: %s %s" % (status, body[:300]))
        row["before"] = cgroup(NAME)

        samp = subprocess.Popen(["docker", "run", "--rm", "--name", SAMPLER, "--pid", "container:" + NAME, "--cgroupns", "host", "-e", "CID=" + sh("docker", "inspect", "-f", "{{.Id}}", NAME).stdout.strip(),
                                 "--entrypoint", "bash", IMAGE, "-c", SAMPLER_SH],
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        stats = subprocess.Popen(["docker", "stats", "--format", "{{json .}}", NAME],
                                 stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        proc_lines, stat_lines, host = [], [], []
        done = threading.Event()

        def rd(p, sink):
            for line in p.stdout:
                sink.append((time.time(), line.strip()))

        def hostcpu():
            psutil.cpu_percent(None)
            while not done.is_set():
                done.wait(1.0)
                host.append(psutil.cpu_percent(None))

        ths = [threading.Thread(target=rd, args=(samp, proc_lines), daemon=True),
               threading.Thread(target=rd, args=(stats, stat_lines), daemon=True),
               threading.Thread(target=hostcpu, daemon=True)]
        for th in ths:
            th.start()
        time.sleep(1.5)  # samplers up
        load0 = psutil.getloadavg() if hasattr(psutil, "getloadavg") else None
        t0 = time.time()
        t = time.perf_counter()
        status, body = http("GET", "/v1/geometry/%s/mesh" % version, token=token)
        wall = time.perf_counter() - t
        t1 = time.time()
        time.sleep(0.6)
        done.set()
        row["after"] = cgroup(NAME)
        samp.kill(); stats.kill()
        sh("docker", "rm", "-f", SAMPLER, check=False)

        # Per-process peaks from the PID-namespace sampler.
        forged_rss = kernel_rss = kernel_hwm = forged_hwm = kernel_sum = 0
        cg_cur = cg_anon = cg_file = 0
        vm = []
        by_t = {}
        for _, line in proc_lines:
            f = line.split()
            if not f:
                continue
            if f[0] == "P" and len(f) >= 6:
                ts, pid, comm, rss, hwm = float(f[1]), f[2], f[3], int(f[4]), int(f[5])
                if comm.startswith("forged"):
                    forged_rss, forged_hwm = max(forged_rss, rss), max(forged_hwm, hwm)
                else:
                    kernel_rss, kernel_hwm = max(kernel_rss, rss), max(kernel_hwm, hwm)
                    by_t[ts] = by_t.get(ts, 0) + rss
            elif f[0] == "C" and len(f) >= 5:
                cg_cur, cg_anon, cg_file = max(cg_cur, int(f[2])), max(cg_anon, int(f[3])), max(cg_file, int(f[4]))
            elif f[0] == "S" and len(f) >= 7:
                ts = float(f[1])
                vals = [int(x) for x in f[3:]]
                vm.append((ts, sum(vals), vals[3] + (vals[4] if len(vals) > 4 else 0)))
        kernel_sum = max(by_t.values() or [0])
        vm_in = [x for x in vm if t0 <= x[0] <= t1]
        vm_cpu = None
        if len(vm_in) >= 2:
            tot = vm_in[-1][1] - vm_in[0][1]
            idle = vm_in[-1][2] - vm_in[0][2]
            vm_cpu = round(100.0 * (1 - idle / max(1, tot)), 1)
        mem, cpu = [], []
        for ts, line in stat_lines:
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
        row.update({
            "status": status, "wall_s": round(wall, 3), "bytes": len(body),
            "forged_rss_peak_mib_sampled": forged_rss >> 10, "forged_hwm_mib": forged_hwm >> 10,
            "kernel_rss_peak_mib_sampled": kernel_rss >> 10, "kernel_hwm_mib": kernel_hwm >> 10,
            "kernel_rss_sum_peak_mib": kernel_sum >> 10,
            "docker_stats_mem_peak_mib": round(max(mem), 1) if mem else None,
            "docker_stats_cpu_peak_pct": max(cpu) if cpu else None,
            "docker_stats_cpu_mean_pct": round(sum(cpu) / len(cpu), 1) if cpu else None,
            "docker_stats_samples": len(mem),
            "host_cpu_mean_pct": round(sum(host) / len(host), 1) if host else None,
            "host_cpu_max_pct": max(host) if host else None,
            "cgroup_current_peak_mib_sampled": cg_cur >> 20, "cgroup_anon_peak_mib_sampled": cg_anon >> 20,
            "cgroup_file_peak_mib_sampled": cg_file >> 20,
            "docker_vm_cpu_pct": vm_cpu, "proc_samples": len(proc_lines),
            "at": time.strftime("%Y-%m-%dT%H:%M:%S"),
        })
        if status == 200:
            reply = json.loads(body)
            row.update({"instances": len(reply.get("instances") or []),
                        "definitions": len(reply.get("definitions") or []),
                        "placed_parts": len(reply.get("parts") or []), "triangles": reply.get("triangles"),
                        "skipped": len(reply.get("skipped") or []), "mesh_error": reply.get("mesh_error")})
        else:
            row["error"] = body[:600].decode("utf-8", "replace")
        insp = sh("docker", "inspect", "-f", "{{.State.OOMKilled}} {{.State.Running}}", NAME, check=False).stdout.strip()
        row["oom_killed_running"] = insp
        logs = sh("docker", "logs", NAME, check=False)
        text = logs.stdout + logs.stderr
        with open(os.path.join(args.scratch, "forged-%s-%s-%d.log" % (args.label, name, run)), "w", encoding="utf-8") as fh:
            fh.write(text)
        for line in text.splitlines():
            try:
                j = json.loads(line)
            except Exception:
                continue
            ev = str(j.get("event", "")) + str(j.get("msg", ""))
            if j.get("version_id") == version and ("kernel_mesh_ms" in j or "code" in j):
                row["log"] = {k: v for k, v in j.items() if k.startswith("kernel_") or k in
                              ("msg", "event", "parts", "definitions", "triangles", "skipped", "code")}
            elif "forge.cad." in ev and "started" not in ev:
                row.setdefault("cad_events", []).append({k: j.get(k) for k in ("time", "event", "msg", "detail", "slot")})
    finally:
        stop()
    return row


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--binary", default="forged-lifted")
    p.add_argument("--label", required=True)
    p.add_argument("--bin", required=True)
    p.add_argument("--docs", required=True)
    p.add_argument("--cpus", default="1")
    p.add_argument("--memory", default="1g")
    p.add_argument("--write-timeout", default="5m")
    p.add_argument("--runs", type=int, default=3)
    p.add_argument("--out", required=True)
    p.add_argument("--interleave", action="store_true", help="run 1 of every design, then run 2, ...")
    p.add_argument("designs", nargs="+")
    args = p.parse_args()
    args.scratch = os.path.dirname(os.path.abspath(args.out))
    stored = ensure_stored(args, args.designs)
    order = ([(n, r) for r in range(1, args.runs + 1) for n in args.designs] if args.interleave
             else [(n, r) for n in args.designs for r in range(1, args.runs + 1)])
    for name, run in order:
        row = measure(args, name, stored[name], stored["tiny"], run)
        with open(args.out, "a") as fh:
            fh.write(json.dumps(row) + "\n")
        print(json.dumps({k: row.get(k) for k in ("design", "run", "status", "wall_s", "log",
                                                    "after", "kernel_hwm_mib", "forged_hwm_mib",
                                                    "docker_stats_mem_peak_mib", "cgroup_anon_peak_mib_sampled", "host_cpu_mean_pct",
                                                    "docker_vm_cpu_pct", "oom_killed_running")}), flush=True)


if __name__ == "__main__":
    main()
