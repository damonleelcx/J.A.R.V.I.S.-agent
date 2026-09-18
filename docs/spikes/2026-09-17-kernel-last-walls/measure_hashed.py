"""One barrel build, timed, with a hash of its whole answer (docs/spikes/2026-09-17-kernel-last-walls).

#89's measure.py child, trimmed to the two modes measured here, plus what the
equivalence claim needs at scale: a SHA-256 of the reply with `phases` removed
(json.dumps, sorted keys), and one of `part_properties` alone. Two sidecars that give
the same hashes gave the same answer, bit for bit, to every field of the reply.

  full  -- _build as shipped, format "": shapes, features, assembly, interference.
  mesh  -- format "mesh" with part properties, the interference check replaced by
           measure.py's broad_phase_only (the grid only), as #89 defined the mode.

Usage (as measure_bounded.py calls it; the last argument is ignored):
  <cad venv python> measure_hashed.py --child <mode> <barrel-N.json> <out dir> <tag> <sidecar.py> 0
"""
import hashlib
import importlib.util
import json
import os
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
MEASURE = os.path.join(HERE, "..", "2026-09-15-one-million-occurrences", "measure.py")


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def child(mode, path, tag, sidecar_path):
    import psutil

    measure = load("measure", MEASURE)
    sidecar = load("sidecar", sidecar_path)
    with open(path, "rb") as fh:
        request = json.loads(fh.read())
    stats = {}
    if mode != "full":
        sidecar._interferences = measure.broad_phase_only(sidecar, stats)
    request["format"] = {"full": "", "mesh": "mesh"}[mode]
    request["properties"] = mode == "mesh"
    t = time.perf_counter()
    reply = sidecar._build(request)
    wall = time.perf_counter() - t
    mem = psutil.Process().memory_info()
    res = {"mode": mode, "tag": tag, "build_s": wall, "ok": reply.get("ok"), "error": reply.get("error"),
           "parts": reply.get("parts"), "phases": reply.get("phases"), "volume": reply.get("volume"),
           "peak_rss_gb": getattr(mem, "peak_wset", mem.rss) / 1e9,
           "found": reply.get("interferences_found"), "interference_booleans": reply.get("interference_booleans"),
           "interference_reused": reply.get("interference_reused"),
           "interference_pairs": reply.get("interference_pairs"),
           "properties": len(reply.get("part_properties") or [])}
    del request
    phases = reply.pop("phases", None)
    t = time.perf_counter()
    props = reply.get("part_properties")
    if props is not None:
        res["properties_sha256"] = hashlib.sha256(json.dumps(props).encode()).hexdigest()
    res["answer_sha256"] = hashlib.sha256(json.dumps(reply, sort_keys=True).encode()).hexdigest()
    res["hash_s"] = time.perf_counter() - t
    reply["phases"] = phases
    sys.stdout.write("RESULT " + json.dumps(res) + "\n")
    sys.stdout.flush()


if __name__ == "__main__":
    if len(sys.argv) >= 7 and sys.argv[1] == "--child":
        child(sys.argv[2], sys.argv[3], sys.argv[5], sys.argv[6])
    else:
        raise SystemExit(__doc__)
