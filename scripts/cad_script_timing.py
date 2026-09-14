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


def sample(pid):
    """Threads and address space of a running process, from /proc (Linux only)."""
    status = "/proc/%d/status" % pid
    if not os.path.exists(status):
        return ""
    fields = dict(line.split(":", 1) for line in open(status) if ":" in line)
    return "at 20 s: threads %s, VmSize %s, VmPeak %s, state %s" % tuple(
        fields.get(k, "?").strip() for k in ("Threads", "VmSize", "VmPeak", "State"))


def run(script_py, request, label, env=None, patience=60):
    """Run script.py once.

    communicate() drains stdout and stderr WHILE it waits. Reading only after the
    process exits deadlocks as soon as the reply outgrows a pipe buffer: a built
    solid's reply does, a box's does not, and the first version of this probe
    reported every gear as hung on a laptop where it builds in 2 s. A run still
    alive at 20 s is sampled; one alive at `patience` is killed and reported HUNG.
    """
    t = time.time()
    proc = subprocess.Popen([sys.executable, script_py], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True, env=env)
    note = ""
    try:
        out, err = proc.communicate(json.dumps(request), timeout=20)
    except subprocess.TimeoutExpired:
        note = sample(proc.pid)
        try:
            out, err = proc.communicate(timeout=max(1, patience - 20))
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.communicate()
            print("%-48s HUNG: still running after %.0f s  %s" % (label, time.time() - t, note))
            return
    wall = time.time() - t
    last = (out.strip().splitlines() or ["(no output)"])[-1]
    try:
        reply = json.loads(last)
        verdict = "ok" if reply.get("ok") else "error: " + str(reply.get("error"))[:200]
    except ValueError:
        verdict = "no JSON reply: " + last[:200]
    print("%-48s %6.1f s  exit %s  %s  %s" % (label, wall, proc.returncode, verdict, note))
    if err.strip():
        print("    stderr:", err.strip()[-300:])


def main():
    script_py, test_go, script_go = sys.argv[1:4]
    machine()
    t = time.time()
    subprocess.run([sys.executable, "-c", "import build123d"], check=True)
    print("import build123d alone: %.1f s" % (time.time() - t))
    source = gear_source(test_go)
    cpu, mem = limits(script_go)
    lifted_cpu, lifted_mem = 600, 64 << 30
    one_thread = dict(os.environ, OMP_NUM_THREADS="1", OPENBLAS_NUM_THREADS="1", TBB_NUM_THREADS="1",
                      MKL_NUM_THREADS="1")
    gear = lambda c, m: {"source": source, "cpu_seconds": c, "memory_bytes": m}
    run(script_py, gear(cpu, mem), "gear, FORGE limits (cpu %d s, AS %d MiB)" % (cpu, mem >> 20))
    run(script_py, gear(lifted_cpu, mem), "gear, address-space cap only")
    run(script_py, gear(cpu, lifted_mem), "gear, CPU limit only")
    run(script_py, gear(lifted_cpu, lifted_mem), "gear, limits lifted")
    run(script_py, gear(cpu, mem), "gear, FORGE limits, one thread", env=one_thread)
    run(script_py, {"source": "result = Box(10, 10, 10)", "cpu_seconds": cpu, "memory_bytes": mem},
        "a single box, FORGE limits")

if __name__ == "__main__":
    main()
