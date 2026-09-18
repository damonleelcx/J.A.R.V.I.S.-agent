import math


def seg(c, s):
    r = (c * c / 4 + s * s) / (2 * s)
    th = 2 * math.asin(c / 2 / r)
    return r * r / 2 * (th - math.sin(th)), r, th


S, r, th = seg(40, 8)
print("lens", 2 * S * 10, "kernel 4400.234526059954")
S, r, th = seg(40, 5)
P = 40 * (r * math.sin(th / 2) / (th / 2) - r + 5)
V = 30 * (800 + P / 6 + S / 3)
k = 26013.83050214928
print("loft", V, (k - V) / V, "naive", 30 * (800 + S / 2), (30 * (800 + S / 2) - k) / k, "dropped", 24000)
