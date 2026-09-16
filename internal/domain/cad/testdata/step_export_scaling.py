"""The sidecar's STEP export past Go's 4,096-part build ceiling, for
step_export_scaling_kernel_test.go.

Usage: step_export_scaling.py <sidecar.py> <small> <large> <readback> <readback.step>

Exports <small> and <large> occurrences through _build, twice each, and the large
one once more with the writer's validation-property walk switched back on
(_STEP_WRITE_PROPS), and compares those two large files here. Then exports
<readback> occurrences once, as shipped, to <readback.step> for the Go test to read
back, and prints one JSON object.

# Why the read-back is a smaller export

‼️ OCCT's STEP READER is the slow direction: reading the 65,536-occurrence file back
with testdata/step_reimport.py did not finish in 18 minutes (measured 2026-09-15,
the first version of this fence, killed by go test's timeout). The file equality is
checked at the large size, where the walk shows; the read-back only has to show that
names and placements survive, at a size past K2's fence and within a test's time.

# Why through _build and not cad.BuildDocument

Go refuses more than 4,096 parts before the kernel is asked (stage S0), and the
export was linear up to there: K2's fence covered 512 -> 4,096 and could not see a
walk that is quadratic only in the occurrences under ONE assembly. The barrel
measured 0.6 s at 10k and 20 s at 90k (docs/spikes/2026-09-15-step-export-scaling).

# Why the interference check is replaced

It is fenced on its own (TestKernel_InterferenceBoxTestsGrowLinearly and the V1
fences), and on 32k parts it would add seconds to every build here without saying
anything about the file. The export phase is timed by the sidecar's own _lap, so the
check's time was never in it.
"""

import base64
import importlib.util
import json
import math
import re
import sys


def load(path):
    spec = importlib.util.spec_from_file_location("sidecar", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def turn_x(degrees):
    c, s = math.cos(math.radians(degrees)), math.sin(math.radians(degrees))
    return [1, 0, 0, 0, c, -s, 0, s, c]


def fixture(n):
    """n occurrences of two definitions, one flat assembly: studs, and pins turned
    onto +Y (a shared shape with an orientation of its own, so a lost location
    shows in the bounds). Each occurrence has its own name, so a name lost or moved
    changes the file."""
    identity = [1, 0, 0, 0, 1, 0, 0, 0, 1]
    solids = []
    for i in range(n):
        x, z = 12.0 * (i % 256), 24.0 * (i // 256)
        if i % 2 == 0:
            solids.append({"id": "stud-%d" % i, "label": "Stud %d" % i, "shape": "box",
                           "dims": {"width": 4.0, "height": 6.0, "depth": 8.0},
                           "matrix": identity, "position": [x, 0.0, z]})
        else:
            solids.append({"id": "pin-%d" % i, "label": "Pin %d" % i, "shape": "cylinder",
                           "dims": {"radius": 1.5, "height": 10.0},
                           "matrix": turn_x(90), "position": [x, 20.0, z]})
    return {"solids": solids, "operations": [], "format": "step"}


NAUO_ID = re.compile(rb"NEXT_ASSEMBLY_USAGE_OCCURRENCE\('\d+'")
WRAP = re.compile(rb"\r?\n[ ]+")


def body(step):
    """The file below its header, with long lines joined and each instance's id
    string blanked.

    The header carries the time it was written. ‼️ The instance ids are a counter
    OCCT keeps for the life of the PROCESS, not the file: measured 2026-09-15, three
    exports of the same 32,768 occurrences in one process wrote their first
    NEXT_ASSEMBLY_USAGE_OCCURRENCE as '1', '32769' and '65537'. The sidecar is a
    long-lived process, so this was already true of every export before this change.

    ‼️ Blanking the ids is not enough on its own: the writer wraps a line past its
    width, and a five-digit id wraps where a one-digit one did not, so the same
    instance is split across lines differently (135,200 of 430,238 lines "differed"
    at 32,768 occurrences). Continuation lines start with spaces; joined, three
    exports from one process (walk off, off, on) matched in all 360,968 lines."""
    marker = step.find(b"ENDSEC;")
    text = step[marker:] if marker >= 0 else step
    return NAUO_ID.sub(b"NEXT_ASSEMBLY_USAGE_OCCURRENCE('#'", WRAP.sub(b"", text))


def main():
    sidecar = load(sys.argv[1])
    small, large, readback, out = int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4]), sys.argv[5]
    sidecar._interferences = lambda solids, ids, labels, placed=None: (
        [], False, 0, {"pairs": 0, "booleans": 0, "reused": 0})
    # A sidecar from before the flag always walked, and says so rather than failing,
    # so the same script measures the path the fix replaced.
    shipped = getattr(sidecar, "_STEP_WRITE_PROPS", True)

    def export(n, props):
        sidecar._STEP_WRITE_PROPS = props
        try:
            reply = sidecar._build(fixture(n))
        finally:
            sidecar._STEP_WRITE_PROPS = shipped
        if not reply.get("ok") or reply.get("skipped") or not reply.get("step"):
            sys.exit("%d occurrences did not export: %s" % (n, reply.get("error") or reply.get("skipped")))
        return reply, base64.b64decode(reply["step"])

    # The faster of two runs a size: a shared machine slows a run, never speeds one.
    runs = {"small_export_s": [], "large_export_s": [], "small_transfer_s": [], "large_transfer_s": []}
    reply, step = None, None
    for _ in range(2):
        phases = export(small, shipped)[0]["phases"]
        runs["small_export_s"].append(phases["export"])
        runs["small_transfer_s"].append(phases.get("export_transfer", 0.0))
        reply, step = export(large, shipped)
        runs["large_export_s"].append(reply["phases"]["export"])
        runs["large_transfer_s"].append(reply["phases"].get("export_transfer", 0.0))
    walked, walked_step = export(large, True)
    same_file = body(step) == body(walked_step)
    instances, breps = step.count(b"NEXT_ASSEMBLY_USAGE_OCCURRENCE("), step.count(b"MANIFOLD_SOLID_BREP(")
    large_bytes, large_parts = len(step), reply["parts"]
    del walked_step, step

    read_reply, read_step = export(readback, shipped)
    with open(out, "wb") as fh:
        fh.write(read_step)
    json.dump({
        "small": small, "large": large, "parts": large_parts,
        "readback": readback, "readback_parts": read_reply["parts"], "readback_bounds": read_reply["bounds"],
        "small_export_s": min(runs["small_export_s"]), "large_export_s": min(runs["large_export_s"]),
        "small_transfer_s": min(runs["small_transfer_s"]), "large_transfer_s": min(runs["large_transfer_s"]),
        "walked_export_s": walked["phases"]["export"],
        "walked_transfer_s": walked["phases"].get("export_transfer", 0.0),
        "props_mode_shipped": bool(shipped),
        "bytes": large_bytes, "same_file": same_file, "instances": instances, "breps": breps,
    }, sys.stdout)


if __name__ == "__main__":
    main()
