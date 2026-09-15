"""Measure a synthetic airframe barrel through the kernel, up to 1,008,160 occurrences.

The scale-up milestone of docs/plan-2026-09-13-millions-of-parts.md. cad.BuildDocument
refuses more than 4096 parts (stage S0), so, like the K1, K2b, K4 and V1 spikes, this
calls the sidecar's own _build. The request is the one Go writes: run
TestScaleUp_MeasureAirframeBarrel first (see README), which expands the barrel document
and writes barrel-<bays>.json.

Every run is a child process, so the parent can read its memory and stop it:

  full  -- _build as shipped, format "": shapes, features, assembly, interference.
  mesh  -- format "mesh" with part properties, and the interference check REPLACED by
           broad_phase_only below, so K4 and V3 can be measured where V1 cannot finish.
  step  -- format "step", with the same replacement.

broad_phase_only does what _candidate_pairs does up to the boxes larger than a grid
cell, which are tested against every box: it runs the grid, counts the large-box tests
it skips, and times three large boxes against every box to price them.

Usage:
  <cad venv python> measure.py --dir D [--bays 1,9,30,100] [--modes full,mesh,step]
                               [--repeat 2] [--cap 1800] [--rss-cap-gb 24]
"""
import argparse
import importlib.util
import json
import math
import os
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
SIDECAR = os.path.join(HERE, "..", "..", "..", "internal", "domain", "cad", "sidecar.py")
MESH_FIELDS = ("mesh", "mesh_definitions", "mesh_instances")


