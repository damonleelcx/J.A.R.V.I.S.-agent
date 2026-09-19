"""A mesh over the triangle budget is really coarsened, for mesh_budget_kernel_test.go.

Usage: mesh_budget.py <sidecar.py>

Ten cylinders of different radii (ten shapes, so nothing is shared) are 500
triangles each at the kernel's angular limit whatever the deflection. The budget
is lowered to 1,500 so the search has to coarsen: before looks-designed stage A5
it coarsened only the deflection, and build123d's mesh() kept the first, finer
triangulation anyway, so every try returned the same 5,000 triangles. Prints one
JSON object: the reply's mesh fields, and the triangles at the budget as shipped.
"""

import importlib.util
import json
import sys


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def request():
    identity = [1, 0, 0, 0, 1, 0, 0, 0, 1]
    return {"format": "mesh", "skip_interferences": True, "solids": [
        {"id": "c%d" % i, "label": "c%d" % i, "shape": "cylinder",
         "dims": {"radius": 10.0 + i, "height": 40.0},
         "position": [60.0 * i, 0.0, 0.0], "matrix": identity}
        for i in range(10)]}


def main():
    sidecar = load("sidecar", sys.argv[1])
    full = sidecar._build(request())
    sidecar._MESH_BUDGET = 1500
    tight = sidecar._build(request())
    keys = ("mesh_triangles", "mesh_deflection", "mesh_angular", "mesh_simplified", "mesh_error")
    print(json.dumps({"full": {k: full.get(k) for k in keys},
                      "tight": {k: tight.get(k) for k in keys}}))


if __name__ == "__main__":
    main()
