"""Time one scripted part end to end, the way FORGE runs it, and say what the machine is.

Why this exists: TestScript_BuildsWhatTheVocabularyCannot builds a 20-tooth gear
from a script. It takes 2-3 s on a laptop and in the production image locally, and
it times out at FORGE's 30 s script limit on GitHub's runners, x86_64 and arm64
alike. Locally, the address-space cap and BLAS/OpenMP thread counts were ruled out
(docs/plan-2026-09-13-millions-of-parts.md, execution record). This measures the
rest where it happens, without changing how FORGE runs a script:

  - the machine: CPU count, CPU affinity, cgroup CPU quota, rlimits;
  - how long importing build123d takes on its own;
  - the gear script through the real script.py, with FORGE's own limits (read from
    script.go, so they cannot drift from what the kernel sends) and with no limits.

It prints and never fails: it is a measurement, not a check.

Usage: python scripts/cad_script_timing.py <script.py> <script_test.go> <script.go>
"""
import json
import os
import re
import resource
import subprocess
import sys
import time


def gear_source(test_go):
    src = open(test_go).read()
    m = re.search(r"const gear = `(.*?)`", src, re.S)
    if not m:
        sys.exit("script_test.go has no `const gear`: point this probe at the script the test builds")
    return m.group(1)


def limits(script_go):
    src = open(script_go).read()
    cpu = int(re.search(r"scriptCPUSeconds\s*=\s*(\d+)", src).group(1))
    mem = re.search(r"scriptMemoryBytes\s*=\s*1\s*<<\s*(\d+)", src)
    return cpu, 1 << int(mem.group(1))


def machine():
    print("cpu_count:", os.cpu_count())
    if hasattr(os, "sched_getaffinity"):
        print("cpu affinity:", len(os.sched_getaffinity(0)))
    for path in ("/sys/fs/cgroup/cpu.max", "/sys/fs/cgroup/cpu/cpu.cfs_quota_us"):
        if os.path.exists(path):
            print(path + ":", open(path).read().strip())
    for name in ("RLIMIT_AS", "RLIMIT_CPU", "RLIMIT_NOFILE", "RLIMIT_NPROC"):
        print(name + ":", resource.getrlimit(getattr(resource, name)))
    for var in ("OPENBLAS_NUM_THREADS", "OMP_NUM_THREADS", "MALLOC_ARENA_MAX"):
        print(var + ":", os.environ.get(var, "(unset)"))


def run(script_py, request, label):
    t = time.time()
    proc = subprocess.run([sys.executable, script_py], input=json.dumps(request),
                          capture_output=True, text=True, timeout=180)
    wall = time.time() - t
    last = (proc.stdout.strip().splitlines() or ["(no output)"])[-1]
    try:
        reply = json.loads(last)
        verdict = "ok" if reply.get("ok") else "error: " + str(reply.get("error"))[:200]
    except ValueError:
        verdict = "no JSON reply: " + last[:200]
    print("%-38s %6.1f s  exit %s  %s" % (label, wall, proc.returncode, verdict))
    if proc.stderr.strip():
        print("    stderr:", proc.stderr.strip()[-400:])


def main():
    script_py, test_go, script_go = sys.argv[1:4]
    machine()
    t = time.time()
    subprocess.run([sys.executable, "-c", "import build123d"], check=True)
    print("import build123d alone: %.1f s" % (time.time() - t))
    source = gear_source(test_go)
    cpu, mem = limits(script_go)
    run(script_py, {"source": source, "cpu_seconds": cpu, "memory_bytes": mem},
        "gear, FORGE limits (cpu %d s, AS %d MiB)" % (cpu, mem >> 20))
    run(script_py, {"source": source, "cpu_seconds": 600, "memory_bytes": 64 << 30},
        "gear, limits lifted")
    run(script_py, {"source": "result = Box(10, 10, 10)", "cpu_seconds": cpu, "memory_bytes": mem},
        "a single box, FORGE limits")


if __name__ == "__main__":
    main()