def load_sidecar(path=SIDECAR):
    spec = importlib.util.spec_from_file_location("sidecar", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def broad_phase_only(sidecar, stats):
    def check(solids, ids, labels, placed=None):
        t0 = time.perf_counter()
        boxes, _ = sidecar._measures(solids, placed)
        t1 = time.perf_counter()
        present = [k for k, b in enumerate(boxes) if b is not None]
        longest = sorted(max(boxes[k][1][a] - boxes[k][0][a] for a in range(3)) for k in present)
        cell = max(longest[len(longest) // 2], 1e-6)
        cells, large = {}, []
        _cell = sidecar._cell
        for k in present:
            lo, hi = boxes[k]
            if max(hi[a] - lo[a] for a in range(3)) > sidecar._GRID_LARGE * cell:
                large.append(k)
                continue
            for cx in range(_cell(lo[0], cell), _cell(hi[0], cell) + 1):
                for cy in range(_cell(lo[1], cell), _cell(hi[1], cell) + 1):
                    for cz in range(_cell(lo[2], cell), _cell(hi[2], cell) + 1):
                        cells.setdefault((cx, cy, cz), []).append(k)
        pairs, tests = 0, 0
        for home, members in cells.items():
            for m, a in enumerate(members):
                for b in members[m + 1:]:
                    if (_cell(max(boxes[a][0][0], boxes[b][0][0]), cell) != home[0]
                            or _cell(max(boxes[a][0][1], boxes[b][0][1]), cell) != home[1]
                            or _cell(max(boxes[a][0][2], boxes[b][0][2]), cell) != home[2]):
                        continue
                    tests += 1
                    if not sidecar._boxes_miss(boxes[a], boxes[b]):
                        pairs += 1
        t2 = time.perf_counter()
        # The shipped loop, for the first three large boxes only.
        sample, sampled = large[:3], 0
        done = set()
        t3 = time.perf_counter()
        for a in sample:
            done.add(a)
            for b in present:
                if b in done:
                    continue
                sampled += 1
                sidecar._boxes_miss(boxes[a], boxes[b])
        t4 = time.perf_counter()
        n, big = len(present), len(large)
        stats.update({"measures_s": t1 - t0, "grid_s": t2 - t1, "cell_mm": cell, "grid_tests": tests,
                      "grid_pairs": pairs, "large": big, "large_tests": big * n - big * (big + 1) // 2,
                      "large_sample_tests": sampled,
                      "large_test_us": 1e6 * (t4 - t3) / sampled if sampled else 0.0})
        return [], False, tests, {"pairs": pairs, "booleans": 0, "reused": 0}
    return check


def child(mode, path, out_dir, tag, sidecar_path=SIDECAR, write_reply="0"):
    import psutil

    sidecar = load_sidecar(sidecar_path)
    t = time.perf_counter()
    with open(path, "rb") as fh:
        request = json.loads(fh.read())
    decode = time.perf_counter() - t
    stats = {}
    if mode != "full":
        sidecar._interferences = broad_phase_only(sidecar, stats)
    request["format"] = {"full": "", "mesh": "mesh", "step": "step"}[mode]
    request["properties"] = mode == "mesh"
    t = time.perf_counter()
    reply = sidecar._build(request)
    wall = time.perf_counter() - t
    res = {"mode": mode, "tag": tag, "decode_s": decode, "build_s": wall, "ok": reply.get("ok"),
           "error": reply.get("error"), "parts": reply.get("parts"), "shape_builds": reply.get("shape_builds"),
           "phases": reply.get("phases"), "volume": reply.get("volume"), "broad_phase_only": stats}
    for k in ("interference_box_tests", "interference_pairs", "interference_booleans", "interference_reused",
              "interferences_truncated", "mesh_triangles", "mesh_error", "mesh_simplified"):
        res[k] = reply.get(k)
    # Added 2026-09-15 (next scale walls). A reply lists at most a bounded number of
    # clashes and counts all of them in interferences_found; "found" is the count,
    # "listed" what the reply carries. A sidecar from before the bound lists them all.
    res["listed"] = len(reply.get("interferences") or [])
    res["found"] = reply.get("interferences_found", res["listed"])
    res["interferences_summarized"] = reply.get("interferences_summarized", False)
    # Added 2026-09-15 (large-box index). The mesh and step modes replace the check,
    # and their rows used to read interferences_truncated false and found 0 — a
    # check that never ran, recorded as a clean one.
    res["interference_checked"] = mode == "full"
    # Read before the reply is encoded below, which holds a second copy of it.
    mem = psutil.Process().memory_info()
    res["peak_rss_gb"] = getattr(mem, "peak_wset", mem.rss) / 1e9
    # Added 2026-09-15 (next scale walls): what the sidecar's main() writes to Go,
    # encoded as it encodes it, and the interference list's share of it.
    t = time.perf_counter()
    line = json.dumps(reply)
    res["reply_dumps_s"] = time.perf_counter() - t
    res["reply_bytes"] = len(line)
    res["interferences_bytes"] = len(json.dumps(reply.get("interferences") or []))
    if write_reply == "1":
        with open(os.path.join(out_dir, "reply-%s-%s.json" % (mode, tag)), "w") as fh:
            fh.write(line)
    del line
    if mode == "mesh":
        t = time.perf_counter()
        body = json.dumps({k: reply[k] for k in MESH_FIELDS if k in reply})
        res["payload_dumps_s"] = time.perf_counter() - t
        res["payload_bytes"] = len(body)
        res["mesh_definitions"] = len(reply.get("mesh_definitions") or [])
        res["mesh_instances"] = len(reply.get("mesh_instances") or [])
        res["mesh_own"] = len(reply.get("mesh") or [])
        res["properties"] = len(reply.get("part_properties") or [])
        with open(os.path.join(out_dir, "mesh-%s.json" % tag), "w") as fh:
            fh.write(body)
        del body
    if mode == "step":
        res["step_bytes"] = len(reply.get("step") or "") * 3 // 4
    sys.stdout.write("RESULT " + json.dumps(res) + "\n")
    sys.stdout.flush()


def interference_answer(res):
    """What the run says about interference, never blank: a truncated check and a
    check that did not run are said, not left to read as a clean one."""
    if not res.get("interference_checked"):
        if res.get("mode") == "full":
            return "interference: NO ANSWER (the build did not finish)"
        return "interference: NOT CHECKED (this mode runs the grid only)"
    pairs = res.get("interference_pairs") or 0
    checked = (res.get("interference_booleans") or 0) + (res.get("interference_reused") or 0)
    return "interference: %s%d of %d pairs checked, %d found (%d listed), %s booleans, %s reused, %s box tests" % (
        "TRUNCATED — " if res.get("interferences_truncated") else "", checked, pairs, res.get("found") or 0,
        res.get("listed") or 0, res.get("interference_booleans"), res.get("interference_reused"),
        res.get("interference_box_tests"))


def parent(args):
    import psutil

    results = os.path.join(args.dir, "results.jsonl")
    for bays in [int(b) for b in args.bays.split(",")]:
        path = os.path.join(args.dir, "barrel-%d.json" % bays)
        for mode in args.modes.split(","):
            for run in range(1, args.repeat + 1):
                others = sorted({p.info["name"] for p in psutil.process_iter(["name"])
                                 if (p.info["name"] or "").lower().split(".")[0] in ("go", "node", "python", "vitest")
                                 or (p.info["name"] or "").lower().endswith(".test.exe")})
                psutil.cpu_percent(None)
                started = time.time()
                proc = subprocess.Popen([sys.executable, os.path.abspath(__file__), "--child", mode, path,
                                         args.dir, str(bays), os.path.abspath(args.sidecar),
                                         "1" if args.write_reply else "0"], stdout=subprocess.PIPE, text=True)
                watched = psutil.Process(proc.pid)
                peak, stopped = 0.0, ""
                while proc.poll() is None:
                    # A venv's python.exe on Windows is a launcher: the interpreter is its child.
                    try:
                        tree = [watched] + watched.children(recursive=True)
                        peak = max([peak] + [p.memory_info().rss / 1e9 for p in tree])
                    except psutil.Error:
                        pass
                    if peak > args.rss_cap_gb:
                        stopped = "rss over %.0f GB" % args.rss_cap_gb
                    elif time.time() - started > args.cap:
                        stopped = "over %d s" % args.cap
                    if stopped:
                        for p in watched.children(recursive=True):
                            try:
                                p.kill()
                            except psutil.Error:
                                pass
                        proc.kill()
                        break
                    time.sleep(0.25)
                out = proc.communicate()[0] or ""
                wall = time.time() - started
                cpu = psutil.cpu_percent(None)
                line = next((l[len("RESULT "):] for l in out.splitlines() if l.startswith("RESULT ")), None)
                res = json.loads(line) if line else {"mode": mode, "tag": str(bays), "ok": False,
                                                     "error": stopped or "child exited %s" % proc.returncode}
                res.update({"bays": bays, "run": run, "child_wall_s": wall, "polled_peak_rss_gb": peak,
                            "stopped": stopped, "system_cpu_pct": cpu, "other_processes": others,
                            "sidecar": os.path.abspath(args.sidecar), "started": time.strftime(
                                "%Y-%m-%dT%H:%M:%S", time.localtime(started))})
                with open(results, "a") as fh:
                    fh.write(json.dumps(res) + "\n")
                ph = res.get("phases") or {}
                print("bays=%d %s run %d: ok=%s build %.1fs [shapes %.1f assembly %.1f interf %.1f props %.1f "
                      "mesh %.1f export %.1f] peak %.1f GB cpu %.0f%% %s" % (
                          bays, mode, run, res.get("ok"), res.get("build_s") or 0, ph.get("shapes", 0),
                          ph.get("assembly", 0), ph.get("interferences", 0), ph.get("properties", 0),
                          ph.get("mesh", 0), ph.get("export", 0), res.get("peak_rss_gb") or peak, cpu, stopped))
                print("  " + interference_answer(res))
                sys.stdout.flush()
                if stopped:
                    break


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "--child":
        child(*sys.argv[2:8])
        return
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True)
    ap.add_argument("--bays", default="1,9,30,100")
    ap.add_argument("--modes", default="full,mesh,step")
    ap.add_argument("--repeat", type=int, default=2)
    ap.add_argument("--cap", type=int, default=1800)
    ap.add_argument("--rss-cap-gb", type=float, default=24.0)
    # Added 2026-09-15 (next scale walls): a frozen copy of the sidecar to measure, so
    # edits in the tree cannot reach a run in progress; and the reply written to
    # reply-<mode>-<bays>.json, for the Go decode measurement.
    ap.add_argument("--sidecar", default=SIDECAR)
    ap.add_argument("--write-reply", action="store_true")
    parent(ap.parse_args())


if __name__ == "__main__":
    main()
