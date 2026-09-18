"""Is a quarter-turned or translated copy's OCCT box its definition's box moved, to the bit?
(docs/spikes/2026-09-17-kernel-last-walls, item 1)

For boxes, cylinders, cones, spheres and an extrusion at all 24 proper quarter-turn
rotations (entries exactly 0 or +-1, identity included) and 30 positions each (the first
at the origin, the rest random within +-5,000 mm), compares _box_of on the placed solid —
what part properties report today — with the definition's _box_of moved by the placement
(_moved_box_direct, exact for a signed permutation). Prints how many agree to the bit.

Usage: <cad venv python> quarter_turn_boxes.py <sidecar.py>
"""
import importlib.util, itertools, json, math, random, sys

spec = importlib.util.spec_from_file_location("sidecar", sys.argv[1])
sc = importlib.util.module_from_spec(spec); spec.loader.exec_module(sc)

def perms():
    out = []
    for p in itertools.permutations(range(3)):
        for signs in itertools.product((1.0, -1.0), repeat=3):
            m = [0.0] * 9
            for r in range(3):
                m[3 * r + p[r]] = signs[r]
            # proper rotations only (det +1)
            det = (m[0] * (m[4] * m[8] - m[5] * m[7]) - m[1] * (m[3] * m[8] - m[5] * m[6]) + m[2] * (m[3] * m[7] - m[4] * m[6]))
            if det > 0:
                out.append(m)
    return out

ELL = {"start": [0.3, 0.1, 0.0], "edges": [
    {"to": [20.7, 0.1, 0.0]}, {"to": [20.7, 5.3, 0.0]}, {"to": [5.1, 5.3, 0.0]},
    {"to": [5.1, 15.9, 0.0]}, {"to": [0.3, 15.9, 0.0]}, {"to": [0.3, 0.1, 0.0]}]}
kinds = [
    {"shape": "box", "dims": {"width": 12.3, "height": 5.1, "depth": 30.7}},
    {"shape": "cylinder", "dims": {"radius": 2.4, "height": 14.0}},
    {"shape": "cylinder", "dims": {"radius": 3.3, "height": 17.1, "radius_top": 1.9}},
    {"shape": "sphere", "dims": {"radius": 7.3}},
    {"shape": "extrusion", "dims": {"depth": 5.3}, "outline": ELL},
]
rnd = random.Random(7)
same = diff = 0
worst = 0.0
examples = []
for kind in kinds:
    for mi, m in enumerate(perms()):
        for trial in range(30):
            pos = [rnd.uniform(-5000, 5000) for _ in range(3)] if trial else [0.0, 0.0, 0.0]
            s = dict(kind, id="x", label="x", matrix=m, position=pos)
            try:
                shape = sc._shape(s)
            except Exception as e:
                print("skip", kind["shape"], e); break
            loc = sc._placement(s, {})
            placed = sc._located(shape, loc, {})
            direct = sc._box_of(placed)
            dbox = sc._box_of(shape)
            e = sc._entries(loc.wrapped)
            moved = sc._moved_box_direct(dbox, e)
            if moved == direct:
                same += 1
            else:
                diff += 1
                d = max(abs(a - b) for a, b in zip(moved[0] + moved[1], direct[0] + direct[1]))
                worst = max(worst, d)
                if len(examples) < 6:
                    examples.append((kind["shape"], m, pos, moved, direct))
print("same", same, "differ", diff, "worst", worst)
for x in examples:
    print(x)
