"""The kernel build ceiling, measured through the mesh endpoint of a real forged.

Phase 4 follow-up to K1-K4 (docs/plan-2026-09-13-millions-of-parts.md). Every earlier
number past 4,096 parts was taken through the sidecar's own _build; this one goes through
the product: GET /v1/geometry/{id}/mesh on forged, which calls cad.Kernel.BuildMesh ->
BuildDocument -> the pool -> the sidecar, and writes the JSON the browser reads.

forged refuses a build past geometry/limits.go maxDrawnParts, so it is run from a binary
built with that constant lifted (and, for the "notimeout" runs, cad.go buildTimeout lifted
too, so a build past 30 s reports its phases instead of being killed). Neither edit is
committed; README.md beside this file says exactly what was built.

Each run starts forged afresh (so its kernel is a new process and both peaks are this
run's), signs in, warms the kernel with a four-part design, then asks for one mesh while a
thread samples the working set of forged and of every process forged started. Windows'
own PeakWorkingSetSize for each process is read at the end as well, which catches a peak
between two samples. One JSON row per run is appended to --out.

Stdlib only, Windows only (ctypes, psapi, toolhelp).

    python measure.py --binary forged-lifted-notimeout.exe --docs DIR --runs 2 --out results.jsonl \
        car-4096 car-8192 ... barrel-60640
"""

import argparse
import ctypes
import ctypes.wintypes as wt
import json
import os
import secrets
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

PORT = 18431
BASE = "http://127.0.0.1:%d" % PORT
EMAIL = "ceiling-measure@example.test"
PASSWORD = "ceiling-measure-password-1"

kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
psapi = ctypes.WinDLL("psapi", use_last_error=True)


class PMC(ctypes.Structure):
    _fields_ = [("cb", wt.DWORD), ("PageFaultCount", wt.DWORD),
                ("PeakWorkingSetSize", ctypes.c_size_t), ("WorkingSetSize", ctypes.c_size_t),
                ("QuotaPeakPagedPoolUsage", ctypes.c_size_t), ("QuotaPagedPoolUsage", ctypes.c_size_t),
                ("QuotaPeakNonPagedPoolUsage", ctypes.c_size_t), ("QuotaNonPagedPoolUsage", ctypes.c_size_t),
                ("PagefileUsage", ctypes.c_size_t), ("PeakPagefileUsage", ctypes.c_size_t)]


class PE32(ctypes.Structure):
    _fields_ = [("dwSize", wt.DWORD), ("cntUsage", wt.DWORD), ("th32ProcessID", wt.DWORD),
                ("th32DefaultHeapID", ctypes.c_void_p), ("th32ModuleID", wt.DWORD),
                ("cntThreads", wt.DWORD), ("th32ParentProcessID", wt.DWORD),
                ("pcPriClassBase", ctypes.c_long), ("dwFlags", wt.DWORD),
                ("szExeFile", ctypes.c_wchar * 260)]


kernel32.OpenProcess.restype = wt.HANDLE
kernel32.CreateToolhelp32Snapshot.restype = wt.HANDLE


def memory(pid):
    """(working set, peak working set, peak commit) in bytes, or None."""
    h = kernel32.OpenProcess(0x1000 | 0x0010, False, pid)  # QUERY_LIMITED_INFORMATION | VM_READ
    if not h:
        return None
    try:
        c = PMC()
        c.cb = ctypes.sizeof(PMC)
        if not psapi.GetProcessMemoryInfo(h, ctypes.byref(c), c.cb):
            return None
        return c.WorkingSetSize, c.PeakWorkingSetSize, c.PeakPagefileUsage
    finally:
        kernel32.CloseHandle(h)


def processes():
    """[(pid, parent, exe)] for every process."""
    snap = kernel32.CreateToolhelp32Snapshot(2, 0)
    out = []
    e = PE32()
    e.dwSize = ctypes.sizeof(PE32)
    ok = kernel32.Process32FirstW(snap, ctypes.byref(e))
    while ok:
        out.append((e.th32ProcessID, e.th32ParentProcessID, e.szExeFile))
        ok = kernel32.Process32NextW(snap, ctypes.byref(e))
    kernel32.CloseHandle(snap)
    return out


def descendants(root):
    procs = processes()
    found, frontier = [], {root}
    while frontier:
        nxt = {p for p, parent, _ in procs if parent in frontier and p not in found and p != root}
        found.extend(nxt)
        frontier = nxt
    return found


class FILETIME(ctypes.Structure):
    _fields_ = [("lo", wt.DWORD), ("hi", wt.DWORD)]


def cpu_times():
    idle, kern, user = FILETIME(), FILETIME(), FILETIME()
    kernel32.GetSystemTimes(ctypes.byref(idle), ctypes.byref(kern), ctypes.byref(user))
    v = lambda f: (f.hi << 32) | f.lo
    return v(idle), v(kern) + v(user)


