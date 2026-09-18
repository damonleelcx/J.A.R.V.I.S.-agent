"""Where the kernel's memory goes after a STEP export, on Linux (docs/spikes/2026-09-17-kernel-last-walls).

#145 measured the worker's kernel keeping ~1.2-1.3 GiB after an 89,744-occurrence
export (733 -> 1,385 -> 1,280 MiB) and levelling at ~1.44 GiB over repeats, and did not
say why. This runs the export job's request (format "step", skip_interferences) the way
the sidecar's main() does — the request line parsed, _build, the reply encoded and
written — `--exports` times in one process, and reads after each:

  - VmRSS / VmHWM from /proc/self/status;
  - glibc's mallinfo2(): `arena` (bytes the main heap took with brk), `hblkhd` (bytes in
    mmap'd chunks), `uordblks` (bytes in use), `fordblks` (bytes FREE inside the heaps
    but still the process's), `keepcost` (the free top chunk). Free bytes the process
    still holds are fragmentation; bytes in use are not;
  - how many documents the XDE application still has open (a document it kept would
    be memory in use, not fragmentation);
  - the objects Python's cycle collector tracks.

Steps per export:

  idle      as main() leaves it waiting for the next line: its loop variables `line`,
            `request` and `reply` still hold the last request and reply;
  dropped   those three released;
  remedy    then one of --remedy: none, gc (gc.collect()), trim (glibc
            malloc_trim(0), which hands free heap pages back to the kernel), both.

Prints one JSON line per step. Linux + glibc only; run in the worker image or
forge-linux-test (python:3.13-slim with the pinned requirements).

Usage: python3 export_memory.py <sidecar.py> <barrel-N.json> [--exports 3] [--remedy none]
"""
import argparse
import ctypes
import ctypes.util
import gc
import importlib.util
import json
import os
import sys
import time


class Mallinfo2(ctypes.Structure):
    _fields_ = [(name, ctypes.c_size_t) for name in (
        "arena", "ordblks", "smblks", "hblks", "hblkhd", "usmblks", "fsmblks", "uordblks", "fordblks", "keepcost")]


LIBC = ctypes.CDLL(ctypes.util.find_library("c") or "libc.so.6")
LIBC.mallinfo2.restype = Mallinfo2
LIBC.malloc_trim.argtypes = [ctypes.c_size_t]
LIBC.malloc_trim.restype = ctypes.c_int


def status():
    out = {}
    with open("/proc/self/status") as fh:
        for line in fh:
            if line.startswith(("VmRSS:", "VmHWM:")):
                key, value = line.split(":")
                out[key.strip().lower() + "_mib"] = round(int(value.split()[0]) / 1024, 1)
    m = LIBC.mallinfo2()
    for name in ("arena", "hblkhd", "uordblks", "fordblks", "keepcost"):
        out[name + "_mib"] = round(getattr(m, name) / 2**20, 1)
    return out


def step_body_sha(b64):
    """SHA-256 of the STEP file below its header (the header carries a timestamp)."""
    if not b64:
        return None
    import base64
    import hashlib

    text = base64.b64decode(b64)
    return hashlib.sha256(text[text.index(b"DATA;"):]).hexdigest()[:16]


def emit(step, **extra):
    row = {"step": step, "t": round(time.time(), 2)}
    row.update(status())
    row.update(extra)
    print(json.dumps(row), flush=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("sidecar")
    ap.add_argument("barrel")
    ap.add_argument("--exports", type=int, default=3)
    ap.add_argument("--remedy", default="none", choices=("none", "gc", "trim", "both"))
    ap.add_argument("--occurrences", type=int, default=0, help="first N solids only (0: all)")
    args = ap.parse_args()
    spec = importlib.util.spec_from_file_location("sidecar", args.sidecar)
    sidecar = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(sidecar)
    app = sidecar.XCAFApp_Application.GetApplication_s()
    emit("imported", remedy=args.remedy, sidecar=os.path.basename(args.sidecar))
    with open(args.barrel, "rb") as fh:
        doc = json.loads(fh.read())
    if args.occurrences:
        doc["solids"] = doc["solids"][:args.occurrences]
    doc.update(format="step", skip_interferences=True)
    text = json.dumps(doc) + "\n"
    solids = len(doc["solids"])
    del doc
    emit("request text ready", solids=solids, line_mb=round(len(text) / 1e6, 1))
    sink = open(os.devnull, "w")
    for n in range(1, args.exports + 1):
        # main()'s loop body: the same three names, left bound when it goes idle.
        line = text.strip()
        t = time.perf_counter()
        request = json.loads(line)
        reply = sidecar._build(request)
        took = time.perf_counter() - t
        sink.write(json.dumps(reply) + "\n")
        sink.flush()
        emit("export %d idle" % n, ok=reply.get("ok"), build_s=round(took, 1),
             step_b64_mb=round(len(reply.get("step") or "") / 1e6, 1), documents=app.NbDocuments(),
             step_body_sha256=step_body_sha(reply.get("step")),
             phases={k: round(v, 2) for k, v in (reply.get("phases") or {}).items()})
        del line, request, reply
        emit("export %d dropped" % n, documents=app.NbDocuments(), gc_tracked=len(gc.get_objects()))
        found, trimmed = None, None
        t = time.perf_counter()
        if args.remedy in ("gc", "both"):
            found = gc.collect()
        if args.remedy in ("trim", "both"):
            trimmed = LIBC.malloc_trim(0)
        emit("export %d %s" % (n, args.remedy), gc_found=found, trim_returned=trimmed,
             remedy_ms=round(1000 * (time.perf_counter() - t), 1))
    # Whatever the remedy was, what the other two would still find now.
    found = gc.collect()
    emit("end gc.collect()", gc_found=found)
    t = time.perf_counter()
    trimmed = LIBC.malloc_trim(0)
    emit("end malloc_trim(0)", trim_returned=trimmed, remedy_ms=round(1000 * (time.perf_counter() - t), 1))


if __name__ == "__main__":
    sys.exit(main())
