"""The sidecar's interference check and the every-pair loop it replaced, on the
same solids, for interference_broad_phase_kernel_test.go.

Usage: interference_all_pairs.py <sidecar.py> <scatter|dense>

Prints one JSON object. The every-pair loop below is the check as it stood
before Phase 4 stage K2b, kept here as the reference the sweep must agree with.
"""

import importlib.util
import json
import random
import sys


def load(path):
    spec = importlib.util.spec_from_file_location("sidecar", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def every_pair(sidecar, solids, ids, labels):
    """_interferences as it was before the sweep: every pair's boxes compared."""
    boxes = sidecar._boxes(solids)
    volumes = []
    for s in solids:
        try:
            volumes.append(float(getattr(s, "volume", 0.0)))
        except Exception:
            volumes.append(0.0)

    found, tested, truncated = [], 0, False
    for i in range(len(solids)):
        for j in range(i + 1, len(solids)):
            if sidecar._boxes_miss(boxes[i], boxes[j]):
                continue
            if tested >= sidecar._INTERFERENCE_PAIR_BUDGET:
                truncated = True
                break
            tested += 1
            try:
                shared = float(getattr(solids[i] & solids[j], "volume", 0.0))
            except Exception:
                continue
            if volumes[i] <= 0 or volumes[j] <= 0 or shared <= 0:
                continue
            smaller = min(volumes[i], volumes[j])
            if shared < sidecar._INTERFERENCE_MIN_VOLUME or shared / smaller < sidecar._INTERFERENCE_MIN_FRACTION:
                continue
            lo, hi = (i, j) if volumes[i] <= volumes[j] else (j, i)
            found.append({"a": ids[lo], "b": ids[hi],
                          "a_label": labels[lo], "b_label": labels[hi],
                          "volume": shared, "fraction": shared / smaller})
        if truncated:
            break

    found.sort(key=lambda f: f["fraction"], reverse=True)
    return found, truncated


class Unmeasurable:
    """A solid whose bounds cannot be read. The broad phase leaves it out."""
    volume = 1000.0

    def bounding_box(self):
        raise RuntimeError("no bounds")


def scatter(b):
    """Parts along a 400 mm envelope, so x is the axis they spread along."""
    rng = random.Random(2026)
    solids = []
    for _ in range(90):
        w, h, d = rng.uniform(5, 40), rng.uniform(5, 30), rng.uniform(5, 30)
        solids.append(b.Pos(rng.uniform(0, 400), rng.uniform(0, 60), rng.uniform(0, 60)) * b.Box(w, h, d))
    for _ in range(20):
        solids.append(b.Pos(rng.uniform(0, 400), rng.uniform(0, 60), rng.uniform(0, 60))
                      * b.Rot(rng.uniform(0, 90), rng.uniform(0, 90), 0)
                      * b.Cylinder(rng.uniform(2, 8), rng.uniform(10, 50)))
    # A rail most of the envelope long, open while most parts start and end.
    solids.append(b.Pos(200, 30, 30) * b.Box(360, 8, 8))
    # Face to face: the boxes touch and share no material.
    solids.append(b.Pos(500, 0, 0) * b.Box(10, 10, 10))
    solids.append(b.Pos(510, 0, 0) * b.Box(10, 10, 10))
    # Two identical boxes, and a box inside another.
    solids.append(b.Pos(-60, 0, 0) * b.Box(12, 12, 12))
    solids.append(b.Pos(-60, 0, 0) * b.Box(12, 12, 12))
    solids.append(b.Pos(-100, 0, 0) * b.Box(30, 30, 30))
    solids.append(b.Pos(-100, 0, 0) * b.Box(6, 6, 6))
    solids.append(Unmeasurable())
    rng.shuffle(solids)
    return solids


def dense(b):
    """Many parts in one small cube: far more overlapping pairs than the budget."""
    rng = random.Random(914)
    return [b.Pos(rng.uniform(0, 60), rng.uniform(0, 60), rng.uniform(0, 60))
            * b.Box(rng.uniform(20, 40), rng.uniform(20, 40), rng.uniform(20, 40))
            for _ in range(40)]


def main():
    sidecar = load(sys.argv[1])
    import build123d

    fixture = sys.argv[2]
    if fixture == "scatter":
        solids = scatter(build123d)
    elif fixture == "dense":
        solids = dense(build123d)
        sidecar._INTERFERENCE_PAIR_BUDGET = 7
    else:
        raise SystemExit("unknown fixture: %s" % fixture)
    ids = ["p%d" % n for n in range(len(solids))]
    labels = ["Part %d" % n for n in range(len(solids))]

    _, box_tests = sidecar._candidate_pairs(sidecar._boxes(solids))
    found, truncated, _ = sidecar._interferences(solids, ids, labels)
    ref_found, ref_truncated = every_pair(sidecar, solids, ids, labels)
    json.dump({"parts": len(solids), "budget": sidecar._INTERFERENCE_PAIR_BUDGET,
               "box_tests": box_tests, "every_pair": len(solids) * (len(solids) - 1) // 2,
               "sweep": {"found": found, "truncated": truncated},
               "all_pairs": {"found": ref_found, "truncated": ref_truncated}}, sys.stdout)


if __name__ == "__main__":
    main()
