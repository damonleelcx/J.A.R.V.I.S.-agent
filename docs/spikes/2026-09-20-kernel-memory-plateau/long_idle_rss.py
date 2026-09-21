"""The kernel process's memory over MANY exports, through its real main() loop.

PR 152 measured six exports and left one thing unexplained: with the
malloc_trim(0) it added, idle RSS still rose about 15 MiB per export (505 → 583
over six). Six is far too few to tell a bounded settling from an unbounded leak,
and the two have completely different consequences for a 2 GiB worker pod. This
runs the same loop long enough to tell them apart.

It is PR 152's docs/spikes/2026-09-17-kernel-last-walls/sidecar_idle_rss.py with
three additions:

  * --exports defaults to 24 rather than 4;
  * every sample also carries VmData, VmHWM, the smaps_rollup private/anonymous
    pages and the NUMBER OF MAPPINGS in /proc/<pid>/maps. The mapping count is
    the one that separates the two explanations: glibc keeping more arenas, or
    more distinct mmap'd blocks, shows up there, while a Python-object leak does
    not;
  * each sample is appended to the output file as it is taken, so a run that is
    killed still leaves every export it managed.

Linux only (/proc). Usage:
  python3 long_idle_rss.py <sidecar.py> <barrel-N.json> [--exports 24]
                           [--settle 2] [--out results.jsonl]
"""
import argparse
import json
import subprocess
import sys
import time


def proc_status(pid):
    out = {}
    with open("/proc/%d/status" % pid) as fh:
        for line in fh:
            if line.startswith(("VmRSS:", "VmHWM:", "VmData:", "VmSize:")):
                key, value = line.split(":")
                out[key.strip().lower() + "_mib"] = round(int(value.split()[0]) / 1024, 1)
    return out


def proc_rollup(pid):
    """Private_Dirty and Anonymous from smaps_rollup, plus the mapping count.

    Private_Dirty is the memory this process alone is keeping alive and cannot
    hand back without a trim or a free; Anonymous separates the heap from file
    pages (OCCT's own .so text is large and would otherwise flatter the total).
    """
    out = {}
    try:
        with open("/proc/%d/smaps_rollup" % pid) as fh:
            for line in fh:
                for want in ("Private_Dirty:", "Anonymous:", "Rss:"):
                    if line.startswith(want):
                        out["rollup_" + want[:-1].lower() + "_mib"] = round(
                            int(line.split()[1]) / 1024, 1)
    except OSError:
        pass
    try:
        with open("/proc/%d/maps" % pid) as fh:
            out["mappings"] = sum(1 for _ in fh)
    except OSError:
        pass
    return out


def sample(pid, **extra):
    row = dict(proc_status(pid))
    row.update(proc_rollup(pid))
    row.update(extra)
    return row


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("sidecar")
    ap.add_argument("barrel")
    ap.add_argument("--exports", type=int, default=24)
    ap.add_argument("--settle", type=float, default=2.0)
    ap.add_argument("--format", default="step")
    ap.add_argument("--out", default="")
    args = ap.parse_args()

    with open(args.barrel, "rb") as fh:
        doc = json.loads(fh.read())
    doc.update(format=args.format, skip_interferences=True)
    line = (json.dumps(doc) + "\n").encode()
    del doc

    out = open(args.out, "a", buffering=1) if args.out else None

    def emit(row):
        text = json.dumps(row)
        print(text, flush=True)
        if out:
            out.write(text + "\n")

    proc = subprocess.Popen([sys.executable, args.sidecar],
                            stdin=subprocess.PIPE, stdout=subprocess.PIPE)
    try:
        ready = proc.stdout.readline()
        emit(sample(proc.pid, step="ready", sidecar=args.sidecar,
                    banner=ready.decode().strip()))
        for n in range(1, args.exports + 1):
            t = time.time()
            proc.stdin.write(line)
            proc.stdin.flush()
            reply = proc.stdout.readline()
            took = time.time() - t
            if not reply:
                emit(dict(step="export %d" % n, ok=False, died=True))
                break
            ok = json.loads(reply).get("ok")
            size = len(reply)
            del reply
            time.sleep(args.settle)
            emit(sample(proc.pid, step="export %d idle" % n, export=n, ok=ok,
                        reply_mb=round(size / 1e6, 1), round_trip_s=round(took, 1)))
    finally:
        proc.stdin.close()
        try:
            proc.wait(timeout=60)
        except subprocess.TimeoutExpired:
            proc.kill()
        if out:
            out.close()


if __name__ == "__main__":
    main()