def others():
    """Other heavy processes on the machine, for the load column."""
    rows = []
    for pid, _, exe in processes():
        if exe.lower() in ("python.exe", "go.exe", "node.exe", "forged.exe", "chrome.exe") or exe.lower().endswith(".test.exe"):
            m = memory(pid)
            if m and m[0] > 200 << 20:
                rows.append("%s:%d MiB" % (exe, m[0] >> 20))
    return rows


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


def environment(args, scratch):
    env = dict(os.environ)
    env.update({
        "FORGE_ENV": "development",
        "FORGE_HTTP_ADDR": "127.0.0.1:%d" % PORT,
        "FORGE_PUBLIC_URL": "http://localhost:%d" % PORT,
        "FORGE_HTTP_WRITE_TIMEOUT": args.write_timeout,
        "FORGE_DATABASE_URL": args.database,
        "FORGE_SESSION_SECRET": args.secret,
        "FORGE_MAIL_TRANSPORT": "file",
        "FORGE_MAIL_OUTBOX_DIR": os.path.join(scratch, "outbox"),
        # No conversation is held; the model endpoint is a port nothing listens on.
        "FORGE_LLM_BASE_URL": "http://127.0.0.1:9/v1",
        "FORGE_LLM_API_KEY": "unused",
        "FORGE_DATA_BOUNDARY": "no_training",
        "FORGE_MEDIA_ENABLED": "false",
        "FORGE_LOG_FORMAT": "json",
        "FORGE_CAD_PYTHON": args.python,
        "FORGE_CAD_POOL": "1",
    })
    return env


def start(args, scratch, log):
    proc = subprocess.Popen([args.binary], cwd=scratch, env=environment(args, scratch),
                            stdout=log, stderr=subprocess.STDOUT)
    for _ in range(300):
        time.sleep(0.2)
        if proc.poll() is not None:
            raise SystemExit("forged exited with %s; see %s" % (proc.returncode, log.name))
        try:
            if http("GET", "/healthz", timeout=2)[0] == 200:
                return proc
        except Exception:
            pass
    raise SystemExit("forged did not become healthy")


def stop(proc):
    for pid in descendants(proc.pid):
        subprocess.run(["taskkill", "/F", "/PID", str(pid)], capture_output=True)
    proc.kill()
    proc.wait()


def sign_in():
    status, body = http("POST", "/v1/auth/sign-in", {"email": EMAIL, "password": PASSWORD})
    if status != 200:
        raise SystemExit("sign-in: %s %s" % (status, body[:300]))
    j = json.loads(body)
    return j["token"], j["user"]["id"]


def ensure_stored(args, scratch, names):
    """Sign up once and store every design once; the version ids are kept in stored.json."""
    path = os.path.join(scratch, "stored.json")
    stored = json.load(open(path)) if os.path.exists(path) else {}
    missing = [n for n in names + ["tiny"] if n not in stored]
    if not missing:
        return stored
    with open(os.path.join(scratch, "forged-setup.log"), "wb") as log:
        proc = start(args, scratch, log)
        try:
            http("POST", "/v1/auth/sign-up", {"email": EMAIL, "password": PASSWORD, "display_name": "Ceiling measure"})
            _, user = sign_in()
        finally:
            stop(proc)
    tiny = os.path.join(args.docs, "tiny.json")
    if not os.path.exists(tiny):
        json.dump({"name": "Warm-up", "units": "mm", "not_verified": ["a kernel warm-up fixture"], "parts": [
            {"id": "b%d" % i, "shape": "box", "size": {"width": 10, "height": 10, "depth": 10},
             "position": [20 * i, 0, 0], "rotation": [0, 0, 0], "color": "#888888", "opacity": 1}
            for i in range(4)]}, open(tiny, "w"))
    env = environment(args, scratch)
    for n in missing:
        out = subprocess.run([args.store, "-user", user, "-design", os.path.join(args.docs, n + ".json"),
                              "-generator", "docs/spikes/2026-09-15-kernel-build-ceiling " + n],
                             cwd=scratch, env=env, capture_output=True, text=True)
        if out.returncode != 0:
            raise SystemExit("storing %s: %s" % (n, out.stdout + out.stderr))
        stored[n] = json.loads(out.stdout.strip().splitlines()[-1])["version_id"]
        json.dump(stored, open(path, "w"), indent=1)
    return stored


