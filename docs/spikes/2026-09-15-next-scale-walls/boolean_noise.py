"""Usage: <cad venv python> boolean_noise.py <sidecar.py>

How much OCCT's common-volume of one clash moves when nothing about the clash changes.

For a shaft (cylinder r10 h400) and a leaning pin (r2 h40, turned 35 deg about z in the shaft's
frame, 4 mm off axis), and a cross pin at y=199:
  (a) the same two placed solids, the boolean repeated 5 times;
  (b) the same relative pose, the pair moved together along the shaft's axis (5 positions);
  (c) the same relative pose, the whole pair moved by an unrelated rigid motion (5 motions);
for an unturned frame and for the fixture's randomly turned frame (seed 1 draw).
"""
import importlib.util
import math
import random
import sys

spec = importlib.util.spec_from_file_location("sidecar", sys.argv[1])
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)


def rot(ax, ay, az):
    ax, ay, az = math.radians(ax), math.radians(ay), math.radians(az)
    cx, sx, cy, sy, cz, sz = math.cos(ax), math.sin(ax), math.cos(ay), math.sin(ay), math.cos(az), math.sin(az)
    rx = [[1, 0, 0], [0, cx, -sx], [0, sx, cx]]
    ry = [[cy, 0, sy], [0, 1, 0], [-sy, 0, cy]]
    rz = [[cz, -sz, 0], [sz, cz, 0], [0, 0, 1]]
    return mul(rz, mul(ry, rx))


def mul(a, b):
    return [[sum(a[i][k] * b[k][j] for k in range(3)) for j in range(3)] for i in range(3)]


def apply(r, v):
    return [sum(r[i][k] * v[k] for k in range(3)) for i in range(3)]


def flat(r):
    return [r[i][j] for i in range(3) for j in range(3)]


shaft_spec = {"shape": "cylinder", "dims": {"radius": 10.0, "height": 400.0}}
pins = {"lean": ({"shape": "cylinder", "dims": {"radius": 2.0, "height": 40.0}}, [4.0, 0.0, 0.0], rot(0, 0, 35)),
        "cross": ({"shape": "cylinder", "dims": {"radius": 2.0, "height": 40.0}}, [0.0, 0.0, 0.0], rot(90, 0, 0))}
shaft = sc._shape(shaft_spec)


def placed(spec, r, origin):
    loc = sc._placement({"matrix": flat(r), "position": origin})
    return loc * sc._shape(spec)


def common(frame_r, frame_o, pin, along):
    spec, local, turn = pins[pin]
    a = placed(shaft_spec, frame_r, frame_o)
    at = [o + d for o, d in zip(frame_o, apply(frame_r, [local[0], local[1] + along, local[2]]))]
    b = placed(spec, mul(frame_r, turn), at)
    return float((a & b).volume)


rng = random.Random(1)
turned = rot(rng.uniform(0, 90), rng.uniform(0, 90), 0)
for name, r in (("unturned", rot(0, 0, 0)), ("turned", turned)):
    for pin, base in (("lean", 37.0), ("cross", 199.0)):
        o = [0.0, 0.0, 9000.0]
        rep = [common(r, o, pin, base) for _ in range(5)]
        slide = [common(r, [o[0], o[1], o[2]], pin, base + dy) for dy in ((0.0, -60.0, -120.0, 13.37, -90.5) if pin == "lean" else (0.0,) * 1)]
        moved = []
        for k in range(5):
            m = rot(rng.uniform(-180, 180), rng.uniform(-180, 180), rng.uniform(-180, 180))
            moved.append(common(mul(m, r), [rng.uniform(-5000, 5000) for _ in range(3)], pin, base))
        spread = lambda xs: (max(xs) - min(xs)) / max(xs)
        print("%-8s %-5s repeat %s (spread %.2g)" % (name, pin, ["%.9g" % x for x in rep], spread(rep)))
        if pin == "lean":
            print("%-8s %-5s along  %s (spread %.2g)" % (name, pin, ["%.9g" % x for x in slide], spread(slide)))
        print("%-8s %-5s moved  %s (spread %.2g)" % (name, pin, ["%.9g" % x for x in moved], spread(moved)))
