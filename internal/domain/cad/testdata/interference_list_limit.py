"""The interference list with and without its bound, for
interference_list_kernel_test.go.

Usage: interference_list_limit.py <sidecar.py>

Builds one fixture through _build with _INTERFERENCE_LIST_LIMIT far above its
clash count, exactly at it, one below it, and at 7, and prints the four replies'
interference fields as one JSON object. Pins stand at different depths in their
plates, so fractions differ, and some stand at the same depth, so fractions tie.
"""

import importlib.util
import json
import sys


def load(path):
    spec = importlib.util.spec_from_file_location("sidecar", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def fixture():
    plate = {"width": 20.0, "height": 4.0, "depth": 20.0}
    pin = {"radius": 2.0, "height": 10.0}
    ident = [1, 0, 0, 0, 1, 0, 0, 0, 1]
    solids = []
    # 30 plates, a pin in each at one of 12 depths: ties at every depth.
    for n in range(30):
        x = 100.0 * n
        solids.append({"id": "plate-%02d" % n, "label": "Plate", "shape": "box", "dims": plate,
                       "matrix": ident, "position": [x, 0.0, 0.0]})
        solids.append({"id": "pin-%02d" % n, "label": "Pin", "shape": "cylinder", "dims": pin,
                       "matrix": ident, "position": [x, 3.0 + 0.25 * (n % 12), 0.0]})
    # Nine blocks in a row, each over its neighbours: many clashes of other sizes.
    for n in range(9):
        solids.append({"id": "block-%d" % n, "label": "Block", "shape": "box",
                       "dims": {"width": 10.0, "height": 10.0, "depth": 10.0},
                       "matrix": ident, "position": [5000.0 + 3.0 * n, 0.0, 0.0]})
    return {"solids": solids, "operations": [], "format": ""}


def run(sidecar, limit):
    saved = sidecar._INTERFERENCE_LIST_LIMIT
    sidecar._INTERFERENCE_LIST_LIMIT = limit
    try:
        reply = sidecar._build(fixture())
    finally:
        sidecar._INTERFERENCE_LIST_LIMIT = saved
    if not reply.get("ok") or reply.get("skipped"):
        raise SystemExit(json.dumps({"error": "the fixture did not build: %s %s" % (reply.get("error"), reply.get("skipped"))}))
    return {"limit": limit, "interferences": reply["interferences"], "found": reply.get("interferences_found"),
            "summarized": reply.get("interferences_summarized"), "truncated": reply["interferences_truncated"],
            "pairs": reply["interference_pairs"], "reply_bytes": len(json.dumps(reply))}


def main():
    sidecar = load(sys.argv[1])
    whole = run(sidecar, 10 ** 9)
    n = len(whole["interferences"])
    json.dump({"shipped_limit": sidecar._INTERFERENCE_LIST_LIMIT, "whole": whole, "at": run(sidecar, n),
               "below": run(sidecar, n - 1), "seven": run(sidecar, 7)}, sys.stdout)


if __name__ == "__main__":
    main()
