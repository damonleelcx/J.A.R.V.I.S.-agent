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


def long_parts(b):
    """Long parts among many small ones (added 2026-09-15, large-box index).

    Studs are most of the parts, so the grid's cell is a stud, and everything else
    spans many cells: rails the envelope long along each axis, rails turned in a
    plane (their boxes large on two axes), panels long on two axes and thin on the
    third, a stringer with rivets along it and one over its end, a floor and a
    slab face to face, and one box around everything.
    """
    rng = random.Random(915)
    solids = []
    for _ in range(120):
        solids.append(b.Pos(rng.uniform(0, 600), rng.uniform(0, 120), rng.uniform(0, 120))
                      * b.Box(rng.uniform(4, 10), rng.uniform(4, 10), rng.uniform(4, 10)))
    for _ in range(4):
        solids.append(b.Pos(300, rng.uniform(0, 120), rng.uniform(0, 120)) * b.Box(640, 6, 6))
        solids.append(b.Pos(rng.uniform(0, 600), 60, rng.uniform(0, 120)) * b.Box(6, 160, 6))
        solids.append(b.Pos(rng.uniform(0, 600), rng.uniform(0, 120), 60) * b.Box(6, 6, 160))
    for _ in range(3):
        solids.append(b.Pos(rng.uniform(100, 500), rng.uniform(0, 120), 60)
                      * b.Rot(0, 0, rng.uniform(10, 80)) * b.Box(300, 5, 5))
    for _ in range(4):
        solids.append(b.Pos(rng.uniform(0, 600), rng.uniform(0, 120), rng.uniform(0, 120))
                      * b.Box(rng.uniform(40, 400), 2, rng.uniform(40, 120)))
    solids.append(b.Pos(300, 250, 0) * b.Box(500, 20, 20))
    for x in range(60, 550, 25):
        solids.append(b.Pos(x, 250, 0) * b.Cylinder(2.4, 30))
    solids.append(b.Pos(550, 250, 0) * b.Cylinder(2.4, 30))
    # Face to face: large boxes that touch and share nothing.
    solids.append(b.Pos(300, -20, 60) * b.Box(700, 4, 200))
    solids.append(b.Pos(300, -32, 60) * b.Box(700, 20, 200))
    solids.append(b.Pos(300, 100, 60) * b.Box(900, 400, 300))
    solids.append(Unmeasurable())
    rng.shuffle(solids)
    return solids


def anisotropic_boxes(seed, n):
    """Boxes only, no solids: sizes spread over four decades on each axis
    independently, flat boxes on cell boundaries, absent boxes and one box around
    everything — so a pair can meet in every combination of levels."""
    rng = random.Random(seed)
    out = []
    for _ in range(n):
        lo = [rng.uniform(-300, 300) for _ in range(3)]
        out.append((tuple(lo), tuple(lo[a] + 10 ** rng.uniform(-1, 3) for a in range(3))))
    for k in range(40):
        x = float(k * 7)
        out.append(((x, 0.0, 0.0), (x + 7.0, 7.0, 7.0)))
        out.append(((x, 3.0, 3.0), (x, 4.0, 4.0)))
        out.append(None)
    out.append(((-1e4, -1e4, -1e4), (1e4, 1e4, 1e4)))
    rng.shuffle(out)
    return out


def candidates(sidecar, boxes):
    """_candidate_pairs against every pair of boxes: the same pairs, in the same
    order, each once."""
    got, tests = sidecar._candidate_pairs(boxes)
    ref = [(i, j) for i in range(len(boxes)) for j in range(i + 1, len(boxes))
           if not sidecar._boxes_miss(boxes[i], boxes[j])]
    n = sum(1 for box in boxes if box is not None)
    return {"pairs": len(got), "every_pair_pairs": len(ref), "match": got == ref,
            "duplicates": len(got) - len(set(got)), "box_tests": tests, "every_pair": n * (n - 1) // 2}


def main():
    sidecar = load(sys.argv[1])
    import build123d

    fixture = sys.argv[2]
    synthetic = None
    if fixture == "scatter":
        solids = scatter(build123d)
    elif fixture == "dense":
        solids = dense(build123d)
        sidecar._INTERFERENCE_PAIR_BUDGET = 7
    elif fixture == "long":
        solids = long_parts(build123d)
        synthetic = [candidates(sidecar, anisotropic_boxes(seed, 1500)) for seed in (1, 2, 3)]
    else:
        raise SystemExit("unknown fixture: %s" % fixture)
    ids = ["p%d" % n for n in range(len(solids))]
    labels = ["Part %d" % n for n in range(len(solids))]

    boxes = sidecar._boxes(solids)
    _, box_tests = sidecar._candidate_pairs(boxes)
    found, truncated = sidecar._interferences(solids, ids, labels)[:2]
    ref_found, ref_truncated = every_pair(sidecar, solids, ids, labels)
    json.dump({"parts": len(solids), "budget": sidecar._INTERFERENCE_PAIR_BUDGET,
               "box_tests": box_tests, "every_pair": len(solids) * (len(solids) - 1) // 2,
               "candidates": candidates(sidecar, boxes), "synthetic": synthetic,
               "sweep": {"found": found, "truncated": truncated},
               "all_pairs": {"found": ref_found, "truncated": ref_truncated}}, sys.stdout)


if __name__ == "__main__":
    main()
