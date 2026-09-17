"""A build with Python's cycle collector paused, against the same build with it running,
for build_without_gc_kernel_test.go (docs/spikes/2026-09-17-one-million-after).

Usage: build_without_gc.py <sidecar.py>

1. The same request through _build with _BUILD_WITHOUT_GC off and on, in every format
   ("", "mesh" with part properties, "step", and the export job's skip_interferences):
   the replies are compared WHOLE as JSON, less "phases" (seconds) and "step" (compared
   below its header, where the writer stamps the time). Each request is built once first
   and the result discarded: a pair's first OCCT boolean on a cold process can differ from
   its later ones in the last bits (docs/spikes/2026-09-16-interference-approach), which is
   not what this compares.
2. How many times the collector ran DURING each build (gc.callbacks), with the switch off
   and on, and how many unreachable objects a gc.collect() finds straight after a build
   made with the collector paused.
3. Whether the collector is running after a build that returned, after one that raised,
   and — when the caller had paused it — whether it is still paused.

Prints one JSON object; "problems" lists every difference found, as sentences.
"""

import gc
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))


def load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def request_for(copies):
    fixture = load(os.path.join(HERE, "placed_copies.py"), "placed_copies").fixture(copies)
    solids = list(fixture["solids"])
    # A plate with 40 pins through it: clashes, most of them measured once and reused.
    solids.append({"id": "deck", "label": "Deck", "shape": "box", "dims": {"width": 400.0, "height": 6.0,
                   "depth": 400.0}, "matrix": [1, 0, 0, 0, 1, 0, 0, 0, 1], "position": [0.0, 5000.0, 0.0]})
    for i in range(40):
        solids.append({"id": "pin-%d" % i, "label": "Pin", "shape": "cylinder",
                       "dims": {"radius": 2.0, "height": 20.0, "radius_top": 2.0},
                       "matrix": [1, 0, 0, 0, 1, 0, 0, 0, 1],
                       "position": [-190.0 + 9.5 * i, 5000.0, (i % 5) * 30.0 - 60.0]})
    # A part that cannot be built, and a feature naming a part that is not there.
    solids.append({"id": "broken", "label": "Broken", "shape": "cylinder", "dims": {"radius": -1.0, "height": 5.0},
                   "matrix": [1, 0, 0, 0, 1, 0, 0, 0, 1], "position": [0.0, -5000.0, 0.0]})
    operations = list(fixture["operations"]) + [{"id": "ghost", "op": "cut", "of": "deck", "with": ["nowhere"]}]
    return {"solids": solids, "operations": operations}


def comparable(reply, step_body):
    out = {k: v for k, v in reply.items() if k not in ("phases", "step")}
    if "step" in reply:
        out["step_body"] = step_body(reply)
    return json.dumps(out, sort_keys=True)


def build(sidecar, request, paused):
    sidecar._BUILD_WITHOUT_GC = paused
    runs = [0]

    def count(phase, info):
        if phase == "start":
            runs[0] += 1

    # Copied BEFORE counting: decoding the copy allocates with the collector running,
    # outside the build, and a collection it triggers is not the build's.
    request = json.loads(json.dumps(request))
    gc.callbacks.append(count)
    try:
        reply = sidecar._build(request)
    finally:
        gc.callbacks.remove(count)
        sidecar._BUILD_WITHOUT_GC = True
    return reply, runs[0]


def main():
    sidecar = load(sys.argv[1], "sidecar")
    step_body = load(os.path.join(HERE, "placed_copies.py"), "placed_copies_step").step_body
    problems, rows = [], []
    base = request_for(8)
    for name, extra in (("format ''", {"format": ""}), ("mesh", {"format": "mesh", "properties": True}),
                        ("step", {"format": "step"}), ("export job", {"format": "step", "skip_interferences": True})):
        request = dict(base, **extra)
        build(sidecar, request, False)  # warm: see the docstring
        gc.collect()
        ref, runs_on = build(sidecar, request, False)
        gc.collect()
        new, runs_paused = build(sidecar, request, True)
        unreachable = gc.collect()
        row = {"request": name, "parts": new.get("parts"), "found": new.get("interferences_found"),
               "collections_running": runs_on, "collections_paused": runs_paused,
               "unreachable_after_paused_build": unreachable, "identical": comparable(ref, step_body) == comparable(new, step_body)}
        rows.append(row)
        if not new.get("ok"):
            problems.append("%s: the build failed: %s" % (name, new.get("error")))
        if not row["identical"]:
            a, b = comparable(ref, step_body), comparable(new, step_body)
            k = next((i for i in range(min(len(a), len(b))) if a[i] != b[i]), min(len(a), len(b)))
            problems.append("%s: the reply differs with the collector paused, at %d: %r against %r" % (
                name, k, b[max(0, k - 80):k + 80], a[max(0, k - 80):k + 80]))
        if runs_paused != 0:
            problems.append("%s: the cycle collector ran %d time(s) during a build with _BUILD_WITHOUT_GC" % (
                name, runs_paused))
        if unreachable != 0:
            problems.append("%s: a build left %d unreachable object(s) for the collector" % (name, unreachable))

    # Restored, however the build ends.
    small = request_for(1)
    sidecar._build(dict(small, format=""))
    if not gc.isenabled():
        problems.append("the cycle collector is still paused after a build returned")
    real = sidecar._interferences

    def failing(*a, **k):
        raise RuntimeError("a check that raises")

    sidecar._interferences = failing
    raised = False
    try:
        sidecar._build(dict(small, format=""))
    except RuntimeError:
        raised = True
    finally:
        sidecar._interferences = real
    if not raised:
        problems.append("the failing check did not raise, so the restore after a raise was not tested")
    if not gc.isenabled():
        problems.append("the cycle collector is still paused after a build raised")
        gc.enable()
    gc.disable()
    try:
        sidecar._build(dict(small, format=""))
        if gc.isenabled():
            problems.append("a build turned the cycle collector on when its caller had paused it")
    finally:
        gc.enable()
    json.dump({"rows": rows, "problems": problems}, sys.stdout)


if __name__ == "__main__":
    main()