def measure(args, scratch, name, version, warm, run):
    logpath = os.path.join(scratch, "forged-%s-%s-%d.log" % (args.label, name, run))
    with open(logpath, "wb") as log:
        proc = start(args, scratch, log)
        try:
            token, _ = sign_in()
            t = time.perf_counter()
            status, body = http("GET", "/v1/geometry/%s/mesh" % warm, token=token)
            warm_s = time.perf_counter() - t
            if status != 200:
                raise SystemExit("warm-up mesh: %s %s" % (status, body[:300]))
            kids = descendants(proc.pid)
            base = {pid: memory(pid) for pid in kids}
            peak = {"forged": 0, "kernel": 0}
            done = threading.Event()

            def sample():
                seen = set(kids)
                last = time.perf_counter()
                while not done.is_set():
                    m = memory(proc.pid)
                    if m:
                        peak["forged"] = max(peak["forged"], m[0])
                    if time.perf_counter() - last > 1:
                        seen.update(descendants(proc.pid))
                        last = time.perf_counter()
                    total = 0
                    for pid in seen:
                        mm = memory(pid)
                        if mm:
                            total += mm[0]
                    peak["kernel"] = max(peak["kernel"], total)
                    done.wait(0.1)
                peak["kernel_pids"] = sorted(seen)

            sampler = threading.Thread(target=sample)
            idle0, busy0 = cpu_times()
            sampler.start()
            t = time.perf_counter()
            status, body = http("GET", "/v1/geometry/%s/mesh" % version, token=token)
            wall = time.perf_counter() - t
            done.set()
            sampler.join()
            idle1, busy1 = cpu_times()
            f = memory(proc.pid)
            kernel_now = [memory(pid) for pid in descendants(proc.pid)]
            row = {
                "label": args.label, "design": name, "run": run, "status": status, "wall_s": round(wall, 3),
                "bytes": len(body), "warmup_s": round(warm_s, 2),
                "forged_peak_ws_mib_sampled": peak["forged"] >> 20,
                "forged_peak_ws_mib_os": (f[1] >> 20) if f else None,
                "kernel_peak_ws_mib_sampled": peak["kernel"] >> 20,
                "kernel_ws_mib_after": sum(m[0] for m in kernel_now if m) >> 20,
                "kernel_peak_ws_mib_os": max([m[1] for m in kernel_now if m] or [0]) >> 20,
                "kernel_peak_commit_mib_os": max([m[2] for m in kernel_now if m] or [0]) >> 20,
                "kernel_pids_before": kids, "kernel_pids_during": peak.get("kernel_pids"),
                "kernel_ws_mib_before": sum((m[0] for m in base.values() if m)) >> 20,
                "system_cpu_pct": round(100.0 * (1 - (idle1 - idle0) / max(1, busy1 - busy0)), 1),
                "others": others(),
                "at": time.strftime("%Y-%m-%dT%H:%M:%S"),
            }
            if status == 200:
                reply = json.loads(body)
                row.update({"instances": len(reply.get("instances") or []),
                            "definitions": len(reply.get("definitions") or []),
                            "placed_parts": len(reply.get("parts") or []), "triangles": reply.get("triangles"),
                            "skipped": len(reply.get("skipped") or []), "mesh_error": reply.get("mesh_error")})
            else:
                row["error"] = body[:600].decode("utf-8", "replace")
        finally:
            stop(proc)
    with open(logpath, "rb") as fh:
        for line in fh:
            try:
                j = json.loads(line)
            except Exception:
                continue
            if j.get("version_id") == version and ("kernel_mesh_ms" in j or "code" in j):
                row["log"] = {k: v for k, v in j.items() if k.startswith("kernel_") or k in
                              ("msg", "event", "parts", "definitions", "triangles", "skipped", "code")}
            elif "cad" in json.dumps(j).lower() and j.get("level") in ("WARN", "ERROR", "warn", "error"):
                row.setdefault("cad_warnings", []).append(str(j)[:400])
    return row


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--binary", required=True)
    p.add_argument("--label", required=True)
    p.add_argument("--docs", required=True)
    p.add_argument("--store", required=True)
    p.add_argument("--python", required=True)
    p.add_argument("--database", required=True)
    p.add_argument("--secret", default="ceiling-measure-" + "x" * 40)
    p.add_argument("--write-timeout", default="5m")
    p.add_argument("--runs", type=int, default=1)
    p.add_argument("--out", required=True)
    p.add_argument("designs", nargs="+")
    args = p.parse_args()
    scratch = os.path.dirname(os.path.abspath(args.out))
    stored = ensure_stored(args, scratch, args.designs)
    for name in args.designs:
        for run in range(1, args.runs + 1):
            row = measure(args, scratch, name, stored[name], stored["tiny"], run)
            with open(args.out, "a") as fh:
                fh.write(json.dumps(row) + "\n")
            print(json.dumps({k: row.get(k) for k in ("label", "design", "run", "status", "wall_s", "bytes",
                                                        "forged_peak_ws_mib_os", "kernel_peak_ws_mib_os",
                                                        "system_cpu_pct", "log")}), flush=True)


if __name__ == "__main__":
    main()
