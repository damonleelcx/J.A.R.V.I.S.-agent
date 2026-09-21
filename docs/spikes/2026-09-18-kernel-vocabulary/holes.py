"""Boolean cost of perforating a panel: N holes, sequential vs one multi-tool cut.

Interleaved: for each round, each (N, method) is run once, in alternating order.
"""
import sys, time, math, os, json
from build123d import *

try:
    import psutil
except Exception:
    psutil = None


def grid(n, W, D, r):
    cols = int(math.ceil(math.sqrt(n * W / D)))
    rows = int(math.ceil(n / cols))
    out = []
    for i in range(n):
        c, rr = i % cols, i // cols
        x = -W / 2 + (c + 0.5) * W / cols
        z = -D / 2 + (rr + 0.5) * D / rows
        out.append(Pos(x, 0, z) * (Plane.XZ * Cylinder(r, 20)))
    return out


def run(n, method):
    W, H, D = 1000.0, 5.0, 600.0
    r = min(3.0, 0.3 * min(W / math.sqrt(n * W / D), D / math.sqrt(n * D / W)))
    panel = Box(W, H, D)
    tools = grid(n, W, D, r)
    t0 = time.perf_counter()
    if method == "sequential":
        out = panel
        for t in tools:
            out = out - t
    else:
        out = panel.cut(*tools)
    dt = time.perf_counter() - t0
    want = W * H * D - n * math.pi * r * r * H
    vol = out.volume
    return dt, vol, want, r


if __name__ == "__main__":
    ns = [int(a) for a in sys.argv[1].split(",")]
    methods = sys.argv[2].split(",")
    rounds = int(sys.argv[3])
    for k in range(rounds):
        order = [(n, m) for n in ns for m in methods]
        if k % 2:
            order.reverse()
        for n, m in order:
            load = psutil.cpu_percent(interval=0.5) if psutil else -1
            dt, vol, want, r = run(n, m)
            print(json.dumps({"round": k, "n": n, "method": m, "seconds": round(dt, 3),
                              "volume": round(vol, 1), "want": round(want, 1), "r": r,
                              "cpu_percent_before": load}), flush=True)
